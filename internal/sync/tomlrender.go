// TOML merge rendering: two string-surgical editors (never a full
// parse-mutate-reserialize) that let a shared TOML file multiple
// domains/agents write into keep unrelated hand-written formatting,
// comments, and fields untouched. Ported from
// aikito/src/aikito/workspace/toml_render.py plus the small formatting
// helpers it borrows from add.py (format_toml_key/format_toml_value,
// update_skills_in_toml and its two private helpers).
package sync

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/mr-miles/aikito-go/internal/compat"
	"github.com/mr-miles/aikito-go/internal/project"
	"github.com/mr-miles/aikito-go/internal/workspace"
)

// --- small path/file helpers for the import-path renderer ---

func joinPosix(root, relative string) string {
	return filepath.Join(root, filepath.FromSlash(relative))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readFileIfExists returns (nil, nil) if path doesn't exist, else its
// content or a read error.
func readFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func readFileRequired(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// projectAddCandidatePath mirrors render_merged_files' call into
// project_config.py's add_candidate_path_to_content(text, value, Path.home(),
// match_resolved=False). Python hardcodes the real OS home directory here
// rather than threading one through; this Go port makes that dependency
// explicit via the home parameter instead, matching this port's
// explicit-environment testing philosophy.
func projectAddCandidatePath(text, rawPath, home string) (string, bool, error) {
	return project.AddCandidatePathToContent(text, rawPath, home, false)
}

// TomlValue is one field's actual dotted key-path components and typed
// value, independent of which physical file/table it currently lives in.
type TomlValue struct {
	Path  []string
	Value any
}

func tomlPathsEqual(a, b []string) bool {
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

// DocumentValues flattens a parsed TOML document into dotted-path TomlValue
// entries: every leaf scalar (after descending nested tables) becomes one
// entry keyed by its dot-joined path. This is the "logical field identity"
// used to find/replace/remove a config field regardless of nesting.
func DocumentValues(document map[string]any, prefix []string) map[string]TomlValue {
	result := map[string]TomlValue{}
	for key, value := range document {
		path := append(append([]string(nil), prefix...), key)
		if nested, ok := value.(map[string]any); ok {
			for k, v := range DocumentValues(nested, path) {
				result[k] = v
			}
		} else {
			result[strings.Join(path, ".")] = TomlValue{Path: path, Value: value}
		}
	}
	return result
}

// removeTomlValue deletes the value at path from document, pruning any
// parent table left empty by the removal (walking back up, stopping at the
// first still-non-empty ancestor) — mirrors toml_render.py's _remove_value.
func removeTomlValue(document map[string]any, path []string) {
	node := document
	type frame struct {
		m   map[string]any
		key string
	}
	var parents []frame
	for _, key := range path[:len(path)-1] {
		child, ok := node[key].(map[string]any)
		if !ok {
			return
		}
		parents = append(parents, frame{node, key})
		node = child
	}
	delete(node, path[len(path)-1])
	for i := len(parents) - 1; i >= 0; i-- {
		p := parents[i]
		if m, ok := p.m[p.key].(map[string]any); ok && len(m) == 0 {
			delete(p.m, p.key)
		} else {
			break
		}
	}
}

// --- format_toml_key / format_toml_value (add.py) ---

// FormatTomlKey mirrors add.py's format_toml_key: a bare (unquoted) key when
// every rune is alphanumeric/"_"/"-", else a JSON-quoted string (reusing
// workspace.CanonicalJSON's string encoder, which already matches Python's
// json.dumps(s, ensure_ascii=False) byte-for-byte for a plain string value).
func FormatTomlKey(key string) string {
	bare := key != ""
	for _, r := range key {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			bare = false
			break
		}
	}
	if bare {
		return key
	}
	return workspace.CanonicalJSON(key)
}

// pyNumberStr mirrors Python's str(val) for an int or float — NOT
// json.dumps, which format_toml_value deliberately avoids for numbers (str()
// and json.dumps produce the same digit sequence for ints and finite
// floats, but str() of nan/inf/-inf is lowercase "nan"/"inf"/"-inf", unlike
// JSON's capitalized non-standard NaN/Infinity extension tokens used
// elsewhere in this port's fingerprint hashing — these are two independent,
// intentionally-not-unified formatters for two different purposes).
func pyNumberStr(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.FormatInt(int64(x), 10)
	case float64:
		return formatPyFloatStr(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func formatPyFloatStr(f float64) string {
	if math.IsNaN(f) {
		return "nan"
	}
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	return compat.PyFloatRepr(f)
}

// FormatTomlValue mirrors add.py's format_toml_value: used directly for
// brand-new document projection (where there's no original formatting to
// preserve, so dict keys are rendered sorted for determinism — unlike
// tomlValueRender's dict branch below, which Python back ends with
// insertion order for an *existing* parsed table).
func FormatTomlValue(val any) string {
	switch x := val.(type) {
	case string:
		return workspace.CanonicalJSON(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64, int, float64:
		return pyNumberStr(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = FormatTomlValue(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = FormatTomlKey(k) + " = " + FormatTomlValue(x[k])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	default:
		return workspace.CanonicalJSON(fmt.Sprintf("%v", val))
	}
}

func isoDate(d toml.LocalDate) string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

func isoTime(t toml.LocalTime) string {
	micro := t.Nanosecond / 1000
	if micro == 0 {
		return fmt.Sprintf("%02d:%02d:%02d", t.Hour, t.Minute, t.Second)
	}
	return fmt.Sprintf("%02d:%02d:%02d.%06d", t.Hour, t.Minute, t.Second, micro)
}

func isoOffsetDateTime(t time.Time) string {
	date := fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
	micro := t.Nanosecond() / 1000
	var timePart string
	if micro == 0 {
		timePart = fmt.Sprintf("%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
	} else {
		timePart = fmt.Sprintf("%02d:%02d:%02d.%06d", t.Hour(), t.Minute(), t.Second(), micro)
	}
	_, offset := t.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hours := offset / 3600
	minutes := (offset % 3600) / 60
	secs := offset % 60
	if secs == 0 {
		return date + "T" + timePart + fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
	}
	return date + "T" + timePart + fmt.Sprintf("%s%02d:%02d:%02d", sign, hours, minutes, secs)
}

// tomlValueRender mirrors toml_render.py's _toml_value: date/time/datetime
// values render via isoformat() (T-separator — distinct from
// workspace.ValueFingerprint's str()-style space-separator wrapper used for
// hashing; these are two independent formatters for two different purposes,
// don't unify them), lists/dicts recurse into this same function, and every
// other scalar delegates to FormatTomlValue.
//
// Known, deliberate simplification: Python's dict branch here iterates
// value.items() in the source dict's original insertion order (meaningful
// for a hand-authored inline TOML table, e.g. MCP header key order).
// go-toml/v2 decodes into a plain Go map with no preserved order, so this
// renders nested dict-VALUE keys sorted instead (same as FormatTomlValue).
// This never affects top-level document structure (handled separately via
// topLevelKeyOrder, see render_sync_file) and always produces valid TOML —
// just not necessarily in the original nested-table field order.
func tomlValueRender(value any) string {
	switch x := value.(type) {
	case toml.LocalDate:
		return isoDate(x)
	case toml.LocalTime:
		return isoTime(x)
	case toml.LocalDateTime:
		return isoDate(x.LocalDate) + "T" + isoTime(x.LocalTime)
	case time.Time:
		return isoOffsetDateTime(x)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = tomlValueRender(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = FormatTomlKey(k) + " = " + tomlValueRender(x[k])
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	default:
		return FormatTomlValue(value)
	}
}

// --- splitlines helpers (simplified: \n only, sufficient for programmatically-managed TOML) ---

func splitLinesKeepEnds(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.SplitAfter(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func splitLinesNoEnds(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// --- _adopt_field ---

// AdoptField mirrors toml_render.py's _adopt_field: set one TOML scalar
// field via line-level text surgery, preserving every other line's
// formatting exactly, including a trailing "# comment" on the edited line.
func AdoptField(text string, sections []string, key string, value any) string {
	header := ""
	if len(sections) > 0 {
		header = "[" + strings.Join(sections, ".") + "]"
	}
	rendered := FormatTomlKey(key) + " = " + tomlValueRender(value)
	lines := splitLinesKeepEnds(text)

	start := 0
	if header != "" {
		found := false
		for i, line := range lines {
			if strings.TrimSpace(line) == header {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return strings.TrimRight(text, "\n") + "\n\n" + header + "\n" + rendered + "\n"
		}
	}

	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimLeftFunc(lines[i], unicode.IsSpace), "[") {
			end = i
			break
		}
	}

	assignmentRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	for i := start; i < end; i++ {
		if assignmentRe.MatchString(lines[i]) {
			comment := ""
			if idx := strings.Index(lines[i], "#"); idx >= 0 {
				comment = " #" + strings.TrimRight(lines[i][idx+1:], "\r\n")
			}
			lines[i] = rendered + comment + "\n"
			return strings.Join(lines, "")
		}
	}

	if end > 0 && !strings.HasSuffix(lines[end-1], "\n") {
		lines[end-1] += "\n"
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:end]...)
	out = append(out, rendered+"\n")
	out = append(out, lines[end:]...)
	return strings.Join(out, "")
}

// --- top-level key order tracking (Go-map-has-no-order workaround) ---

// topLevelKeyRe matches a top-level "key =" assignment line (bare or
// double-quoted key). Used only to recover Python dict insertion order for
// a document's TOP-LEVEL keys (these shared files are always flat — no
// "[section]" headers — per this port's shared-file design, see
// render_sync_file's doc comment), since go-toml/v2 decodes into a plain
// unordered Go map. Known gap: single-quoted TOML literal keys aren't
// recognized (these files are always machine-written by this tool or its
// Python counterpart, which never emits single-quoted keys).
var topLevelKeyRe = regexp.MustCompile(`(?m)^[ \t]*("(?:[^"\\]|\\.)*"|[A-Za-z0-9_-]+)[ \t]*=`)

func topLevelKeyOrder(text string) []string {
	var order []string
	seen := map[string]bool{}
	for _, m := range topLevelKeyRe.FindAllStringSubmatch(text, -1) {
		raw := m[1]
		key := raw
		if strings.HasPrefix(raw, `"`) {
			if decoded, err := workspace.DecodeStrictJSON(raw); err == nil {
				if s, ok := decoded.(string); ok {
					key = s
				}
			}
		}
		if !seen[key] {
			seen[key] = true
			order = append(order, key)
		}
	}
	return order
}

// orderedDocumentKeys returns document's keys ordered to match, as closely
// as this port can reconstruct, what Python's dict iteration would have
// produced: original-text order first (for keys still present), then any
// keys introduced only during this edit (in extraOrder, the order they were
// first touched), then anything left over (sorted, for determinism) as a
// final safety net.
func orderedDocumentKeys(document map[string]any, originalText string, extraOrder []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range topLevelKeyOrder(originalText) {
		if _, ok := document[k]; ok && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	for _, k := range extraOrder {
		if _, ok := document[k]; ok && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range document {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	out = append(out, rest...)
	return out
}

func renderFlatDocument(document map[string]any, originalText string, extraOrder []string) string {
	var b strings.Builder
	for _, key := range orderedDocumentKeys(document, originalText, extraOrder) {
		b.WriteString(FormatTomlKey(key))
		b.WriteString(" = ")
		b.WriteString(tomlValueRender(document[key]))
		b.WriteString("\n")
	}
	return b.String()
}

// --- render_sync_file ---

func partitionAfterSlash(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return ""
}

// RenderSyncFile mirrors render_sync_file: batched multi-field
// reconciliation for a shared TOML file (config.toml, skills.toml, or a
// project's agent.toml), used by sync (apply canonical workspace values
// back over a shared file, deleting fields that got removed) as opposed to
// import's renderMergedFiles (project only selected incoming fields,
// preserving the target's existing formatting of everything else).
//
// Deletions (Fingerprint == nil) are processed before creates/updates —
// confirmed directly against toml_render.py's
// `sorted(items, key=lambda item: item.fingerprint is not None)`: Python's
// bool sort key puts False (no fingerprint, i.e. deletions) before True —
// so a same-batch "move to a different dotted path" converges correctly
// regardless of input order.
//
// Returns (nil, nil) when relative is a project file and no "project" kind
// resource remains in expected (the project was deleted in this same
// batch) — the whole file should be dropped, not rewritten, matching
// Python's `return None`.
func RenderSyncFile(text string, items []ResourceWrite, values map[string]TomlValue, expected map[string]workspace.Resource) (*string, error) {
	document, err := workspace.DecodeTOML([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("invalid TOML content: %w", err)
	}
	if document == nil {
		document = map[string]any{}
	}

	ordered := append([]ResourceWrite(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		iHas := ordered[i].Fingerprint != nil
		jHas := ordered[j].Fingerprint != nil
		return !iHas && jHas
	})

	var newTopLevelKeys []string
	noteNewKey := func(key string) {
		if _, ok := document[key]; !ok {
			newTopLevelKeys = append(newTopLevelKeys, key)
		}
	}

	for _, item := range ordered {
		if item.Kind != "config" && item.Kind != "project-field" {
			continue
		}
		var current *TomlValue
		if item.Kind == "config" {
			if v, ok := DocumentValues(document, nil)[item.Name]; ok {
				cv := v
				current = &cv
			}
		} else {
			member := partitionAfterSlash(item.Name)
			cv := TomlValue{Path: []string{member}, Value: document[member]}
			current = &cv
		}

		var field *TomlValue
		if item.Fingerprint != nil {
			if v, ok := values[item.ID()]; ok {
				fv := v
				field = &fv
			}
		} else {
			field = current
		}

		if field == nil {
			if item.Fingerprint == nil {
				continue
			}
			return nil, fmt.Errorf("missing TOML value: %s", item.ID())
		}

		if current != nil && !tomlPathsEqual(field.Path, current.Path) {
			removeTomlValue(document, current.Path)
		}

		if item.Fingerprint != nil {
			// Creating/updating: note the TOP-LEVEL key as newly introduced
			// (once — subsequent checks see it already present in document
			// and skip) before any mutation touches it, so newTopLevelKeys
			// reflects first-touched order, matching Python dict insertion
			// order for a brand-new top-level key.
			noteNewKey(field.Path[0])
		}

		node := document
		type frame struct {
			m   map[string]any
			key string
		}
		var parents []frame
		for _, key := range field.Path[:len(field.Path)-1] {
			child, ok := node[key].(map[string]any)
			if !ok {
				if _, exists := node[key]; exists {
					return nil, fmt.Errorf("conflicting TOML field: %s", item.ID())
				}
				child = map[string]any{}
				node[key] = child
			}
			parents = append(parents, frame{node, key})
			node = child
		}
		leafKey := field.Path[len(field.Path)-1]
		if item.Fingerprint == nil {
			delete(node, leafKey)
			for i := len(parents) - 1; i >= 0; i-- {
				p := parents[i]
				if m, ok := p.m[p.key].(map[string]any); ok && len(m) == 0 {
					delete(p.m, p.key)
				} else {
					break
				}
			}
		} else {
			node[leafKey] = field.Value
		}
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("RenderSyncFile requires at least one item")
	}
	relative := items[0].RelativePath
	var resources []workspace.Resource
	for _, r := range expected {
		if len(r.Parts) > 0 && r.Parts[0].Path == relative {
			resources = append(resources, r)
		}
	}

	switch {
	case relative == "skills.toml":
		var names []string
		for _, r := range resources {
			if r.Kind == "skill-selection" {
				names = append(names, r.Name)
			}
		}
		sort.Strings(names)
		asAny := make([]any, len(names))
		for i, n := range names {
			asAny[i] = n
		}
		noteNewKey("skills")
		document["skills"] = asAny
	case strings.HasPrefix(relative, "projects/"):
		hasProject := false
		for _, r := range resources {
			if r.Kind == "project" {
				hasProject = true
				break
			}
		}
		if !hasProject {
			return nil, nil
		}
		kinds := map[string]bool{}
		for _, item := range items {
			kinds[item.Kind] = true
		}
		if kinds["project-skill"] {
			var names []string
			for _, r := range resources {
				if r.Kind == "project-skill" {
					names = append(names, partitionAfterSlash(r.Name))
				}
			}
			sort.Strings(names)
			asAny := make([]any, len(names))
			for i, n := range names {
				asAny[i] = n
			}
			noteNewKey("skills")
			document["skills"] = asAny
		}
		if kinds["project-path"] {
			delete(document, "path")
			delete(document, "paths")
			var paths []string
			for _, r := range resources {
				if r.Kind == "project-path" {
					paths = append(paths, partitionAfterSlash(r.Name))
				}
			}
			sort.Strings(paths)
			if len(paths) > 0 {
				asAny := make([]any, len(paths))
				for i, p := range paths {
					asAny[i] = p
				}
				noteNewKey("paths")
				document["paths"] = asAny
			}
		}
	}

	var comments strings.Builder
	for _, line := range splitLinesNoEnds(text) {
		if strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "#") {
			comments.WriteString(line)
			comments.WriteString("\n")
		}
	}
	result := comments.String() + renderFlatDocument(document, text, newTopLevelKeys)
	return &result, nil
}

// --- update_skills_in_toml / _format_skills_array / _find_matching_bracket (add.py) ---

func formatSkillsArray(skills []string) string {
	if len(skills) == 0 {
		return "skills = []"
	}
	items := make([]string, len(skills))
	for i, s := range skills {
		items[i] = `    "` + s + `"`
	}
	return "skills = [\n" + strings.Join(items, ",\n") + "\n]"
}

// findMatchingBracket finds the closing "]" matching the opening bracket at
// startBracketPos, skipping over bracket characters inside quoted strings.
func findMatchingBracket(text string, startBracketPos int) int {
	inString := false
	var stringChar byte
	escape := false
	depth := 0
	i := startBracketPos
	for i < len(text) {
		c := text[i]
		if escape {
			escape = false
			i++
			continue
		}
		if c == '\\' {
			if inString {
				escape = true
			}
			i++
			continue
		}
		if c == '"' || c == '\'' {
			if !inString {
				inString = true
				stringChar = c
			} else if stringChar == c {
				inString = false
			}
			i++
			continue
		}
		if !inString {
			if c == '[' {
				depth++
			} else if c == ']' {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
		i++
	}
	return -1
}

var skillsBracketRe = regexp.MustCompile(`(?m)^skills\s*=\s*\[`)
var skillsOtherRe = regexp.MustCompile(`(?m)^skills\s*=.*$`)
var tableHeaderRe = regexp.MustCompile(`(?m)^\[[a-zA-Z0-9_.-]+\]`)

// UpdateSkillsInToml mirrors add.py's update_skills_in_toml: update the
// top-level "skills" array while preserving every other key, nested table,
// comment, and whitespace exactly.
func UpdateSkillsInToml(originalText string, newSkills []string) string {
	formatted := formatSkillsArray(newSkills)

	if loc := skillsBracketRe.FindStringIndex(originalText); loc != nil {
		startBracket := loc[1] - 1
		endBracket := findMatchingBracket(originalText, startBracket)
		if endBracket != -1 {
			return originalText[:loc[0]] + formatted + originalText[endBracket+1:]
		}
	}

	if loc := skillsOtherRe.FindStringIndex(originalText); loc != nil {
		return originalText[:loc[0]] + formatted + originalText[loc[1]:]
	}

	if loc := tableHeaderRe.FindStringIndex(originalText); loc != nil {
		prefix := strings.TrimRight(originalText[:loc[0]], " \t\r\n")
		suffix := originalText[loc[0]:]
		if prefix != "" {
			return prefix + "\n\n" + formatted + "\n\n" + suffix
		}
		return formatted + "\n\n" + suffix
	}

	trimmed := strings.TrimRight(originalText, " \t\r\n")
	if trimmed != "" {
		return trimmed + "\n\n" + formatted + "\n"
	}
	return formatted + "\n"
}

// --- import-path rendering: render_merged_files / _new_file_text / _list_field ---

// listField reads relative's TOML content under root and returns its `key`
// field as a []string (mirrors toml_render.py's _list_field: a bare string
// value is treated as a single-element list; anything else non-list-of-
// strings is an error). A missing file yields an empty list, not an error.
func listField(root, relative, key string) ([]string, error) {
	path := joinPosix(root, relative)
	data, err := readFileIfExists(path)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	document, err := workspace.DecodeTOML(data)
	if err != nil {
		return nil, fmt.Errorf("invalid %s in %s: %w", key, path, err)
	}
	value, ok := document[key]
	if !ok {
		return nil, nil
	}
	if s, ok := value.(string); ok {
		return []string{s}, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid %s in %s", key, path)
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("invalid %s in %s", key, path)
		}
		out[i] = s
	}
	return out, nil
}

func nestedTomlValue(document map[string]any, name string) (any, error) {
	var current any = document
	for _, part := range strings.Split(name, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("missing source configuration field: %s", name)
		}
		v, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("missing source configuration field: %s", name)
		}
		current = v
	}
	return current, nil
}

// newFileText mirrors toml_render.py's _new_file_text: project a new shared
// file's initial content when some source fields were skipped. Returns
// original verbatim when the projection turns out to equal the whole source
// document (the common "import everything" case, formatting-preserving).
func newFileText(document map[string]any, items []ResourceWrite, original string) string {
	kinds := map[string]bool{}
	for _, item := range items {
		kinds[item.Kind] = true
	}

	var filtered map[string]any
	switch {
	case kinds["config"]:
		selected := map[string]bool{}
		for _, item := range items {
			if item.Kind == "config" {
				selected[item.Name] = true
			}
		}
		var project func(value map[string]any, prefix string) map[string]any
		project = func(value map[string]any, prefix string) map[string]any {
			result := map[string]any{}
			for key, child := range value {
				name := key
				if prefix != "" {
					name = prefix + "." + key
				}
				if nested, ok := child.(map[string]any); ok {
					if p := project(nested, name); len(p) > 0 {
						result[key] = p
					}
				} else if selected[name] {
					result[key] = child
				}
			}
			return result
		}
		filtered = project(document, "")
		if documentsEqual(filtered, document) {
			return original
		}
	case kinds["project"]:
		selected := map[string]bool{}
		for _, item := range items {
			if item.Kind == "project-field" {
				selected[partitionAfterSlash(item.Name)] = true
			}
		}
		fields := map[string]bool{}
		for key := range document {
			if key != "path" && key != "paths" && key != "skills" {
				fields[key] = true
			}
		}
		paths := map[string]bool{}
		for _, item := range items {
			if item.Kind == "project-path" {
				paths[partitionAfterSlash(item.Name)] = true
			}
		}
		sourcePaths := map[string]bool{}
		switch raw := document["paths"].(type) {
		case map[string]any:
			for _, v := range raw {
				if s, ok := v.(string); ok {
					sourcePaths[s] = true
				}
			}
		case []any:
			for _, v := range raw {
				if s, ok := v.(string); ok {
					sourcePaths[s] = true
				}
			}
		}
		if p, ok := document["path"].(string); ok && p != "" {
			sourcePaths[p] = true
		}
		fieldsSubsetOfSelected := true
		for f := range fields {
			if !selected[f] {
				fieldsSubsetOfSelected = false
				break
			}
		}
		sourcePathsSubsetOfPaths := true
		for p := range sourcePaths {
			if !paths[p] {
				sourcePathsSubsetOfPaths = false
				break
			}
		}
		if fieldsSubsetOfSelected && sourcePathsSubsetOfPaths {
			return original
		}
		filtered = map[string]any{}
		for key, value := range document {
			if selected[key] {
				filtered[key] = value
			}
		}
		if len(paths) > 0 {
			names := make([]string, 0, len(paths))
			for p := range paths {
				names = append(names, p)
			}
			sort.Strings(names)
			asAny := make([]any, len(names))
			for i, n := range names {
				asAny[i] = n
			}
			filtered["paths"] = asAny
		}
	default:
		return original
	}

	keys := make([]string, 0, len(filtered))
	for k := range filtered {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(FormatTomlKey(k))
		b.WriteString(" = ")
		b.WriteString(tomlValueRender(filtered[k]))
		b.WriteString("\n")
	}
	return b.String()
}

func documentsEqual(a, b map[string]any) bool {
	return workspace.ValueFingerprint(a) == workspace.ValueFingerprint(b)
}

// RenderMergedFiles mirrors render_merged_files: the import-path renderer.
// Per distinct target relative path touched by changes, either synthesizes
// fresh text (newFileText, projecting only selected fields from the
// source) when the target doesn't exist yet, or starts from the target's
// own current text and overlays selected scalar fields read from the
// source via AdoptField (so import never replaces a pre-existing target
// file wholesale) — then applies skill-selection/project-skill membership
// union and project candidate-path union, and finally re-parses the result
// to assert it's still valid TOML before returning it.
func RenderMergedFiles(sourceRoot, targetRoot string, changes []ResourceWrite, home string) (map[string]string, error) {
	groups := map[string][]ResourceWrite{}
	var order []string
	for _, item := range changes {
		if _, ok := groups[item.RelativePath]; !ok {
			order = append(order, item.RelativePath)
		}
		groups[item.RelativePath] = append(groups[item.RelativePath], item)
	}

	result := map[string]string{}
	for _, relative := range order {
		items := groups[relative]
		targetPath := joinPosix(targetRoot, relative)
		existing := fileExists(targetPath)

		var text string
		if existing {
			data, err := readFileRequired(targetPath)
			if err != nil {
				return nil, err
			}
			text = data
		} else {
			data, err := readFileRequired(joinPosix(sourceRoot, relative))
			if err != nil {
				return nil, err
			}
			text = data
		}

		sourceData, err := readFileRequired(joinPosix(sourceRoot, relative))
		if err != nil {
			return nil, err
		}
		sourceDocument, err := workspace.DecodeTOML([]byte(sourceData))
		if err != nil {
			return nil, fmt.Errorf("invalid TOML content: %w", err)
		}

		if !existing {
			text = newFileText(sourceDocument, items, text)
		}

		kinds := map[string]bool{}
		for _, item := range items {
			kinds[item.Kind] = true
		}

		if existing {
			for _, item := range items {
				switch item.Kind {
				case "config":
					parts := strings.Split(item.Name, ".")
					sections := parts[:len(parts)-1]
					key := parts[len(parts)-1]
					value, err := nestedTomlValue(sourceDocument, item.Name)
					if err != nil {
						return nil, err
					}
					text = AdoptField(text, sections, key, value)
				case "project-field":
					key := partitionAfterSlash(item.Name)
					value, ok := sourceDocument[key]
					if !ok {
						return nil, fmt.Errorf("missing source configuration field: %s", key)
					}
					text = AdoptField(text, nil, key, value)
				}
			}
		}

		if kinds["skill-selection"] || kinds["project-skill"] ||
			(!existing && kinds["project"] && sourceDocument["skills"] != nil) {
			names := map[string]bool{}
			for _, item := range items {
				switch item.Kind {
				case "skill-selection":
					names[item.Name] = true
				case "project-skill":
					names[partitionAfterSlash(item.Name)] = true
				}
			}
			existingSkills, err := listField(targetRoot, relative, "skills")
			if err != nil {
				return nil, err
			}
			for _, s := range existingSkills {
				names[s] = true
			}
			sorted := make([]string, 0, len(names))
			for n := range names {
				sorted = append(sorted, n)
			}
			sort.Strings(sorted)
			text = UpdateSkillsInToml(text, sorted)
		}

		if kinds["project-path"] {
			var paths []string
			for _, item := range items {
				if item.Kind == "project-path" {
					paths = append(paths, partitionAfterSlash(item.Name))
				}
			}
			sort.Strings(paths)
			for _, p := range paths {
				updated, changed, err := projectAddCandidatePath(text, p, home)
				if err != nil {
					return nil, err
				}
				if changed {
					text = updated
				}
			}
		}

		if _, err := workspace.DecodeTOML([]byte(text)); err != nil {
			return nil, fmt.Errorf("invalid merged TOML: %s: %w", relative, err)
		}
		result[relative] = text
	}
	return result, nil
}
