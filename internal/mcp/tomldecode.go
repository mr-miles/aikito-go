package mcp

import toml "github.com/pelletier/go-toml/v2"

// DecodeTOMLOrdered parses TOML text into an *OrderedObject tree (nested
// tables as *OrderedObject, arrays as []any). Read-only use only in this
// package (get_toml_server-style lookups by key, never a full-document
// reserialize) so the loss of the TOML file's own key order during
// go-toml/v2's map[string]any decode is immaterial: Python dict equality
// and key-based lookups are both order-independent, and nothing in this
// package re-emits a TOML document as a whole — writes are always pure text
// surgery (toml.go) operating on the original text, never on this decoded
// value.
func DecodeTOMLOrdered(text string) (*OrderedObject, error) {
	var m map[string]any
	if err := toml.Unmarshal([]byte(text), &m); err != nil {
		return nil, err
	}
	obj, _ := anyToOrdered(m).(*OrderedObject)
	if obj == nil {
		obj = NewOrderedObject()
	}
	return obj, nil
}

func anyToOrdered(v any) any {
	switch x := v.(type) {
	case map[string]any:
		obj := NewOrderedObject()
		for k, val := range x {
			obj.Set(k, anyToOrdered(val))
		}
		return obj
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = anyToOrdered(item)
		}
		return out
	default:
		return v
	}
}
