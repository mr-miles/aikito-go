package projectsync

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SkillTarget is skill_plan.py's SkillTarget.
type SkillTarget struct {
	WorkspaceRoot    string
	WorkspaceID      string
	ProjectName      string
	PhysicalCheckout string
	SkillName        string
	TargetPath       string
}

// ObservedSkill is skill_plan.py's ObservedSkill.
type ObservedSkill struct {
	Target                    SkillTarget
	EntryType                 string // "missing", "dir", "symlink", "unsupported"
	RawLinkTarget             string
	ResolvedLinkTarget        string
	LinkPointsToCanonical     bool
	LinkPointsWithinCanonical bool
	CanonicalValid            bool
	CanonicalError            string
	RuntimeFingerprint        string // "" for None
	CanonicalFingerprint      string // "" for None
	StateRecord               *SkillStateRecord
	StateError                string
	StateRevision             int
}

// DesiredSkill is skill_plan.py's DesiredSkill.
type DesiredSkill struct {
	SkillName            string
	Mode                 string // "link", "copy", "absent"
	CanonicalPath        string
	CanonicalFingerprint string
}

// CandidatePathCAS is skill_plan.py's CandidatePathCAS.
type CandidatePathCAS struct {
	ConfigPath    string
	PreImage      []byte
	PreImageHash  string
	PostImage     []byte
	PostImageHash string
	IsNoop        bool
}

// SkillOperation is skill_plan.py's SkillOperation. Optional strings are
// "" for None; ExpectedRevision is nil for None.
type SkillOperation struct {
	Action                 string
	RuleID                 string
	Target                 SkillTarget
	Reason                 string
	Finding                string
	RequiresForce          bool
	ForceType              string
	IsAuthorized           bool
	ExpectedRepresentation string
	DesiredRepresentation  string
	ExpectedFingerprint    string
	DesiredFingerprint     string
	ExpectedRevision       *int
	NextStateLifecycle     string
	NextBaselineOrigin     string
}

// SkillPlan is skill_plan.py's SkillPlan.
type SkillPlan struct {
	WorkspaceRoot  string
	ProjectName    string
	Operations     []SkillOperation
	Findings       []string
	Authorizations []string
	CanApply       bool
	ConfigCAS      *CandidatePathCAS
}

func intPtr(i int) *int { return &i }

func skillOpFromLinkOp(l LinkOperation, t SkillTarget, rev *int) SkillOperation {
	return SkillOperation{
		Action: l.Action, RuleID: l.RuleID, Target: t, Reason: l.Reason, Finding: l.Finding,
		IsAuthorized: l.IsAuthorized, ExpectedRepresentation: l.ExpectedRepresentation,
		DesiredRepresentation: l.DesiredRepresentation, ExpectedRevision: rev,
	}
}

func authOr(auth bool, yes, no string) string {
	if auth {
		return yes
	}
	return no
}

