package projectsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// SkillStateRecord is skill_state.py's SkillStateRecord.
type SkillStateRecord struct {
	SkillName            string
	Representation       string
	Lifecycle            string
	BaselineFingerprint  string
	BaselineOrigin       string
	LastObservedSelected bool
}

func (r SkillStateRecord) toDict() map[string]any {
	return map[string]any{
		"skill_name":             r.SkillName,
		"representation":         r.Representation,
		"lifecycle":              r.Lifecycle,
		"baseline_fingerprint":   r.BaselineFingerprint,
		"baseline_origin":        r.BaselineOrigin,
		"last_observed_selected": r.LastObservedSelected,
	}
}

// ProjectSkillStateDocument is skill_state.py's ProjectSkillStateDocument.
type ProjectSkillStateDocument struct {
	Version          int
	Revision         int
	WorkspaceRoot    string
	ProjectName      string
	PhysicalCheckout string
	Records          map[string]SkillStateRecord
}

func (d *ProjectSkillStateDocument) clone() *ProjectSkillStateDocument {
	c := *d
	c.Records = make(map[string]SkillStateRecord, len(d.Records))
	for k, v := range d.Records {
		c.Records[k] = v
	}
	return &c
}

func (d *ProjectSkillStateDocument) toDict() map[string]any {
	records := map[string]any{}
	for k, v := range d.Records {
		records[k] = v.toDict()
	}
	return map[string]any{
		"version":           d.Version,
		"revision":          d.Revision,
		"workspace_root":    d.WorkspaceRoot,
		"project_name":      d.ProjectName,
		"physical_checkout": d.PhysicalCheckout,
		"records":           records,
	}
}

// pyStr is str(value) for decoded JSON scalars.
func pyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case nil:
		return "None"
	case json.Number:
		return t.String()
	}
	return fmt.Sprint(v)
}

func pyTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case json.Number:
		f, _ := t.Float64()
		return f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

type keyError string

func (k keyError) Error() string { return "'" + string(k) + "'" }

func recordFromDict(v any) (SkillStateRecord, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return SkillStateRecord{}, fmt.Errorf("'%s' object is not subscriptable", pyTypeName(v))
	}
	need := func(k string) (any, error) {
		x, ok := m[k]
		if !ok {
			return nil, keyError(k)
		}
		return x, nil
	}
	get := func(k string, def any) any {
		if x, ok := m[k]; ok {
			return x
		}
		return def
	}
	name, err := need("skill_name")
	if err != nil {
		return SkillStateRecord{}, err
	}
	lc, err := need("lifecycle")
	if err != nil {
		return SkillStateRecord{}, err
	}
	fp, err := need("baseline_fingerprint")
	if err != nil {
		return SkillStateRecord{}, err
	}
	return SkillStateRecord{
		SkillName:            pyStr(name),
		Representation:       pyStr(get("representation", "copy")),
		Lifecycle:            pyStr(lc),
		BaselineFingerprint:  pyStr(fp),
		BaselineOrigin:       pyStr(get("baseline_origin", "write")),
		LastObservedSelected: pyTruthy(get("last_observed_selected", true)),
	}, nil
}

func pyTypeName(v any) string {
	switch v.(type) {
	case string:
		return "str"
	case bool:
		return "bool"
	case nil:
		return "NoneType"
	case []any:
		return "list"
	case json.Number:
		return "int"
	}
	return "object"
}

