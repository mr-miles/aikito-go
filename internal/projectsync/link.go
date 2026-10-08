package projectsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
)

// ObservedLink is link.py's ObservedLink.
type ObservedLink struct {
	TargetPath            string
	EntryType             string // "missing", "symlink", "dir", "file", "unsupported"
	ExpectedCanonical     string // "" for None
	CanonicalValid        bool
	CanonicalError        string
	RawLinkTarget         string // "" for None
	ResolvedLinkTarget    string // "" for None
	LinkPointsToCanonical bool
	IsSameObject          bool
	TargetKind            string // "managed_entry", "consumer_link", "managed_container", "instruction_link", "memory_link"
	Scope                 string // "global", "project"
}

// LinkOperation is link.py's LinkOperation.
type LinkOperation struct {
	Action                 string
	RuleID                 string
	TargetPath             string
	CanonicalPath          string // "" for None
	Reason                 string
	Finding                string // "" for None
	IsAuthorized           bool
	ExpectedRepresentation string
	DesiredRepresentation  string
	RequiresParentCreation bool
	IsSameObject           bool
	TargetKind             string
	ResourceName           string
}

func newLinkOp(action, rule, target, canonical string) LinkOperation {
	return LinkOperation{
		Action: action, RuleID: rule, TargetPath: target, CanonicalPath: canonical,
		IsAuthorized: true, ExpectedRepresentation: "missing", DesiredRepresentation: "link",
		TargetKind: "managed_entry",
	}
}

func pointsTo(candidate, canonical string) bool {
	return normcase(physical(candidate)) == normcase(physical(canonical))
}

// InspectLinkTarget is link.py's inspect_link_target.
func InspectLinkTarget(targetPath, expectedCanonical string, canonicalValid bool, canonicalError, targetKind, scope string, isSameObject bool) ObservedLink {
	o := ObservedLink{
		TargetPath: targetPath, EntryType: "missing", ExpectedCanonical: expectedCanonical,
		CanonicalValid: canonicalValid, CanonicalError: canonicalError,
		IsSameObject: isSameObject, TargetKind: targetKind, Scope: scope,
	}
	switch {
	case isSymlink(targetPath):
		o.EntryType = "symlink"
		o.ResolvedLinkTarget = resolveSymlinkTarget(targetPath)
		if raw, err := os.Readlink(targetPath); err == nil {
			o.RawLinkTarget = joinRaw(filepath.Dir(targetPath), raw)
		}
		if expectedCanonical != "" {
			if o.ResolvedLinkTarget != "" && pointsTo(o.ResolvedLinkTarget, expectedCanonical) {
				o.LinkPointsToCanonical = true
			}
			if !o.LinkPointsToCanonical && o.RawLinkTarget != "" && pointsTo(o.RawLinkTarget, expectedCanonical) {
				o.LinkPointsToCanonical = true
			}
		}
	case isDir(targetPath):
		o.EntryType = "dir"
	case isFile(targetPath):
		o.EntryType = "file"
	case !exists(targetPath):
		o.EntryType = "missing"
	default:
		o.EntryType = "unsupported"
	}
	return o
}

// observedDest is `raw_link_target or resolved_link_target or "unknown"`.
func observedDest(o ObservedLink) string {
	if o.RawLinkTarget != "" {
		return o.RawLinkTarget
	}
	if o.ResolvedLinkTarget != "" {
		return o.ResolvedLinkTarget
	}
	return "unknown"
}

// PlanLinkOptions are plan_link_target's keyword arguments.
type PlanLinkOptions struct {
	AvailabilityStatus string // default "installed"
	ParentExists       *bool
	HasStateRecord     bool
	IsLegacyContainer  bool
	ResourceName       string
}

func ifName(name, withName, without string) string {
	if name != "" {
		return withName
	}
	return without
}

