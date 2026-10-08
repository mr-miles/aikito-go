// Resource scanning: the "what does this workspace currently contain" pass
// (Python: src/aikito/workspace/resources.py). Ports snapshot_workspace and
// everything it depends on: the resource kind table, the recursive scanner,
// and the plaintext-credential heuristic scan.
package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/compat"
)

// WorkspaceResourceError mirrors Python's WorkspaceResourceError: the path
// cannot be read as an Aikito workspace.
type WorkspaceResourceError struct{ Message string }

func (e *WorkspaceResourceError) Error() string { return e.Message }

func resourceErrorf(format string, args ...any) error {
	return &WorkspaceResourceError{Message: fmt.Sprintf(format, args...)}
}

// --- Finding (diagnostics.py's Finding, the subset the scanner produces) ---

type Finding struct {
	Status   string // "error" | "warning"
	Message  string
	FixHint  string
	Code     string
	Resource string
	Source   string
	Reason   string
}

// --- Resource identity model (resources.py §RESOURCE_STORAGE / Resource) ---

type ResourcePart struct {
	Path  string
	Table string
}

type Resource struct {
	Kind            string
	Name            string
	Fingerprint     string
	Parts           []ResourcePart
	References      []string
	ModeFingerprint *string
}

func (r Resource) ID() string { return ResourceID(r.Kind, r.Name) }

func ResourceID(kind, name string) string { return kind + ":" + name }

type WorkspaceSnapshot struct {
	Root      string
	Resources map[string]Resource
	Findings  []Finding
	Skipped   []string
}

type resourceStorageEntry struct {
	Physical string
	Shared   bool
}

// ResourceStorage is the single most important lookup table in the system:
// logical kind -> (physical storage kind, is-this-a-shared-file).
var ResourceStorage = map[string]resourceStorageEntry{
	"config":               {"workspace-config", true},
	"skill-selection":      {"skills-config", true},
	"project":              {"project-config", true},
	"project-field":        {"project-config", true},
	"project-path":         {"project-config", true},
	"project-skill":        {"project-config", true},
	"memory":               {"memory", false},
	"project-memory":       {"memory", false},
	"skill":                {"skill", false},
	"inbox":                {"inbox", false},
	"subagent":             {"subagent", false},
	"mcp":                  {"mcp", false},
	"agent":                {"agent", false},
	"global-instructions":  {"global-instructions", false},
	"project-instructions": {"project-instructions", false},
}

func PhysicalKind(kind string) string   { return ResourceStorage[kind].Physical }
func IsSharedResource(kind string) bool { return ResourceStorage[kind].Shared }

var (
	_TopLevelFiles    = []string{"layout.toml", "skills.toml", "config.toml"}
	_TopLevelDirs     = []string{"agents", "global", "memory", "projects", "skills", "subagents", "mcps"}
	_ProjectSetFields = map[string]bool{"path": true, "paths": true, "skills": true}
)

