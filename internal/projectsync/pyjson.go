package projectsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// pyDumps renders v like Python's json.dumps(v, indent=indent,
// sort_keys=True) with the default ensure_ascii=True. indent < 0 means
// json.dumps(v, sort_keys=True) (compact separators ", " and ": ").
// Supported values: map[string]any, []any, []string, string, bool, int,
// int64, json.Number, nil.
func pyDumps(v any, indent int) string {
	var b strings.Builder
	writePyJSON(&b, v, indent, 0)
	return b.String()
}

func writePyJSON(b *strings.Builder, v any, indent, depth int) {
	nl := func(d int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*d))
		}
	}
	itemSep := ", "
	if indent >= 0 {
		itemSep = ","
	}
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case json.Number:
		b.WriteString(t.String())
	case string:
		writePyString(b, t)
	case []string:
		items := make([]any, len(t))
		for i, s := range t {
			items[i] = s
		}
		writePyJSON(b, items, indent, depth)
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(itemSep)
			}
			nl(depth + 1)
			writePyJSON(b, item, indent, depth+1)
		}
		nl(depth)
		b.WriteByte(']')
	case map[string]any:
		if len(t) == 0 {
			b.WriteString("{}")
			return
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(itemSep)
			}
			nl(depth + 1)
			writePyString(b, k)
			b.WriteString(": ")
			writePyJSON(b, t[k], indent, depth+1)
		}
		nl(depth)
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("pyDumps: unsupported type %T", v))
	}
}

func writePyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r > 0x7e {
				if r > 0xffff {
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

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
