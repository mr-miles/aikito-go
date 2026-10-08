package sync

import "fmt"

// SubagentPlanCanApply is SubagentPlan.can_apply.
func SubagentPlanCanApply(ops []SubagentOperation) bool {
	for _, op := range ops {
		if (op.Action == SAConflict && !op.IsAuthorized) || op.Action == SAError {
			return false
		}
	}
	return true
}

func isSubagentMutation(a SubagentOpAction) bool {
	return a == SACreate || a == SAUpdate || a == SARemove
}

// SubagentOperationEffect ports subagent.py's subagent_operation_effect.
func SubagentOperationEffect(op SubagentOperation) (OperationEffect, error) {
	if isSubagentMutation(op.Action) && !op.IsAuthorized {
		return EffectNone, nil
	}
	switch op.Action {
	case SACreate:
		return EffectCreate, nil
	case SAUpdate:
		return EffectUpdate, nil
	case SARemove:
		return EffectRemove, nil
	case SANoop:
		return EffectNoop, nil
	case SASkip, SAOrphan:
		return EffectSkip, nil
	case SAConflict, SAError:
		return EffectNone, nil
	default:
		return "", &UnknownPlanActionError{Message: fmt.Sprintf("Unhandled subagent action: %s", op.Action)}
	}
}

// subagentTargetPath is str(op.target.path): Python's Path("") prints ".".
func subagentTargetPath(op SubagentOperation) string {
	if op.TargetPath == "" {
		return "."
	}
	return op.TargetPath
}

// SubagentOperationFinding ports subagent_operation_finding.
func SubagentOperationFinding(op SubagentOperation) *Finding {
	msg := fmt.Sprintf("%s/%s: %s", op.Agent, op.Subagent, op.Reason)
	switch {
	case op.Action == SAOrphan:
		return &Finding{Status: "WARNING", Code: "SUBAGENT_ORPHAN", Message: msg, Resource: subagentTargetPath(op)}
	case (op.Action == SAConflict || isSubagentMutation(op.Action)) && !op.IsAuthorized:
		return &Finding{Status: "CONFLICT", Code: "SUBAGENT_CONFLICT", Message: msg, Resource: subagentTargetPath(op)}
	case op.Action == SAError:
		return &Finding{Status: "ERROR", Code: "SUBAGENT_ERROR", Message: msg, Resource: subagentTargetPath(op)}
	}
	return nil
}

// ObserveSubagentPlan ports SubagentPlan.observe.
func ObserveSubagentPlan(ops []SubagentOperation) PlanObservation {
	obs := PlanObservation{CanApply: SubagentPlanCanApply(ops)}
	for _, op := range ops {
		effect, err := SubagentOperationEffect(op)
		var finding *Finding
		if err != nil {
			effect = EffectNone
			finding = &Finding{Status: "ERROR", Code: "UNKNOWN_PLAN_ACTION", Message: err.Error(), Resource: subagentTargetPath(op)}
		} else {
			finding = SubagentOperationFinding(op)
		}
		obs.Operations = append(obs.Operations, PlanOperationView{
			ResourceType: "subagent", ResourceName: op.Subagent, Effect: effect,
			Scope: "global", Agent: op.Agent, Target: subagentTargetPath(op),
			Reason: op.Reason, DomainAction: string(op.Action), Authorized: op.IsAuthorized,
		})
		if finding != nil {
			obs.Findings = append(obs.Findings, *finding)
			if IsErrorFinding(*finding) {
				obs.CanApply = false
			}
		}
	}
	return obs
}