func documentFromDict(v any) (*ProjectSkillStateDocument, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(v))
	}
	version := 1
	if x, ok := m["version"]; ok {
		n, ok := x.(json.Number)
		if !ok {
			return nil, fmt.Errorf("int() argument must be a string, a bytes-like object or a real number, not '%s'", pyTypeName(x))
		}
		i, err := n.Int64()
		if err != nil {
			f, _ := n.Float64()
			i = int64(f)
		}
		version = int(i)
	}
	rawRev, ok := m["revision"]
	if !ok {
		if rawRev, ok = m["generation"]; !ok {
			return nil, keyError("generation")
		}
	}
	n, isNum := rawRev.(json.Number)
	rev, err := n.Int64()
	if !isNum || err != nil || rev < 0 {
		return nil, fmt.Errorf("Invalid skill state revision")
	}
	doc := &ProjectSkillStateDocument{Version: version, Revision: int(rev), Records: map[string]SkillStateRecord{}}
	for _, k := range []string{"workspace_root", "project_name", "physical_checkout"} {
		x, ok := m[k]
		if !ok {
			return nil, keyError(k)
		}
		switch k {
		case "workspace_root":
			doc.WorkspaceRoot = pyStr(x)
		case "project_name":
			doc.ProjectName = pyStr(x)
		default:
			doc.PhysicalCheckout = pyStr(x)
		}
	}
	if raw, ok := m["records"]; ok {
		rm, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("'%s' object has no attribute 'items'", pyTypeName(raw))
		}
		for k, rv := range rm {
			rec, err := recordFromDict(rv)
			if err != nil {
				return nil, err
			}
			doc.Records[k] = rec
		}
	}
	return doc, nil
}

