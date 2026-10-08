package projectsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// ValidationError is init.py's _project_validation_error. With
// rejectUnexpected it is project_validation_error (registration);
// without, project_sync_validation_error. Returns "" when valid.
func ValidationError(aikitoDir, projectName, projectPath, home string, rejectUnexpected bool) string {
	if !project.ValidProjectName(projectName) {
		return "Project name must start with a letter or digit and contain only " +
			"letters, digits, dots, underscores, or hyphens."
	}
	if !exists(projectPath) {
		return fmt.Sprintf("Project path does not exist: %s", projectPath)
	}
	if !isDir(projectPath) {
		return fmt.Sprintf("Project path is not a directory: %s", projectPath)
	}
	if !isFile(filepath.Join(aikitoDir, "layout.toml")) {
		return fmt.Sprintf("Aikito workspace is not initialized: %s", aikitoDir)
	}

	configPath := filepath.Join(aikitoDir, "projects", projectName, "agent.toml")
	var configData map[string]any
	if exists(configPath) {
		cfg, err := loadTOMLMap(configPath)
		if err != nil {
			return fmt.Sprintf("Failed to read existing project config %s: %s", configPath, tomlErrorText(err))
		}
		configData = cfg
		binding := project.ResolveProjectBinding(cfg, home)
		if len(binding.Entries) > 0 {
			matched := false
			var registered []string
			for _, e := range binding.Entries {
				if e.ResolvedPath == projectPath {
					matched = true
				}
				registered = append(registered, e.ResolvedPath)
			}
			if !matched && rejectUnexpected {
				return fmt.Sprintf("Project '%s' is already registered to %s, not %s.", projectName, strings.Join(registered, ", "), projectPath)
			}
		}
	}

	canonical := filepath.Join(aikitoDir, "projects", projectName, "AGENTS.md")
	reg, err := registry.LoadStrict(aikitoDir, home)
	if err != nil {
		return err.Error()
	}
	targets, err := registry.ResolveTargets("project_instructions", aikitoDir, home, reg,
		registry.ResolveTargetsOptions{ProjectPath: projectPath, ProjectName: projectName})
	if err != nil {
		return err.Error()
	}
	enabled := false
	if isFile(canonical) {
		enabled, _ = canonicalNonEmpty(canonical)
	}
	if enabled {
		for _, t := range targets {
			if t.IsSameObject() {
				continue
			}
			if isSymlink(t.Path) {
				if resolve(t.Path) == resolve(canonical) {
					continue
				}
			} else if !exists(t.Path) {
				continue
			}
			return fmt.Sprintf("Unmanaged project instructions for %s already exist: %s", strings.Join(t.ConsumerDisplayNames, ", "), t.Path)
		}
	}

	allowedMemory := map[string]bool{}
	if configData != nil {
		for _, m := range memoryList(configData) {
			allowedMemory[m] = true
		}
		projMem := filepath.Join(aikitoDir, "projects", projectName, "memory")
		if isDir(projMem) {
			entries, _ := os.ReadDir(projMem)
			for _, e := range entries {
				allowedMemory[e.Name()] = true
			}
		}
	}
	for _, name := range []string{"skills", "memory"} {
		managed := filepath.Join(projectPath, ".agents", name)
		if isSymlink(managed) || !exists(managed) {
			continue
		}
		if !isDir(managed) {
			return fmt.Sprintf("Unmanaged project resources already exist: %s", managed)
		}
		if name == "skills" || !rejectUnexpected {
			continue
		}
		entries, _ := os.ReadDir(managed)
		var unexpected []string
		for _, e := range entries {
			if !allowedMemory[e.Name()] {
				unexpected = append(unexpected, e.Name())
			}
		}
		if len(unexpected) > 0 {
			sort.Strings(unexpected)
			return fmt.Sprintf("Unmanaged project resources already exist in %s: %s", managed, strings.Join(unexpected, ", "))
		}
	}
	return ""
}

// tomlErrorText renders a TOML decode error. Python's tomllib wording
// cannot be reproduced exactly; go-toml's message is used as-is.
func tomlErrorText(err error) string { return err.Error() }

