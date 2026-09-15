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

	"github.com/akynte/local-engineer/internal/proxy"
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
	Egress    EgressConfig    `yaml:"egress"`
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

	// ACPAddr turns on the ACP-over-TCP bridge of §4.3 and binds it. Empty
	// leaves it off, which is the default for two reasons: it is another
	// listening socket, and DR-5's native engine does not speak ACP, so there
	// is nothing to carry until an agent command is configured.
	ACPAddr string `yaml:"acp_addr,omitempty"`
	// ACPCommand is the agent the bridge runs per connection, argv style. The
	// bridge carries bytes and never parses the protocol, so any ACP agent
	// works here.
	ACPCommand []string `yaml:"acp_command,omitempty"`
}

// InferenceConfig selects the mode and the embedded server's command line.
type InferenceConfig struct {
	Mode InferenceMode `yaml:"mode"`
	// BaseURL is used when Mode is external.
	BaseURL string `yaml:"base_url,omitempty"`
	// Binary is the embedded llama-server executable.
	Binary string `yaml:"binary,omitempty"`
	// Model is the GGUF to serve: an absolute path, or a filename under
	// /data/models. It is configuration rather than part of the profile
	// because a profile describes the *machine*, and the same machine runs
	// different models.
	Model string `yaml:"model,omitempty"`
	// Args are appended after the arguments derived from the active profile,
	// so an operator can override any of them — llama-server takes the last
	// occurrence of a repeated flag — or pass something the profile has no
	// field for. Everything the profile does express belongs there, not here:
	// §9.3 puts thread counts and offload layers in the profile on purpose.
	Args []string `yaml:"args,omitempty"`
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

// EgressConfig configures the allowlisting proxy of §6.1.
//
// The proxy is the provisioning lane's only route out, and it is off by
// default. A machine whose premise is that it has no egress should not acquire
// some because a config file shipped with it enabled — turning it on is an
// operator saying "this box may talk to these hosts".
type EgressConfig struct {
	// Enabled starts the proxy with `le api`. Off by default, and forced off
	// in offline mode.
	Enabled bool `yaml:"enabled"`
	// DepsPort and DocsPort are the loopback ports for the two lanes of §6.1.
	// One listener per lane is what keeps the lane out of the request, where
	// a client could choose it.
	DepsPort int `yaml:"deps_port"`
	DocsPort int `yaml:"docs_port"`
	// Allowlist is the hosts each lane may reach.
	Allowlist proxy.Allowlist `yaml:"allowlist"`
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
				// The installation's own tree: profiles, the Go tools, the
				// TypeScript sidecar and semgrep's virtualenv all live here,
				// and all are read-only at run time. Workspace data is under
				// $LE_DATA and is granted per task, never from this list.
				//
				// This said "/opt/le/toolchain" until it was noticed that no
				// image ever created that directory, so every tool under
				// /opt/le was denied. Nothing caught it because no recipe
				// invoked one until semgrep shipped, and the symptom was the
				// sandbox helper exiting 126.
				"/opt/le", "/usr/local/go",
			},
		},
		Egress: EgressConfig{
			// Off by default: see EgressConfig. The ports and the allowlist
			// are written anyway so `le config init` produces a file an
			// operator can read and enable, rather than one that hides the
			// feature until they find the documentation.
			Enabled:   false,
			DepsPort:  7780,
			DocsPort:  7781,
			Allowlist: proxy.DefaultAllowlist(),
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

// EnvInferenceMode and EnvInferenceBaseURL override the inference block, so the
// split deployment can point the supervisor at an inference container without
// baking a le.yaml into the image. They follow the same rule as every other
// override here: the environment wins over the file, so the deployment that
// sets them does not depend on what a previous start happened to write.
const (
	EnvInferenceMode    = "LE_INFERENCE_MODE"
	EnvInferenceBaseURL = "LE_INFERENCE_BASE_URL"
)

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
	// The split deployment (deploy/docker-compose.split.yml) runs llama-server
	// in its own container and points the supervisor at it over HTTP. DR-1
	// promised that layout as the alternative to the single container, and it
	// is a configuration change only — but only if the configuration actually
	// arrives, which is what these two do.
	if v := os.Getenv(EnvInferenceMode); v != "" {
		// An unrecognised value is left for Validate to reject by name, rather
		// than being silently dropped back to the default: a deployment that
		// misspells the mode should fail loudly, not run with no inference.
		c.Inference.Mode = InferenceMode(v)
	}
	if v := os.Getenv(EnvInferenceBaseURL); v != "" {
		c.Inference.BaseURL = v
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
	if c.API.ACPAddr != "" && len(c.API.ACPCommand) == 0 {
		return fmt.Errorf("config: api.acp_addr is set but api.acp_command is empty; " +
			"the bridge would accept connections and have no agent to hand them to")
	}

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
	// §6.1's offline mode is "--network none plus an in-container inference
	// route only". An egress proxy is the opposite of that, so the two
	// settings contradicting each other is an error rather than a precedence
	// rule: silently winning either way would leave an operator believing
	// something about their machine that is not true.
	if c.Offline && c.Egress.Enabled {
		return errors.New("config: offline and egress.enabled are both set. " +
			"Offline mode has no route out, so the §6.1 provisioning lanes cannot exist. " +
			"Set egress.enabled to false, or unset offline / LE_OFFLINE — whichever you " +
			"actually meant. This is an error rather than a precedence rule because " +
			"either silent winner would leave you believing something untrue about this machine")
	}
	if c.Egress.Enabled {
		if err := c.Egress.Allowlist.Validate(); err != nil {
			return fmt.Errorf("config: egress.allowlist: %w", err)
		}
		if len(c.Egress.Allowlist.Rules) == 0 {
			return errors.New("config: egress.enabled is set with an empty allowlist; " +
				"a proxy that allows nothing is a slower way to have no egress")
		}
		if c.Egress.DepsPort == c.Egress.DocsPort {
			return fmt.Errorf("config: egress.deps_port and egress.docs_port are both %d; "+
				"one listener per lane is what keeps a client from choosing its own lane", c.Egress.DepsPort)
		}
		for name, port := range map[string]int{"deps_port": c.Egress.DepsPort, "docs_port": c.Egress.DocsPort} {
			if port <= 0 || port > 65535 {
				return fmt.Errorf("config: egress.%s (%d) is not a port", name, port)
			}
			if port == c.Inference.Port {
				return fmt.Errorf("config: egress.%s (%d) collides with the inference port", name, port)
			}
		}
	}
	return nil
}

func isLoopback(url string) bool {
	return strings.Contains(url, "127.0.0.1") || strings.Contains(url, "localhost") ||
		strings.Contains(url, "[::1]") || strings.Contains(url, "unix://")
}
