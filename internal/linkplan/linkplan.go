// Package linkplan ports aikito/link.py: observe a symlink/container target
// without side effects (Inspect), decide what to do about it (Plan), and
// carry the decision out with preflight re-checks (Apply). The messages,
// rule IDs and printed lines match the reference implementation exactly,
// because they surface verbatim in `aikito sync global` output.
package linkplan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
)

// Entry types reported by Inspect.
const (
	EntryMissing     = "missing"
	EntrySymlink     = "symlink"
	EntryDir         = "dir"
	EntryFile        = "file"
	EntryUnsupported = "unsupported"
)

// Target kinds.
const (
	ManagedEntry     = "managed_entry"
	ManagedContainer = "managed_container"
	ConsumerLink     = "consumer_link"
	InstructionLink  = "instruction_link"
)

// Actions.
const (
	ActCreate           = "CREATE"
	ActUnlink           = "UNLINK"
	ActNoop             = "NOOP"
	ActConflict         = "CONFLICT"
	ActSharedPath       = "SHARED_PATH"
	ActSkip             = "SKIP"
	ActMigrateContainer = "MIGRATE_CONTAINER"
)

// ObservedLink is link.py's ObservedLink: filesystem facts about a target.
type ObservedLink struct {
	TargetPath            string
	EntryType             string
	ExpectedCanonical     string // "" = None
	CanonicalValid        bool
	CanonicalError        string
	RawLinkTarget         string // "" = None
	ResolvedLinkTarget    string // "" = None
	LinkPointsToCanonical bool
	IsSameObject          bool
	TargetKind            string
	Scope                 string
}

// Operation is link.py's LinkOperation.
type Operation struct {
	Action                 string
	RuleID                 string
	TargetPath             string
	CanonicalPath          string // "" = None
	Reason                 string
	Finding                string // "" = None
	IsAuthorized           bool
	ExpectedRepresentation string
	DesiredRepresentation  string
	RequiresParentCreation bool
	IsSameObject           bool
	TargetKind             string
	ResourceName           string
}

// InspectOptions are inspect_link_target's keyword arguments. The zero
// value is not Python's default: callers set CanonicalValid explicitly
// (Python defaults it to True), see Inspect.
type InspectOptions struct {
	CanonicalInvalid bool // canonical_valid=False
	CanonicalError   string
	TargetKind       string // default managed_entry
	Scope            string // default project
	IsSameObject     bool
}

// pathlibJoin is `Path(dir) / rel` for a relative rel: pathlib collapses
// empty and "." components but, unlike filepath.Join, keeps "..".
func pathlibJoin(dir, rel string) string {
	if filepath.IsAbs(rel) {
		return pathlibNorm(rel)
	}
	return pathlibNorm(dir + string(filepath.Separator) + rel)
}

func pathlibNorm(p string) string {
	sep := string(filepath.Separator)
	abs := strings.HasPrefix(p, sep)
	var parts []string
	for _, part := range strings.Split(p, sep) {
		if part == "" || part == "." {
			continue
		}
		parts = append(parts, part)
	}
	out := strings.Join(parts, sep)
	if abs {
		return sep + out
	}
	if out == "" {
		return "."
	}
	return out
}

