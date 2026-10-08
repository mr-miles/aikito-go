package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// globalSyncPlan ports workspace/sync.py's GlobalSyncPlan, shared by
// `sync global` and the whole-workspace `aikito sync`.
type globalSyncPlan struct {
	bundled        []bundledRefreshOp
	skillPlan      *linkplan.GlobalSkillPlan
	instrPlan      *linkplan.InstructionPlan
	findings       []sync.Finding
	canApply       bool
	replanRequired bool
	errorMessage   string
}

// observe ports GlobalSyncPlan.observe.
func (p globalSyncPlan) observe() sync.PlanObservation {
	children := []sync.PlanObservation{observeBundledRefresh(p.bundled)}
	if p.skillPlan != nil {
		children = append(children, p.skillPlan.Observe())
	}
	if p.instrPlan != nil {
		children = append(children, p.instrPlan.Observe())
	}
	canApply := p.canApply
	return sync.CombineObservations(children, p.findings, &canApply)
}

type globalPlanOptions struct {
	containerPath string
	// outdatedFn mirrors passing outdated_bundled_skills, as `sync global`
	// does; the whole-workspace sync passes none.
	outdatedFn bool
}

func globalPlanFailure(errorMessage string, findings ...sync.Finding) globalSyncPlan {
	return globalSyncPlan{findings: findings, errorMessage: errorMessage}
}

func conflictMarkerFindings(errs []string, resource string) []sync.Finding {
	var out []sync.Finding
	for _, e := range errs {
		out = append(out, sync.Finding{Status: "ERROR", Code: "CONFLICT_MARKER", Message: e, Resource: resource})
	}
	return out
}

// buildGlobalSyncPlan ports build_global_sync_plan.
func buildGlobalSyncPlan(aikitoDir, home string, opts globalPlanOptions) globalSyncPlan {
	skillsToml := filepath.Join(aikitoDir, "skills.toml")
	instructionSource := filepath.Join(aikitoDir, "global", "AGENTS.md")

	if _, err := os.Stat(skillsToml); err != nil {
		msg := "Global skills configuration not found."
		return globalPlanFailure(msg, sync.Finding{Status: "ERROR", Code: "GLOBAL_SKILLS_MISSING", Message: msg, Resource: skillsToml})
	}
	if errs := collectResourceConflicts([]string{skillsToml}, home); len(errs) > 0 {
		return globalPlanFailure("Conflict markers detected in skills.toml.", conflictMarkerFindings(errs, skillsToml)...)
	}
	data, err := os.ReadFile(skillsToml)
	var doc map[string]any
	if err == nil {
		doc, err = workspace.DecodeTOML(data)
	}
	if err != nil {
		return globalPlanFailure(err.Error(), sync.Finding{Status: "ERROR", Code: "TOML_DECODE_ERROR",
			Message: fmt.Sprintf("Failed to read global skills configuration: %v", err), Resource: skillsToml})
	}
	var skills []string
	if raw, present := doc["skills"]; present {
		list, ok := raw.([]any)
		if !ok {
			return globalPlanFailure("Global skills configuration is malformed.", sync.Finding{Status: "ERROR", Code: "INVALID_CONFIG",
				Message: "Global skills configuration is malformed (expected a list of skill names).", Resource: skillsToml})
		}
		for _, v := range list {
			skills = append(skills, fmt.Sprint(v))
		}
	}

	bundled := planBundledRefresh(aikitoDir, opts.outdatedFn)
	outdated := refreshedNames(bundled)
	var globalResources []string
	if isRegularFile(instructionSource) {
		globalResources = append(globalResources, instructionSource)
	}
	for _, s := range skills {
		dir := filepath.Join(aikitoDir, "skills", s)
		if info, err := os.Stat(dir); err == nil && info.IsDir() && !outdated[s] {
			globalResources = append(globalResources, dir)
		}
	}
	if errs := collectResourceConflicts(globalResources, home); len(errs) > 0 {
		p := globalPlanFailure("Conflict markers detected in global resources.", conflictMarkerFindings(errs, "")...)
		p.bundled = bundled
		return p
	}

	agentError := func(err error) globalSyncPlan {
		p := globalPlanFailure(err.Error(), sync.Finding{Status: "ERROR", Code: "MCP_CONFIG_ERROR", Message: err.Error()})
		p.bundled = bundled
		return p
	}
	reg, err := registry.LoadStrict(aikitoDir, home)
	if err != nil {
		return agentError(err)
	}
	reg = reg.InFileOrder()
	batch, err := linkplan.BuildGlobalSkillBatch(aikitoDir, home, skills, reg, opts.containerPath)
	if err != nil {
		return agentError(err)
	}
	skillPlan := linkplan.PlanGlobalSkills(batch, home, outdated)
	instrBatch, err := linkplan.BuildGlobalInstructionBatch(aikitoDir, home, reg)
	if err != nil {
		return agentError(err)
	}
	instrPlan := linkplan.PlanInstructions(instrBatch, home, false)

	plan := globalSyncPlan{bundled: bundled, skillPlan: &skillPlan, instrPlan: &instrPlan, canApply: true, replanRequired: len(outdated) > 0}
	if obs := skillPlan.Observe(); obs.Summary().Blocked() || !obs.CanApply {
		plan.canApply = false
	}
	if !isRegularFile(instructionSource) {
		plan.canApply = false
		msg := fmt.Sprintf("Global instruction file not found: %s", instructionSource)
		plan.findings = append(plan.findings, sync.Finding{Status: "ERROR", Code: "GLOBAL_INSTRUCTION_MISSING", Message: msg, Resource: instructionSource})
	}
	if obs := instrPlan.Observe(); obs.Summary().Blocked() || !obs.CanApply {
		plan.canApply = false
	}
	if !plan.canApply {
		plan.errorMessage = "Conflicts detected in global plan."
	}
	return plan
}

