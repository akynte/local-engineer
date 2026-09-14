// Package workspace implements the unit of isolation (design v3 §2).
//
// A workspace is identified by a stable, content-derived id that is pinned in
// a `.le/workspace.yaml` file inside the root. Moving the directory without
// that file creates a new workspace on purpose; `le workspace adopt` re-binds
// an existing id after a move (§2.1, DR-6).
package workspace

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/akynte/local-engineer/internal/version"
)

// IDLength is the number of base32 characters retained from the digest (§2.1).
const IDLength = 26

// MarkerDir and MarkerFile locate the pin file inside a workspace root.
const (
	MarkerDir  = ".le"
	MarkerFile = "workspace.yaml"
)

var idPattern = regexp.MustCompile(`^[a-z2-7]{26}$`)

// ErrNotAWorkspace is returned when no `.le/workspace.yaml` exists at or above
// a path.
var ErrNotAWorkspace = errors.New("workspace: no .le/workspace.yaml found")

// ID is a workspace identifier. It is deliberately a distinct type so that the
// storage API cannot be called with an arbitrary string (§2.3).
type ID string

func (id ID) String() string { return string(id) }

// Valid reports whether the id has the shape produced by DeriveID.
func (id ID) Valid() bool { return idPattern.MatchString(string(id)) }

// b32 is lowercase RFC 4648 base32 without padding. Lowercase keeps ids usable
// as directory names on case-insensitive filesystems.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// writeComponents feeds a length-prefixed encoding of each component into h.
//
// A separator byte is not enough: if a component can contain the separator,
// two different component tuples can produce the same byte stream, and two
// different projects would then share a workspace id. A fuzz test found
// exactly that collision when NUL was the separator, so the encoding is
// length-prefixed instead, which is injective for any content.
func writeComponents(h io.Writer, parts ...string) {
	var lenBuf [binary.MaxVarintLen64]byte
	for _, p := range parts {
		n := binary.PutUvarint(lenBuf[:], uint64(len(p)))
		h.Write(lenBuf[:n])
		io.WriteString(h, p)
	}
}

// DeriveID computes
//
//	workspace_id = base32(sha256(canonical_root_path || git_remote_url_if_any || user_supplied_name))[:26]
//
// exactly as specified in §2.1. The three components are joined with a NUL
// separator so that no concatenation of one set of inputs can collide with a
// different set, and the scheme version is mixed in so DR-6's re-key migration
// path stays open.
func DeriveID(canonicalRoot, gitRemote, name string) ID {
	h := sha256.New()
	writeComponents(h,
		fmt.Sprintf("le-workspace-v%d", version.WorkspaceIDScheme),
		canonicalRoot, gitRemote, name)
	return ID(strings.ToLower(b32.EncodeToString(h.Sum(nil)))[:IDLength])
}

// DeriveRepositoryID computes the per-repository id used inside a multi-repo
// workspace (§2.1: "each with its own repository id").
func DeriveRepositoryID(ws ID, relPath, remote string) string {
	h := sha256.New()
	writeComponents(h,
		fmt.Sprintf("le-repo-v%d", version.WorkspaceIDScheme),
		ws.String(), filepath.ToSlash(relPath), remote)
	return strings.ToLower(b32.EncodeToString(h.Sum(nil)))[:IDLength]
}

// CanonicalRoot resolves a path to the absolute, symlink-free form used as the
// first identity component. The path must exist: identity must never depend on
// a guess about a path that is not there.
func CanonicalRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("workspace: absolute path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: resolve %s: %w", abs, err)
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspace: stat %s: %w", resolved, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("workspace: %s is not a directory", resolved)
	}
	return resolved, nil
}

// GitRemote returns the fetch URL of `origin` for a repository root, or the
// empty string when the directory is not a git repository or has no origin.
// A missing remote is not an error: §2.1 says the remote participates "if any".
func GitRemote(root string) string {
	cmd := exec.Command("git", "-C", root, "remote", "get-url", "origin")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GitDefaultBranch reports the branch `origin/HEAD` points at, falling back to
// the currently checked-out branch and then to "main".
func GitDefaultBranch(root string) string {
	if out, err := exec.Command("git", "-C", root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return strings.TrimPrefix(s, "origin/")
		}
	}
	if out, err := exec.Command("git", "-C", root, "branch", "--show-current").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return "main"
}

// FindRoot walks up from start looking for a directory holding
// `.le/workspace.yaml`, and returns the first one found.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, MarkerDir, MarkerFile)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotAWorkspace
		}
		dir = parent
	}
}
