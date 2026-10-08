package compat

import (
	"strconv"
	"strings"
)

// PyFloatRepr is CPython's repr(float) for finite values: the shortest
// round-tripping digits, in fixed notation when the decimal exponent is in
// [-4, 16) (with ".0" added to integral values), otherwise scientific with
// a signed two-digit-minimum exponent ("1e+16", "1.5e-05"). Go's %g uses a
// different switch-over point, so it can't be used directly.
func PyFloatRepr(f float64) string {
	// 'e' with -1 precision gives the shortest digits: d[.ddd]e±XX.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	digits := strings.Replace(mant, ".", "", 1)

	if exp < -4 || exp >= 16 {
		out := digits[:1]
		if len(digits) > 1 {
			out += "." + digits[1:]
		}
		es := strconv.Itoa(abs(exp))
		if len(es) < 2 {
			es = "0" + es
		}
		if exp < 0 {
			return sign + out + "e-" + es
		}
		return sign + out + "e+" + es
	}
	// Fixed notation: the decimal point goes after digit index exp+1.
	point := exp + 1
	var s string
	switch {
	case point <= 0:
		s = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		s = digits + strings.Repeat("0", point-len(digits)) + ".0"
	default:
		s = digits[:point] + "." + digits[point:]
	}
	return sign + s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
