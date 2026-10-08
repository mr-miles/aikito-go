package projectsync

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// FileUpdate is one (path, pre_content, post_content) of a selection
// transaction.
type FileUpdate struct{ Path, Pre, Post string }

func normalizeNewlines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

// casCheck verifies a file still matches its pre-image.
func casCheck(u FileUpdate) string {
	if isFile(u.Path) {
		current, err := os.ReadFile(u.Path)
		if err != nil || normalizeNewlines(string(current)) != normalizeNewlines(u.Pre) {
			return fmt.Sprintf("Concurrent modification detected in %s: pre-image mismatch", u.Path)
		}
		return ""
	}
	if u.Pre != "" {
		return fmt.Sprintf("Expected existing file at %s matching pre-image, but file does not exist", u.Path)
	}
	return ""
}

func journalFileEntry(u FileUpdate) map[string]any {
	return map[string]any{
		"path":              u.Path,
		"pre_image_base64":  base64.StdEncoding.EncodeToString([]byte(u.Pre)),
		"post_image_base64": base64.StdEncoding.EncodeToString([]byte(u.Post)),
	}
}

func deactivate(doc *ProjectSkillStateDocument, skills []string) {
	for _, s := range skills {
		if old, ok := doc.Records[s]; ok {
			old.Lifecycle = "inactive"
			old.LastObservedSelected = false
			doc.Records[s] = old
		}
	}
}

