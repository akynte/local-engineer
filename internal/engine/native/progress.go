package native

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// repeatLimit is how many times the same call may return the same answer
// before the attempt is ended.
//
// Two is ordinary: a model re-reads a file it edited, or checks something it
// half-remembers. Three identical calls returning identical bytes is not
// checking, it is a loop, and every further step spends the budget to arrive
// back where it started.
const repeatLimit = 3

// staleLimit is how many consecutive steps may pass with no new information
// before the attempt is ended.
//
// A step is stale when every tool call in it was one already made, answered
// identically. A model can be stale once and recover — it re-reads two files
// then edits. Three steps in a row learning nothing means the strategy is not
// going to produce anything, and the remaining budget is better spent telling
// the operator what happened than on more of it.
const staleLimit = 3

// progress tracks whether a tool loop is still learning anything.
//
// The step limit already bounds cost, but it bounds it at the wrong place: a
// model that reads the same file sixty times costs sixty steps and produces
// the same report as one that gave up at three, except later and with a larger
// bill. This distinguishes "working" from "stuck" while there is still budget
// left to say so.
type progress struct {
	// seen maps a call fingerprint to the digest of the answer it last gave,
	// and how many times that exact pair has occurred.
	seen map[string]*record
	// stale counts consecutive steps in which nothing new was learned.
	stale int
}

type record struct {
	digest string
	count  int
}

func newProgress() *progress { return &progress{seen: map[string]*record{}} }

// verdict is what a single tool call was worth.
type verdict int

const (
	// learned: the call was new, or answered differently than last time.
	learned verdict = iota
	// repeated: the same call, the same answer, again.
	repeated
)

// observe records one call and its result, and says whether anything was
// learned. times is how many times this exact call-and-answer has now occurred.
func (p *progress) observe(name, arguments, content string) (v verdict, times int) {
	fp := fingerprint(name, arguments)
	d := digest(content)
	r, ok := p.seen[fp]
	if !ok {
		p.seen[fp] = &record{digest: d, count: 1}
		return learned, 1
	}
	if r.digest != d {
		// The same question with a different answer is real information: the
		// file changed, the build now fails differently. Counting it as a
		// repeat would punish the model for checking its own work.
		r.digest, r.count = d, 1
		return learned, 1
	}
	r.count++
	return repeated, r.count
}

// endOfStep closes a step. learnedAnything is whether any call in it, or an
// edit, produced something new.
func (p *progress) endOfStep(learnedAnything bool) {
	if learnedAnything {
		p.stale = 0
		return
	}
	p.stale++
}

// stuck reports whether the loop should be ended, and why.
//
// The reason is written for the operator reading the task's summary, so it
// names the actions rather than reporting a counter.
func (p *progress) stuck() (bool, string) {
	if p.stale >= staleLimit {
		return true, fmt.Sprintf(
			"stopped after %d consecutive steps that learned nothing new: %s",
			p.stale, p.topRepeats())
	}
	for fp, r := range p.seen {
		if r.count >= repeatLimit {
			return true, fmt.Sprintf(
				"stopped after calling %s %d times with the same arguments and getting "+
					"the same answer every time", describeCall(fp), r.count)
		}
	}
	return false, ""
}

// topRepeats names the calls the loop kept making, most repeated first.
func (p *progress) topRepeats() string {
	type entry struct {
		call string
		n    int
	}
	var es []entry
	for fp, r := range p.seen {
		if r.count > 1 {
			es = append(es, entry{describeCall(fp), r.count})
		}
	}
	if len(es) == 0 {
		return "no tool call produced a new answer"
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].n != es[j].n {
			return es[i].n > es[j].n
		}
		return es[i].call < es[j].call
	})
	var parts []string
	for i, e := range es {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("and %d other repeated call(s)", len(es)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", e.call, e.n))
	}
	return strings.Join(parts, ", ")
}

// note is what the supervisor tells the model when it repeats itself.
//
// It is returned separately from the tool result because it is the supervisor
// speaking, not the repository: it must sit outside the untrusted fence, or the
// model is being told to treat its own supervisor's warning as data.
func repeatNote(name string, times int) string {
	return fmt.Sprintf(
		"[supervisor] You have now called %s with these exact arguments %d times and "+
			"received the same answer each time. You already have this result above. "+
			"Repeating it will not produce new information — either act on what you have, "+
			"or try something different.\n", name, times)
}

// fingerprint identifies a call by name and arguments, canonicalised so that
// two calls differing only in key order or spacing are recognised as the same.
func fingerprint(name, arguments string) string {
	canon := strings.TrimSpace(arguments)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err == nil {
		// Go marshals map keys in sorted order, which is the canonical form
		// wanted here.
		if b, err := json.Marshal(parsed); err == nil {
			canon = string(b)
		}
	}
	return name + "\x00" + canon
}

// describeCall renders a fingerprint back into something readable.
func describeCall(fp string) string {
	name, args, ok := strings.Cut(fp, "\x00")
	if !ok {
		return fp
	}
	if len(args) > 80 {
		args = args[:80] + "…"
	}
	return name + args
}

// digest keeps a fixed-size fingerprint of a result rather than the result, so
// a long tool loop does not retain every file it read.
func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