func isSymlink(p string) bool {
	info, err := os.Lstat(p)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func normcase(p string) string {
	if compat.IsWindows() {
		return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	}
	return p
}

func physical(p string) string { return normcase(compat.PhysicalPath(p)) }

// ResolveSymlinkTarget ports compat.py's resolve_symlink_target.
func ResolveSymlinkTarget(path string) string {
	var target string
	if raw, err := os.Readlink(path); err == nil {
		if strings.HasPrefix(raw, `\\?\UNC\`) {
			raw = `\\` + raw[8:]
		} else if strings.HasPrefix(raw, `\\?\`) {
			raw = raw[4:]
		}
		target = pathlibJoin(filepath.Dir(path), raw)
	} else {
		target = compat.PhysicalPath(path)
	}
	var parts []string
	curr := target
	for !exists(curr) && filepath.Dir(curr) != curr {
		parts = append(parts, filepath.Base(curr))
		curr = filepath.Dir(curr)
	}
	resolved := compat.PhysicalPath(curr)
	for i := len(parts) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, parts[i])
	}
	return resolved
}

// IsSameTargetLocation ports compat.py's is_same_target_location: the
// parents are compared physically, the final component literally (so the
// target's own symlink is not followed).
func IsSameTargetLocation(p1, p2 string) bool {
	d1 := compat.PhysicalPath(filepath.Dir(p1))
	d2 := compat.PhysicalPath(filepath.Dir(p2))
	probe := d1
	for !exists(probe) && filepath.Dir(probe) != probe {
		probe = filepath.Dir(probe)
	}
	folds := compat.IsWindows()
	if exists(probe) {
		folds = compat.DirectoryFoldsCase(probe)
	}
	if folds {
		return strings.EqualFold(d1, d2) && strings.EqualFold(filepath.Base(p1), filepath.Base(p2))
	}
	return d1 == d2 && filepath.Base(p1) == filepath.Base(p2)
}

// Inspect ports inspect_link_target.
func Inspect(targetPath, expectedCanonical string, opts InspectOptions) ObservedLink {
	kind := opts.TargetKind
	if kind == "" {
		kind = ManagedEntry
	}
	scope := opts.Scope
	if scope == "" {
		scope = "project"
	}
	obs := ObservedLink{
		TargetPath:        targetPath,
		EntryType:         EntryMissing,
		ExpectedCanonical: expectedCanonical,
		CanonicalValid:    !opts.CanonicalInvalid,
		CanonicalError:    opts.CanonicalError,
		IsSameObject:      opts.IsSameObject,
		TargetKind:        kind,
		Scope:             scope,
	}
	switch {
	case isSymlink(targetPath):
		obs.EntryType = EntrySymlink
		obs.ResolvedLinkTarget = ResolveSymlinkTarget(targetPath)
		if raw, err := os.Readlink(targetPath); err == nil {
			obs.RawLinkTarget = pathlibJoin(filepath.Dir(targetPath), raw)
		}
		if expectedCanonical != "" {
			want := physical(expectedCanonical)
			if obs.ResolvedLinkTarget != "" && physical(obs.ResolvedLinkTarget) == want {
				obs.LinkPointsToCanonical = true
			}
			if !obs.LinkPointsToCanonical && obs.RawLinkTarget != "" && physical(obs.RawLinkTarget) == want {
				obs.LinkPointsToCanonical = true
			}
		}
	case isDir(targetPath):
		obs.EntryType = EntryDir
	case isFile(targetPath):
		obs.EntryType = EntryFile
	case !exists(targetPath):
		obs.EntryType = EntryMissing
	default:
		obs.EntryType = EntryUnsupported
	}
	return obs
}

// PlanOptions are plan_link_target's keyword arguments.
type PlanOptions struct {
	AvailabilityStatus string // default "installed"
	ParentExists       *bool  // nil = check the filesystem
	HasStateRecord     bool
	IsLegacyContainer  bool
	ResourceName       string
}

func (o ObservedLink) dest() string {
	if o.RawLinkTarget != "" {
		return o.RawLinkTarget
	}
	if o.ResolvedLinkTarget != "" {
		return o.ResolvedLinkTarget
	}
	return "unknown"
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Plan ports plan_link_target.
func Plan(observed ObservedLink, desiredMode string, opts PlanOptions) Operation {
	op := planImpl(observed, desiredMode, opts)
	op.TargetKind = observed.TargetKind
	if opts.IsLegacyContainer {
		op.TargetKind = ManagedContainer
	}
	op.ResourceName = opts.ResourceName
	return op
}

func planImpl(observed ObservedLink, desiredMode string, opts PlanOptions) Operation {
	target := observed.TargetPath
	canonical := observed.ExpectedCanonical
	name := opts.ResourceName
	resLabel := ""
	if name != "" {
		resLabel = fmt.Sprintf(" for '%s'", name)
	}
	availability := or(opts.AvailabilityStatus, "installed")
	base := Operation{TargetPath: target, CanonicalPath: canonical}
	mk := func(action, rule, reason, finding, expected, desired string, authorized bool) Operation {
		op := base
		op.Action, op.RuleID, op.Reason, op.Finding = action, rule, reason, finding
		op.ExpectedRepresentation, op.DesiredRepresentation, op.IsAuthorized = expected, desired, authorized
		return op
	}

	// 1. Container disposition
	if opts.IsLegacyContainer || observed.TargetKind == ManagedContainer {
		switch observed.EntryType {
		case EntrySymlink:
			if observed.LinkPointsToCanonical {
				return mk(ActMigrateContainer, "INV-GLB-04",
					fmt.Sprintf("Migrate legacy container symlink at %s to directory", target),
					"", "symlink", "dir", true)
			}
			dest := observed.dest()
			isSub := false
			if canonical != "" {
				d, c := physical(dest), physical(canonical)
				isSub = d != c && strings.HasPrefix(d, c+string(filepath.Separator))
			}
			detail := "points outside current workspace"
			if isSub {
				detail = "points to a subpath instead of skills root"
			}
			return mk(ActConflict, "INV-GLB-04",
				fmt.Sprintf("Target preserved: %s. Current legacy container symlink %s: %s, expected: %s. "+
					"Legacy container migration only allows exact root link to current workspace skills; "+
					"inspect manually, then run 'aikito sync global' again.", target, detail, dest, canonical),
				fmt.Sprintf("Legacy container symlink %s: %s -> %s", detail, target, dest),
				"symlink", "dir", false)
		case EntryDir:
			return mk(ActNoop, "INV-GLB-04", "Managed container directory already exists", "", "dir", "dir", true)
		case EntryMissing:
			return mk(ActCreate, "INV-GLB-04",
				fmt.Sprintf("Create managed container directory: %s", target), "", "missing", "dir", true)
		}
		return mk(ActConflict, "INV-GLB-04",
			fmt.Sprintf("Target preserved: %s. Container path is an invalid entry type '%s' (expected directory). "+
				"Will not overwrite automatically; inspect manually, then run 'aikito sync global' again.",
				target, observed.EntryType),
			fmt.Sprintf("Container path is invalid entry type: %s", target),
			observed.EntryType, "dir", false)
	}

	// 2. Same-object disposition
	if observed.IsSameObject && observed.EntryType != EntrySymlink {
		op := mk(ActSharedPath, "INV-GLB-06",
			fmt.Sprintf("Target path is the same physical object as canonical container; no link required%s", resLabel),
			"", observed.EntryType, observed.EntryType, true)
		op.IsSameObject = true
		return op
	}

	// 3. Selected link
	if desiredMode == "link" {
		if !observed.CanonicalValid {
			if observed.EntryType == EntrySymlink && observed.LinkPointsToCanonical {
				reason := "Broken symbolic link points to missing canonical resource"
				finding := fmt.Sprintf("Broken symbolic link points to missing canonical resource: %s", target)
				if name != "" {
					reason = fmt.Sprintf("Broken symbolic link points to missing canonical skill '%s'", name)
					finding = fmt.Sprintf("Broken symbolic link points to missing canonical skill: %s", target)
				}
				return mk(ActConflict, "INV-TR-04", reason, finding, "symlink", "link", false)
			}
			errText := observed.CanonicalError
			if errText == "" {
				errText = "Canonical source is missing or unreadable"
				if name != "" {
					errText = fmt.Sprintf("Canonical skill '%s' is missing or unreadable", name)
				}
			}
			msg := fmt.Sprintf("Canonical source missing or unreadable: %s", errText)
			if name != "" {
				msg = fmt.Sprintf("Canonical skill source missing or unreadable: %s", errText)
			}
			return mk(ActConflict, "INV-TR-02", msg, msg, observed.EntryType, "link", false)
		}

		requiresParent := false
		if observed.TargetKind == ConsumerLink || observed.TargetKind == InstructionLink {
			parent := filepath.Dir(target)
			pExists := exists(parent)
			if opts.ParentExists != nil {
				pExists = *opts.ParentExists
			}
			if !pExists {
				ruleSkip := "INV-GLB-05"
				if observed.TargetKind == InstructionLink {
					ruleSkip = "INV-INST-07"
				}
				if availability == "not_installed" {
					reason := fmt.Sprintf("Agent not detected: %s", parent)
					if name != "" {
						reason = fmt.Sprintf("%s not detected: %s", name, parent)
					}
					return mk(ActSkip, ruleSkip, reason, "", "missing", "link", true)
				}
				if availability == "unknown" {
					return mk(ActSkip, ruleSkip,
						fmt.Sprintf("Agent installation unknown; parent directory does not exist: %s", parent),
						fmt.Sprintf("Agent installation unknown and parent missing: %s", parent),
						"missing", "link", true)
				}
				requiresParent = true
			}
		}

		switch observed.EntryType {
		case EntryMissing:
			reason := fmt.Sprintf("Create symbolic link: %s -> %s", target, canonical)
			if name != "" {
				reason = fmt.Sprintf("Create symbolic link for skill '%s'", name)
			}
			op := mk(ActCreate, "INV-TR-01", reason, "", "missing", "link", true)
			op.RequiresParentCreation = requiresParent
			return op
		case EntrySymlink:
			if observed.LinkPointsToCanonical {
				reason := "Symbolic link already points to canonical resource"
				if name != "" {
					reason = fmt.Sprintf("Symbolic link for skill '%s' already points to canonical resource", name)
				}
				return mk(ActNoop, "INV-TR-03", reason, "", "link", "link", true)
			}
			dest := observed.dest()
			rule := "INV-TR-05"
			reason := fmt.Sprintf("Target preserved: %s. Symbolic link points to unauthorized destination: %s (expected %s). "+
				"Other workspace or unmanaged skill symlink will not be overwritten automatically; "+
				"inspect manually, then run 'aikito sync global' again.", target, dest, canonical)
			if observed.TargetKind == ConsumerLink {
				rule = "INV-GLB-05"
				reason = fmt.Sprintf("Target preserved: %s. Symbolic link points to unauthorized destination: %s (expected %s). "+
					"External or unexpected consumer symlink will not be overwritten automatically; "+
					"inspect manually, then run 'aikito sync global' again.", target, dest, canonical)
			} else if opts.HasStateRecord {
				rule = "INV-TR-06"
			}
			return mk(ActConflict, rule, reason,
				fmt.Sprintf("Symbolic link points to unauthorized destination: %s -> %s", target, dest),
				"symlink", "link", false)
		case EntryDir:
			rule := "INV-GLB-02"
			if observed.TargetKind != ManagedEntry {
				rule = "INV-GLB-01"
			}
			var reason string
			switch {
			case observed.Scope == "global":
				reason = fmt.Sprintf("Target preserved: %s. Target is a regular directory (expected symlink to %s). "+
					"Unmanaged or matching directory will not be overwritten automatically; "+
					"move or merge it manually, then run 'aikito sync global' again.", target, canonical)
			case name != "":
				reason = fmt.Sprintf("Cannot switch copy to link for skill '%s': directory is drifted, unmanaged, or inactive", name)
			default:
				reason = fmt.Sprintf("Target path is an unmanaged directory: %s", target)
			}
			finding := fmt.Sprintf("Target path is an unmanaged directory: %s", target)
			expected := "dir"
			if name != "" {
				finding = fmt.Sprintf("Cannot switch copy to link for skill '%s': %s is not an unchanged active copy", name, target)
				expected = "copy"
			}
			return mk(ActConflict, rule, reason, finding, expected, "link", false)
		}
		var reason string
		switch {
		case observed.Scope == "global":
			reason = fmt.Sprintf("Target preserved: %s. Target is an unexpected entry type '%s' (expected link to %s). "+
				"Will not overwrite automatically; inspect manually, then run 'aikito sync global' again.",
				target, observed.EntryType, canonical)
		case name != "":
			reason = fmt.Sprintf("Unsupported target filesystem entry for skill '%s'", name)
		default:
			reason = fmt.Sprintf("Unsupported target filesystem entry: %s", target)
		}
		return mk(ActConflict, "INV-TR-13", reason,
			fmt.Sprintf("Unsupported target filesystem entry: %s", target), "unsupported", "link", false)
	}

	// 4. Deselected link (desired_mode == "absent")
	switch observed.EntryType {
	case EntrySymlink:
		if observed.LinkPointsToCanonical {
			reason := fmt.Sprintf("Remove deselected symbolic link: %s", target)
			if name != "" {
				reason = fmt.Sprintf("Remove deselected symbolic link for skill '%s'", name)
			}
			return mk(ActUnlink, "INV-TR-14", reason, "", "link", "absent", true)
		}
		dest := observed.dest()
		if observed.Scope == "global" {
			return mk(ActConflict, "INV-GLB-03",
				fmt.Sprintf("Target preserved: %s. Stale entry symlink destination: %s, expected: %s. "+
					"Other workspace or unmanaged skill symlink will not be deleted automatically; "+
					"inspect manually, then run 'aikito sync global' again.", target, dest, canonical),
				fmt.Sprintf("Unmanaged global skill item: %s", target), "symlink", "absent", false)
		}
		reason := "Preserve unmanaged symbolic link for deselected entry"
		if name != "" {
			reason = fmt.Sprintf("Preserve unmanaged symbolic link for deselected skill '%s'", name)
		}
		return mk(ActNoop, "INV-TR-15", reason, "", "symlink", "absent", true)
	case EntryDir:
		if observed.Scope == "global" {
			return mk(ActConflict, "INV-GLB-03",
				fmt.Sprintf("Target preserved: %s. Stale target is a regular directory (expected symlink to %s). "+
					"Matching directory is not owned by link-only global skills and will not be deleted; "+
					"inspect or remove manually, then run 'aikito sync global' again.", target, canonical),
				fmt.Sprintf("Stale target is a regular directory: %s", target), "dir", "absent", false)
		}
		reason := "Preserve unmanaged directory for deselected entry"
		expected := "dir"
		if name != "" {
			reason = fmt.Sprintf("Preserve unmanaged directory for deselected skill '%s'", name)
			expected = "copy"
		}
		return mk(ActNoop, "INV-TR-17", reason, "", expected, "absent", true)
	case EntryMissing:
		rule := "INV-TR-17"
		if observed.Scope == "global" {
			rule = "INV-GLB-03"
		}
		return mk(ActNoop, rule, fmt.Sprintf("Entry is already absent%s", resLabel), "", "missing", "absent", true)
	}
	if observed.Scope == "global" {
		return mk(ActConflict, "INV-GLB-03",
			fmt.Sprintf("Target preserved: %s. Stale target is an unexpected entry type '%s'. "+
				"Unmanaged item will not be deleted automatically; inspect manually, then run 'aikito sync global' again.",
				target, observed.EntryType),
			fmt.Sprintf("Unmanaged global skill item: %s", target), observed.EntryType, "absent", false)
	}
	return mk(ActNoop, "INV-TR-15",
		fmt.Sprintf("Preserve unmanaged entry of type %s%s", observed.EntryType, resLabel), "",
		observed.EntryType, "absent", true)
}

// ExecResult is link.py's LinkExecutionResult.
type ExecResult struct {
	Operation    Operation
	Success      bool
	Applied      bool
	ErrorMessage string
}

func fail(op Operation, format string, args ...any) ExecResult {
	return ExecResult{Operation: op, ErrorMessage: fmt.Sprintf(format, args...)}
}

// ownedBy reports whether the symlink at target points at canonical,
// by its resolved or raw destination.
func ownedBy(target, canonical string) bool {
	if canonical == "" {
		return false
	}
	want := physical(canonical)
	if physical(ResolveSymlinkTarget(target)) == want {
		return true
	}
	if raw, err := os.Readlink(target); err == nil && raw != "" {
		return physical(pathlibJoin(filepath.Dir(target), raw)) == want
	}
	return false
}

// Apply ports apply_link_operation. Status lines go to stdout and
// safe_symlink's failure line to stderr, as in Python.
func Apply(op Operation, dryRun, verbose bool, stdout, stderr io.Writer) ExecResult {
	target, canonical := op.TargetPath, op.CanonicalPath
	ok := func(applied bool) ExecResult { return ExecResult{Operation: op, Success: true, Applied: applied} }

	switch op.Action {
	case ActSharedPath:
		if !dryRun {
			if isSymlink(target) {
				return fail(op, "Preflight failed: shared path %s is a symlink (stale plan)", target)
			}
			if canonical == "" || !exists(canonical) {
				return fail(op, "Preflight failed: canonical source does not exist: %s (stale plan)", noneStr(canonical))
			}
			if !exists(target) || !IsSameTargetLocation(target, canonical) {
				return fail(op, "Preflight failed: shared path %s no longer matches canonical %s (stale plan)", target, canonical)
			}
		}
		switch {
		case op.TargetKind == ConsumerLink:
			fmt.Fprintf(stdout, "[OK] %s skills: shared path %s\n", op.ResourceName, target)
		case op.TargetKind == InstructionLink:
			fmt.Fprintf(stdout, "[OK] %s instructions: shared path %s\n", op.ResourceName, target)
		case verbose:
			fmt.Fprintf(stdout, "[OK] shared path %s\n", target)
		}
		return ok(false)

	case ActNoop:
		switch {
		case op.DesiredRepresentation == "link":
			if !isSymlink(target) {
				return fail(op, "Preflight failed: target is no longer a symlink: %s (stale plan)", target)
			}
			if !ownedBy(target, canonical) {
				return fail(op, "Preflight failed: symlink %s no longer points to canonical %s (stale plan)", target, noneStr(canonical))
			}
		case op.DesiredRepresentation == "dir":
			if !(isDir(target) && !isSymlink(target)) {
				kind := "target"
				if op.TargetKind == ManagedContainer || op.TargetKind == "container_link" ||
					strings.Contains(strings.ToLower(op.Reason), "container") {
					kind = "managed container"
				}
				return fail(op, "Preflight failed: %s is no longer a directory: %s (stale plan)", kind, target)
			}
		case op.ExpectedRepresentation == "missing":
			if isSymlink(target) || exists(target) {
				return fail(op, "Preflight failed: expected %s to be missing but entry exists (stale plan)", target)
			}
		case op.ExpectedRepresentation == "file":
			if !(isFile(target) && !isSymlink(target)) {
				return fail(op, "Preflight failed: expected %s to be a regular file: %s (stale plan)", target, target)
			}
		case op.ExpectedRepresentation == "symlink":
			if !isSymlink(target) {
				return fail(op, "Preflight failed: expected %s to be a symlink (stale plan)", target)
			}
		}
		if op.TargetKind == ConsumerLink {
			fmt.Fprintf(stdout, "[OK] %s skills: %s -> %s\n", op.ResourceName, target, noneStr(canonical))
		} else if op.TargetKind == InstructionLink && (verbose || op.Reason != "") {
			fmt.Fprintf(stdout, "[OK] %s instructions: %s -> %s\n", op.ResourceName, target, noneStr(canonical))
		}
		return ok(false)

	case ActSkip:
		if op.Reason != "" {
			fmt.Fprintf(stdout, "[SKIP] %s\n", op.Reason)
		}
		return ok(false)

	case ActConflict:
		return ExecResult{Operation: op, ErrorMessage: op.Reason}

	case ActMigrateContainer:
		if !isSymlink(target) {
			return fail(op, "Preflight failed: container is no longer a symlink: %s", target)
		}
		resolved := ResolveSymlinkTarget(target)
		if canonical != "" && physical(resolved) != physical(canonical) {
			return fail(op, "Preflight failed: container symlink changed destination: %s -> %s", target, resolved)
		}
		fmt.Fprintf(stdout, "[INFO] Replacing old top-level symlink at %s with directory\n", target)
		if !dryRun {
			if err := os.Remove(target); err != nil {
				return fail(op, "Failed to migrate container %s: %v", target, err)
			}
			if err := os.MkdirAll(target, 0o777); err != nil {
				return fail(op, "Failed to migrate container %s: %v", target, err)
			}
		}
		return ok(!dryRun)

	case ActCreate:
		if op.DesiredRepresentation == "dir" {
			if !dryRun {
				if err := os.MkdirAll(target, 0o777); err != nil {
					return fail(op, "Failed to create directory %s: %v", target, err)
				}
			}
			return ok(!dryRun)
		}
		if dryRun {
			switch op.TargetKind {
			case ConsumerLink:
				fmt.Fprintf(stdout, "[DRY RUN LINK] %s skills: %s -> %s\n", op.ResourceName, target, noneStr(canonical))
			case InstructionLink:
				fmt.Fprintf(stdout, "[DRY RUN LINK] %s instructions: %s -> %s\n", op.ResourceName, target, noneStr(canonical))
			default:
				fmt.Fprintf(stdout, "[DRY RUN LINK] %s -> %s\n", noneStr(canonical), target)
			}
			return ok(false)
		}
		if canonical == "" || !exists(canonical) {
			return fail(op, "Preflight failed: canonical source does not exist: %s (stale plan)", noneStr(canonical))
		}
		if op.TargetKind == ManagedEntry && !isDir(canonical) {
			return fail(op, "Preflight failed: canonical skill source is not a directory: %s (stale plan)", canonical)
		}
		if op.TargetKind == ConsumerLink && !(isDir(canonical) && !isSymlink(canonical)) {
			return fail(op, "Preflight failed: managed container %s is not a valid directory (stale plan)", canonical)
		}
		if op.TargetKind == InstructionLink && !isFile(canonical) {
			return fail(op, "Preflight failed: canonical instruction source is not a file: %s (stale plan)", canonical)
		}
		if op.TargetKind == ManagedEntry {
			container := filepath.Dir(target)
			if !(isDir(container) && !isSymlink(container)) {
				return fail(op, "Preflight failed: managed container %s is not a valid directory (stale plan)", container)
			}
		}
		if (op.TargetKind == ConsumerLink || op.TargetKind == InstructionLink) && !op.RequiresParentCreation {
			if !exists(filepath.Dir(target)) {
				return fail(op, "Preflight failed: consumer parent directory missing for %s (stale plan)", target)
			}
		}
		if isSymlink(target) || exists(target) {
			return fail(op, "Preflight failed: target already exists or changed: %s (stale plan)", target)
		}
		if op.RequiresParentCreation {
			curr := filepath.Dir(target)
			for !exists(curr) && filepath.Dir(curr) != curr {
				curr = filepath.Dir(curr)
			}
			if isSymlink(curr) {
				switch filepath.Base(curr) {
				case ".agents", "memory", "skills":
					return fail(op, "Preflight failed: parent directory is a symlink: %s", curr)
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
				return fail(op, "Failed to create symlink: %v", err)
			}
		}
		if err := os.Symlink(canonical, target); err != nil {
			fmt.Fprintf(stderr, "[ERROR] Failed to create symlink %s -> %s: %v\n", target, canonical, err)
			return fail(op, "Failed to create symlink: %s -> %s", target, canonical)
		}
		switch op.TargetKind {
		case ConsumerLink:
			fmt.Fprintf(stdout, "[LINK] %s skills: %s -> %s\n", op.ResourceName, target, canonical)
		case InstructionLink:
			fmt.Fprintf(stdout, "[LINK] %s instructions: %s -> %s\n", op.ResourceName, target, canonical)
		default:
			fmt.Fprintf(stdout, "[LINK] %s -> %s\n", target, canonical)
		}
		return ok(true)

	case ActUnlink:
		if !isSymlink(target) {
			return fail(op, "Preflight failed: target is not a symlink: %s", target)
		}
		if !ownedBy(target, canonical) {
			return fail(op, "Preflight failed: link %s no longer points to canonical %s", target, noneStr(canonical))
		}
		if dryRun {
			fmt.Fprintf(stdout, "[DRY RUN CLEANUP] Would remove stale managed item: %s\n", target)
			return ok(false)
		}
		if err := os.Remove(target); err != nil {
			return fail(op, "Failed to remove stale link %s: %v", target, err)
		}
		fmt.Fprintf(stdout, "[CLEANUP] Removed stale managed item: %s\n", target)
		return ok(true)
	}
	return fail(op, "Unknown link action: %s", op.Action)
}

func noneStr(s string) string {
	if s == "" {
		return "None"
	}
	return s
}
