// Package index builds the source index, symbol index, graph and chunks for a
// workspace (design v3 §2.4, §3.2).
//
// Analyzers are pluggable: this package owns the filesystem and containment
// layer, which §3.2 lists as "resolved" from the filesystem and git tree, and
// language analyzers contribute the compiler-backed relations behind the
// Analyzer interface. Adding a language analyzer is a documented extension
// point (docs/how-to/add-a-language-analyzer.md).
package index

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// File is one indexed source file handed to analyzers.
type File struct {
	ID           int64
	RepositoryID string
	WorktreeID   string
	Path         string // repository-relative, slash separated
	AbsPath      string
	Lang         string
	ContentHash  string
	Size         int64
}

// Result is what an analyzer contributes for a repository.
type Result struct {
	Nodes []graph.Node
	// Edges reference nodes by FQN because analyzers run before ids exist.
	Edges []PendingEdge
}

// PendingEdge names its endpoints by (kind, fqn) so analyzers never deal in
// database ids.
type PendingEdge struct {
	SrcKind graph.NodeKind
	SrcFQN  string
	DstKind graph.NodeKind
	DstFQN  string
	Kind    graph.EdgeKind
	// Evidence must be stated explicitly: §3.2 records a category for every
	// relationship, and "unknown" is a valid, honest answer.
	Evidence graph.Evidence
	Attrs    string
}

// Analyzer contributes typed nodes and edges for one language or concern.
type Analyzer interface {
	// Name identifies the analyzer and is stored as the edge source, so that a
	// wrong edge can be traced to the code that produced it.
	Name() string
	// Handles reports whether this analyzer wants the file.
	Handles(f File) bool
	// Analyze runs over the files this analyzer accepted.
	Analyze(ctx context.Context, repoRoot string, files []File) (Result, error)
}

// Options configures a run.
type Options struct {
	// MaxFileBytes skips files larger than this; generated bundles and
	// vendored blobs are cost without retrieval value.
	MaxFileBytes int64
	// Excludes are directory names pruned during the walk.
	Excludes []string
	// ChunkLines is the line window used for lexical chunks.
	ChunkLines int
	// Analyzers run after the filesystem layer.
	Analyzers []Analyzer
}

// DefaultOptions are the values used when a field is left zero.
func DefaultOptions() Options {
	return Options{
		MaxFileBytes: 1 << 20,
		Excludes: []string{".git", "node_modules", "vendor", "dist", "build", ".next", ".nuxt",
			"target", "__pycache__", ".venv", ".le", ".idea", ".cache"},
		ChunkLines: 60,
	}
}

// Stats summarises a run.
type Stats struct {
	Files    int           `json:"files"`
	Chunks   int           `json:"chunks"`
	Nodes    int           `json:"nodes"`
	Edges    int           `json:"edges"`
	Skipped  int           `json:"skipped"`
	Duration time.Duration `json:"duration"`
}

// Indexer writes into one workspace's index database.
type Indexer struct {
	st   *store.Store
	g    graph.Graph
	opts Options
}

// New binds an indexer to a workspace store.
func New(s *store.Store, opts Options) *Indexer {
	d := DefaultOptions()
	if opts.MaxFileBytes == 0 {
		opts.MaxFileBytes = d.MaxFileBytes
	}
	if opts.ChunkLines == 0 {
		opts.ChunkLines = d.ChunkLines
	}
	if opts.Excludes == nil {
		opts.Excludes = d.Excludes
	}
	return &Indexer{st: s, g: graph.New(s), opts: opts}
}

