package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StateEntry is one entry of the MCP state file's "entries" map (keyed by
// AgentSpec.StateKey(): "<agent>:<server>").
type StateEntry struct {
	Fingerprint string `json:"fingerprint"`
	ConfigPath  string `json:"config_path"`
	TargetName  string `json:"target_name"`
}

// MCPState mirrors the on-disk shape of .local/state/aikito/mcp-state.json.
type MCPState struct {
	Version int                   `json:"version"`
	Entries map[string]StateEntry `json:"entries"`
}

// LoadState mirrors executor.py's _load_state: version==1 with entries
// missing/absent is treated as a fresh empty state; any other malformed
// shape is an error.
func LoadState(home string) (MCPState, error) {
	path := filepath.Join(home, StateFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return MCPState{Version: StateVersion, Entries: map[string]StateEntry{}}, nil
	}
	if err != nil {
		return MCPState{}, configErrorf("Cannot read MCP state file %s: %v", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return MCPState{}, configErrorf("Cannot read MCP state file %s: %v", path, err)
	}
	var version int
	if v, ok := raw["version"]; ok {
		_ = json.Unmarshal(v, &version)
	}
	entries := map[string]StateEntry{}
	if e, ok := raw["entries"]; ok {
		if err := json.Unmarshal(e, &entries); err != nil {
			return MCPState{}, configErrorf("Unsupported MCP state file: %s", path)
		}
	} else {
		return MCPState{}, configErrorf("Unsupported MCP state file: %s", path)
	}
	if version != StateVersion {
		return MCPState{}, configErrorf("Unsupported MCP state file: %s", path)
	}
	return MCPState{Version: version, Entries: entries}, nil
}

// stateFileHash mirrors _state_file_hash exactly: "absent" if the file
// doesn't exist, sha256 hex of its bytes otherwise, "error" on read failure.
func stateFileHash(path string) string {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		return "error"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// renderStateJSON mirrors json.dumps(state, ensure_ascii=False, indent=2,
// sort_keys=True) + "\n" for the MCPState shape (only "version" and sorted
// "entries" keys, each entry's 3 fields in the fixed
// fingerprint/config_path/target_name order — matching dataclass field
// declaration order, which is also alphabetical here, so sort_keys=True
// produces the same order either way).
func renderStateJSON(state MCPState) string {
	obj := map[string]any{"version": int64(state.Version)}
	entries := map[string]any{}
	for k, v := range state.Entries {
		entries[k] = map[string]any{
			"config_path": v.ConfigPath,
			"fingerprint": v.Fingerprint,
			"target_name": v.TargetName,
		}
	}
	obj["entries"] = entries
	return pythonJSONDumpsIndent2SortKeys(obj) + "\n"
}

// pythonJSONDumpsIndent2SortKeys renders a map[string]any/[]any/scalar tree
// the way Python's json.dumps(value, ensure_ascii=False, indent=2,
// sort_keys=True) does: keys sorted at every level, 2-space indent, each
// item on its own line, empty object/array inline as "{}"/"[]". Reuses
// writeScalarJSON/writeJSONStringPy from orderedjson.go for leaf rendering.
func pythonJSONDumpsIndent2SortKeys(value any) string {
	var b strings.Builder
	writeIndentedSorted(&b, value, "")
	return b.String()
}

func writeIndentedSorted(b *strings.Builder, value any, indent string) {
	switch x := value.(type) {
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		childIndent := indent + "  "
		b.WriteString("{\n")
		for i, k := range keys {
			b.WriteString(childIndent)
			writeJSONStringPy(b, k)
			b.WriteString(": ")
			writeIndentedSorted(b, x[k], childIndent)
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent)
		b.WriteString("}")
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return
		}
		childIndent := indent + "  "
		b.WriteString("[\n")
		for i, item := range x {
			b.WriteString(childIndent)
			writeIndentedSorted(b, item, childIndent)
			if i < len(x)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent)
		b.WriteString("]")
	default:
		writeScalarJSON(b, value)
	}
}