func planLinkTargetImpl(o ObservedLink, desiredMode string, opts PlanLinkOptions) LinkOperation {
	target := o.TargetPath
	canonical := o.ExpectedCanonical
	name := opts.ResourceName
	resLabel := ""
	if name != "" {
		resLabel = fmt.Sprintf(" for '%s'", name)
	}
	avail := opts.AvailabilityStatus
	if avail == "" {
		avail = "installed"
	}
	op := func(action, rule string) LinkOperation { return newLinkOp(action, rule, target, canonical) }

	// 1. Container disposition
	if opts.IsLegacyContainer || o.TargetKind == "managed_container" {
		switch o.EntryType {
		case "symlink":
			if o.LinkPointsToCanonical {
				r := op("MIGRATE_CONTAINER", "INV-GLB-04")
				r.Reason = fmt.Sprintf("Migrate legacy container symlink at %s to directory", target)
				r.ExpectedRepresentation, r.DesiredRepresentation = "symlink", "dir"
				return r
			}
			dest := observedDest(o)
			isSub := false
			if canonical != "" {
				d := normcase(physical(dest))
				c := normcase(physical(canonical))
				isSub = d != c && (strings.HasPrefix(d, c+string(filepath.Separator)) || isRelativeTo(physical(dest), physical(canonical)))
			}
			detail := "points outside current workspace"
			if isSub {
				detail = "points to a subpath instead of skills root"
			}
			r := op("CONFLICT", "INV-GLB-04")
			r.Reason = fmt.Sprintf("Target preserved: %s. Current legacy container symlink %s: %s, expected: %s. "+
				"Legacy container migration only allows exact root link to current workspace skills; "+
				"inspect manually, then run 'aikito sync global' again.", target, detail, dest, pyPath(canonical))
			r.Finding = fmt.Sprintf("Legacy container symlink %s: %s -> %s", detail, target, dest)
			r.ExpectedRepresentation, r.DesiredRepresentation = "symlink", "dir"
			r.IsAuthorized = false
			return r
		case "dir":
			r := op("NOOP", "INV-GLB-04")
			r.Reason = "Managed container directory already exists"
			r.ExpectedRepresentation, r.DesiredRepresentation = "dir", "dir"
			return r
		case "missing":
			r := op("CREATE", "INV-GLB-04")
			r.Reason = fmt.Sprintf("Create managed container directory: %s", target)
			r.ExpectedRepresentation, r.DesiredRepresentation = "missing", "dir"
			return r
		}
		r := op("CONFLICT", "INV-GLB-04")
		r.Reason = fmt.Sprintf("Target preserved: %s. Container path is an invalid entry type '%s' (expected directory). "+
			"Will not overwrite automatically; inspect manually, then run 'aikito sync global' again.", target, o.EntryType)
		r.Finding = fmt.Sprintf("Container path is invalid entry type: %s", target)
		r.ExpectedRepresentation, r.DesiredRepresentation = o.EntryType, "dir"
		r.IsAuthorized = false
		return r
	}

	// 2. Same-object disposition
	if o.IsSameObject && o.EntryType != "symlink" {
		r := op("SHARED_PATH", "INV-GLB-06")
		r.Reason = "Target path is the same physical object as canonical container; no link required" + resLabel
		r.ExpectedRepresentation, r.DesiredRepresentation = o.EntryType, o.EntryType
		r.IsSameObject = true
		return r
	}

	// 3. Selected link
	if desiredMode == "link" {
		if !o.CanonicalValid {
			if o.EntryType == "symlink" && o.LinkPointsToCanonical {
				r := op("CONFLICT", "INV-TR-04")
				r.Reason = ifName(name, fmt.Sprintf("Broken symbolic link points to missing canonical skill '%s'", name),
					"Broken symbolic link points to missing canonical resource")
				r.Finding = ifName(name, fmt.Sprintf("Broken symbolic link points to missing canonical skill: %s", target),
					fmt.Sprintf("Broken symbolic link points to missing canonical resource: %s", target))
				r.ExpectedRepresentation = "symlink"
				r.IsAuthorized = false
				return r
			}
			errMsg := o.CanonicalError
			if errMsg == "" {
				errMsg = ifName(name, fmt.Sprintf("Canonical skill '%s' is missing or unreadable", name), "Canonical source is missing or unreadable")
			}
			r := op("CONFLICT", "INV-TR-02")
			r.Reason = ifName(name, "Canonical skill source missing or unreadable: "+errMsg, "Canonical source missing or unreadable: "+errMsg)
			r.Finding = r.Reason
			r.ExpectedRepresentation = o.EntryType
			r.IsAuthorized = false
			return r
		}

		requiresParent := false
		if o.TargetKind == "consumer_link" || o.TargetKind == "instruction_link" {
			parent := filepath.Dir(target)
			pExists := exists(parent)
			if opts.ParentExists != nil {
				pExists = *opts.ParentExists
			}
			if !pExists {
				ruleSkip := "INV-GLB-05"
				if o.TargetKind == "instruction_link" {
					ruleSkip = "INV-INST-07"
				}
				if avail == "not_installed" {
					r := op("SKIP", ruleSkip)
					r.Reason = ifName(name, fmt.Sprintf("%s not detected: %s", name, parent), fmt.Sprintf("Agent not detected: %s", parent))
					return r
				}
				if avail == "unknown" {
					r := op("SKIP", ruleSkip)
					r.Reason = fmt.Sprintf("Agent installation unknown; parent directory does not exist: %s", parent)
					r.Finding = fmt.Sprintf("Agent installation unknown and parent missing: %s", parent)
					return r
				}
				requiresParent = true
			}
		}

		switch o.EntryType {
		case "missing":
			r := op("CREATE", "INV-TR-01")
			r.Reason = ifName(name, fmt.Sprintf("Create symbolic link for skill '%s'", name),
				fmt.Sprintf("Create symbolic link: %s -> %s", target, pyPath(canonical)))
			r.RequiresParentCreation = requiresParent
			return r
		case "symlink":
			if o.LinkPointsToCanonical {
				r := op("NOOP", "INV-TR-03")
				r.Reason = ifName(name, fmt.Sprintf("Symbolic link for skill '%s' already points to canonical resource", name),
					"Symbolic link already points to canonical resource")
				r.ExpectedRepresentation = "link"
				return r
			}
			dest := observedDest(o)
			rule := "INV-TR-05"
			reason := fmt.Sprintf("Target preserved: %s. Symbolic link points to unauthorized destination: %s (expected %s). "+
				"Other workspace or unmanaged skill symlink will not be overwritten automatically; "+
				"inspect manually, then run 'aikito sync global' again.", target, dest, pyPath(canonical))
			if o.TargetKind == "consumer_link" {
				rule = "INV-GLB-05"
				reason = fmt.Sprintf("Target preserved: %s. Symbolic link points to unauthorized destination: %s (expected %s). "+
					"External or unexpected consumer symlink will not be overwritten automatically; "+
					"inspect manually, then run 'aikito sync global' again.", target, dest, pyPath(canonical))
			} else if opts.HasStateRecord {
				rule = "INV-TR-06"
			}
			r := op("CONFLICT", rule)
			r.Reason = reason
			r.Finding = fmt.Sprintf("Symbolic link points to unauthorized destination: %s -> %s", target, dest)
			r.ExpectedRepresentation = "symlink"
			r.IsAuthorized = false
			return r
		case "dir":
			rule := "INV-GLB-02"
			if o.TargetKind != "managed_entry" {
				rule = "INV-GLB-01"
			}
			r := op("CONFLICT", rule)
			if o.Scope == "global" {
				r.Reason = fmt.Sprintf("Target preserved: %s. Target is a regular directory (expected symlink to %s). "+
					"Unmanaged or matching directory will not be overwritten automatically; "+
					"move or merge it manually, then run 'aikito sync global' again.", target, pyPath(canonical))
			} else {
				r.Reason = ifName(name, fmt.Sprintf("Cannot switch copy to link for skill '%s': directory is drifted, unmanaged, or inactive", name),
					fmt.Sprintf("Target path is an unmanaged directory: %s", target))
			}
			r.Finding = ifName(name, fmt.Sprintf("Cannot switch copy to link for skill '%s': %s is not an unchanged active copy", name, target),
				fmt.Sprintf("Target path is an unmanaged directory: %s", target))
			r.ExpectedRepresentation = ifName(name, "copy", "dir")
			r.IsAuthorized = false
			return r
		}
		r := op("CONFLICT", "INV-TR-13")
		if o.Scope == "global" {
			r.Reason = fmt.Sprintf("Target preserved: %s. Target is an unexpected entry type '%s' (expected link to %s). "+
				"Will not overwrite automatically; inspect manually, then run 'aikito sync global' again.", target, o.EntryType, pyPath(canonical))
		} else {
			r.Reason = ifName(name, fmt.Sprintf("Unsupported target filesystem entry for skill '%s'", name),
				fmt.Sprintf("Unsupported target filesystem entry: %s", target))
		}
		r.Finding = fmt.Sprintf("Unsupported target filesystem entry: %s", target)
		r.ExpectedRepresentation = "unsupported"
		r.IsAuthorized = false
		return r
	}

	// 4. Deselected (desired_mode == "absent")
	absent := func(action, rule, exp string) LinkOperation {
		r := op(action, rule)
		r.ExpectedRepresentation, r.DesiredRepresentation = exp, "absent"
		return r
	}
	switch o.EntryType {
	case "symlink":
		if o.LinkPointsToCanonical {
			r := absent("UNLINK", "INV-TR-14", "link")
			r.Reason = ifName(name, fmt.Sprintf("Remove deselected symbolic link for skill '%s'", name),
				fmt.Sprintf("Remove deselected symbolic link: %s", target))
			return r
		}
		dest := observedDest(o)
		if o.Scope == "global" {
			r := absent("CONFLICT", "INV-GLB-03", "symlink")
			r.Reason = fmt.Sprintf("Target preserved: %s. Stale entry symlink destination: %s, expected: %s. "+
				"Other workspace or unmanaged skill symlink will not be deleted automatically; "+
				"inspect manually, then run 'aikito sync global' again.", target, dest, pyPath(canonical))
			r.Finding = fmt.Sprintf("Unmanaged global skill item: %s", target)
			r.IsAuthorized = false
			return r
		}
		r := absent("NOOP", "INV-TR-15", "symlink")
		r.Reason = ifName(name, fmt.Sprintf("Preserve unmanaged symbolic link for deselected skill '%s'", name),
			"Preserve unmanaged symbolic link for deselected entry")
		return r
	case "dir":
		if o.Scope == "global" {
			r := absent("CONFLICT", "INV-GLB-03", "dir")
			r.Reason = fmt.Sprintf("Target preserved: %s. Stale target is a regular directory (expected symlink to %s). "+
				"Matching directory is not owned by link-only global skills and will not be deleted; "+
				"inspect or remove manually, then run 'aikito sync global' again.", target, pyPath(canonical))
			r.Finding = fmt.Sprintf("Stale target is a regular directory: %s", target)
			r.IsAuthorized = false
			return r
		}
		r := absent("NOOP", "INV-TR-17", ifName(name, "copy", "dir"))
		r.Reason = ifName(name, fmt.Sprintf("Preserve unmanaged directory for deselected skill '%s'", name),
			"Preserve unmanaged directory for deselected entry")
		return r
	case "missing":
		rule := "INV-TR-17"
		if o.Scope == "global" {
			rule = "INV-GLB-03"
		}
		r := absent("NOOP", rule, "missing")
		r.Reason = "Entry is already absent" + resLabel
		return r
	}
	if o.Scope == "global" {
		r := absent("CONFLICT", "INV-GLB-03", o.EntryType)
		r.Reason = fmt.Sprintf("Target preserved: %s. Stale target is an unexpected entry type '%s'. "+
			"Unmanaged item will not be deleted automatically; inspect manually, then run 'aikito sync global' again.", target, o.EntryType)
		r.Finding = fmt.Sprintf("Unmanaged global skill item: %s", target)
		r.IsAuthorized = false
		return r
	}
	r := absent("NOOP", "INV-TR-15", o.EntryType)
	r.Reason = fmt.Sprintf("Preserve unmanaged entry of type %s%s", o.EntryType, resLabel)
	return r
}

