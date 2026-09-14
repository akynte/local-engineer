// Package memory is the durable external memory of design v3 §8.2, split into
// intent, observation and advice.
//
// The split is the point. The technique table justifies it as what "prevents
// stale rules and self-praise from steering", and §11 of the sources calls the
// underlying pattern "good, easy to abuse". Three properties follow, and each
// exists because leaving it out is how the abuse happens:
//
//   - Every note carries provenance. A rule with no source cannot be judged,
//     and a system that writes rules about itself will happily write flattering
//     ones. A note that cannot say where it came from is rejected.
//   - Kinds are not interchangeable. An observation is something that was seen
//     once; advice is a rule meant to steer future work. Letting an observation
//     drift into advice is exactly how a one-off becomes a law.
//   - There are caps. A playbook that grows without bound stops being read and
//     starts being pasted, and the oldest entries are the most likely to be
//     stale.
//
// Notes live in the repository under .le/memory/ so they travel with it
// (§2.2). There is no global store: cross-project sharing is `le lessons
// export/import`, which copies text a human has read.
package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Kind is what a note is for.
type Kind string

const (
	// KindIntent records why something is being done: the requirement behind a
	// task, a constraint the operator stated. It is the least volatile kind.
	KindIntent Kind = "intent"
	// KindObservation records something that was seen once — a test that is
	// flaky, a service that is slow to start. It is evidence, not a rule.
	KindObservation Kind = "observation"
	// KindAdvice is a rule meant to steer future work. It is the kind that can
	// do harm, so it is the kind with the strictest provenance requirement.
	KindAdvice Kind = "advice"
)

// Kinds lists every kind, in the order a reader should see them.
func Kinds() []Kind { return []Kind{KindIntent, KindObservation, KindAdvice} }

// Valid reports whether a kind is one this package handles.
func (k Kind) Valid() bool {
	for _, known := range Kinds() {
		if k == known {
			return true
		}
	}
	return false
}

// Provenance says where a note came from. A note without it is refused.
type Provenance struct {
	// Source is who or what produced the note: "operator", a task id, or an
	// import path. "model" is deliberately spellable — a note the model wrote
	// about its own work should be visibly that.
	Source string `yaml:"source" json:"source"`
	// Evidence is the id of the evidence that supports it, when there is any.
	// Advice without evidence is allowed but marked, because that is the
	// category most likely to be a guess.
	Evidence string `yaml:"evidence,omitempty" json:"evidence,omitempty"`
	// At is when it was recorded.
	At time.Time `yaml:"at" json:"at"`
}

