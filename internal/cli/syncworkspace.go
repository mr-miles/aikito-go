package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/sync"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// Whole-workspace `aikito sync`: ports cli.py's cmd_sync_all,
// workspace/sync.py's build_workspace_sync_plan /
// execute_workspace_sync_plan, and render.py's render_workspace_sync_plan.

// projectSyncEntry ports ProjectSyncEntry.
type projectSyncEntry struct {
	name         string
	status       string // "active", "offline", "unbound", "error"
	data         map[string]any
	batch        *projectsync.Batch
	offlinePaths []string
	errorMessage string
}

func (e projectSyncEntry) observe() sync.PlanObservation {
	configError := func() sync.Finding {
		msg := e.errorMessage
		if msg == "" {
			msg = fmt.Sprintf("Project '%s' has configuration errors.", e.name)
		}
		return sync.Finding{Status: "ERROR", Code: "PROJECT_CONFIG_ERROR", Message: msg, Resource: e.name}
	}
	hasError := e.status == "error" || e.errorMessage != ""
	if e.batch != nil {
		obs := e.batch.Observe()
		if hasError {
			no := false
			return sync.CombineObservations([]sync.PlanObservation{obs}, []sync.Finding{configError()}, &no)
		}
		return obs
	}
	if hasError {
		return sync.PlanObservation{Findings: []sync.Finding{configError()}, CanApply: false}
	}
	return sync.PlanObservation{CanApply: true}
}

// workspaceSyncPlan ports WorkspaceSyncPlan.
type workspaceSyncPlan struct {
	global         globalSyncPlan
	subagentOps    []sync.SubagentOperation
	hasSubagents   bool
	mcpPlan        *mcp.MCPPlan
	projects       []projectSyncEntry
	findings       []sync.Finding
	canApply       bool
	replanRequired bool
}

func (p workspaceSyncPlan) observe() sync.PlanObservation {
	children := []sync.PlanObservation{p.global.observe()}
	if p.hasSubagents {
		children = append(children, sync.ObserveSubagentPlan(p.subagentOps))
	}
	if p.mcpPlan != nil {
		children = append(children, p.mcpPlan.Observe())
	}
	for _, e := range p.projects {
		children = append(children, e.observe())
	}
	canApply := p.canApply
	return sync.CombineObservations(children, p.findings, &canApply)
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func (p workspaceSyncPlan) offline() int {
	n := 0
	for _, e := range p.projects {
		if e.status == "offline" {
			n++
		}
	}
	return n
}

func (p workspaceSyncPlan) conflicts(obs sync.PlanObservation) []string {
	var out []string
	for _, f := range obs.Findings {
		if sync.IsConflictFinding(f) {
			out = appendUnique(out, f.Message)
		}
	}
	for _, f := range obs.Findings {
		if f.Code == "MCP_ERROR" {
			out = appendUnique(out, f.Message)
		}
	}
	return out
}

func (p workspaceSyncPlan) errors(obs sync.PlanObservation) []string {
	var out []string
	if p.global.errorMessage != "" {
		out = append(out, p.global.errorMessage)
	}
	for _, f := range obs.Findings {
		if sync.IsErrorFinding(f) && f.Code != "MCP_ERROR" {
			out = appendUnique(out, f.Message)
		}
	}
	return out
}

func (p workspaceSyncPlan) warnings(obs sync.PlanObservation) []string {
	var out []string
	for _, f := range obs.Findings {
		if sync.IsWarningFinding(f) {
			out = appendUnique(out, f.Message)
		}
	}
	return out
}

// finish applies WorkspaceSyncPlan.__post_init__.
func (p workspaceSyncPlan) finish() workspaceSyncPlan {
	if p.canApply && !p.observe().CanApply {
		p.canApply = false
	}
	return p
}

// buildWorkspaceSyncPlan ports build_workspace_sync_plan with the defaults
// cmd_sync_all uses (no force, no prune).
func buildWorkspaceSyncPlan(aikitoDir string, env Environment) workspaceSyncPlan {
	home := env.Home
	containerPath := filepath.Join(home, ".agents", "skills")
	if agentsDir := env.Env.Getenv("AIKITO_AGENTS_DIR"); agentsDir != "" {
		containerPath = filepath.Join(agentsDir, "skills")
	}
	plan := workspaceSyncPlan{global: buildGlobalSyncPlan(aikitoDir, home, globalPlanOptions{containerPath: containerPath})}

	ops, err := sync.BuildSubagentPlan(aikitoDir, home, sync.BuildSubagentPlanOptions{GateInstalled: true})
	if err != nil {
		plan.findings = []sync.Finding{{Status: "error", Message: fmt.Sprintf("Subagent configuration error: %v", err), Resource: "subagents", Code: "SUBAGENT_CONFIG_ERROR"}}
		return plan.finish()
	}
	plan.subagentOps, plan.hasSubagents = ops, true

	mcpPlan, err := mcp.BuildMCPPlan(aikitoDir, home, mcp.BuildMCPPlanOptions{})
	if err != nil {
		plan.findings = []sync.Finding{{Status: "error", Message: fmt.Sprintf("MCP configuration error: %v", err), Resource: "mcp", Code: "MCP_CONFIG_ERROR"}}
		return plan.finish()
	}
	plan.mcpPlan = &mcpPlan

	projectsDir := filepath.Join(aikitoDir, "projects")
	entries, _ := os.ReadDir(projectsDir)
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if info, err := os.Stat(filepath.Join(projectsDir, e.Name())); err == nil && info.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		agentTOML := filepath.Join(projectsDir, name, "agent.toml")
		if !isRegularFile(agentTOML) {
			continue
		}
		raw, err := os.ReadFile(agentTOML)
		var data map[string]any
		if err == nil {
			data, err = workspace.DecodeTOML(raw)
		}
		if err != nil {
			plan.projects = append(plan.projects, projectSyncEntry{name: name, status: "error",
				errorMessage: fmt.Sprintf("Failed to read configuration for project '%s': %v", name, err)})
			continue
		}
		binding := project.ResolveProjectBinding(data, home)
		if len(binding.Entries) == 0 {
			plan.projects = append(plan.projects, projectSyncEntry{name: name, status: "unbound", data: data})
			continue
		}
		var offlinePaths []string
		for _, e := range binding.OfflineEntries() {
			offlinePaths = append(offlinePaths, e.RawPath)
		}
		if len(binding.ActiveEntries()) == 0 {
			plan.projects = append(plan.projects, projectSyncEntry{name: name, status: "offline", data: data, offlinePaths: offlinePaths})
			continue
		}
		batch := projectsync.BuildBatch(aikitoDir, home, name, data, "", false)
		plan.projects = append(plan.projects, projectSyncEntry{name: name, status: "active", data: data, batch: &batch, offlinePaths: offlinePaths})
	}

	plan.canApply = plan.global.canApply && sync.SubagentPlanCanApply(plan.subagentOps) && mcpPlan.CanApply()
	hasBatch := false
	for _, e := range plan.projects {
		if e.batch != nil {
			hasBatch = true
			if !e.batch.CanApply {
				plan.canApply = false
			}
		}
		if e.status == "error" {
			plan.canApply = false
		}
	}
	plan.replanRequired = plan.global.replanRequired && hasBatch
	return plan.finish()
}

