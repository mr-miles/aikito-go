package projectsync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MemoryResource is memory_runtime.py's MemoryResource.
type MemoryResource struct {
	Identity         string
	CanonicalSource  string
	RelativeTarget   string // slash-separated relative path
	SourceKind       string
	Exists           bool
	IsDir            bool
	IsSafe           bool
	SafetyError      string
	ExpectedModeType os.FileMode
	HasModeType      bool
}

// MemoryBatch is memory_runtime.py's MemoryBatch.
type MemoryBatch struct {
	ProjectName            string
	WorkspaceRoot          string
	ActiveCheckouts        []string
	OfflineCheckouts       []string
	Resources              []MemoryResource
	SelectedReferences     []string
	PlannedNotesCanonical  string
	PlannedHasProjMem      bool
	PlannedNotesIsDir      bool
	PlannedAgentTOMLMemory []string
	PlannedAgentTOMLMemOK  bool
	PlannedAgentTOMLExists bool
}

// MemoryPlan is memory_runtime.py's MemoryPlan.
type MemoryPlan struct {
	Batch      MemoryBatch
	Operations []LinkOperation
}

func (p MemoryPlan) Conflicts() []LinkOperation {
	var out []LinkOperation
	for _, op := range p.Operations {
		if op.Action == "CONFLICT" {
			out = append(out, op)
		}
	}
	return out
}

func (p MemoryPlan) CanApply() bool {
	if len(p.Conflicts()) > 0 {
		return false
	}
	for _, op := range p.Operations {
		if !op.IsAuthorized {
			return false
		}
	}
	return true
}

func (p MemoryPlan) findings() []string {
	var out []string
	for _, op := range p.Operations {
		if op.Finding != "" {
			out = append(out, op.Finding)
		}
	}
	return out
}

// MemoryResult is memory_runtime.py's MemoryExecutionResult.
type MemoryResult struct {
	Success      bool
	ErrorMessage string
}

func resolveProjectNotesSource(workspaceRoot, projectName string) (string, bool) {
	projMem := filepath.Join(workspaceRoot, "projects", projectName, "memory")
	if lexists(projMem) {
		return filepath.Join(projMem, "notes"), true
	}
	return filepath.Join(workspaceRoot, "memory", projectName, "notes"), false
}

// strictResolve is Path.resolve() (strict=False semantics in Python 3.6+,
// but only called where the path exists).
func strictResolve(p string) string { return resolve(p) }

