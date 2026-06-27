package chrome

// The variable-height list windowing engine that lived here was lifted verbatim
// into github.com/0xbenc/termnav (where passage now shares it too). This file
// re-exports it so ssherpa's call sites (the host picker, host chooser, and
// transfer browser) are unchanged while the scroll math lives in exactly one
// place. See termnav/window.go for the documented behavior.

import "github.com/0xbenc/termnav"

// WindowContainsCursor reports whether a viewport starting at filtered row
// `start` can show the row at `cursor` within `budget` body lines.
func WindowContainsCursor(n, start, cursor, budget int, groupAt func(i int) (group string, ok bool)) bool {
	return termnav.WindowContainsCursor(n, start, cursor, budget, groupAt)
}

// ClampWindow clamps cursor and scroll into [0,n) and advances scroll until the
// cursor is visible.
func ClampWindow(n, cursor, scroll int, contains func(start, cursor int) bool) (newCursor, newScroll int) {
	return termnav.ClampWindow(n, cursor, scroll, contains)
}

// JumpSection returns the cursor position after jumping to the adjacent group
// boundary in direction delta.
func JumpSection(n, cursor, delta int, groupAt func(i int) string) int {
	return termnav.JumpSection(n, cursor, delta, groupAt)
}