// RegisterRepository records a repository in the index database. Every node,
// file and chunk hangs off one of these.
func (ix *Indexer) RegisterRepository(ctx context.Context, r workspace.Repository) error {
	return ix.st.Index().Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO repositories (repository_id, workspace_id, name, rel_path, remote, default_branch)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT (repository_id) DO UPDATE SET
			  name = excluded.name, rel_path = excluded.rel_path,
			  remote = excluded.remote, default_branch = excluded.default_branch`,
			r.ID, ix.st.ID().String(), r.Name, r.Path, r.Remote, r.DefaultBranch)
		return err
	})
}

// Repository indexes one repository rooted at absRoot.
func (ix *Indexer) Repository(ctx context.Context, repositoryID, absRoot string) (Stats, error) {
	start := time.Now()
	var st Stats

	files, skipped, err := ix.walk(ctx, repositoryID, absRoot)
	if err != nil {
		return st, err
	}
	st.Skipped = skipped
	st.Files = len(files)

	if err := ix.writeFiles(ctx, files); err != nil {
		return st, err
	}
	nodes, edges, err := ix.writeFilesystemGraph(ctx, repositoryID, files)
	if err != nil {
		return st, err
	}
	st.Nodes += nodes
	st.Edges += edges

	chunks, err := ix.writeChunks(ctx, files)
	if err != nil {
		return st, err
	}
	st.Chunks = chunks

	for _, a := range ix.opts.Analyzers {
		var accepted []File
		for _, f := range files {
			if a.Handles(f) {
				accepted = append(accepted, f)
			}
		}
		if len(accepted) == 0 {
			continue
		}
		res, err := a.Analyze(ctx, absRoot, accepted)
		if err != nil {
			return st, fmt.Errorf("index: analyzer %s: %w", a.Name(), err)
		}
		n, e, err := ix.writeResult(ctx, repositoryID, a.Name(), res)
		if err != nil {
			return st, err
		}
		st.Nodes += n
		st.Edges += e
	}

	if err := ix.recordIndexKey(ctx, repositoryID, files); err != nil {
		return st, err
	}
	st.Duration = time.Since(start)
	return st, nil
}

func (ix *Indexer) walk(ctx context.Context, repositoryID, absRoot string) ([]File, int, error) {
	excludes := map[string]bool{}
	for _, e := range ix.opts.Excludes {
		excludes[e] = true
	}
	var files []File
	skipped := 0

	err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != absRoot && excludes[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			skipped++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > ix.opts.MaxFileBytes {
			skipped++
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		hash, binary, err := hashFile(path)
		if err != nil {
			return err
		}
		if binary {
			skipped++
			return nil
		}
		files = append(files, File{
			RepositoryID: repositoryID,
			Path:         filepath.ToSlash(rel),
			AbsPath:      path,
			Lang:         langOf(rel),
			ContentHash:  hash,
			Size:         info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, skipped, fmt.Errorf("index: walk %s: %w", absRoot, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, skipped, nil
}

// hashFile returns the sha256 of a file and whether it looks binary. A NUL
// byte in the first 8 KiB is the usual heuristic and matches what git does.
func hashFile(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	h := sha256.New()
	var head [8192]byte
	n, err := io.ReadFull(f, head[:])
	// A file shorter than the probe window is normal, not a failure.
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", false, err
	}
	binary := false
	for _, b := range head[:n] {
		if b == 0 {
			binary = true
			break
		}
	}
	h.Write(head[:n])
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), binary, nil
}

func langOf(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".vue":
		return "vue"
	case ".py":
		return "python"
	case ".sql":
		return "sql"
	case ".proto":
		return "proto"
	case ".tf", ".hcl":
		return "terraform"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	case ".md":
		return "markdown"
	case ".sh", ".bash":
		return "shell"
	case ".dockerfile":
		return "dockerfile"
	}
	switch strings.ToLower(filepath.Base(rel)) {
	case "dockerfile":
		return "dockerfile"
	case "makefile":
		return "make"
	case "go.mod", "go.sum":
		return "gomod"
	}
	return ""
}

func (ix *Indexer) writeFiles(ctx context.Context, files []File) error {
	return ix.st.Index().Tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO files (workspace_id, repository_id, worktree_id, path, lang, size_bytes,
			                   mode, content_hash, indexed_at, index_version)
			VALUES (?,?,?,?,?,?,0,?,?,?)
			ON CONFLICT (repository_id, worktree_id, path) DO UPDATE SET
			  lang = excluded.lang, size_bytes = excluded.size_bytes,
			  content_hash = excluded.content_hash, indexed_at = excluded.indexed_at,
			  index_version = excluded.index_version
			RETURNING file_id`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		now := time.Now().Unix()
		for i := range files {
			f := &files[i]
			if err := stmt.QueryRowContext(ctx, ix.st.ID().String(), f.RepositoryID, f.WorktreeID,
				f.Path, f.Lang, f.Size, f.ContentHash, now, version.IndexerVersion).Scan(&f.ID); err != nil {
				return fmt.Errorf("index: write file %s: %w", f.Path, err)
			}
		}
		return nil
	})
}

