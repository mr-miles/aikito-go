// Package project ports aikito's project configuration model:
// src/aikito/project_config.py (path-candidate parsing, resolution, and the
// pure TOML surgical editor that appends a new candidate path) and the
// config schema/validation half of src/aikito/project_runtime.py (the
// Project.load() validation path, PROJECT_NAME_RE, _validate_project_config).
//
// Out of scope for this package (left for other, already-in-progress parts
// of the Go port):
//   - project.py's collect_project_summaries/status-aggregation layer: that
//     is the `aikito status`/`show project` reporting engine, and it
//     depends on skill_plan, skill_runtime, instructions, memory_runtime,
//     diff_model, and context_footprint — none of which exist in this Go
//     port yet. Porting it here would mean inventing fake dependencies on
//     unbuilt packages.
//   - Project.prepare()/add_path()/sync_project_path(): these orchestrate
//     the registry (load_agent_definitions) and the project sync engine
//     (project_sync.py's build/apply_project_sync_batch), both being built
//     elsewhere. This package exposes the pieces those will need
//     (ResolveProjectBinding, path resolution, config validation) but does
//     not itself drive agent launch or sync.
//   - The "empty project tree is tolerated as if absent" rule: that lives in
//     the workspace resource *scanner* (resources.py's _empty_project_tree),
//     not in project.py/project_config.py/project_runtime.py — it is the
//     scanner's decision whether a project directory counts as a resource
//     at all, so it belongs in internal/workspace, not here.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// DefaultSyncMode mirrors project_config.py's DEFAULT_PROJECT_SYNC_MODE.
const DefaultSyncMode = "link"

// PathEntry mirrors project_config.py's ProjectPathEntry.
type PathEntry struct {
	Label        string
	RawPath      string
	ResolvedPath string
	Exists       bool
}

// Binding mirrors project_config.py's ProjectBinding.
type Binding struct {
	Entries []PathEntry
}

func (b Binding) ActiveEntries() []PathEntry {
	var out []PathEntry
	for _, e := range b.Entries {
		if e.Exists {
			out = append(out, e)
		}
	}
	return out
}

func (b Binding) OfflineEntries() []PathEntry {
	var out []PathEntry
	for _, e := range b.Entries {
		if !e.Exists {
			out = append(out, e)
		}
	}
	return out
}

// ResolveProjectPath mirrors project_config.py's resolve_project_path.
// rawPath is `any` because it comes straight out of a decoded TOML/JSON
// value (config.get(...) in Python accepts any type and returns None for
// non-strings); ok is false when rawPath is not a non-empty string.
func ResolveProjectPath(rawPath any, home string) (string, bool) {
	s, isStr := rawPath.(string)
	if !isStr || s == "" {
		return "", false
	}
	raw := strings.TrimSpace(s)
	if raw == "~" {
		resolved, err := workspace.ResolvePath(home)
		if err != nil {
			return "", false
		}
		return resolved, true
	}
	normalized := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(normalized, "~/") {
		resolved, err := workspace.ResolvePath(filepath.Join(home, normalized[2:]))
		if err != nil {
			return "", false
		}
		return resolved, true
	}
	// Deliberately uses `raw` (not `normalized`): Python's fallback branch is
	// Path(raw).expanduser().resolve() using the original, non-backslash-
	// normalized string. This only matters for Windows-style absolute paths
	// reaching this branch, which the backslash normalization above does not
	// touch since it couldn't have matched the "~/" prefix anyway.
	expanded := workspace.ExpandUser(home, raw)
	resolved, err := workspace.ResolvePath(expanded)
	if err != nil {
		return "", false
	}
	return resolved, true
}

// candidate is a (label, raw path) pair, mirroring the tuple[str, str]
// entries get_project_candidate_paths returns.
type candidate struct{ label, raw string }

// GetProjectCandidatePaths mirrors project_config.py's
// get_project_candidate_paths.
//
// Known divergence: for the `[paths]` *table* form, Python iterates the
// dict in TOML source order; go-toml/v2 decodes TOML tables into a plain
// Go map, which has no preserved order, so this port iterates the table's
// keys sorted alphabetically instead. This only affects display/ordering
// (e.g. which labeled path is listed first) for projects with 2+ table-form
// path labels — not correctness of which paths are considered, since
// AmbiguousProjectPathError already blocks silently picking between
// multiple *active* paths regardless of order. Fixing this exactly would
// require an order-preserving TOML decode primitive in internal/workspace,
// out of scope for this package.
func GetProjectCandidatePaths(config map[string]any) []candidate {
	var candidates []candidate

	if rawPathsSec, ok := config["paths"]; ok {
		switch v := rawPathsSec.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if s, ok := v[k].(string); ok && strings.TrimSpace(s) != "" {
					candidates = append(candidates, candidate{k, strings.TrimSpace(s)})
				}
			}
		case []any:
			for idx, item := range v {
				if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
					candidates = append(candidates, candidate{strconv.Itoa(idx + 1), strings.TrimSpace(s)})
				}
			}
		}
	}

	if len(candidates) == 0 {
		if rawPath, ok := config["path"]; ok {
			switch v := rawPath.(type) {
			case []any:
				for idx, item := range v {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						candidates = append(candidates, candidate{strconv.Itoa(idx + 1), strings.TrimSpace(s)})
					}
				}
			case string:
				if strings.TrimSpace(v) != "" {
					candidates = append(candidates, candidate{"default", strings.TrimSpace(v)})
				}
			}
		}
	}

	return candidates
}

