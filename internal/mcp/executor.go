package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MCPExecutionResult is the structured result of applying an MCPPlan
// (executor.py's MCPExecutionResult).
type MCPExecutionResult struct {
	Success          bool
	AppliedCount     int
	NoopCount        int
	SkippedCount     int
	ConflictCount    int
	FailedCount      int
	BackupsCreated   []string
	FailedFiles      []string
	BackupWarnings   []string
	ErrorMessage     string
	RecoveryRequired bool
	RecoveryGuidance string
}

func countByAction(ops []MCPOperation, action string) int {
	n := 0
	for _, op := range ops {
		if op.Action == action {
			n++
		}
	}
	return n
}

// ExecuteMCPPlan mirrors execute_mcp_plan: a careful multi-phase commit with
// rollback (precondition re-validation, reject-if-not-CanApply, compute new
// state map in memory, write state to a temp file first, backup phase
// before any runtime file is touched, write phase with abort-and-roll-back
// on partial failure, and a state-promotion step that itself rolls back
// every just-written config on failure to keep config and state
// consistent). output receives one line per [SYNC]/[BACKUP]/[AUTH] message,
// matching Python's `output: Callable[[str], None] = print` parameter.
func ExecuteMCPPlan(plan MCPPlan, home string, output func(string)) (MCPExecutionResult, error) {
	if output == nil {
		output = func(string) {}
	}

	// 1. Validate preconditions.
	if err := plan.ValidatePreconditions(home); err != nil {
		output(fmt.Sprintf("[ERROR] MCP plan is stale: %v", err))
		return MCPExecutionResult{
			Success: false, FailedCount: len(plan.Operations),
			ErrorMessage: fmt.Sprintf("Plan is stale: %v", err),
		}, nil
	}

	// 2. Check conflicts/authorization.
	if !plan.CanApply() {
		return MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), ConflictCount: plan.ConflictsCount(),
			ErrorMessage: "Plan contains unauthorized conflicts",
		}, nil
	}

	// 3. Prepare new state in memory.
	state, err := LoadState(home)
	if err != nil {
		return MCPExecutionResult{}, err
	}
	newEntries := map[string]StateEntry{}
	for k, v := range state.Entries {
		newEntries[k] = v
	}
	for _, op := range plan.Operations {
		if !op.IsAuthorized {
			continue
		}
		switch op.Action {
		case "NOOP", "CREATE", "UPDATE":
			if op.StateTransition != nil {
				fp := ""
				if op.StateTransition.Fingerprint != nil {
					fp = *op.StateTransition.Fingerprint
				}
				newEntries[op.StateTransition.StateKey] = StateEntry{
					Fingerprint: fp, ConfigPath: op.Target.Path, TargetName: op.Target.TargetName,
				}
			}
		case "REMOVE":
			if op.StateTransition != nil {
				delete(newEntries, op.StateTransition.StateKey)
			}
			if op.Spec != nil {
				delete(newEntries, op.Spec.StateKey())
			}
			suffix := ":" + op.Target.LogicalIdentity
			for k := range newEntries {
				if hasSuffixCompat(k, suffix) {
					delete(newEntries, k)
				}
			}
		}
	}
	newState := MCPState{Version: state.Version, Entries: newEntries}
	if newState.Version == 0 {
		newState.Version = StateVersion
	}

	// 4. Write the new state to a temp file (same dir as the real state
	// file), not promoted yet.
	statePath := filepath.Join(home, StateFile)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o777); err != nil {
		return MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), FailedCount: len(plan.Operations),
			ErrorMessage: fmt.Sprintf("Failed to prepare state file: %v", err),
		}, nil
	}
	stateTmp, err := os.CreateTemp(filepath.Dir(statePath), ".mcp-state-*.tmp")
	if err != nil {
		return MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), FailedCount: len(plan.Operations),
			ErrorMessage: fmt.Sprintf("Failed to prepare state file: %v", err),
		}, nil
	}
	stateTmpPath := stateTmp.Name()
	if _, err := stateTmp.WriteString(renderStateJSON(newState)); err != nil {
		stateTmp.Close()
		os.Remove(stateTmpPath)
		return MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), FailedCount: len(plan.Operations),
			ErrorMessage: fmt.Sprintf("Failed to prepare state file: %v", err),
		}, nil
	}
	stateTmp.Close()

	// 5. Fast path: nothing actually mutates.
	var mutatingFiles []MCPFilePlan
	for _, fp := range plan.FilePlans {
		if fp.WillMutate() {
			mutatingFiles = append(mutatingFiles, fp)
		}
	}
	if len(mutatingFiles) == 0 {
		if err := os.Rename(stateTmpPath, statePath); err != nil {
			os.Remove(stateTmpPath)
			return MCPExecutionResult{
				FailedCount: 1, ErrorMessage: fmt.Sprintf("Failed to update state: %v", err),
			}, nil
		}
		return MCPExecutionResult{
			Success: true, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"),
		}, nil
	}

	// 6. Backup phase — every eligible file, BEFORE any runtime file write.
	type backupPair struct {
		fp  MCPFilePlan
		bak string
	}
	var backupsCreated []backupPair
	var backupErr error
	var failedBackupFP *MCPFilePlan

	for i := range mutatingFiles {
		fp := mutatingFiles[i]
		if !fp.ShouldBackup() {
			continue
		}
		bak, err := backupFilePlan(home, fp)
		if err != nil {
			backupErr = err
			failedBackupFP = &fp
			agentSrv := fp.Path
			if len(fp.Operations) > 0 {
				agentSrv = fp.Operations[0].Target.Agent + "/" + fp.Operations[0].Target.LogicalIdentity
			}
			output(fmt.Sprintf("[ERROR] %s: backup failed (%v); aborting before modifying runtime files", agentSrv, err))
			break
		}
		if bak != "" {
			backupsCreated = append(backupsCreated, backupPair{fp, bak})
		}
	}

	if backupErr != nil {
		for _, b := range backupsCreated {
			os.Remove(b.bak)
		}
		os.Remove(stateTmpPath)
		var failedFiles []string
		if failedBackupFP != nil {
			failedFiles = []string{failedBackupFP.Path}
		}
		return MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), FailedCount: 1,
			FailedFiles: failedFiles, ErrorMessage: fmt.Sprintf("Backup failed: %v", backupErr),
		}, nil
	}

	backupFor := func(path string) string {
		for _, b := range backupsCreated {
			if b.fp.Path == path {
				return b.bak
			}
		}
		return ""
	}

	// 7. Atomic write of each mutating file.
	type committedEntry struct {
		fp  MCPFilePlan
		bak string
	}
	var committed []committedEntry
	var writeErr error
	var failedWriteFP *MCPFilePlan

	for i := range mutatingFiles {
		fp := mutatingFiles[i]
		content := ""
		if fp.FinalContent != nil {
			content = *fp.FinalContent
		}
		if err := atomicWriteFile(fp.Path, content, fp.Sensitive); err != nil {
			writeErr = err
			failedWriteFP = &fp
			agentSrv := fp.Path
			if len(fp.Operations) > 0 {
				agentSrv = fp.Operations[0].Target.Agent + "/" + fp.Operations[0].Target.LogicalIdentity
			}
			output(fmt.Sprintf("[ERROR] %s: write failed (%v); rolling back all committed agent configs", agentSrv, err))
			break
		}
		committed = append(committed, committedEntry{fp, backupFor(fp.Path)})
	}

	rollback := func() (bool, map[string]bool, []string) {
		allSucceeded := true
		retained := map[string]bool{}
		var warnings []string
		for _, c := range committed {
			rbOK := false
			var rbErr error
			if c.fp.PreImage.Exists {
				orig := ""
				if c.fp.OrigContent != nil {
					orig = *c.fp.OrigContent
				}
				rbErr = atomicWriteFile(c.fp.Path, orig, c.fp.Sensitive)
			} else if _, err := os.Stat(c.fp.Path); err == nil {
				rbErr = os.Remove(c.fp.Path)
			}
			if rbErr == nil {
				rbOK = true
			} else {
				allSucceeded = false
				hint := ""
				if c.bak != "" {
					hint = "; backup retained at " + c.bak
				}
				agentSrv := c.fp.Path
				if len(c.fp.Operations) > 0 {
					agentSrv = c.fp.Operations[0].Target.Agent + "/" + c.fp.Operations[0].Target.LogicalIdentity
				}
				msg := fmt.Sprintf("%s: rollback failed (%v); manual inspection required%s", agentSrv, rbErr, hint)
				output("[WARN] " + msg)
				warnings = append(warnings, msg)
			}
			if c.bak != "" {
				if !rbOK {
					retained[c.bak] = true
				} else {
					os.Remove(c.bak)
				}
			}
		}
		return allSucceeded, retained, warnings
	}

	if writeErr != nil {
		os.Remove(stateTmpPath)
		rbSuccess, retained, warnings := rollback()
		for _, b := range backupsCreated {
			if !retained[b.bak] {
				os.Remove(b.bak)
			}
		}
		var failedFiles []string
		if failedWriteFP != nil {
			failedFiles = []string{failedWriteFP.Path}
		}
		result := MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), FailedCount: 1,
			FailedFiles: failedFiles, BackupsCreated: mapKeys(retained), BackupWarnings: warnings,
			ErrorMessage: fmt.Sprintf("Write failed: %v", writeErr), RecoveryRequired: !rbSuccess,
		}
		if !rbSuccess {
			result.RecoveryGuidance = joinStrings(warnings, "; ")
		}
		return result, nil
	}

	// 8. Promote state temp file.
	if err := os.Rename(stateTmpPath, statePath); err != nil {
		output(fmt.Sprintf("[ERROR] All agent configs written but state save failed: %v; rolling back runtime changes to keep state consistent", err))
		os.Remove(stateTmpPath)
		rbSuccess, retained, warnings := rollback()
		for _, b := range backupsCreated {
			if !retained[b.bak] {
				os.Remove(b.bak)
			}
		}
		result := MCPExecutionResult{
			Success: false, NoopCount: countByAction(plan.Operations, "NOOP"),
			SkippedCount: countByAction(plan.Operations, "SKIP"), FailedCount: 1,
			BackupsCreated: mapKeys(retained), BackupWarnings: warnings,
			ErrorMessage: fmt.Sprintf("State promotion failed: %v", err), RecoveryRequired: !rbSuccess,
		}
		if !rbSuccess {
			result.RecoveryGuidance = joinStrings(warnings, "; ")
		}
		return result, nil
	}

	// 9. Success: emit sync logs.
	for _, c := range committed {
		firstOpBackup := true
		for _, op := range c.fp.Operations {
			if op.IsAuthorized && (op.Action == "CREATE" || op.Action == "UPDATE" || op.Action == "REMOVE") {
				actionStr := ""
				if op.Action == "REMOVE" {
					actionStr = "removed from"
				} else {
					actionStr = strings.ToLower(op.Action) + "d"
				}
				output(fmt.Sprintf("[SYNC] %s/%s: %s %s", op.Target.Agent, op.Target.LogicalIdentity, actionStr, c.fp.Path))
				if firstOpBackup && c.bak != "" {
					output("[BACKUP] " + c.bak)
					firstOpBackup = false
				}
				if op.Spec != nil && len(op.Spec.AuthCommand) > 0 {
					output(fmt.Sprintf("[AUTH] aikito auth mcp %s %s", op.Target.Agent, op.Target.LogicalIdentity))
				}
			}
		}
	}

	var allBackups []string
	for _, b := range backupsCreated {
		allBackups = append(allBackups, b.bak)
	}
	return MCPExecutionResult{
		Success: true, AppliedCount: plan.ChangesCount(),
		NoopCount: countByAction(plan.Operations, "NOOP"), SkippedCount: countByAction(plan.Operations, "SKIP"),
		BackupsCreated: allBackups,
	}, nil
}

