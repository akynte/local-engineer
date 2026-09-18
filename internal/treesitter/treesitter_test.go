package treesitter_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/treesitter"
)

func names(symbols []treesitter.Symbol) []string {
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, s.Name)
	}
	return out
}

const goSource = `package risk

type Checker struct{ limit int }

type Limiter struct{}

func (c *Checker) Check(ctx context.Context, o Order) error {
	return nil
}

func (l Limiter) Check(o Order) bool { return true }

func Free(a, b string) (int, error) { return 0, nil }
`

// Every Check in a repository being the same symbol would report obligations
// for the callers of all of them on a change to one.
func TestGoMethodsAreQualifiedByTheirReceiver(t *testing.T) {
	symbols, err := treesitter.Symbols("risk/order.go", []byte(goSource))
	if err != nil {
		t.Fatal(err)
	}
	got := names(symbols)
	for _, want := range []string{"Checker.Check", "Limiter.Check", "Free", "Checker", "Limiter"} {
		if !slices.Contains(got, want) {
			t.Fatalf("%s missing from %v", want, got)
		}
	}
	if slices.Contains(got, "Check") {
		t.Fatalf("an unqualified method name survived: %v", got)
	}
}

const rustSource = `pub struct Checker { limit: u32 }

pub trait Risk {
    fn check(&self, order: &Order) -> Result<(), Error>;
}

impl Checker {
    pub fn check(&self, order: &Order) -> Result<(), Error> {
        Ok(())
    }

    fn internal(&self) -> u32 { self.limit }
}

pub fn free(a: &str, b: &str) -> Result<u32, Error> { Ok(0) }
`

// Rust had no signature detection at all: an attempt could change a public
// function and the obligations check would find nothing because it never
// looked.
func TestRustDeclarationsAndImplQualification(t *testing.T) {
	symbols, err := treesitter.Symbols("risk/order.rs", []byte(rustSource))
	if err != nil {
		t.Fatal(err)
	}
	got := names(symbols)
	for _, want := range []string{"Checker", "Risk", "Checker.check", "free"} {
		if !slices.Contains(got, want) {
			t.Fatalf("%s missing from %v", want, got)
		}
	}
}

func TestRustSignatureChangeIsDetectedAndAReformatIsNot(t *testing.T) {
	changedParam := strings.Replace(rustSource,
		"pub fn free(a: &str, b: &str) -> Result<u32, Error>",
		"pub fn free(a: &str, b: &str, c: u8) -> Result<u32, Error>", 1)
	changed, err := treesitter.ChangedSignatures("risk/order.rs", []byte(rustSource), []byte(changedParam))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changed, "free") {
		t.Fatalf("a new parameter was not reported: %v", changed)
	}

	// Reformatting must not manufacture an obligation for every caller.
	reformatted := strings.Replace(rustSource,
		"pub fn free(a: &str, b: &str) -> Result<u32, Error>",
		"pub fn free(\n    a: &str,\n    b: &str,\n) -> Result<u32, Error>", 1)
	changed, err = treesitter.ChangedSignatures("risk/order.rs", []byte(rustSource), []byte(reformatted))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(changed, "free") {
		t.Fatalf("a line break was reported as a signature change: %v", changed)
	}

	// A body rewritten without touching the signature is not an obligation:
	// that distinction is what the whole mechanism rests on.
	body := strings.Replace(rustSource, "fn internal(&self) -> u32 { self.limit }", "fn internal(&self) -> u32 { self.limit * 2 }", 1)
	changed, err = treesitter.ChangedSignatures("risk/order.rs", []byte(rustSource), []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("an implementation change was reported as a signature change: %v", changed)
	}
}

const tsSource = `export interface Order { id: string }

export class Checker {
  private limit: number

  check(order: Order): boolean {
    return true
  }
}

export function free(a: string, b: string): number {
  return 0
}

export type Verdict = "pass" | "fail"
`

func TestTypeScriptDeclarationsAndClassQualification(t *testing.T) {
	symbols, err := treesitter.Symbols("web/risk.ts", []byte(tsSource))
	if err != nil {
		t.Fatal(err)
	}
	got := names(symbols)
	for _, want := range []string{"Order", "Checker", "Checker.check", "free", "Verdict"} {
		if !slices.Contains(got, want) {
			t.Fatalf("%s missing from %v", want, got)
		}
	}
}