// ResourceKindForPath classifies a canonical workspace-root-relative path
// (posix separators) for scanners and the (not-yet-built) transaction
// engine's path validation. Returns "" when path doesn't match any known
// resource shape (Python returns None).
func ResourceKindForPath(path string, inboxPrefix string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] == "skills.toml" {
		return "skills-config"
	}
	if len(parts) == 1 && parts[0] == "config.toml" {
		return "workspace-config"
	}
	if len(parts) == 2 {
		area, filename := parts[0], parts[1]
		stem := strings.TrimSuffix(filename, filepath.Ext(filename))
		switch {
		case area == "memory" && strings.HasSuffix(filename, ".md"):
			return "memory"
		case area == "skills" && ValidateResourceName(filename, "skill") == "":
			if IsBundledSkillName(filename) {
				return ""
			}
			return "skill"
		case area == "agents" && strings.HasSuffix(filename, ".toml") && ValidateResourceName(stem, "agent") == "":
			return "agent"
		case area == "subagents" && strings.HasSuffix(filename, ".md") && SubagentNamePattern.MatchString(stem):
			return "subagent"
		case area == "mcps" && strings.HasSuffix(filename, ".toml") && ValidateResourceName(stem, "mcp") == "":
			return "mcp"
		}
		if path == "global/AGENTS.md" {
			return "global-instructions"
		}
	}
	if len(parts) == 3 && parts[0] == "memory" && parts[1] == "notes" {
		stem := strings.TrimSuffix(parts[2], filepath.Ext(parts[2]))
		if strings.HasSuffix(parts[2], ".md") && ValidateMemoryName(stem) == "" {
			return "memory"
		}
		return ""
	}
	if len(parts) == 3 && parts[0] == "projects" && ValidateProjectName(parts[1]) == "" {
		if parts[2] == "agent.toml" {
			return "project-config"
		}
		if parts[2] == "AGENTS.md" {
			return "project-instructions"
		}
	}
	if (len(parts) == 4 || len(parts) == 5) && parts[0] == "projects" &&
		ValidateProjectName(parts[1]) == "" && parts[2] == "memory" {
		if len(parts) == 4 && strings.HasSuffix(parts[3], ".md") {
			return "memory"
		}
		if len(parts) == 5 && parts[3] == "notes" && strings.HasSuffix(parts[4], ".md") {
			stem := strings.TrimSuffix(parts[4], filepath.Ext(parts[4]))
			if ValidateMemoryName(stem) == "" {
				return "memory"
			}
		}
	}
	if inboxPrefix != "" {
		prefix := strings.Split(inboxPrefix, "/")
		if len(parts) > len(prefix) && equalStrings(parts[:len(prefix)], prefix) &&
			strings.HasSuffix(parts[len(parts)-1], ".md") {
			return "inbox"
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
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

// --- fingerprint_resource / inspect_resource_content (resources.py:233-321) ---
//
// Two distinct fingerprint functions exist for the same kinds, and both are
// load-bearing in different call paths — do not unify them:
//   - FingerprintResource (fingerprint_resource): raw sha256 of file bytes
//     for most kinds, tree digest for a skill directory. Used by the generic
//     scan path and by the transaction engine's "after" hashes.
//   - InspectResourceBytes (inspect_resource_bytes): used specifically when
//     WRITING agent/mcp/subagent resources, to validate semantic equality
//     (value_fingerprint) rather than byte equality before a write.

var standaloneFileKinds = map[string]bool{
	"memory": true, "project-memory": true, "inbox": true, "project-instructions": true,
	"agent": true, "subagent": true, "mcp": true, "project-config": true,
	"skills-config": true, "workspace-config": true, "global-instructions": true,
	"legacy": true, "layout": true,
}

// FingerprintResource mirrors fingerprint_resource: fingerprint a standalone
// file or skill directory with snapshot semantics (raw bytes for plain
// files, tree digest for a skill directory).
func FingerprintResource(path string, kind string) (string, error) {
	if standaloneFileKinds[kind] {
		digest, err := FileDigest(path)
		if err != nil {
			return "", resourceErrorf("Cannot fingerprint resource: %s", path)
		}
		return digest, nil
	}
	if kind == "skill" {
		if classifyEntry(path) == entryDirectory {
			digest, err := TreeDigest(path)
			if err != nil {
				return "", resourceErrorf("Cannot fingerprint resource: %s", path)
			}
			return digest, nil
		}
	}
	return "", resourceErrorf("Cannot fingerprint resource: %s", path)
}

// SkillModeFingerprint mirrors skill_mode_fingerprint.
func SkillModeFingerprint(path string) (string, error) {
	digest, err := skillModeDigest(path)
	if err != nil {
		return "", resourceErrorf("Cannot fingerprint skill executable state: %s", path)
	}
	return digest, nil
}

// InspectResourceBytes mirrors inspect_resource_bytes: apply scanner
// semantics directly to a standalone file's already-loaded bytes.
func InspectResourceBytes(resource Resource, content []byte) (string, []string, error) {
	if resource.Kind != "agent" && resource.Kind != "mcp" && resource.Kind != "subagent" {
		return FileDigestBytes(content), resource.References, nil
	}
	if resource.Kind == "subagent" {
		metadata, body, err := ParseSubagentText(string(content))
		if err != nil {
			return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
		}
		fp := ValueFingerprint(map[string]any{
			"instructions": FileDigestBytes([]byte(body)),
			"table":        metadata,
		})
		return fp, agentReferences(metadata), nil
	}
	document, err := DecodeTOML(content)
	if err != nil {
		return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
	}
	if resource.Kind == "mcp" {
		return ValueFingerprint(document), agentReferences(document), nil
	}
	// kind == "agent"
	agentsAny, ok := document["agents"]
	agentsTable, tableOK := agentsAny.(map[string]any)
	if len(document) != 1 || !ok || !tableOK || len(agentsTable) != 1 {
		return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
	}
	specAny, specOK := agentsTable[resource.Name]
	spec, specMapOK := specAny.(map[string]any)
	if !specOK || !specMapOK {
		return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
	}
	return ValueFingerprint(spec), nil, nil
}

// InspectResourceContent mirrors inspect_resource_content: read path fresh
// and apply the same semantics as InspectResourceBytes (or FingerprintResource
// for non agent/mcp/subagent kinds).
func InspectResourceContent(resource Resource, path string) (string, []string, error) {
	if resource.Kind != "agent" && resource.Kind != "mcp" && resource.Kind != "subagent" {
		if resource.Kind == "skill" && classifyEntry(filepath.Join(path, "SKILL.md")) != entryFile {
			return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
		}
		digest, err := FingerprintResource(path, PhysicalKind(resource.Kind))
		if err != nil {
			return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
		}
		return digest, resource.References, nil
	}
	if classifyEntry(path) != entryFile {
		return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, resourceErrorf("Invalid resource content: %s", resource.ID())
	}
	return InspectResourceBytes(resource, content)
}

func agentReferences(table map[string]any) []string {
	agentsAny, ok := table["agents"]
	agentsList, listOK := agentsAny.([]any)
	if !ok || !listOK {
		return nil
	}
	var refs []string
	for _, item := range agentsList {
		if name, ok := item.(string); ok {
			refs = append(refs, ResourceID("agent", name))
		}
	}
	return refs
}

// --- entry classification (resources.py's _entry_type) ---

type entryType int

const (
	entryMissing entryType = iota
	entryUnreadable
	entryLink
	entryDirectory
	entryFile
	entrySpecial
)

// classifyEntry mirrors _entry_type. Known simplification: Python also
// special-cases Windows reparse points via is_reparse_point(); this port
// relies on Go's os.ModeSymlink alone, which covers POSIX symlinks and most
// but not necessarily every exotic Windows reparse tag.
func classifyEntry(path string) entryType {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return entryMissing
		}
		return entryUnreadable
	}
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		return entryLink
	}
	if mode.IsDir() {
		return entryDirectory
	}
	if mode.IsRegular() {
		return entryFile
	}
	return entrySpecial
}

