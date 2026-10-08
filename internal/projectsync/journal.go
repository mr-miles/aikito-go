package projectsync

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// Journal is skill_state.py's SkillTransactionJournal. The entry lists keep
// Python's dict shape so journals stay interchangeable.
type Journal struct {
	TxID             string
	Kind             string
	Phase            string
	WorkspaceRoot    string
	ProjectNames     []string
	CheckoutPaths    []string
	AffectedSkills   []string
	Files            []map[string]any
	RecoveryDirs     []map[string]any
	StateTransitions []map[string]any
	Symlinks         []map[string]any
}

func mapsToAny(ms []map[string]any) []any {
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

func strsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func (j *Journal) toDict() map[string]any {
	return map[string]any{
		"tx_id":             j.TxID,
		"kind":              j.Kind,
		"phase":             j.Phase,
		"workspace_root":    j.WorkspaceRoot,
		"project_names":     strsToAny(j.ProjectNames),
		"checkout_paths":    strsToAny(j.CheckoutPaths),
		"affected_skills":   strsToAny(j.AffectedSkills),
		"files":             mapsToAny(j.Files),
		"recovery_dirs":     mapsToAny(j.RecoveryDirs),
		"state_transitions": mapsToAny(j.StateTransitions),
		"symlinks":          mapsToAny(j.Symlinks),
	}
}

func anyList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

func dictList(v any) []map[string]any {
	var out []map[string]any
	for _, x := range anyList(v) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		} else {
			out = append(out, map[string]any{})
		}
	}
	return out
}

func strList(v any) []string {
	var out []string
	for _, x := range anyList(v) {
		out = append(out, pyStr(x))
	}
	return out
}

func journalFromDict(v any) (*Journal, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'%s' object is not subscriptable", pyTypeName(v))
	}
	get := func(k, def string) string {
		if x, ok := m[k]; ok {
			return pyStr(x)
		}
		return def
	}
	txID, ok := m["tx_id"]
	if !ok {
		return nil, keyError("tx_id")
	}
	ws, ok := m["workspace_root"]
	if !ok {
		return nil, keyError("workspace_root")
	}
	return &Journal{
		TxID: pyStr(txID), Kind: get("kind", "runtime_apply"), Phase: get("phase", "pending"),
		WorkspaceRoot: pyStr(ws), ProjectNames: strList(m["project_names"]),
		CheckoutPaths: strList(m["checkout_paths"]), AffectedSkills: strList(m["affected_skills"]),
		Files: dictList(m["files"]), RecoveryDirs: dictList(m["recovery_dirs"]),
		StateTransitions: dictList(m["state_transitions"]), Symlinks: dictList(m["symlinks"]),
	}, nil
}

func transactionsDir(home string) string { return filepath.Join(SkillStateDir(home), "transactions") }

// writeJournal is write_transaction_journal.
func writeJournal(home string, j *Journal) string {
	txDir := filepath.Join(transactionsDir(home), j.TxID)
	fail := func(err error) string {
		return fmt.Sprintf("Failed to persist transaction journal %s: %s", j.TxID, pyOSError(err))
	}
	if err := os.MkdirAll(txDir, 0o777); err != nil {
		return fail(err)
	}
	secureDirectoryPermissions(txDir)
	tmp := filepath.Join(txDir, ".journal.tmp")
	if err := os.WriteFile(tmp, []byte(pyDumps(j.toDict(), 2)), 0o666); err != nil {
		return fail(err)
	}
	secureFilePermissions(tmp)
	if err := os.Rename(tmp, filepath.Join(txDir, "journal.json")); err != nil {
		return fail(err)
	}
	return ""
}

var txIDRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

func isSafeTxID(id string) bool { return txIDRe.MatchString(id) }

// deleteJournal is delete_transaction_journal.
func deleteJournal(home, txID string) {
	if !isSafeTxID(txID) {
		return
	}
	root := transactionsDir(home)
	txDir := filepath.Join(root, txID)
	if !isRelativeTo(physical(txDir), physical(root)) {
		return
	}
	if isDir(txDir) {
		os.RemoveAll(txDir)
	}
}

