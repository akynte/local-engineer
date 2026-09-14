package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/akynte/local-engineer/profiles"
)

// Profile is a hardware profile from profiles/ (§9.2). Profiles are shipped as
// data, generated from measurements by `le models bench`, and are the only
// place the values §9.3 forbids hardcoding may live.
type Profile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Hardware records what the profile was measured on, so a mismatch can be
	// warned about rather than silently tolerated (§9.2).
	Hardware Hardware `yaml:"hardware"`

	// Context and packet limits (§9.3).
	ContextTokens   int `yaml:"context_tokens"`
	MaxPacketTokens int `yaml:"max_packet_tokens"`
	ReservedOutput  int `yaml:"reserved_output_tokens"`
	ToolSurfaceMax  int `yaml:"max_tools_exposed"`

	// Admission limits, measured not guessed.
	PeakVRAMMB  int `yaml:"measured_peak_vram_mb"`
	PeakRAMMB   int `yaml:"measured_peak_ram_mb"`
	Concurrency int `yaml:"max_concurrent_tasks"`

	// Runtime knobs passed to llama-server.
	Runtime RuntimeKnobs `yaml:"runtime"`

	// Sampling defaults and thinking policy.
	Sampling Sampling `yaml:"sampling"`
	Thinking string   `yaml:"thinking_policy"` // off | auto | always

	// Measured is the throughput record that justified the numbers above.
	Measured *Measurement `yaml:"measured,omitempty"`
}

// Hardware describes the machine a profile targets.
type Hardware struct {
	GPU     string `yaml:"gpu,omitempty"`
	VRAMMB  int    `yaml:"vram_mb,omitempty"`
	RAMMB   int    `yaml:"ram_mb,omitempty"`
	CPU     string `yaml:"cpu,omitempty"`
	Threads int    `yaml:"threads,omitempty"`
}

// RuntimeKnobs are the llama-server flags a profile sets.
type RuntimeKnobs struct {
	Threads      int      `yaml:"threads,omitempty"`
	ThreadsBatch int      `yaml:"threads_batch,omitempty"`
	GPULayers    int      `yaml:"gpu_layers,omitempty"`
	BatchSize    int      `yaml:"batch_size,omitempty"`
	UBatchSize   int      `yaml:"ubatch_size,omitempty"`
	FlashAttn    bool     `yaml:"flash_attn,omitempty"`
	CacheTypeK   string   `yaml:"cache_type_k,omitempty"`
	CacheTypeV   string   `yaml:"cache_type_v,omitempty"`
	Slots        int      `yaml:"slots,omitempty"`
	ExtraArgs    []string `yaml:"extra_args,omitempty"`
}

// Sampling holds generation defaults.
type Sampling struct {
	Temperature float64 `yaml:"temperature"`
	TopP        float64 `yaml:"top_p"`
	TopK        int     `yaml:"top_k"`
	MinP        float64 `yaml:"min_p"`
	RepeatPen   float64 `yaml:"repeat_penalty,omitempty"`
	Seed        int     `yaml:"seed,omitempty"`
}

// Measurement is what `le models bench` writes (§9.2).
type Measurement struct {
	Model            string  `yaml:"model"`
	Quant            string  `yaml:"quant,omitempty"`
	PrefillTokensSec float64 `yaml:"prefill_tokens_per_second"`
	DecodeTokensSec  float64 `yaml:"decode_tokens_per_second"`
	LoadSeconds      float64 `yaml:"load_seconds"`
	PeakVRAMMB       int     `yaml:"peak_vram_mb"`
	PeakRAMMB        int     `yaml:"peak_ram_mb"`
	MeasuredAt       string  `yaml:"measured_at"`
	Host             string  `yaml:"host,omitempty"`
}

// FallbackProfile is used when no profile file is present. It is deliberately
// conservative and is NOT a tuned configuration: `le doctor` warns whenever it
// is in force, because §9.3 wants real values to come from a measurement.
func FallbackProfile() Profile {
	return Profile{
		Name:        "fallback",
		Description: "Conservative defaults used when no profile is loaded. Run `le models bench` to generate a real one.",
		// A 32k window with a 6k packet cap fits every candidate model in the
		// design and leaves room for output. The needle test sets the real cap
		// per model profile (§8.3).
		ContextTokens:   32768,
		MaxPacketTokens: 6000,
		ReservedOutput:  4096,
		ToolSurfaceMax:  12,
		Concurrency:     1,
		Sampling:        Sampling{Temperature: 0.2, TopP: 0.95, TopK: 40, MinP: 0.05},
		Thinking:        "auto",
	}
}