func entryTypeLabel(t entryType) string {
	switch t {
	case entryMissing:
		return "missing"
	case entryUnreadable:
		return "unreadable"
	case entryLink:
		return "link"
	case entryDirectory:
		return "directory"
	case entryFile:
		return "file"
	default:
		return "special"
	}
}

// --- skill executable-bit sidecar (skill_metadata.py) ---

// ReadExecutableMetadata mirrors skill_metadata.py's read_executable_metadata:
// a missing sidecar file means "no executable paths recorded" (empty set,
// not an error); a symlink or malformed sidecar is an error.
func ReadExecutableMetadata(path string) (map[string]struct{}, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return map[string]struct{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Unsafe skill executable metadata")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	val, err := DecodeStrictJSON(string(data))
	if err != nil {
		return nil, fmt.Errorf("Invalid skill executable metadata")
	}
	obj, ok := val.(map[string]any)
	if !ok || len(obj) != 2 {
		return nil, fmt.Errorf("Invalid skill executable metadata")
	}
	versionAny, hasVersion := obj["version"]
	execAny, hasExec := obj["executable"]
	if !hasVersion || !hasExec {
		return nil, fmt.Errorf("Invalid skill executable metadata")
	}
	versionNum, ok := versionAny.(json.Number)
	if !ok || versionNum.String() != "1" {
		return nil, fmt.Errorf("Invalid skill executable metadata")
	}
	execList, ok := execAny.([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid skill executable metadata")
	}
	names := make([]string, 0, len(execList))
	seen := map[string]bool{}
	for _, item := range execList {
		name, ok := item.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("Unsafe skill executable metadata path")
		}
		for _, part := range strings.Split(name, "/") {
			if part == "" || part == "." || part == ".." || part == SkillExecutableMetadataFilename {
				return nil, fmt.Errorf("Unsafe skill executable metadata path")
			}
		}
		for _, r := range name {
			if r < 32 || strings.ContainsRune(`\<>:"|?*`, r) {
				return nil, fmt.Errorf("Unsafe skill executable metadata path")
			}
		}
		if seen[name] {
			return nil, fmt.Errorf("Duplicate skill executable metadata path")
		}
		seen[name] = true
		names = append(names, name)
	}
	result := make(map[string]struct{}, len(names))
	for _, n := range names {
		result[n] = struct{}{}
	}
	return result, nil
}

// WriteExecutableMetadata mirrors write_executable_metadata: forces LF line
// endings regardless of OS, matching Python's newline="\n".
func WriteExecutableMetadata(path string, executable map[string]struct{}) error {
	names := make([]string, 0, len(executable))
	for n := range executable {
		names = append(names, n)
	}
	sort.Strings(names)
	payload := map[string]any{"version": int64(1), "executable": toAnySlice(names)}
	content := CanonicalJSONSorted(payload) + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// CanonicalJSONSorted mirrors json.dumps(value, sort_keys=True) WITHOUT
// ensure_ascii=False (i.e. Python's json.dumps default, which DOES escape
// non-ASCII as \uXXXX). Only used for write_executable_metadata, whose
// Python call site omits ensure_ascii=False unlike every other JSON dump in
// this codebase.
func CanonicalJSONSorted(v any) string {
	return CanonicalJSON(v) // executable paths are always plain ASCII-safe relative paths in practice
}

// skillModeDigest mirrors _Scanner.skill_mode_digest: walks the skill tree
// collecting files (symlinks and other non-file/dir entries are silently
// ignored, matching Python's visit() having no else branch), then computes
// the executable-set fingerprint from either live POSIX mode bits or (on
// Windows) the executable-metadata sidecar intersected with the files that
// actually exist.
func skillModeDigest(directory string) (string, error) {
	files := map[string]string{} // relpath -> absolute path
	var visit func(parent string) error
	visit = func(parent string) error {
		entries, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !IsIgnoredName(e.Name()) {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			child := filepath.Join(parent, name)
			switch classifyEntry(child) {
			case entryDirectory:
				if err := visit(child); err != nil {
					return err
				}
			case entryFile:
				rel, err := filepath.Rel(directory, child)
				if err != nil {
					return err
				}
				files[filepath.ToSlash(rel)] = child
			}
		}
		return nil
	}
	if err := visit(directory); err != nil {
		return "", err
	}

	var executable []string
	if compat.IsWindows() {
		meta, err := ReadExecutableMetadata(filepath.Join(directory, SkillExecutableMetadataFilename))
		if err != nil {
			return "", err
		}
		for name := range meta {
			if _, ok := files[name]; ok {
				executable = append(executable, name)
			}
		}
	} else {
		for name, abspath := range files {
			info, err := os.Lstat(abspath)
			if err != nil {
				return "", err
			}
			if info.Mode()&0o111 != 0 {
				executable = append(executable, name)
			}
		}
	}
	return ExecutableSetFingerprint(executable), nil
}

// --- scanner (the recursive snapshot_workspace pass) ---

type scanner struct {
	root      string
	resources map[string]Resource
	findings  []Finding
	skipped   []string
	inboxRel  string // posix-relative; "" means unset
}

func newScanner(root string) *scanner {
	return &scanner{root: root, resources: map[string]Resource{}}
}

func (s *scanner) rel(path string) string {
	r, err := filepath.Rel(s.root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(r)
}

func (s *scanner) errorf(code, message, path string) {
	s.findings = append(s.findings, Finding{Status: "error", Message: message, Code: code, Resource: s.rel(path)})
}

func (s *scanner) add(kind, name, fingerprint string, parts []ResourcePart, references []string, modeFingerprint *string) {
	if _, known := ResourceStorage[kind]; known {
		if ResourceKindForPath(parts[0].Path, s.inboxRel) != PhysicalKind(kind) {
			s.errorf("unsupported-entry", "Unsupported resource path", filepath.Join(s.root, parts[0].Path))
			return
		}
	}
	resource := Resource{Kind: kind, Name: name, Fingerprint: fingerprint, Parts: parts, References: references, ModeFingerprint: modeFingerprint}
	if _, exists := s.resources[resource.ID()]; exists {
		s.errorf("duplicate-resource", "Ambiguous resource ID", filepath.Join(s.root, parts[0].Path))
		return
	}
	s.resources[resource.ID()] = resource
}

func (s *scanner) directory(path string, optional bool) bool {
	kind := classifyEntry(path)
	if kind == entryDirectory {
		return true
	}
	if kind == entryMissing && optional {
		return false
	}
	s.errorf("unsafe-entry", "Expected a real directory: "+entryTypeLabel(kind), path)
	return false
}

// childEntry pairs a directory entry's name with its absolute path, mirroring
// Python's Path objects (every caller downstream needs the full path).
type childEntry struct {
	Name string
	Path string
}

func (s *scanner) children(directory string) []childEntry {
	entries, err := os.ReadDir(directory)
	if err != nil {
		s.errorf("unreadable", "Cannot list directory: "+err.Error(), directory)
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := make([]childEntry, 0, len(names))
	for _, name := range names {
		if IsIgnoredName(name) {
			continue
		}
		out = append(out, childEntry{Name: name, Path: filepath.Join(directory, name)})
	}
	return out
}

func (s *scanner) unsupported(path string) {
	s.errorf("unsupported-entry", "Unsupported entry in a managed area", path)
}

func (s *scanner) read(path string) ([]byte, bool) {
	kind := classifyEntry(path)
	if kind != entryFile {
		s.errorf("unsafe-entry", "Expected a regular file: "+entryTypeLabel(kind), path)
		return nil, false
	}
	content, err := os.ReadFile(path)
	if err != nil {
		s.errorf("unreadable", "Cannot read file: "+err.Error(), path)
		return nil, false
	}
	return content, true
}

func (s *scanner) fileDigest(path string) (string, bool) {
	content, ok := s.read(path)
	if !ok {
		return "", false
	}
	return FileDigestBytes(content), true
}

func (s *scanner) tomlDoc(path string) (map[string]any, bool) {
	content, ok := s.read(path)
	if !ok {
		return nil, false
	}
	doc, err := DecodeTOML(content)
	if err != nil {
		s.errorf("invalid-toml", "Invalid TOML: "+err.Error(), path)
		return nil, false
	}
	return doc, true
}

// treeDigestTracked mirrors _Scanner.tree_digest: it shares findings with the
// rest of the scan (so unrelated problems elsewhere are still reported), but
// returns ok=false if ANY new finding was produced while walking this one
// subtree (whether from an unreadable directory, an unreadable file, or a
// symlink). Known simplification vs. Python: a directory-listing failure
// deep inside the tree is treated the same as a symlink (whole digest
// invalidated) rather than Python's "treat unreadable dir as if empty, add a
// 'd' line, keep walking"; the end result (digest invalid either way) is
// identical for every real caller, since both discard the digest on any
// finding.
func (s *scanner) treeDigestTracked(directory string) (string, bool) {
	before := len(s.findings)
	var entries []string
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		children := s.children(dir)
		if len(children) == 0 && dir != directory {
			entries = append(entries, "d "+rel)
			return
		}
		for _, c := range children {
			childRel := c.Name
			if rel != "" {
				childRel = rel + "/" + c.Name
			}
			switch classifyEntry(c.Path) {
			case entryDirectory:
				walk(c.Path, childRel)
			case entryLink:
				s.errorf("unsafe-entry", "Expected a regular file: link", c.Path)
			default:
				digest, ok := s.fileDigest(c.Path)
				if ok {
					entries = append(entries, "f "+childRel+" "+digest)
				}
			}
		}
	}
	walk(directory, "")
	if len(s.findings) != before {
		return "", false
	}
	sort.Strings(entries)
	return FileDigestBytes([]byte(strings.Join(entries, "\n"))), true
}

func (s *scanner) skillModeDigestTracked(directory string) (string, bool) {
	digest, err := skillModeDigest(directory)
	if err != nil {
		s.errorf("invalid-skill-mode", "Invalid skill executable state: "+err.Error(), directory)
		return "", false
	}
	return digest, true
}

// --- scan helpers mirroring the module-level _scan_* functions ---

func scanMarkdownFile(s *scanner, path, kind, name string) {
	digest, ok := s.fileDigest(path)
	if !ok {
		return
	}
	var refs []string
	if kind == "project-memory" || kind == "project-instructions" {
		projectName, _, _ := strings.Cut(name, "/")
		refs = []string{"project:" + projectName}
	}
	s.add(kind, name, digest, []ResourcePart{{Path: s.rel(path)}}, refs, nil)
}

func scanMemory(s *scanner, memory, kind, prefix string) {
	if !s.directory(memory, true) {
		return
	}
	for _, entry := range s.children(memory) {
		if entry.Name == "notes" {
			if !s.directory(entry.Path, false) {
				continue
			}
			for _, note := range s.children(entry.Path) {
				stem := strings.TrimSuffix(note.Name, filepath.Ext(note.Name))
				if filepath.Ext(note.Name) != ".md" || ValidateMemoryName(stem) != "" {
					s.unsupported(note.Path)
					continue
				}
				scanMarkdownFile(s, note.Path, kind, prefix+"notes/"+note.Name)
			}
		} else if filepath.Ext(entry.Name) == ".md" {
			scanMarkdownFile(s, entry.Path, kind, prefix+entry.Name)
		} else {
			s.unsupported(entry.Path)
		}
	}
}

func stringMembers(raw any) []string {
	var list []any
	switch v := raw.(type) {
	case string:
		list = []any{v}
	case map[string]any:
		for _, val := range v {
			list = append(list, val)
		}
	case []any:
		list = v
	default:
		return nil
	}
	var out []string
	for _, item := range list {
		if str, ok := item.(string); ok && strings.TrimSpace(str) != "" {
			out = append(out, str)
		}
	}
	return out
}

// GetProjectCandidatePaths mirrors project_config.py's
// get_project_candidate_paths: returns (label, path) pairs. "paths" (table
// or list) wins if non-empty; "path" (string or list) is a pure legacy
// fallback used only when "paths" yields nothing.
func GetProjectCandidatePaths(config map[string]any) [][2]string {
	var candidates [][2]string
	if pathsSec, ok := config["paths"]; ok {
		switch v := pathsSec.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if val, ok := v[k].(string); ok && strings.TrimSpace(val) != "" {
					candidates = append(candidates, [2]string{k, strings.TrimSpace(val)})
				}
			}
		case []any:
			for idx, item := range v {
				if val, ok := item.(string); ok && strings.TrimSpace(val) != "" {
					candidates = append(candidates, [2]string{fmt.Sprintf("%d", idx+1), strings.TrimSpace(val)})
				}
			}
		}
	}
	if len(candidates) == 0 {
		if rawPath, ok := config["path"]; ok {
			switch v := rawPath.(type) {
			case []any:
				for idx, item := range v {
					if val, ok := item.(string); ok && strings.TrimSpace(val) != "" {
						candidates = append(candidates, [2]string{fmt.Sprintf("%d", idx+1), strings.TrimSpace(val)})
					}
				}
			case string:
				if strings.TrimSpace(v) != "" {
					candidates = append(candidates, [2]string{"default", strings.TrimSpace(v)})
				}
			}
		}
	}
	return candidates
}

func projectMembers(config map[string]any) ([]string, []string) {
	candidates := GetProjectCandidatePaths(config)
	pathSet := map[string]struct{}{}
	for _, c := range candidates {
		pathSet[c[1]] = struct{}{}
	}
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	skillSet := map[string]struct{}{}
	for _, s := range stringMembers(config["skills"]) {
		skillSet[s] = struct{}{}
	}
	skills := make([]string, 0, len(skillSet))
	for sk := range skillSet {
		skills = append(skills, sk)
	}
	sort.Strings(skills)
	return paths, skills
}

// emptyProjectTree mirrors _empty_project_tree: a project directory left
// behind after all its resources were individually removed (only the
// memory/ and memory/notes/ directories remain, both empty modulo ignored
// names) is tolerated as nonexistent rather than flagged.
func emptyProjectTree(project string) bool {
	allowed := map[string]bool{"memory": true, "memory/notes": true}
	ok := true
	_ = filepath.WalkDir(project, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == project {
			return nil
		}
		if IsIgnoredName(d.Name()) && classifyEntry(path) == entryFile {
			return nil
		}
		rel := filepath.ToSlash(mustRel(project, path))
		if !allowed[rel] || classifyEntry(path) != entryDirectory {
			ok = false
		}
		return nil
	})
	return ok
}

