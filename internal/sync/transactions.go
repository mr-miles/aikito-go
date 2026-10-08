// Transaction engine: a two-phase journal + per-root staging atomic
// write/recover primitive, ported line-by-line from
// aikito/src/aikito/workspace/transactions.py (the one file in this port
// where a literal translation, not a reinterpretation, is the right call).
//
// Every mutating command is expected to: acquire a per-home writer lock,
// call Recover first, build/re-validate a plan, then call Apply — Apply
// itself refuses to start if a journal is already pending.
package sync

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// WorkspaceCoreError mirrors Python's WorkspaceCoreError: a resource or
// transaction cannot be handled safely.
type WorkspaceCoreError struct{ Message string }

func (e *WorkspaceCoreError) Error() string { return e.Message }

func coreErrorf(format string, args ...any) error {
	return &WorkspaceCoreError{Message: fmt.Sprintf(format, args...)}
}

// Version is a resource's current on-disk fingerprint, typed by kind.
type Version struct {
	Kind        string
	Fingerprint string
}

// Change is one planned mutation. Target indexes into the roots slice
// passed to Apply/Recover (transactions operate across possibly multiple
// workspace roots at once, e.g. a future migrate/import spanning two
// workspaces — kept plural from day one so phase 2 doesn't need to touch
// the journal schema). Before/After are expected fingerprints; nil means
// "must not exist" / "must end up not existing". Source is where to copy
// After's content from; it must be non-empty whenever After is non-nil.
type Change struct {
	Target int
	Path   string
	Kind   string
	Source string
	Before *string
	After  *string
}

// StateUpdate is a private state-file write (a JSON blob under
// .local/state/aikito/..., not a content-addressed workspace resource)
// bundled into the same atomic journal as the resource Changes.
type StateUpdate struct {
	Target int
	Path   string
	Before *string
	After  string
}

// PathPolicy is caller-owned resource and state paths admitted to a
// transaction. Resources is an explicit (kind, path) allow-list for paths
// that don't fit Classifier's generic pattern matching (e.g. a one-off
// inbox path). This struct is the pure-data half of authorization — it is
// persisted verbatim into the transaction journal so recovery can
// self-describe its own trusted policy (see journalPolicy).
type PathPolicy struct {
	Resources          [][2]string
	States             []string
	CreateParents      bool
	InboxPrefix        string
	ExtraInboxPrefixes []string
}

func effectiveClassifier(c Classifier) Classifier {
	if c != nil {
		return c
	}
	return DefaultClassifier
}

// --- Path safety & classification (transactions.py validate_resource_path/entry_type/require_ancestors) ---

type entryKind string

const (
	entryMissing   entryKind = "missing"
	entryFile      entryKind = "file"
	entryDirectory entryKind = "directory"
	entryUnsafe    entryKind = "unsafe"
)

// entryTypeAt classifies a path as missing/file/directory/unsafe. Any
// symlink (or, on Windows, reparse point — not yet ported, see isReparsePoint)
// is always "unsafe": the transaction engine never operates through a
// symlink anywhere in a managed path. This is a security boundary, not just
// a correctness one — never relax it.
func entryTypeAt(path string) entryKind {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return entryMissing
		}
		return entryUnsafe
	}
	if info.Mode()&os.ModeSymlink != 0 || isReparsePoint(path) {
		return entryUnsafe
	}
	if info.Mode().IsRegular() {
		return entryFile
	}
	if info.IsDir() {
		return entryDirectory
	}
	return entryUnsafe
}

// isReparsePoint is a TODO stub for Windows junction/reparse-point
// detection (compat.py's is_reparse_point uses GetFileAttributesW with
// FILE_ATTRIBUTE_REPARSE_POINT on Windows). On POSIX it is always false
// (symlinks are already caught by entryTypeAt's ModeSymlink check above).
// Deferred: full Windows parity is not yet in scope for this port.
func isReparsePoint(path string) bool {
	return false
}

// ValidateResourcePath mirrors validate_resource_path: rejects empty,
// absolute, backslash-containing paths or any component equal to
// ""/"."/".."/" .git"/" .local", then requires either an explicit
// (kind, path) policy.Resources allow-list entry or that classify (falling
// back to DefaultClassifier when nil) agrees the path is of kind `kind`.
func ValidateResourcePath(path, kind string, policy PathPolicy, classify Classifier) (string, error) {
	classify = effectiveClassifier(classify)
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return "", coreErrorf("Unsafe resource path: %s", path)
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == ".git" || part == ".local" {
			return "", coreErrorf("Unsafe resource path: %s", path)
		}
	}
	for _, pair := range policy.Resources {
		if pair[0] == kind && pair[1] == path {
			return path, nil
		}
	}
	classified, ok := classify(path, policy.InboxPrefix)
	if kind == "inbox" && classified != kind {
		for _, prefix := range policy.ExtraInboxPrefixes {
			if c, ok2 := classify(path, prefix); ok2 && c == kind {
				classified, ok = c, true
				break
			}
		}
	}
	if !ok || classified != kind {
		return "", coreErrorf("Unsupported resource path: %s", path)
	}
	return path, nil
}