// PlanLinkTarget is link.py's plan_link_target.
func PlanLinkTarget(o ObservedLink, desiredMode string, opts PlanLinkOptions) LinkOperation {
	op := planLinkTargetImpl(o, desiredMode, opts)
	op.TargetKind = o.TargetKind
	if opts.IsLegacyContainer {
		op.TargetKind = "managed_container"
	}
	op.ResourceName = opts.ResourceName
	return op
}

// pyPath renders an optional path the way an f-string renders a Path or
// None.
func pyPath(p string) string {
	if p == "" {
		return "None"
	}
	return p
}

// linkOwned reports whether the symlink at target points at canonical
// (resolved or raw), the ownership check apply_link_operation repeats.
func linkOwned(target, canonical string) bool {
	if canonical == "" {
		return false
	}
	canon := normcase(physical(canonical))
	resolved := resolveSymlinkTarget(target)
	if resolved != "" && normcase(physical(resolved)) == canon {
		return true
	}
	if raw, err := os.Readlink(target); err == nil && raw != "" {
		return normcase(physical(joinRaw(filepath.Dir(target), raw))) == canon
	}
	return false
}

// LinkResult is link.py's LinkExecutionResult.
type LinkResult struct {
	Success      bool
	Applied      bool
	ErrorMessage string
}

