package projectsync

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/mr-miles/aikito-go/internal/workspace"
)

// pyDumps is workspace.PyDumps (Python's json.dumps(v, indent=indent,
// sort_keys=True)).
func pyDumps(v any, indent int) string { return workspace.PyDumps(v, indent) }

// decodeJSON decodes like json.loads, keeping numbers as json.Number so int
// vs float stays visible.
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("Extra data")
	}
	return v, nil
}

// jsonEqual is Python dict/list equality for decoded JSON values.
func jsonEqual(a, b any) bool { return pyDumps(normalizeJSON(a), -1) == pyDumps(normalizeJSON(b), -1) }

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = normalizeJSON(x)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalizeJSON(x)
		}
		return out
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		return t
	case int:
		return int64(t)
	}
	return v
}
