package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ProvidersFile is `/data/config/providers.yaml` (§9.1): it maps roles to a
// provider and model alias, "with a default that maps all roles to one model.
// Multi-model routing is off by default and turned on per role only with
// Stage E evidence."
type ProvidersFile struct {
	// Default is the provider every unrouted role uses.
	Default string `yaml:"default"`
	// Providers are the declared backends.
	Providers []ProviderSpec `yaml:"providers"`
	// Roles overrides the default per role. Empty is the shipped state.
	Roles map[string]string `yaml:"roles,omitempty"`
}

// ProviderSpec declares one backend.
type ProviderSpec struct {
	Name    string `yaml:"name"`
	Kind    Kind   `yaml:"kind"`
	BaseURL string `yaml:"base_url,omitempty"`
	Model   string `yaml:"model,omitempty"`
	// APIKeyEnv names an environment variable rather than holding a secret, so
	// providers.yaml stays safe to commit and to attach to a bug report.
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
	// Capabilities may be declared explicitly for openai_compatible backends
	// whose feature set the system cannot infer (DR-4).
	Capabilities *Capabilities `yaml:"capabilities,omitempty"`
	// TimeoutSeconds bounds one HTTP request to this provider. Zero means
	// DefaultTimeout.
	//
	// It belongs in configuration for the same reason §9.3's other limits do:
	// how long a request takes is a fact about the model and the machine, not
	// about this code. A profile's own budgets decide it — prefill of
	// context_tokens plus decode of reserved_output_tokens, at the rates `le
	// models bench` measured — and a fixed limit smaller than that makes a
	// documented, tunable budget unusable against a constant nobody can tune.
	//
	// Raising reserved_output_tokens from 8192 to 16384 on a 416 tok/s prefill
	// and 35 tok/s decode is how this was found: about 10.4 minutes for one
	// call, against a hardcoded 10, surfacing as a client timeout rather than
	// as "your budget implies a request longer than the client allows".
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
}

// DefaultProvidersFile is the shipped configuration: one local provider, every
// role on it.
func DefaultProvidersFile(baseURL, model string) ProvidersFile {
	return ProvidersFile{
		Default: "local",
		Providers: []ProviderSpec{{
			Name: "local", Kind: KindLlamaCPP, BaseURL: baseURL, Model: model,
		}},
	}
}

// Router resolves a role to a provider.
type Router struct {
	mu        sync.RWMutex
	providers map[string]Provider
	roles     map[Role]string
	def       string
	offline   bool
}

// NewRouter builds a router from a parsed providers file.
//
// offline enforces §9.1's "remote, opt-in, off in offline mode": when set, a
// non-local provider is refused at construction rather than at call time, so
// a misconfiguration is a startup failure and never a silent network egress.
func NewRouter(f ProvidersFile, offline bool) (*Router, error) {
	if len(f.Providers) == 0 {
		return nil, errors.New("llm: providers.yaml declares no providers")
	}
	r := &Router{providers: map[string]Provider{}, roles: map[Role]string{}, def: f.Default, offline: offline}

	for _, spec := range f.Providers {
		p, err := build(spec)
		if err != nil {
			return nil, err
		}
		if offline && !p.Capabilities().Local {
			return nil, fmt.Errorf("llm: offline mode is on but provider %q (%s) is remote; "+
				"remove it from providers.yaml or turn offline off", spec.Name, spec.Kind)
		}
		r.providers[spec.Name] = p
	}
	if r.def == "" {
		r.def = f.Providers[0].Name
	}
	if _, ok := r.providers[r.def]; !ok {
		return nil, fmt.Errorf("llm: default provider %q is not declared", r.def)
	}
	for roleName, providerName := range f.Roles {
		role := Role(roleName)
		if !validRole(role) {
			return nil, fmt.Errorf("llm: %q is not a routable role (one of %v)", roleName, AllRoles())
		}
		if _, ok := r.providers[providerName]; !ok {
			return nil, fmt.Errorf("llm: role %s routes to undeclared provider %q", roleName, providerName)
		}
		r.roles[role] = providerName
	}
	return r, nil
}

func validRole(r Role) bool {
	for _, c := range AllRoles() {
		if c == r {
			return true
		}
	}
	return false
}