// renderWorkspaceSyncPlan ports render_workspace_sync_plan.
func renderWorkspaceSyncPlan(p workspaceSyncPlan, verbose bool) string {
	obs := p.observe()
	summary := obs.Summary()
	warnings, conflicts, errs := p.warnings(obs), p.conflicts(obs), p.errors(obs)
	lines := []string{
		"Sync plan",
		"",
		fmt.Sprintf("  Changes:   %d", summary.Changes()),
		fmt.Sprintf("  Unchanged: %d", summary.Unchanged),
		fmt.Sprintf("  Offline:   %d", p.offline()),
		fmt.Sprintf("  Warnings:  %d", len(warnings)),
		fmt.Sprintf("  Conflicts: %d", len(conflicts)),
		fmt.Sprintf("  Errors:    %d", len(errs)),
	}
	important := append(append(append([]string{}, warnings...), conflicts...), errs...)
	if len(important) > 0 {
		lines = append(lines, "", "Needs attention:")
		for _, l := range important {
			lines = append(lines, "  "+l)
		}
	}
	if p.canApply {
		lines = append(lines, "", "Safe to apply")
	} else {
		lines = append(lines, "", "Blocked; no changes were made")
	}
	if verbose {
		var details []string
		add := func(child *sync.PlanObservation, format func(v sync.PlanOperationView) string) {
			if child == nil {
				return
			}
			for _, v := range child.Operations {
				if v.Effect != sync.EffectNoop {
					details = append(details, format(v))
				}
			}
		}
		bundled := observeBundledRefresh(p.global.bundled)
		add(&bundled, func(v sync.PlanOperationView) string {
			return fmt.Sprintf("  [%s] bundled skill '%s'", v.DomainAction, v.ResourceName)
		})
		sourceTarget := func(v sync.PlanOperationView) string {
			return fmt.Sprintf("  [%s] %s -> %s", v.DomainAction, v.Source, v.Target)
		}
		if p.global.skillPlan != nil {
			o := p.global.skillPlan.Observe()
			add(&o, sourceTarget)
		}
		if p.global.instrPlan != nil {
			o := p.global.instrPlan.Observe()
			add(&o, sourceTarget)
		}
		if p.hasSubagents {
			o := sync.ObserveSubagentPlan(p.subagentOps)
			add(&o, func(v sync.PlanOperationView) string {
				return fmt.Sprintf("  [%s] %s/%s -> %s", v.DomainAction, v.Agent, v.ResourceName, v.Target)
			})
		}
		if p.mcpPlan != nil {
			o := p.mcpPlan.Observe()
			add(&o, func(v sync.PlanOperationView) string {
				return fmt.Sprintf("  [%s] %s/%s (%s)", v.DomainAction, v.Agent, v.ResourceName, v.Reason)
			})
		}
		for _, e := range p.projects {
			switch {
			case e.status == "offline":
				candidates := strings.Join(e.offlinePaths, ", ")
				if candidates == "" {
					candidates = "-"
				}
				details = append(details, fmt.Sprintf("  Project '%s': offline on this host (%s), skipping.", e.name, candidates))
			case e.status == "unbound":
				details = append(details, fmt.Sprintf("  Project '%s': no configured paths (unbound), skipping.", e.name))
			case e.status == "active" && e.batch != nil:
				o := e.batch.SkillPlan.Observe()
				add(&o, func(v sync.PlanOperationView) string {
					return fmt.Sprintf("  [%s] %s/%s -> %s", v.DomainAction, v.Project, v.ResourceName, v.Target)
				})
			}
		}
		if len(details) > 0 {
			lines = append(lines, "", "Details", "")
			lines = append(lines, details...)
		}
	}
	return strings.Join(lines, "\n")
}