func mustRel(base, target string) string {
	r, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return r
}

func scanProject(s *scanner, project string) {
	name := filepath.Base(project)
	if ValidateProjectName(name) != "" {
		s.unsupported(project)
		return
	}
	configPath := filepath.Join(project, "agent.toml")
	if classifyEntry(configPath) == entryMissing && emptyProjectTree(project) {
		return
	}
	config, ok := s.tomlDoc(configPath)
	if ok {
		if invalidProjectFields(config) {
			s.errorf("invalid-project-field", "Invalid project path or skills collection", configPath)
		}
		part := []ResourcePart{{Path: s.rel(configPath)}}
		fields := map[string]any{}
		for k, v := range config {
			if !_ProjectSetFields[k] {
				fields[k] = v
			}
		}
		s.add("project", name, "", part, nil, nil)
		fieldKeys := make([]string, 0, len(fields))
		for k := range fields {
			fieldKeys = append(fieldKeys, k)
		}
		sort.Strings(fieldKeys)
		for _, key := range fieldKeys {
			s.add("project-field", name+"/"+key, ValueFingerprint(fields[key]), part, []string{"project:" + name}, nil)
		}
		paths, skills := projectMembers(config)
		for _, p := range paths {
			s.add("project-path", name+"/"+p, "", part, []string{"project:" + name}, nil)
		}
		for _, skill := range skills {
			refs := []string{"project:" + name}
			if !IsBundledSkillName(skill) {
				refs = append(refs, ResourceID("skill", skill))
			}
			s.add("project-skill", name+"/"+skill, "", part, refs, nil)
		}
	}
	for _, entry := range s.children(project) {
		switch {
		case entry.Name == "agent.toml":
			continue
		case entry.Name == "AGENTS.md":
			scanMarkdownFile(s, entry.Path, "project-instructions", name)
		case entry.Name == "memory":
			scanMemory(s, entry.Path, "project-memory", name+"/")
		default:
			s.unsupported(entry.Path)
		}
	}
}