// Batch is project_sync.py's ProjectSyncBatch.
type Batch struct {
	WorkspaceRoot     string
	ProjectName       string
	ActiveCheckouts   []string
	OfflineCheckouts  []string
	SkillPlan         SkillPlan
	PreflightFindings []string
	CanApply          bool
	ConfigCAS         *CandidatePathCAS
	InstructionPlan   *InstructionPlan
	MemoryPlan        *MemoryPlan
}

func skillList(data map[string]any) []string {
	var out []string
	if l, ok := data["skills"].([]any); ok {
		for _, v := range l {
			out = append(out, pyStr(v))
		}
	}
	return out
}

func syncMode(data map[string]any) string {
	v, ok := data["sync_mode"]
	if !ok {
		return "link"
	}
	return strings.ToLower(pyStr(v))
}

// checkCaseCollision is compat.check_case_collision.
func checkCaseCollision(names []string, dir string) (string, string, bool) {
	probe := dir
	for !exists(probe) && filepath.Dir(probe) != probe {
		probe = filepath.Dir(probe)
	}
	if !compat.DirectoryFoldsCase(probe) {
		return "", "", false
	}
	seen := map[string]string{}
	for _, n := range names {
		lower := strings.ToLower(n)
		if prev, ok := seen[lower]; ok && prev != n {
			return prev, n, true
		}
		seen[lower] = n
	}
	return "", "", false
}

