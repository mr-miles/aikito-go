package mcp

import (
	"sort"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// DecodeTOMLOrdered parses TOML text into an *OrderedObject tree (nested
// tables as *OrderedObject, arrays as []any) whose keys keep the document's
// order, as tomllib's dicts do. Values come from go-toml's decoder; the key
// order is recovered from its document-order parser.
func DecodeTOMLOrdered(text string) (*OrderedObject, error) {
	var m map[string]any
	if err := toml.Unmarshal([]byte(text), &m); err != nil {
		return nil, err
	}
	rec := tomlKeyOrder{order: map[string][]string{}, seen: map[string]bool{}, arrays: map[string]int{}}
	rec.scan([]byte(text))
	obj, _ := rec.toOrdered(m, nil).(*OrderedObject)
	if obj == nil {
		obj = NewOrderedObject()
	}
	return obj, nil
}

// tomlKeyOrder records, per table path, the order keys first appear in.
// Array-of-tables elements and inline tables inside arrays get "#<index>"
// path components.
type tomlKeyOrder struct {
	order  map[string][]string
	seen   map[string]bool
	arrays map[string]int
}

func tomlPathKey(parts []string) string { return strings.Join(parts, "\x00") }

func (r *tomlKeyOrder) add(path []string, key string) {
	id := tomlPathKey(path) + "\x01" + key
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	p := tomlPathKey(path)
	r.order[p] = append(r.order[p], key)
}

// addKey records a (possibly dotted) key under base and returns its full path.
func (r *tomlKeyOrder) addKey(base []string, it unstable.Iterator) []string {
	path := append([]string{}, base...)
	for it.Next() {
		k := string(it.Node().Data)
		r.add(path, k)
		path = append(path, k)
	}
	return path
}

func (r *tomlKeyOrder) value(path []string, n *unstable.Node) {
	switch n.Kind {
	case unstable.InlineTable:
		for it := n.Children(); it.Next(); {
			kv := it.Node()
			if kv.Kind == unstable.KeyValue {
				r.value(r.addKey(path, kv.Key()), kv.Value())
			}
		}
	case unstable.Array:
		i := 0
		for it := n.Children(); it.Next(); i++ {
			r.value(append(append([]string{}, path...), "#"+strconv.Itoa(i)), it.Node())
		}
	}
}

// headerPath records a [table] or [[array]] header's key and returns its
// path. A component that names an array of tables refers to its latest
// element (so [a.b] after [[a]] is inside the last a); for [[array]] the
// final component instead starts a new element.
func (r *tomlKeyOrder) headerPath(it unstable.Iterator, arrayTable bool) []string {
	var parts []string
	for it.Next() {
		parts = append(parts, string(it.Node().Data))
	}
	var path []string
	for i, k := range parts {
		r.add(path, k)
		path = append(path, k)
		key := tomlPathKey(path)
		if arrayTable && i == len(parts)-1 {
			idx := r.arrays[key]
			r.arrays[key] = idx + 1
			path = append(path, "#"+strconv.Itoa(idx))
		} else if n := r.arrays[key]; n > 0 {
			path = append(path, "#"+strconv.Itoa(n-1))
		}
	}
	return path
}

func (r *tomlKeyOrder) scan(data []byte) {
	var p unstable.Parser
	p.Reset(data)
	var current []string
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.KeyValue:
			r.value(r.addKey(current, e.Key()), e.Value())
		case unstable.Table:
			current = r.headerPath(e.Key(), false)
		case unstable.ArrayTable:
			current = r.headerPath(e.Key(), true)
		}
	}
}

func (r *tomlKeyOrder) toOrdered(v any, path []string) any {
	switch x := v.(type) {
	case map[string]any:
		obj := NewOrderedObject()
		for _, k := range r.order[tomlPathKey(path)] {
			if val, ok := x[k]; ok {
				obj.Set(k, r.toOrdered(val, append(append([]string{}, path...), k)))
			}
		}
		var rest []string
		for k := range x {
			if !obj.Has(k) {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range rest {
			obj.Set(k, r.toOrdered(x[k], append(append([]string{}, path...), k)))
		}
		return obj
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = r.toOrdered(item, append(append([]string{}, path...), "#"+strconv.Itoa(i)))
		}
		return out
	default:
		return v
	}
}
