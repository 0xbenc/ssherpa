package chrome

import "testing"

// flat returns a groupAt for n ungrouped rows (every row group "").
func flat(n int) func(int) (string, bool) {
	return func(i int) (string, bool) {
		if i < 0 || i >= n {
			return "", false
		}
		return "", true
	}
}

func TestWindowContainsCursorFlat(t *testing.T) {
	n := 20
	// budget 5: from start 0, with a trailing "N more" reserve, rows 0..3 fit
	// (4 rows + 1 reserve = 5), row 4 would need 6.
	if !WindowContainsCursor(n, 0, 3, 5, flat(n)) {
		t.Error("row 3 should fit in budget 5 from start 0")
	}
	if WindowContainsCursor(n, 0, 4, 5, flat(n)) {
		t.Error("row 4 should not fit in budget 5 from start 0")
	}
	// scrolled: start 10 reserves a leading "N more above" line.
	if !WindowContainsCursor(n, 10, 12, 5, flat(n)) {
		t.Error("row 12 should fit from start 10 in budget 5")
	}
	if WindowContainsCursor(n, 10, 13, 5, flat(n)) {
		t.Error("row 13 should not fit from start 10 (leading reserve)")
	}
	// last row has no trailing reserve.
	if !WindowContainsCursor(n, 16, 19, 5, flat(n)) {
		t.Error("last row 19 should fit from start 16")
	}
}

func TestClampWindowScrollsCursorIntoView(t *testing.T) {
	n := 20
	contains := func(start, cursor int) bool {
		return WindowContainsCursor(n, start, cursor, 5, flat(n))
	}
	// Cursor below the window pushes scroll down.
	cur, scroll := ClampWindow(n, 10, 0, contains)
	if cur != 10 {
		t.Errorf("cursor = %d, want 10", cur)
	}
	if scroll == 0 || !contains(scroll, cur) {
		t.Errorf("scroll %d does not contain cursor %d", scroll, cur)
	}
	// Cursor above the window snaps scroll up to the cursor.
	cur, scroll = ClampWindow(n, 2, 10, contains)
	if scroll != 2 {
		t.Errorf("scroll = %d, want 2 (snap up to cursor)", scroll)
	}
	// Empty list resets.
	if c, s := ClampWindow(0, 5, 5, contains); c != 0 || s != 0 {
		t.Errorf("empty clamp = (%d,%d), want (0,0)", c, s)
	}
	// Out-of-range cursor clamps into [0,n).
	if c, _ := ClampWindow(n, 99, 0, contains); c != n-1 {
		t.Errorf("clamped cursor = %d, want %d", c, n-1)
	}
}

func TestJumpSection(t *testing.T) {
	// groups: rows 0-2 = "a", 3-5 = "b", 6-8 = "c"
	groups := []string{"a", "a", "a", "b", "b", "b", "c", "c", "c"}
	groupAt := func(i int) string {
		if i < 0 || i >= len(groups) {
			return ""
		}
		return groups[i]
	}
	n := len(groups)
	// Forward from within "a" jumps to first row of "b".
	if got := JumpSection(n, 1, 1, groupAt); got != 3 {
		t.Errorf("forward from 1 = %d, want 3", got)
	}
	// Forward from last group goes to its last row.
	if got := JumpSection(n, 6, 1, groupAt); got != 8 {
		t.Errorf("forward from 6 (last group) = %d, want 8", got)
	}
	// Backward from mid-group goes to that group's first row.
	if got := JumpSection(n, 5, -1, groupAt); got != 3 {
		t.Errorf("backward from 5 = %d, want 3 (group start)", got)
	}
	// Backward from a group's first row goes to the previous group's first row.
	if got := JumpSection(n, 3, -1, groupAt); got != 0 {
		t.Errorf("backward from 3 = %d, want 0 (prev group start)", got)
	}
	// No movement at the extremes returns cursor unchanged.
	if got := JumpSection(n, 0, -1, groupAt); got != 0 {
		t.Errorf("backward from 0 = %d, want 0", got)
	}
}