func validateMemoryCanonicalSource(canonical, workspaceRoot, kind, projectName string) (bool, string) {
	wsResolved := resolve(workspaceRoot)
	switch kind {
	case "project_notes":
		projDir := filepath.Dir(filepath.Dir(canonical))
		if projectName != "" {
			projDir = filepath.Join(workspaceRoot, "projects", projectName)
		}
		projResolved := resolve(projDir)
		projMem := filepath.Join(projDir, "memory")
		if lexists(projMem) {
			if isSymlink(projMem) {
				if !exists(projMem) {
					return false, fmt.Sprintf("Project memory root is a broken symlink: %s", projMem)
				}
				memResolved := strictResolve(projMem)
				if !isDir(memResolved) {
					return false, fmt.Sprintf("Project memory root must be a directory: %s", projMem)
				}
				if !isRelativeTo(memResolved, projResolved) || !isRelativeTo(memResolved, wsResolved) {
					return false, fmt.Sprintf("Project memory root escapes project boundary: %s", projMem)
				}
			} else if !isDir(projMem) || isFile(projMem) {
				return false, fmt.Sprintf("Project memory root must be a directory: %s", projMem)
			}
		}
		if lexists(canonical) {
			if isSymlink(canonical) {
				if !exists(canonical) {
					return false, fmt.Sprintf("Project notes canonical is a broken symlink: %s", canonical)
				}
				canResolved := strictResolve(canonical)
				if !isDir(canResolved) {
					return false, fmt.Sprintf("Project notes source must be a directory: %s", canonical)
				}
				memResolved := resolve(projMem)
				if !isRelativeTo(canResolved, memResolved) || !isRelativeTo(canResolved, wsResolved) {
					return false, fmt.Sprintf("Project notes canonical escapes project memory boundary: %s", canonical)
				}
			} else if !isDir(canonical) || isFile(canonical) {
				return false, fmt.Sprintf("Project notes source must be a directory: %s", canonical)
			}
		}
		return true, ""

	case "workspace_ref":
		wsMem := filepath.Join(workspaceRoot, "memory")
		if lexists(wsMem) {
			if isSymlink(wsMem) {
				if !exists(wsMem) {
					return false, fmt.Sprintf("Workspace memory root is a broken symlink: %s", wsMem)
				}
				memResolved := strictResolve(wsMem)
				if !isDir(memResolved) {
					return false, fmt.Sprintf("Workspace memory root must be a directory: %s", wsMem)
				}
				if !isRelativeTo(memResolved, wsResolved) {
					return false, fmt.Sprintf("Workspace memory root escapes workspace boundary: %s", wsMem)
				}
			} else if !isDir(wsMem) {
				return false, fmt.Sprintf("Workspace memory root must be a directory: %s", wsMem)
			}
		}
		canResolved := resolve(canonical)
		memResolved := resolve(wsMem)
		if !isRelativeTo(canResolved, memResolved) || !isRelativeTo(canResolved, wsResolved) {
			return false, fmt.Sprintf("Memory canonical path escapes workspace memory root: %s", canonical)
		}
		if isSymlink(canonical) {
			if !exists(canonical) {
				return false, fmt.Sprintf("Memory canonical is a broken symlink: %s", canonical)
			}
			t := strictResolve(canonical)
			if !isRelativeTo(t, memResolved) || !isRelativeTo(t, wsResolved) {
				return false, fmt.Sprintf("Memory canonical symlink escapes workspace memory root: %s", canonical)
			}
		}
		return true, ""

	case "legacy_notes":
		wsMem := filepath.Join(workspaceRoot, "memory")
		if lexists(wsMem) && isSymlink(wsMem) {
			if !exists(wsMem) {
				return false, fmt.Sprintf("Workspace memory root is a broken symlink: %s", wsMem)
			}
			if !isRelativeTo(strictResolve(wsMem), wsResolved) {
				return false, fmt.Sprintf("Workspace memory root escapes workspace boundary: %s", wsMem)
			}
		}
		if lexists(canonical) {
			if isSymlink(canonical) {
				if !exists(canonical) {
					return false, fmt.Sprintf("Legacy notes canonical is a broken symlink: %s", canonical)
				}
				canResolved := strictResolve(canonical)
				memResolved := resolve(wsMem)
				if !isRelativeTo(canResolved, memResolved) || !isRelativeTo(canResolved, wsResolved) {
					return false, fmt.Sprintf("Legacy notes canonical escapes workspace memory root: %s", canonical)
				}
			} else if !isDir(canonical) {
				return false, fmt.Sprintf("Legacy notes source must be a directory: %s", canonical)
			}
		}
		return true, ""
	}
	return true, ""
}

// pathParts is PurePath(ref).parts for a relative reference.
func pathParts(ref string) []string {
	var parts []string
	for _, p := range strings.Split(filepath.ToSlash(ref), "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

// normRef is str(Path(ref)): separators collapsed, "." components dropped.
func normRef(ref string) string {
	parts := pathParts(ref)
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, "/")
}

func validateMemoryReference(ref, workspaceRoot string) (bool, string, string) {
	if strings.TrimSpace(ref) == "" {
		return false, "", "Empty memory reference"
	}
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") {
		return false, "", fmt.Sprintf("Memory reference cannot be absolute: %s", ref)
	}
	if contains(pathParts(ref), "..") {
		return false, "", fmt.Sprintf("Memory reference cannot contain '..' traversal: %s", ref)
	}
	rel := normRef(ref)
	canonical := filepath.Join(workspaceRoot, "memory", filepath.FromSlash(rel))
	if ok, err := validateMemoryCanonicalSource(canonical, workspaceRoot, "workspace_ref", ""); !ok {
		return false, "", err
	}
	return true, rel, ""
}