// ResolveProjectBinding mirrors project_config.py's resolve_project_binding:
// resolves every candidate path, drops ones that fail to resolve, dedupes
// by resolved path (first label wins), and records whether each resolved
// path currently exists as a directory on this host.
func ResolveProjectBinding(config map[string]any, home string) Binding {
	candidates := GetProjectCandidatePaths(config)
	var entries []PathEntry
	seen := map[string]bool{}
	for _, c := range candidates {
		resolved, ok := ResolveProjectPath(c.raw, home)
		if !ok {
			continue
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		info, err := os.Stat(resolved)
		exists := err == nil && info.IsDir()
		entries = append(entries, PathEntry{
			Label:        c.label,
			RawPath:      c.raw,
			ResolvedPath: resolved,
			Exists:       exists,
		})
	}
	return Binding{Entries: entries}
}

// safeRelativePath approximates compat.py's safe_relative_path: a display
// string relative to base with a "~/" prefix, falling back to the raw
// (slash-normalized) path when path is not under base. Never resolves
// symlinks. This is an approximation of Python's Path.relative_to (pure
// string-prefix containment) via Go's filepath.Rel (path algebra) — display
// purposes only, not used for any security-relevant decision.
func safeRelativePath(path, base string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, `..\`) {
		return filepath.ToSlash(path)
	}
	return "~/" + filepath.ToSlash(rel)
}

// DisplayProjectPath mirrors project_config.py's display_project_path.
// path == nil represents Python's `None`.
func DisplayProjectPath(path *string, home string) string {
	if path == nil {
		return "-"
	}
	resolvedHome, err := workspace.ResolvePath(home)
	if err != nil {
		resolvedHome = home
	}
	for _, base := range []string{home, resolvedHome} {
		displayed := safeRelativePath(*path, base)
		if strings.HasPrefix(displayed, "~/") {
			return displayed
		}
	}
	resolvedPath, err := workspace.ResolvePath(*path)
	if err == nil && resolvedPath != *path {
		displayed := safeRelativePath(resolvedPath, resolvedHome)
		if strings.HasPrefix(displayed, "~/") {
			return displayed
		}
	}
	return filepath.ToSlash(*path)
}

// DisplayCandidatePath mirrors project_config.py's display_candidate_path.
func DisplayCandidatePath(entry PathEntry, home string) string {
	if entry.Exists {
		resolved := entry.ResolvedPath
		return DisplayProjectPath(&resolved, home)
	}
	raw := strings.ReplaceAll(strings.TrimSpace(entry.RawPath), `\`, "/")
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		return raw
	}
	if filepath.IsAbs(raw) {
		return DisplayProjectPath(&raw, home)
	}
	// Keep configured other-OS paths (e.g. D:/...) instead of cwd-resolving them.
	return raw
}

// CandidateView mirrors one entry of project_config.py's
// candidate_path_views tuple output.
type CandidateView struct {
	Label   string
	Display string
	Exists  bool
}

// CandidatePathViews mirrors project_config.py's candidate_path_views.
func CandidatePathViews(binding Binding, home string) []CandidateView {
	views := make([]CandidateView, 0, len(binding.Entries))
	for _, e := range binding.Entries {
		views = append(views, CandidateView{
			Label:   e.Label,
			Display: DisplayCandidatePath(e, home),
			Exists:  e.Exists,
		})
	}
	return views
}

// JoinedCandidatePaths mirrors project_config.py's joined_candidate_paths.
func JoinedCandidatePaths(views []CandidateView) string {
	if len(views) == 0 {
		return "-"
	}
	parts := make([]string, len(views))
	for i, v := range views {
		parts[i] = v.Display
	}
	return strings.Join(parts, ", ")
}

// --- Config schema validation (project_runtime.py _validate_project_config) ---

// InvalidConfigError mirrors project_runtime.py's InvalidProjectConfigError.
type InvalidConfigError struct{ Message string }

func (e *InvalidConfigError) Error() string { return e.Message }

func invalidConfigf(format string, args ...any) error {
	return &InvalidConfigError{Message: fmt.Sprintf(format, args...)}
}

// ValidateProjectConfig mirrors project_runtime.py's _validate_project_config.
//
// Note on the "memory" field: it is validated identically to "skills" (a
// list of non-empty strings) but neither project_config.py nor
// project_runtime.py do anything further with it — its consumer is
// project.py's collect_project_summaries (out of scope here, see the
// package doc comment), which reads it as a set of memory note names to
// filter against. There is no additional semantic to port at this layer.
func ValidateProjectConfig(configPath string, config map[string]any) error {
	for _, field := range []string{"skills", "memory"} {
		value, ok := config[field]
		if !ok {
			continue // default: empty list, always valid
		}
		list, isList := value.([]any)
		if !isList {
			return invalidConfigf("Project field '%s' must be a list of non-empty strings in %s", field, configPath)
		}
		for _, item := range list {
			s, isStr := item.(string)
			if !isStr || s == "" {
				return invalidConfigf("Project field '%s' must be a list of non-empty strings in %s", field, configPath)
			}
		}
	}

	syncMode := DefaultSyncMode
	if v, ok := config["sync_mode"]; ok {
		s, isStr := v.(string)
		if !isStr {
			return invalidConfigf("Project field 'sync_mode' must be 'link' or 'copy' in %s", configPath)
		}
		syncMode = s
	}
	if syncMode != "link" && syncMode != "copy" {
		return invalidConfigf("Project field 'sync_mode' must be 'link' or 'copy' in %s", configPath)
	}

	paths, hasPaths := config["paths"]
	validPaths := true
	if hasPaths {
		switch v := paths.(type) {
		case []any:
			for _, item := range v {
				s, isStr := item.(string)
				if !isStr || s == "" {
					validPaths = false
					break
				}
			}
		case map[string]any:
			for label, val := range v {
				s, isStr := val.(string)
				if label == "" || !isStr || s == "" {
					validPaths = false
					break
				}
			}
		default:
			validPaths = false
		}
	}

	path, hasPath := config["path"]
	validPath := true
	if hasPath {
		switch v := path.(type) {
		case string:
			validPath = v != ""
		case []any:
			for _, item := range v {
				s, isStr := item.(string)
				if !isStr || s == "" {
					validPath = false
					break
				}
			}
		default:
			validPath = false
		}
	}

	if !validPaths || !validPath {
		return invalidConfigf("Project paths must contain non-empty strings in %s", configPath)
	}
	return nil
}

var projectNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidProjectName reports whether name matches PROJECT_NAME_RE
// (project_runtime.py). Confirmed identical, by direct source comparison,
// to init.py's _validate_project_name regex `[A-Za-z0-9][A-Za-z0-9._-]*`
// (fullmatch) — the research spec that scoped this package flagged these as
// *possibly* divergent and asked for confirmation; they are not divergent,
// they are the exact same pattern. (internal/workspace's ValidateProjectName,
// added by a concurrent part of this port, already encodes this same
// pattern for the CLI/init layer; this package's own regexp is kept
// independent of that one's error-message wording since project_runtime.py's
// "Invalid Aikito project name: %r" error text differs from init.py's.)
func ValidProjectName(name string) bool {
	return projectNameRe.MatchString(name)
}

// Config is a loaded, validated project configuration: the raw decoded
// agent.toml tree plus the project identity needed to resolve its paths.
type Config struct {
	Name string
	Home string
	Path string // absolute path to projects/<name>/agent.toml
	Raw  map[string]any
}

// LoadConfig mirrors the validation half of project_runtime.py's
// Project.load(): name pattern check, config-name-must-match-directory
// check, and _validate_project_config. It deliberately does NOT port
// collect_resource_conflicts's symlink-safety preflight (conflict.py is not
// yet part of this Go port) — callers that need that guarantee must add it
// once the conflict-detection package exists.
func LoadConfig(workspace_, home, name string) (*Config, error) {
	if !ValidProjectName(name) {
		return nil, invalidConfigf("Invalid Aikito project name: %q", name)
	}
	configPath := filepath.Join(workspace_, "projects", name, "agent.toml")
	info, err := os.Stat(configPath)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("Aikito project not found: %s", name)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, invalidConfigf("Invalid Aikito project config %s: %v", configPath, err)
	}
	config, err := wsDecodeTOML(data)
	if err != nil {
		return nil, invalidConfigf("Invalid Aikito project config %s: %v", configPath, err)
	}
	if configuredName, ok := config["name"]; ok {
		if s, isStr := configuredName.(string); !isStr || s != name {
			return nil, invalidConfigf("Project name in %s must be '%s', got %v", configPath, name, configuredName)
		}
	}
	if err := ValidateProjectConfig(configPath, config); err != nil {
		return nil, err
	}
	return &Config{Name: name, Home: home, Path: configPath, Raw: config}, nil
}

// Binding resolves this project's configured candidate paths for the
// current host.
func (c *Config) Binding() Binding {
	return ResolveProjectBinding(c.Raw, c.Home)
}

// wsDecodeTOML is a thin indirection so this file's only internal/workspace
// dependency for TOML parsing is explicit and easy to find.
func wsDecodeTOML(data []byte) (map[string]any, error) {
	return workspace.DecodeTOML(data)
}