// BuildBatch is build_project_sync_batch (register_explicit_path=True).
func BuildBatch(workspaceRoot, home, projectName string, data map[string]any, explicitPath string, force bool) Batch {
	binding := project.ResolveProjectBinding(data, home)
	var active, offline []string
	for _, e := range binding.OfflineEntries() {
		offline = append(offline, e.ResolvedPath)
	}
	if explicitPath != "" {
		active = []string{explicitPath}
	} else {
		for _, e := range binding.ActiveEntries() {
			active = append(active, e.ResolvedPath)
		}
	}

	var errs []string
	for _, co := range active {
		if v := ValidationError(workspaceRoot, projectName, co, home, false); v != "" && !contains(errs, v) {
			errs = append(errs, v)
		}
	}
	skills := skillList(data)
	memoryFiles := memoryList(data)
	mode := syncMode(data)

	projMemSource := filepath.Join(workspaceRoot, "projects", projectName, "memory")
	if !exists(projMemSource) {
		projMemSource = filepath.Join(workspaceRoot, "memory", projectName)
	}
	for _, s := range skills {
		src := filepath.Join(workspaceRoot, "skills", s)
		if !isDir(src) {
			errs = append(errs, fmt.Sprintf("Project skill source does not exist: %s", src))
		}
	}
	var resourcePaths []string
	agentTOML := filepath.Join(workspaceRoot, "projects", projectName, "agent.toml")
	if isFile(agentTOML) {
		resourcePaths = append(resourcePaths, agentTOML)
	}
	instructions := filepath.Join(workspaceRoot, "projects", projectName, "AGENTS.md")
	if isFile(instructions) {
		resourcePaths = append(resourcePaths, instructions)
	}
	for _, s := range skills {
		if d := filepath.Join(workspaceRoot, "skills", s); isDir(d) {
			resourcePaths = append(resourcePaths, d)
		}
	}
	if isDir(projMemSource) {
		if notes := filepath.Join(projMemSource, "notes"); isDir(notes) {
			resourcePaths = append(resourcePaths, notes)
		}
	}
	for _, m := range memoryFiles {
		if p := filepath.Join(workspaceRoot, "memory", m); exists(p) {
			resourcePaths = append(resourcePaths, p)
		}
	}
	errs = append(errs, CollectResourceConflicts(resourcePaths, home)...)

	var cas *CandidatePathCAS
	if explicitPath != "" && isFile(agentTOML) {
		pre, err := os.ReadFile(agentTOML)
		if err == nil {
			preSum := sha256.Sum256(pre)
			preHash := hex.EncodeToString(preSum[:])
			noop := &CandidatePathCAS{ConfigPath: agentTOML, PreImage: pre, PreImageHash: preHash, PostImage: pre, PostImageHash: preHash, IsNoop: true}
			configured := false
			for _, e := range binding.Entries {
				if resolve(e.ResolvedPath) == resolve(explicitPath) {
					configured = true
				}
			}
			if configured {
				cas = noop
			} else {
				post, changed, perr := project.AddCandidatePath(pre, safeRelativePath(explicitPath, home), home)
				switch {
				case perr != nil:
					err = perr
				case !changed:
					cas = noop
				default:
					postSum := sha256.Sum256(post)
					cas = &CandidatePathCAS{ConfigPath: agentTOML, PreImage: pre, PreImageHash: preHash, PostImage: post, PostImageHash: hex.EncodeToString(postSum[:])}
				}
			}
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("Failed to save codebase path for project '%s': %s\nPlease configure the codebase path for project '%s'.", projectName, err, projectName))
		}
	}

	var ops []SkillOperation
	findings := append([]string(nil), errs...)
	selected := map[string]bool{}
	for _, s := range skills {
		selected[s] = true
	}
	wsID := filepath.Base(workspaceRoot)
	for _, co := range active {
		skillsDir := filepath.Join(co, ".agents", "skills")
		if a, b, ok := checkCaseCollision(skills, skillsDir); ok {
			findings = append(findings, fmt.Sprintf("Case collision detected between skills '%s' and '%s' on case-insensitive filesystem at %s", a, b, skillsDir))
		}
		known := map[string]bool{}
		for _, s := range skills {
			known[s] = true
		}
		if doc, _ := LoadProjectSkillState(home, workspaceRoot, projectName, co); doc != nil {
			for k := range doc.Records {
				known[k] = true
			}
		}
		if isDir(skillsDir) {
			entries, _ := os.ReadDir(skillsDir)
			for _, e := range entries {
				known[e.Name()] = true
			}
		}
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			t := SkillTarget{WorkspaceRoot: workspaceRoot, WorkspaceID: wsID, ProjectName: projectName, PhysicalCheckout: co,
				SkillName: n, TargetPath: filepath.Join(skillsDir, n)}
			desired := "absent"
			if selected[n] {
				desired = mode
			}
			o, d := InspectSkillTarget(t, desired, home)
			ops = append(ops, PlanSingleSkill(t, d, o, force, false))
		}
	}
	for _, co := range offline {
		skillsDir := filepath.Join(co, ".agents", "skills")
		sorted := append([]string(nil), skills...)
		sort.Strings(sorted)
		for _, n := range sorted {
			t := SkillTarget{WorkspaceRoot: workspaceRoot, WorkspaceID: wsID, ProjectName: projectName, PhysicalCheckout: co,
				SkillName: n, TargetPath: filepath.Join(skillsDir, n)}
			d := DesiredSkill{SkillName: n, Mode: mode, CanonicalPath: filepath.Join(workspaceRoot, "skills", n)}
			ops = append(ops, PlanSingleSkill(t, d, ObservedSkill{Target: t, EntryType: "missing", CanonicalValid: true}, false, true))
		}
	}
	plan := BuildSkillPlan(workspaceRoot, projectName, ops, cas)

	var instrPlan *InstructionPlan
	if isFile(instructions) {
		batch, err := BuildProjectInstructionBatch(workspaceRoot, projectName, active, home, nil, offline)
		if err != nil {
			findings = append(findings, err.Error())
		} else {
			p := PlanInstructions(batch, home)
			instrPlan = &p
		}
	}
	memPlan := PlanProjectMemory(BuildProjectMemoryBatch(workspaceRoot, projectName, data, active, offline))
	canApply := plan.CanApply && (instrPlan == nil || instrPlan.CanApply()) && memPlan.CanApply() && len(findings) == 0
	return Batch{WorkspaceRoot: workspaceRoot, ProjectName: projectName, ActiveCheckouts: active, OfflineCheckouts: offline,
		SkillPlan: plan, PreflightFindings: findings, CanApply: canApply, ConfigCAS: cas, InstructionPlan: instrPlan, MemoryPlan: &memPlan}
}

