// Package workspace implements the aikito workspace's resource model:
// identity, fingerprinting, scanning, and the atomic write pipeline.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// --- Ignored filesystem noise (workspace/resources.py IGNORED_NAMES / is_ignored_name) ---

const SkillExecutableMetadataFilename = ".aikito-executable.json"

var ignoredNames = map[string]struct{}{
	".DS_Store":     {},
	"Thumbs.db":     {},
	"desktop.ini":   {},
	"__pycache__":   {},
	".pytest_cache": {},
	".ruff_cache":   {},
}

// IsIgnoredName mirrors Python's is_ignored_name: these entries are never
// treated as resource content, anywhere they're encountered.
func IsIgnoredName(name string) bool {
	if _, ok := ignoredNames[name]; ok {
		return true
	}
	if name == SkillExecutableMetadataFilename {
		return true
	}
	if strings.HasPrefix(name, "._") {
		return true
	}
	if strings.HasSuffix(name, ".pyc") {
		return true
	}
	return false
}

// LocalOnlyNames are host-local top-level workspace entries, skipped
// silently (not even reported) by the scanner.
var LocalOnlyNames = map[string]struct{}{".git": {}, ".local": {}}

// --- value_fingerprint: semantic TOML/JSON value hashing (resources.py:222) ---

// ValueFingerprint hashes the parsed value (not source bytes): sha256 hex of
// CanonicalJSON(v). Non-JSON-native TOML date/time values are wrapped exactly
// as Python's json.dumps(..., default=...) fallback does.
func ValueFingerprint(v any) string {
	sum := sha256.Sum256([]byte(CanonicalJSON(v)))
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON renders v the way Python's
// json.dumps(v, sort_keys=True, ensure_ascii=False, default=<toml-type wrapper>)
// renders it: default separators (", " and ": "), sorted object keys, raw
// UTF-8 for non-ASCII, and a {"toml-type": ..., "value": ...} wrapper for
// TOML date/time values (via go-toml/v2's Local* types and time.Time).
//
// Known limitation: Python float repr switches to scientific notation at
// different magnitude thresholds than Go's shortest-round-trip formatter for
// floats roughly >= 1e16 and < 1e21; such values are vanishingly unlikely in
// TOML/MCP config values but would not byte-match Python in that narrow band.
func CanonicalJSON(v any) string {
	var b strings.Builder
	encodeCanonical(&b, v)
	return b.String()
}

func encodeCanonical(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		encodeJSONString(b, x, false)
	case json.Number:
		s := string(x)
		if !strings.ContainsAny(s, ".eE") {
			b.WriteString(s)
		} else {
			f, _ := x.Float64()
			b.WriteString(formatPythonFloat(f))
		}
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(formatPythonFloat(x))
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			encodeCanonical(b, item)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			encodeJSONString(b, k, false)
			b.WriteString(": ")
			encodeCanonical(b, x[k])
		}
		b.WriteByte('}')
	case toml.LocalDate:
		encodeCanonical(b, tomlTypeWrapper("date", formatLocalDate(x)))
	case toml.LocalTime:
		encodeCanonical(b, tomlTypeWrapper("time", formatLocalTime(x)))
	case toml.LocalDateTime:
		encodeCanonical(b, tomlTypeWrapper("datetime", formatLocalDate(x.LocalDate)+" "+formatLocalTime(x.LocalTime)))
	case time.Time:
		encodeCanonical(b, tomlTypeWrapper("datetime", formatOffsetDateTime(x)))
	default:
		panic(fmt.Sprintf("workspace.CanonicalJSON: unsupported value type %T", v))
	}
}

func tomlTypeWrapper(typeName, value string) map[string]any {
	return map[string]any{"toml-type": typeName, "value": value}
}

func formatLocalDate(d toml.LocalDate) string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

func formatLocalTime(t toml.LocalTime) string {
	micro := t.Nanosecond / 1000
	if micro == 0 {
		return fmt.Sprintf("%02d:%02d:%02d", t.Hour, t.Minute, t.Second)
	}
	return fmt.Sprintf("%02d:%02d:%02d.%06d", t.Hour, t.Minute, t.Second, micro)
}