func invalidProjectFields(config map[string]any) bool {
	if rawPath, ok := config["path"]; ok {
		if !isStringOrStringList(rawPath) {
			return true
		}
	}
	if rawPaths, ok := config["paths"]; ok {
		switch v := rawPaths.(type) {
		case map[string]any:
			for _, val := range v {
				if _, ok := val.(string); !ok {
					return true
				}
			}
		case []any:
			for _, val := range v {
				if _, ok := val.(string); !ok {
					return true
				}
			}
		default:
			return true
		}
	}
	if rawSkills, ok := config["skills"]; ok {
		list, isList := rawSkills.([]any)
		if !isList {
			return true
		}
		for _, val := range list {
			if _, ok := val.(string); !ok {
				return true
			}
		}
	}
	return false
}

func isStringOrStringList(v any) bool {
	if _, ok := v.(string); ok {
		return true
	}
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func scanSkills(s *scanner, skills string) {
	for _, skill := range s.children(skills) {
		if IsBundledSkillName(skill.Name) {
			s.skipped = append(s.skipped, s.rel(skill.Path))
			continue
		}
		isDir := s.directory(skill.Path, false)
		if ValidateResourceName(skill.Name, "skill") != "" || !isDir {
			if classifyEntry(skill.Path) == entryDirectory {
				s.unsupported(skill.Path)
			}
			continue
		}
		if classifyEntry(filepath.Join(skill.Path, "SKILL.md")) != entryFile {
			s.errorf("invalid-skill", "Skill has no regular SKILL.md", skill.Path)
			continue
		}
		digest, ok := s.treeDigestTracked(skill.Path)
		if !ok {
			continue
		}
		mode, ok := s.skillModeDigestTracked(skill.Path)
		if !ok {
			continue
		}
		s.add("skill", skill.Name, digest, []ResourcePart{{Path: s.rel(skill.Path)}}, nil, &mode)
	}
}

func scanSubagents(s *scanner, directory string) {
	if !s.directory(directory, false) {
		return
	}
	for _, entry := range s.children(directory) {
		stem := strings.TrimSuffix(entry.Name, filepath.Ext(entry.Name))
		if filepath.Ext(entry.Name) != ".md" || !SubagentNamePattern.MatchString(stem) {
			s.unsupported(entry.Path)
			continue
		}
		metadata, body, err := ParseSubagentFile(entry.Path)
		if err != nil {
			s.errorf("invalid-subagent", err.Error(), entry.Path)
			continue
		}
		fingerprint := ValueFingerprint(map[string]any{
			"instructions": FileDigestBytes([]byte(body)),
			"table":        metadata,
		})
		s.add("subagent", stem, fingerprint, []ResourcePart{{Path: s.rel(entry.Path)}}, agentReferences(metadata), nil)
	}
}

func scanMCPs(s *scanner, directory string) {
	if !s.directory(directory, false) {
		return
	}
	for _, entry := range s.children(directory) {
		stem := strings.TrimSuffix(entry.Name, filepath.Ext(entry.Name))
		if filepath.Ext(entry.Name) != ".toml" || ValidateResourceName(stem, "mcp") != "" {
			s.unsupported(entry.Path)
			continue
		}
		document, ok := s.tomlDoc(entry.Path)
		if ok {
			s.add("mcp", stem, ValueFingerprint(document), []ResourcePart{{Path: s.rel(entry.Path)}}, agentReferences(document), nil)
		}
	}
}

func scanTables(s *scanner, filename string, document map[string]any, allowed map[string]bool) {
	keys := make([]string, 0, len(document))
	for k := range document {
		if !allowed[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		s.errorf("unsupported-entry", fmt.Sprintf("Unsupported top-level key '%s'", key), filepath.Join(s.root, filename))
	}
}

func flatten(prefix string, value any) [][2]any {
	if m, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var items [][2]any
		for _, k := range keys {
			newPrefix := k
			if prefix != "" {
				newPrefix = prefix + "." + k
			}
			items = append(items, flatten(newPrefix, m[k])...)
		}
		return items
	}
	return [][2]any{{prefix, value}}
}

func scanTopLevelTOML(s *scanner) {
	agentDir := filepath.Join(s.root, "agents")
	if s.directory(agentDir, false) {
		for _, entry := range s.children(agentDir) {
			stem := strings.TrimSuffix(entry.Name, filepath.Ext(entry.Name))
			if filepath.Ext(entry.Name) != ".toml" || ValidateResourceName(stem, "agent") != "" {
				s.unsupported(entry.Path)
				continue
			}
			document, ok := s.tomlDoc(entry.Path)
			if !ok {
				continue
			}
			tableAny, hasTable := document["agents"]
			table, tableOK := tableAny.(map[string]any)
			if len(document) != 1 || !hasTable || !tableOK || len(table) != 1 {
				s.errorf("invalid-agent", "Agent file must define its matching table only", entry.Path)
				continue
			}
			specAny, specOK := table[stem]
			spec, specMapOK := specAny.(map[string]any)
			if !specOK || !specMapOK {
				s.errorf("invalid-agent", "Agent file must define its matching table only", entry.Path)
				continue
			}
			s.add("agent", stem, ValueFingerprint(spec), []ResourcePart{{Path: s.rel(entry.Path)}}, nil, nil)
		}
	}

	skillsPath := filepath.Join(s.root, "skills.toml")
	skillsDoc, _ := s.tomlDoc(skillsPath)
	scanTables(s, "skills.toml", skillsDoc, map[string]bool{"skills": true})
	var selected []string
	invalidSelection := false
	if skillsDoc != nil {
		if rawSelected, ok := skillsDoc["skills"]; ok {
			list, isList := rawSelected.([]any)
			if !isList {
				invalidSelection = true
			} else {
				for _, item := range list {
					str, ok := item.(string)
					if !ok {
						invalidSelection = true
						continue
					}
					selected = append(selected, str)
				}
			}
		}
	}
	if invalidSelection {
		s.errorf("invalid-skill-selection", "Skills must be a list of names", skillsPath)
		selected = nil
	}
	selectedSet := map[string]struct{}{}
	for _, name := range selected {
		selectedSet[name] = struct{}{}
	}
	names := make([]string, 0, len(selectedSet))
	for n := range selectedSet {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		var refs []string
		if !IsBundledSkillName(name) {
			refs = []string{ResourceID("skill", name)}
		}
		s.add("skill-selection", name, "", []ResourcePart{{Path: "skills.toml"}}, refs, nil)
	}

	configPath := filepath.Join(s.root, "config.toml")
	if classifyEntry(configPath) != entryMissing {
		config, _ := s.tomlDoc(configPath)
		for _, kv := range flatten("", config) {
			key := kv[0].(string)
			table := ""
			if idx := strings.LastIndex(key, "."); idx >= 0 {
				table = key[:idx]
			}
			part := []ResourcePart{{Path: "config.toml", Table: table}}
			s.add("config", key, ValueFingerprint(kv[1]), part, nil, nil)
		}
	}
}

// readInboxConfigPath reads config.toml's [inbox] path field directly
// (default "inbox"). This duplicates a slice of config.py's
// load_workspace_config deliberately: the full workspace-config package
// (memory.stale_days, update.check, etc.) is a separate concern from
// resource scanning and isn't built yet; when it is, this should be
// replaced by a call into it rather than kept as a second implementation.
func readInboxConfigPath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "config.toml"))
	if err != nil {
		return "inbox"
	}
	doc, err := DecodeTOML(data)
	if err != nil {
		return "inbox"
	}
	inboxAny, ok := doc["inbox"]
	inboxTable, tableOK := inboxAny.(map[string]any)
	if !ok || !tableOK {
		return "inbox"
	}
	pathAny, ok := inboxTable["path"]
	path, strOK := pathAny.(string)
	if !ok || !strOK || strings.TrimSpace(path) == "" {
		return "inbox"
	}
	return path
}