func validateCheckoutTargetBoundary(checkout, rel string) (bool, string, string) {
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return false, "", fmt.Sprintf("Target path cannot be absolute: %s", rel)
	}
	if contains(pathParts(rel), "..") {
		return false, "", fmt.Sprintf("Target path cannot contain '..' traversal: %s", rel)
	}
	agentsDir := filepath.Join(checkout, ".agents")
	if isSymlink(agentsDir) {
		return false, "", fmt.Sprintf("Checkout .agents directory cannot be a symlink: %s", agentsDir)
	}
	memDir := filepath.Join(agentsDir, "memory")
	if isSymlink(memDir) {
		return false, "", fmt.Sprintf("Checkout .agents/memory directory cannot be a symlink: %s", memDir)
	}
	target := filepath.Join(memDir, filepath.FromSlash(rel))
	if !isRelativeTo(filepath.Clean(target), filepath.Clean(memDir)) || !isRelativeTo(filepath.Clean(target), filepath.Clean(checkout)) {
		return false, "", fmt.Sprintf("Target path escapes checkout memory directory: %s", target)
	}
	parts := pathParts(rel)
	curr := memDir
	for i := 0; i < len(parts)-1; i++ {
		curr = filepath.Join(curr, parts[i])
		if isSymlink(curr) {
			return false, "", fmt.Sprintf("Intermediate directory in memory target path cannot be a symlink: %s", curr)
		}
	}
	return true, target, ""
}

func memoryList(cfg map[string]any) []string {
	var out []string
	if l, ok := cfg["memory"].([]any); ok {
		for _, v := range l {
			out = append(out, pyStr(v))
		}
	}
	return out
}