// applyBatch is apply_project_sync_batch for an applicable batch. It
// returns "" on success, else the error message.
func applyBatch(out Out, b Batch, home string, dryRun bool) string {
	res := ExecuteSkillPlan(out, b.SkillPlan, home, dryRun)
	if !res.IsSuccess() {
		return res.ErrorMessage
	}
	if b.InstructionPlan != nil {
		r := ExecuteInstructionPlan(out, *b.InstructionPlan, dryRun)
		if !r.Success {
			return orStr(r.ErrorMessage, "Failed to synchronize project instructions")
		}
	}
	if b.MemoryPlan != nil {
		r := ExecuteMemoryPlan(out, *b.MemoryPlan, dryRun)
		if !r.Success {
			return orStr(r.ErrorMessage, "Failed to synchronize project memory")
		}
	}
	return ""
}

func renderBatchOps(out Out, b Batch, checkout string, dryRun bool) {
	for _, a := range b.SkillPlan.Authorizations {
		out.println("[AUTH] %s", a)
	}
	for _, op := range b.SkillPlan.Operations {
		if resolve(op.Target.PhysicalCheckout) != resolve(checkout) {
			continue
		}
		canonical := filepath.Join(b.WorkspaceRoot, "skills", op.Target.SkillName)
		target := op.Target.TargetPath
		switch op.Action {
		case "UNLINK":
			if dryRun {
				out.println("[DRY RUN CLEANUP] Would remove stale managed item: %s", target)
			} else {
				out.println("[CLEANUP] Removed stale managed item: %s", target)
			}
		case "CREATE", "UPDATE":
			switch {
			case op.DesiredRepresentation == "link" && dryRun:
				out.println("[DRY RUN LINK] %s -> %s", canonical, target)
			case op.DesiredRepresentation == "link":
				out.println("[LINK] %s -> %s", target, canonical)
			case dryRun:
				out.println("[DRY RUN COPY] %s -> %s", canonical, target)
			default:
				out.println("[COPY DIR] %s -> %s", canonical, target)
			}
		case "RECONCILE_STATE", "CLAIM_STATE", "REACTIVATE_STATE":
			if !dryRun {
				out.println("[INFO] %s", op.Reason)
			}
		case "DEACTIVATE_STATE":
			out.println("[INFO] Preserving project-owned skill: %s", target)
		case "NOOP":
			switch op.RuleID {
			case "INV-TR-15", "INV-TR-17":
				out.println("[INFO] Preserving project-owned skill: %s", target)
			case "INV-TR-11":
				out.println("[INFO] Skill '%s' at %s is registered but unmanaged (matches canonical without state). Run 'aikito sync project %s %s --force' to claim management.",
					op.Target.SkillName, target, op.Target.ProjectName, op.Target.PhysicalCheckout)
			case "INV-TR-18":
				out.println("[INFO] Skill '%s' at %s is registered but inactive (matches canonical). Run 'aikito sync project %s %s --force' to reactivate management.",
					op.Target.SkillName, target, op.Target.ProjectName, op.Target.PhysicalCheckout)
			}
		}
	}
}