func build(spec ProviderSpec) (Provider, error) {
	if spec.Name == "" {
		return nil, errors.New("llm: a provider entry has no name")
	}
	opts := Options{Name: spec.Name, BaseURL: spec.BaseURL, Model: spec.Model}
	if spec.APIKeyEnv != "" {
		opts.APIKey = os.Getenv(spec.APIKeyEnv)
		if opts.APIKey == "" {
			return nil, fmt.Errorf("llm: provider %q expects the API key in $%s, which is unset",
				spec.Name, spec.APIKeyEnv)
		}
	}
	if spec.Capabilities != nil {
		opts.Caps = *spec.Capabilities
	}
	if spec.TimeoutSeconds < 0 {
		return nil, fmt.Errorf("llm: provider %q has a negative timeout_seconds (%d)",
			spec.Name, spec.TimeoutSeconds)
	}
	opts.Timeout = time.Duration(spec.TimeoutSeconds) * time.Second

	switch spec.Kind {
	case KindLlamaCPP:
		if spec.BaseURL == "" {
			return nil, fmt.Errorf("llm: provider %q needs a base_url", spec.Name)
		}
		return NewLlamaCPP(opts), nil
	case KindOpenAICompatible:
		if spec.BaseURL == "" {
			return nil, fmt.Errorf("llm: provider %q needs a base_url", spec.Name)
		}
		if spec.Capabilities == nil {
			// DR-4: feature gaps must be declared explicitly. An
			// openai_compatible backend gets the conservative floor — chat
			// only — unless the operator says otherwise.
			opts.Caps = Capabilities{Kind: KindOpenAICompatible, ToolCalling: true, Local: isLocalURL(spec.BaseURL)}
		}
		opts.Caps.Kind = KindOpenAICompatible
		return NewOpenAICompatible(opts), nil
	case KindOpenAI:
		if opts.BaseURL == "" {
			opts.BaseURL = "https://api.openai.com"
		}
		opts.Caps = Capabilities{Kind: KindOpenAI, ToolCalling: true, StructuredOutput: true,
			Embeddings: true, Vision: true, Local: false, MaxContext: opts.Caps.MaxContext}
		return NewOpenAICompatible(opts), nil
	case KindAnthropic:
		if opts.BaseURL == "" {
			opts.BaseURL = "https://api.anthropic.com"
		}
		opts.Caps = Capabilities{Kind: KindAnthropic, ToolCalling: true, StructuredOutput: true,
			Vision: true, ThinkingControl: true, Local: false, MaxContext: opts.Caps.MaxContext}
		return NewAnthropic(opts), nil
	default:
		return nil, fmt.Errorf("llm: provider %q has unknown kind %q (one of %s)", spec.Name, spec.Kind,
			strings.Join([]string{string(KindLlamaCPP), string(KindOpenAICompatible),
				string(KindOpenAI), string(KindAnthropic)}, ", "))
	}
}

func isLocalURL(u string) bool {
	return strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") ||
		strings.Contains(u, "[::1]") || strings.Contains(u, "host.docker.internal")
}

// For resolves a role to its provider.
func (r *Router) For(role Role) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name := r.def
	if n, ok := r.roles[role]; ok {
		name = n
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("llm: no provider for role %s", role)
	}
	return p, nil
}

// Names lists declared providers.
func (r *Router) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Routing reports the effective role-to-provider map, for `le doctor`.
func (r *Router) Routing() map[Role]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[Role]string{}
	for _, role := range AllRoles() {
		if n, ok := r.roles[role]; ok {
			out[role] = n
		} else {
			out[role] = r.def
		}
	}
	return out
}

// Health probes every declared provider.
func (r *Router) Health(ctx context.Context) map[string]string {
	r.mu.RLock()
	providers := make(map[string]Provider, len(r.providers))
	for k, v := range r.providers {
		providers[k] = v
	}
	r.mu.RUnlock()

	out := map[string]string{}
	for name, p := range providers {
		if err := p.Health(ctx); err != nil {
			out[name] = err.Error()
		} else {
			out[name] = "ok"
		}
	}
	return out
}

// Close releases every provider.
func (r *Router) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, p := range r.providers {
		errs = append(errs, p.Close())
	}
	return errors.Join(errs...)
}

// LoadProvidersFile reads providers.yaml from the config directory.
func LoadProvidersFile(configDir string) (ProvidersFile, error) {
	path := filepath.Join(configDir, "providers.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return ProvidersFile{}, err
	}
	var f ProvidersFile
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("llm: parse %s: %w", path, err)
	}
	return f, nil
}

// SaveProvidersFile writes providers.yaml.
func SaveProvidersFile(configDir string, f ProvidersFile) error {
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		return err
	}
	body, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	header := "# Provider and role routing (design v3 §9.1).\n" +
		"# The shipped state maps every role to one local model. Turn on multi-model\n" +
		"# routing per role only with Stage E evidence that it helps.\n" +
		"# Never put a secret here: use api_key_env to name an environment variable.\n"
	path := filepath.Join(configDir, "providers.yaml")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), body...), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
