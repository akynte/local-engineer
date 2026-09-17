// Package mcp exposes Local Engineer to an MCP client such as OpenCode.
//
// It is an adapter, not a second implementation. Every tool here resolves a
// workspace through internal/session and then calls the same packages the CLI
// calls — internal/graph, internal/retrieval, internal/doctor. Nothing in this
// package shells out to `le`: a tool that built a command line out of
// model-supplied arguments would put an injection boundary between the model
// and a shell, which is the failure this whole system is arranged to avoid.
//
// # Why MCP rather than a plugin
//
// OpenCode's officially supported extension points are MCP servers, plugins and
// custom commands. Plugins are JavaScript or TypeScript and register tools and
// hooks; they cannot render a panel, and this project is Go. An MCP server is
// language-agnostic, is registered in one block of opencode.jsonc, and its
// tools become available to the agent alongside the built-in ones.
//
// # Isolation
//
// Unlike a CLI process, this server is long-lived and will be asked about more
// than one repository. Every call rebinds through session.Open, which performs
// §2.2's switch and clears the previous workspace's saved inference slots. No
// handle is cached between calls, because a cached handle is how project B ends
// up reading project A's index.
//
// # Trust
//
// A tool result carries repository content, and a result is read by a model. It
// is therefore returned as data with its origin named, on the same reasoning as
// internal/trust: content that arrives labelled cannot be mistaken for an
// instruction the supervisor issued.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/akynte/local-engineer/internal/session"
	"github.com/akynte/local-engineer/internal/version"
)

// Options configures the server.
type Options struct {
	// DataDir is the storage root, as `--data` gives the CLI.
	DataDir string
	// WorkDir is the directory a tool call resolves against when it is given
	// no path of its own. It is the repository the client has open.
	WorkDir string
}

// Server holds what the tools need. It deliberately holds no workspace handle:
// see the package comment.
type Server struct {
	opts Options
}

// New builds the MCP server with Local Engineer's tools registered.
func New(o Options) (*mcp.Server, error) {
	if o.DataDir == "" {
		return nil, errors.New("mcp: no data directory")
	}
	if o.WorkDir == "" {
		return nil, errors.New("mcp: no working directory")
	}
	s := &Server{opts: o}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "local-engineer",
		Title:   "Local Engineer",
		Version: version.Version,
	}, nil)
	s.register(srv)
	return srv, nil
}

// resolve binds to the workspace a tool call is about.
//
// The path is optional and relative to the working directory the server was
// started in. It is confined to that directory: a client may ask about a
// subdirectory of the repository it opened, and must not be able to ask about
// an unrelated one by walking upwards. The workspace pin is then what actually
// decides which storage is opened, so this bounds the search rather than
// granting access.
func (s *Server) resolve(ctx context.Context, path string) (*session.Session, error) {
	dir, err := s.confine(path)
	if err != nil {
		return nil, err
	}
	sess, err := session.Open(ctx, s.opts.DataDir, dir)
	if err != nil {
		var missing *session.ErrNoWorkspace
		if errors.As(err, &missing) {
			return nil, fmt.Errorf(
				"%s is not inside a Local Engineer workspace. Use the le_workspace_init "+
					"tool to set one up in the repository root", dir)
		}
		return nil, err
	}
	return sess, nil
}

// confine resolves a caller-supplied path inside the server's working
// directory, refusing anything that escapes it.
func (s *Server) confine(path string) (string, error) {
	base, err := filepath.EvalSymlinks(s.opts.WorkDir)
	if err != nil {
		return "", fmt.Errorf("mcp: resolving the working directory: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		return base, nil
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be relative to the open repository, got %q", path)
	}
	joined := filepath.Join(base, filepath.FromSlash(path))
	// EvalSymlinks after joining, so a symlink inside the repository cannot
	// point out of it. A path that does not exist yet is checked as written.
	if resolved, err := filepath.EvalSymlinks(joined); err == nil {
		joined = resolved
	}
	rel, err := filepath.Rel(base, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the open repository", path)
	}
	return joined, nil
}

// fail returns a tool error the model can act on. MCP carries tool failures as
// results rather than protocol errors, so the model sees them and can retry or
// change approach instead of the call vanishing.
func fail(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

// text returns a successful result carrying rendered output.
func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
