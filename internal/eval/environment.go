package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Environment is what a result needs to carry to be reproducible.
//
// A benchmark number without it is an anecdote: the same tasks on the same code
// with a different quantisation, a different llama.cpp build or a different
// card produce different numbers, and a reader cannot tell which they are
// looking at. Everything here is captured rather than declared, so it cannot
// drift from what actually ran. Fields that could not be determined are empty
// and reported as unknown rather than filled with a plausible value.
type Environment struct {
	CapturedAt time.Time `json:"captured_at"`

	// Commit and Dirty pin the code under test. A dirty tree means the result
	// is not reproducible from the commit alone, and that is stated rather
	// than hidden.
	Commit    string `json:"le_commit"`
	Dirty     bool   `json:"le_dirty"`
	GoVersion string `json:"go_version"`

	// Model identifies what produced the tokens.
	Model        string `json:"model,omitempty"`
	ModelSHA256  string `json:"model_sha256,omitempty"`
	Quantization string `json:"quantization,omitempty"`
	Profile      string `json:"profile,omitempty"`

	// Runtime is the inference server and the settings that change results.
	RuntimeName     string   `json:"runtime_name,omitempty"`
	RuntimeVersion  string   `json:"runtime_version,omitempty"`
	RuntimeArgs     []string `json:"runtime_args,omitempty"`
	ContextTokens   int      `json:"context_tokens,omitempty"`
	CacheTypeK      string   `json:"cache_type_k,omitempty"`
	CacheTypeV      string   `json:"cache_type_v,omitempty"`
	Temperature     float64  `json:"temperature,omitempty"`
	TopP            float64  `json:"top_p,omitempty"`
	TopK            int      `json:"top_k,omitempty"`
	ReasoningTokens int      `json:"reasoning_tokens,omitempty"`

	// Hardware is why a wall-clock number means anything.
	GPU     string `json:"gpu,omitempty"`
	VRAMMiB int    `json:"vram_mib,omitempty"`
	CPU     string `json:"cpu,omitempty"`
	CPUs    int    `json:"cpus,omitempty"`
	RAMMiB  int    `json:"ram_mib,omitempty"`
	OS      string `json:"os,omitempty"`
	Kernel  string `json:"kernel,omitempty"`

	// Seed makes the report's resampling reproducible. It does not make the
	// model deterministic, and the distinction is stated so nobody reads a
	// fixed seed as a fixed result.
	Seed int64 `json:"seed"`
}

// Complete reports whether the fields a reproduction actually needs are present.
//
// Model and runtime identity are required because a result cannot be repeated
// without them. Hardware is required because a timing without it is not a
// measurement of anything. The commit is required because the code changed.
func (e Environment) Complete() bool { return len(e.Missing()) == 0 }

// Missing names the fields a reproduction would need and does not have.
func (e Environment) Missing() []string {
	var out []string
	for name, present := range map[string]bool{
		"le_commit":       e.Commit != "",
		"model":           e.Model != "",
		"runtime_version": e.RuntimeVersion != "",
		"hardware_cpu":    e.CPU != "" || e.CPUs > 0,
		"hardware_gpu":    e.GPU != "",
	} {
		if !present {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CaptureEnvironment reads what it can from the machine and the repository.
//
// Every probe is best-effort: a benchmark must not fail because nvidia-smi is
// absent. What a probe cannot answer is left empty, and Missing reports it.
func CaptureEnvironment(ctx context.Context, seed int64) Environment {
	e := Environment{CapturedAt: time.Now().UTC(), Seed: seed,
		GoVersion: runtime.Version(), OS: runtime.GOOS, CPUs: runtime.NumCPU()}

	if out, err := probe(ctx, "git", "rev-parse", "HEAD"); err == nil {
		e.Commit = strings.TrimSpace(out)
	}
	if out, err := probe(ctx, "git", "status", "--porcelain"); err == nil {
		e.Dirty = strings.TrimSpace(out) != ""
	}
	if out, err := probe(ctx, "uname", "-r"); err == nil {
		e.Kernel = strings.TrimSpace(out)
	}
	if out, err := probe(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader"); err == nil {
		if name, mem, ok := strings.Cut(strings.TrimSpace(out), ","); ok {
			e.GPU = strings.TrimSpace(name)
			// A GPU whose size could not be parsed is still a GPU worth
			// recording, so the name is kept and the size stays zero, which
			// Rows renders as "not recorded".
			if _, err := fmt.Sscanf(strings.TrimSpace(mem), "%d", &e.VRAMMiB); err != nil {
				e.VRAMMiB = 0
			}
		}
	}
	if body, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			if name, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "model name" {
				e.CPU = strings.TrimSpace(value)
				break
			}
		}
	}
	if body, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				var kb int
				if _, err := fmt.Sscanf(line, "MemTotal: %d kB", &kb); err == nil {
					e.RAMMiB = kb / 1024
				}
				break
			}
		}
	}
	return e
}

