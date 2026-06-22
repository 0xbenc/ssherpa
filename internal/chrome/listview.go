package chrome

// ListView is the windowing engine shared by the variable-height selectable
// lists in package ui (the host picker, the host chooser, and the transfer file
// browser). Each of those screens previously carried a byte-identical copy of
// this scroll math; the only thing they vary is how a filtered row maps to its
// group label, which they supply as a callback. Keep these functions
// behaviorally identical to the screens' golden frames — they are pure index
// math with no rendering, so a change here moves every list at once.
//
// Row model: each visible (filtered) row is one body line, plus a one-line
// group header — and, except before the first rendered row, a blank separator
// line — whenever a row begins a new non-empty group. A leading "N more above"
// line is reserved while scrolled down (start > 0); a trailing "N more" line is
// reserved while rows remain below the cursor.

// WindowContainsCursor reports whether a viewport starting at filtered row
// `start` can show the row at `cursor` within `budget` body lines. groupAt
// returns the group label of filtered row i and ok=false for rows that should
// be skipped entirely (stale indices), mirroring each screen's bounds guard.
func WindowContainsCursor(n, start, cursor, budget int, groupAt func(i int) (group string, ok bool)) bool {
	if n == 0 || cursor < start || cursor < 0 || cursor >= n {
		return false
	}
	lines := 0
	if start > 0 {
		lines++ // "N more above"
	}
	lastGroup := ""
	rendered := 0
	for i := start; i < n; i++ {
		group, ok := groupAt(i)
		if !ok {
			continue
		}
		groupCost := 0
		newGroup := group != "" && group != lastGroup
		if newGroup {
			groupCost = 1 // group header
			if rendered > 0 {
				groupCost++ // blank separator before the header
			}
		}
		reserve := 0
		if n-i-1 > 0 {
			reserve = 1 // room for the "N more" notice
		}
		if lines+groupCost+1+reserve > budget {
			return false
		}
		if i == cursor {
			return true
		}
		lines += groupCost + 1
		if newGroup {
			lastGroup = group
		}
		rendered++
	}
	return false
}

// ClampWindow clamps cursor and scroll into [0,n) and advances scroll until the
// cursor is visible, returning the adjusted (cursor, scroll). contains(start,
// cursor) reports visibility — typically a WindowContainsCursor closure that
// captures the current budget. With n == 0 it resets both to 0.
func ClampWindow(n, cursor, scroll int, contains func(start, cursor int) bool) (newCursor, newScroll int) {
	if n == 0 {
		return 0, 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll >= n {
		scroll = n - 1
	}
	if cursor < scroll {
		scroll = cursor
	}
	for scroll < cursor && !contains(scroll, cursor) {
		scroll++
	}
	if !contains(scroll, cursor) {
		scroll = cursor
	}
	return cursor, scroll
}

// JumpSection returns the cursor position after jumping to the adjacent group
// boundary in direction delta (negative = previous group, positive = next),
// mirroring each screen's section jump. groupAt returns the group label of
// filtered row i ("" when out of range). The caller re-clamps the viewport
// afterward. With no movement possible it returns cursor unchanged.
func JumpSection(n, cursor, delta int, groupAt func(i int) string) int {
	if n == 0 || delta == 0 || cursor < 0 || cursor >= n {
		return cursor
	}
	currentGroup := groupAt(cursor)
	if delta > 0 {
		currentEnd := cursor
		for i := cursor + 1; i < n; i++ {
			if groupAt(i) != currentGroup {
				return i
			}
			currentEnd = i
		}
		if currentEnd > cursor {
			return currentEnd
		}
		return cursor
	}

	currentStart := cursor
	for currentStart > 0 && groupAt(currentStart-1) == currentGroup {
		currentStart--
	}
	if currentStart < cursor {
		return currentStart
	}

	for i := currentStart - 1; i >= 0; i-- {
		group := groupAt(i)
		if group == currentGroup {
			continue
		}
		for i > 0 && groupAt(i-1) == group {
			i--
		}
		return i
	}
	return cursor
}
