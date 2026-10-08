package mcp

import "github.com/mr-miles/aikito-go/internal/sync"

// Observe ports MCPPlan.observe.
func (p MCPPlan) Observe() sync.PlanObservation {
	obs := sync.PlanObservation{CanApply: p.CanApply()}
	for _, op := range p.Operations {
		view, finding := ObserveMCPOperation(op)
		obs.Operations = append(obs.Operations, view)
		if finding != nil {
			obs.Findings = append(obs.Findings, *finding)
			if sync.IsErrorFinding(*finding) {
				obs.CanApply = false
			}
		}
	}
	return obs
}
