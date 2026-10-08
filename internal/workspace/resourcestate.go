// Resource-state decode/encode: the pure (filesystem-free) mapping between
// a bare "kind:name" resource ID + fingerprint and a full Resource, used by
// the (not yet built) remote-sync layer to interpret a wire resource-state
// map without needing the remote's actual file content.
// Python: src/aikito/workspace/resource_state.py.
package workspace

import (
	"fmt"
	"sort"
	"strings"
)

// WorkspaceCoreError mirrors transactions.py's WorkspaceCoreError. This is a
// deliberate, acceptable duplicate: the real transaction engine now lives in
// internal/sync (which imports internal/workspace for fingerprinting, so the
// reverse import isn't possible without a cycle), and defines its own
// WorkspaceCoreError there. If the two ever need to interoperate directly
// (e.g. a caller wanting one error type across both), introduce a shared
// leaf package rather than making either package import the other.
type WorkspaceCoreError struct{ Message string }

func (e *WorkspaceCoreError) Error() string { return e.Message }

func coreErrorf(format string, args ...any) error {
	return &WorkspaceCoreError{Message: fmt.Sprintf(format, args...)}
}

// SyncKinds is the set of resource kinds eligible for sync/remote state
// (resource_state.py's SYNC_KINDS).
var SyncKinds = map[string]bool{
	"memory": true, "project-memory": true, "skill": true, "inbox": true,
	"global-instructions": true, "project-instructions": true,
	"agent": true, "mcp": true, "subagent": true,
	"skill-selection": true, "project": true, "project-field": true,
	"project-path": true, "project-skill": true, "config": true,
}

// LocalConfig holds host-local resource IDs' name components that can never
// be the target of a sync/remote write (currently just "inbox.path": the
// inbox location is per-machine).
var LocalConfig = map[string]bool{"inbox.path": true}

const (
	RemoteStateFile        = ".local/state/aikito/workspace-reconcile/remote.json"
	ReplicaStateFile       = ".local/state/aikito/workspace-reconcile/replica.json"
	PendingCommitStateFile = ".local/state/aikito/workspace-reconcile/pending.json"
	RemoteBindingStateFile = ".local/state/aikito/workspace-reconcile/binding.json"
)

// validateResourcePathBasic is a pure path-safety check standing in for
// transactions.py's validate_resource_path(path, physical_kind, policy). The
// full version (internal/sync.ValidateResourcePath, PathPolicy-aware) lives
// in internal/sync, which this package cannot import without a cycle (sync
// already imports workspace for fingerprinting). This covers the
// security-relevant subset (reject absolute/backslash/unsafe-component
// paths) that LocalResourceForID needs for its own decode-only purpose; the
// authoritative enforcement for anything actually written to disk happens
// in internal/sync.Apply via the real PathPolicy-aware validator.
func validateResourcePathBasic(path string) error {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return coreErrorf("Unsafe resource path: %s", path)
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" || part == ".local" || IsIgnoredName(part) {
			return coreErrorf("Unsafe resource path: %s", path)
		}
	}
	return nil
}

