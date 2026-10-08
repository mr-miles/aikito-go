package cli

import (
	"fmt"
	"strings"
)

// splitKeepEnds is a simplified splitlines(keepends=True) for the \n-only
// case (adequate here: config/subagent file content is always \n or
// \r\n-terminated plain text, not resource-identity-sensitive like
// workspace.pythonSplitLines's unexported, more complete version).
func splitKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// diffOp mirrors one element of Python's difflib.SequenceMatcher.get_opcodes():
// tag is "equal"/"replace"/"delete"/"insert", and [i1:i2)/[j1:j2) are the
// corresponding index ranges into a/b.
type diffOp struct {
	tag            string
	i1, i2, j1, j2 int
}

// lcsOpcodes computes opcodes via a dynamic-programming LCS over lines (not
// difflib's actual Ratcliff/Obershelp algorithm, but produces an equally
// valid, correct edit script — exact-minimal-diff parity isn't required
// here, just a correct and readable unified diff).
func lcsOpcodes(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var raw []byte // 'e' equal, 'd' delete (a only), 'i' insert (b only)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			raw = append(raw, 'e')
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			raw = append(raw, 'd')
			i++
		default:
			raw = append(raw, 'i')
			j++
		}
	}
	for i < n {
		raw = append(raw, 'd')
		i++
	}
	for j < m {
		raw = append(raw, 'i')
		j++
	}

	var ops []diffOp
	ai, bj := 0, 0
	idx := 0
	for idx < len(raw) {
		if raw[idx] == 'e' {
			start := idx
			for idx < len(raw) && raw[idx] == 'e' {
				idx++
			}
			count := idx - start
			ops = append(ops, diffOp{"equal", ai, ai + count, bj, bj + count})
			ai += count
			bj += count
			continue
		}
		delCount, insCount := 0, 0
		for idx < len(raw) && raw[idx] != 'e' {
			if raw[idx] == 'd' {
				delCount++
			} else {
				insCount++
			}
			idx++
		}
		tag := "replace"
		if delCount == 0 {
			tag = "insert"
		} else if insCount == 0 {
			tag = "delete"
		}
		ops = append(ops, diffOp{tag, ai, ai + delCount, bj, bj + insCount})
		ai += delCount
		bj += insCount
	}
	return ops
}

// groupOpcodes mirrors difflib.SequenceMatcher.get_grouped_opcodes(n):
// trims the leading/trailing equal runs down to n lines of context, and
// splits into separate hunks wherever an interior equal run exceeds 2n
// lines (keeping n lines of context on each side of the split).
func groupOpcodes(ops []diffOp, n int) [][]diffOp {
	if len(ops) == 0 {
		return nil
	}
	ops = append([]diffOp(nil), ops...)
	if ops[0].tag == "equal" {
		o := ops[0]
		i1 := max(o.i1, o.i2-n)
		j1 := max(o.j1, o.j2-n)
		ops[0] = diffOp{"equal", i1, o.i2, j1, o.j2}
	}
	if len(ops) > 0 && ops[len(ops)-1].tag == "equal" {
		o := ops[len(ops)-1]
		i2 := min(o.i2, o.i1+n)
		j2 := min(o.j2, o.j1+n)
		ops[len(ops)-1] = diffOp{"equal", o.i1, i2, o.j1, j2}
	}

	var groups [][]diffOp
	var cur []diffOp
	for _, op := range ops {
		if op.tag == "equal" && (op.i2-op.i1) > 2*n && len(cur) > 0 {
			cur = append(cur, diffOp{"equal", op.i1, min(op.i2, op.i1+n), op.j1, min(op.j2, op.j1+n)})
			groups = append(groups, cur)
			i1b := max(op.i1, op.i2-n)
			j1b := max(op.j1, op.j2-n)
			cur = []diffOp{{"equal", i1b, op.i2, j1b, op.j2}}
			continue
		}
		cur = append(cur, op)
	}
	if len(cur) > 0 {
		allEqual := true
		for _, op := range cur {
			if op.tag != "equal" {
				allEqual = false
				break
			}
		}
		if !allEqual {
			groups = append(groups, cur)
		}
	}
	return groups
}

// formatRangeUnified mirrors difflib._format_range_unified exactly: a
// single line number (no ",1") when the range covers exactly one line, and
// the off-by-one "start,0" convention for a zero-length range.
func formatRangeUnified(start, stop int) string {
	length := stop - start
	beginning := start + 1
	if length == 1 {
		return fmt.Sprintf("%d", beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}

// unifiedDiff mirrors diff.py's _unified_diff: Python's
// difflib.unified_diff(actual, expected, fromfile=..., tofile=...) joined
// and rstripped. Returns "" when the two line slices are identical (no
// hunks), matching Python's behavior of producing empty output for an
// all-equal sequence.
func unifiedDiff(a, b []string, fromFile, toFile string) string {
	ops := lcsOpcodes(a, b)
	hasChange := false
	for _, o := range ops {
		if o.tag != "equal" {
			hasChange = true
			break
		}
	}
	if !hasChange {
		return ""
	}

	groups := groupOpcodes(ops, 3)
	if len(groups) == 0 {
		return ""
	}

	var out []string
	out = append(out, "--- "+fromFile+"\n", "+++ "+toFile+"\n")
	for _, group := range groups {
		i1, i2 := group[0].i1, group[len(group)-1].i2
		j1, j2 := group[0].j1, group[len(group)-1].j2
		out = append(out, fmt.Sprintf("@@ -%s +%s @@\n", formatRangeUnified(i1, i2), formatRangeUnified(j1, j2)))
		for _, op := range group {
			switch op.tag {
			case "equal":
				for k := op.i1; k < op.i2; k++ {
					out = append(out, " "+a[k])
				}
			case "delete", "replace":
				for k := op.i1; k < op.i2; k++ {
					out = append(out, "-"+a[k])
				}
				if op.tag == "replace" {
					for k := op.j1; k < op.j2; k++ {
						out = append(out, "+"+b[k])
					}
				}
			case "insert":
				for k := op.j1; k < op.j2; k++ {
					out = append(out, "+"+b[k])
				}
			}
		}
	}
	return strings.TrimRight(strings.Join(out, ""), " \t\n\r\v\f")
}