func strictlyUnder(p, root string) bool { return p != root && isRelativeTo(p, root) }

func isValidFilePath(p, wsRoot string, checkouts []string) bool {
	pr := physical(p)
	if strictlyUnder(pr, physical(wsRoot)) {
		return true
	}
	for _, co := range checkouts {
		if strictlyUnder(pr, physical(co)) {
			return true
		}
	}
	return false
}

func isValidStagingOrRecoveryDir(p, txID, wsRoot string, checkouts []string) bool {
	if !isSafeTxID(txID) {
		return false
	}
	name := filepath.Base(p)
	if name == "" || name == "." || name == ".." {
		return false
	}
	parent := physical(filepath.Dir(p))
	if isRelativeTo(parent, physical(filepath.Join(wsRoot, ".aikito-tx", txID))) {
		return true
	}
	for _, co := range checkouts {
		if isRelativeTo(parent, physical(filepath.Join(co, ".agents", ".aikito-tx", txID))) {
			return true
		}
	}
	return false
}

func isValidTargetPath(p, wsRoot string, checkouts []string) bool {
	name := filepath.Base(p)
	if name == "" || name == "." || name == ".." {
		return false
	}
	parent := physical(filepath.Dir(p))
	if parent == physical(filepath.Join(wsRoot, "skills")) {
		return true
	}
	for _, co := range checkouts {
		if parent == physical(filepath.Join(co, ".agents", "skills")) {
			return true
		}
	}
	return false
}

func normalizeBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

func b64(v any) []byte {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return out
}

func optStr(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func removeEntry(p string) error {
	if isSymlink(p) || isFile(p) {
		return os.Remove(p)
	}
	if isDir(p) {
		return os.RemoveAll(p)
	}
	return nil
}

func loadTOMLMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return workspace.DecodeTOML(data)
}

var hashRe = regexp.MustCompile(`^[0-9a-f]{1,128}$`)