// CalculateDirectoryFingerprint is skill_state.py's
// calculate_directory_fingerprint: "v1:" + sha256 over sorted entries of
// "f <rel> <sha256>[ *]" and "d <rel>" (empty directories), skipping the
// executable-metadata sidecar. Returns (fingerprint, error message).
func CalculateDirectoryFingerprint(dirPath string) (string, string) {
	if !exists(dirPath) {
		return "", fmt.Sprintf("Directory does not exist: %s", dirPath)
	}
	if isReparsePoint(dirPath) {
		return "", fmt.Sprintf("Directory root cannot be a symbolic link or reparse point: %s", dirPath)
	}
	if !isDir(dirPath) {
		return "", fmt.Sprintf("Path is not a directory: %s", dirPath)
	}
	var entries []string
	var walk func(root string) string
	walk = func(root string) string {
		items, err := os.ReadDir(root)
		if err != nil {
			// os.walk's default onerror ignores unreadable directories.
			return ""
		}
		var dirs, files []string
		for _, it := range items {
			name := it.Name()
			if name == workspace.SkillExecutableMetadataFilename {
				continue
			}
			full := filepath.Join(root, name)
			if isDir(full) {
				dirs = append(dirs, name)
			} else {
				files = append(files, name)
			}
		}
		if len(dirs) == 0 && len(files) == 0 && root != dirPath {
			rel, _ := filepath.Rel(dirPath, root)
			entries = append(entries, "d "+filepath.ToSlash(rel))
		}
		for _, d := range dirs {
			if isReparsePoint(filepath.Join(root, d)) {
				return fmt.Sprintf("Symbolic links are not supported inside copied skills: %s", filepath.Join(root, d))
			}
		}
		for _, f := range files {
			p := filepath.Join(root, f)
			if isReparsePoint(p) {
				return fmt.Sprintf("Symbolic links are not supported inside copied skills: %s", p)
			}
			st, err := os.Lstat(p)
			if err != nil {
				return fmt.Sprintf("Failed to inspect file %s: %s", p, pyOSError(err))
			}
			if !st.Mode().IsRegular() {
				return fmt.Sprintf("Unsupported filesystem entry: %s", p)
			}
			content, err := os.ReadFile(p)
			if err != nil {
				return fmt.Sprintf("Failed to read file %s: %s", p, pyOSError(err))
			}
			sum := sha256.Sum256(content)
			rel, _ := filepath.Rel(dirPath, p)
			suffix := ""
			if !compat.IsWindows() && st.Mode().Perm()&0o111 != 0 {
				suffix = " *"
			}
			entries = append(entries, fmt.Sprintf("f %s %s%s", filepath.ToSlash(rel), hex.EncodeToString(sum[:]), suffix))
		}
		for _, d := range dirs {
			if msg := walk(filepath.Join(root, d)); msg != "" {
				return msg
			}
		}
		return ""
	}
	if msg := walk(dirPath); msg != "" {
		return "", msg
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return "v1:" + hex.EncodeToString(sum[:]), ""
}

// SkillStateDir is get_skill_state_dir.
func SkillStateDir(home string) string {
	return filepath.Join(home, ".local", "state", "aikito", "project-skills")
}

func normalizeIdentityPath(p string) string {
	posix := filepath.ToSlash(physical(p))
	if compat.IsWindows() && compat.DirectoryFoldsCase(physical(p)) {
		posix = strings.ToLower(posix)
	}
	return posix
}

// BindingHash is get_binding_hash.
func BindingHash(workspaceRoot, projectName, checkout string) string {
	token := fmt.Sprintf("%s:%s:%s", normalizeIdentityPath(workspaceRoot), strings.TrimSpace(projectName), normalizeIdentityPath(checkout))
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func checkDirectoryPermissions(p string) (bool, string) {
	st, err := os.Stat(p)
	if err != nil {
		return true, "missing"
	}
	mode := st.Mode().Perm()
	if !compat.IsWindows() && mode&0o022 != 0 {
		return false, fmt.Sprintf("0o%o", mode)
	}
	return true, fmt.Sprintf("0o%o", mode)
}

func secureDirectoryPermissions(p string) {
	if exists(p) {
		_ = compat.SecureDirectoryPermissions(p)
	}
}

func secureFilePermissions(p string) {
	if exists(p) {
		_ = compat.SecureFilePermissions(p)
	}
}

// validateStateStoreRoot is validate_state_store_root.
func validateStateStoreRoot(home string, createIfMissing bool) (string, string) {
	stateDir := SkillStateDir(home)
	if !exists(stateDir) {
		if !createIfMissing {
			return stateDir, ""
		}
		if err := os.MkdirAll(stateDir, 0o777); err != nil {
			return stateDir, fmt.Sprintf("Failed to create state directory %s: %s", stateDir, pyOSError(err))
		}
		secureDirectoryPermissions(stateDir)
	}
	for curr := stateDir; curr != home && filepath.Dir(curr) != curr; curr = filepath.Dir(curr) {
		if isReparsePoint(curr) {
			return stateDir, fmt.Sprintf("State directory component is a symbolic link or reparse point: %s", curr)
		}
		ok, desc := checkDirectoryPermissions(curr)
		if !ok && createIfMissing {
			secureDirectoryPermissions(curr)
			ok, desc = checkDirectoryPermissions(curr)
		}
		if !ok {
			return stateDir, fmt.Sprintf("Insecure permissions on state directory %s: %s", curr, desc)
		}
	}
	return stateDir, ""
}

// LoadProjectSkillState is load_project_skill_state. A nil document with an
// empty error means no state exists.
func LoadProjectSkillState(home, workspaceRoot, projectName, checkout string) (*ProjectSkillStateDocument, string) {
	stateDir, errMsg := validateStateStoreRoot(home, false)
	if errMsg != "" {
		return nil, errMsg
	}
	if !exists(stateDir) {
		return nil, ""
	}
	stateFile := filepath.Join(stateDir, BindingHash(workspaceRoot, projectName, checkout)+".json")
	if !exists(stateFile) {
		return nil, ""
	}
	if isReparsePoint(stateFile) {
		return nil, fmt.Sprintf("State file cannot be a symbolic link or reparse point: %s", stateFile)
	}
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, fmt.Sprintf("Failed to read state file %s: %s", stateFile, pyOSError(err))
	}
	raw, err := decodeJSON(data)
	if err != nil {
		return nil, fmt.Sprintf("Failed to read state file %s: %s", stateFile, err)
	}
	doc, err := documentFromDict(raw)
	if err != nil {
		return nil, fmt.Sprintf("Malformed state document schema in %s: %s", stateFile, err)
	}
	expectedWS := normalizeIdentityPath(workspaceRoot)
	expectedCO := normalizeIdentityPath(checkout)
	if normalizeIdentityPath(doc.WorkspaceRoot) != expectedWS || doc.ProjectName != projectName || normalizeIdentityPath(doc.PhysicalCheckout) != expectedCO {
		return nil, fmt.Sprintf("State file %s binding identity mismatch: expected (%s, %s, %s), found (%s, %s, %s)",
			stateFile, expectedWS, projectName, expectedCO, doc.WorkspaceRoot, doc.ProjectName, doc.PhysicalCheckout)
	}
	return doc, ""
}

// SaveProjectSkillState is save_project_skill_state. expectedRevision < 0
// means no revision guard.
func SaveProjectSkillState(home string, doc *ProjectSkillStateDocument, expectedRevision int) (bool, string) {
	stateDir, errMsg := validateStateStoreRoot(home, true)
	if errMsg != "" {
		return false, errMsg
	}
	hash := BindingHash(doc.WorkspaceRoot, doc.ProjectName, doc.PhysicalCheckout)
	stateFile := filepath.Join(stateDir, hash+".json")
	if len(doc.Records) == 0 {
		if exists(stateFile) {
			if err := os.Remove(stateFile); err != nil {
				return false, fmt.Sprintf("Failed to remove empty state file %s: %s", stateFile, pyOSError(err))
			}
		}
		return true, ""
	}
	if expectedRevision >= 0 && exists(stateFile) {
		current, loadErr := LoadProjectSkillState(home, doc.WorkspaceRoot, doc.ProjectName, doc.PhysicalCheckout)
		if loadErr != "" {
			return false, "Cannot verify revision: " + loadErr
		}
		if current != nil && current.Revision != expectedRevision {
			return false, fmt.Sprintf("State revision mismatch for %s: expected %d, found %d", stateFile, expectedRevision, current.Revision)
		}
	}
	doc.Revision++
	content := pyDumps(doc.toDict(), 2)
	tmp, err := os.CreateTemp(stateDir, "."+hash+".*.tmp")
	if err != nil {
		return false, fmt.Sprintf("Failed to atomically write state file %s: %s", stateFile, pyOSError(err))
	}
	tmpPath := tmp.Name()
	_, werr := tmp.WriteString(content)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		secureFilePermissions(tmpPath)
		werr = os.Rename(tmpPath, stateFile)
	}
	if werr != nil {
		os.Remove(tmpPath)
		return false, fmt.Sprintf("Failed to atomically write state file %s: %s", stateFile, pyOSError(werr))
	}
	return true, ""
}

