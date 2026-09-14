// Package config loads the operator-facing configuration of design v3 §5.2:
// le.yaml, providers.yaml and the hardware profiles.
//
// §9.3 is the governing rule here: context limits, VRAM and RAM budgets,
// thread counts, offload layers, sampling defaults, thinking policy,
// tool-surface size and packet sizes must not be hardcoded. Every one of them
// lives in a profile or the workspace config, and the reference profile is
// only a default value. Anything this package exposes as a Go constant is a
// fallback used when no profile is loaded, and is named as such.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// InferenceMode selects where inference runs (§4.3).
type InferenceMode string

const (
	// ModeEmbedded starts llama-server as a supervised child.
	ModeEmbedded InferenceMode = "embedded"
	// ModeExternal talks to a server the operator runs.
	ModeExternal InferenceMode = "external"
	// ModeNone disables inference, for indexing-only use.
	ModeNone InferenceMode = "none"
)

// Config is `/data/config/le.yaml`.
type Config struct {
	// DataDir is normally set by LE_DATA and repeated here for the record.
	DataDir string `yaml:"data_dir,omitempty"`

	API       APIConfig       `yaml:"api"`
	Inference InferenceConfig `yaml:"inference"`
	Sandbox   SandboxConfig   `yaml:"sandbox"`
	Index     IndexConfig     `yaml:"index"`
	Offline   bool            `yaml:"offline"`

	// Profile names the active hardware profile in profiles/ (§9.2).
	Profile string `yaml:"profile"`

	// Gates configures the human gates of §3.3.
	Gates GateConfig `yaml:"gates"`
}

// GateConfig selects which decisions need a person.
//
// The shipped default is not "approve everything": a system that never asks is
// one whose gates are decoration. It is also not "ask about everything", which
// trains people to approve without reading.
type GateConfig struct {
	// Breaking gates a change whose impact report names breaking consumers.
	Breaking bool `yaml:"breaking"`
	// OutOfScope gates a change outside the task's declared scope.
	OutOfScope bool `yaml:"out_of_scope"`
	// Apply gates applying a completed task's change.
	Apply bool `yaml:"apply"`
	// Plan gates a decomposition before it runs.
	Plan bool `yaml:"plan"`
	// TimeoutMinutes expires an unanswered gate. Zero waits indefinitely.
	TimeoutMinutes int `yaml:"timeout_minutes"`
}

// APIConfig configures `le api` (§4.3 child 3).
type APIConfig struct {
	// Addr defaults to loopback only: §4.1 publishes 127.0.0.1:7777 and the
	// supervisor must not be reachable from outside the host by accident.
	Addr string `yaml:"addr"`
	// ShutdownGrace is the documented 30 s of §4.4.
	ShutdownGraceSeconds int `yaml:"shutdown_grace_seconds"`
}

// InferenceConfig selects the mode and the embedded server's command line.
type InferenceConfig struct {
	Mode InferenceMode `yaml:"mode"`
	// BaseURL is used when Mode is external.
	BaseURL string `yaml:"base_url,omitempty"`
	// Binary and Args drive the embedded llama-server.
	Binary string   `yaml:"binary,omitempty"`
	Args   []string `yaml:"args,omitempty"`
	// Port is the embedded server's port; the sandbox allows TCP connect to
	// exactly this port and nothing else (§6.1).
	Port int `yaml:"port"`
	// StartTimeoutSeconds bounds model load, which on a cold GGUF can be slow.
	StartTimeoutSeconds int `yaml:"start_timeout_seconds"`
}

// SandboxConfig selects the isolation layers (§6.1, DR-3).
type SandboxConfig struct {
	// Mode is "auto" (strongest available), "landlock", "bwrap" or "none".
	Mode string `yaml:"mode"`
	// ReadOnlyPaths are toolchain paths every task may read.
	ReadOnlyPaths []string `yaml:"read_only_paths"`
	// AllowedTCPConnect lists extra ports tasks may dial beyond the inference
	// port. Keep this empty unless a test service needs it.
	AllowedTCPConnect []int `yaml:"allowed_tcp_connect"`
}

// IndexConfig tunes indexing.
type IndexConfig struct {
	MaxFileBytes int64    `yaml:"max_file_bytes"`
	Excludes     []string `yaml:"excludes"`
	ChunkLines   int      `yaml:"chunk_lines"`
	// WatchEnabled turns on the fsnotify watcher of §3.4.
	WatchEnabled bool `yaml:"watch_enabled"`
}

// DefaultExcludes are the directories the indexer prunes. They are here
// rather than only in internal/index so that `le config init` writes them into
// le.yaml, where an operator can see and change them.
func DefaultExcludes() []string {
	return []string{
		".git", ".le", ".idea", ".vscode",
		"node_modules", "vendor", "dist", "build", "out", "target",
		".next", ".nuxt", ".svelte-kit", "__pycache__", ".venv", ".tox",
		".cache", ".gradle", ".terraform", "coverage",
	}
}

