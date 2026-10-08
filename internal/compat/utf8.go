package compat

import (
	"strings"
	"unicode/utf8"
)

// DecodeUTF8Replace is CPython's bytes.decode("utf-8", "replace"): each
// maximal invalid subpart becomes one U+FFFD (Unicode's recommended
// practice). A stray byte gives one U+FFFD per byte, but a truncated
// sequence such as E2 82 gives a single one. strings.ToValidUTF8 instead
// collapses a whole run of invalid bytes into one replacement.
func DecodeUTF8Replace(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data) + 8)
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r != utf8.RuneError || size > 1 {
			b.Write(data[i : i+size])
			i += size
			continue
		}
		b.WriteRune(utf8.RuneError)
		i += maximalSubpart(data[i:])
	}
	return b.String()
}

// maximalSubpart returns how many bytes, starting at an invalid position,
// form the longest prefix of a well-formed sequence (at least 1).
func maximalSubpart(p []byte) int {
	lead := p[0]
	var need int
	lo, hi := byte(0x80), byte(0xBF) // allowed range for the second byte
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		need = 1
	case lead == 0xE0:
		need, lo = 2, 0xA0
	case lead >= 0xE1 && lead <= 0xEC, lead == 0xEE, lead == 0xEF:
		need = 2
	case lead == 0xED:
		need, hi = 2, 0x9F
	case lead == 0xF0:
		need, lo = 3, 0x90
	case lead >= 0xF1 && lead <= 0xF3:
		need = 3
	case lead == 0xF4:
		need, hi = 3, 0x8F
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(p); k++ {
		c := p[n]
		if k == 0 && (c < lo || c > hi) || k > 0 && (c < 0x80 || c > 0xBF) {
			break
		}
		n++
	}
	return n
}
