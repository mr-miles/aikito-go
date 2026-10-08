// Package mcp ports aikito's MCP server config subsystem: the per-agent
// native config file adapters (TOML, JSONC, plain JSON, Cordis-patch YAML),
// their domain model, and credential redaction. This file (part one) is
// pure parsing/rendering/formatting logic — no process-shelling, no
// transactional apply, no planner state machine (that's a follow-up task).
package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/mr-miles/aikito-go/internal/compat"
)

// OrderedObject is a JSON object that preserves Python dict semantics
// exactly: insertion order is preserved, and re-setting an existing key
// updates its value in place without moving it to the end. This matters
// throughout this package because several adapters fully re-parse, mutate,
// and re-serialize a whole JSON document (agy/claude/copilot JSON), and
// Python's json.dumps(document, indent=2) reproduces the document's
// original key order verbatim — a plain Go map[string]any would silently
// randomize that order and produce a non-matching, non-deterministic
// rewrite of the user's config file.
type OrderedObject struct {
	keys []string
	vals map[string]any
}

// NewOrderedObject returns an empty ordered object.
func NewOrderedObject() *OrderedObject {
	return &OrderedObject{vals: map[string]any{}}
}

// OO builds an OrderedObject from alternating key/value arguments, in the
// given order — the Go equivalent of a Python dict literal {"a": 1, "b": 2},
// used throughout the BuildDesired functions to mirror their exact
// insertion-order construction.
func OO(kv ...any) *OrderedObject {
	o := NewOrderedObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Set inserts key at the end if new, or updates its value in place
// (keeping its existing position) if key is already present — exactly
// Python's dict[key] = value semantics.
func (o *OrderedObject) Set(key string, val any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

func (o *OrderedObject) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// GetOr returns the value for key, or fallback if absent — Python's
// dict.get(key, fallback).
func (o *OrderedObject) GetOr(key string, fallback any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return fallback
}

func (o *OrderedObject) Has(key string) bool {
	if o == nil {
		return false
	}
	_, ok := o.vals[key]
	return ok
}

func (o *OrderedObject) Delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Keys returns the object's keys in insertion order. The returned slice is
// a copy; callers may not mutate the object through it.
func (o *OrderedObject) Keys() []string {
	if o == nil {
		return nil
	}
	out := make([]string, len(o.keys))
	copy(out, o.keys)
	return out
}

func (o *OrderedObject) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Clone returns a deep copy (nested *OrderedObject and []any values are
// also cloned; scalars are copied by value).
func (o *OrderedObject) Clone() *OrderedObject {
	if o == nil {
		return nil
	}
	c := NewOrderedObject()
	for _, k := range o.keys {
		c.Set(k, cloneJSONValue(o.vals[k]))
	}
	return c
}

func cloneJSONValue(v any) any {
	switch x := v.(type) {
	case *OrderedObject:
		return x.Clone()
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneJSONValue(item)
		}
		return out
	default:
		return v
	}
}

// --- Order-preserving JSON parsing (mirrors plain json.loads: lenient on
// duplicate keys — last value wins, original position kept — unlike
// workspace.DecodeStrictJSON's duplicate-rejecting frontmatter parser) ---

// ParseJSONOrdered decodes JSON text into nil/bool/string/int64/float64/
// []any/*OrderedObject, preserving object key insertion order.
func ParseJSONOrdered(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	val, err := decodeOrderedValue(dec)
	if err != nil {
		return nil, err
	}
	// dec.More() is false before a stray '}' or ']', so require EOF instead.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected content after JSON document")
	}
	return val, nil
}

func decodeOrderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := NewOrderedObject()
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return nil, fmt.Errorf("expected string object key")
				}
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(key, val)
			}
			if _, err := dec.Token(); err != nil { // consume closing '}'
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // consume closing ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %v", t)
		}
	case json.Number:
		return jsonNumberToGo(t), nil
	case nil, bool, string:
		return t, nil
	default:
		return nil, fmt.Errorf("unexpected JSON token %v", tok)
	}
}

