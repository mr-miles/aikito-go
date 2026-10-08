package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// DuplicateKeyError is raised by DecodeStrictJSON's object_pairs_hook
// equivalent: an object repeated a key. Like Python's hook it fires when
// the object closes, after its values have been parsed.
type DuplicateKeyError struct{ Key string }

func (e *DuplicateKeyError) Error() string { return "duplicate object key: " + e.Key }

// ConstantError is raised by DecodeStrictJSON's parse_constant equivalent:
// a NaN, Infinity or -Infinity literal, reported as soon as it is read.
type ConstantError struct{ Constant string }

func (e *ConstantError) Error() string { return "unsupported constant " + e.Constant }

// DecodeStrictJSON parses a single JSON value as Python's
// json.loads(text, object_pairs_hook=<reject duplicates>,
// parse_constant=<reject>) does, scanning in the same order so the first
// problem found is the one Python reports: a *ConstantError, a
// *DuplicateKeyError, or a syntax error. Numbers decode as json.Number so
// callers can tell integer from float literals as Python's int/float does.
func DecodeStrictJSON(text string) (any, error) {
	p := &strictParser{s: text}
	p.ws()
	val, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, fmt.Errorf("trailing data after JSON value at char %d", p.i)
	}
	return val, nil
}

type strictParser struct {
	s string
	i int
}

var errStrictSyntax = errors.New("invalid JSON")

func (p *strictParser) syntax() error {
	return fmt.Errorf("%w at char %d", errStrictSyntax, p.i)
}

// ws skips json.loads' whitespace: space, tab, newline, carriage return.
func (p *strictParser) ws() {
	for p.i < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *strictParser) value() (any, error) {
	if p.i >= len(p.s) {
		return nil, p.syntax()
	}
	rest := p.s[p.i:]
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		return p.str()
	case strings.HasPrefix(rest, "null"):
		p.i += 4
		return nil, nil
	case strings.HasPrefix(rest, "true"):
		p.i += 4
		return true, nil
	case strings.HasPrefix(rest, "false"):
		p.i += 5
		return false, nil
	case strings.HasPrefix(rest, "NaN"):
		return nil, &ConstantError{"NaN"}
	case strings.HasPrefix(rest, "Infinity"):
		return nil, &ConstantError{"Infinity"}
	case strings.HasPrefix(rest, "-Infinity"):
		return nil, &ConstantError{"-Infinity"}
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.syntax()
}

func (p *strictParser) object() (any, error) {
	p.i++ // '{'
	obj := map[string]any{}
	dup := ""
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return obj, nil
	}
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.syntax()
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		key := k.(string)
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.syntax()
		}
		p.i++
		p.ws()
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		if _, exists := obj[key]; exists && dup == "" {
			dup = key
		}
		obj[key] = val
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			break
		}
		return nil, p.syntax()
	}
	if dup != "" {
		return nil, &DuplicateKeyError{dup}
	}
	return obj, nil
}

func (p *strictParser) array() (any, error) {
	p.i++ // '['
	arr := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return arr, nil
	}
	for {
		p.ws()
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return arr, nil
		}
		return nil, p.syntax()
	}
}

// str scans a string literal and decodes it with encoding/json, which
// applies the same escape rules and rejects raw control characters as
// json.loads' strict mode does.
func (p *strictParser) str() (any, error) {
	start := p.i
	p.i++
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '\\':
			p.i += 2
			continue
		case '"':
			p.i++
			var out string
			if err := json.Unmarshal([]byte(p.s[start:p.i]), &out); err != nil {
				p.i = start
				return nil, p.syntax()
			}
			return out, nil
		}
		p.i++
	}
	p.i = start
	return nil, p.syntax()
}

// number scans json.loads' number grammar: -?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?
func (p *strictParser) number() (any, error) {
	start := p.i
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.s) && p.s[p.i] == '0' {
		p.i++
	} else if digits() == 0 {
		p.i = start
		return nil, p.syntax()
	}
	if p.i+1 < len(p.s) && p.s[p.i] == '.' && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		p.i++
		digits()
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		save := p.i
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			p.i = save
		}
	}
	return json.Number(p.s[start:p.i]), nil
}
