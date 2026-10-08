package sync

import "testing"

func TestSummarizePlan(t *testing.T) {
	ops := []PlanOperationView{
		NewPlanOperationView("skill", "a", EffectCreate),
		NewPlanOperationView("skill", "b", EffectUpdate),
		NewPlanOperationView("skill", "c", EffectRemove),
		NewPlanOperationView("skill", "d", EffectStateOnly),
		NewPlanOperationView("skill", "e", EffectNoop),
		NewPlanOperationView("skill", "f", EffectSkip),
		NewPlanOperationView("skill", "g", EffectNone),
	}
	findings := []Finding{
		{Status: "CONFLICT"},
		{Status: "warn"},
		{Status: "FAIL"},
	}
	s := SummarizePlan(ops, findings)
	want := PlanSummary{Creates: 1, Updates: 1, Removes: 1, StateOnly: 1, Unchanged: 1, Skipped: 1, Conflicts: 1, Warnings: 1, Errors: 1}
	if s != want {
		t.Fatalf("SummarizePlan() = %+v, want %+v", s, want)
	}
	if s.Changes() != 3 {
		t.Errorf("Changes() = %d, want 3 (state_only must be excluded)", s.Changes())
	}
	if !s.Blocked() {
		t.Errorf("Blocked() = false, want true (has conflicts+errors)")
	}
}

func TestCombineObservationsNoDedup(t *testing.T) {
	a := PlanObservation{Operations: []PlanOperationView{NewPlanOperationView("skill", "x", EffectCreate)}, CanApply: true}
	b := PlanObservation{Operations: []PlanOperationView{NewPlanOperationView("skill", "x", EffectCreate)}, CanApply: false}
	combined := CombineObservations([]PlanObservation{a, b}, nil, nil)
	if len(combined.Operations) != 2 {
		t.Errorf("expected no dedup, got %d operations", len(combined.Operations))
	}
	if combined.CanApply {
		t.Errorf("CanApply should be false when any child is false")
	}
}

func TestCombineObservationsOverride(t *testing.T) {
	a := PlanObservation{CanApply: true}
	falseOverride := false
	combined := CombineObservations([]PlanObservation{a}, nil, &falseOverride)
	if combined.CanApply {
		t.Errorf("override should force CanApply=false")
	}
}

type panickyPlan struct{}

func (panickyPlan) Observe() PlanObservation { panic("boom") }

type okPlan struct{ obs PlanObservation }

func (p okPlan) Observe() PlanObservation { return p.obs }

func TestSafeObservePlanNil(t *testing.T) {
	if SafeObservePlan(nil) != nil {
		t.Errorf("expected nil for nil plan")
	}
}

func TestSafeObservePlanPanicBecomesFinding(t *testing.T) {
	result := SafeObservePlan(panickyPlan{})
	if result == nil {
		t.Fatal("expected a non-nil observation")
	}
	if result.CanApply {
		t.Errorf("CanApply should be false after a panic")
	}
	if len(result.Findings) != 1 || result.Findings[0].Code != "PLAN_OBSERVATION_ERROR" {
		t.Errorf("expected one PLAN_OBSERVATION_ERROR finding, got %+v", result.Findings)
	}
}

func TestSafeObservePlanPassthrough(t *testing.T) {
	want := PlanObservation{CanApply: true, Operations: []PlanOperationView{NewPlanOperationView("skill", "y", EffectNoop)}}
	result := SafeObservePlan(okPlan{obs: want})
	if result == nil || result.CanApply != want.CanApply || len(result.Operations) != 1 {
		t.Errorf("SafeObservePlan passthrough mismatch: %+v", result)
	}
}
