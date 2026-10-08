package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/linkplan"
	"github.com/mr-miles/aikito-go/internal/registry"
	"github.com/mr-miles/aikito-go/internal/workspace"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// symlinkRequirementGuidance is compat.py's SYMLINK_REQUIREMENT_GUIDANCE.
const symlinkRequirementGuidance = "[ERROR] Aikito requires symbolic link support to manage Agent resources.\n" +
	"On Windows, symbolic links require enabling Developer Mode (no restart required):\n\n" +
	"  Option 1 (Windows Settings GUI):\n" +
	"    Settings -> System -> For developers -> Developer Mode -> Turn On\n\n" +
	"  Option 2 (PowerShell / Command Prompt as Administrator):\n" +
	`    reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock" /t REG_DWORD /f /v "AllowDevelopmentWithoutDevLicense" /d "1"` + "\n"

// argparseUnrecognized prints argparse's top-level "unrecognized arguments"
// error, which is what the reference CLI emits for any extra argument to a
// subcommand.
func argparseUnrecognized(stderr io.Writer, extra []string) int {
	usage := helpTexts["__noargs__"]
	if i := strings.Index(usage, "aikito: error:"); i >= 0 {
		usage = usage[:i]
	}
	fmt.Fprintf(stderr, "%saikito: error: unrecognized arguments: %s\n", usage, strings.Join(extra, " "))
	return 2
}

// requireLayoutLikePython is cli.py main()'s gate: only a directory that
// already looks like a workspace has to be on the current layout.
func requireLayoutLikePython(aikitoDir string) error {
	known := isRecognizedWorkspace(aikitoDir)
	for _, name := range []string{"agents.toml", "subagents.toml", "layout.toml"} {
		if _, err := os.Lstat(filepath.Join(aikitoDir, name)); err == nil {
			known = true
		}
	}
	if !known {
		return nil
	}
	return workspace.RequireCurrentLayout(aikitoDir)
}

// safeRelativePath is compat.py's safe_relative_path: "~/rel" under base,
// else the path as-is.
func safeRelativePath(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(path)
	}
	return "~/" + filepath.ToSlash(rel)
}

// collectResourceConflicts ports conflict.py's collect_resource_conflicts.
func collectResourceConflicts(paths []string, home string) []string {
	var errs []string
	seen := map[string]bool{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		var candidates []string
		switch {
		case info.Mode().IsRegular():
			candidates = []string{p}
		case info.IsDir():
			_ = filepath.WalkDir(p, func(fp string, d os.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					candidates = append(candidates, fp)
				}
				return nil
			})
		default:
			continue
		}
		for _, fp := range candidates {
			resolved, _ := compat.ResolvePath(fp)
			if seen[resolved] {
				continue
			}
			seen[resolved] = true
			blocking, _ := findConflictMarkerLines(fp)
			for _, ln := range blocking {
				errs = append(errs, fmt.Sprintf("%s:%d: Git conflict marker detected", safeRelativePath(fp, home), ln))
			}
		}
	}
	return errs
}

