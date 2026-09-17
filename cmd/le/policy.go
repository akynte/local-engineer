package main

import (
	"github.com/akynte/local-engineer/internal/supervisor"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/config"
)

// policyFrom translates the operator's gate configuration into a broker policy.
func policyFrom(g config.GateConfig) broker.Policy {
	return supervisor.GatePolicy(g)
}