// Default returns the shipped defaults. These are fallbacks, not tuning: the
// operative values come from the active profile (§9.3).
func Default() Config {
	return Config{
		API: APIConfig{Addr: LoopbackAddr, ShutdownGraceSeconds: 30},
		Inference: InferenceConfig{
			Mode: ModeNone, Port: 8080, StartTimeoutSeconds: 300,
			Binary: "llama-server",
		},
		Sandbox: SandboxConfig{
			Mode: "auto",
			ReadOnlyPaths: []string{
				"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc/ssl", "/etc/ca-certificates",
				"/opt/le/toolchain", "/usr/local/go",
			},
		},
		Gates: GateConfig{Breaking: true, OutOfScope: true, Apply: true, Plan: false},
		Index: IndexConfig{
			MaxFileBytes: 1 << 20, ChunkLines: 60, WatchEnabled: true,
			// Carried explicitly so the generated le.yaml holds them. Leaving
			// this nil wrote `excludes: []` to the file, which on the next
			// load is an empty-but-present list — and the indexer then walked
			// .git and node_modules.
			Excludes: DefaultExcludes(),
		},
		Profile: "reference-8gb-cuda-64gb-ram",
	}
}

// EnvAPIAddr overrides api.addr. The container image sets it to 0.0.0.0:7777
// because Docker's port publishing cannot reach a loopback bind inside the
// container; exposure is then restricted on the host side by publishing as
// `-p 127.0.0.1:7777:7777` (§4.1).
const EnvAPIAddr = "LE_API_ADDR"

// LoopbackAddr is the safe default when running directly on a host.
const LoopbackAddr = "127.0.0.1:7777"

// IsLoopbackAddr reports whether an address binds only the loopback interface.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExposureWarning returns a warning when the API is bound to an address
// reachable from outside this machine without the container boundary in front
// of it, and "" when the binding is safe. The supervisor logs it at startup
// and `le doctor` reports it: a supervisor that can drive a sandbox and read
// every indexed repository must never be silently reachable from a network.
func ExposureWarning(addr string, inContainer bool) string {
	if IsLoopbackAddr(addr) {
		return ""
	}
	if inContainer {
		return fmt.Sprintf("the API is bound to %s inside the container. That is expected: "+
			"publish it as `-p 127.0.0.1:7777:7777` so only this host can reach it. "+
			"Publishing as `-p 7777:7777` would expose the supervisor to your whole network.", addr)
	}
	return fmt.Sprintf("the API is bound to %s on this host, not to loopback. "+
		"The supervisor can run sandboxed commands and read every indexed repository; "+
		"bind %s unless you have put an authenticating proxy in front of it.", addr, LoopbackAddr)
}

// Path is the location of le.yaml inside the data directory.
func Path(configDir string) string { return filepath.Join(configDir, "le.yaml") }

// Load reads le.yaml, filling absent fields from Default. A missing file is
// not an error: the shipped defaults must produce a working container.
func Load(configDir string) (Config, error) {
	cfg := Default()
	body, err := os.ReadFile(Path(configDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.applyEnv()
			return cfg, cfg.Validate()
		}
		return cfg, fmt.Errorf("config: read %s: %w", Path(configDir), err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", Path(configDir), err)
	}
	cfg.applyEnv()
	return cfg, cfg.Validate()
}

// applyEnv lets the container image and the operator override a small number
// of fields without rewriting le.yaml.
func (c *Config) applyEnv() {
	if v := os.Getenv(EnvAPIAddr); v != "" {
		c.API.Addr = v
	}
	if v := os.Getenv("LE_PROFILE"); v != "" {
		c.Profile = v
	}
	if v := os.Getenv("LE_OFFLINE"); v == "1" || v == "true" {
		c.Offline = true
	}
}

// Save writes le.yaml.
func Save(configDir string, cfg Config) error {
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return err
	}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# local-engineer configuration. See docs/reference/configuration.md.\n" +
		"# Values here are defaults; per-hardware tuning belongs in the active profile (§9.2).\n"
	tmp := Path(configDir) + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), body...), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, Path(configDir))
}

// Validate rejects configurations that would start a container in a state the
// operator did not intend.
func (c Config) Validate() error {
	switch c.Inference.Mode {
	case ModeEmbedded, ModeExternal, ModeNone, "":
	default:
		return fmt.Errorf("config: inference.mode %q is not one of embedded, external, none", c.Inference.Mode)
	}
	if c.Inference.Mode == ModeExternal && c.Inference.BaseURL == "" {
		return fmt.Errorf("config: inference.mode is external but no base_url is set")
	}
	switch c.Sandbox.Mode {
	case "auto", "landlock", "bwrap", "none", "":
	default:
		return fmt.Errorf("config: sandbox.mode %q is not one of auto, landlock, bwrap, none", c.Sandbox.Mode)
	}
	if c.Offline && c.Inference.Mode == ModeExternal && !isLoopback(c.Inference.BaseURL) {
		return fmt.Errorf("config: offline is set but inference.base_url %q is not local", c.Inference.BaseURL)
	}
	return nil
}

func isLoopback(url string) bool {
	return strings.Contains(url, "127.0.0.1") || strings.Contains(url, "localhost") ||
		strings.Contains(url, "[::1]") || strings.Contains(url, "unix://")
}
