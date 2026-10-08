package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/mcp"
	"github.com/mr-miles/aikito-go/internal/projectsync"
	"github.com/mr-miles/aikito-go/internal/writerlock"
)

// local_state.py: inspect host-local project skill state and remove proven
// abandoned temporary bindings.

func localStateWarning(path, code, reason string) Finding {
	return Finding{
		Status: "WARN", Code: "local-state." + code, Resource: "project-skill-state",
		Source: path, Reason: reason, Message: path + ": " + reason,
	}
}

func lpathMissing(p string) bool {
	_, err := os.Lstat(p)
	return os.IsNotExist(err)
}

func temporaryRoots(env func(string) string) []string {
	roots := []string{os.TempDir()}
	for _, v := range []string{"TMPDIR", "RUNNER_TEMP"} {
		if val := env(v); val != "" {
			roots = append(roots, val)
		}
	}
	return append(roots, "/tmp", "/var/folders", "/private/tmp", "/private/var/folders")
}

func isSubpath(p, root string) bool {
	p, root = filepath.Clean(p), filepath.Clean(root)
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

func isTemporaryPath(p string, env func(string) string) bool {
	res := compat.PhysicalPath(p)
	for _, root := range temporaryRoots(env) {
		resRoot := compat.PhysicalPath(root)
		if isSubpath(res, resRoot) || isSubpath(p, root) || isSubpath(res, root) || isSubpath(p, resRoot) {
			return true
		}
	}
	return false
}

var stateFileName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

func inspectStateFile(path string, cleanupAllowed bool, env func(string) string) *Finding {
	bad := func(msg string) *Finding {
		f := localStateWarning(path, "invalid", "cannot verify state: "+msg)
		return &f
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return bad("not a regular state file")
	}
	if !stateFileName.MatchString(filepath.Base(path)) {
		return bad("unrecognized state filename")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return bad(err.Error())
	}
	// json.loads: its error wording, trailing data rejected, and a
	// non-object document is "invalid state metadata", not a parse error.
	if msg := mcp.PythonJSONDecodeError(string(data)); msg != "" {
		return bad(msg)
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return bad(err.Error())
	}
	raw, _ := doc.(map[string]any)
	version, _ := raw["version"].(json.Number)
	records, recOK := raw["records"].(map[string]any)
	if raw == nil || version.String() != "1" || !recOK {
		return bad("invalid state metadata")
	}
	for _, v := range records {
		if _, ok := v.(map[string]any); !ok {
			return bad("invalid state metadata")
		}
	}
	var fields [3]string
	for i, k := range []string{"workspace_root", "project_name", "physical_checkout"} {
		s, ok := raw[k].(string)
		if !ok || s == "" {
			return bad("missing binding identity")
		}
		fields[i] = s
	}
	ws, co := fields[0], fields[2]
	if !filepath.IsAbs(ws) || !filepath.IsAbs(co) {
		return bad("binding paths must be absolute")
	}
	if projectsync.BindingHash(ws, fields[1], co) != strings.TrimSuffix(filepath.Base(path), ".json") {
		return bad("binding identity does not match state filename")
	}
	wsMissing, coMissing := lpathMissing(ws), lpathMissing(co)
	if !wsMissing && !coMissing {
		return nil
	}
	if wsMissing && coMissing && isTemporaryPath(ws, env) && isTemporaryPath(co, env) {
		f := localStateWarning(path, "stale", "temporary workspace and checkout no longer exist")
		if cleanupAllowed {
			f.FixHint = "aikito doctor --fix"
			f.Actions = []FindingAction{{Label: "Clean up", Command: "aikito doctor --fix"}}
		}
		return &f
	}
	f := localStateWarning(path, "unavailable", "workspace or checkout is unavailable; state retained")
	return &f
}

// inspectLocalState ports local_state.py inspect_local_state.
func inspectLocalState(home string, env func(string) string) []Finding {
	stateDir, errText := projectsync.ValidateStateStoreRoot(home)
	if errText != "" || isSymlinkPath(stateDir) {
		if errText == "" {
			errText = "state directory is a symlink or reparse point"
		}
		return []Finding{localStateWarning(stateDir, "invalid", errText)}
	}
	if _, err := os.Stat(stateDir); err != nil {
		return nil
	}
	transactions := filepath.Join(stateDir, "transactions")
	if isSymlinkPath(transactions) {
		return []Finding{localStateWarning(transactions, "invalid", "transaction directory is a symlink or reparse point")}
	}
	var journals []string
	if _, err := os.Stat(transactions); err == nil {
		for _, name := range sortedDirEntries(transactions) {
			t := filepath.Join(transactions, name)
			if isSymlinkPath(t) || !isDirPath(t) {
				return []Finding{localStateWarning(stateDir, "invalid", "cannot inspect local state: unverifiable transaction directory: "+t)}
			}
			if j := filepath.Join(t, "journal.json"); !lpathMissing(j) {
				journals = append(journals, j)
			}
		}
	}
	var findings []Finding
	for _, j := range journals {
		findings = append(findings, localStateWarning(j, "recovery-required", "transaction journal present; local state cleanup deferred"))
	}
	matches, _ := filepath.Glob(filepath.Join(stateDir, "*.json"))
	sort.Strings(matches)
	for _, p := range matches {
		if f := inspectStateFile(p, len(journals) == 0, env); f != nil {
			findings = append(findings, *f)
		}
	}
	return findings
}

// cleanLocalState ports local_state.py clean_local_state: delete only
// verified stale bindings, rechecking under the writer lock.
func cleanLocalState(home string, env func(string) string) ([]string, error) {
	hasAction := false
	for _, f := range inspectLocalState(home, env) {
		if len(f.Actions) > 0 {
			hasAction = true
		}
	}
	if !hasAction {
		return nil, nil
	}
	lock, err := writerlock.Acquire(home)
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	var removed []string
	for _, f := range inspectLocalState(home, env) {
		if f.Code == "local-state.stale" && len(f.Actions) > 0 {
			if err := os.Remove(f.Source); err != nil {
				return removed, err
			}
			removed = append(removed, fmt.Sprintf("Removed stale local skill state: %s", f.Source))
		}
	}
	return removed, nil
}