// jsonNumberToGo mirrors Python json.loads' int/float split: an integer
// literal (no '.', 'e', or 'E') decodes as int64, else float64.
func jsonNumberToGo(n json.Number) any {
	s := string(n)
	if !strings.ContainsAny(s, ".eE") {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
	}
	f, _ := n.Float64()
	return f
}

// --- Python-style json.dumps(..., indent=2, ensure_ascii=False) rendering ---

// DumpIndented renders value the way Python's json.dumps(value, indent=2,
// ensure_ascii=False) does: 2-space indent, ", " item/": " key separators
// collapsed to "," and ":" at line ends (i.e. each item/key-value pair on
// its own line), empty object/array as "{}"/"[]" inline, insertion order
// preserved for *OrderedObject.
func DumpIndented(value any) string {
	var b strings.Builder
	writeIndented(&b, value, "")
	return b.String()
}

func writeIndented(b *strings.Builder, value any, indent string) {
	switch x := value.(type) {
	case *OrderedObject:
		if x.Len() == 0 {
			b.WriteString("{}")
			return
		}
		childIndent := indent + "  "
		b.WriteString("{\n")
		keys := x.Keys()
		for i, k := range keys {
			b.WriteString(childIndent)
			writeJSONStringPy(b, k)
			b.WriteString(": ")
			v, _ := x.Get(k)
			writeIndented(b, v, childIndent)
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
			writeIndented(b, item, childIndent)
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

func writeScalarJSON(b *strings.Builder, value any) {
	switch x := value.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJSONStringPy(b, x)
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(formatPyFloat(x))
	default:
		panic(fmt.Sprintf("mcp: unsupported JSON value type %T", value))
	}
}

// writeJSONStringPy mirrors Python's json string escaping with
// ensure_ascii=False (same rules as workspace.CanonicalJSON's encoder,
// duplicated here to keep this package dependency-free of workspace's
// internals): control chars \u-escaped (with \b\t\n\f\r shorthand), '"' and
// '\\' escaped, everything else (incl. 0x7F and all non-ASCII) literal.
func writeJSONStringPy(b *strings.Builder, s string) {
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

func formatPyFloat(f float64) string {
	return compat.PyFloatRepr(f)
}

// --- Canonical compact sorted-key JSON (mcp/adapters/__init__.py's
// _fingerprint: json.dumps(value, ensure_ascii=False, sort_keys=True,
// separators=(",", ":"))) ---

// DumpCompactSorted renders value as compact JSON with keys sorted at every
// level — the exact shape _fingerprint hashes.
func DumpCompactSorted(value any) string {
	var b strings.Builder
	writeCompactSorted(&b, value)
	return b.String()
}

func writeCompactSorted(b *strings.Builder, value any) {
	switch x := value.(type) {
	case *OrderedObject:
		keys := x.Keys()
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONStringPy(b, k)
			b.WriteByte(':')
			v, _ := x.Get(k)
			writeCompactSorted(b, v)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCompactSorted(b, item)
		}
		b.WriteByte(']')
	default:
		writeScalarJSON(b, value)
	}
}

// --- Order-independent deep equality (Python dict/list/scalar ==) ---

// JSONEqual compares two decoded JSON values the way Python's == does for
// dict/list/scalar trees: object equality ignores key order, and an int64
// compares equal to a float64 of the same numeric value (matching Python's
// `1 == 1.0`).
func JSONEqual(a, b any) bool {
	switch av := a.(type) {
	case *OrderedObject:
		bv, ok := b.(*OrderedObject)
		if !ok || av.Len() != bv.Len() {
			return false
		}
		for _, k := range av.Keys() {
			bval, ok := bv.Get(k)
			if !ok {
				return false
			}
			aval, _ := av.Get(k)
			if !JSONEqual(aval, bval) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !JSONEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case int64:
		return numEqual(float64(av), b)
	case float64:
		return numEqual(av, b)
	default:
		return a == b
	}
}

func numEqual(f float64, b any) bool {
	switch bv := b.(type) {
	case int64:
		return f == float64(bv)
	case float64:
		return f == bv
	default:
		return false
	}
}
