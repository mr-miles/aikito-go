package sync

import (
	"fmt"
	"strings"
)

// Finding mirrors diagnostics.py's Finding: a diagnostic with stable
// identity and optional actionable context.
type Finding struct {
	Status   string // "ERROR"/"FAIL", "WARNING"/"WARN", "CONFLICT", or any other status string
	Message  string
	FixHint  string
	Code     string
	Resource string
	Source   string
	Reason   string
}

// NormalizedFindingStatus mirrors normalized_finding_status: canonicalizes
// Status to "error", "warning", "conflict", or the lower-cased original.
func NormalizedFindingStatus(f Finding) string {
	switch strings.ToUpper(strings.TrimSpace(f.Status)) {
	case "ERROR", "FAIL":
		return "error"
	case "WARNING", "WARN":
		return "warning"
	case "CONFLICT":
		return "conflict"
	default:
		return strings.ToLower(strings.TrimSpace(f.Status))
	}
}

func IsErrorFinding(f Finding) bool    { return NormalizedFindingStatus(f) == "error" }
func IsWarningFinding(f Finding) bool  { return NormalizedFindingStatus(f) == "warning" }
func IsConflictFinding(f Finding) bool { return NormalizedFindingStatus(f) == "conflict" }

// OperationEffect is the categorized mutation effect of a single planned
// operation — the only vocabulary the cross-domain aggregator understands;
// each domain's own richer action enum (e.g. a subagent plan's
// CREATE/UPDATE/REMOVE/CONFLICT/...) maps down to this set in its Observe().
type OperationEffect string

const (
	EffectCreate    OperationEffect = "create"
	EffectUpdate    OperationEffect = "update"
	EffectRemove    OperationEffect = "remove"
	EffectStateOnly OperationEffect = "state_only"
	EffectNoop      OperationEffect = "noop"
	EffectSkip      OperationEffect = "skip"
	EffectNone      OperationEffect = "none"
)

// UnknownPlanActionError mirrors UnknownPlanActionError: raised when an
// unrecognized domain operation action is encountered while mapping down to
// an OperationEffect.
type UnknownPlanActionError struct{ Message string }

func (e *UnknownPlanActionError) Error() string { return e.Message }

// PlanOperationView is a read-only, resource-agnostic projection of one
// planned action, used for cross-resource aggregation (status/sync/diff
// rendering walks a tree of these, never a domain's own richer type).
type PlanOperationView struct {
	ResourceType string
	ResourceName string
	Effect       OperationEffect

	Scope   string
	Agent   string
	Project string
	Target  string
	Source  string

	Reason       string
	DomainAction string
	Authorized   bool
}

// NewPlanOperationView returns a view with Authorized defaulting to true,
// matching the Python dataclass's default.
func NewPlanOperationView(resourceType, resourceName string, effect OperationEffect) PlanOperationView {
	return PlanOperationView{ResourceType: resourceType, ResourceName: resourceName, Effect: effect, Authorized: true}
}

// PlanSummary is the aggregated quantitative summary of planned operations
// and diagnostics.
type PlanSummary struct {
	Creates   int
	Updates   int
	Removes   int
	StateOnly int
	Unchanged int
	Skipped   int
	Conflicts int
	Warnings  int
	Errors    int
}

// Changes counts mutations affecting user-visible resources; state_only is
// deliberately excluded so a state-only op (e.g. self-healing local
// bookkeeping with no user-visible file change) never reads to the user as
// "N files changed".
func (s PlanSummary) Changes() int { return s.Creates + s.Updates + s.Removes }

// Blocked is true if the plan contains blocking conflict or error diagnostics.
func (s PlanSummary) Blocked() bool { return s.Conflicts > 0 || s.Errors > 0 }