// RequireAncestors walks every parent component of relative (excluding
// relative itself) and requires each to already be a plain directory, or
// (only for the trailing missing segment(s), only if allowMissing) missing
// — never auto-vivifying through an unknown/unsafe parent.
func RequireAncestors(root, relative string, allowMissing bool) error {
	parts := strings.Split(relative, "/")
	current := root
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		actual := entryTypeAt(current)
		if actual != entryDirectory && !(allowMissing && actual == entryMissing) {
			return coreErrorf("Unsafe resource parent: %s", current)
		}
	}
	return nil
}

// --- Fingerprint dispatch (resources.py fingerprint_resource) ---

var fileDigestKinds = map[string]struct{}{
	"memory": {}, "project-memory": {}, "inbox": {}, "project-instructions": {},
	"agent": {}, "subagent": {}, "mcp": {}, "project-config": {}, "skills-config": {},
	"workspace-config": {}, "global-instructions": {}, "legacy": {}, "layout": {},
}

// fingerprintResource mirrors fingerprint_resource exactly: raw file-byte
// sha256 for every standalone resource kind (including agent/mcp/subagent —
// those get a *semantic* value_fingerprint only in the separate
// inspect_resource_content pre-write-validation path, not here), and the
// skill-tree digest for kind=="skill" when the path is a directory.
func fingerprintResource(path, kind string) (string, error) {
	if _, ok := fileDigestKinds[kind]; ok {
		return workspace.FileDigest(path)
	}
	if kind == "skill" {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return workspace.TreeDigest(path)
		}
	}
	return "", fmt.Errorf("cannot fingerprint resource: %s", path)
}

func fingerprintPrivate(path, kind string) (*string, error) {
	et := entryTypeAt(path)
	if et == entryMissing {
		return nil, nil
	}
	expected := entryFile
	if kind == "skill" {
		expected = entryDirectory
	}
	if et != expected {
		return nil, coreErrorf("Unsafe private resource: %s", path)
	}
	fp, err := fingerprintResource(path, kind)
	if err != nil {
		return nil, coreErrorf("%v", err)
	}
	return &fp, nil
}

// VersionAt reads the live fingerprint of the resource at path under root,
// or nil if it doesn't exist. It is the transaction engine's own
// independent re-fingerprinting used for the TOCTOU checks in Apply/Recover
// — callers should not assume a cached fingerprint is still accurate.
func VersionAt(root, path, kind string, policy PathPolicy, classify Classifier) (*Version, error) {
	relative, err := ValidateResourcePath(path, kind, policy, classify)
	if err != nil {
		return nil, err
	}
	if err := RequireAncestors(root, relative, policy.CreateParents); err != nil {
		return nil, err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	actual := entryTypeAt(target)
	if actual == entryMissing {
		return nil, nil
	}
	expected := entryFile
	if kind == "skill" {
		expected = entryDirectory
	}
	if actual != expected {
		return nil, coreErrorf("Resource type changed: %s", target)
	}
	fp, err := fingerprintResource(target, kind)
	if err != nil {
		return nil, coreErrorf("%v", err)
	}
	return &Version{Kind: kind, Fingerprint: fp}, nil
}

// --- Secure dirs/files, durable single-file writes (transactions.py _secure_dir, atomic_text/atomic_unlink) ---

func chmodSecureDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil // TODO: icacls-based hardening on Windows, not yet ported.
	}
	return os.Chmod(path, 0o700)
}

func chmodSecureFile(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return os.Chmod(path, 0o600)
}

func secureDir(path string) error {
	if entryTypeAt(path) == entryMissing {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		if err := chmodSecureDir(path); err != nil {
			return coreErrorf("Cannot secure directory: %s", path)
		}
		return nil
	}
	if entryTypeAt(path) != entryDirectory {
		return coreErrorf("Unsafe directory: %s", path)
	}
	return nil
}

func fsyncDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic("sync: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// atomicText replaces a private state file after syncing its content and
// directory: write-tmp-fsync-rename-fsyncdir, the classic durable write.
// Directory fsync is skipped on Windows (no equivalent).
func atomicText(path, content string) (err error) {
	dir := filepath.Dir(path)
	if err := secureDir(dir); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+"-"+randomHex(16))
	defer func() {
		_ = os.Remove(tmp)
	}()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := chmodSecureFile(tmp); err != nil {
		return coreErrorf("Cannot secure state file: %s", tmp)
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncDir(dir)
}

func atomicUnlink(path string) error {
	et := entryTypeAt(path)
	if et != entryFile && et != entryMissing {
		return coreErrorf("Unsafe state file for removal")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return fsyncDir(filepath.Dir(path))
}

// --- Resource copy/remove (transactions.py _copy_resource/_remove_resource) ---

// copyResource recursively copies a standalone file or a whole skill
// directory tree, mirroring _copy_resource's shape: never follows a
// symlink (the source must already be a plain file/dir per entryTypeAt),
// and skips ignored names during a skill-tree copy.
//
// Deferred simplification: Python's skill-tree copy special-cases the
// executable-bit metadata sidecar file via skill_metadata.py's
// read_executable_metadata/write_executable_metadata (a semantic
// validate-and-rewrite round trip). That package doesn't exist yet in this
// Go port (owned by the workspace resource-model work), so this copies the
// sidecar file verbatim as plain bytes instead — preserving its content
// across a copy without validating/renormalizing it. Revisit once a Go
// skill-metadata helper exists.
func copyResource(source, dest, kind string) error {
	expected := entryFile
	if kind == "skill" {
		expected = entryDirectory
	}
	if entryTypeAt(source) != expected {
		return coreErrorf("Unsafe source: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
		return err
	}
	if kind != "skill" {
		return copyFilePreservingMode(source, dest)
	}
	if err := os.Mkdir(dest, 0o777); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		childSource := filepath.Join(source, name)
		if name == workspace.SkillExecutableMetadataFilename {
			if err := copyFilePreservingMode(childSource, filepath.Join(dest, name)); err != nil {
				return err
			}
			continue
		}
		if workspace.IsIgnoredName(name) {
			continue
		}
		childKind := "memory"
		if entryTypeAt(childSource) == entryDirectory {
			childKind = "skill"
		}
		if err := copyResource(childSource, filepath.Join(dest, name), childKind); err != nil {
			return err
		}
	}
	return nil
}

func copyFilePreservingMode(source, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, info.Mode().Perm()); err != nil {
		return err
	}
	return os.Chtimes(dest, info.ModTime(), info.ModTime())
}

func removeResource(path, kind string) error {
	if kind == "skill" {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

// --- State dir / tx dir / journal path helpers ---

func stateDir(root string, create bool) (path string, ok bool, err error) {
	current := root
	for _, part := range []string{".local", "state", "aikito", "workspace-transactions"} {
		current = filepath.Join(current, part)
		if entryTypeAt(current) == entryMissing && !create {
			return "", false, nil
		}
		if err := secureDir(current); err != nil {
			return "", false, err
		}
	}
	return current, true, nil
}

func txDir(root, txid string, create bool) (string, error) {
	state, ok, err := stateDir(root, create)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", coreErrorf("Missing transaction state: %s", root)
	}
	parent := filepath.Join(state, "tx")
	if create {
		if err := secureDir(parent); err != nil {
			return "", err
		}
	} else if entryTypeAt(parent) != entryDirectory {
		return "", coreErrorf("Unsafe transaction state: %s", parent)
	}
	tx := filepath.Join(parent, txid)
	if create {
		if err := secureDir(tx); err != nil {
			return "", err
		}
	} else if entryTypeAt(tx) != entryDirectory {
		return "", coreErrorf("Missing transaction: %s", tx)
	}
	return tx, nil
}

func journalPath(root string, create bool) (path string, ok bool, err error) {
	state, found, err := stateDir(root, create)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}
	return filepath.Join(state, "pending.json"), true, nil
}

func statePathFor(root, relative string, policy PathPolicy) (string, error) {
	found := false
	for _, s := range policy.States {
		if s == relative {
			found = true
			break
		}
	}
	if !found {
		return "", coreErrorf("Unsafe state path: %s", relative)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if entryTypeAt(filepath.Dir(path)) != entryDirectory {
		return "", coreErrorf("Missing state directory: %s", filepath.Dir(path))
	}
	return path, nil
}

func cleanup(roots []string, txid string) error {
	for _, root := range roots {
		state, ok, err := stateDir(root, false)
		if err != nil {
			return err
		}
		if ok && entryTypeAt(filepath.Join(state, "tx", txid)) != entryMissing {
			tx, err := txDir(root, txid, false)
			if err != nil {
				return err
			}
			if err := os.RemoveAll(tx); err != nil {
				return err
			}
		}
	}
	for _, root := range roots {
		path, ok, err := journalPath(root, false)
		if err != nil {
			return err
		}
		if ok {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// --- Journal schema & validation ---

type journalChange struct {
	Target         int      `json:"target"`
	Path           string   `json:"path"`
	Destination    string   `json:"destination"`
	CreatedParents []string `json:"created_parents"`
	Kind           string   `json:"kind"`
	Before         *string  `json:"before"`
	After          *string  `json:"after"`
}

type journalState struct {
	Target int     `json:"target"`
	Path   string  `json:"path"`
	Before *string `json:"before"`
	After  string  `json:"after"`
}

type journalPolicyData struct {
	Resources          [][2]string `json:"resources"`
	States             []string    `json:"states"`
	CreateParents      bool        `json:"create_parents"`
	InboxPrefix        string      `json:"inbox_prefix"`
	ExtraInboxPrefixes []string    `json:"extra_inbox_prefixes"`
}

type journalData struct {
	Version int               `json:"version"`
	Roots   []string          `json:"roots"`
	TxID    string            `json:"txid"`
	Phase   string            `json:"phase"`
	Changes []journalChange   `json:"changes"`
	States  []journalState    `json:"states"`
	Policy  journalPolicyData `json:"policy"`
}

// This Go port always writes/expects the current (Python "v2") journal
// schema; Python's legacy v1-journal-trusts-caller compat shim (for
// journals written before policy-recording existed) is intentionally not
// reproduced — there are no pre-existing journals from a from-scratch Go
// install to be compatible with.
const journalSchemaVersion = 2

func policyToJournal(p PathPolicy) journalPolicyData {
	resources := make([][2]string, len(p.Resources))
	copy(resources, p.Resources)
	states := append([]string(nil), p.States...)
	extra := append([]string(nil), p.ExtraInboxPrefixes...)
	return journalPolicyData{
		Resources: resources, States: states,
		CreateParents: p.CreateParents, InboxPrefix: p.InboxPrefix,
		ExtraInboxPrefixes: extra,
	}
}

func statesToJournal(states []StateUpdate) []journalState {
	out := make([]journalState, len(states))
	for i, s := range states {
		out[i] = journalState{Target: s.Target, Path: s.Path, Before: s.Before, After: s.After}
	}
	return out
}

// journalPolicy restores path classification without widening caller-owned
// permissions: Resources/States must match the caller's current policy
// exactly (not trusted from the journal); CreateParents/InboxPrefix/
// ExtraInboxPrefixes are taken from the journal (self-describing trusted
// policy), so recovery reconstructs authorization from journal contents,
// not from whatever policy the recovering process happens to pass live.
func journalPolicy(jd journalData, caller PathPolicy) (PathPolicy, error) {
	saved := jd.Policy
	if !resourcesEqual(saved.Resources, caller.Resources) || !stringsEqual(saved.States, caller.States) {
		return PathPolicy{}, coreErrorf("Invalid journal path policy")
	}
	prefixes := append([]string{saved.InboxPrefix}, saved.ExtraInboxPrefixes...)
	for _, prefix := range prefixes {
		if prefix != "" {
			if _, err := ValidateResourcePath(prefix+"/recovery.md", "inbox", PathPolicy{InboxPrefix: prefix}, nil); err != nil {
				return PathPolicy{}, err
			}
		}
	}
	return PathPolicy{
		Resources:          caller.Resources,
		States:             caller.States,
		CreateParents:      saved.CreateParents,
		InboxPrefix:        saved.InboxPrefix,
		ExtraInboxPrefixes: append([]string(nil), saved.ExtraInboxPrefixes...),
	}, nil
}

func resourcesEqual(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var fixedJournalKinds = map[string]struct{}{
	"memory": {}, "skill": {}, "agent": {}, "subagent": {}, "mcp": {},
	"project-config": {}, "project-instructions": {}, "skills-config": {},
	"workspace-config": {}, "global-instructions": {}, "inbox": {},
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// validateJournal mirrors _validate_journal: structural + path-safety
// validation of a decoded journal, re-deriving and checking its trusted
// policy via journalPolicy, and re-running ValidateResourcePath /
// statePathFor on every change/state path.
func validateJournal(jd journalData, roots []string, policy PathPolicy, classify Classifier) error {
	if jd.Version != journalSchemaVersion {
		return coreErrorf("Invalid workspace journal")
	}
	if len(jd.Roots) != len(roots) {
		return coreErrorf("Invalid workspace journal")
	}
	for i, r := range roots {
		if jd.Roots[i] != r {
			return coreErrorf("Invalid workspace journal")
		}
	}
	resolvedPolicy, err := journalPolicy(jd, policy)
	if err != nil {
		return err
	}
	if !isHex32(jd.TxID) {
		return coreErrorf("Invalid transaction ID")
	}
	if jd.Phase != "pending" && jd.Phase != "committed" {
		return coreErrorf("Invalid workspace journal contents")
	}

	allowedKinds := map[string]struct{}{}
	for k := range fixedJournalKinds {
		allowedKinds[k] = struct{}{}
	}
	for _, pair := range resolvedPolicy.Resources {
		allowedKinds[pair[0]] = struct{}{}
	}

	for _, item := range jd.Changes {
		if item.Target < 0 || item.Target >= len(roots) {
			return coreErrorf("Invalid journal target")
		}
		if _, ok := allowedKinds[item.Kind]; !ok {
			return coreErrorf("Invalid journal resource")
		}
		if _, err := ValidateResourcePath(item.Path, item.Kind, resolvedPolicy, classify); err != nil {
			return err
		}
		wantDest := filepath.Join(roots[item.Target], filepath.FromSlash(item.Path))
		if item.Destination != wantDest {
			return coreErrorf("Invalid journal destination")
		}
		ancestors := pathAncestors(item.Path)
		for _, p := range item.CreatedParents {
			if _, ok := ancestors[p]; !ok {
				return coreErrorf("Invalid journal resource parents")
			}
		}
		if len(item.CreatedParents) > 0 && !resolvedPolicy.CreateParents {
			return coreErrorf("Invalid journal resource parents")
		}
		for _, fp := range []*string{item.Before, item.After} {
			if fp != nil && !isHex64(*fp) {
				return coreErrorf("Invalid journal fingerprint")
			}
		}
		if item.Before == nil && item.After == nil {
			return coreErrorf("Empty journal change")
		}
	}
	for _, item := range jd.States {
		if item.Target < 0 || item.Target >= len(roots) {
			return coreErrorf("Invalid journal state target")
		}
		if _, err := statePathFor(roots[item.Target], item.Path, resolvedPolicy); err != nil {
			return err
		}
	}
	return nil
}

// pathAncestors returns the set of proper ancestor directories of a
// "/"-joined relative path, e.g. "a/b/c.md" -> {"a", "a/b"}.
func pathAncestors(path string) map[string]struct{} {
	parts := strings.Split(path, "/")
	out := map[string]struct{}{}
	for i := 1; i < len(parts); i++ {
		out[strings.Join(parts[:i], "/")] = struct{}{}
	}
	return out
}

func readJournal(roots []string, policy PathPolicy, classify Classifier) (*journalData, error) {
	var found []journalData
	for _, root := range roots {
		path, ok, err := journalPath(root, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		et := entryTypeAt(path)
		if et == entryMissing {
			continue
		}
		if et != entryFile {
			return nil, coreErrorf("Unsafe journal: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, coreErrorf("Cannot read journal: %s", path)
		}
		var jd journalData
		if err := json.Unmarshal(data, &jd); err != nil {
			return nil, coreErrorf("Cannot read journal: %s", path)
		}
		found = append(found, jd)
	}
	if len(found) == 0 {
		return nil, nil
	}
	for _, item := range found {
		if err := validateJournal(item, roots, policy, classify); err != nil {
			return nil, err
		}
	}
	allEqual := true
	for _, item := range found {
		if !reflect.DeepEqual(item, found[0]) {
			allEqual = false
			break
		}
	}
	if !allEqual {
		for _, item := range found {
			a, b := item, found[0]
			a.Phase, b.Phase = "", ""
			if !reflect.DeepEqual(a, b) {
				return nil, coreErrorf("Workspace journals differ")
			}
		}
		found[0].Phase = "pending"
	}
	result := found[0]
	if err := validateJournal(result, roots, policy, classify); err != nil {
		return nil, err
	}
	return &result, nil
}

// HasPending reports an unfinished or not yet cleaned transaction without
// writing anything.
func HasPending(roots []string, policy PathPolicy, classify Classifier) (bool, error) {
	jd, err := readJournal(roots, policy, classify)
	if err != nil {
		return false, err
	}
	return jd != nil, nil
}

// PendingKinds returns resource kinds present in an unfinished transaction
// without writing anything (used by RequireCurrentLayout-style preflight
// checks to detect e.g. a pending "layout" migration).
func PendingKinds(roots []string) (map[string]struct{}, error) {
	// The read-only journal scan lives in internal/workspace so that
	// workspace.RequireCurrentLayout can reject an interrupted migration
	// without importing this package (which imports workspace).
	kinds, err := workspace.PendingTransactionKinds(roots)
	if err != nil {
		var wsErr *workspace.WorkspaceCoreError
		if errors.As(err, &wsErr) {
			return nil, &WorkspaceCoreError{Message: wsErr.Message}
		}
		return nil, err
	}
	return kinds, nil
}

// --- Pointer helpers for Before/After fingerprint fields ---

func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func strPtrIn(x, a, b *string) bool { return strPtrEq(x, a) || strPtrEq(x, b) }

func strp(s string) *string { return &s }

// --- Recover ---

// Recover rolls back an interrupted transaction, keeping externally changed
// data (it refuses to recover a resource whose live fingerprint matches
// neither the journal's before nor after value — a sign something else
// touched it during/after the crash). It is idempotent: a crash during
// recovery itself is safe to recover again, since it only acts on resources
// whose current fingerprint still differs from their target state, and it
// re-marks the journal committed as its own last step (a completed recovery
// *is* a committed transaction, a committed "undo").
func Recover(roots []string, policy PathPolicy, classify Classifier) (bool, error) {
	classify = effectiveClassifier(classify)
	jd, err := readJournal(roots, policy, classify)
	if err != nil {
		return false, err
	}
	if jd == nil {
		return false, nil
	}
	resolvedPolicy, err := journalPolicy(*jd, policy)
	if err != nil {
		return false, err
	}
	txid := jd.TxID
	if jd.Phase == "committed" {
		if err := cleanup(roots, txid); err != nil {
			return false, err
		}
		return true, nil
	}

	type restoreItem struct {
		root, path, kind string
		current, before  *string
		source           string
		fromMoved        bool
	}
	var restores []restoreItem

	for i := len(jd.Changes) - 1; i >= 0; i-- {
		item := jd.Changes[i]
		root := roots[item.Target]
		currentVersion, err := VersionAt(root, item.Path, item.Kind, resolvedPolicy, classify)
		if err != nil {
			return false, err
		}
		var currentFp *string
		if currentVersion != nil {
			currentFp = &currentVersion.Fingerprint
		}

		tx, err := txDir(root, txid, false)
		if err != nil {
			return false, err
		}
		moved := filepath.Join(tx, "moved", filepath.FromSlash(item.Path))
		backup := filepath.Join(tx, "backup", filepath.FromSlash(item.Path))

		movedFp, err := fingerprintPrivate(moved, item.Kind)
		if err != nil {
			return false, err
		}
		betweenMoves := currentFp == nil && item.Before != nil && movedFp != nil && *movedFp == *item.Before

		if !strPtrIn(currentFp, item.Before, item.After) && !betweenMoves {
			return false, coreErrorf("Cannot recover externally changed resource: %s", filepath.Join(root, item.Path))
		}

		fromMoved := movedFp != nil && item.Before != nil && *movedFp == *item.Before
		source := backup
		if fromMoved {
			source = moved
		}

		if item.Before != nil && !strPtrEq(currentFp, item.Before) {
			sourceFp, err := fingerprintPrivate(source, item.Kind)
			if err != nil {
				return false, err
			}
			if !strPtrEq(sourceFp, item.Before) {
				return false, coreErrorf("Missing recovery copy: %s", filepath.Join(root, item.Path))
			}
		}

		restores = append(restores, restoreItem{root, item.Path, item.Kind, currentFp, item.Before, source, fromMoved})
	}

	for _, item := range jd.States {
		path, err := statePathFor(roots[item.Target], item.Path, resolvedPolicy)
		if err != nil {
			return false, err
		}
		et := entryTypeAt(path)
		if et != entryFile && et != entryMissing {
			return false, coreErrorf("Unsafe transaction state: %s", path)
		}
		var current *string
		if et == entryFile {
			data, err := os.ReadFile(path)
			if err != nil {
				return false, err
			}
			current = strp(string(data))
		}
		if !strPtrIn(current, item.Before, strp(item.After)) {
			return false, coreErrorf("Cannot recover externally changed state: %s", path)
		}
	}

	for _, r := range restores {
		if strPtrEq(r.current, r.before) {
			continue
		}
		dest := filepath.Join(r.root, filepath.FromSlash(r.path))
		if r.current != nil {
			if err := removeResource(dest, r.kind); err != nil {
				return false, err
			}
		}
		if r.before != nil {
			if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
				return false, err
			}
			if r.fromMoved {
				if err := os.Rename(r.source, dest); err != nil {
					return false, err
				}
			} else {
				if err := copyResource(r.source, dest, r.kind); err != nil {
					return false, err
				}
			}
		}
	}

	for _, item := range jd.States {
		path, err := statePathFor(roots[item.Target], item.Path, resolvedPolicy)
		if err != nil {
			return false, err
		}
		if item.Before == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return false, err
			}
		} else {
			if err := atomicText(path, *item.Before); err != nil {
				return false, err
			}
		}
	}

	type parentKey struct {
		target   int
		relative string
	}
	seen := map[parentKey]struct{}{}
	var parentsList []parentKey
	for _, item := range jd.Changes {
		for _, p := range item.CreatedParents {
			k := parentKey{item.Target, p}
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				parentsList = append(parentsList, k)
			}
		}
	}
	sort.Slice(parentsList, func(i, j int) bool {
		return len(strings.Split(parentsList[i].relative, "/")) > len(strings.Split(parentsList[j].relative, "/"))
	})
	for _, pk := range parentsList {
		root := roots[pk.target]
		if err := RequireAncestors(root, pk.relative, true); err != nil {
			return false, err
		}
		path := filepath.Join(root, filepath.FromSlash(pk.relative))
		et := entryTypeAt(path)
		if et == entryMissing {
			continue
		}
		if et != entryDirectory {
			return false, coreErrorf("Unsafe recovery parent: %s", path)
		}
		if err := os.Remove(path); err != nil {
			if !isDirNotEmptyErr(err) {
				return false, err
			}
		}
	}

	jd.Phase = "committed"
	data, err := json.Marshal(jd)
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		path, _, err := journalPath(root, true)
		if err != nil {
			return false, err
		}
		if err := atomicText(path, string(data)); err != nil {
			return false, err
		}
	}
	if err := cleanup(roots, txid); err != nil {
		return false, err
	}
	return true, nil
}

func isDirNotEmptyErr(err error) bool {
	var perr *os.PathError
	if errors.As(err, &perr) {
		if errno, ok := perr.Err.(syscall.Errno); ok {
			return errno == syscall.ENOTEMPTY || errno == syscall.EEXIST
		}
	}
	// Windows reports a non-empty-directory removal failure with a
	// different, non-syscall.Errno error; fall back to a string check.
	return runtime.GOOS == "windows" && strings.Contains(strings.ToLower(err.Error()), "not empty")
}

// --- Apply ---

// Apply applies a validated batch of changes (plus optional private state
// updates) with one journal and rollback protocol: stage phase (TOCTOU
// fingerprint checks against both source and target), journal write (the
// journal self-describes its own trusted PathPolicy), apply phase (atomic
// renames, never a copy, for the actual resource swap), an optional verify
// callback run after resource renames but before state writes, then state
// writes, commit, and cleanup. Any failure from the apply phase onward
// triggers Recover and re-returns the original error; a failure during
// staging (before the journal exists) just cleans up the staging dir.
//
// Apply refuses to start if a journal is already pending — callers must
// call Recover first (within the same writer-lock acquisition).
func Apply(roots []string, changes []Change, states []StateUpdate, verify func() error, policy PathPolicy, classify Classifier) error {
	classify = effectiveClassifier(classify)
	existing, err := readJournal(roots, policy, classify)
	if err != nil {
		return err
	}
	if existing != nil {
		return coreErrorf("Pending transaction needs recovery")
	}

	txid := randomHex(16)
	var entries []journalChange

	stagingFail := func(err error) error {
		_ = cleanup(roots, txid)
		return err
	}

	for _, change := range changes {
		if _, err := ValidateResourcePath(change.Path, change.Kind, policy, classify); err != nil {
			return stagingFail(err)
		}
		root := roots[change.Target]
		current, err := VersionAt(root, change.Path, change.Kind, policy, classify)
		if err != nil {
			return stagingFail(err)
		}
		var currentFp *string
		if current != nil {
			currentFp = &current.Fingerprint
		}
		if !strPtrEq(currentFp, change.Before) {
			return stagingFail(coreErrorf("Target changed before staging: %s", filepath.Join(root, change.Path)))
		}

		var createdParents []string
		if policy.CreateParents {
			parts := strings.Split(change.Path, "/")
			cur := ""
			for i := 0; i < len(parts)-1; i++ {
				if cur == "" {
					cur = parts[i]
				} else {
					cur = cur + "/" + parts[i]
				}
				if entryTypeAt(filepath.Join(root, filepath.FromSlash(cur))) == entryMissing {
					createdParents = append(createdParents, cur)
				}
			}
		}

		tx, err := txDir(root, txid, true)
		if err != nil {
			return stagingFail(err)
		}

		if change.Before != nil {
			backup := filepath.Join(tx, "backup", filepath.FromSlash(change.Path))
			if err := copyResource(filepath.Join(root, filepath.FromSlash(change.Path)), backup, change.Kind); err != nil {
				return stagingFail(err)
			}
			fp, err := fingerprintPrivate(backup, change.Kind)
			if err != nil {
				return stagingFail(err)
			}
			if !strPtrEq(fp, change.Before) {
				return stagingFail(coreErrorf("Target changed during staging: %s", filepath.Join(root, change.Path)))
			}
		}
		if change.After != nil {
			if change.Source == "" {
				return stagingFail(coreErrorf("Missing source for resource change"))
			}
			stage := filepath.Join(tx, "stage", filepath.FromSlash(change.Path))
			if err := copyResource(change.Source, stage, change.Kind); err != nil {
				return stagingFail(err)
			}
			fp, err := fingerprintPrivate(stage, change.Kind)
			if err != nil {
				return stagingFail(err)
			}
			if !strPtrEq(fp, change.After) {
				return stagingFail(coreErrorf("Source changed during staging: %s", change.Source))
			}
		}

		entries = append(entries, journalChange{
			Target:         change.Target,
			Path:           change.Path,
			Destination:    filepath.Join(root, filepath.FromSlash(change.Path)),
			CreatedParents: createdParents,
			Kind:           change.Kind,
			Before:         change.Before,
			After:          change.After,
		})
	}

	jd := journalData{
		Version: journalSchemaVersion,
		Roots:   append([]string(nil), roots...),
		TxID:    txid,
		Phase:   "pending",
		Changes: entries,
		States:  statesToJournal(states),
		Policy:  policyToJournal(policy),
	}

	commitFail := func(err error) error {
		if _, rerr := Recover(roots, policy, classify); rerr != nil {
			return rerr
		}
		return err
	}

	data, err := json.Marshal(jd)
	if err != nil {
		return commitFail(err)
	}
	for _, root := range roots {
		path, _, err := journalPath(root, true)
		if err != nil {
			return commitFail(err)
		}
		if err := atomicText(path, string(data)); err != nil {
			return commitFail(err)
		}
	}

	for _, item := range entries {
		root := roots[item.Target]
		current, err := VersionAt(root, item.Path, item.Kind, policy, classify)
		if err != nil {
			return commitFail(err)
		}
		var currentFp *string
		if current != nil {
			currentFp = &current.Fingerprint
		}
		if !strPtrEq(currentFp, item.Before) {
			return commitFail(coreErrorf("Target changed during apply: %s", filepath.Join(root, item.Path)))
		}
		tx, err := txDir(root, txid, false)
		if err != nil {
			return commitFail(err)
		}
		stage := filepath.Join(tx, "stage", filepath.FromSlash(item.Path))
		if item.After != nil {
			fp, err := fingerprintPrivate(stage, item.Kind)
			if err != nil {
				return commitFail(err)
			}
			if !strPtrEq(fp, item.After) {
				return commitFail(coreErrorf("Staged resource changed: %s", stage))
			}
		}
		dest := filepath.Join(root, filepath.FromSlash(item.Path))
		if item.Before != nil {
			moved := filepath.Join(tx, "moved", filepath.FromSlash(item.Path))
			if err := os.MkdirAll(filepath.Dir(moved), 0o777); err != nil {
				return commitFail(err)
			}
			if err := os.Rename(dest, moved); err != nil {
				return commitFail(err)
			}
		}
		if item.After != nil {
			if policy.CreateParents {
				parts := strings.Split(item.Path, "/")
				cur := root
				for i := 0; i < len(parts)-1; i++ {
					cur = filepath.Join(cur, parts[i])
					if err := secureDir(cur); err != nil {
						return commitFail(err)
					}
				}
			}
			if err := os.Rename(stage, dest); err != nil {
				return commitFail(err)
			}
		}
	}

	if verify != nil {
		if err := verify(); err != nil {
			return commitFail(err)
		}
	}

	for _, item := range states {
		root := roots[item.Target]
		path, err := statePathFor(root, item.Path, policy)
		if err != nil {
			return commitFail(err)
		}
		et := entryTypeAt(path)
		if et != entryFile && et != entryMissing {
			return commitFail(coreErrorf("Unsafe transaction state: %s", path))
		}
		var current *string
		if et == entryFile {
			data, err := os.ReadFile(path)
			if err != nil {
				return commitFail(err)
			}
			current = strp(string(data))
		}
		if !strPtrEq(current, item.Before) {
			return commitFail(coreErrorf("State changed during apply: %s", path))
		}
		if err := atomicText(path, item.After); err != nil {
			return commitFail(err)
		}
	}

	jd.Phase = "committed"
	data, err = json.Marshal(jd)
	if err != nil {
		return commitFail(err)
	}
	for _, root := range roots {
		path, _, err := journalPath(root, true)
		if err != nil {
			return commitFail(err)
		}
		if err := atomicText(path, string(data)); err != nil {
			return commitFail(err)
		}
	}

	return cleanup(roots, txid)
}