// PlanSingleSkill is skill_plan.py's plan_single_skill.
func PlanSingleSkill(t SkillTarget, d DesiredSkill, o ObservedSkill, force, isOffline bool) SkillOperation {
	name := t.SkillName
	base := func(action, rule, reason string) SkillOperation {
		return SkillOperation{Action: action, RuleID: rule, Target: t, Reason: reason, IsAuthorized: true,
			ExpectedRepresentation: "missing", DesiredRepresentation: "link"}
	}
	if isOffline {
		op := base("NOOP", "INV-AUTH-02", fmt.Sprintf("Skill '%s' on offline checkout is not synchronized", name))
		op.ExpectedRepresentation, op.DesiredRepresentation = o.EntryType, d.Mode
		return op
	}
	expectedRevision := intPtr(o.StateRevision)

	if d.Mode == "link" || d.Mode == "copy" {
		if !o.CanonicalValid {
			if o.EntryType == "symlink" && o.LinkPointsToCanonical {
				op := base("CONFLICT", "INV-TR-04", fmt.Sprintf("Broken symlink pointing to missing canonical source for skill '%s'", name))
				op.Finding = fmt.Sprintf("Broken symlink pointing to missing canonical skill: %s", t.TargetPath)
				op.IsAuthorized = false
				op.ExpectedRepresentation, op.DesiredRepresentation = "symlink", d.Mode
				return op
			}
			errMsg := o.CanonicalError
			if errMsg == "" {
				errMsg = fmt.Sprintf("Canonical skill '%s' is missing or unreadable", name)
			}
			op := base("CONFLICT", "INV-TR-02", "Canonical skill source missing or unreadable: "+errMsg)
			op.Finding = op.Reason
			op.IsAuthorized = false
			op.ExpectedRepresentation, op.DesiredRepresentation = o.EntryType, d.Mode
			return op
		}

		if d.Mode == "link" {
			if o.EntryType == "dir" {
				s := o.StateRecord
				if s != nil && s.Lifecycle == "active" && o.RuntimeFingerprint == s.BaselineFingerprint {
					op := base("UPDATE", "INV-TR-20", fmt.Sprintf("Switch mode from copy to link for unchanged active skill '%s'", name))
					op.ExpectedRepresentation, op.DesiredRepresentation = "copy", "link"
					op.ExpectedFingerprint = o.RuntimeFingerprint
					op.ExpectedRevision = expectedRevision
					op.NextStateLifecycle = "inactive"
					return op
				}
				op := base("CONFLICT", "INV-TR-20", fmt.Sprintf("Cannot switch copy to link for skill '%s': directory is drifted, unmanaged, or inactive", name))
				op.Finding = fmt.Sprintf("Cannot switch copy to link for skill '%s': %s is not an unchanged active copy", name, t.TargetPath)
				op.ExpectedRepresentation, op.DesiredRepresentation = "copy", "link"
				op.IsAuthorized = false
				return op
			}
			ol := ObservedLink{
				TargetPath: t.TargetPath, EntryType: o.EntryType, ExpectedCanonical: d.CanonicalPath,
				CanonicalValid: o.CanonicalValid, CanonicalError: o.CanonicalError,
				RawLinkTarget: o.RawLinkTarget, ResolvedLinkTarget: o.ResolvedLinkTarget,
				LinkPointsToCanonical: o.LinkPointsToCanonical, TargetKind: "managed_entry", Scope: "project",
			}
			l := PlanLinkTarget(ol, "link", PlanLinkOptions{HasStateRecord: o.StateRecord != nil, ResourceName: name})
			return skillOpFromLinkOp(l, t, expectedRevision)
		}

		// Copy mode
		if o.StateError != "" {
			op := base("CONFLICT", "INV-TR-13", "State record is corrupt or unreadable: "+o.StateError)
			op.Finding = fmt.Sprintf("Corrupt state record for skill '%s': %s", name, o.StateError)
			op.ExpectedRepresentation, op.DesiredRepresentation = o.EntryType, "copy"
			op.IsAuthorized = false
			return op
		}
		switch o.EntryType {
		case "missing":
			op := base("CREATE", "INV-TR-01", fmt.Sprintf("Create copy of skill '%s'", name))
			op.DesiredRepresentation = "copy"
			op.DesiredFingerprint = o.CanonicalFingerprint
			op.ExpectedRevision = expectedRevision
			op.NextStateLifecycle, op.NextBaselineOrigin = "active", "write"
			return op
		case "symlink":
			if o.LinkPointsToCanonical {
				op := base("UPDATE", "INV-TR-20", fmt.Sprintf("Switch mode from link to copy for skill '%s'", name))
				op.ExpectedRepresentation, op.DesiredRepresentation = "link", "copy"
				op.DesiredFingerprint = o.CanonicalFingerprint
				op.ExpectedRevision = expectedRevision
				op.NextStateLifecycle, op.NextBaselineOrigin = "active", "write"
				return op
			}
			op := base("CONFLICT", "INV-TR-20", "Cannot switch link to copy: symlink points to unauthorized destination")
			op.Finding = fmt.Sprintf("Cannot switch link to copy: %s points elsewhere", t.TargetPath)
			op.ExpectedRepresentation, op.DesiredRepresentation = "symlink", "copy"
			op.IsAuthorized = false
			return op
		case "dir":
			r, c := o.RuntimeFingerprint, o.CanonicalFingerprint
			s := o.StateRecord
			copyOp := func(action, rule, reason string) SkillOperation {
				op := base(action, rule, reason)
				op.ExpectedRepresentation, op.DesiredRepresentation = "copy", "copy"
				op.ExpectedFingerprint, op.DesiredFingerprint = r, c
				op.ExpectedRevision = expectedRevision
				return op
			}
			if s != nil && s.Lifecycle == "active" {
				b := s.BaselineFingerprint
				switch {
				case r == b && b == c:
					return copyOp("NOOP", "INV-TR-07", fmt.Sprintf("Copied skill '%s' matches canonical and baseline", name))
				case r == b && r != c:
					op := copyOp("UPDATE", "INV-TR-08", fmt.Sprintf("Update copied skill '%s' to match upstream canonical changes", name))
					op.NextStateLifecycle, op.NextBaselineOrigin = "active", "write"
					return op
				case r != b && r != c:
					op := copyOp(authOr(force, "UPDATE", "CONFLICT"), "INV-TR-09", fmt.Sprintf("Copied project skill '%s' drifted from workspace skill", name))
					op.Finding = authOr(force, "", fmt.Sprintf("Project skill '%s' drifted at %s", name, t.TargetPath))
					op.RequiresForce, op.ForceType, op.IsAuthorized = true, "overwrite_drift", force
					op.NextStateLifecycle, op.NextBaselineOrigin = authOr(force, "active", ""), authOr(force, "write", "")
					return op
				case r != b && r == c:
					op := copyOp("RECONCILE_STATE", "INV-TR-10", fmt.Sprintf("Reconcile management state for skill '%s' to upstream canonical", name))
					op.NextStateLifecycle, op.NextBaselineOrigin = "active", "reconcile"
					return op
				}
			}
			if s != nil && s.Lifecycle == "inactive" {
				if r == c {
					if force {
						op := copyOp("REACTIVATE_STATE", "INV-TR-18", fmt.Sprintf("Reactivate state for inactive copied skill '%s'", name))
						op.RequiresForce, op.ForceType = true, "reactivate_state"
						op.NextStateLifecycle, op.NextBaselineOrigin = "active", "reactivate"
						return op
					}
					return copyOp("NOOP", "INV-TR-18", fmt.Sprintf("Inactive copied skill '%s' matches canonical (unmanaged)", name))
				}
				op := copyOp(authOr(force, "UPDATE", "CONFLICT"), "INV-TR-19", fmt.Sprintf("Inactive copied skill '%s' differs from canonical", name))
				op.Finding = authOr(force, "", fmt.Sprintf("Inactive copied skill '%s' differs from canonical; review diff or use --force to overwrite", name))
				op.RequiresForce, op.ForceType, op.IsAuthorized = true, "overwrite_unmanaged", force
				op.NextStateLifecycle, op.NextBaselineOrigin = authOr(force, "active", ""), authOr(force, "write", "")
				return op
			}
			if r == c {
				if force {
					op := copyOp("CLAIM_STATE", "INV-TR-11", fmt.Sprintf("Claim management state for skill '%s'", name))
					op.RequiresForce, op.ForceType = true, "claim_state"
					op.NextStateLifecycle, op.NextBaselineOrigin = "active", "claim"
					return op
				}
				return copyOp("NOOP", "INV-TR-11", fmt.Sprintf("Unmanaged skill directory '%s' matches canonical", name))
			}
			op := copyOp(authOr(force, "UPDATE", "CONFLICT"), "INV-TR-12", fmt.Sprintf("Unmanaged skill directory '%s' conflicts with canonical skill", name))
			op.Finding = authOr(force, "", fmt.Sprintf("Unmanaged directory for skill '%s' conflicts with canonical; use --force to overwrite", name))
			op.RequiresForce, op.ForceType, op.IsAuthorized = true, "overwrite_unmanaged", force
			op.NextStateLifecycle, op.NextBaselineOrigin = authOr(force, "active", ""), authOr(force, "write", "")
			return op
		}
		op := base("CONFLICT", "INV-TR-13", fmt.Sprintf("Target path %s is an unsupported filesystem entry", t.TargetPath))
		op.Finding = fmt.Sprintf("Target path is unsupported filesystem entry: %s", t.TargetPath)
		op.ExpectedRepresentation, op.DesiredRepresentation = "unsupported", "copy"
		op.IsAuthorized = false
		return op
	}

	// Deselected: mode == "absent"
	if o.EntryType == "symlink" {
		ol := ObservedLink{
			TargetPath: t.TargetPath, EntryType: o.EntryType, ExpectedCanonical: d.CanonicalPath,
			CanonicalValid: o.CanonicalValid, CanonicalError: o.CanonicalError,
			RawLinkTarget: o.RawLinkTarget, ResolvedLinkTarget: o.ResolvedLinkTarget,
			LinkPointsToCanonical: o.LinkPointsToCanonical, TargetKind: "managed_entry", Scope: "project",
		}
		l := PlanLinkTarget(ol, "absent", PlanLinkOptions{ResourceName: name})
		return skillOpFromLinkOp(l, t, expectedRevision)
	}
	s := o.StateRecord
	if o.EntryType == "dir" {
		if s != nil && s.Lifecycle == "active" {
			op := base("DEACTIVATE_STATE", "INV-TR-16", fmt.Sprintf("Deactivate state record for deselected copy skill '%s'; preserve directory", name))
			op.ExpectedRepresentation, op.DesiredRepresentation = "copy", "absent"
			op.ExpectedFingerprint = o.RuntimeFingerprint
			op.ExpectedRevision = expectedRevision
			op.NextStateLifecycle = "inactive"
			return op
		}
		op := base("NOOP", "INV-TR-17", fmt.Sprintf("Preserve unmanaged or inactive copy directory for deselected skill '%s'", name))
		op.ExpectedRepresentation, op.DesiredRepresentation = "copy", "absent"
		return op
	}
	if s != nil && s.Lifecycle == "active" {
		op := base("DEACTIVATE_STATE", "INV-TR-16", fmt.Sprintf("Deactivate state record for absent deselected skill '%s'", name))
		op.ExpectedRepresentation, op.DesiredRepresentation = "missing", "absent"
		op.ExpectedRevision = expectedRevision
		op.NextStateLifecycle = "inactive"
		return op
	}
	op := base("NOOP", "INV-TR-17", fmt.Sprintf("Deselected skill '%s' is already absent", name))
	op.ExpectedRepresentation, op.DesiredRepresentation = "missing", "absent"
	return op
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatAuthorizationItem is format_authorization_item.
func formatAuthorizationItem(op SkillOperation) string {
	t := op.Target
	rev := "-"
	if op.ExpectedRevision != nil {
		rev = strconv.Itoa(*op.ExpectedRevision)
	}
	forceType := op.ForceType
	if forceType == "" {
		forceType = "default"
	}
	return fmt.Sprintf("AUTH: [%s/%s] project='%s' skill='%s' path='%s' rep='%s->%s' fp='%s->%s' state_revision='%s->%s'",
		op.Action, forceType, t.ProjectName, t.SkillName, filepath.ToSlash(t.TargetPath),
		op.ExpectedRepresentation, op.DesiredRepresentation,
		orDash(op.ExpectedFingerprint), orDash(op.DesiredFingerprint), rev, orDash(op.NextStateLifecycle))
}

// BuildSkillPlan is build_skill_plan.
func BuildSkillPlan(workspaceRoot, projectName string, ops []SkillOperation, cas *CandidatePathCAS) SkillPlan {
	sorted := append([]SkillOperation(nil), ops...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := asPosix(sorted[i].Target.PhysicalCheckout), asPosix(sorted[j].Target.PhysicalCheckout)
		if a != b {
			return a < b
		}
		return sorted[i].Target.SkillName < sorted[j].Target.SkillName
	})
	var findings, auths []string
	for _, op := range sorted {
		if op.Finding != "" && !contains(findings, op.Finding) {
			findings = append(findings, op.Finding)
		}
		if op.RequiresForce && op.IsAuthorized {
			auths = append(auths, formatAuthorizationItem(op))
		}
	}
	canApply := true
	for _, op := range sorted {
		if op.Action == "CONFLICT" || !op.IsAuthorized {
			canApply = false
		}
	}
	for _, f := range findings {
		lf := strings.ToLower(f)
		if strings.Contains(lf, "conflict") || strings.Contains(lf, "collision") {
			canApply = false
		}
	}
	return SkillPlan{WorkspaceRoot: workspaceRoot, ProjectName: projectName, Operations: sorted,
		Findings: findings, Authorizations: auths, CanApply: canApply, ConfigCAS: cas}
}
