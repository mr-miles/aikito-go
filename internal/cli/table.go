package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"
)

// isStdoutTTY mirrors Python's sys.stdout.isatty() without adding a new
// go.mod dependency: a character-device file mode is the standard
// zero-dependency Go idiom for "this is a terminal, not a pipe/file/socket".
// Less complete than a real terminal-capability library (no width query —
// see getTerminalWidth's doc comment — and less reliable on Windows), but
// correctly answers the actual yes/no question this port needs for its
// use_unicode/use_color decision.
func isStdoutTTY() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// This file ports render.py's generic Unicode/ASCII box-table renderer
// (_build_generic_table, _get_display_width, _truncate_display_text,
// _colorize, _format_scope_status_badge) faithfully, since it's reused by
// every table-rendering command (status, and later show/diff/doctor) and
// its box-drawing characters/column-width math are exactly the kind of
// thing worth getting byte-exact rather than approximating.

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
)

func colorize(text, colorCode string, enabled bool) string {
	if !enabled {
		return text
	}
	return colorCode + text + colorReset
}

var ansiEscapeRE = regexp.MustCompile("\x1B(?:[@-Z\\\\-_]|\\[[0-?]*[ -/]*[@-~])")

// displayWidth mirrors _get_display_width: ANSI escapes don't count, and
// East-Asian Wide/Fullwidth runes count as 2 columns, everything else 1.
func displayWidth(text string) int {
	clean := ansiEscapeRE.ReplaceAllString(text, "")
	width := 0
	for _, r := range clean {
		if isEastAsianWideOrFull(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

// isEastAsianWideOrFull approximates Python's unicodedata.east_asian_width(c)
// in ("F", "W") using Go's unicode "wide" range tables (unicode.Han/Hangul/
// Hiragana/Katakana cover the overwhelming majority of real-world Wide/
// Fullwidth text; this is not a byte-exact Unicode-database port, but none
// of this port's workspace/project content is expected to be CJK-heavy).
func isEastAsianWideOrFull(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		(r >= 0xFF00 && r <= 0xFFEF) // fullwidth forms block
}

func truncateDisplayText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displayWidth(text) <= maxWidth {
		return text
	}
	plain := ansiEscapeRE.ReplaceAllString(text, "")
	targetW := maxWidth - 1
	currentW := 0
	var b strings.Builder
	for _, r := range plain {
		w := 1
		if isEastAsianWideOrFull(r) {
			w = 2
		}
		if currentW+w > targetW {
			break
		}
		currentW += w
		b.WriteRune(r)
	}
	res := b.String() + "…"
	switch {
	case strings.Contains(text, "\x1b[32m"):
		return "\033[32m" + res + "\033[0m"
	case strings.Contains(text, "\x1b[31m"):
		return "\033[31m" + res + "\033[0m"
	case strings.Contains(text, "\x1b[33m"):
		return "\033[33m" + res + "\033[0m"
	case strings.Contains(text, "\x1b[2m"):
		return "\033[2m" + res + "\033[0m"
	}
	return res
}

// getTerminalWidth mirrors _get_terminal_width, simplified: Python's version
// returns None (no width-based truncation) whenever sys.stdout.isatty() is
// false, which is already true for every non-interactive invocation this Go
// build's tests and most real pipe/redirect usage hit. Detecting a real TTY
// width without a new go.mod dependency would need a raw ioctl syscall per
// platform; since the practical effect of "no truncation" is just a wider
// (not wrong) table, this always returns nil rather than adding that
// complexity for a cosmetic-only narrow-terminal case. Revisit if a real
// TTY width is ever load-bearing rather than a nicety.
func getTerminalWidth() *int {
	return nil
}

// buildGenericTable is a direct port of render.py's _build_generic_table.
func buildGenericTable(headers []string, rows [][]string, useUnicode, useColor bool, truncatableCols []int) string {
	colWidths := make([]int, len(headers))
	for i, h := range headers {
		colWidths[i] = displayWidth(h)
	}
	for _, row := range rows {
		for i, val := range row {
			if i < len(colWidths) && displayWidth(val) > colWidths[i] {
				colWidths[i] = displayWidth(val)
			}
		}
	}

	numCols := len(headers)
	if termWidth := getTerminalWidth(); termWidth != nil {
		borderOverhead := 3*numCols + 1
		maxContentWidth := *termWidth - borderOverhead
		currentContentWidth := 0
		for _, w := range colWidths {
			currentContentWidth += w
		}
		if currentContentWidth > maxContentWidth && maxContentWidth > 0 {
			excess := currentContentWidth - maxContentWidth
			cols := truncatableCols
			if cols == nil {
				for c := numCols - 1; c > 0; c-- {
					cols = append(cols, c)
				}
			}
			minWidths := map[int]int{}
			for _, c := range cols {
				mw := displayWidth(headers[c])
				if mw < 6 {
					mw = 6
				}
				minWidths[c] = mw
			}
			for excess > 0 {
				var reducible []int
				for _, c := range cols {
					if colWidths[c] > minWidths[c] {
						reducible = append(reducible, c)
					}
				}
				if len(reducible) == 0 {
					break
				}
				widest := reducible[0]
				for _, c := range reducible {
					if colWidths[c] > colWidths[widest] {
						widest = c
					}
				}
				colWidths[widest]--
				excess--
			}
		}
	}

	var topL, topM, topR, midL, midM, midR, botL, botM, botR, horiz, vert string
	if useUnicode {
		topL, topM, topR = "┌", "┬", "┐"
		midL, midM, midR = "├", "┼", "┤"
		botL, botM, botR = "└", "┴", "┘"
		horiz, vert = "─", "│"
	} else {
		topL, topM, topR = "+", "+", "+"
		midL, midM, midR = "+", "+", "+"
		botL, botM, botR = "+", "+", "+"
		horiz, vert = "-", "|"
	}

	var lines []string

	topParts := make([]string, len(colWidths))
	for i, w := range colWidths {
		topParts[i] = strings.Repeat(horiz, w+2)
	}
	lines = append(lines, topL+strings.Join(topParts, topM)+topR)

	headerCells := make([]string, len(headers))
	for i, h := range headers {
		vl := displayWidth(h)
		if vl > colWidths[i] {
			h = truncateDisplayText(h, colWidths[i])
			vl = displayWidth(h)
		}
		headerCells[i] = " " + h + strings.Repeat(" ", colWidths[i]-vl) + " "
	}
	headerLine := vert + strings.Join(headerCells, vert) + vert
	lines = append(lines, colorize(headerLine, colorBold, useColor))

	midParts := make([]string, len(colWidths))
	for i, w := range colWidths {
		midParts[i] = strings.Repeat(horiz, w+2)
	}
	midSepLine := midL + strings.Join(midParts, midM) + midR
	lines = append(lines, midSepLine)

	for _, row := range rows {
		if len(row) == 1 && row[0] == "---SEPARATOR---" {
			lines = append(lines, midSepLine)
			continue
		}
		cells := make([]string, len(row))
		for i, val := range row {
			vl := displayWidth(val)
			if i < len(colWidths) && vl > colWidths[i] {
				val = truncateDisplayText(val, colWidths[i])
				vl = displayWidth(val)
			}
			pad := 0
			if i < len(colWidths) {
				pad = colWidths[i] - vl
			}
			cells[i] = " " + val + strings.Repeat(" ", pad) + " "
		}
		lines = append(lines, vert+strings.Join(cells, vert)+vert)
	}

	botParts := make([]string, len(colWidths))
	for i, w := range colWidths {
		botParts[i] = strings.Repeat(horiz, w+2)
	}
	lines = append(lines, botL+strings.Join(botParts, botM)+botR)

	return strings.Join(lines, "\n")
}

// formatScopeStatusBadge mirrors _format_scope_status_badge.
func formatScopeStatusBadge(status string, useUnicode, useColor bool) string {
	okSym := "v"
	if useUnicode {
		okSym = "✓"
	}
	if status == "OK" {
		return colorize(okSym, colorGreen, useColor)
	}
	if status == "-" || status == "OFFLINE" {
		return colorize("-", colorDim, useColor)
	}
	if strings.HasPrefix(status, "!") {
		return colorize(status, colorRed, useColor)
	}
	return colorize(fmt.Sprintf("! %s", strings.ToLower(status)), colorRed, useColor)
}
