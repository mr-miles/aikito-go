package projectsync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/registry"
)

// InstructionBatch is instructions.py's InstructionBatch (project scope).
type InstructionBatch struct {
	Scope            string
	CanonicalSource  string
	Targets          []registry.Target
	Enabled          bool
	StaleTargets     []registry.Target
	Checkout         string
	ProjectName      string
	OfflineCheckouts []string
}

// InstructionPlan is instructions.py's InstructionPlan.
type InstructionPlan struct {
	Batch      InstructionBatch
	Operations []LinkOperation
}

func (p InstructionPlan) Conflicts() []LinkOperation {
	var out []LinkOperation
	for _, op := range p.Operations {
		if op.Action == "CONFLICT" {
			out = append(out, op)
		}
	}
	return out
}

func (p InstructionPlan) CanApply() bool {
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

// InstructionResult is instructions.py's InstructionExecutionResult.
type InstructionResult struct {
	Success      bool
	ErrorMessage string
}

// canonicalNonEmpty is `canonical.read_text(errors="replace").strip() != ""`.
func canonicalNonEmpty(p string) (bool, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), "�")) != "", nil
}

// BuildProjectInstructionBatch is build_project_instruction_batch.
func BuildProjectInstructionBatch(workspaceRoot, projectName string, active []string, home string, reg *registry.AgentRegistry, offline []string) (InstructionBatch, error) {
	if reg == nil {
		reg = registry.Load(workspaceRoot, home)
	}
	canonical := filepath.Join(workspaceRoot, "projects", projectName, "AGENTS.md")
	var all, stale []registry.Target
	addTarget := func(t registry.Target) {
		for i, existing := range all {
			if isSameTargetLocation(t.Path, existing.Path) {
				merged := existing
				merged.Consumers = uniqueStrings(append(append([]string(nil), existing.Consumers...), t.Consumers...))
				merged.ConsumerDisplayNames = uniqueStrings(append(append([]string(nil), existing.ConsumerDisplayNames...), t.ConsumerDisplayNames...))
				// Python's merged Target drops consumer_agents, so availability
				// falls back to bundled_agent(name) for each consumer.
				merged.ConsumerAgents = nil
				for _, n := range merged.Consumers {
					if a, err := registry.BundledAgent(n, home); err == nil {
						merged.ConsumerAgents = append(merged.ConsumerAgents, a)
					} else if a, ok := reg.Get(n); ok {
						merged.ConsumerAgents = append(merged.ConsumerAgents, a)
					}
				}
				all[i] = merged
				return
			}
		}
		all = append(all, t)
	}
	for _, co := range active {
		targets, err := registry.ResolveTargets("project_instructions", workspaceRoot, home, reg,
			registry.ResolveTargetsOptions{ProjectPath: co, ProjectName: projectName})
		if err != nil {
			return InstructionBatch{}, err
		}
		for _, t := range targets {
			addTarget(t)
		}
		legacy := filepath.Join(co, ".agents", "AGENTS.md")
		formal := false
		for _, t := range all {
			if isSameTargetLocation(t.Path, legacy) {
				formal = true
			}
		}
		if !formal && lexists(legacy) {
			dup := false
			for _, s := range stale {
				if isSameTargetLocation(legacy, s.Path) {
					dup = true
				}
			}
			if !dup {
				stale = append(stale, registry.Target{Kind: "instruction_link", Scope: "project", Path: legacy,
					CanonicalSource: canonical, ConsumerDisplayNames: []string{"Legacy .agents"}})
			}
		}
	}
	for _, co := range offline {
		targets, err := registry.ResolveTargets("project_instructions", workspaceRoot, home, reg,
			registry.ResolveTargetsOptions{ProjectPath: co, ProjectName: projectName})
		if err != nil {
			return InstructionBatch{}, err
		}
		for _, t := range targets {
			addTarget(t)
		}
	}
	enabled := false
	if isFile(canonical) {
		enabled, _ = canonicalNonEmpty(canonical)
	}
	first := ""
	if len(active) > 0 {
		first = active[0]
	} else if len(offline) > 0 {
		first = offline[0]
	}
	return InstructionBatch{Scope: "project", CanonicalSource: canonical, Targets: all, Enabled: enabled,
		StaleTargets: stale, Checkout: first, ProjectName: projectName, OfflineCheckouts: offline}, nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func targetResourceName(t registry.Target) string {
	if n := strings.Join(t.ConsumerDisplayNames, "/"); n != "" {
		return n
	}
	return filepath.Base(t.Path)
}

// PlanInstructions is plan_instructions (not offline).
func PlanInstructions(batch InstructionBatch, home string) InstructionPlan {
	var ops []LinkOperation
	canonicalValid := isFile(batch.CanonicalSource)
	canonicalError := ""
	if batch.Scope == "global" && !canonicalValid {
		canonicalError = fmt.Sprintf("Global instruction file not found: %s", batch.CanonicalSource)
	}
	desired := "absent"
	if batch.Enabled {
		desired = "link"
	}
	instr := func(action, rule, target, reason, exp, des, name string) LinkOperation {
		return LinkOperation{Action: action, RuleID: rule, TargetPath: target, CanonicalPath: batch.CanonicalSource,
			Reason: reason, ExpectedRepresentation: exp, DesiredRepresentation: des, TargetKind: "instruction_link",
			ResourceName: name, IsAuthorized: true}
	}
	for _, target := range batch.Targets {
		name := targetResourceName(target)
		offline := false
		for _, off := range batch.OfflineCheckouts {
			if target.Path == off || isRelativeTo(target.Path, off) {
				offline = true
			}
		}
		if offline {
			ops = append(ops, instr("SKIP", "INV-INST-11", target.Path, fmt.Sprintf("Checkout is offline: %s", target.Path), "missing", "link", name))
			continue
		}
		sameObj := target.IsSameObject() && !isSymlink(target.Path)
		o := InspectLinkTarget(target.Path, batch.CanonicalSource, canonicalValid, canonicalError, "instruction_link", batch.Scope, sameObj)
		avail := registry.CheckTargetAvailability(target, home)
		parentExists := exists(filepath.Dir(target.Path))
		op := PlanLinkTarget(o, desired, PlanLinkOptions{AvailabilityStatus: avail.Status, ParentExists: &parentExists, ResourceName: name})

		if batch.Enabled {
			switch {
			case o.IsSameObject:
				op = instr("SHARED_PATH", "INV-INST-05", target.Path,
					fmt.Sprintf("Target path is the same physical object as canonical instructions; no link required for '%s'", name), o.EntryType, "link", name)
				op.IsSameObject = true
			case !canonicalValid:
				op = instr("CONFLICT", "INV-TR-02", target.Path, fmt.Sprintf("Canonical instruction file not found: %s", batch.CanonicalSource), o.EntryType, "link", name)
				op.Finding = fmt.Sprintf("Missing instruction source: %s", batch.CanonicalSource)
				op.IsAuthorized = false
			case o.EntryType == "file":
				op = instr("CONFLICT", "INV-INST-03", target.Path, fmt.Sprintf("Target preserved: %s. "+
					"Target is a regular file; Aikito does not overwrite existing instruction files. "+
					"Move or remove it manually, then run sync again.", target.Path), "file", "link", name)
				op.Finding = fmt.Sprintf("Pre-existing regular instruction file: %s", target.Path)
				op.IsAuthorized = false
			case o.EntryType == "symlink":
				if o.LinkPointsToCanonical {
					op = instr("NOOP", "INV-INST-12", target.Path, fmt.Sprintf("Symbolic link already points to canonical instructions for '%s'", name), "symlink", "link", name)
				} else {
					rule := "INV-INST-02"
					if batch.Scope == "global" {
						rule = "INV-INST-06"
					}
					dest := observedDest(o)
					op = instr("CONFLICT", rule, target.Path, fmt.Sprintf("Target preserved: %s. "+
						"Symbolic link points to unauthorized destination: %s (expected %s). "+
						"Foreign or external instruction symlinks will not be overwritten automatically; "+
						"inspect manually, then run sync again.", target.Path, dest, batch.CanonicalSource), "symlink", "link", name)
					op.Finding = fmt.Sprintf("Unauthorized instruction symlink destination: %s -> %s", target.Path, dest)
					op.IsAuthorized = false
				}
			case o.EntryType == "missing":
				if op.Action == "CREATE" {
					rule := "INV-TR-01"
					if batch.Scope == "project" {
						rule = "INV-INST-07"
					}
					requires := op.RequiresParentCreation
					op = instr("CREATE", rule, target.Path, fmt.Sprintf("Create symbolic link: %s -> %s", target.Path, batch.CanonicalSource), "missing", "link", name)
					op.RequiresParentCreation = requires
				}
			}
		} else {
			switch {
			case o.IsSameObject:
				op = instr("NOOP", "INV-INST-05", target.Path, fmt.Sprintf("Canonical instructions file itself is not deleted: %s", target.Path), o.EntryType, "absent", name)
				op.IsSameObject = true
			case o.EntryType == "symlink":
				if o.LinkPointsToCanonical {
					op = instr("UNLINK", "INV-INST-08", target.Path, fmt.Sprintf("Remove unselected instruction symbolic link: %s", target.Path), "symlink", "absent", name)
				} else {
					dest := observedDest(o)
					op = instr("NOOP", "INV-INST-08", target.Path, fmt.Sprintf("Preserve foreign or unmanaged symbolic link: %s -> %s", target.Path, dest), "symlink", "absent", name)
					op.Finding = fmt.Sprintf("Unmanaged instruction symlink: %s", target.Path)
				}
			case o.EntryType == "file":
				op = instr("NOOP", "INV-INST-10", target.Path, fmt.Sprintf("Preserve project-owned instruction file: %s", target.Path), "file", "absent", name)
				op.Finding = fmt.Sprintf("Project-owned instruction file preserved: %s", target.Path)
			case o.EntryType == "missing":
				op = instr("NOOP", "INV-INST-12", target.Path, fmt.Sprintf("Instruction link is already absent: %s", target.Path), "missing", "absent", name)
			}
		}
		ops = append(ops, op)
	}

	for _, st := range batch.StaleTargets {
		o := InspectLinkTarget(st.Path, batch.CanonicalSource, canonicalValid, "", "instruction_link", batch.Scope, false)
		name := targetResourceName(st)
		var op LinkOperation
		switch o.EntryType {
		case "symlink":
			if o.LinkPointsToCanonical {
				op = instr("UNLINK", "INV-INST-09", st.Path, fmt.Sprintf("Remove legacy instruction link: %s", st.Path), "symlink", "absent", name)
			} else {
				op = instr("NOOP", "INV-INST-09", st.Path, fmt.Sprintf("Preserve unmanaged legacy item: %s", st.Path), "symlink", "absent", name)
				op.Finding = fmt.Sprintf("Unmanaged legacy instruction item: %s", st.Path)
			}
		case "missing":
			op = instr("NOOP", "INV-INST-09", st.Path, fmt.Sprintf("Legacy instruction entry is already absent: %s", st.Path), "missing", "absent", name)
		default:
			op = instr("NOOP", "INV-INST-09", st.Path, fmt.Sprintf("Preserve unmanaged legacy item of type %s: %s", o.EntryType, st.Path), o.EntryType, "absent", name)
			op.Finding = fmt.Sprintf("Unmanaged legacy instruction entry: %s", st.Path)
		}
		ops = append(ops, op)
	}
	return InstructionPlan{Batch: batch, Operations: ops}
}

// ExecuteInstructionPlan is execute_instruction_plan.
func ExecuteInstructionPlan(out Out, plan InstructionPlan, dryRun bool) InstructionResult {
	if !plan.CanApply() && !dryRun {
		return InstructionResult{ErrorMessage: "Instruction plan contains unresolved conflicts."}
	}
	src := plan.Batch.CanonicalSource
	if plan.Batch.Enabled {
		if !isFile(src) {
			return InstructionResult{ErrorMessage: fmt.Sprintf("Preflight failed: canonical instruction file not found: %s (stale plan)", src)}
		}
		if plan.Batch.Scope == "project" {
			nonEmpty, err := canonicalNonEmpty(src)
			if err != nil {
				return InstructionResult{ErrorMessage: "Preflight failed: could not read canonical instruction file: " + pyOSError(err)}
			}
			if !nonEmpty {
				return InstructionResult{ErrorMessage: fmt.Sprintf("Preflight failed: canonical instruction file changed from non-empty to empty: %s (stale plan)", src)}
			}
		}
	} else if isFile(src) {
		nonEmpty, err := canonicalNonEmpty(src)
		if err != nil {
			return InstructionResult{ErrorMessage: "Preflight failed: could not read canonical instruction file: " + pyOSError(err)}
		}
		if nonEmpty {
			return InstructionResult{ErrorMessage: fmt.Sprintf("Preflight failed: canonical instruction file changed from empty to non-empty: %s (stale plan)", src)}
		}
	}
	for _, op := range plan.Operations {
		res := ApplyLinkOperation(out, op, dryRun, false)
		if !res.Success {
			msg := res.ErrorMessage
			if msg == "" {
				msg = fmt.Sprintf("Failed operation on %s", op.TargetPath)
			}
			return InstructionResult{ErrorMessage: msg}
		}
	}
	return InstructionResult{Success: true}
}
