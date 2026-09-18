package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/treesitter"
	"github.com/akynte/local-engineer/internal/worktree"
)

var noise = []*regexp.Regexp{
	regexp.MustCompile(`0x[0-9a-fA-F]+`),
	regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ms|µs|ns|s)\b`),
	regexp.MustCompile(`\b\d{4}-\d\d-\d\d[T ][0-9:.+Z-]+`),
	regexp.MustCompile(`goroutine \d+`),
	regexp.MustCompile(`/tmp/[^/\s]+`),
}

func Normalize(text, root string) string {
	if root != "" {
		text = strings.ReplaceAll(text, root+"/", "")
	}
	for _, re := range noise {
		text = re.ReplaceAllString(text, "<volatile>")
	}
	return strings.Join(strings.Fields(text), " ")
}

// Fingerprint hashes what identifies a failure. FingerprintWithSymbols is the
// same computation when the caller also wants the symbols it resolved.
func Fingerprint(r recipe.Result, root string) string {
	fp, _ := FingerprintWithSymbols(r, root)
	return fp
}

// FingerprintWithSymbols returns the fingerprint and §11.1's primary symbols.
//
// Line numbers stay in the hash, as §11.1 specifies, so this does not make an
// error that moved down the file hash the same — nothing here claims it does.
// What the symbol adds is discrimination and a usable query: two identical
// messages in two functions of one file are different failures, and a repeated
// failure routed back to localization arrives naming a declaration rather than
// a line.
func FingerprintWithSymbols(r recipe.Result, root string) (string, []string) {
	findings := append([]recipe.Finding(nil), r.Summary.Findings...)
	for i := range findings {
		findings[i].File = Normalize(findings[i].File, root)
		findings[i].Message = Normalize(findings[i].Message, root)
	}
	sort.Slice(findings, func(i, j int) bool {
		a, _ := json.Marshal(findings[i])
		b, _ := json.Marshal(findings[j])
		return string(a) < string(b)
	})
	// §11.1's primary_symbol: the declaration each finding sits inside, so the
	// same compiler error is the same failure after an edit moves it down the
	// file. Without it, inserting a line above a broken function makes the
	// failure look new, the ladder treats it as progress, and the attempt that
	// changed nothing gets another turn.

	symbols := make([]string, 0, len(findings))
	seen := map[string]bool{}
	for _, f := range findings {
		symbol, ok := enclosingSymbol(root, f)
		if !ok || seen[symbol] {
			continue
		}
		seen[symbol] = true
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)

	body, _ := json.Marshal(struct {
		Tool           string
		Status         recipe.Status
		Exit           int
		Summary, Error string
		Findings       []recipe.Finding
		PrimarySymbols []string
	}{r.Recipe, r.Status, r.ExitCode, Normalize(r.Summary.Headline, root), Normalize(r.Err, root), findings, symbols})
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:]), symbols
}

func Environmental(r recipe.Result) bool {
	text := strings.ToLower(r.Err + " " + r.Summary.Headline)
	for _, f := range r.Summary.Findings {
		text += " " + strings.ToLower(f.Message)
	}
	for _, pattern := range []string{"no space left", "network unreachable", "network is unreachable", "address already in use", "module lookup disabled", "module not found in cache", "executable file not found", "permission denied"} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return r.Status == recipe.Error
}

func Regression(r recipe.Result, baseline []recipe.Result) bool {
	if r.Status != recipe.Fail {
		return false
	}
	for _, old := range baseline {
		if old.Recipe == r.Recipe {
			for id, status := range r.Summary.Tests {
				if status == recipe.Fail && old.Summary.Tests[id] == recipe.Pass {
					return true
				}
			}
		}
		if old.Recipe == r.Recipe && old.Status == recipe.Pass {
			return true
		}
	}
	return false
}

type FailureClass string

const (
	New         FailureClass = "NEW"
	Same        FailureClass = "SAME"
	Regressed   FailureClass = "REGRESSION"
	Environment FailureClass = "ENVIRONMENTAL"
	Flaky       FailureClass = "FLAKY"
)

type FailureRecord struct {
	Fingerprint string       `json:"fingerprint"`
	Class       FailureClass `json:"class"`
	Recipe      string       `json:"recipe"`
	Candidate   string       `json:"candidate"`
	// Symbols are the declarations the findings sit inside, §11.1's
	// primary_symbol. They are kept on the record and not only folded into the
	// hash because §11.2 sends a repeated failure back to localization "with
	// the failure as the query", and `Checker.Check` is a query the index can
	// answer where `order.go:412` is a line that has already moved.
	Symbols []string `json:"symbols,omitempty"`
}

func Classify(r recipe.Result, baseline []recipe.Result, seen map[string]int, root string) FailureRecord {
	fp, symbols := FingerprintWithSymbols(r, root)
	class := New
	if Regression(r, baseline) {
		class = Regressed
	}
	if seen[fp] > 0 {
		class = Same
	}
	if Environmental(r) {
		class = Environment
	}
	return FailureRecord{Fingerprint: fp, Class: class, Recipe: r.Recipe, Candidate: r.Candidate, Symbols: symbols}
}

// enclosingSymbol resolves a finding's location to the declaration containing
// it.
//
// Best-effort, and deliberately so: a finding with no file, a language with no
// grammar, or a file that has since been deleted all contribute nothing rather
// than failing the fingerprint. The fingerprint is still well-defined without a
// symbol — it is simply less able to tell a moved error from a new one.
func enclosingSymbol(root string, f recipe.Finding) (string, bool) {
	if f.File == "" || f.Line <= 0 || !treesitter.Supports(f.File) {
		return "", false
	}
	body, err := worktree.ReadWithin(root, f.File)
	if err != nil {
		return "", false
	}
	symbol, ok := treesitter.EnclosingDeclaration(f.File, body, f.Line)
	if !ok {
		return "", false
	}
	return f.File + ":" + symbol.Name, true
}
