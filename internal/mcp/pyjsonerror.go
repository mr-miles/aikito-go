package mcp

import (
	"fmt"
	"strings"
)

// PythonJSONDecodeError returns the message CPython's json.loads raises for
// text ("<msg>: line L column C (char N)", from the C _json scanner), or ""
// when CPython would accept it (including NaN/Infinity, which Go rejects).
// Positions count code points, as Python strings do.
func PythonJSONDecodeError(text string) string {
	p := pyJSONScanner{s: []rune(text)}
	return p.decode()
}

type pyJSONScanner struct {
	s []rune
}

type pyJSONError struct {
	msg string
	pos int
}

// stopIteration is the scanner's "no value here" signal, which callers turn
// into "Expecting value".
type stopIteration struct{ pos int }

func (p *pyJSONScanner) errmsg(e pyJSONError) string {
	line := 1
	lastNL := -1
	for i := 0; i < e.pos && i < len(p.s); i++ {
		if p.s[i] == '\n' {
			line++
			lastNL = i
		}
	}
	return fmt.Sprintf("%s: line %d column %d (char %d)", e.msg, line, e.pos-lastNL, e.pos)
}

func isJSONSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (p *pyJSONScanner) skipWS(i int) int {
	for i < len(p.s) && isJSONSpace(p.s[i]) {
		i++
	}
	return i
}

func (p *pyJSONScanner) at(i int) rune {
	if i >= 0 && i < len(p.s) {
		return p.s[i]
	}
	return 0
}

func (p *pyJSONScanner) decode() (msg string) {
	defer func() {
		if r := recover(); r != nil {
			switch e := r.(type) {
			case pyJSONError:
				msg = p.errmsg(e)
			case stopIteration:
				msg = p.errmsg(pyJSONError{"Expecting value", e.pos})
			default:
				panic(r)
			}
		}
	}()
	if len(p.s) > 0 && p.s[0] == 0xFEFF {
		return p.errmsg(pyJSONError{"Unexpected UTF-8 BOM (decode using utf-8-sig)", 0})
	}
	end := p.scanOnce(p.skipWS(0))
	end = p.skipWS(end)
	if end != len(p.s) {
		return p.errmsg(pyJSONError{"Extra data", end})
	}
	return ""
}

func (p *pyJSONScanner) hasPrefix(i int, lit string) bool {
	r := []rune(lit)
	if i+len(r) > len(p.s) {
		return false
	}
	for j, c := range r {
		if p.s[i+j] != c {
			return false
		}
	}
	return true
}

