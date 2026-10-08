package linkplan

import (
	"path/filepath"

	"github.com/mr-miles/aikito-go/internal/sync"
)

// LinkOperationEffect ports link.py's link_operation_effect.
func LinkOperationEffect(op Operation) (sync.OperationEffect, error) {
	switch op.Action {
	case ActCreate:
		return sync.EffectCreate, nil
	case ActUnlink:
		return sync.EffectRemove, nil
	case ActMigrateContainer:
		return sync.EffectUpdate, nil
	case ActNoop:
		return sync.EffectNoop, nil
	case ActSharedPath, ActSkip:
		return sync.EffectSkip, nil
	case ActConflict:
		return sync.EffectNone, nil
	default:
		return "", &sync.UnknownPlanActionError{Message: "Unhandled link action: " + op.Action}
	}
}

// LinkOperationFinding ports link_operation_finding.
func LinkOperationFinding(op Operation, defaultCode string) *sync.Finding {
	if op.Action != ActConflict {
		return nil
	}
	return &sync.Finding{
		Status:   "CONFLICT",
		Code:     or(op.RuleID, defaultCode),
		Message:  or(op.Finding, op.Reason),
		Resource: op.TargetPath,
	}
}

// ObserveOptions are observe_link_operation's keyword arguments.
type ObserveOptions struct {
	ResourceType string
	Scope        string
	Project      string
	Agent        string
	DefaultCode  string // default LINK_CONFLICT
}

// ObserveLinkOperation ports observe_link_operation.
func ObserveLinkOperation(op Operation, o ObserveOptions) (sync.PlanOperationView, *sync.Finding) {
	effect, err := LinkOperationEffect(op)
	var finding *sync.Finding
	if err != nil {
		effect = sync.EffectNone
		finding = &sync.Finding{Status: "ERROR", Code: "UNKNOWN_PLAN_ACTION", Message: err.Error(), Resource: op.TargetPath}
	} else {
		finding = LinkOperationFinding(op, or(o.DefaultCode, "LINK_CONFLICT"))
	}
	return sync.PlanOperationView{
		ResourceType: o.ResourceType,
		ResourceName: or(op.ResourceName, filepath.Base(op.TargetPath)),
		Effect:       effect,
		Scope:        o.Scope,
		Project:      o.Project,
		Agent:        o.Agent,
		Target:       op.TargetPath,
		Source:       op.CanonicalPath,
		Reason:       op.Reason,
		DomainAction: op.Action,
		Authorized:   op.IsAuthorized,
	}, finding
}

// ObserveLinkOperations projects a plan's operations, as each plan's
// observe() does: can_apply is the plan's own can_apply and no error
// finding.
func ObserveLinkOperations(ops []Operation, planCanApply bool, o ObserveOptions) sync.PlanObservation {
	obs := sync.PlanObservation{CanApply: planCanApply}
	for _, op := range ops {
		view, finding := ObserveLinkOperation(op, o)
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

// Observe ports GlobalSkillBatchPlan.observe.
func (p GlobalSkillPlan) Observe() sync.PlanObservation {
	return ObserveLinkOperations(p.AllOperations(), p.CanApply(),
		ObserveOptions{ResourceType: "global_skill", Scope: "global", DefaultCode: "SKILL_CONFLICT"})
}

// Observe ports InstructionPlan.observe.
func (p InstructionPlan) Observe() sync.PlanObservation {
	return ObserveLinkOperations(p.Operations, p.CanApply(),
		ObserveOptions{ResourceType: "instruction", Scope: p.Batch.Scope, Project: p.Batch.ProjectName, DefaultCode: "INSTRUCTION_CONFLICT"})
}