// globalSyncResult ports GlobalSyncExecutionResult.
type globalSyncResult struct {
	success        bool
	skillRes       *linkplan.GlobalSkillExecutionResult
	instrRes       *linkplan.InstructionExecutionResult
	refreshed      []string
	replanRequired bool
	errorMessage   string
}

// executeGlobalSyncPlan ports execute_global_sync_plan.
func executeGlobalSyncPlan(plan globalSyncPlan, aikitoDir, home string, dryRun bool, stdout, stderr io.Writer) globalSyncResult {
	fail := func(msg string) globalSyncResult { return globalSyncResult{errorMessage: msg} }
	if plan.skillPlan == nil {
		return fail(or(plan.errorMessage, "Global sync plan cannot be applied."))
	}
	if obs := plan.skillPlan.Observe(); obs.Summary().Blocked() || !obs.CanApply {
		return fail(or(plan.errorMessage, "Global sync plan cannot be applied."))
	}

	res := globalSyncResult{}
	runSkills := func() bool {
		refreshed, err := executeBundledRefresh(plan.bundled, aikitoDir, home, dryRun, stdout)
		if err != nil {
			res.errorMessage = err.Error()
			return false
		}
		res.refreshed = refreshed
		if !dryRun {
			for _, name := range refreshed {
				if info, err := os.Stat(filepath.Join(aikitoDir, "skills", name)); err != nil || !info.IsDir() {
					res.errorMessage = fmt.Sprintf("Canonical skill '%s' not found after refresh.", name)
					return false
				}
			}
		}
		r := linkplan.ExecuteGlobalSkills(*plan.skillPlan, dryRun, false, stdout, stderr)
		res.skillRes = &r
		return true
	}
	if dryRun {
		if !runSkills() {
			return res
		}
	} else {
		lock, err := writerlock.Acquire(home)
		if err != nil {
			return fail(err.Error())
		}
		ok := runSkills()
		lock.Release()
		if !ok {
			return res
		}
	}
	if !res.skillRes.Success {
		res.errorMessage = res.skillRes.ErrorMessage
		return res
	}

	instructionSource := filepath.Join(aikitoDir, "global", "AGENTS.md")
	failedInstr := func(msg string) globalSyncResult {
		res.instrRes = &linkplan.InstructionExecutionResult{Success: false}
		res.errorMessage = msg
		return res
	}
	if !isRegularFile(instructionSource) {
		return failedInstr(fmt.Sprintf("Global instruction file not found: %s", instructionSource))
	}
	if plan.instrPlan != nil && len(plan.instrPlan.Conflicts()) > 0 {
		return failedInstr("Instruction targets have conflicts.")
	}
	if plan.instrPlan != nil {
		r := linkplan.ExecuteInstructionPlan(*plan.instrPlan, dryRun, false, stdout, stderr)
		res.instrRes = &r
		if !r.Success {
			res.errorMessage = "Instruction targets have conflicts."
			return res
		}
	}
	res.success = true
	res.replanRequired = plan.replanRequired
	return res
}