// formatOffsetDateTime mirrors Python's str(aware_datetime): date and time
// separated by a space (not "T"), offset as sign+HH:MM(:SS), no "Z" form.
func formatOffsetDateTime(t time.Time) string {
	datePart := fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
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
	var offStr string
	if secs == 0 {
		offStr = fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
	} else {
		offStr = fmt.Sprintf("%s%02d:%02d:%02d", sign, hours, minutes, secs)
	}
	return datePart + " " + timePart + offStr
}

// formatPythonFloat approximates Python's repr(float) / json.dumps float
// rendering: a decimal point is always present (bare integers get ".0"), and
// nan/inf render as the non-standard-JSON tokens Python emits by default.
func formatPythonFloat(f float64) string {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa, exp := s[:i], s[i+1:]
		sign := "+"
		if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
			sign = string(exp[0])
			exp = exp[1:]
		}
		for len(exp) < 2 {
			exp = "0" + exp
		}
		return mantissa + "e" + sign + exp
	}
	if !strings.ContainsRune(s, '.') {
		s += ".0"
	}
	return s
}

// encodeJSONString replicates Python's json string escaping with
// ensure_ascii=False: control chars (<0x20) are \u-escaped (with shorthand
// for \b\t\n\f\r), '"' and '\\' are escaped, and everything else — including
// 0x7F and all non-ASCII, U+2028/U+2029 included — is emitted as literal
// UTF-8 bytes. (Confirmed empirically against the real Python encoder: its
// repr() display shows   for readability, but the actual encoded bytes
// are raw UTF-8 — there is no special-case escaping of line/paragraph
// separators when ensure_ascii=False.) The compact flag is accepted for
// call-site symmetry but does not affect string escaping (only separators
// differ between the default and compact JSON dump modes in Python).
func encodeJSONString(b *strings.Builder, s string, _ bool) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// DecodeJSONPreservingNumbers decodes JSON text into Go values the way this
// package's fingerprint functions expect: objects become map[string]any,
// arrays []any, and numbers become json.Number (so CanonicalJSON can tell an
// integer literal from a float literal exactly as Python's json.loads does).
func DecodeJSONPreservingNumbers(data []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// --- Content fingerprints for standalone (non-shared) resources (resources.py:436) ---

// FileDigest is the raw sha256 hex of a file's bytes.
func FileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// FileDigestBytes is the raw sha256 hex of in-memory bytes (used when
// content is already loaded, e.g. before deciding whether to write it).
func FileDigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TreeDigest computes a skill directory's content-tree fingerprint
// (resources.py tree_digest/_collect_tree): walk the tree; emit
// "f <relpath> <sha256>" for every file and "d <relpath>" for every leaf
// empty directory (not the root, and not a directory that merely contains
// only further-empty subdirectories, which themselves get their own "d"
// line); sort all emitted lines lexicographically; join with "\n"; sha256
// the UTF-8 bytes. A symlink anywhere in the tree is refused (matches the
// Python scanner recording an "unsafe-entry" finding and the whole
// tree_digest call returning None).
func TreeDigest(root string) (string, error) {
	var entries []string
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		raw, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(raw))
		byName := map[string]os.DirEntry{}
		for _, e := range raw {
			if IsIgnoredName(e.Name()) {
				continue
			}
			names = append(names, e.Name())
			byName[e.Name()] = e
		}
		sort.Strings(names)
		if len(names) == 0 {
			if rel != "" {
				entries = append(entries, "d "+rel)
			}
			return nil
		}
		for _, name := range names {
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			full := filepath.Join(dir, name)
			info, err := os.Lstat(full)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("unsafe entry (symlink) in skill tree: %s", childRel)
			}
			if byName[name].IsDir() {
				if err := walk(full, childRel); err != nil {
					return err
				}
				continue
			}
			digest, err := FileDigest(full)
			if err != nil {
				return err
			}
			entries = append(entries, "f "+childRel+" "+digest)
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return "", err
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// ExecutableSetFingerprint hashes the sorted set of relative paths marked
// executable (skill_metadata.py executable_fingerprint): a compact JSON
// array (separators "," and ":", no spaces), sorted, ensure_ascii=False.
func ExecutableSetFingerprint(paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		encodeJSONString(&b, p, true)
	}
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