// writeFilesystemGraph produces the directory and file nodes and the
// containment edges that §3.2 sources from the filesystem and git tree, with
// evidence "resolved".
func (ix *Indexer) writeFilesystemGraph(ctx context.Context, repositoryID string, files []File) (int, int, error) {
	dirSet := map[string]bool{".": true}
	for _, f := range files {
		for d := filepath.ToSlash(filepath.Dir(f.Path)); d != "." && d != "/"; d = filepath.ToSlash(filepath.Dir(d)) {
			dirSet[d] = true
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	nodes := make([]graph.Node, 0, len(dirs)+len(files))
	for _, d := range dirs {
		name := d
		if d != "." {
			name = filepath.Base(d)
		}
		nodes = append(nodes, graph.Node{
			RepositoryID: repositoryID, Kind: graph.KindDirectory, Name: name, FQN: d,
		})
	}
	for _, f := range files {
		nodes = append(nodes, graph.Node{
			RepositoryID: repositoryID, Kind: graph.KindFile, Name: filepath.Base(f.Path),
			FQN: f.Path, FileID: f.ID, ContentHash: f.ContentHash,
		})
	}
	ids, err := ix.g.UpsertNodes(ctx, nodes)
	if err != nil {
		return 0, 0, err
	}
	byFQN := map[string]int64{}
	for i, n := range nodes {
		byFQN[string(n.Kind)+"\x00"+n.FQN] = ids[i]
	}

	var edges []graph.Edge
	add := func(parentDir, childKind, childFQN string) {
		src, ok := byFQN[string(graph.KindDirectory)+"\x00"+parentDir]
		dst, ok2 := byFQN[childKind+"\x00"+childFQN]
		if !ok || !ok2 {
			return
		}
		edges = append(edges, graph.Edge{Src: src, Dst: dst, Kind: graph.EdgeContains,
			Evidence: graph.Resolved, Source: "filesystem"})
	}
	for _, d := range dirs {
		if d == "." {
			continue
		}
		add(filepath.ToSlash(filepath.Dir(d)), string(graph.KindDirectory), d)
	}
	for _, f := range files {
		add(filepath.ToSlash(filepath.Dir(f.Path)), string(graph.KindFile), f.Path)
	}
	if err := ix.g.UpsertEdges(ctx, edges); err != nil {
		return 0, 0, err
	}
	return len(nodes), len(edges), nil
}

func (ix *Indexer) writeChunks(ctx context.Context, files []File) (int, error) {
	total := 0
	err := ix.st.Index().Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks`); err != nil {
			return err
		}
		insChunk, err := tx.PrepareContext(ctx, `
			INSERT INTO chunks (workspace_id, repository_id, file_id, start_line, end_line,
			                    content_hash, token_estimate, index_version)
			VALUES (?,?,?,?,?,?,?,?) RETURNING chunk_id`)
		if err != nil {
			return err
		}
		defer insChunk.Close()
		insFTS, err := tx.PrepareContext(ctx, `INSERT INTO chunks_fts (rowid, body, path, symbol) VALUES (?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insFTS.Close()

		for _, f := range files {
			windows, err := chunkFile(f.AbsPath, ix.opts.ChunkLines)
			if err != nil {
				return err
			}
			for _, w := range windows {
				sum := sha256.Sum256([]byte(w.body))
				var chunkID int64
				if err := insChunk.QueryRowContext(ctx, ix.st.ID().String(), f.RepositoryID, f.ID,
					w.start, w.end, hex.EncodeToString(sum[:]), len(w.body)/4+1, version.IndexerVersion).Scan(&chunkID); err != nil {
					return err
				}
				if _, err := insFTS.ExecContext(ctx, chunkID, w.body, f.Path, filepath.Base(f.Path)); err != nil {
					return err
				}
				total++
			}
		}
		return nil
	})
	return total, err
}

type window struct {
	start, end int
	body       string
}

func chunkFile(path string, lines int) ([]window, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []window
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var b strings.Builder
	start, n := 1, 0
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
		n++
		if n == lines {
			out = append(out, window{start: start, end: start + n - 1, body: b.String()})
			start += n
			n = 0
			b.Reset()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("index: read %s: %w", path, err)
	}
	if n > 0 {
		out = append(out, window{start: start, end: start + n - 1, body: b.String()})
	}
	return out, nil
}

func (ix *Indexer) writeResult(ctx context.Context, repositoryID, source string, res Result) (int, int, error) {
	for i := range res.Nodes {
		res.Nodes[i].RepositoryID = repositoryID
	}
	if _, err := ix.g.UpsertNodes(ctx, res.Nodes); err != nil {
		return 0, 0, err
	}
	var edges []graph.Edge
	for _, pe := range res.Edges {
		src, err := ix.g.NodeByFQN(ctx, repositoryID, pe.SrcKind, pe.SrcFQN)
		if err != nil {
			continue // an edge whose endpoint was not indexed is "not discovered", not an error
		}
		dst, err := ix.g.NodeByFQN(ctx, repositoryID, pe.DstKind, pe.DstFQN)
		if err != nil {
			continue
		}
		ev := pe.Evidence
		if ev == "" {
			ev = graph.Unknown
		}
		edges = append(edges, graph.Edge{Src: src.ID, Dst: dst.ID, Kind: pe.Kind,
			Evidence: ev, Source: source, Attrs: pe.Attrs})
	}
	if err := ix.g.UpsertEdges(ctx, edges); err != nil {
		return 0, 0, err
	}
	return len(res.Nodes), len(edges), nil
}

// recordIndexKey stores the content manifest for the repository so that §3.4's
// dirty-marking has a baseline to compare against.
func (ix *Indexer) recordIndexKey(ctx context.Context, repositoryID string, files []File) error {
	pairs := make(map[string]string, len(files))
	lock := map[string]string{}
	for _, f := range files {
		pairs[f.Path] = f.ContentHash
		switch filepath.Base(f.Path) {
		case "go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "Cargo.lock", "poetry.lock":
			lock[f.Path] = f.ContentHash
		}
	}
	return ix.st.Index().Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO index_keys (scope, scope_id, manifest_hash, lockfile_hash, toolchain,
			                        build_mode, index_version, dirty, updated_at)
			VALUES ('repository', ?, ?, ?, ?, '', ?, 0, ?)
			ON CONFLICT (scope, scope_id) DO UPDATE SET
			  manifest_hash = excluded.manifest_hash, lockfile_hash = excluded.lockfile_hash,
			  toolchain = excluded.toolchain, index_version = excluded.index_version,
			  dirty = 0, updated_at = excluded.updated_at`,
			repositoryID, manifestHash(pairs), manifestHash(lock), version.Current().Go,
			version.IndexerVersion, time.Now().Unix())
		return err
	})
}

func manifestHash(pairs map[string]string) string {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	var lenBuf [binary.MaxVarintLen64]byte
	for _, k := range keys {
		// Length-prefixed so that no two different (path, hash) sets can
		// produce the same manifest.
		for _, part := range [2]string{k, pairs[k]} {
			n := binary.PutUvarint(lenBuf[:], uint64(len(part)))
			h.Write(lenBuf[:n])
			io.WriteString(h, part)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