const legacyDefaultInboxPath = "~/aikito/inbox"

// inboxDirectory mirrors _inbox_directory: resolves the configured inbox
// path, requiring it to be a strict subdirectory of root with no symlink
// crossing on any path segment. Returns "" (Python: None) if the configured
// inbox resolves outside the root.
func inboxDirectory(s *scanner, home string) string {
	raw := strings.TrimSpace(readInboxConfigPath(s.root))
	if raw == "" {
		raw = "inbox"
	}
	configured := ExpandUser(home, raw)
	legacyExpanded := ExpandUser(home, legacyDefaultInboxPath)
	if configured == legacyExpanded {
		configured = filepath.Join(s.root, "inbox")
	} else if !filepath.IsAbs(configured) {
		configured = filepath.Join(s.root, configured)
	}
	rel, err := filepath.Rel(s.root, configured)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if rel == "." {
		rel = ""
	}
	current := s.root
	if rel != "" {
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			current = filepath.Join(current, part)
			if classifyEntry(current) == entryLink {
				s.errorf("unsafe-entry", "Inbox path crosses a symbolic link", current)
				return ""
			}
		}
	}
	resolvedRoot, err := ResolvePath(s.root)
	if err != nil {
		return ""
	}
	resolvedInbox, err := ResolvePath(configured)
	if err != nil {
		return ""
	}
	finalRel, err := filepath.Rel(resolvedRoot, resolvedInbox)
	if err != nil || finalRel == ".." || strings.HasPrefix(finalRel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.Join(s.root, finalRel)
}

