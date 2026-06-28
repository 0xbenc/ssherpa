// Package chrome is ssherpa's adapter over the shared box-drawing primitives.
// The geometry now lives in github.com/0xbenc/termchrome (so passage shares the
// exact same border math); this package is a thin shim that converts ssherpa's
// termstyle.Theme to termtheme.Theme and keeps every call site unchanged. The
// DECSC/DECRC bottom-strip paint mechanics stay in internal/session; only the
// strings live here. (Mirrors listview.go, which re-exports termnav.)
package chrome

import (
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/termchrome"
	"github.com/0xbenc/termtheme"
)

// Truncator shortens a (possibly styled) string to width cells. Callers pass
// their own so the trusted-chrome policy (Sanitize on overflow) and the raw
// transcript-body policy (Strip on overflow) are each preserved.
type Truncator = termchrome.Truncator

// Top draws the rounded top border with an optional label.
func Top(theme termstyle.Theme, label string, width int, tr Truncator) string {
	return termchrome.Top(termtheme.Theme(theme), label, width, tr)
}

// Divider draws a mid-box horizontal rule.
func Divider(theme termstyle.Theme, width int) string {
	return termchrome.Divider(termtheme.Theme(theme), width)
}

// Bottom draws the rounded bottom border.
func Bottom(theme termstyle.Theme, width int) string {
	return termchrome.Bottom(termtheme.Theme(theme), width)
}

// Edge draws a top/divider/bottom border row.
func Edge(theme termstyle.Theme, left, right, label string, width int, tr Truncator) string {
	return termchrome.Edge(termtheme.Theme(theme), left, right, label, width, tr)
}

// Line wraps content as a box body row ("│ … │").
func Line(theme termstyle.Theme, content string, width int, tr Truncator) string {
	return termchrome.Line(termtheme.Theme(theme), content, width, tr)
}
