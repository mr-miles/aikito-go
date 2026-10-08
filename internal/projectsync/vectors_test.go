package projectsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testdata/vectors.json is generated from the reference implementation by
// testdata/gen_vectors.py; never edit it by hand.
type vectorFile struct {
	SkillCases []struct {
		In struct {
			Mode, Entry    string
			Points         bool
			CanonicalValid bool   `json:"canonical_valid"`
			CanonicalError string `json:"canonical_error"`
			Lifecycle      string
			RuntimeFP      string `json:"runtime_fp"`
			BaselineFP     string `json:"baseline_fp"`
			CanonicalFP    string `json:"canonical_fp"`
			StateError     string `json:"state_error"`
			Force, Offline bool
		} `json:"in"`
		Out map[string]any `json:"out"`
	} `json:"skill_cases"`
	Authorization string `json:"authorization"`
	LinkCases     []struct {
		In struct {
			Mode, Entry           string
			Points, Same          bool
			CanonicalValid        bool   `json:"canonical_valid"`
			CanonicalError        string `json:"canonical_error"`
			Kind, Scope, Avail    string
			Parent, State, Legacy bool
			Name                  string
		} `json:"in"`
		Out map[string]any `json:"out"`
	} `json:"link_cases"`
	Fingerprints []struct {
		Name  string
		Files map[string]*struct {
			Content string
			Mode    os.FileMode
		}
		Fingerprint *string
		Error       *string
	}
	Binding struct {
		WorkspaceRoot string `json:"workspace_root"`
		ProjectName   string `json:"project_name"`
		Checkout      string
		Hash          string
	}
	StateJSON string `json:"state_json"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func skillOpDict(op SkillOperation) map[string]any {
	var rev any
	if op.ExpectedRevision != nil {
		rev = float64(*op.ExpectedRevision)
	}
	return map[string]any{
		"action": op.Action, "rule_id": op.RuleID, "reason": op.Reason, "finding": op.Finding,
		"requires_force": op.RequiresForce, "force_type": op.ForceType, "is_authorized": op.IsAuthorized,
		"expected_representation": op.ExpectedRepresentation, "desired_representation": op.DesiredRepresentation,
		"expected_fingerprint": op.ExpectedFingerprint, "desired_fingerprint": op.DesiredFingerprint,
		"expected_revision": rev, "next_state_lifecycle": op.NextStateLifecycle, "next_baseline_origin": op.NextBaselineOrigin,
	}
}

var vectorTarget = SkillTarget{WorkspaceRoot: "/ws", WorkspaceID: "ws", ProjectName: "proj", PhysicalCheckout: "/co",
	SkillName: "alpha", TargetPath: "/co/.agents/skills/alpha"}

func TestPlanSingleSkillMatchesPython(t *testing.T) {
	v := loadVectors(t)
	if len(v.SkillCases) < 50 {
		t.Fatalf("only %d skill cases", len(v.SkillCases))
	}
	for _, c := range v.SkillCases {
		in := c.In
		o := ObservedSkill{Target: vectorTarget, EntryType: in.Entry, LinkPointsToCanonical: in.Points,
			CanonicalValid: in.CanonicalValid, CanonicalError: in.CanonicalError,
			RuntimeFingerprint: in.RuntimeFP, CanonicalFingerprint: in.CanonicalFP, StateError: in.StateError, StateRevision: 3}
		if in.Entry == "symlink" {
			o.RawLinkTarget, o.ResolvedLinkTarget = "/elsewhere/alpha", "/elsewhere/alpha"
		}
		if in.Lifecycle != "" {
			o.StateRecord = &SkillStateRecord{SkillName: "alpha", Representation: "copy", Lifecycle: in.Lifecycle,
				BaselineFingerprint: in.BaselineFP, BaselineOrigin: "write", LastObservedSelected: true}
		}
		d := DesiredSkill{SkillName: "alpha", Mode: in.Mode, CanonicalPath: "/ws/skills/alpha", CanonicalFingerprint: in.CanonicalFP}
		got := skillOpDict(PlanSingleSkill(vectorTarget, d, o, in.Force, in.Offline))
		if jsonOf(got) != jsonOf(c.Out) {
			t.Errorf("input %+v:\n got %s\nwant %s", in, jsonOf(got), jsonOf(c.Out))
		}
	}
}

func TestAuthorizationItemMatchesPython(t *testing.T) {
	v := loadVectors(t)
	d := DesiredSkill{SkillName: "alpha", Mode: "copy", CanonicalPath: "/ws/skills/alpha", CanonicalFingerprint: "v1:ccc"}
	o := ObservedSkill{Target: vectorTarget, EntryType: "dir", CanonicalValid: true, RuntimeFingerprint: "v1:aaa", CanonicalFingerprint: "v1:ccc",
		StateRecord:   &SkillStateRecord{SkillName: "alpha", Representation: "copy", Lifecycle: "active", BaselineFingerprint: "v1:bbb", BaselineOrigin: "write", LastObservedSelected: true},
		StateRevision: 3}
	plan := BuildSkillPlan("/ws", "proj", []SkillOperation{PlanSingleSkill(vectorTarget, d, o, true, false)}, nil)
	if len(plan.Authorizations) != 1 || plan.Authorizations[0] != v.Authorization {
		t.Errorf("authorizations = %q, want %q", plan.Authorizations, v.Authorization)
	}
}

func TestPlanLinkTargetMatchesPython(t *testing.T) {
	v := loadVectors(t)
	if len(v.LinkCases) < 100 {
		t.Fatalf("only %d link cases", len(v.LinkCases))
	}
	for _, c := range v.LinkCases {
		in := c.In
		o := ObservedLink{TargetPath: "/t/target", EntryType: in.Entry, ExpectedCanonical: "/c/canon",
			CanonicalValid: in.CanonicalValid, CanonicalError: in.CanonicalError, LinkPointsToCanonical: in.Points,
			IsSameObject: in.Same, TargetKind: in.Kind, Scope: in.Scope}
		if in.Entry == "symlink" {
			o.RawLinkTarget, o.ResolvedLinkTarget = "/x/raw", "/x/resolved"
		}
		parent := in.Parent
		op := PlanLinkTarget(o, in.Mode, PlanLinkOptions{AvailabilityStatus: in.Avail, ParentExists: &parent,
			HasStateRecord: in.State, IsLegacyContainer: in.Legacy, ResourceName: in.Name})
		got := map[string]any{
			"action": op.Action, "rule_id": op.RuleID, "reason": op.Reason, "finding": op.Finding,
			"is_authorized": op.IsAuthorized, "expected_representation": op.ExpectedRepresentation,
			"desired_representation": op.DesiredRepresentation, "requires_parent_creation": op.RequiresParentCreation,
			"is_same_object": op.IsSameObject, "target_kind": op.TargetKind, "resource_name": op.ResourceName,
		}
		if jsonOf(got) != jsonOf(c.Out) {
			t.Errorf("input %+v:\n got %s\nwant %s", in, jsonOf(got), jsonOf(c.Out))
		}
	}
}

func TestDirectoryFingerprintMatchesPython(t *testing.T) {
	v := loadVectors(t)
	for _, fp := range v.Fingerprints {
		root := t.TempDir()
		for rel, spec := range fp.Files {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if spec == nil {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(spec.Content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, spec.Mode); err != nil {
				t.Fatal(err)
			}
		}
		got, errMsg := CalculateDirectoryFingerprint(root)
		if fp.Error != nil || fp.Fingerprint == nil {
			t.Fatalf("%s: vector has no fingerprint (error %v)", fp.Name, fp.Error)
		}
		if errMsg != "" || got != *fp.Fingerprint {
			t.Errorf("%s: got %q (%s), want %q", fp.Name, got, errMsg, *fp.Fingerprint)
		}
	}
}

func TestBindingHashAndStateJSONMatchPython(t *testing.T) {
	v := loadVectors(t)
	if got := BindingHash(v.Binding.WorkspaceRoot, v.Binding.ProjectName, v.Binding.Checkout); got != v.Binding.Hash {
		t.Errorf("binding hash = %s, want %s", got, v.Binding.Hash)
	}
	doc := &ProjectSkillStateDocument{Version: 1, Revision: 4, WorkspaceRoot: "/ws/aikito", ProjectName: "proj", PhysicalCheckout: "/co/p\u00e9",
		Records: map[string]SkillStateRecord{
			"zeta":  {SkillName: "zeta", Representation: "copy", Lifecycle: "inactive", BaselineFingerprint: "v1:z", BaselineOrigin: "claim"},
			"alpha": {SkillName: "alpha", Representation: "copy", Lifecycle: "active", BaselineFingerprint: "v1:a", BaselineOrigin: "write", LastObservedSelected: true},
		}}
	if got := pyDumps(doc.toDict(), 2); got != v.StateJSON {
		t.Errorf("state JSON:\n got %s\nwant %s", got, v.StateJSON)
	}
	// And it round-trips through the loader's schema.
	raw, err := decodeJSON([]byte(v.StateJSON))
	if err != nil {
		t.Fatal(err)
	}
	back, err := documentFromDict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pyDumps(back.toDict(), 2) != v.StateJSON {
		t.Error("state document does not round-trip")
	}
}