// scanOnce is scan_once_unicode: returns the index after the value.
func (p *pyJSONScanner) scanOnce(i int) int {
	if i >= len(p.s) {
		panic(stopIteration{i})
	}
	switch p.s[i] {
	case '"':
		return p.scanString(i + 1)
	case '{':
		return p.parseObject(i + 1)
	case '[':
		return p.parseArray(i + 1)
	}
	for _, lit := range []string{"null", "true", "false", "NaN", "Infinity", "-Infinity"} {
		if p.hasPrefix(i, lit) {
			return i + len(lit)
		}
	}
	return p.matchNumber(i)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// matchNumber is _match_number_unicode.
func (p *pyJSONScanner) matchNumber(start int) int {
	i := start
	if p.at(i) == '-' {
		i++
		if i >= len(p.s) {
			panic(stopIteration{start})
		}
	}
	switch c := p.at(i); {
	case c >= '1' && c <= '9':
		i++
		for i < len(p.s) && isDigit(p.s[i]) {
			i++
		}
	case c == '0':
		i++
	default:
		panic(stopIteration{start})
	}
	if i < len(p.s)-1 && p.s[i] == '.' && isDigit(p.s[i+1]) {
		i += 2
		for i < len(p.s) && isDigit(p.s[i]) {
			i++
		}
	}
	if i < len(p.s)-1 && (p.s[i] == 'e' || p.s[i] == 'E') {
		eStart := i
		i++
		if i < len(p.s)-1 && (p.s[i] == '-' || p.s[i] == '+') {
			i++
		}
		for i < len(p.s) && isDigit(p.s[i]) {
			i++
		}
		if !isDigit(p.s[i-1]) {
			i = eStart
		}
	}
	return i
}

func isHex(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// scanString is scanstring_unicode (strict): end is the index after the
// opening quote.
func (p *pyJSONScanner) scanString(end int) int {
	begin := end - 1
	n := len(p.s)
	for {
		next := end
		var c rune
		for ; next < n; next++ {
			c = p.s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				panic(pyJSONError{"Invalid control character at", next})
			}
		}
		if next >= n {
			panic(pyJSONError{"Unterminated string starting at", begin})
		}
		next++
		if c == '"' {
			return next
		}
		if next == n {
			panic(pyJSONError{"Unterminated string starting at", begin})
		}
		c = p.s[next]
		if c != 'u' {
			end = next + 1
			if !strings.ContainsRune(`"\/bfnrt`, c) {
				panic(pyJSONError{"Invalid \\escape", end - 2})
			}
			continue
		}
		next++
		end = next + 4
		if end >= n {
			panic(pyJSONError{"Invalid \\uXXXX escape", next - 1})
		}
		var v rune
		for ; next < end; next++ {
			if !isHex(p.s[next]) {
				panic(pyJSONError{"Invalid \\uXXXX escape", end - 5})
			}
			v = v<<4 | hexVal(p.s[next])
		}
		if v >= 0xd800 && v <= 0xdbff && end+6 < n && p.s[next] == '\\' && p.s[next+1] == 'u' {
			next += 2
			end += 6
			var v2 rune
			for ; next < end; next++ {
				if !isHex(p.s[next]) {
					panic(pyJSONError{"Invalid \\uXXXX escape", end - 5})
				}
				v2 = v2<<4 | hexVal(p.s[next])
			}
			if v2 < 0xdc00 || v2 > 0xdfff {
				end -= 6
			}
		}
	}
}

func hexVal(r rune) rune {
	switch {
	case isDigit(r):
		return r - '0'
	case r >= 'a' && r <= 'f':
		return r - 'a' + 10
	default:
		return r - 'A' + 10
	}
}

// parseObject is _parse_object_unicode: i is the index after '{'.
func (p *pyJSONScanner) parseObject(i int) int {
	i = p.skipWS(i)
	if i < len(p.s) && p.s[i] == '}' {
		return i + 1
	}
	for {
		if i >= len(p.s) || p.s[i] != '"' {
			panic(pyJSONError{"Expecting property name enclosed in double quotes", i})
		}
		i = p.skipWS(p.scanString(i + 1))
		if i >= len(p.s) || p.s[i] != ':' {
			panic(pyJSONError{"Expecting ':' delimiter", i})
		}
		i = p.skipWS(i + 1)
		i = p.scanValue(i)
		i = p.skipWS(i)
		if i < len(p.s) && p.s[i] == '}' {
			return i + 1
		}
		if i >= len(p.s) || p.s[i] != ',' {
			panic(pyJSONError{"Expecting ',' delimiter", i})
		}
		comma := i
		i = p.skipWS(i + 1)
		if i < len(p.s) && p.s[i] == '}' {
			panic(pyJSONError{"Illegal trailing comma before end of object", comma})
		}
	}
}

// parseArray is _parse_array_unicode: i is the index after '['.
func (p *pyJSONScanner) parseArray(i int) int {
	i = p.skipWS(i)
	if i < len(p.s) && p.s[i] == ']' {
		return i + 1
	}
	for {
		i = p.scanValue(i)
		i = p.skipWS(i)
		if i < len(p.s) && p.s[i] == ']' {
			return i + 1
		}
		if i >= len(p.s) || p.s[i] != ',' {
			panic(pyJSONError{"Expecting ',' delimiter", i})
		}
		comma := i
		i = p.skipWS(i + 1)
		if i < len(p.s) && p.s[i] == ']' {
			panic(pyJSONError{"Illegal trailing comma before end of array", comma})
		}
	}
}

// scanValue scans a nested value; a missing one is "Expecting value".
func (p *pyJSONScanner) scanValue(i int) int {
	defer func() {
		if r := recover(); r != nil {
			if s, ok := r.(stopIteration); ok {
				panic(pyJSONError{"Expecting value", s.pos})
			}
			panic(r)
		}
	}()
	return p.scanOnce(i)
}
