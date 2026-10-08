package projectsync

import "github.com/mr-miles/aikito-go/internal/linkplan"

// Link planning and application are link.py's, ported in internal/linkplan.
type (
	ObservedLink    = linkplan.ObservedLink
	LinkOperation   = linkplan.Operation
	PlanLinkOptions = linkplan.PlanOptions
)

// InspectLinkTarget is link.py's inspect_link_target with its keyword
// arguments spelled out.
func InspectLinkTarget(targetPath, expectedCanonical string, canonicalValid bool, canonicalError, targetKind, scope string, isSameObject bool) ObservedLink {
	return linkplan.Inspect(targetPath, expectedCanonical, linkplan.InspectOptions{
		CanonicalInvalid: !canonicalValid, CanonicalError: canonicalError,
		TargetKind: targetKind, Scope: scope, IsSameObject: isSameObject,
	})
}

// PlanLinkTarget is link.py's plan_link_target.
func PlanLinkTarget(o ObservedLink, desiredMode string, opts PlanLinkOptions) LinkOperation {
	return linkplan.Plan(o, desiredMode, opts)
}

// applyLink is link.py's apply_link_operation.
func applyLink(out Out, op LinkOperation, dryRun bool) linkplan.ExecResult {
	return linkplan.Apply(op, dryRun, false, out.Stdout, out.Stderr)
}

func observedDest(o ObservedLink) string { return o.Dest() }

// pyPath renders an optional path the way an f-string renders a Path or
// None.
func pyPath(p string) string {
	if p == "" {
		return "None"
	}
	return p
}