// ExecuteSelectionTransaction is skill_runtime.py's
// execute_selection_transaction: rewrite project configs (and optionally
// skills.toml), move a canonical skill aside, and mark the given skills'
// copied-state records inactive, all under one recoverable journal.
// canonicalDir is "" when no canonical directory is removed.
func ExecuteSelectionTransaction(home, workspaceRoot string, projectNames []string, fileUpdates []FileUpdate,
	skillsTOML *FileUpdate, canonicalDir string, deactivateSkills []string, writeFn func(path, content string) error) (bool, string) {
	lock, err := writerlock.Acquire(home)
	if err != nil {
		return false, err.Error()
	}
	defer lock.Release()

	recOK, recMsg := RunRecoveryPass(home, workspaceRoot, projectNames, deactivateSkills, nil)
	if !recOK && recMsg != "" {
		return false, "Pre-transaction recovery failed: " + recMsg
	}
	if recOK {
		return false, fmt.Sprintf("Pending transaction recovered: %s. Please re-run command.", recMsg)
	}

	for _, u := range fileUpdates {
		if msg := casCheck(u); msg != "" {
			return false, msg
		}
	}
	if skillsTOML != nil {
		if msg := casCheck(*skillsTOML); msg != "" {
			return false, msg
		}
	}

	txID := newTxID()
	var journalFiles []map[string]any
	for _, u := range fileUpdates {
		journalFiles = append(journalFiles, journalFileEntry(u))
	}
	if skillsTOML != nil {
		journalFiles = append(journalFiles, journalFileEntry(*skillsTOML))
	}

	var recoveryDirs []map[string]any
	backupDir := ""
	if canonicalDir != "" {
		backupDir = filepath.Join(workspaceRoot, ".aikito-tx", txID, "canonical_backup")
		fp, fpErr := CalculateDirectoryFingerprint(canonicalDir)
		if fpErr != "" {
			return false, "Cannot snapshot canonical skill for rollback: " + fpErr
		}
		recoveryDirs = append(recoveryDirs, map[string]any{
			"target_path": canonicalDir, "recovery_dir": backupDir, "staging_dir": "",
			"pre_fingerprint": fp, "post_fingerprint": nil,
		})
	}

	// Each project checkout's state document, before and after.
	checkouts := map[string]bool{}
	var transitions []map[string]any
	if len(deactivateSkills) > 0 {
		for _, proj := range projectNames {
			cfgPath := filepath.Join(workspaceRoot, "projects", proj, "agent.toml")
			if !isFile(cfgPath) {
				continue
			}
			cfg, err := loadTOMLMap(cfgPath)
			if err != nil {
				return false, fmt.Sprintf("Failed to snapshot state for rollback: %s", tomlErrorText(err))
			}
			for _, entry := range project.ResolveProjectBinding(cfg, home).Entries {
				co := physical(entry.ResolvedPath)
				checkouts[co] = true
				doc, loadErr := LoadProjectSkillState(home, workspaceRoot, proj, co)
				if loadErr != "" {
					return false, "Cannot load state for pre-image snapshot: " + loadErr
				}
				var preDoc any
				preRevision := 0
				postDocs := []any{}
				if doc != nil {
					preDoc, preRevision = doc.toDict(), doc.Revision
					anticipated := doc.clone()
					deactivate(anticipated, deactivateSkills)
					anticipated.Revision++
					postDocs = append(postDocs, anticipated.toDict())
				}
				transitions = append(transitions, map[string]any{
					"binding_hash": BindingHash(workspaceRoot, proj, co), "workspace_root": filepath.ToSlash(workspaceRoot),
					"project_name": proj, "physical_checkout": filepath.ToSlash(co),
					"pre_doc": preDoc, "pre_revision": preRevision, "post_docs": postDocs,
				})
			}
		}
	}
	var checkoutList []string
	for c := range checkouts {
		checkoutList = append(checkoutList, filepath.ToSlash(c))
	}
	sort.Strings(checkoutList)

	journal := &Journal{
		TxID: txID, Kind: "selection_mutation", Phase: "pending", WorkspaceRoot: filepath.ToSlash(workspaceRoot),
		ProjectNames: append([]string{}, projectNames...), CheckoutPaths: checkoutList,
		AffectedSkills: append([]string{}, deactivateSkills...), Files: journalFiles,
		RecoveryDirs: recoveryDirs, StateTransitions: transitions,
	}
	if msg := writeJournal(home, journal); msg != "" {
		return false, "Cannot persist transaction journal: " + msg
	}

	canonicalStaged := false
	apply := func() error {
		if canonicalDir != "" && exists(canonicalDir) {
			if err := os.MkdirAll(filepath.Dir(backupDir), 0o777); err != nil {
				return err
			}
			if err := os.Rename(canonicalDir, backupDir); err != nil {
				return err
			}
			canonicalStaged = true
		}
		for _, u := range fileUpdates {
			if err := writeFn(u.Path, u.Post); err != nil {
				return err
			}
		}
		if skillsTOML != nil {
			if err := writeFn(skillsTOML.Path, skillsTOML.Post); err != nil {
				return err
			}
		}
		if len(deactivateSkills) > 0 {
			for _, proj := range projectNames {
				cfgPath := filepath.Join(workspaceRoot, "projects", proj, "agent.toml")
				if !isFile(cfgPath) {
					continue
				}
				cfg, err := loadTOMLMap(cfgPath)
				if err != nil {
					return err
				}
				for _, entry := range project.ResolveProjectBinding(cfg, home).Entries {
					co := physical(entry.ResolvedPath)
					doc, loadErr := LoadProjectSkillState(home, workspaceRoot, proj, co)
					if loadErr != "" {
						return fmt.Errorf("State load failed during deactivation: %s", loadErr)
					}
					if doc == nil {
						continue
					}
					deactivate(doc, deactivateSkills)
					if ok, saveErr := SaveProjectSkillState(home, doc, -1); !ok {
						return fmt.Errorf("State save failed during deactivation: %s", saveErr)
					}
				}
			}
		}
		journal.Phase = "committed"
		if msg := writeJournal(home, journal); msg != "" {
			return fmt.Errorf("Cannot mark transaction committed: %s", msg)
		}
		return nil
	}
	if err := apply(); err != nil {
		recovered, recErr := RunRecoveryPass(home, workspaceRoot, projectNames, deactivateSkills, nil)
		message := err.Error()
		if !recovered {
			if recErr == "" {
				recErr = "pending journal retained"
			}
			message += "; rollback incomplete: " + recErr
		}
		return false, message
	}

	var cleanupErrors []string
	if canonicalStaged && exists(backupDir) {
		if err := os.RemoveAll(backupDir); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("backup dir %s: %s", backupDir, pyOSError(err)))
		}
	}
	wsTx := filepath.Join(workspaceRoot, ".aikito-tx", txID)
	if isDir(wsTx) {
		if err := os.RemoveAll(wsTx); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Sprintf("tx dir %s: %s", wsTx, pyOSError(err)))
		}
	}
	if len(cleanupErrors) > 0 {
		return false, fmt.Sprintf("Committed selection transaction cleanup failed: %s. Journal retained for recovery.", strings.Join(cleanupErrors, "; "))
	}
	deleteJournal(home, txID)
	return true, ""
}