func isHexFingerprint(s string) bool {
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

// LocalResourceForID mirrors local_resource_for_id: decode a bare resource
// ID string + fingerprint into a full Resource (including its single
// physical ResourcePart and inferred References) without touching the
// filesystem.
func LocalResourceForID(identity, fingerprint string) (Resource, error) {
	kind, name, found := strings.Cut(identity, ":")
	if !found || name == "" || !SyncKinds[kind] {
		return Resource{}, coreErrorf("Unsupported resource ID: %s", identity)
	}

	var references []string
	table := ""
	project, member, _ := strings.Cut(name, "/")

	if kind == "project" && strings.Contains(name, "/") {
		return Resource{}, coreErrorf("Invalid resource ID: %s", identity)
	}
	if kind == "project-field" && (member == "path" || member == "paths" || member == "skills") {
		return Resource{}, coreErrorf("Invalid project field ID: %s", identity)
	}

	var path string
	switch {
	case kind == "config":
		if LocalConfig[name] {
			return Resource{}, coreErrorf("Host-local resource ID: %s", identity)
		}
		path = "config.toml"
		if idx := strings.LastIndex(name, "."); idx >= 0 {
			table = name[:idx]
		}
	case kind == "skill-selection":
		path = "skills.toml"
		references = []string{"skill:" + name}
	case kind == "project" || kind == "project-field" || kind == "project-path" || kind == "project-skill":
		if kind != "project" && member == "" {
			return Resource{}, coreErrorf("Invalid resource ID: %s", identity)
		}
		path = "projects/" + project + "/agent.toml"
		if kind != "project" {
			references = append(references, "project:"+project)
		}
		if kind == "project-skill" {
			references = append(references, "skill:"+member)
		}
	case kind == "project-memory":
		if member == "" {
			return Resource{}, coreErrorf("Invalid resource ID: %s", identity)
		}
		path = "projects/" + project + "/memory/" + member
		references = []string{"project:" + project}
	case kind == "project-instructions":
		path = "projects/" + name + "/AGENTS.md"
		references = []string{"project:" + name}
	case kind == "global-instructions":
		if name != "AGENTS.md" {
			return Resource{}, coreErrorf("Invalid resource ID: %s", identity)
		}
		path = "global/AGENTS.md"
	case kind == "agent" || kind == "mcp" || kind == "subagent":
		area := map[string]string{"agent": "agents", "mcp": "mcps", "subagent": "subagents"}[kind]
		suffix := "toml"
		if kind == "subagent" {
			suffix = "md"
		}
		path = area + "/" + name + "." + suffix
	default: // skill, memory, inbox
		area := map[string]string{"skill": "skills", "memory": "memory", "inbox": "inbox"}[kind]
		path = area + "/" + name
	}

	if err := validateResourcePathBasic(path); err != nil {
		return Resource{}, err
	}
	for _, part := range strings.Split(path, "/") {
		if IsIgnoredName(part) {
			return Resource{}, coreErrorf("Excluded resource ID: %s", identity)
		}
	}

	empty := kind == "project" || kind == "project-path" || kind == "project-skill" || kind == "skill-selection"
	if empty {
		if fingerprint != "" {
			return Resource{}, coreErrorf("Invalid resource fingerprint: %s", identity)
		}
	} else if !isHexFingerprint(fingerprint) {
		return Resource{}, coreErrorf("Invalid resource fingerprint: %s", identity)
	}

	// Bundled skills are virtual providers and never center content.
	filtered := make([]string, 0, len(references))
	for _, ref := range references {
		if strings.HasPrefix(ref, "skill:") && IsBundledSkillName(strings.TrimPrefix(ref, "skill:")) {
			continue
		}
		filtered = append(filtered, ref)
	}

	return Resource{
		Kind:        kind,
		Name:        name,
		Fingerprint: fingerprint,
		Parts:       []ResourcePart{{Path: path, Table: table}},
		References:  filtered,
	}, nil
}

// DecodeResources mirrors decode_resources: raw is a JSON-object-shaped
// map[string]any where each value is either a bare fingerprint string or a
// {"fingerprint", "references", "mode_fingerprint"} object.
func DecodeResources(raw map[string]any) (map[string]Resource, error) {
	resources := make(map[string]Resource, len(raw))
	for key, value := range raw {
		var fingerprint string
		valueMap, isMap := value.(map[string]any)
		if isMap {
			fp, _ := valueMap["fingerprint"].(string)
			fingerprint = fp
		} else if str, ok := value.(string); ok {
			fingerprint = str
		} else {
			return nil, coreErrorf("Invalid resource state")
		}

		resource, err := LocalResourceForID(key, fingerprint)
		if err != nil {
			return nil, err
		}

		if isMap {
			if modeAny, hasMode := valueMap["mode_fingerprint"]; hasMode && modeAny != nil {
				mode, ok := modeAny.(string)
				if resource.Kind != "skill" || !ok || !isHexFingerprint(mode) {
					return nil, coreErrorf("Invalid skill mode fingerprint")
				}
				resource.ModeFingerprint = &mode
			}
			refsAny, hasRefs := valueMap["references"]
			refsList, refsOK := refsAny.([]any)
			if !hasRefs || !refsOK {
				return nil, coreErrorf("Invalid resource references")
			}
			refs := make([]string, 0, len(refsList))
			for _, r := range refsList {
				str, ok := r.(string)
				if !ok {
					return nil, coreErrorf("Invalid resource references")
				}
				refs = append(refs, str)
			}
			if resource.Kind != "mcp" && resource.Kind != "subagent" && !equalStringSlices(refs, resource.References) {
				return nil, coreErrorf("Invalid resource references")
			}
			resource.References = refs
		}
		resources[key] = resource
	}
	return resources, nil
}

func equalStringSlices(a, b []string) bool {
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

// EncodeResources mirrors encode_resources.
func EncodeResources(resources map[string]Resource) map[string]any {
	out := make(map[string]any, len(resources))
	keys := make([]string, 0, len(resources))
	for k := range resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resource := resources[key]
		if resource.Kind == "skill" && resource.ModeFingerprint != nil {
			refs := resource.References
			if refs == nil {
				refs = []string{}
			}
			out[key] = map[string]any{
				"fingerprint":      resource.Fingerprint,
				"references":       toAnySlice(refs),
				"mode_fingerprint": *resource.ModeFingerprint,
			}
		} else {
			out[key] = resource.Fingerprint
		}
	}
	return out
}

// DecodeRevision mirrors decode_revision: reads the required nonnegative
// integer revision without rewriting state.
func DecodeRevision(state map[string]any) (int, error) {
	revAny, ok := state["revision"]
	if !ok {
		return 0, coreErrorf("Invalid resource state revision")
	}
	switch v := revAny.(type) {
	case int:
		if v < 0 {
			return 0, coreErrorf("Invalid resource state revision")
		}
		return v, nil
	case int64:
		if v < 0 {
			return 0, coreErrorf("Invalid resource state revision")
		}
		return int(v), nil
	default:
		return 0, coreErrorf("Invalid resource state revision")
	}
}

// ValidateSkillFingerprintScheme mirrors validate_skill_fingerprint_scheme:
// refuses to proceed if a stored state's fingerprint scheme tag isn't
// exactly SKILL_FINGERPRINT_SCHEME ("content-v1") and the state contains any
// skill resources — a forward-compat tripwire for a future fingerprint
// scheme migration. scheme == nil mirrors Python's scheme is None.
func ValidateSkillFingerprintScheme(scheme any, resources map[string]Resource) error {
	if s, ok := scheme.(string); ok && s == SkillFingerprintScheme {
		return nil
	}
	if scheme == nil {
		hasSkill := false
		for _, r := range resources {
			if r.Kind == "skill" {
				hasSkill = true
				break
			}
		}
		if !hasSkill {
			return nil
		}
	}
	return coreErrorf("Unsupported skill fingerprint scheme; preserve the existing center and " +
		"replica Base, then explicitly create and pair a new resource center")
}

// SkillFingerprintScheme mirrors resources.py's SKILL_FINGERPRINT_SCHEME.
const SkillFingerprintScheme = "content-v1"

// ValidIdentity mirrors valid_identity: a lowercase 32-char hex string (a
// replica/sync id), used by the (not yet built) remote-sync layer.
func ValidIdentity(value any) bool {
	s, ok := value.(string)
	if !ok || len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