// backupConfig mirrors _backup_config: copy spec.ConfigPath to
// home/BackupDir/spec.Agent/<UTC-compact-timestamp>-<filename>, or ("", nil)
// if the config doesn't currently exist.
func backupConfig(home string, spec AgentSpec) (string, error) {
	if _, err := os.Stat(spec.ConfigPath); err != nil {
		return "", nil
	}
	return copyToBackup(home, spec.Agent, spec.ConfigPath)
}

// backupFilePlan mirrors _backup_file_plan.
func backupFilePlan(home string, fp MCPFilePlan) (string, error) {
	if _, err := os.Stat(fp.Path); err != nil {
		return "", nil
	}
	if len(fp.Operations) > 0 && fp.Operations[0].Spec != nil {
		return backupConfig(home, *fp.Operations[0].Spec)
	}
	agent := "common"
	if len(fp.Operations) > 0 {
		agent = fp.Operations[0].Target.Agent
	}
	return copyToBackup(home, agent, fp.Path)
}

// backupTimestamp mirrors Python's
// datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ"): compact UTC date
// + time + 6-digit microseconds + literal "Z".
func backupTimestamp(t time.Time) string {
	return t.UTC().Format("20060102T150405") + fmt.Sprintf("%06d", t.UTC().Nanosecond()/1000) + "Z"
}

func copyToBackup(home, agent, path string) (string, error) {
	backupDir := filepath.Join(home, BackupDir, agent)
	if err := os.MkdirAll(backupDir, 0o777); err != nil {
		return "", err
	}
	backup := filepath.Join(backupDir, backupTimestamp(time.Now())+"-"+filepath.Base(path))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(backup, data, info.Mode().Perm()); err != nil {
		return "", err
	}
	_ = os.Chtimes(backup, info.ModTime(), info.ModTime())
	return backup, nil
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func hasSuffixCompat(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