// Note is one durable memory entry.
type Note struct {
	ID   string `yaml:"id" json:"id"`
	Kind Kind   `yaml:"kind" json:"kind"`
	// Text is what a reader sees. It is deliberately plain prose: a note that
	// needs a schema to interpret is a data structure wearing a note's clothes.
	Text       string     `yaml:"text" json:"text"`
	Provenance Provenance `yaml:"provenance" json:"provenance"`
	// Tags are optional, for filtering.
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Caps bound the store. §419 names the failure they prevent.
type Caps struct {
	// PerKind is the most notes of one kind that are kept. The oldest are
	// dropped first: a playbook's oldest entries are the most likely to be
	// stale, and an unbounded one stops being read.
	PerKind int `yaml:"per_kind"`
	// MaxTextBytes bounds one note. A note longer than this is an essay, and
	// an essay in a prompt is the cost this whole design exists to avoid.
	MaxTextBytes int `yaml:"max_text_bytes"`
}

// DefaultCaps are deliberately small.
func DefaultCaps() Caps { return Caps{PerKind: 50, MaxTextBytes: 1000} }

// ErrNoProvenance is returned for a note that cannot say where it came from.
var ErrNoProvenance = errors.New("memory: a note must record where it came from")

// ErrTooLong is returned for a note over the cap.
var ErrTooLong = errors.New("memory: the note is longer than the cap")

// Validate rejects a note that cannot be judged or cannot be read.
func (n Note) Validate(caps Caps) error {
	if !n.Kind.Valid() {
		return fmt.Errorf("memory: %q is not one of intent, observation, advice", n.Kind)
	}
	if strings.TrimSpace(n.Text) == "" {
		return errors.New("memory: a note needs text")
	}
	if caps.MaxTextBytes > 0 && len(n.Text) > caps.MaxTextBytes {
		return fmt.Errorf("%w: %d bytes, cap is %d", ErrTooLong, len(n.Text), caps.MaxTextBytes)
	}
	if strings.TrimSpace(n.Provenance.Source) == "" {
		return ErrNoProvenance
	}
	return nil
}

// file is the on-disk shape, one per kind.
type file struct {
	Notes []Note `yaml:"notes"`
}

// Store is a workspace's memory, held in the repository.
type Store struct {
	dir  string
	caps Caps
}

// Open binds a store to a repository root. The directory is created on write,
// not here, so reading a repository with no memory is not a side effect.
func Open(repoRoot string, caps Caps) *Store {
	if caps.PerKind <= 0 {
		caps.PerKind = DefaultCaps().PerKind
	}
	if caps.MaxTextBytes <= 0 {
		caps.MaxTextBytes = DefaultCaps().MaxTextBytes
	}
	return &Store{dir: filepath.Join(repoRoot, ".le", "memory"), caps: caps}
}

// Dir reports where notes are kept, for diagnostics.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(k Kind) string { return filepath.Join(s.dir, string(k)+".yaml") }

// List returns the notes of one kind, oldest first.
func (s *Store) List(k Kind) ([]Note, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("memory: %q is not a kind", k)
	}
	body, err := os.ReadFile(s.path(k))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f file
	if err := yaml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("memory: %s is not readable: %w", s.path(k), err)
	}
	for i := range f.Notes {
		f.Notes[i].Kind = k
	}
	sort.SliceStable(f.Notes, func(i, j int) bool {
		return f.Notes[i].Provenance.At.Before(f.Notes[j].Provenance.At)
	})
	return f.Notes, nil
}

// All returns every note, grouped by kind in reading order.
func (s *Store) All() (map[Kind][]Note, error) {
	out := map[Kind][]Note{}
	for _, k := range Kinds() {
		notes, err := s.List(k)
		if err != nil {
			return nil, err
		}
		out[k] = notes
	}
	return out, nil
}

// Add records a note, enforcing provenance and the caps.
func (s *Store) Add(n Note) (Note, error) {
	if n.Provenance.At.IsZero() {
		n.Provenance.At = time.Now().UTC().Truncate(time.Second)
	}
	if err := n.Validate(s.caps); err != nil {
		return Note{}, err
	}
	notes, err := s.List(n.Kind)
	if err != nil {
		return Note{}, err
	}
	if n.ID == "" {
		n.ID = newID(n.Kind, n.Provenance.At, len(notes))
	}
	for _, existing := range notes {
		if existing.ID == n.ID {
			return Note{}, fmt.Errorf("memory: a note with id %s already exists", n.ID)
		}
	}
	notes = append(notes, n)
	// Oldest first, so trimming from the front drops the stalest.
	if s.caps.PerKind > 0 && len(notes) > s.caps.PerKind {
		notes = notes[len(notes)-s.caps.PerKind:]
	}
	return n, s.write(n.Kind, notes)
}

// Remove deletes a note by id, reporting whether it was there.
func (s *Store) Remove(k Kind, id string) (bool, error) {
	notes, err := s.List(k)
	if err != nil {
		return false, err
	}
	out := notes[:0:0]
	var found bool
	for _, n := range notes {
		if n.ID == id {
			found = true
			continue
		}
		out = append(out, n)
	}
	if !found {
		return false, nil
	}
	return true, s.write(k, out)
}

func (s *Store) write(k Kind, notes []Note) error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return err
	}
	body, err := yaml.Marshal(file{Notes: notes})
	if err != nil {
		return err
	}
	header := "# " + string(k) + " notes (design v3 §8.2).\n" +
		"# Kept in the repository so they travel with it. Every note records where\n" +
		"# it came from: a rule with no source cannot be judged.\n"
	tmp := s.path(k) + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), body...), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(k))
}

func newID(k Kind, at time.Time, n int) string {
	return fmt.Sprintf("%s-%d-%02d", string(k)[:3], at.Unix(), n+1)
}