// Validate checks internal consistency.
func (p Profile) Validate() error {
	if p.Name == "" {
		return errors.New("profile: name is required")
	}
	if p.ContextTokens <= 0 {
		return fmt.Errorf("profile %s: context_tokens must be positive", p.Name)
	}
	if p.MaxPacketTokens <= 0 {
		return fmt.Errorf("profile %s: max_packet_tokens must be positive", p.Name)
	}
	if p.MaxPacketTokens+p.ReservedOutput > p.ContextTokens {
		return fmt.Errorf("profile %s: max_packet_tokens (%d) plus reserved_output_tokens (%d) exceeds context_tokens (%d)",
			p.Name, p.MaxPacketTokens, p.ReservedOutput, p.ContextTokens)
	}
	switch p.Thinking {
	case "off", "auto", "always", "":
	default:
		return fmt.Errorf("profile %s: thinking_policy %q is not one of off, auto, always", p.Name, p.Thinking)
	}
	return nil
}

// FitsHost reports whether the profile's measured peak memory fits the host,
// and the warning text when it does not (§9.2: "`le doctor` warns when the
// active profile's measured memory does not fit the host").
func (p Profile) FitsHost(vramMB, ramMB int) (bool, string) {
	var problems []string
	if p.PeakVRAMMB > 0 && vramMB > 0 && p.PeakVRAMMB > vramMB {
		problems = append(problems, fmt.Sprintf("profile needs %d MB VRAM, host has %d MB", p.PeakVRAMMB, vramMB))
	}
	if p.PeakRAMMB > 0 && ramMB > 0 && p.PeakRAMMB > ramMB {
		problems = append(problems, fmt.Sprintf("profile needs %d MB RAM, host has %d MB", p.PeakRAMMB, ramMB))
	}
	if len(problems) == 0 {
		return true, ""
	}
	return false, strings.Join(problems, "; ")
}

// Embedded reports the names of the profiles compiled into this binary.
func Embedded() []string {
	entries, err := profiles.FS.ReadDir(".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(out)
	return out
}

// LoadEmbeddedProfile reads a profile compiled into the binary.
func LoadEmbeddedProfile(name string) (Profile, error) {
	body, err := profiles.FS.ReadFile(name + ".yaml")
	if err != nil {
		return Profile{}, fmt.Errorf("config: no embedded profile %q", name)
	}
	return parseProfile(name, body, "embedded:"+name+".yaml")
}

// LoadProfile reads one profile by name from a directory, falling back to the
// embedded set. An on-disk profile always wins: a measurement from `le models
// bench` must override a shipped starting point of the same name.
func LoadProfile(dir, name string) (Profile, error) {
	path := filepath.Join(dir, name+".yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LoadEmbeddedProfile(name)
		}
		return Profile{}, fmt.Errorf("config: read profile %s: %w", path, err)
	}
	return parseProfile(name, body, path)
}

func parseProfile(name string, body []byte, source string) (Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("config: parse profile %s: %w", source, err)
	}
	if p.Name == "" {
		p.Name = name
	}
	return p, p.Validate()
}

// SaveProfile writes a profile, which is how `le models bench` records a
// measurement as a reusable configuration.
func SaveProfile(dir string, p Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	body, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	header := "# Hardware profile (design v3 §9.2). Generated by `le models bench`.\n" +
		"# Every value here is configuration, not code: §9.3 forbids hardcoding any of it.\n"
	path := filepath.Join(dir, p.Name+".yaml")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), body...), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ListProfiles enumerates the profiles available in a directory, merged with
// the embedded set so that every install shape sees the shipped profiles.
func ListProfiles(dir string) ([]string, error) {
	seen := map[string]bool{}
	for _, n := range Embedded() {
		seen[n] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		seen[strings.TrimSuffix(e.Name(), ".yaml")] = true
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// OnDisk reports whether a profile name resolves to a file rather than to the
// embedded set, so `le config profiles` can say which is which.
func OnDisk(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name+".yaml"))
	return err == nil
}