// executeWorkspaceSyncPlan ports execute_workspace_sync_plan for a real
// run (cmd_sync_all never executes a dry run). It returns "" on success,
// else the error message.
func executeWorkspaceSyncPlan(p workspaceSyncPlan, aikitoDir, home string, stdout, stderr io.Writer) (bool, string) {
	if !p.canApply || !p.observe().CanApply {
		return false, "Workspace sync plan contains unhandled conflicts or errors; cannot apply."
	}

	g := executeGlobalSyncPlan(p.global, aikitoDir, home, false, stdout, stderr)
	if !g.success {
		return false, or(g.errorMessage, "Global sync failed.")
	}
	hasBatch := false
	for _, e := range p.projects {
		if e.batch != nil {
			hasBatch = true
		}
	}
	if g.replanRequired && hasBatch {
		return false, "Bundled skills refreshed or canonical skills changed during global sync; " +
			"workspace sync plan invalidated. Please re-run 'aikito sync'."
	}

	if p.hasSubagents {
		if msg, err := sync.ApplySubagentPlan(p.subagentOps, home); err != nil {
			return false, msg
		}
	}

	if p.mcpPlan != nil {
		res, err := mcp.ExecuteMCPPlan(*p.mcpPlan, home, func(line string) { fmt.Fprintln(stdout, line) })
		if err != nil {
			return false, err.Error()
		}
		if !res.Success {
			return false, or(res.ErrorMessage, "MCP sync failed.")
		}
	}

	out := projectsync.Out{Stdout: stdout, Stderr: stderr}
	firstError := ""
	projectsOK := true
	for _, e := range p.projects {
		if e.batch == nil {
			continue
		}
		if msg := projectsync.ApplyBatch(out, *e.batch, home, false); msg != "" {
			projectsOK = false
			if firstError == "" {
				firstError = msg
			}
		}
	}
	if !projectsOK || !p.canApply {
		return false, firstError
	}
	return true, ""
}

// cmdSyncAll ports cli.py's cmd_sync_all.
func cmdSyncAll(dryRun, verbose bool, stdout, stderr io.Writer, env Environment) int {
	if !compat.CanSymlink() {
		fmt.Fprint(stderr, symlinkRequirementGuidance)
		return 1
	}
	aikitoDir, err := env.AikitoDir()
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}
	if err := requireLayoutLikePython(aikitoDir); err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return 1
	}

	plan := buildWorkspaceSyncPlan(aikitoDir, env)
	fmt.Fprintln(stdout, renderWorkspaceSyncPlan(plan, verbose))
	if !plan.canApply {
		return 1
	}
	if dryRun {
		return 0
	}
	if ok, msg := executeWorkspaceSyncPlan(plan, aikitoDir, env.Home, stdout, stderr); !ok {
		if msg != "" {
			fmt.Fprintf(stderr, "[ERROR] %s\n", msg)
		}
		return 1
	}
	fmt.Fprintln(stdout, "\n[SUCCESS] Full workspace sync completed successfully.")
	return 0
}
