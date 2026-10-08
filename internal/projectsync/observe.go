package projectsync

import (
	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/sync"
)

// SkillOperationEffect ports skill_plan.py's skill_operation_effect.
func SkillOperationEffect(op SkillOperation) (sync.OperationEffect, error) {
	if !op.IsAuthorized {
		return sync.EffectNone, nil
	}
	switch op.Action {
	case "CREATE":
		return sync.EffectCreate, nil
	case "UPDATE":
		return sync.EffectUpdate, nil
	case "UNLINK":
		return sync.EffectRemove, nil
	case "NOOP":
		return sync.EffectNoop, nil
	case "RECONCILE_STATE", "CLAIM_STATE", "REACTIVATE_STATE", "DEACTIVATE_STATE":
		return sync.EffectStateOnly, nil
	case "CONFLICT":
		return sync.EffectNone, nil
	default:
		return "", &sync.UnknownPlanActionError{Message: "Unhandled skill action: " + op.Action}
	}
}

// SkillOperationFinding ports skill_operation_finding.
func SkillOperationFinding(op SkillOperation) *sync.Finding {
	if op.Action != "CONFLICT" && op.IsAuthorized {
		return nil
	}
	return &sync.Finding{
		Status:   "CONFLICT",
		Code:     orStr(op.RuleID, "SKILL_CONFLICT"),
		Message:  orStr(op.Finding, op.Reason),
		Resource: op.Target.TargetPath,
	}
}

// ObserveSkillOperation ports observe_skill_operation.
func ObserveSkillOperation(op SkillOperation) (sync.PlanOperationView, *sync.Finding) {
	effect, err := SkillOperationEffect(op)
	var finding *sync.Finding
	if err != nil {
		effect = sync.EffectNone
		finding = &sync.Finding{Status: "ERROR", Code: "UNKNOWN_PLAN_ACTION", Message: err.Error(), Resource: op.Target.TargetPath}
	} else {
		finding = SkillOperationFinding(op)
	}
	return sync.PlanOperationView{
		ResourceType: "skill",
		ResourceName: op.Target.SkillName,
		Effect:       effect,
		Scope:        "project",
		Project:      op.Target.ProjectName,
		Target:       op.Target.TargetPath,
		Reason:       op.Reason,
		DomainAction: op.Action,
		Authorized:   op.IsAuthorized,
	}, finding
}

// Observe ports SkillPlan.observe.
func (p SkillPlan) Observe() sync.PlanObservation {
	obs := sync.PlanObservation{CanApply: p.CanApply}
	for _, op := range p.Operations {
		view, finding := ObserveSkillOperation(op)
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

// Observe ports memory_runtime.py's MemoryPlan.observe.
func (p MemoryPlan) Observe() sync.PlanObservation {
	return linkplan.ObserveLinkOperations(p.Operations, p.CanApply(), linkplan.ObserveOptions{
		ResourceType: "memory", Scope: "project", Project: p.Batch.ProjectName, DefaultCode: "MEMORY_CONFLICT",
	})
}

// Observe ports ProjectSyncBatch.observe.
func (b Batch) Observe() sync.PlanObservation {
	children := []sync.PlanObservation{b.SkillPlan.Observe()}
	if b.InstructionPlan != nil {
		children = append(children, b.InstructionPlan.Observe())
	}
	if b.MemoryPlan != nil {
		children = append(children, b.MemoryPlan.Observe())
	}
	var findings []sync.Finding
	for _, text := range b.PreflightFindings {
		if text != "" {
			findings = append(findings, sync.Finding{Status: "ERROR", Code: "PREFLIGHT_ERROR", Message: text, Resource: b.ProjectName})
		}
	}
	canApply := b.CanApply
	return sync.CombineObservations(children, findings, &canApply)
}

// ApplyBatch is apply_project_sync_batch for an applicable batch, as the
// whole-workspace sync calls it: no per-checkout rendering, only the
// executors' own output. It returns "" on success, else the error message.
func ApplyBatch(out Out, b Batch, home string, dryRun bool) string {
	return applyBatch(out, b, home, dryRun)
}
