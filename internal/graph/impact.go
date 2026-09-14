package graph

import (
	"fmt"
	"sort"
)

// ChangeKind classifies what is being done to the changed nodes. The
// compatibility verdict of §3.3 is deterministic: it follows from the kind of
// change and the kind of edge that reaches a consumer, never from a model's
// opinion.
type ChangeKind string

const (
	// ChangeSignature alters a callable's parameters or results.
	ChangeSignature ChangeKind = "signature"
	// ChangeBehaviour keeps the signature and changes what the code does.
	ChangeBehaviour ChangeKind = "behaviour"
	// ChangeRemove deletes the entity.
	ChangeRemove ChangeKind = "remove"
	// ChangeRename renames without changing shape.
	ChangeRename ChangeKind = "rename"
	// ChangeAddField adds a field or parameter with a default.
	ChangeAddField ChangeKind = "add_field"
	// ChangeSchema alters a database schema object.
	ChangeSchema ChangeKind = "schema"
	// ChangeConfig alters a configuration key.
	ChangeConfig ChangeKind = "config"
	// ChangeRoute alters an HTTP route or its contract.
	ChangeRoute ChangeKind = "route"
)

// Verdict is the deterministic compatibility judgement for one consumer.
type Verdict string

const (
	// Breaking: the consumer cannot keep working unchanged.
	Breaking Verdict = "breaking"
	// Compiles: the consumer still builds, but behaviour may differ, so it
	// needs a test rather than an edit.
	Compiles Verdict = "compiles_behaviour_may_differ"
	// Compatible: no action expected.
	Compatible Verdict = "compatible"
	// Undetermined: the edge evidence is too weak to judge. §3.3 requires
	// these to be surfaced, not dropped.
	Undetermined Verdict = "undetermined"
)

// Consumer is one affected entity.
type Consumer struct {
	Node     Node     `json:"node"`
	Depth    int      `json:"depth"`
	Via      EdgeKind `json:"via"`
	Evidence Evidence `json:"evidence"`
	Verdict  Verdict  `json:"verdict"`
	// Migration is the deterministic step a human or the planner must take.
	Migration string `json:"migration,omitempty"`
}

// Impact is the report handed to the planner before edits, to the plan
// reviewer, and to the human gate (§3.3).
type Impact struct {
	Changed   []Node     `json:"changed"`
	Kind      ChangeKind `json:"change_kind"`
	Consumers []Consumer `json:"consumers"`
	// Counts by verdict, for the summary line.
	Counts map[Verdict]int `json:"counts"`
	// Truncated is set when the traversal hit its node cap: the report is then
	// a lower bound and must be presented as one.
	Truncated bool `json:"truncated"`
	// Caveat is always populated. A missing edge means "not discovered", and
	// every consumer of this report is told so explicitly.
	Caveat string `json:"caveat"`
}

// ImpactCaveat is the standing disclaimer required by §3.3.
const ImpactCaveat = "A missing edge means 'not discovered', not 'does not exist'. " +
	"Consumers reached by inferred or unknown evidence are listed and treated as present."

