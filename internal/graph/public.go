package graph

import "github.com/akynte/local-engineer/internal/policy"

// PublicImpact removes protected source content from model-visible reports,
// including reports computed from a pre-policy index.
func PublicImpact(in Impact) Impact {
	out := in
	out.Changed = nil
	out.Consumers = nil
	out.Counts = map[Verdict]int{}
	for _, node := range in.Changed {
		if !policy.Sensitive(node.Path) {
			out.Changed = append(out.Changed, node)
		}
	}
	for _, consumer := range in.Consumers {
		if policy.Sensitive(consumer.Node.Path) {
			out.Truncated = true
			continue
		}
		out.Consumers = append(out.Consumers, consumer)
		out.Counts[consumer.Verdict]++
	}
	return out
}
