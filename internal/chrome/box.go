// Package chrome owns ssherpa's shared box-drawing primitives — the bordered
// shell geometry every screen and the live session-map overlay render through.
// It owns STRINGS ONLY; the DECSC/DECRC bottom-strip paint mechanics stay in
// internal/session. Centralizing the geometry here means the cell-accurate
// width and sanitize fixes land once and reach every surface, and the picker
// and the overlay can never disagree about how a border is drawn.
package chrome

import (
	"strings"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// Truncator shortens a (possibly styled) string to width cells. Callers pass
// their own so the trusted-chrome policy (Sanitize on overflow) and the raw
// transcript-body policy (Strip on overflow, preserve on fit) are each
// preserved while the box geometry is shared.
type Truncator func(value string, width int) string

// Top draws the rounded top border with an optional label.
func Top(theme termstyle.Theme, label string, width int, tr Truncator) string {
	return Edge(theme, "╭", "╮", label, width, tr)
}

// Divider draws a mid-box horizontal rule.
func Divider(theme termstyle.Theme, width int) string {
	return Edge(theme, "├", "┤", "", width, nil)
}

// Bottom draws the rounded bottom border.
func Bottom(theme termstyle.Theme, width int) string {
	return Edge(theme, "╰", "╯", "", width, nil)
}

// Edge draws a top/divider/bottom border row. The fill dashes are always
// border-styled — the canonical choice that resolves the historical divergence
// between the picker (styled) and the overlay (default-colored). The label is
// styled by the caller and truncated by tr; an empty label yields a plain rule.
func Edge(theme termstyle.Theme, left, right, label string, width int, tr Truncator) string {
	inner := max(0, width-2)
	if inner == 0 {
		return theme.Style(termstyle.RoleBorder, left+right)
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return theme.Style(termstyle.RoleBorder, left+strings.Repeat("─", inner)+right)
	}
	if tr != nil {
		label = " " + tr(label, max(0, inner-2)) + " "
	} else {
		label = " " + label + " "
	}
	remaining := inner - termstyle.VisibleWidth(label)
	if remaining < 0 {
		remaining = 0
	}
	return theme.Style(termstyle.RoleBorder, left) + label + theme.Style(termstyle.RoleBorder, strings.Repeat("─", remaining)+right)
}

// Line wraps content as a box body row ("│ … │"), truncating with tr and
// padding to the inner width.
func Line(theme termstyle.Theme, content string, width int, tr Truncator) string {
	inner := max(0, width-4)
	content = termstyle.PadRight(tr(content, inner), inner)
	return theme.Style(termstyle.RoleBorder, "│ ") + content + theme.Style(termstyle.RoleBorder, " │")
}
