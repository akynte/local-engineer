package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/akynte/local-engineer/internal/version"
)

// Repository is one repository inside a workspace. A multi-repository system
// is a single workspace with several of these (§2.1).
type Repository struct {
	ID            string `yaml:"id" json:"id"`
	Name          string `yaml:"name" json:"name"`
	Path          string `yaml:"path" json:"path"` // relative to the workspace root
	Remote        string `yaml:"remote,omitempty" json:"remote,omitempty"`
	DefaultBranch string `yaml:"default_branch" json:"default_branch"`
}

// Manifest is the on-disk `.le/workspace.yaml`. It pins the id so that moving
// the directory keeps the identity, and records what the workspace contains.
type Manifest struct {
	SchemeVersion int       `yaml:"scheme_version" json:"scheme_version"`
	ID            ID        `yaml:"id" json:"id"`
	Name          string    `yaml:"name" json:"name"`
	CreatedAt     time.Time `yaml:"created_at" json:"created_at"`
	// DerivedFrom records the inputs the id was computed from, so that `le
	// workspace adopt` can explain what changed after a move.
	DerivedFrom  Derivation   `yaml:"derived_from" json:"derived_from"`
	Repositories []Repository `yaml:"repositories" json:"repositories"`
}

// Derivation is the recorded input triple of DeriveID.
type Derivation struct {
	CanonicalRoot string `yaml:"canonical_root" json:"canonical_root"`
	GitRemote     string `yaml:"git_remote,omitempty" json:"git_remote,omitempty"`
	Name          string `yaml:"name" json:"name"`
}

// Workspace is an opened workspace: a manifest bound to a concrete root path.
type Workspace struct {
	Root     string
	Manifest Manifest
}

func (w *Workspace) ID() ID       { return w.Manifest.ID }
func (w *Workspace) Name() string { return w.Manifest.Name }

// MarkerPath is the absolute path of the pin file.
func MarkerPath(root string) string { return filepath.Join(root, MarkerDir, MarkerFile) }

// Repository returns the repository with the given id.
func (w *Workspace) Repository(id string) (Repository, bool) {
	for _, r := range w.Manifest.Repositories {
		if r.ID == id {
			return r, true
		}
	}
	return Repository{}, false
}

// RepositoryPath resolves a repository's absolute path inside the workspace.
func (w *Workspace) RepositoryPath(id string) (string, error) {
	r, ok := w.Repository(id)
	if !ok {
		return "", fmt.Errorf("workspace: no repository %q in workspace %s", id, w.ID())
	}
	return filepath.Join(w.Root, filepath.FromSlash(r.Path)), nil
}

// InitOptions configures Init.
type InitOptions struct {
	// Name is the user-supplied name component of the identity. Empty means
	// the base name of the root directory.
	Name string
	// Repositories are paths relative to the root. Empty means the root itself
	// is the single repository.
	Repositories []string
	// Force overwrites an existing manifest.
	Force bool
}

// Init creates `.le/workspace.yaml` at root and returns the new workspace.
func Init(root string, opts InitOptions) (*Workspace, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	marker := MarkerPath(canonical)
	if _, err := os.Stat(marker); err == nil && !opts.Force {
		return nil, fmt.Errorf("workspace: %s already exists (use --force to overwrite)", marker)
	}

	name := opts.Name
	if name == "" {
		name = filepath.Base(canonical)
	}
	remote := GitRemote(canonical)
	id := DeriveID(canonical, remote, name)

	repoPaths := opts.Repositories
	if len(repoPaths) == 0 {
		repoPaths = []string{"."}
	}
	sort.Strings(repoPaths)

	m := Manifest{
		SchemeVersion: version.WorkspaceIDScheme,
		ID:            id,
		Name:          name,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		DerivedFrom:   Derivation{CanonicalRoot: canonical, GitRemote: remote, Name: name},
	}
	for _, rel := range repoPaths {
		rel = filepath.ToSlash(filepath.Clean(rel))
		abs := filepath.Join(canonical, filepath.FromSlash(rel))
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("workspace: repository path %q is not a directory", rel)
		}
		rremote := GitRemote(abs)
		m.Repositories = append(m.Repositories, Repository{
			ID:            DeriveRepositoryID(id, rel, rremote),
			Name:          repoDisplayName(canonical, rel),
			Path:          rel,
			Remote:        rremote,
			DefaultBranch: GitDefaultBranch(abs),
		})
	}

	if err := writeManifest(canonical, m); err != nil {
		return nil, err
	}
	return &Workspace{Root: canonical, Manifest: m}, nil
}

func repoDisplayName(root, rel string) string {
	if rel == "." {
		return filepath.Base(root)
	}
	return filepath.Base(rel)
}

// Open loads the workspace whose marker file is at or above start.
func Open(start string) (*Workspace, error) {
	root, err := FindRoot(start)
	if err != nil {
		return nil, err
	}
	return OpenRoot(root)
}

// OpenRoot loads the workspace rooted exactly at root.
func OpenRoot(root string) (*Workspace, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(MarkerPath(canonical))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s", ErrNotAWorkspace, canonical)
		}
		return nil, err
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("workspace: parse %s: %w", MarkerPath(canonical), err)
	}
	if !m.ID.Valid() {
		return nil, fmt.Errorf("workspace: %s holds an invalid id %q", MarkerPath(canonical), m.ID)
	}
	if m.SchemeVersion > version.WorkspaceIDScheme {
		return nil, fmt.Errorf("workspace: %s uses identity scheme v%d, this build understands v%d; upgrade local-engineer",
			MarkerPath(canonical), m.SchemeVersion, version.WorkspaceIDScheme)
	}
	return &Workspace{Root: canonical, Manifest: m}, nil
}

// Moved reports whether the workspace root differs from the path the id was
// derived from. A moved workspace keeps working (the id is pinned) but `le
// workspace adopt` should be run to refresh the recorded derivation.
func (w *Workspace) Moved() bool {
	return w.Manifest.DerivedFrom.CanonicalRoot != "" &&
		w.Manifest.DerivedFrom.CanonicalRoot != w.Root
}

// Adopt re-binds the pinned id to the current location, refreshing the
// recorded derivation and each repository's remote and default branch. The id
// itself never changes: that is the entire point of adoption (§2.1).
func (w *Workspace) Adopt() error {
	w.Manifest.DerivedFrom.CanonicalRoot = w.Root
	w.Manifest.DerivedFrom.GitRemote = GitRemote(w.Root)
	for i := range w.Manifest.Repositories {
		abs := filepath.Join(w.Root, filepath.FromSlash(w.Manifest.Repositories[i].Path))
		w.Manifest.Repositories[i].Remote = GitRemote(abs)
		w.Manifest.Repositories[i].DefaultBranch = GitDefaultBranch(abs)
	}
	return writeManifest(w.Root, w.Manifest)
}

func writeManifest(root string, m Manifest) error {
	dir := filepath.Join(root, MarkerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("# local-engineer workspace identity. Commit this file.\n")
	sb.WriteString("# Moving the directory without it creates a NEW workspace on purpose;\n")
	sb.WriteString("# after a move run `le workspace adopt` to re-bind this id.\n")
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	tmp := filepath.Join(dir, MarkerFile+".tmp")
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, MarkerFile))
}