// cmdSyncGlobal ports cli.py's cmd_global_sync / sync_global_resources and
// workspace/sync.py's build_global_sync_plan + execute_global_sync_plan:
// the shared ~/.agents/skills container with one link per selected skill,
// one consumer link per distinct agent skills path (agents already reading
// ~/.agents/skills share it), and one link per distinct instruction path.
func cmdSyncGlobal(args []string, stdout, stderr io.Writer, env Environment) int {
	dryRun := false
	var extra []string
	for _, a := range args {
		if a == "--dry-run" {
			dryRun = true
		} else {
			extra = append(extra, a)
		}
	}
	if len(extra) > 0 {
		return argparseUnrecognized(stderr, extra)
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
	if !compat.CanSymlink() {
		fmt.Fprint(stderr, symlinkRequirementGuidance)
		return 1
	}
	if syncGlobalResources(aikitoDir, env.Home, dryRun, stdout, stderr) {
		return 0
	}
	return 1
}

func syncGlobalResources(aikitoDir, home string, dryRun bool, stdout, stderr io.Writer) bool {
	containerPath := filepath.Join(home, ".agents", "skills")
	skillsToml := filepath.Join(aikitoDir, "skills.toml")
	instructionSource := filepath.Join(aikitoDir, "global", "AGENTS.md")
	aborted := func() bool {
		fmt.Fprintln(stderr, "[ERROR] Global synchronization aborted.")
		return false
	}

	// --- build_global_sync_plan ---
	if _, err := os.Stat(skillsToml); err != nil {
		fmt.Fprintln(stderr, "[ERROR] Global skills configuration not found.")
		return false
	}
	if errs := collectResourceConflicts([]string{skillsToml}, home); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(stderr, "[ERROR] %s\n", e)
		}
		return aborted()
	}
	data, err := os.ReadFile(skillsToml)
	var doc map[string]any
	if err == nil {
		doc, err = workspace.DecodeTOML(data)
	}
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] Failed to read global skills configuration: %v\n", err)
		return false
	}
	var skills []string
	if raw, present := doc["skills"]; present {
		list, ok := raw.([]any)
		if !ok {
			fmt.Fprintln(stderr, "[ERROR] Global skills configuration is malformed (expected a list of skill names).")
			return false
		}
		for _, v := range list {
			skills = append(skills, fmt.Sprint(v))
		}
	}

	refreshOps := planBundledRefresh(aikitoDir)
	outdated := map[string]bool{}
	for _, op := range refreshOps {
		outdated[op.name] = true
	}
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
		for _, e := range errs {
			fmt.Fprintf(stderr, "[ERROR] %s\n", e)
		}
		return aborted()
	}

	reg, err := registry.LoadStrict(aikitoDir, home)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return false
	}
	reg = reg.InFileOrder()

	batch, err := linkplan.BuildGlobalSkillBatch(aikitoDir, home, skills, reg, containerPath)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return false
	}
	skillPlan := linkplan.PlanGlobalSkills(batch, home, outdated)
	instrBatch, err := linkplan.BuildGlobalInstructionBatch(aikitoDir, home, reg)
	if err != nil {
		fmt.Fprintf(stderr, "[ERROR] %v\n", err)
		return false
	}
	instrPlan := linkplan.PlanInstructions(instrBatch, home, false)

	// --- sync_global_resources: skill conflicts abort before any write ---
	if skillConflicts := skillPlan.Conflicts(); len(skillConflicts) > 0 || !skillPlan.CanApply() {
		for _, op := range skillConflicts {
			prefix := "[CONFLICT]"
			if op.RuleID == "INV-TR-02" || op.RuleID == "INV-TR-04" {
				prefix = "[ERROR]"
			}
			fmt.Fprintf(stderr, "%s %s\n", prefix, op.Reason)
		}
		return aborted()
	}

	// --- execute_global_sync_plan ---
	var skillRes linkplan.GlobalSkillExecutionResult
	errorMessage := ""
	runSkills := func() bool {
		refreshed, rerr := executeBundledRefresh(refreshOps, aikitoDir, home, dryRun, stdout)
		if rerr != nil {
			errorMessage = rerr.Error()
			return false
		}
		if !dryRun {
			for _, name := range refreshed {
				if info, err := os.Stat(filepath.Join(aikitoDir, "skills", name)); err != nil || !info.IsDir() {
					errorMessage = fmt.Sprintf("Canonical skill '%s' not found after refresh.", name)
					return false
				}
			}
		}
		skillRes = linkplan.ExecuteGlobalSkills(skillPlan, dryRun, false, stdout, stderr)
		return true
	}
	ranSkills := false
	if dryRun {
		ranSkills = runSkills()
	} else {
		lock, lerr := writerlock.Acquire(home)
		if lerr != nil {
			fmt.Fprintf(stderr, "[ERROR] %v\n", lerr)
			return false
		}
		ranSkills = runSkills()
		lock.Release()
	}

	instrConflicts := instrPlan.Conflicts()
	var instrRes *linkplan.InstructionExecutionResult
	success := ranSkills && skillRes.Success
	if ranSkills && !skillRes.Success {
		errorMessage = skillRes.ErrorMessage
	}
	if success && isRegularFile(instructionSource) && len(instrConflicts) == 0 {
		r := linkplan.ExecuteInstructionPlan(instrPlan, dryRun, false, stdout, stderr)
		instrRes = &r
		if !r.Success {
			success = false
			errorMessage = "Instruction targets have conflicts."
		}
	}

	// --- report (cli.py sync_global_resources) ---
	if !isRegularFile(instructionSource) {
		fmt.Fprintf(stderr, "[ERROR] Global instruction file not found: %s\n", instructionSource)
		return false
	}
	if len(instrConflicts) > 0 {
		for _, op := range instrConflicts {
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
	if !success {
		if errorMessage != "" {
			fmt.Fprintf(stderr, "[ERROR] %s\n", errorMessage)
		}
		switch {
		case instrRes != nil && !instrRes.Success:
			fmt.Fprintln(stderr, "[ERROR] Global skills were synced successfully, but one or more Agent instruction runtime targets have conflicts.")
		case ranSkills && !skillRes.Success:
			fmt.Fprintf(stderr, "[ERROR] Global skill synchronization aborted: %s\n", or(skillRes.ErrorMessage, "execution failed"))
		}
		return false
	}
	fmt.Fprintf(stdout, "[SUCCESS] Global resources synced successfully (%d skills, 1 instruction source, %d Agent skill entries across %d consumers).\n",
		len(batch.SelectedEntries), skillRes.ConsumerTargetCount, batch.ConsumerAgentCount())
	return true
}

func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