// SyncProject is project_sync.py's sync_project. projectPath is "" when
// not given.
func SyncProject(out Out, aikitoDir, home, projectName, projectPath string, dryRun, force bool) bool {
	projDir := filepath.Join(aikitoDir, "projects", projectName)
	agentTOML := filepath.Join(projDir, "agent.toml")
	if !isFile(agentTOML) {
		out.eprintln("[ERROR] Project '%s' not found at %s.", projectName, projDir)
		return false
	}
	data, err := loadTOMLMap(agentTOML)
	if err != nil {
		out.eprintln("[ERROR] Failed to read configuration for project '%s': %s", projectName, tomlErrorText(err))
		return false
	}
	binding := project.ResolveProjectBinding(data, home)
	targetPath := ""
	if projectPath != "" {
		targetPath, _ = compat.ResolvePath(workspace.ExpandUser(home, projectPath))
		if !exists(targetPath) {
			out.eprintln("[ERROR] Project path does not exist: %s", targetPath)
			return false
		}
		if !isDir(targetPath) {
			out.eprintln("[ERROR] Project path is not a directory: %s", targetPath)
			return false
		}
	} else {
		if len(binding.Entries) == 0 {
			out.eprintln("[ERROR] Project path not provided and no saved path found for project '%s'.\nUsage: aikito sync project %s <project_path>", projectName, projectName)
			return false
		}
		if len(binding.ActiveEntries()) == 0 {
			var lines []string
			for _, e := range binding.OfflineEntries() {
				lines = append(lines, fmt.Sprintf("  - [%s] %s", e.Label, e.ResolvedPath))
			}
			out.eprintln("[ERROR] None of the configured paths for project '%s' exist on this machine:\n%s\n\n"+
				"Please clone/create the project directory or specify the path explicitly:\n  aikito sync project %s <project_path>",
				projectName, strings.Join(lines, "\n"), projectName)
			return false
		}
	}

	b := BuildBatch(aikitoDir, home, projectName, data, targetPath, force)
	if !b.CanApply {
		for _, e := range b.PreflightFindings {
			out.eprintln("[ERROR] %s", e)
		}
		for _, op := range b.SkillPlan.Operations {
			if op.Action == "CONFLICT" {
				out.eprintln("[CONFLICT] %s", op.Reason)
			} else if op.Finding != "" && !contains(b.PreflightFindings, op.Finding) {
				out.eprintln("[ERROR] %s", op.Finding)
			}
		}
		printLinkConflicts := func(ops []LinkOperation, label string) {
			for _, op := range ops {
				if op.Action == "CONFLICT" {
					prefix := "[CONFLICT]"
					if op.ResourceName != "" {
						prefix = fmt.Sprintf("[CONFLICT] %s %s:", op.ResourceName, label)
					}
					out.eprintln("%s %s", prefix, op.Reason)
				} else if op.Finding != "" && !contains(b.PreflightFindings, op.Finding) {
					out.eprintln("[ERROR] %s", op.Finding)
				}
			}
		}
		if b.InstructionPlan != nil {
			printLinkConflicts(b.InstructionPlan.Operations, "instructions")
		}
		if b.MemoryPlan != nil {
			printLinkConflicts(b.MemoryPlan.Operations, "memory")
		}
		out.eprintln("[ERROR] Project synchronization aborted.")
		return false
	}

	operation := "Syncing"
	if dryRun {
		operation = "Previewing sync for"
	}
	mode := syncMode(data)
	result := "synced successfully"
	if dryRun {
		result = "sync preview completed"
	}

	if targetPath != "" {
		out.println("[INFO] %s project '%s' (mode: %s)", operation, projectName, mode)
		renderBatchOps(out, b, targetPath, dryRun)
		if !dryRun && b.ConfigCAS != nil && !b.ConfigCAS.IsNoop {
			out.println("[INFO] Registered codebase path for project '%s'.", projectName)
		}
		if msg := applyBatch(out, b, home, dryRun); msg != "" {
			out.eprintln("[ERROR] %s", msg)
			return false
		}
		out.println("[SUCCESS] Project '%s' %s at %s.", projectName, result, targetPath)
		return true
	}

	activeEntries := binding.ActiveEntries()
	multi := len(activeEntries) > 1
	if multi {
		out.println("[INFO] %s project '%s' across %d active paths:", operation, projectName, len(activeEntries))
	}
	for i, e := range activeEntries {
		if multi {
			out.println("\n[INFO] === [%d/%d] Path [%s]: %s ===", i+1, len(activeEntries), e.Label, e.ResolvedPath)
		} else {
			out.println("[INFO] %s project '%s' (mode: %s)", operation, projectName, mode)
		}
		renderBatchOps(out, b, e.ResolvedPath, dryRun)
	}
	if msg := applyBatch(out, b, home, dryRun); msg != "" {
		out.eprintln("[ERROR] %s", msg)
		return false
	}
	out.println("[SUCCESS] Project '%s' %s.", projectName, result)
	return true
}