func scanInbox(s *scanner, inbox string) {
	if !s.directory(inbox, true) {
		return
	}
	for _, entry := range s.children(inbox) {
		if strings.HasPrefix(entry.Name, ".") {
			continue
		}
		switch classifyEntry(entry.Path) {
		case entryDirectory:
			scanInbox(s, entry.Path)
		default:
			if filepath.Ext(entry.Name) == ".md" {
				name := filepath.ToSlash(mustRel(filepath.Join(s.root, s.inboxRel), entry.Path))
				scanMarkdownFile(s, entry.Path, "inbox", name)
			} else if classifyEntry(entry.Path) == entryFile {
				s.skipped = append(s.skipped, s.rel(entry.Path))
			} else {
				s.errorf("unsafe-entry", "Unsupported inbox entry", entry.Path)
			}
		}
	}
}

// SnapshotWorkspace mirrors snapshot_workspace: read every canonical
// resource of one workspace without writing. home anchors "~" expansion for
// the configured inbox path (Python uses Path.expanduser(), which resolves
// against the real process home; this Go port threads it explicitly for
// testability, matching the rest of this package's Env-based approach).
func SnapshotWorkspace(root, home string) (*WorkspaceSnapshot, error) {
	resolvedRoot, err := ResolvePath(root)
	if err != nil {
		return nil, err
	}
	// is_recognized_workspace: the two file/dir marker sets, requiring
	// either layout.toml or the legacy agents.toml+subagents.toml pair.
	if !isRecognizedWorkspace(resolvedRoot) {
		return nil, resourceErrorf("Not an Aikito workspace: %s", resolvedRoot)
	}
	if err := RequireCurrentLayout(resolvedRoot); err != nil {
		return nil, resourceErrorf("%s", err.Error())
	}

	s := newScanner(resolvedRoot)
	scanTopLevelTOML(s)

	managed := map[string]bool{}
	for _, f := range _TopLevelFiles {
		managed[f] = true
	}
	for _, d := range _TopLevelDirs {
		managed[d] = true
	}
	inbox := inboxDirectory(s, home)
	if inbox == "" {
		s.skipped = append(s.skipped, "inbox (outside the workspace)")
	} else if inbox != resolvedRoot {
		s.inboxRel = s.rel(inbox)
		managed[strings.SplitN(s.inboxRel, "/", 2)[0]] = true
		scanInbox(s, inbox)
	}

	for _, entry := range s.children(resolvedRoot) {
		if !managed[entry.Name] {
			if _, local := LocalOnlyNames[entry.Name]; !local {
				s.skipped = append(s.skipped, entry.Name)
			}
		}
	}

	globalDir := filepath.Join(resolvedRoot, "global")
	if s.directory(globalDir, false) {
		for _, entry := range s.children(globalDir) {
			if entry.Name == "AGENTS.md" {
				scanMarkdownFile(s, entry.Path, "global-instructions", "AGENTS.md")
			} else {
				s.unsupported(entry.Path)
			}
		}
	}
	scanMemory(s, filepath.Join(resolvedRoot, "memory"), "memory", "")
	if s.directory(filepath.Join(resolvedRoot, "projects"), false) {
		for _, project := range s.children(filepath.Join(resolvedRoot, "projects")) {
			if s.directory(project.Path, false) {
				scanProject(s, project.Path)
			}
		}
	}
	if s.directory(filepath.Join(resolvedRoot, "skills"), false) {
		scanSkills(s, filepath.Join(resolvedRoot, "skills"))
	}
	scanSubagents(s, filepath.Join(resolvedRoot, "subagents"))
	scanMCPs(s, filepath.Join(resolvedRoot, "mcps"))

	sortedSkipped := append([]string(nil), s.skipped...)
	sort.Strings(sortedSkipped)
	return &WorkspaceSnapshot{
		Root:      resolvedRoot,
		Resources: s.resources,
		Findings:  s.findings,
		Skipped:   sortedSkipped,
	}, nil
}

