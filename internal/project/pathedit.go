package project

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mr-miles/aikito-rs/internal/workspace"
)

// splitLinesNoEnds mirrors Python's str.splitlines() (no keepends): splits
// on \n, \r, \r\n, and the additional line-boundary code points Python
// recognizes, WITHOUT keeping terminators and WITHOUT producing a trailing
// empty element for a string ending in a line terminator (unlike a plain
// strings.Split(s, "\n"), which does produce that trailing empty element —
// the exact mismatch that first-cut this function's caller's line-insertion
// offsets).
func splitLinesNoEnds(s string) []string {
	var lines []string
	lineStart := 0
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch r {
		case '\r':
			next := i + size
			if next < len(s) && s[next] == '\n' {
				lines = append(lines, s[lineStart:i])
				i = next + 1
				lineStart = i
				continue
			}
			lines = append(lines, s[lineStart:i])
			i += size
			lineStart = i
			continue
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, s[lineStart:i])
			i += size
			lineStart = i
			continue
		}
		i += size
	}
	if lineStart < len(s) {
		lines = append(lines, s[lineStart:])
	}
	return lines
}

var sectionHeaderRe = regexp.MustCompile(`^\s*\[([a-zA-Z0-9_.\-]+)\]\s*(?:#.*)?$`)
var pathsTableAssignRe = regexp.MustCompile(`^\s*paths\s*=\s*\{`)
var pathsListAssignRe = regexp.MustCompile(`^\s*paths\s*=\s*\[`)
var pathAssignRe = regexp.MustCompile(`^\s*path\s*=`)

// jsonQuoteASCII mirrors Python's default json.dumps(s) (ensure_ascii=True,
// the default): standard JSON escaping for control chars/quote/backslash,
// plus \uXXXX-escaping (with surrogate pairs above the BMP) for every
// character outside printable ASCII. Used only for the literal string value
// add_candidate_path_to_content splices into TOML source text — distinct
// from internal/workspace's CanonicalJSON, which deliberately does NOT
// ASCII-escape (matching Python's ensure_ascii=False call sites instead).
func jsonQuoteASCII(s string) string {
	var b strings.Builder
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
			if r >= 0x20 && r <= 0x7e {
				b.WriteRune(r)
			} else if r > 0xFFFF {
				r2 := r - 0x10000
				hi := 0xD800 + (r2 >> 10)
				lo := 0xDC00 + (r2 & 0x3FF)
				fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// AddCandidatePathToContent mirrors project_config.py's
// add_candidate_path_to_content: a pure text transform that appends
// newRawPath as a new candidate path inside an agent.toml document's text,
// preserving everything else verbatim (comments, formatting, unrelated
// fields) via line-oriented surgical edits rather than a full TOML
// re-serialize. Returns (newContent, true) on success, ("", false) if
// newRawPath (or, when matchResolved, its resolved form) is already present
// — matching Python's None-return-means-"already present" contract.
// matchResolved=false keeps distinct raw collection members during import.
func AddCandidatePathToContent(content, newRawPath, home string, matchResolved bool) (string, bool, error) {
	data, err := workspace.DecodeTOML([]byte(content))
	if err != nil {
		return "", false, fmt.Errorf("Invalid TOML content: %w", err)
	}

	candidates := GetProjectCandidatePaths(data)
	newResolved, newResolvedOK := ResolveProjectPath(newRawPath, home)
	for _, c := range candidates {
		if c.raw == newRawPath {
			return "", false, nil
		}
		if matchResolved && newResolvedOK {
			if r, ok := ResolveProjectPath(c.raw, home); ok && r == newResolved {
				return "", false, nil
			}
		}
	}

	lines := splitLinesNoEnds(content)

	firstSectionIdx := len(lines)
	pathsSectionHeaderIdx := -1
	pathsSectionEndIdx := len(lines)

	for i, line := range lines {
		m := sectionHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		secName := m[1]
		if firstSectionIdx == len(lines) {
			firstSectionIdx = i
		}
		if secName == "paths" {
			pathsSectionHeaderIdx = i
		} else if pathsSectionHeaderIdx >= 0 && pathsSectionEndIdx == len(lines) {
			pathsSectionEndIdx = i
		}
	}

	newPathRepr := jsonQuoteASCII(newRawPath)

	pathsVal, hasPaths := data["paths"]
	pathsIsMap, _ := pathsVal.(map[string]any)
	pathsIsList, pathsIsListOK := pathsVal.([]any)
	_, pathHasAssignLine := firstAssignLine(lines, firstSectionIdx, pathAssignRe)

	switch {
	case pathsSectionHeaderIdx >= 0:
		existingKeys := map[string]bool{}
		if hasPaths {
			for k := range pathsIsMap {
				existingKeys[k] = true
			}
		}
		newKey := nextPathKey(existingKeys)
		insertLine := fmt.Sprintf("%s = %s", newKey, newPathRepr)
		lines = insertAt(lines, pathsSectionEndIdx, insertLine)

	case hasPaths && pathsIsMap != nil:
		existingKeys := map[string]bool{}
		for k := range pathsIsMap {
			existingKeys[k] = true
		}
		newKey := nextPathKey(existingKeys)
		replaced := false
		for i := 0; i < firstSectionIdx && i < len(lines); i++ {
			if !pathsTableAssignRe.MatchString(lines[i]) {
				continue
			}
			if rIdx := strings.LastIndex(lines[i], "}"); rIdx >= 0 {
				before := strings.TrimRight(lines[i][:rIdx], " \t")
				after := lines[i][rIdx:]
				sep := " "
				if before != "" && !strings.HasSuffix(before, "{") {
					sep = ", "
				}
				lines[i] = fmt.Sprintf("%s%s%s = %s %s", before, sep, newKey, newPathRepr, strings.TrimLeft(after, " \t"))
				replaced = true
				break
			}
			for j := i + 1; j < firstSectionIdx && j < len(lines); j++ {
				if strings.Contains(lines[j], "}") {
					lines = insertAt(lines, j, fmt.Sprintf("    %s = %s,", newKey, newPathRepr))
					replaced = true
					break
				}
			}
			if replaced {
				break
			}
		}
		if !replaced {
			lines = insertAt(lines, firstSectionIdx, fmt.Sprintf("[paths]\n%s = %s\n", newKey, newPathRepr))
		}

	case hasPaths && pathsIsListOK:
		replaced := false
		for i := 0; i < firstSectionIdx && i < len(lines); i++ {
			if !pathsListAssignRe.MatchString(lines[i]) {
				continue
			}
			if rIdx := strings.LastIndex(lines[i], "]"); rIdx >= 0 {
				before := strings.TrimRight(lines[i][:rIdx], " \t")
				after := lines[i][rIdx:]
				sep := " "
				if before != "" && !strings.HasSuffix(before, "[") {
					sep = ", "
				}
				lines[i] = fmt.Sprintf("%s%s%s%s", before, sep, newPathRepr, after)
				replaced = true
				break
			}
			for j := i + 1; j < len(lines); j++ {
				if strings.Contains(lines[j], "]") {
					lines = insertAt(lines, j, fmt.Sprintf("    %s,", newPathRepr))
					replaced = true
					break
				}
			}
			if replaced {
				break
			}
		}
		if !replaced {
			lines = insertAt(lines, firstSectionIdx, fmt.Sprintf("paths = [%s]", newPathRepr))
		}
		_ = pathsIsList // keep linters quiet about the unused type-assert form above

	case pathHasAssignLine:
		for i := 0; i < firstSectionIdx && i < len(lines); i++ {
			if !pathAssignRe.MatchString(lines[i]) {
				continue
			}
			oldVal := data["path"]
			var items []string
			if list, ok := oldVal.([]any); ok {
				for _, p := range list {
					if s, ok := p.(string); ok {
						items = append(items, jsonQuoteASCII(s))
					}
				}
			} else if s, ok := oldVal.(string); ok {
				items = append(items, jsonQuoteASCII(s))
			}
			items = append(items, newPathRepr)
			var b strings.Builder
			b.WriteString("paths = [\n")
			for _, item := range items {
				b.WriteString("    " + item + ",\n")
			}
			b.WriteString("]")
			lines[i] = b.String()
			break
		}

	default:
		entry := fmt.Sprintf("paths = [\n    %s,\n]\n", newPathRepr)
		if firstSectionIdx < len(lines) {
			lines = insertAt(lines, firstSectionIdx, entry)
		} else {
			lines = append(lines, entry)
		}
	}

	newContent := strings.TrimSpace(strings.Join(lines, "\n")) + "\n"

	verifiedData, err := workspace.DecodeTOML([]byte(newContent))
	if err != nil {
		return "", false, fmt.Errorf("Failed to generate valid TOML: %w\nGenerated:\n%s", err, newContent)
	}
	verifiedCandidates := GetProjectCandidatePaths(verifiedData)
	found := false
	for _, c := range verifiedCandidates {
		if c.raw == newRawPath {
			found = true
			break
		}
	}
	if !found {
		return "", false, fmt.Errorf("Failed to record new candidate path '%s'", newRawPath)
	}

	return newContent, true, nil
}

func firstAssignLine(lines []string, limit int, re *regexp.Regexp) (int, bool) {
	for i := 0; i < limit && i < len(lines); i++ {
		if re.MatchString(lines[i]) {
			return i, true
		}
	}
	return -1, false
}

func insertAt(lines []string, idx int, line string) []string {
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:idx]...)
	out = append(out, line)
	out = append(out, lines[idx:]...)
	return out
}