// RunRecoveryPass is run_recovery_pass. It returns (recovered, message);
// (false, msg) with a non-empty msg is a hard error.
func RunRecoveryPass(home, workspaceRoot string, projects, skills, authorizedCheckouts []string) (bool, string) {
	txRoot := transactionsDir(home)
	if !isDir(txRoot) {
		return false, ""
	}
	normWS := normalizeIdentityPath(workspaceRoot)
	projSet := map[string]bool{}
	for _, p := range projects {
		projSet[p] = true
	}
	skillSet := map[string]bool{}
	for _, s := range skills {
		skillSet[s] = true
	}
	recovered := false
	var details []string

	items, _ := os.ReadDir(txRoot)
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		item := filepath.Join(txRoot, name)
		if !isDir(item) {
			continue
		}
		if !isSafeTxID(name) {
			return false, fmt.Sprintf("Unsafe transaction directory name %s: must be a safe identifier. Manual recovery required.", name)
		}
		journalPath := filepath.Join(item, "journal.json")
		if !exists(journalPath) {
			os.RemoveAll(item)
			recovered = true
			details = append(details, "Cleaned incomplete transaction directory: "+name)
			continue
		}
		if isReparsePoint(journalPath) {
			return false, fmt.Sprintf("Transaction journal is an unsafe symlink/reparse point: %s", journalPath)
		}
		var j *Journal
		data, err := os.ReadFile(journalPath)
		if err == nil {
			var raw any
			if raw, err = decodeJSON(data); err == nil {
				j, err = journalFromDict(raw)
			}
		}
		if err != nil {
			return false, fmt.Sprintf("Corrupted transaction journal %s: %s. Manual recovery required.", journalPath, err)
		}
		if j.TxID != name {
			return false, fmt.Sprintf("Transaction journal ID mismatch (%s != %s) in %s. Manual recovery required.", j.TxID, name, journalPath)
		}
		if normalizeIdentityPath(j.WorkspaceRoot) != normWS {
			continue
		}
		relevant := len(projSet) == 0 && len(skillSet) == 0
		for _, p := range j.ProjectNames {
			if projSet[p] {
				relevant = true
			}
		}
		for _, s := range j.AffectedSkills {
			if skillSet[s] {
				relevant = true
			}
		}
		if !relevant {
			continue
		}

		wsResolved := physical(workspaceRoot)
		derived := map[string]bool{}
		for _, c := range authorizedCheckouts {
			derived[physical(c)] = true
		}
		projectsDir := filepath.Join(workspaceRoot, "projects")
		if entries, err := os.ReadDir(projectsDir); err == nil {
			for _, e := range entries {
				cfgFile := filepath.Join(projectsDir, e.Name(), "agent.toml")
				if !isDir(filepath.Join(projectsDir, e.Name())) || !isFile(cfgFile) {
					continue
				}
				if cfg, err := loadTOMLMap(cfgFile); err == nil {
					for _, entry := range project.ResolveProjectBinding(cfg, home).Entries {
						derived[physical(entry.ResolvedPath)] = true
					}
				}
			}
		}
		if j.Kind == "runtime_apply" {
			for _, fe := range j.Files {
				filePath := pyStr(fe["path"])
				projectName := filepath.Base(filepath.Dir(filePath))
				expected := filepath.Join(projectsDir, projectName, "agent.toml")
				if !contains(j.ProjectNames, projectName) || physical(filePath) != physical(expected) {
					continue
				}
				pre, post := b64(fe["pre_image_base64"]), b64(fe["post_image_base64"])
				if fe["pre_image_base64"] == nil || fe["post_image_base64"] == nil || !isFile(filePath) {
					continue
				}
				curr, err := os.ReadFile(filePath)
				if err != nil {
					continue
				}
				cn := normalizeBytes(curr)
				if !bytes.Equal(cn, normalizeBytes(pre)) && !bytes.Equal(cn, normalizeBytes(post)) {
					continue
				}
				postCfg, err := workspace.DecodeTOML(post)
				if err != nil {
					continue
				}
				for _, entry := range project.ResolveProjectBinding(postCfg, home).Entries {
					co := physical(entry.ResolvedPath)
					for _, tr := range j.StateTransitions {
						if pyStr(tr["project_name"]) == projectName && physical(pyStr(tr["physical_checkout"])) == co &&
							pyStr(tr["binding_hash"]) == BindingHash(workspaceRoot, projectName, co) {
							derived[co] = true
							break
						}
					}
				}
			}
		}

		var valid []string
		for _, cp := range j.CheckoutPaths {
			res := physical(cp)
			if !derived[res] {
				return false, fmt.Sprintf("Journal declared unauthorized checkout path not configured in workspace: %s", res)
			}
			valid = append(valid, res)
		}

		if j.Phase == "committed" {
			var cleanupErrors []string
			for _, rec := range j.RecoveryDirs {
				if recDir, ok := optStr(rec["recovery_dir"]); ok {
					if !isValidStagingOrRecoveryDir(recDir, j.TxID, wsResolved, valid) {
						return false, fmt.Sprintf("Journal recovery_dir escapes sandbox: %s", recDir)
					}
					if lexists(recDir) {
						if err := removeEntry(recDir); err != nil {
							cleanupErrors = append(cleanupErrors, fmt.Sprintf("recovery_dir %s: %s", recDir, pyOSError(err)))
						}
					}
				}
				if stageDir, ok := optStr(rec["staging_dir"]); ok {
					if !isValidStagingOrRecoveryDir(stageDir, j.TxID, wsResolved, valid) {
						return false, fmt.Sprintf("Journal staging_dir escapes sandbox: %s", stageDir)
					}
					if lexists(stageDir) {
						if err := removeEntry(stageDir); err != nil {
							cleanupErrors = append(cleanupErrors, fmt.Sprintf("staging_dir %s: %s", stageDir, pyOSError(err)))
						}
					}
				}
			}
			cleanupErrors = append(cleanupErrors, removeTxDirs(j.TxID, valid, wsResolved)...)
			if len(cleanupErrors) > 0 {
				return false, fmt.Sprintf("Failed to clean up committed transaction %s: %s. Journal retained for retry.", j.TxID, strings.Join(cleanupErrors, "; "))
			}
			deleteJournal(home, j.TxID)
			recovered = true
			details = append(details, "Finalized committed transaction "+j.TxID)
			continue
		}

		// Pending phase: roll back.
		stateRoot := physical(SkillStateDir(home))
		for i := len(j.Files) - 1; i >= 0; i-- {
			fe := j.Files[i]
			fPath := pyStr(fe["path"])
			if !isValidFilePath(fPath, wsResolved, valid) {
				return false, fmt.Sprintf("Journal file path escapes sandbox: %s", fPath)
			}
			var pre, post []byte
			if fe["pre_image_base64"] != nil {
				pre = b64(fe["pre_image_base64"])
				if pre == nil {
					pre = []byte{}
				}
			}
			if fe["post_image_base64"] != nil {
				post = b64(fe["post_image_base64"])
				if post == nil {
					post = []byte{}
				}
			}
			if !exists(fPath) {
				if pre == nil {
					continue
				}
				if post == nil {
					os.MkdirAll(filepath.Dir(fPath), 0o777)
					if err := os.WriteFile(fPath, pre, 0o666); err != nil {
						return false, fmt.Sprintf("Failed to restore file %s during recovery: %s", fPath, pyOSError(err))
					}
					continue
				}
				return false, fmt.Sprintf("Concurrent modification detected in file %s (externally deleted); recovery aborted to preserve pending journal %s", fPath, j.TxID)
			}
			curr, err := os.ReadFile(fPath)
			if err != nil {
				return false, fmt.Sprintf("Failed to restore file %s during recovery: %s", fPath, pyOSError(err))
			}
			cn := normalizeBytes(curr)
			if pre != nil && bytes.Equal(cn, normalizeBytes(pre)) {
				continue
			}
			if post != nil && !bytes.Equal(cn, normalizeBytes(post)) {
				return false, fmt.Sprintf("Concurrent modification detected in file %s; recovery aborted to preserve pending journal %s", fPath, j.TxID)
			}
			if pre == nil {
				err = os.Remove(fPath)
			} else {
				err = os.WriteFile(fPath, pre, 0o666)
			}
			if err != nil {
				return false, fmt.Sprintf("Failed to restore file %s during recovery: %s", fPath, pyOSError(err))
			}
		}

		for i := len(j.Symlinks) - 1; i >= 0; i-- {
			se := j.Symlinks[i]
			targetPath, ok := optStr(se["target_path"])
			if !ok {
				continue
			}
			if !isValidTargetPath(targetPath, wsResolved, valid) {
				return false, fmt.Sprintf("Journal symlink target_path escapes sandbox: %s", targetPath)
			}
			preLink, hasPre := se["pre_link"].(string)
			postLink, hasPost := se["post_link"].(string)
			currIsLink := isSymlink(targetPath)
			currTarget, hasCurr := "", false
			if currIsLink {
				if t, err := os.Readlink(targetPath); err == nil {
					currTarget, hasCurr = t, true
				}
			}
			if hasCurr == hasPre && currTarget == preLink && (hasPre || !exists(targetPath)) {
				continue
			}
			if hasPost {
				if currIsLink && (currTarget == postLink || physical(currTarget) == physical(postLink)) {
					os.Remove(targetPath)
					if hasPre {
						os.Symlink(preLink, targetPath)
					}
					continue
				}
				return false, fmt.Sprintf("Concurrent modification detected in symlink %s; recovery aborted to preserve pending journal %s", targetPath, j.TxID)
			}
			if hasPre {
				if currIsLink || exists(targetPath) {
					return false, fmt.Sprintf("Concurrent modification detected at %s; recovery aborted to preserve pending journal %s", targetPath, j.TxID)
				}
				os.Symlink(preLink, targetPath)
			} else if currIsLink {
				os.Remove(targetPath)
			}
		}

		for _, rec := range j.RecoveryDirs {
			targetPath, hasTarget := optStr(rec["target_path"])
			recDir, hasRec := optStr(rec["recovery_dir"])
			stageDir, hasStage := optStr(rec["staging_dir"])
			if hasStage && !isValidStagingOrRecoveryDir(stageDir, j.TxID, wsResolved, valid) {
				return false, fmt.Sprintf("Journal staging_dir escapes sandbox: %s", stageDir)
			}
			if hasRec && !isValidStagingOrRecoveryDir(recDir, j.TxID, wsResolved, valid) {
				return false, fmt.Sprintf("Journal recovery_dir escapes sandbox: %s", recDir)
			}
			if hasTarget && !isValidTargetPath(targetPath, wsResolved, valid) {
				return false, fmt.Sprintf("Journal target_path escapes sandbox: %s", targetPath)
			}
			if hasStage && lexists(stageDir) {
				removeEntry(stageDir)
			}
			preFP, hasPreFP := rec["pre_fingerprint"].(string)
			postFP, hasPostFP := rec["post_fingerprint"].(string)
			restoreFail := func(err error) (bool, string) {
				return false, fmt.Sprintf("Failed to restore target %s during recovery: %s", targetPath, pyOSError(err))
			}
			if hasRec && lexists(recDir) && hasTarget {
				switch {
				case isSymlink(targetPath):
					if hasPostFP {
						return false, fmt.Sprintf("Concurrent modification detected at %s (expected dir, found symlink); recovery aborted to preserve pending journal %s", targetPath, j.TxID)
					}
					expected, _ := optStr(rec["expected_symlink_target"])
					if expected == "" {
						expected = filepath.Join(wsResolved, "skills", filepath.Base(targetPath))
					}
					currLink, _ := os.Readlink(targetPath)
					if currLink != expected && physical(currLink) != physical(expected) {
						return false, fmt.Sprintf("Concurrent modification detected at %s (symlink target mismatch: expected %s, found %s); recovery aborted to preserve pending journal %s", targetPath, expected, currLink, j.TxID)
					}
					os.Remove(targetPath)
					if err := os.Rename(recDir, targetPath); err != nil {
						return restoreFail(err)
					}
				case isFile(targetPath):
					return false, fmt.Sprintf("Concurrent modification detected at %s (expected dir, found file); recovery aborted to preserve pending journal %s", targetPath, j.TxID)
				case isDir(targetPath):
					currFP, _ := CalculateDirectoryFingerprint(targetPath)
					switch {
					case hasPreFP && currFP == preFP:
					case (hasPostFP && currFP == postFP) || (!hasPostFP && !hasPreFP):
						os.RemoveAll(targetPath)
						if err := os.Rename(recDir, targetPath); err != nil {
							return restoreFail(err)
						}
					default:
						return false, fmt.Sprintf("Concurrent modification detected in target directory %s; recovery aborted to preserve pending journal %s", targetPath, j.TxID)
					}
				default:
					if err := os.Rename(recDir, targetPath); err != nil {
						return restoreFail(err)
					}
				}
			} else if pyTruthy(rec["created"]) && hasTarget && lexists(targetPath) {
				if isSymlink(targetPath) || isFile(targetPath) {
					return false, fmt.Sprintf("Concurrent modification detected at %s (unexpected non-directory created); recovery aborted to preserve pending journal %s", targetPath, j.TxID)
				}
				if isDir(targetPath) {
					currFP, _ := CalculateDirectoryFingerprint(targetPath)
					if !hasPostFP || currFP == postFP {
						os.RemoveAll(targetPath)
					} else {
						return false, fmt.Sprintf("Concurrent modification detected in newly created target directory %s; recovery aborted to preserve pending journal %s", targetPath, j.TxID)
					}
				}
			}
		}

		for _, st := range j.StateTransitions {
			bHash := pyStr(st["binding_hash"])
			if st["binding_hash"] == nil || !hashRe.MatchString(bHash) {
				return false, fmt.Sprintf("Journal state_transitions binding_hash is missing or not safe hex: %s", pyRepr(st["binding_hash"]))
			}
			stWS, ok1 := optStr(st["workspace_root"])
			stProj, ok2 := optStr(st["project_name"])
			stCO, ok3 := optStr(st["physical_checkout"])
			if !(ok1 && ok2 && ok3) {
				return false, fmt.Sprintf("Journal state transition missing identity metadata for %s", bHash)
			}
			if normalizeIdentityPath(stWS) != normWS {
				return false, fmt.Sprintf("Journal state transition workspace mismatch for %s", bHash)
			}
			if !contains(j.ProjectNames, stProj) {
				return false, fmt.Sprintf("Journal state transition project mismatch for %s", bHash)
			}
			coResolved := physical(stCO)
			if !contains(valid, coResolved) {
				return false, fmt.Sprintf("Journal state transition checkout not authorized for %s", bHash)
			}
			if BindingHash(stWS, stProj, coResolved) != bHash {
				return false, fmt.Sprintf("Journal state transition binding hash mismatch for %s", bHash)
			}
			stFile := filepath.Join(SkillStateDir(home), bHash+".json")
			if !isRelativeTo(physical(stFile), stateRoot) {
				return false, fmt.Sprintf("Journal state file path validation failed: %s outside state root", stFile)
			}
			postDocs, ok := st["post_docs"].([]any)
			if !ok {
				return false, fmt.Sprintf("Journal state transition lacks exact post-images for %s", bHash)
			}
			preDoc := st["pre_doc"]
			if !exists(stFile) {
				if preDoc == nil {
					continue
				}
				return false, fmt.Sprintf("Concurrent modification detected in state file %s (externally deleted); recovery aborted to preserve pending journal %s", stFile, j.TxID)
			}
			data, err := os.ReadFile(stFile)
			var curr any
			if err == nil {
				curr, err = decodeJSON(data)
			}
			if err != nil {
				return false, fmt.Sprintf("Failed to restore state %s during recovery: %s", bHash, err)
			}
			inPost := false
			for _, pd := range postDocs {
				if jsonEqual(curr, pd) {
					inPost = true
				}
			}
			if preDoc == nil {
				if !inPost {
					return false, fmt.Sprintf("Concurrent modification detected in state file %s; recovery aborted to preserve pending journal %s", stFile, j.TxID)
				}
				os.Remove(stFile)
				continue
			}
			if jsonEqual(curr, preDoc) {
				continue
			}
			if !inPost {
				rev := any(0)
				if cm, ok := curr.(map[string]any); ok {
					if r, ok := cm["revision"]; ok {
						rev = r
					}
				}
				return false, fmt.Sprintf("Concurrent modification detected in state file %s (revision %s is not a recorded transaction post-image after %s); recovery aborted to preserve pending journal %s",
					stFile, pyStr(rev), pyStr(orDefault(st["pre_revision"], 0)), j.TxID)
			}
			if err := os.WriteFile(stFile, []byte(pyDumps(normalizeJSON(preDoc), 2)), 0o666); err != nil {
				return false, fmt.Sprintf("Failed to restore state %s during recovery: %s", bHash, pyOSError(err))
			}
			secureFilePermissions(stFile)
		}

		if errs := removeTxDirs(j.TxID, valid, wsResolved); len(errs) > 0 {
			return false, fmt.Sprintf("Failed to clean up staging directories for pending transaction %s: %s. Journal retained for retry.", j.TxID, strings.Join(errs, "; "))
		}
		deleteJournal(home, j.TxID)
		recovered = true
		details = append(details, "Rolled back interrupted transaction "+j.TxID)
	}
	if recovered {
		return true, strings.Join(details, "; ")
	}
	return false, ""
}

func removeTxDirs(txID string, checkouts []string, wsResolved string) []string {
	var errs []string
	for _, co := range checkouts {
		d := filepath.Join(co, ".agents", ".aikito-tx", txID)
		if isDir(d) {
			if err := os.RemoveAll(d); err != nil {
				errs = append(errs, fmt.Sprintf("checkout tx %s: %s", d, pyOSError(err)))
			}
		}
	}
	d := filepath.Join(wsResolved, ".aikito-tx", txID)
	if isDir(d) {
		if err := os.RemoveAll(d); err != nil {
			errs = append(errs, fmt.Sprintf("workspace tx %s: %s", d, pyOSError(err)))
		}
	}
	return errs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func orDefault(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		return "'" + s + "'"
	}
	return pyStr(v)
}
