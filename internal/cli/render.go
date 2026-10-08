package cli

import (
	"strconv"
	"strings"
	"time"
)

// render.py helpers shared by the show/status tables.

// formatBadgeText ports _format_badge_text: (display text, "ok"|"issue"|"skip").
func formatBadgeText(status string, useUnicode bool) (string, string) {
	okSym, skipSym, warnSym := "v", "-", "!"
	if useUnicode {
		okSym, skipSym, warnSym = "✓", "–", "⚠"
	}
	switch {
	case status == "OK":
		return okSym, "ok"
	case status == "OK_LIVE":
		return okSym + " (live)", "ok"
	case strings.HasPrefix(status, "OK ("):
		return status[4 : len(status)-1], "ok"
	case status == "SKIP" || status == "N/A" || status == "NOT_TARGETED" || status == "OFFLINE":
		return skipSym, "skip"
	case status == "PRESENT":
		return "P", "skip"
	}
	for _, kc := range [][2]string{{"CONFLICT", "C"}, {"MISSING", "M"}, {"DRIFT", "D"}, {"UPDATE", "D"}, {"ERROR", "E"}} {
		if status == kc[0] {
			return warnSym + " " + kc[1], "issue"
		}
		if strings.HasPrefix(status, kc[0]+" (") {
			_, rest, _ := strings.Cut(status, " (")
			return warnSym + " " + kc[1] + " " + rest[:len(rest)-1], "issue"
		}
	}
	return status, "ok"
}

// formatStatusBadge ports _format_status_badge.
func formatStatusBadge(status string, useUnicode, useColor bool) string {
	text, kind := formatBadgeText(status, useUnicode)
	switch kind {
	case "issue":
		return colorize(text, colorRed, useColor)
	case "skip":
		return colorize(text, colorDim, useColor)
	}
	return colorize(text, colorGreen, useColor)
}

func badgeIsIssue(status string, useUnicode bool) bool {
	_, kind := formatBadgeText(status, useUnicode)
	return kind == "issue"
}

// renderLegend ports render_legend.
func renderLegend(useUnicode, useColor bool) string {
	dot, okSym, skipSym, warnSym := "*", "v", "-", "!"
	if useUnicode {
		dot, okSym, skipSym, warnSym = "·", "✓", "–", "⚠"
	}
	text := "Legend: " + okSym + " synced " + dot + " " + skipSym + " n/a " + dot + " 0 none " + dot + " " +
		warnSym + " M missing " + dot + " " + warnSym + " C conflict " + dot + " " + warnSym + " D drift " + dot + " " + warnSym + " E error"
	if useColor {
		text = colorize(text, colorDim, true)
	}
	return text
}

// renderKeyValueFields ports render_key_value_fields: labels right-aligned.
func renderKeyValueFields(fields [][2]string) string {
	width := 0
	for _, f := range fields {
		if w := displayWidth(f[0]); w > width {
			width = w
		}
	}
	lines := make([]string, len(fields))
	for i, f := range fields {
		lines[i] = strings.Repeat(" ", width-displayWidth(f[0])) + f[0] + "  " + f[1]
	}
	return strings.Join(lines, "\n")
}

// formatMemoryUpdatedDate ports _format_memory_updated_date.
func formatMemoryUpdatedDate(d *time.Time) string {
	if d == nil {
		return "-"
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
	switch {
	case day.Equal(today):
		return "today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "yesterday"
	case day.Year() == today.Year():
		return day.Format("Jan") + " " + strconv.Itoa(day.Day())
	}
	return day.Format("2006-01-02")
}

// localDate is date.fromtimestamp(mtime).
func localDate(t time.Time) *time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	return &d
}