func linkFail(msg string) LinkResult { return LinkResult{ErrorMessage: msg} }

// ApplyLinkOperation is link.py's apply_link_operation.
func ApplyLinkOperation(out Out, op LinkOperation, dryRun, verbose bool) LinkResult {
	target := op.TargetPath
	canonical := op.CanonicalPath

	switch op.Action {
	case "SHARED_PATH":
		report := func() {
			switch {
			case op.TargetKind == "consumer_link":
				out.println("[OK] %s skills: shared path %s", op.ResourceName, target)
			case op.TargetKind == "instruction_link":
				out.println("[OK] %s instructions: shared path %s", op.ResourceName, target)
			case verbose:
				out.println("[OK] shared path %s", target)
			}
		}
		if dryRun {
			report()
			return LinkResult{Success: true}
		}
		if isSymlink(target) {
			return linkFail(fmt.Sprintf("Preflight failed: shared path %s is a symlink (stale plan)", target))
		}
		if canonical == "" || !exists(canonical) {
			return linkFail(fmt.Sprintf("Preflight failed: canonical source does not exist: %s (stale plan)", pyPath(canonical)))
		}
		if !exists(target) || !isSameTargetLocation(target, canonical) {
			return linkFail(fmt.Sprintf("Preflight failed: shared path %s no longer matches canonical %s (stale plan)", target, canonical))
		}
		report()
		return LinkResult{Success: true}

	case "NOOP":
		switch {
		case op.DesiredRepresentation == "link":
			if !isSymlink(target) {
				return linkFail(fmt.Sprintf("Preflight failed: target is no longer a symlink: %s (stale plan)", target))
			}
			if !linkOwned(target, canonical) {
				return linkFail(fmt.Sprintf("Preflight failed: symlink %s no longer points to canonical %s (stale plan)", target, pyPath(canonical)))
			}
		case op.DesiredRepresentation == "dir":
			if !(isDir(target) && !isSymlink(target)) {
				kind := "target"
				if op.TargetKind == "managed_container" || op.TargetKind == "container_link" || strings.Contains(strings.ToLower(op.Reason), "container") {
					kind = "managed container"
				}
				return linkFail(fmt.Sprintf("Preflight failed: %s is no longer a directory: %s (stale plan)", kind, target))
			}
		case op.ExpectedRepresentation == "missing":
			if lexists(target) {
				return linkFail(fmt.Sprintf("Preflight failed: expected %s to be missing but entry exists (stale plan)", target))
			}
		case op.ExpectedRepresentation == "file":
			if !(isFile(target) && !isSymlink(target)) {
				return linkFail(fmt.Sprintf("Preflight failed: expected %s to be a regular file: %s (stale plan)", target, target))
			}
		case op.ExpectedRepresentation == "symlink":
			if !isSymlink(target) {
				return linkFail(fmt.Sprintf("Preflight failed: expected %s to be a symlink (stale plan)", target))
			}
		}
		if op.TargetKind == "consumer_link" {
			out.println("[OK] %s skills: %s -> %s", op.ResourceName, target, pyPath(canonical))
		} else if op.TargetKind == "instruction_link" && (verbose || op.Reason != "") {
			out.println("[OK] %s instructions: %s -> %s", op.ResourceName, target, pyPath(canonical))
		}
		return LinkResult{Success: true}

	case "SKIP":
		if op.Reason != "" {
			out.println("[SKIP] %s", op.Reason)
		}
		return LinkResult{Success: true}

	case "CONFLICT":
		return linkFail(op.Reason)

	case "MIGRATE_CONTAINER":
		if !isSymlink(target) {
			return linkFail(fmt.Sprintf("Preflight failed: container is no longer a symlink: %s", target))
		}
		resolved := resolveSymlinkTarget(target)
		if canonical != "" && resolved != "" && normcase(physical(resolved)) != normcase(physical(canonical)) {
			return linkFail(fmt.Sprintf("Preflight failed: container symlink changed destination: %s -> %s", target, resolved))
		}
		out.println("[INFO] Replacing old top-level symlink at %s with directory", target)
		if !dryRun {
			if err := os.Remove(target); err == nil {
				err = os.MkdirAll(target, 0o777)
				if err != nil {
					return linkFail(fmt.Sprintf("Failed to migrate container %s: %s", target, pyOSError(err)))
				}
			} else {
				return linkFail(fmt.Sprintf("Failed to migrate container %s: %s", target, pyOSError(err)))
			}
		}
		return LinkResult{Success: true, Applied: !dryRun}

	case "CREATE":
		if op.DesiredRepresentation == "dir" {
			if !dryRun {
				if err := os.MkdirAll(target, 0o777); err != nil {
					return linkFail(fmt.Sprintf("Failed to create directory %s: %s", target, pyOSError(err)))
				}
			}
			return LinkResult{Success: true, Applied: !dryRun}
		}
		if dryRun {
			switch op.TargetKind {
			case "consumer_link":
				out.println("[DRY RUN LINK] %s skills: %s -> %s", op.ResourceName, target, pyPath(canonical))
			case "instruction_link":
				out.println("[DRY RUN LINK] %s instructions: %s -> %s", op.ResourceName, target, pyPath(canonical))
			default:
				out.println("[DRY RUN LINK] %s -> %s", pyPath(canonical), target)
			}
			return LinkResult{Success: true}
		}
		if canonical == "" || !exists(canonical) {
			return linkFail(fmt.Sprintf("Preflight failed: canonical source does not exist: %s (stale plan)", pyPath(canonical)))
		}
		if op.TargetKind == "managed_entry" && !isDir(canonical) {
			return linkFail(fmt.Sprintf("Preflight failed: canonical skill source is not a directory: %s (stale plan)", canonical))
		}
		if op.TargetKind == "consumer_link" && !(isDir(canonical) && !isSymlink(canonical)) {
			return linkFail(fmt.Sprintf("Preflight failed: managed container %s is not a valid directory (stale plan)", canonical))
		}
		if op.TargetKind == "instruction_link" && !isFile(canonical) {
			return linkFail(fmt.Sprintf("Preflight failed: canonical instruction source is not a file: %s (stale plan)", canonical))
		}
		if op.TargetKind == "managed_entry" {
			container := filepath.Dir(target)
			if !(isDir(container) && !isSymlink(container)) {
				return linkFail(fmt.Sprintf("Preflight failed: managed container %s is not a valid directory (stale plan)", container))
			}
		}
		if (op.TargetKind == "consumer_link" || op.TargetKind == "instruction_link") && !op.RequiresParentCreation {
			if !exists(filepath.Dir(target)) {
				return linkFail(fmt.Sprintf("Preflight failed: consumer parent directory missing for %s (stale plan)", target))
			}
		}
		if lexists(target) {
			return linkFail(fmt.Sprintf("Preflight failed: target already exists or changed: %s (stale plan)", target))
		}
		if op.RequiresParentCreation {
			curr := filepath.Dir(target)
			for !exists(curr) && filepath.Dir(curr) != curr {
				curr = filepath.Dir(curr)
			}
			if isSymlink(curr) {
				switch filepath.Base(curr) {
				case ".agents", "memory", "skills":
					return linkFail(fmt.Sprintf("Preflight failed: parent directory is a symlink: %s", curr))
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
				return linkFail(fmt.Sprintf("Failed to create symlink: %s", pyOSError(err)))
			}
		}
		if !compat.CanSymlink() {
			return linkFail(fmt.Sprintf("Failed to create symlink: %s -> %s", target, canonical))
		}
		if !safeSymlink(out, canonical, target) {
			return linkFail(fmt.Sprintf("Failed to create symlink: %s -> %s", target, canonical))
		}
		switch op.TargetKind {
		case "consumer_link":
			out.println("[LINK] %s skills: %s -> %s", op.ResourceName, target, canonical)
		case "instruction_link":
			out.println("[LINK] %s instructions: %s -> %s", op.ResourceName, target, canonical)
		default:
			out.println("[LINK] %s -> %s", target, canonical)
		}
		return LinkResult{Success: true, Applied: true}

	case "UNLINK":
		if !isSymlink(target) {
			return linkFail(fmt.Sprintf("Preflight failed: target is not a symlink: %s", target))
		}
		if !linkOwned(target, canonical) {
			return linkFail(fmt.Sprintf("Preflight failed: link %s no longer points to canonical %s", target, pyPath(canonical)))
		}
		if dryRun {
			out.println("[DRY RUN CLEANUP] Would remove stale managed item: %s", target)
			return LinkResult{Success: true}
		}
		if err := os.Remove(target); err != nil {
			return linkFail(fmt.Sprintf("Failed to remove stale link %s: %s", target, pyOSError(err)))
		}
		out.println("[CLEANUP] Removed stale managed item: %s", target)
		return LinkResult{Success: true, Applied: true}
	}
	return linkFail(fmt.Sprintf("Unknown link action: %s", op.Action))
}
