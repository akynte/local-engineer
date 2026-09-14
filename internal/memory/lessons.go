package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Cross-project lessons (§2.2).
//
// "There is no global semantic memory. 'Cross-project lessons' are an explicit
// opt-in feature (`le lessons export/import`) that copies text you have read,
// never an automatic channel."
//
// Both halves of that sentence are load-bearing, and the implementation is
// shaped by the second one. Export writes a file a human can read. Import shows
// what it is about to copy and refuses to proceed without confirmation, because
// the moment it copies silently it has become the automatic channel the design
// rules out. The provenance of every imported note is rewritten to say it came
// from elsewhere, so a rule learned in another repository can never be mistaken
// for one this repository established.

// Bundle is the exported form.
type Bundle struct {
	// Version lets the format change without silently misreading an old file.
	Version int `yaml:"version"`
	// From identifies the workspace the notes were taken from, so an importer
	// can see whose lessons these are.
	From       string    `yaml:"from"`
	ExportedAt time.Time `yaml:"exported_at"`
	Notes      []Note    `yaml:"notes"`
}

// BundleVersion is the current format.
const BundleVersion = 1

// Export collects notes of the given kinds into a bundle. Kinds are chosen by
// the operator rather than fixed: intent is usually specific to one repository
// and rarely worth carrying, while advice is the kind people want to share.
func (s *Store) Export(from string, kinds []Kind) (Bundle, error) {
	if len(kinds) == 0 {
		kinds = []Kind{KindAdvice}
	}
	b := Bundle{Version: BundleVersion, From: from, ExportedAt: time.Now().UTC().Truncate(time.Second)}
	for _, k := range kinds {
		notes, err := s.List(k)
		if err != nil {
			return Bundle{}, err
		}
		b.Notes = append(b.Notes, notes...)
	}
	sort.SliceStable(b.Notes, func(i, j int) bool {
		if b.Notes[i].Kind != b.Notes[j].Kind {
			return b.Notes[i].Kind < b.Notes[j].Kind
		}
		return b.Notes[i].Provenance.At.Before(b.Notes[j].Provenance.At)
	})
	return b, nil
}

// WriteBundle writes a bundle as YAML with a header explaining what it is.
func WriteBundle(w io.Writer, b Bundle) error {
	body, err := yaml.Marshal(b)
	if err != nil {
		return err
	}
	header := "# Exported lessons (design v3 §2.2).\n" +
		"# This file is meant to be read before it is imported. Importing copies\n" +
		"# these notes into another repository's memory, where they will steer work.\n" +
		"# There is no automatic channel between projects; this file is the channel.\n"
	_, err = w.Write(append([]byte(header), body...))
	return err
}

// ReadBundle parses an exported file.
func ReadBundle(r io.Reader) (Bundle, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if err := yaml.Unmarshal(body, &b); err != nil {
		return Bundle{}, fmt.Errorf("memory: the bundle is not readable: %w", err)
	}
	if b.Version == 0 {
		return Bundle{}, errors.New("memory: the bundle declares no version")
	}
	if b.Version > BundleVersion {
		return Bundle{}, fmt.Errorf("memory: the bundle is version %d; this build understands %d",
			b.Version, BundleVersion)
	}
	return b, nil
}

// ErrNotConfirmed is returned when an import was not confirmed. It is a
// distinct error because the caller prints the notes and asks, and a refusal is
// an ordinary outcome rather than a fault.
var ErrNotConfirmed = errors.New("memory: the import was not confirmed")

// ImportOptions configures an import.
type ImportOptions struct {
	// Confirmed must be true. The parameter exists so that the refusal lives
	// here rather than in one caller: any future caller that forgets to ask
	// gets an error, not a silent copy.
	Confirmed bool
	// Kinds restricts what is taken. Empty takes everything in the bundle.
	Kinds []Kind
}

// Import copies a bundle's notes into this store.
//
// Every imported note's provenance is rewritten to record that it came from
// elsewhere. A rule learned in another repository must never read as one this
// repository established: that is the difference between a lesson and a rumour.
func (s *Store) Import(b Bundle, opts ImportOptions) ([]Note, error) {
	if !opts.Confirmed {
		return nil, ErrNotConfirmed
	}
	want := map[Kind]bool{}
	for _, k := range opts.Kinds {
		want[k] = true
	}

	var added []Note
	for _, n := range b.Notes {
		if len(want) > 0 && !want[n.Kind] {
			continue
		}
		origin := b.From
		if origin == "" {
			origin = "an unnamed export"
		}
		imported := Note{
			Kind: n.Kind,
			Text: n.Text,
			Tags: append(append([]string{}, n.Tags...), "imported"),
			Provenance: Provenance{
				// The source records both that this was imported and what it
				// claimed before, so the chain stays visible.
				Source:   "imported from " + origin + " (originally: " + n.Provenance.Source + ")",
				Evidence: n.Provenance.Evidence,
				At:       time.Now().UTC().Truncate(time.Second),
			},
		}
		stored, err := s.Add(imported)
		if err != nil {
			return added, fmt.Errorf("memory: importing %q: %w", truncate(n.Text, 40), err)
		}
		added = append(added, stored)
	}
	return added, nil
}

// Preview renders what an import would copy, for a human to read before
// confirming. This is the "text you have read" half of §2.2.
func Preview(b Bundle, kinds []Kind) string {
	want := map[Kind]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Bundle from %s, exported %s\n\n",
		orDefault(b.From, "(unnamed)"), b.ExportedAt.Format(time.RFC3339))

	var shown int
	for _, k := range Kinds() {
		var group []Note
		for _, n := range b.Notes {
			if n.Kind == k && (len(want) == 0 || want[k]) {
				group = append(group, n)
			}
		}
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%s (%d)\n", k, len(group))
		for _, n := range group {
			fmt.Fprintf(&sb, "  - %s\n", truncate(n.Text, 100))
			fmt.Fprintf(&sb, "    from %s\n", orDefault(n.Provenance.Source, "(no source)"))
			shown++
		}
		sb.WriteString("\n")
	}
	if shown == 0 {
		sb.WriteString("Nothing in this bundle matches the selected kinds.\n")
		return sb.String()
	}
	sb.WriteString("These will be copied into this repository's memory, where they steer\n" +
		"future work. Every one will be marked as imported, with its original source kept.\n")
	return sb.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// ExportTo writes a bundle to a path.
func ExportTo(path string, b Bundle) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	return WriteBundle(f, b)
}

// ImportFrom reads a bundle from a path.
func ImportFrom(path string) (Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return Bundle{}, err
	}
	defer f.Close()
	return ReadBundle(f)
}