func nextPathKey(existing map[string]bool) string {
	idx := 1
	for existing[fmt.Sprintf("path_%d", idx)] {
		idx++
	}
	return "path_" + strconv.Itoa(idx)
}

// AddCandidatePath mirrors project_config.py's add_candidate_path: the byte
// version of AddCandidatePathToContent.
func AddCandidatePath(preBytes []byte, newRawPath, home string) ([]byte, bool, error) {
	newContent, changed, err := AddCandidatePathToContent(string(preBytes), newRawPath, home, true)
	if err != nil || !changed {
		return nil, changed, err
	}
	return []byte(newContent), true, nil
}

// AppendCandidatePathToConfig mirrors project_config.py's
// append_candidate_path_to_config: reads configPath, appends newRawPath if
// not already present, and writes the result back via a simple
// temp-file-then-rename atomic write. Returns whether a write happened.
//
// Note: this does not yet route through the shared workspace transaction
// engine (internal/workspace/transactions.go does not exist at the time
// this package was written) — once it does, this write should be migrated
// to go through it for crash-safety parity with the rest of the port, the
// same deferred-wiring note already left on layout.go's layout-marker
// writer.
func AppendCandidatePathToConfig(configPath, newRawPath, home string) (bool, error) {
	info, err := os.Stat(configPath)
	if err != nil || info.IsDir() {
		return false, fmt.Errorf("Project config file not found: %s", configPath)
	}
	preBytes, err := os.ReadFile(configPath)
	if err != nil {
		return false, fmt.Errorf("Failed to read %s: %w", configPath, err)
	}
	postBytes, changed, err := AddCandidatePath(preBytes, newRawPath, home)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := atomicWriteFile(configPath, postBytes); err != nil {
		return false, err
	}
	return true, nil
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
