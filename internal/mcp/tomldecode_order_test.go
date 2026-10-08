package mcp

import (
	"encoding/json"
	"os"
	"testing"
)

func orderedKeys(v any) any {
	switch x := v.(type) {
	case *OrderedObject:
		out := []any{}
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			out = append(out, []any{k, orderedKeys(val)})
		}
		return out
	case []any:
		out := []any{}
		for _, item := range x {
			out = append(out, orderedKeys(item))
		}
		return out
	}
	return nil
}

// toml_order_vectors.json holds tomllib's key order for each document
// (testdata/gen_toml_order_vectors.py). DecodeTOMLOrdered must match it.
func TestDecodeTOMLOrderedKeepsDocumentOrder(t *testing.T) {
	data, err := os.ReadFile("testdata/toml_order_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Doc  string          `json:"doc"`
		Keys json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		obj, err := DecodeTOMLOrdered(c.Doc)
		if err != nil {
			t.Fatalf("%q: %v", c.Doc, err)
		}
		got, _ := json.Marshal(orderedKeys(obj))
		var want any
		_ = json.Unmarshal(c.Keys, &want)
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("%q:\n got %s\nwant %s", c.Doc, got, wantJSON)
		}
	}
}
