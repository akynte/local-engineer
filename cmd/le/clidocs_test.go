package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The CLI reference is the page an operator reads to find out what exists. Two
// commands had shipped without reaching it — `le models needle` and
// `le models conformance` — and nothing noticed, because a missing row breaks
// no build and fails no test. Documentation that omits a command is the same
// defect class as documentation that promises one the code does not have; this
// catches the first, and the executable command blocks in docs/how-to catch
// the second.
func TestEveryCommandIsInTheCLIReference(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "reference", "cli.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)

	// Fenced blocks first: a regex for inline code pairs backticks, and a ```
	// fence leaves it out of step for the rest of the file — which is how the
	// first version of this test reported eighteen documented commands as
	// missing.
	doc = regexp.MustCompile("(?s)```.*?```").ReplaceAllString(doc, "")

	// The page is organised as one `## `le <group>`` section per command
	// group, with the subcommands in a table inside it. Checking a leaf name
	// against the whole page is too weak to be worth much: `show` and `list`
	// appear in half a dozen sections, so an undocumented `le memory list`
	// would pass on a mention of `le workspace list`. Each section is searched
	// on its own instead.
	sections := map[string]string{}
	headings := regexp.MustCompile("(?m)^## `le ([a-z-]+)`.*$")
	locs := headings.FindAllStringSubmatchIndex(doc, -1)
	for i, loc := range locs {
		end := len(doc)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		sections[doc[loc[2]:loc[3]]] = doc[loc[1]:end]
	}

	inline := regexp.MustCompile("`([^`]+)`")
	names := func(text string) map[string]bool {
		out := map[string]bool{}
		for _, m := range inline.FindAllStringSubmatch(text, -1) {
			for _, word := range strings.Fields(m[1]) {
				out[word] = true
			}
		}
		return out
	}
	anywhere := names(doc)

	for _, group := range newRootCmd().Commands() {
		if group.Hidden || group.Name() == "help" || group.Name() == "completion" {
			continue
		}
		section, documented := sections[group.Name()]
		if !documented {
			// A command with no subcommands needs no section of its own, only
			// a mention.
			if !group.HasSubCommands() {
				if !anywhere[group.Name()] {
					t.Errorf("`le %s` is not in docs/reference/cli.md", group.Name())
				}
				continue
			}
			t.Errorf("`le %s` has no section in docs/reference/cli.md, and it has "+
				"%d subcommands", group.Name(), len(group.Commands()))
			continue
		}
		listed := names(section)
		for _, sub := range group.Commands() {
			if sub.Hidden || sub.Name() == "help" {
				continue
			}
			if !listed[sub.Name()] {
				t.Errorf("`le %s %s` is missing from the `le %s` section of "+
					"docs/reference/cli.md", group.Name(), sub.Name(), group.Name())
			}
		}
	}
}