func TestTypeScriptReturnTypeChangeIsASignatureChange(t *testing.T) {
	changed, err := treesitter.ChangedSignatures("web/risk.ts",
		[]byte(tsSource),
		[]byte(strings.Replace(tsSource, "check(order: Order): boolean", "check(order: Order): Promise<boolean>", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changed, "Checker.check") {
		t.Fatalf("a changed return type was not reported: %v", changed)
	}
}

// A caller of something that no longer exists is as broken as a caller of
// something whose parameters moved.
func TestAddedAndRemovedDeclarationsBothCount(t *testing.T) {
	without := strings.Replace(goSource, "func Free(a, b string) (int, error) { return 0, nil }\n", "", 1)
	changed, err := treesitter.ChangedSignatures("risk/order.go", []byte(goSource), []byte(without))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changed, "Free") {
		t.Fatalf("a removed declaration was not reported: %v", changed)
	}
	changed, err = treesitter.ChangedSignatures("risk/order.go", []byte(without), []byte(goSource))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(changed, "Free") {
		t.Fatalf("an added declaration was not reported: %v", changed)
	}
}

// §11.1's primary_symbol: a compiler error that moves from one line of a
// function to another is the same failure, and this is what says so.
func TestEnclosingDeclarationNamesTheInnermostSymbol(t *testing.T) {
	// Line 8 is inside Checker.Check's body.
	got, ok := treesitter.EnclosingDeclaration("risk/order.go", []byte(goSource), 8)
	if !ok {
		t.Fatal("no enclosing declaration for a line inside a method")
	}
	if got.Name != "Checker.Check" {
		t.Fatalf("enclosing declaration is %q, want Checker.Check", got.Name)
	}
	if got.StartLine < 1 || got.EndLine < got.StartLine {
		t.Fatalf("line range is wrong: %+v", got)
	}

	// Rust: the method inside the impl block, not the impl block.
	got, ok = treesitter.EnclosingDeclaration("risk/order.rs", []byte(rustSource), 9)
	if !ok || got.Name != "Checker.check" {
		t.Fatalf("rust enclosing declaration is %q (ok=%v), want Checker.check", got.Name, ok)
	}

	// A line between declarations has no enclosing symbol, and saying so is
	// better than naming the nearest one.
	if _, ok := treesitter.EnclosingDeclaration("risk/order.go", []byte(goSource), 2); ok {
		t.Fatal("a blank line between declarations was attributed to one")
	}
}

// A file mid-edit is usually not valid source, and that is exactly when this is
// asked. tree-sitter is error-tolerant; the wrapper must not discard what it
// recovered.
func TestBrokenSourceStillYieldsWhatParsed(t *testing.T) {
	broken := "package risk\n\nfunc Good() error { return nil }\n\nfunc Broken(a int {\n"
	symbols, err := treesitter.Symbols("risk/order.go", []byte(broken))
	if err != nil {
		t.Fatalf("a parse error became a hard failure: %v", err)
	}
	if !slices.Contains(names(symbols), "Good") {
		t.Fatalf("declarations before the syntax error were lost: %v", names(symbols))
	}
}

func TestUnsupportedFilesSaySoRatherThanReturningNothing(t *testing.T) {
	// Vue has no Go module for its grammar, and an empty result would read as
	// "this file declares nothing".
	if treesitter.Supports("web/App.vue") {
		t.Fatal("Vue is reported as supported; no grammar is compiled in")
	}
	if _, err := treesitter.Symbols("web/App.vue", []byte("<template></template>")); err == nil {
		t.Fatal("an unsupported file returned an empty symbol list instead of an error")
	}
	if _, err := treesitter.ChangedSignatures("notes.md", []byte("a"), []byte("b")); err == nil {
		t.Fatal("an unsupported file reported no signature changes instead of an error")
	}
	if _, ok := treesitter.EnclosingDeclaration("notes.md", []byte("a"), 1); ok {
		t.Fatal("an unsupported file produced an enclosing declaration")
	}
}

func TestLanguagesReportsWhatThisBuildCanParse(t *testing.T) {
	got := treesitter.Languages()
	for _, want := range []string{".go", ".rs", ".ts", ".tsx"} {
		if !slices.Contains(got, want) {
			t.Fatalf("%s is not in %v", want, got)
		}
	}
}

// The parser holds C memory. A leak in a long-running supervisor is measured in
// gigabytes, so this exercises the path many times under -race.
func TestRepeatedParsingIsSafeAndConcurrent(t *testing.T) {
	done := make(chan error, 8)
	for range 8 {
		go func() {
			for range 25 {
				if _, err := treesitter.Symbols("risk/order.go", []byte(goSource)); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