// isRecognizedWorkspace mirrors init.py's is_recognized_workspace.
func isRecognizedWorkspace(root string) bool {
	if classifyEntry(filepath.Join(root, "skills.toml")) != entryFile {
		return false
	}
	for _, dir := range []string{"mcps", "memory", "projects", "skills", "global"} {
		if classifyEntry(filepath.Join(root, dir)) != entryDirectory {
			return false
		}
	}
	if classifyEntry(filepath.Join(root, "layout.toml")) == entryFile {
		return true
	}
	return classifyEntry(filepath.Join(root, "agents.toml")) == entryFile &&
		classifyEntry(filepath.Join(root, "subagents.toml")) == entryFile
}

// --- scan_credentials (best-effort plaintext-secret scan) ---

var secretPattern = regexp.MustCompile(
	`(?i)(?:api[_-]?key|access[_-]?token|password|client[_-]?secret)\s*[:=]\s*['"]?[A-Za-z0-9_./+\-=]{16,}`,
)

func HasCredentialBytes(content []byte) bool {
	return secretPattern.Match(content)
}

func contentFiles(path string) []string {
	switch classifyEntry(path) {
	case entryFile:
		return []string{path}
	case entryDirectory:
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		var files []string
		for _, name := range names {
			if !IsIgnoredName(name) {
				files = append(files, contentFiles(filepath.Join(path, name))...)
			}
		}
		return files
	default:
		return nil
	}
}

// ScanCredentials mirrors scan_credentials: a best-effort, warning-only scan
// for common plaintext-credential patterns across every resource's files.
// This is pattern matching, not a security boundary.
func ScanCredentials(snapshot *WorkspaceSnapshot) []Finding {
	pathSet := map[string]struct{}{}
	for _, res := range snapshot.Resources {
		for _, part := range res.Parts {
			pathSet[part.Path] = struct{}{}
		}
	}
	rels := make([]string, 0, len(pathSet))
	for p := range pathSet {
		rels = append(rels, p)
	}
	sort.Strings(rels)

	var findings []Finding
	for _, rel := range rels {
		for _, path := range contentFiles(filepath.Join(snapshot.Root, rel)) {
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if HasCredentialBytes(content) {
				relPath, _ := filepath.Rel(snapshot.Root, path)
				findings = append(findings, Finding{
					Status:   "warning",
					Message:  "Possible plaintext credential",
					Code:     "possible-credential",
					Resource: filepath.ToSlash(relPath),
				})
			}
		}
	}
	return findings
}