// verdictFor is the deterministic table behind §3.3. Rows are (change kind,
// edge kind) and the result is a verdict plus the migration step.
func verdictFor(change ChangeKind, via EdgeKind, ev Evidence) (Verdict, string) {
	if !ev.Certain() {
		// The relationship itself is a heuristic. The consumer is reported and
		// treated as present, but no compatibility claim can be made.
		return Undetermined, "verify manually: relationship evidence is " + string(ev)
	}
	switch change {
	case ChangeRemove:
		return Breaking, "remove or redirect this reference before deleting the definition"
	case ChangeRename:
		switch via {
		case EdgeCalls, EdgeUsesType, EdgeReferences, EdgeImplements, EdgeImports, EdgeExtends, EdgeEmbeds:
			return Breaking, "update the reference to the new name"
		case EdgeReadsConfig:
			return Breaking, "update the configuration key and every deployment that sets it"
		}
		return Compiles, "no source change expected; re-run this consumer's tests"
	case ChangeSignature:
		switch via {
		case EdgeCalls, EdgeReturns, EdgeAccepts:
			return Breaking, "update the call site to the new signature"
		case EdgeImplements:
			return Breaking, "update the implementation to satisfy the new interface method set"
		case EdgeTests:
			return Breaking, "update the test to the new signature"
		}
		return Compiles, "re-run this consumer's tests"
	case ChangeAddField:
		switch via {
		case EdgeImplements:
			return Breaking, "implement the new interface member"
		case EdgeUsesType:
			return Compiles, "check for exhaustive switches and struct literals without field names"
		}
		return Compatible, ""
	case ChangeBehaviour:
		switch via {
		case EdgeTests:
			return Breaking, "the test encodes the old behaviour; update or confirm it"
		}
		return Compiles, "re-run this consumer's tests: behaviour changed, the signature did not"
	case ChangeSchema:
		switch via {
		case EdgeReadsSchema, EdgeWritesSchema:
			return Breaking, "write a migration and update the queries that touch this object"
		case EdgeTests:
			return Breaking, "update fixtures and golden data"
		}
		return Compiles, "re-run this consumer's tests"
	case ChangeConfig:
		switch via {
		case EdgeReadsConfig:
			return Breaking, "update the key here and in every deployment manifest that sets it"
		case EdgeDeploys, EdgeProvisions:
			return Breaking, "update the deployment or infrastructure definition"
		}
		return Compiles, ""
	case ChangeRoute:
		switch via {
		case EdgeRoutesTo, EdgeHandles:
			return Breaking, "update the handler registration"
		case EdgeReferences:
			return Breaking, "update the client call site and any generated client"
		case EdgeTests:
			return Breaking, "update the route under test"
		}
		return Compiles, ""
	}
	return Undetermined, "unclassified change kind"
}

// buildImpact turns a reverse traversal into the report of §3.3. It is
// separate from the storage implementation so that any Graph backend produces
// an identical verdict table.
func buildImpact(changed []Node, kind ChangeKind, reached []Reached, truncated bool) Impact {
	changedIDs := make(map[int64]bool, len(changed))
	for _, n := range changed {
		changedIDs[n.ID] = true
	}
	imp := Impact{
		Changed:   changed,
		Kind:      kind,
		Counts:    map[Verdict]int{},
		Truncated: truncated,
		Caveat:    ImpactCaveat,
	}
	for _, r := range reached {
		if changedIDs[r.Node.ID] {
			continue
		}
		v, migration := verdictFor(kind, r.Via, r.Evidence)
		imp.Consumers = append(imp.Consumers, Consumer{
			Node:      r.Node,
			Depth:     r.Depth,
			Via:       r.Via,
			Evidence:  r.Evidence,
			Verdict:   v,
			Migration: migration,
		})
		imp.Counts[v]++
	}
	// Breaking first, then by depth, then by path: a stable order so that the
	// same change always produces the same report for review and for diffing.
	order := map[Verdict]int{Breaking: 0, Undetermined: 1, Compiles: 2, Compatible: 3}
	sort.SliceStable(imp.Consumers, func(i, j int) bool {
		a, b := imp.Consumers[i], imp.Consumers[j]
		if order[a.Verdict] != order[b.Verdict] {
			return order[a.Verdict] < order[b.Verdict]
		}
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		if a.Node.Path != b.Node.Path {
			return a.Node.Path < b.Node.Path
		}
		return a.Node.FQN < b.Node.FQN
	})
	return imp
}

// Summary is the one-line form shown at the human gate.
func (i Impact) Summary() string {
	return fmt.Sprintf("%d consumers: %d breaking, %d undetermined, %d behaviour-only, %d compatible%s",
		len(i.Consumers), i.Counts[Breaking], i.Counts[Undetermined],
		i.Counts[Compiles], i.Counts[Compatible],
		map[bool]string{true: " (truncated: lower bound)", false: ""}[i.Truncated])
}

// ChangeKinds lists every classification, for CLI validation.
func ChangeKinds() []ChangeKind {
	return []ChangeKind{ChangeSignature, ChangeBehaviour, ChangeRemove, ChangeRename,
		ChangeAddField, ChangeSchema, ChangeConfig, ChangeRoute}
}

// ParseChangeKind validates user input.
func ParseChangeKind(s string) (ChangeKind, error) {
	for _, k := range ChangeKinds() {
		if string(k) == s {
			return k, nil
		}
	}
	return "", fmt.Errorf("graph: unknown change kind %q (one of %v)", s, ChangeKinds())
}
