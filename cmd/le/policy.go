package main

import (
	"time"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/config"
)

// policyFrom translates the operator's gate configuration into a broker policy.
func policyFrom(g config.GateConfig) broker.Policy {
	return broker.Policy{
		RequireForBreaking:   g.Breaking,
		RequireForOutOfScope: g.OutOfScope,
		RequireForApply:      g.Apply,
		RequireForPlan:       g.Plan,
		Timeout:              time.Duration(g.TimeoutMinutes) * time.Minute,
	}
}