// SummarizePlan computes a deterministic summary from operations and findings.
func SummarizePlan(operations []PlanOperationView, findings []Finding) PlanSummary {
	var s PlanSummary
	for _, op := range operations {
		switch op.Effect {
		case EffectCreate:
			s.Creates++
		case EffectUpdate:
			s.Updates++
		case EffectRemove:
			s.Removes++
		case EffectStateOnly:
			s.StateOnly++
		case EffectNoop:
			s.Unchanged++
		case EffectSkip:
			s.Skipped++
		case EffectNone:
			// no count
		}
	}
	for _, f := range findings {
		if IsConflictFinding(f) {
			s.Conflicts++
		}
		if IsWarningFinding(f) {
			s.Warnings++
		}
		if IsErrorFinding(f) {
			s.Errors++
		}
	}
	return s
}

// PlanObservation is an immutable, pure observation projection of any
// resource or composite plan.
type PlanObservation struct {
	Operations []PlanOperationView
	Findings   []Finding
	CanApply   bool
}

// NewPlanObservation returns an observation with CanApply defaulting to
// true, matching the Python dataclass's default.
func NewPlanObservation(operations []PlanOperationView, findings []Finding) PlanObservation {
	return PlanObservation{Operations: operations, Findings: findings, CanApply: true}
}

// Summary computes the observation's PlanSummary on demand (not stored).
func (o PlanObservation) Summary() PlanSummary {
	return SummarizePlan(o.Operations, o.Findings)
}

// ObservablePlan is the structural protocol any plan satisfies to project a
// PlanObservation. Concrete domain plans (MCP, subagent, project sync,
// bundled-skill refresh, ...) are built on top of this package; this file
// only defines the shared protocol/primitive they implement, not any
// concrete plan type itself.
type ObservablePlan interface {
	Observe() PlanObservation
}

// CombineObservations combines multiple child observations into a composite
// observation without deduplication — duplicates across domains are
// intentional, each domain's findings are domain-scoped. This is the whole
// aggregation mechanism `aikito status`/`sync --dry-run` renders from: port
// this shape structurally, don't flatten it, so new domains and a
// per-resource-kind breakdown can walk the resulting tree.
func CombineObservations(observations []PlanObservation, additionalFindings []Finding, canApplyOverride *bool) PlanObservation {
	var combinedOps []PlanOperationView
	var combinedFindings []Finding
	for _, obs := range observations {
		combinedOps = append(combinedOps, obs.Operations...)
		combinedFindings = append(combinedFindings, obs.Findings...)
	}
	combinedFindings = append(combinedFindings, additionalFindings...)

	allChildrenCanApply := true
	for _, obs := range observations {
		if !obs.CanApply {
			allChildrenCanApply = false
			break
		}
	}
	effectiveCanApply := allChildrenCanApply
	if canApplyOverride != nil {
		effectiveCanApply = *canApplyOverride && allChildrenCanApply
	}

	return PlanObservation{
		Operations: combinedOps,
		Findings:   combinedFindings,
		CanApply:   effectiveCanApply,
	}
}

// SafeObservePlan safely obtains a PlanObservation from an ObservablePlan,
// mirroring safe_observe_plan's defensive contract: nil in means nil out; an
// already-PlanObservation passes through; a plan whose Observe() panics (the
// Go analogue of Python's "except Exception") becomes a single ERROR finding
// with CanApply=false instead of crashing the whole status/sync aggregation.
//
// Go has no exact equivalent of Python's duck-typed "has an .observe
// attribute but it's not callable, or it returned a non-PlanObservation"
// fallback paths (the ObservablePlan interface makes that case unreachable
// by construction), so this function only has the two real branches: nil,
// and a panic-safe call to Observe().
func SafeObservePlan(plan ObservablePlan) *PlanObservation {
	if plan == nil {
		return nil
	}
	obs, errFinding := observeRecovering(plan)
	if errFinding != nil {
		return &PlanObservation{Findings: []Finding{*errFinding}, CanApply: false}
	}
	return &obs
}

func observeRecovering(plan ObservablePlan) (obs PlanObservation, errFinding *Finding) {
	defer func() {
		if r := recover(); r != nil {
			errFinding = &Finding{
				Status:  "ERROR",
				Code:    "PLAN_OBSERVATION_ERROR",
				Message: fmt.Sprintf("Failed to observe plan %T: %v", plan, r),
			}
		}
	}()
	obs = plan.Observe()
	return obs, nil
}