// WithModel records the model identity, hashing the file when it is one.
//
// The hash matters more than the name: two files called the same thing at
// different quantisations are different models, and only the bytes say which
// one produced a number.
func (e Environment) WithModel(model, quantization, profile string) Environment {
	e.Model = model
	e.Quantization = quantization
	e.Profile = profile
	if info, err := os.Stat(model); err == nil && info.Mode().IsRegular() {
		if sum, err := fileSHA256(model); err == nil {
			e.ModelSHA256 = sum
		}
	}
	return e
}

// WithRuntime records the inference server behind the endpoint.
func (e Environment) WithRuntime(name, version string, args []string) Environment {
	e.RuntimeName, e.RuntimeVersion = name, version
	e.RuntimeArgs = append([]string(nil), args...)
	return e
}

func probe(ctx context.Context, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	//nolint:gosec // fixed probe commands, never caller input
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// fileSHA256 hashes a file without reading it all into memory.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Rows renders the environment as ordered label/value pairs for a report.
//
// A field that was not captured is shown as "not recorded" rather than left
// out. An absent row reads as "this did not apply"; an explicit gap reads as
// "nobody wrote this down", which is what it actually means and is the thing a
// reader needs to know before trusting a comparison across two runs.
func (e Environment) Rows() [][2]string {
	dirty := ""
	if e.Dirty {
		dirty = " (working tree dirty — these numbers are not from a committed state)"
	}
	return [][2]string{
		{"Captured", e.CapturedAt.Format(time.RFC3339)},
		{"Local Engineer commit", value(e.Commit) + dirty},
		{"Go", value(e.GoVersion)},
		{"Model", value(e.Model)},
		{"Model SHA-256", value(e.ModelSHA256)},
		{"Quantization", value(e.Quantization)},
		{"Runtime", strings.TrimSpace(e.RuntimeName + " " + e.RuntimeVersion)},
		{"Runtime arguments", value(strings.Join(e.RuntimeArgs, " "))},
		{"Context tokens", numberValue(e.ContextTokens)},
		{"KV cache type", strings.TrimSpace(e.CacheTypeK + " / " + e.CacheTypeV)},
		{"Sampling", fmt.Sprintf("temperature %.2f, top-p %.2f, top-k %d", e.Temperature, e.TopP, e.TopK)},
		{"Seed", numberValue(int(e.Seed))},
		{"GPU", value(e.GPU)},
		{"VRAM (MiB)", numberValue(e.VRAMMiB)},
		{"CPU", value(e.CPU)},
		{"Cores", numberValue(e.CPUs)},
		{"RAM (MiB)", numberValue(e.RAMMiB)},
		{"OS", value(e.OS)},
		{"Kernel", value(e.Kernel)},
	}
}

func value(s string) string {
	if strings.TrimSpace(s) == "" {
		return "not recorded"
	}
	return s
}

func numberValue(n int) string {
	if n == 0 {
		return "not recorded"
	}
	return fmt.Sprintf("%d", n)
}