// writerLock is skill_state.py's WorkspaceWriterLock: an exclusive,
// process-reentrant lock on <state dir>/writer.lock.
var writerLock struct {
	mu    sync.Mutex
	depth int
	path  string
	file  *os.File
}

// AcquireWriterLock takes the workspace writer lock and returns its
// release function.
func AcquireWriterLock(home string) (func(), error) {
	resolvedHome, err := compat.ResolvePath(home)
	if err != nil {
		resolvedHome = home
	}
	lockPath := filepath.Join(SkillStateDir(resolvedHome), "writer.lock")
	writerLock.mu.Lock()
	if writerLock.depth > 0 {
		if writerLock.path != lockPath {
			writerLock.mu.Unlock()
			return nil, fmt.Errorf("Cannot nest workspace writer locks for different state stores")
		}
		writerLock.depth++
		writerLock.mu.Unlock()
		return releaseWriterLock, nil
	}
	defer writerLock.mu.Unlock()
	stateDir, errMsg := validateStateStoreRoot(resolvedHome, true)
	if errMsg != "" {
		return nil, fmt.Errorf("Failed to validate state store root %s: %s", stateDir, errMsg)
	}
	if isReparsePoint(lockPath) {
		return nil, fmt.Errorf("Writer lock file is a reparse point or symlink: %s", lockPath)
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return nil, err
	}
	secureFilePermissions(lockPath)
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	writerLock.file = f
	writerLock.path = lockPath
	writerLock.depth = 1
	return releaseWriterLock, nil
}

func releaseWriterLock() {
	writerLock.mu.Lock()
	defer writerLock.mu.Unlock()
	if writerLock.depth <= 0 {
		return
	}
	writerLock.depth--
	if writerLock.depth == 0 && writerLock.file != nil {
		_ = unlockFile(writerLock.file)
		writerLock.file.Close()
		writerLock.file = nil
		writerLock.path = ""
	}
}