// BuildProjectMemoryBatch is build_project_memory_batch.
func BuildProjectMemoryBatch(workspaceRoot, projectName string, data map[string]any, active, offline []string) MemoryBatch {
	refs := memoryList(data)
	var resources []MemoryResource
	seen := map[string]bool{}

	agentTOML := filepath.Join(workspaceRoot, "projects", projectName, "agent.toml")
	b := MemoryBatch{ProjectName: projectName, WorkspaceRoot: workspaceRoot, ActiveCheckouts: active, OfflineCheckouts: offline,
		SelectedReferences: refs, PlannedAgentTOMLExists: isFile(agentTOML)}
	if b.PlannedAgentTOMLExists {
		if cfg, err := loadTOMLMap(agentTOML); err == nil {
			b.PlannedAgentTOMLMemory, b.PlannedAgentTOMLMemOK = memoryList(cfg), true
		}
	}

	notes, hasProjMem := resolveProjectNotesSource(workspaceRoot, projectName)
	kind := "legacy_notes"
	if hasProjMem {
		kind = "project_notes"
	}
	safe, notesErr := validateMemoryCanonicalSource(notes, workspaceRoot, kind, projectName)
	notesIsDir := exists(notes) && isDir(notes)
	if !safe {
		resources = append(resources, MemoryResource{Identity: "project_notes", CanonicalSource: notes, RelativeTarget: "notes",
			SourceKind: "project_notes", Exists: exists(notes), IsDir: notesIsDir, IsSafe: false, SafetyError: notesErr})
		seen["notes"] = true
	} else if notesIsDir {
		r := MemoryResource{Identity: "project_notes", CanonicalSource: notes, RelativeTarget: "notes",
			SourceKind: "project_notes", Exists: true, IsDir: true, IsSafe: true}
		if st, err := os.Lstat(notes); err == nil {
			r.ExpectedModeType, r.HasModeType = st.Mode().Type(), true
		}
		resources = append(resources, r)
		seen["notes"] = true
	}

	wsMem := filepath.Join(workspaceRoot, "memory")
	for _, ref := range refs {
		ok, rel, errMsg := validateMemoryReference(ref, workspaceRoot)
		if !ok {
			if errMsg == "" {
				errMsg = "Unsafe memory reference: " + ref
			}
			resources = append(resources, MemoryResource{Identity: "workspace_ref:" + ref, CanonicalSource: filepath.Join(wsMem, ref),
				RelativeTarget: normRef(ref), SourceKind: "workspace_ref", IsSafe: false, SafetyError: errMsg})
			continue
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		canonical := filepath.Join(wsMem, filepath.FromSlash(rel))
		r := MemoryResource{Identity: "workspace_ref:" + ref, CanonicalSource: canonical, RelativeTarget: rel,
			SourceKind: "workspace_ref", Exists: exists(canonical), IsSafe: true}
		if r.Exists {
			if st, err := os.Lstat(canonical); err == nil {
				r.ExpectedModeType, r.HasModeType = st.Mode().Type(), true
				r.IsDir = st.IsDir()
			}
		}
		resources = append(resources, r)
	}
	b.Resources = resources
	b.PlannedNotesCanonical, b.PlannedHasProjMem, b.PlannedNotesIsDir = notes, hasProjMem, notesIsDir
	return b
}

// PlanProjectMemory is plan_project_memory (not offline).
func PlanProjectMemory(b MemoryBatch) MemoryPlan {
	var ops []LinkOperation
	mem := func(action, rule, target, canonical, reason, name string) LinkOperation {
		return LinkOperation{Action: action, RuleID: rule, TargetPath: target, CanonicalPath: canonical, Reason: reason,
			IsAuthorized: true, ExpectedRepresentation: "missing", DesiredRepresentation: "link", TargetKind: "memory_link", ResourceName: name}
	}
	conflict := func(rule, target, canonical, reason, finding, name string) LinkOperation {
		op := mem("CONFLICT", rule, target, canonical, reason, name)
		op.Finding, op.IsAuthorized = finding, false
		return op
	}

	for _, co := range b.OfflineCheckouts {
		for _, r := range b.Resources {
			ops = append(ops, mem("SKIP", "INV-MEM-08", filepath.Join(co, ".agents", "memory", filepath.FromSlash(r.RelativeTarget)),
				r.CanonicalSource, fmt.Sprintf("Checkout is offline: %s", co), r.RelativeTarget))
		}
	}

	overlap := map[string]string{}
	var valid []MemoryResource
	for _, r := range b.Resources {
		if r.IsSafe {
			valid = append(valid, r)
		}
	}
	for i, r1 := range valid {
		for j, r2 := range valid {
			if i == j {
				continue
			}
			p1, p2 := pathParts(r1.RelativeTarget), pathParts(r2.RelativeTarget)
			if len(p2) > len(p1) && strings.Join(p2[:len(p1)], "/") == strings.Join(p1, "/") {
				overlap[r2.RelativeTarget] = fmt.Sprintf("Hierarchical memory target collision: %s overlaps with %s", r1.RelativeTarget, r2.RelativeTarget)
			}
		}
	}

	for _, co := range b.ActiveCheckouts {
		memDir := filepath.Join(co, ".agents", "memory")
		agentsDir := filepath.Join(co, ".agents")
		if isSymlink(agentsDir) {
			f := fmt.Sprintf("Checkout .agents directory cannot be a symlink: %s", agentsDir)
			op := conflict("INV-MEM-02", agentsDir, "", f, f, "")
			ops = append(ops, op)
			continue
		}
		if isSymlink(memDir) {
			f := fmt.Sprintf("Checkout .agents/memory directory cannot be a symlink: %s", memDir)
			ops = append(ops, conflict("INV-MEM-02", memDir, "", f, f, ""))
			continue
		}
		for _, r := range b.Resources {
			target := filepath.Join(memDir, filepath.FromSlash(r.RelativeTarget))
			if !r.IsSafe {
				f := r.SafetyError
				if f == "" {
					f = "Unsafe memory reference: " + r.RelativeTarget
				}
				ops = append(ops, conflict("INV-MEM-02", target, r.CanonicalSource, f, f, r.RelativeTarget))
				continue
			}
			if f, ok := overlap[r.RelativeTarget]; ok {
				ops = append(ops, conflict("INV-MEM-02", target, r.CanonicalSource, f, f, r.RelativeTarget))
				continue
			}
			okT, validated, tErr := validateCheckoutTargetBoundary(co, r.RelativeTarget)
			if !okT {
				f := tErr
				if f == "" {
					f = "Target path escapes checkout memory directory: " + r.RelativeTarget
				}
				ops = append(ops, conflict("INV-MEM-02", target, r.CanonicalSource, f, f, r.RelativeTarget))
				continue
			}
			target = validated
			canonical := r.CanonicalSource
			if !r.Exists {
				f := fmt.Sprintf("Project memory source does not exist: %s", canonical)
				ops = append(ops, conflict("INV-MEM-07", target, canonical, f, f, r.RelativeTarget))
				continue
			}
			o := InspectLinkTarget(target, canonical, true, "", "memory_link", "project", false)
			switch o.EntryType {
			case "missing":
				op := mem("CREATE", "INV-MEM-01", target, canonical, fmt.Sprintf("Create symbolic link to canonical memory %s", canonical), r.RelativeTarget)
				op.RequiresParentCreation = true
				ops = append(ops, op)
			case "symlink":
				if o.LinkPointsToCanonical {
					op := mem("NOOP", "INV-MEM-10", target, canonical, fmt.Sprintf("Symbolic link already points to %s", canonical), r.RelativeTarget)
					op.ExpectedRepresentation = "symlink"
					ops = append(ops, op)
					continue
				}
				legacy := filepath.Join(b.WorkspaceRoot, "memory", b.ProjectName, "notes")
				migration := r.SourceKind == "project_notes" && InspectLinkTarget(target, legacy, true, "", "memory_link", "project", false).LinkPointsToCanonical
				if migration {
					un := mem("UNLINK", "INV-MEM-06", target, legacy, fmt.Sprintf("Remove legacy project notes symlink %s -> %s", target, legacy), r.RelativeTarget)
					un.ExpectedRepresentation, un.DesiredRepresentation = "symlink", "absent"
					cr := mem("CREATE", "INV-MEM-03", target, canonical, fmt.Sprintf("Migrate to new canonical project notes link %s -> %s", target, canonical), r.RelativeTarget)
					cr.RequiresParentCreation = true
					ops = append(ops, un, cr)
				} else {
					op := conflict("INV-MEM-07", target, canonical, fmt.Sprintf("Target preserved: %s. Symlink points to unexpected destination: %s", target, observedDest(o)),
						fmt.Sprintf("Unmanaged project runtime item: %s", target), r.RelativeTarget)
					op.ExpectedRepresentation = "symlink"
					ops = append(ops, op)
				}
			default:
				op := conflict("INV-MEM-07", target, canonical, fmt.Sprintf("Target preserved: %s. Entry is a regular %s (expected symlink)", target, o.EntryType),
					fmt.Sprintf("Unmanaged project runtime item: %s", target), r.RelativeTarget)
				op.ExpectedRepresentation = o.EntryType
				ops = append(ops, op)
			}
		}

		if isDir(memDir) {
			desiredTop := map[string]bool{}
			for _, r := range b.Resources {
				if parts := pathParts(r.RelativeTarget); len(parts) > 0 {
					desiredTop[parts[0]] = true
				}
			}
			entries, err := os.ReadDir(memDir)
			if err != nil {
				continue
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, name := range names {
				if desiredTop[name] {
					continue
				}
				entry := filepath.Join(memDir, name)
				projMemRoot := filepath.Join(b.WorkspaceRoot, "projects", b.ProjectName, "memory")
				if !exists(projMemRoot) {
					projMemRoot = filepath.Join(b.WorkspaceRoot, "memory", b.ProjectName)
				}
				candidates := []string{filepath.Join(projMemRoot, name)}
				if name != "notes" || contains(b.SelectedReferences, "notes") {
					candidates = append(candidates, filepath.Join(b.WorkspaceRoot, "memory", name))
				}
				stale := InspectLinkTarget(entry, "", true, "", "memory_link", "project", false)
				if stale.EntryType == "symlink" {
					matched := ""
					for _, c := range candidates {
						if InspectLinkTarget(entry, c, true, "", "memory_link", "project", false).LinkPointsToCanonical {
							matched = c
							break
						}
					}
					if matched != "" {
						op := mem("UNLINK", "INV-MEM-06", entry, matched, fmt.Sprintf("Remove stale managed memory symlink: %s -> %s", entry, matched), name)
						op.ExpectedRepresentation, op.DesiredRepresentation = "symlink", "absent"
						ops = append(ops, op)
					} else {
						op := conflict("INV-MEM-07", entry, "", fmt.Sprintf("Target preserved: %s. Deselected link points to %s", entry, observedDest(stale)),
							fmt.Sprintf("Unmanaged project runtime item: %s", entry), name)
						op.ExpectedRepresentation, op.DesiredRepresentation = "symlink", "absent"
						ops = append(ops, op)
					}
				} else {
					op := conflict("INV-MEM-07", entry, "", fmt.Sprintf("Target preserved: %s. Unmanaged %s", entry, stale.EntryType),
						fmt.Sprintf("Unmanaged project runtime item: %s", entry), name)
					op.ExpectedRepresentation, op.DesiredRepresentation = stale.EntryType, "absent"
					ops = append(ops, op)
				}
			}
		}
	}
	return MemoryPlan{Batch: b, Operations: ops}
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ExecuteMemoryPlan is execute_memory_plan.
func ExecuteMemoryPlan(out Out, plan MemoryPlan, dryRun bool) MemoryResult {
	b := plan.Batch
	if len(plan.Conflicts()) > 0 {
		return MemoryResult{ErrorMessage: "Memory plan has conflicts: " + strings.Join(plan.findings(), "; ")}
	}
	fail := func(msg string) MemoryResult { return MemoryResult{ErrorMessage: msg} }

	agentTOML := filepath.Join(b.WorkspaceRoot, "projects", b.ProjectName, "agent.toml")
	currExists := isFile(agentTOML)
	if currExists != b.PlannedAgentTOMLExists {
		return fail("Stale memory plan: agent.toml file presence changed before apply")
	}
	if currExists {
		cfg, err := loadTOMLMap(agentTOML)
		if err != nil {
			return fail("Stale memory plan: unable to verify agent.toml: " + err.Error())
		}
		curr := memoryList(cfg)
		if b.PlannedAgentTOMLMemOK && !stringsEqual(curr, b.PlannedAgentTOMLMemory) {
			return fail("Stale memory plan: agent.toml memory configuration changed before apply")
		}
		if !stringsEqual(curr, b.SelectedReferences) {
			return fail("Stale memory plan: agent.toml memory configuration diverged from planned references")
		}
	}

	notes, hasProjMem := resolveProjectNotesSource(b.WorkspaceRoot, b.ProjectName)
	kind := "legacy_notes"
	if hasProjMem {
		kind = "project_notes"
	}
	if ok, e := validateMemoryCanonicalSource(notes, b.WorkspaceRoot, kind, b.ProjectName); !ok {
		return fail("Stale memory plan: project memory source safety violation: " + e)
	}
	notesIsDir := exists(notes) && isDir(notes)
	if (b.PlannedNotesCanonical != "" && notes != b.PlannedNotesCanonical) || hasProjMem != b.PlannedHasProjMem || notesIsDir != b.PlannedNotesIsDir {
		return fail("Stale memory plan: project memory source priority changed before apply")
	}

	for _, r := range b.Resources {
		if !r.IsSafe {
			continue
		}
		if !exists(r.CanonicalSource) {
			return fail(fmt.Sprintf("Stale memory plan: canonical source missing: %s", r.CanonicalSource))
		}
		st, err := os.Lstat(r.CanonicalSource)
		if err != nil {
			return fail(fmt.Sprintf("Stale memory plan: failed to stat canonical source: %s: %s", r.CanonicalSource, pyOSError(err)))
		}
		if r.HasModeType && st.Mode().Type() != r.ExpectedModeType {
			return fail(fmt.Sprintf("Stale memory plan: canonical source type changed: %s (expected mode type %d, got %d)",
				r.CanonicalSource, pyModeType(r.ExpectedModeType), pyModeType(st.Mode().Type())))
		}
		if ok, e := validateMemoryCanonicalSource(r.CanonicalSource, b.WorkspaceRoot, r.SourceKind, b.ProjectName); !ok {
			return fail("Stale memory plan: canonical source safety violation: " + e)
		}
	}

	for _, co := range b.ActiveCheckouts {
		memDir := filepath.Join(co, ".agents", "memory")
		for _, r := range b.Resources {
			if !r.IsSafe {
				continue
			}
			if ok, _, e := validateCheckoutTargetBoundary(co, r.RelativeTarget); !ok {
				return fail("Stale memory plan: target boundary violation before apply: " + e)
			}
		}
		for _, op := range plan.Operations {
			if isRelativeTo(op.TargetPath, memDir) {
				rel, _ := filepath.Rel(memDir, op.TargetPath)
				if ok, _, e := validateCheckoutTargetBoundary(co, filepath.ToSlash(rel)); !ok {
					return fail("Stale memory plan: target boundary violation before apply: " + e)
				}
			}
		}
	}

	unlinks := map[string]LinkOperation{}
	creates := map[string]LinkOperation{}
	for _, op := range plan.Operations {
		if op.Action == "UNLINK" {
			unlinks[op.TargetPath] = op
		} else if op.Action == "CREATE" {
			creates[op.TargetPath] = op
		}
	}
	replacement := func(p string) bool {
		_, u := unlinks[p]
		_, c := creates[p]
		return u && c
	}
	for _, op := range plan.Operations {
		switch op.Action {
		case "UNLINK":
			if !isSymlink(op.TargetPath) {
				return fail(fmt.Sprintf("Stale memory plan: target is not a symlink for unlink: %s", op.TargetPath))
			}
			if !InspectLinkTarget(op.TargetPath, op.CanonicalPath, true, "", "memory_link", "project", false).LinkPointsToCanonical {
				return fail(fmt.Sprintf("Stale memory plan: unlink target ownership evidence broken; symlink %s does not point to expected canonical %s", op.TargetPath, pyPath(op.CanonicalPath)))
			}
		case "CREATE":
			if !replacement(op.TargetPath) && lexists(op.TargetPath) {
				return fail(fmt.Sprintf("Stale memory plan: target already exists: %s", op.TargetPath))
			}
		}
	}

	handled := map[string]bool{}
	for _, op := range plan.Operations {
		if op.Action == "SKIP" {
			continue
		}
		if replacement(op.TargetPath) {
			if handled[op.TargetPath] {
				continue
			}
			handled[op.TargetPath] = true
			cr, un := creates[op.TargetPath], unlinks[op.TargetPath]
			if dryRun {
				out.println("[DRY RUN CLEANUP] Would remove legacy project notes symlink: %s", un.TargetPath)
				out.println("[DRY RUN LINK] %s -> %s", cr.TargetPath, pyPath(cr.CanonicalPath))
				continue
			}
			if !InspectLinkTarget(un.TargetPath, un.CanonicalPath, true, "", "memory_link", "project", false).LinkPointsToCanonical {
				return fail(fmt.Sprintf("Failed to migrate symlink: ownership evidence broken on %s", un.TargetPath))
			}
			tmp := filepath.Join(filepath.Dir(op.TargetPath), ".tmp_replace_"+newTxID())
			replaced := false
			if cr.CanonicalPath != "" && safeSymlink(out, cr.CanonicalPath, tmp) {
				if err := os.Rename(tmp, op.TargetPath); err == nil {
					replaced = true
					out.println("[CLEANUP] Removed legacy project notes symlink: %s", un.TargetPath)
					out.println("[LINK] %s -> %s", cr.TargetPath, cr.CanonicalPath)
				} else if lexists(tmp) {
					os.Remove(tmp)
				}
			}
			if !replaced {
				oldDest, _ := os.Readlink(un.TargetPath)
				os.Remove(un.TargetPath)
				if cr.CanonicalPath == "" || !safeSymlink(out, cr.CanonicalPath, cr.TargetPath) {
					if oldDest != "" {
						os.Symlink(oldDest, un.TargetPath)
					}
					return fail(fmt.Sprintf("Failed to migrate symlink: %s", op.TargetPath))
				}
				out.println("[CLEANUP] Removed legacy project notes symlink: %s", un.TargetPath)
				out.println("[LINK] %s -> %s", cr.TargetPath, cr.CanonicalPath)
			}
			continue
		}
		res := applyLink(out, op, dryRun)
		if !res.Success {
			msg := res.ErrorMessage
			if msg == "" {
				msg = fmt.Sprintf("Failed to apply memory operation on %s", op.TargetPath)
			}
			return fail(msg)
		}
	}
	return MemoryResult{Success: true}
}

// pyModeType is stat.S_IFMT for an os.FileMode type.
func pyModeType(t os.FileMode) int {
	switch {
	case t&os.ModeSymlink != 0:
		return 0o120000
	case t&os.ModeDir != 0:
		return 0o040000
	case t&os.ModeNamedPipe != 0:
		return 0o010000
	case t&os.ModeSocket != 0:
		return 0o140000
	case t&os.ModeCharDevice != 0:
		return 0o020000
	case t&os.ModeDevice != 0:
		return 0o060000
	}
	return 0o100000
}