// syncGlobalResourcesQuiet ports workspace/sync.py's sync_global_resources,
// which add and rm call for --sync (cli.py's same-named function is the
// `sync global` command and reports more). It prints only what the
// executors print: no summary line, and nothing when the plan can't apply.
func syncGlobalResourcesQuiet(aikitoDir, home string, stdout, stderr io.Writer) bool {
	plan := buildGlobalSyncPlan(aikitoDir, home, globalPlanOptions{
		containerPath: filepath.Join(home, ".agents", "skills"),
	})
	if !plan.canApply {
		return false
	}
	res := executeGlobalSyncPlan(plan, aikitoDir, home, false, stdout, stderr)
	if !isRegularFile(filepath.Join(aikitoDir, "global", "AGENTS.md")) {
		return false
	}
	if plan.instrPlan != nil && len(plan.instrPlan.Conflicts()) > 0 {
		return false
	}
	return res.success
}

// syncGlobalResources ports cli.py's sync_global_resources.
func syncGlobalResources(aikitoDir, home string, dryRun bool, stdout, stderr io.Writer) bool {
	plan := buildGlobalSyncPlan(aikitoDir, home, globalPlanOptions{
		containerPath: filepath.Join(home, ".agents", "skills"),
		outdatedFn:    true,
	})
	aborted := func() bool {
		fmt.Fprintln(stderr, "[ERROR] Global synchronization aborted.")
		return false
	}

	if !plan.canApply {
		hasCode := func(codes ...string) bool {
			for _, f := range plan.findings {
				for _, c := range codes {
					if f.Code == c {
						return true
					}
				}
			}
			return false
		}
		for _, f := range plan.findings {
			switch f.Code {
			case "GLOBAL_SKILLS_MISSING", "TOML_DECODE_ERROR", "INVALID_CONFIG", "CONFLICT_MARKER", "MCP_CONFIG_ERROR":
				fmt.Fprintf(stderr, "[ERROR] %s\n", f.Message)
			}
		}
		switch plan.errorMessage {
		case "Conflict markers detected in skills.toml.", "Conflict markers detected in global resources.":
			return aborted()
		case "Global skills configuration not found.", "Global skills configuration is malformed.":
			return false
		}
		if hasCode("TOML_DECODE_ERROR", "MCP_CONFIG_ERROR") {
			return false
		}
		if plan.skillPlan != nil {
			if conflicts := plan.skillPlan.Conflicts(); len(conflicts) > 0 {
				for _, op := range conflicts {
					prefix := "[CONFLICT]"
					if op.RuleID == "INV-TR-02" || op.RuleID == "INV-TR-04" {
						prefix = "[ERROR]"
					}
					fmt.Fprintf(stderr, "%s %s\n", prefix, op.Reason)
				}
				return aborted()
			}
		}
	}

	res := executeGlobalSyncPlan(plan, aikitoDir, home, dryRun, stdout, stderr)

	instructionSource := filepath.Join(aikitoDir, "global", "AGENTS.md")
	if !isRegularFile(instructionSource) {
		fmt.Fprintf(stderr, "[ERROR] Global instruction file not found: %s\n", instructionSource)
		return false
	}
	if plan.instrPlan != nil {
		if conflicts := plan.instrPlan.Conflicts(); len(conflicts) > 0 {
			for _, op := range conflicts {
				if op.RuleID == "INV-TR-02" {
					fmt.Fprintf(stderr, "[ERROR] Global instruction file not found: %s\n", instructionSource)
					continue
				}
				prefix := "[CONFLICT]"
				if op.ResourceName != "" {
					prefix = fmt.Sprintf("[CONFLICT] %s instructions:", op.ResourceName)
				}
				fmt.Fprintf(stderr, "%s %s\n", prefix, op.Reason)
			}
			fmt.Fprintln(stderr, "[ERROR] Global skills were synced successfully, but one or more Agent instruction runtime targets have conflicts.")
			return false
		}
	}
	if !res.success {
		if res.errorMessage != "" {
			fmt.Fprintf(stderr, "[ERROR] %s\n", res.errorMessage)
		}
		switch {
		case res.instrRes != nil && !res.instrRes.Success:
			fmt.Fprintln(stderr, "[ERROR] Global skills were synced successfully, but one or more Agent instruction runtime targets have conflicts.")
		case res.skillRes != nil && !res.skillRes.Success:
			fmt.Fprintf(stderr, "[ERROR] Global skill synchronization aborted: %s\n", or(res.skillRes.ErrorMessage, "execution failed"))
		}
		return false
	}
	if plan.skillPlan != nil {
		consumers := 0
		if res.skillRes != nil {
			consumers = res.skillRes.ConsumerTargetCount
		}
		fmt.Fprintf(stdout, "[SUCCESS] Global resources synced successfully (%d skills, 1 instruction source, %d Agent skill entries across %d consumers).\n",
			len(plan.skillPlan.Batch.SelectedEntries), consumers, plan.skillPlan.Batch.ConsumerAgentCount())
	}
	return true
}
