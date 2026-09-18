package lsp

import (
	"context"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// ServerConfig is one operator-configured language server.
//
// Argv is a command, so it is operator configuration and never model input, for
// the same reason a verification preset is: a model that could name the binary
// would have a process launcher wearing a language server's name.
type ServerConfig struct {
	// Argv is the command, for example ["gopls", "-mode=stdio"].
	Argv []string `yaml:"argv"`
	// Extensions are the file suffixes this server answers for, with the dot.
	Extensions []string `yaml:"extensions"`
	// LanguageID is the protocol's identifier for the language, which a server
	// uses to decide how to parse a buffer it is told about.
	LanguageID string `yaml:"language_id"`
}

// Config is the whole live-query layer.
//
// It is empty by default, and an empty configuration is a supported state
// rather than a degraded one: SCIP answers the same questions for files that
// have not changed, which is most of them. Configuring a server buys freshness
// for the file being edited, at the cost of a process holding an index in
// memory.
type Config struct {
	Servers []ServerConfig `yaml:"servers"`
}

// Pool starts servers lazily, one per configured entry, and keeps them for the
// life of the pool.
//
// Lazily because a language server costs seconds to start and gigabytes to
// index, and a task that touches only Go should not pay for rust-analyzer. Kept
// because paying that cost once per task and then again per query would make
// the live layer slower than the index it is meant to complement.
type Pool struct {
	config Config
	root   string

	mu      sync.Mutex
	clients map[int]*Client
	// failed records servers that would not start, so a missing binary is
	// reported once and then stops being retried on every query.
	failed map[int]error
}

// NewPool builds a pool over a worktree.
func NewPool(root string, config Config) *Pool {
	return &Pool{root: root, config: config, clients: map[int]*Client{}, failed: map[int]error{}}
}

// Enabled reports whether any server is configured.
func (p *Pool) Enabled() bool { return p != nil && len(p.config.Servers) > 0 }

// For returns the client that answers for a path, or nil when none is
// configured, none could start, or the pool itself is nil.
//
// A nil client is the caller's cue to use the index. Every caller here treats
// the live layer as an improvement on a deterministic answer rather than a
// prerequisite for one, so "no server" is never an error.
func (p *Pool) For(ctx context.Context, rel string) (*Client, string) {
	if !p.Enabled() {
		return nil, ""
	}
	ext := strings.ToLower(path.Ext(rel))
	if ext == "" {
		return nil, ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, server := range p.config.Servers {
		if !matches(server.Extensions, ext) {
			continue
		}
		if err := p.failed[i]; err != nil {
			return nil, ""
		}
		if c, ok := p.clients[i]; ok {
			return c, server.LanguageID
		}
		if len(server.Argv) == 0 {
			p.failed[i] = errNoCommand
			return nil, ""
		}
		if _, err := exec.LookPath(server.Argv[0]); err != nil {
			p.failed[i] = err
			return nil, ""
		}
		c, err := Start(ctx, p.root, server.Argv...)
		if err != nil {
			p.failed[i] = err
			return nil, ""
		}
		p.clients[i] = c
		return c, server.LanguageID
	}
	return nil, ""
}

// Failures reports the servers that would not start, so `le doctor` and the
// trace can say why the live layer is not answering rather than leaving it
// looking like there was nothing to find.
func (p *Pool) Failures() map[string]error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]error{}
	for i, err := range p.failed {
		if i < len(p.config.Servers) && len(p.config.Servers[i].Argv) > 0 {
			out[p.config.Servers[i].Argv[0]] = err
		}
	}
	return out
}

// Close shuts every started server down. A pool that outlived its task would
// hold an index of a worktree that no longer exists.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	clients := make([]*Client, 0, len(p.clients))
	for _, c := range p.clients {
		clients = append(clients, c)
	}
	p.clients = map[int]*Client{}
	p.mu.Unlock()

	for _, c := range clients {
		_ = c.Close()
	}
}

var errNoCommand = &responseError{Code: -1, Message: "server configured with no command"}

func matches(extensions []string, ext string) bool {
	for _, e := range extensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}
