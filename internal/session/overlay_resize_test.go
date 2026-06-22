package session

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/creack/pty"
)

// TestClearSessionOverlayResizeClearsCurrentBand is the S15 invariant: if the
// terminal is resized while the bottom-pinned overlay is open, closing it must
// clear the band where the overlay *now* sits (the current bottom), not only
// the draw-time rows. Otherwise a SIGWINCH leaves overlay residue painted over
// the live stream and the trailing DECRC restores against a stale geometry.
func TestClearSessionOverlayResizeClearsCurrentBand(t *testing.T) {
	userPTY, userTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer userPTY.Close()
	defer userTTY.Close()

	// The overlay was drawn when the terminal was 40 rows tall: a 5-line band
	// pinned to the bottom starts at row 36.
	frame := overlayFrame{terminal: true, startRow: 36, lines: 5}

	// Now the terminal is 24 rows tall (a SIGWINCH shrank it). The current
	// bottom band of 5 lines starts at row 20.
	if err := pty.Setsize(userTTY, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("setsize: %v", err)
	}

	var out bytes.Buffer
	clearSessionOverlay(&out, userTTY, frame)
	got := out.String()

	// Draw-time rows (36..40) are cleared.
	for row := 36; row <= 40; row++ {
		if !strings.Contains(got, fmt.Sprintf("\x1b[%d;1H\x1b[2K", row)) {
			t.Errorf("draw-time row %d not cleared: %q", row, got)
		}
	}
	// Current bottom band (20..24) is also cleared after the resize.
	for row := 20; row <= 24; row++ {
		if !strings.Contains(got, fmt.Sprintf("\x1b[%d;1H\x1b[2K", row)) {
			t.Errorf("current-band row %d not cleared after resize: %q", row, got)
		}
	}
	// Cursor restored + shown.
	if !strings.HasSuffix(got, "\x1b8\x1b[?25h") {
		t.Errorf("missing trailing DECRC/show-cursor: %q", got)
	}
}

// TestClearSessionOverlayNoResizeClearsOnce verifies that when the terminal is
// unchanged, clear emits exactly the draw-time band once (no redundant second
// band) — keeping the no-resize path byte-minimal.
func TestClearSessionOverlayNoResizeClearsOnce(t *testing.T) {
	userPTY, userTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer userPTY.Close()
	defer userTTY.Close()

	if err := pty.Setsize(userTTY, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("setsize: %v", err)
	}
	// Bottom band of 3 lines on a 24-row terminal starts at row 22.
	frame := overlayFrame{terminal: true, startRow: 22, lines: 3}

	var out bytes.Buffer
	clearSessionOverlay(&out, userTTY, frame)
	got := out.String()

	if n := strings.Count(got, "\x1b[2K"); n != 3 {
		t.Errorf("expected 3 row clears with no resize, got %d: %q", n, got)
	}
}

// TestClearSessionOverlayNonTerminalFallback verifies the non-terminal
// (frame.terminal == false) path still prints the plain marker and never
// touches the cursor.
func TestClearSessionOverlayNonTerminalFallback(t *testing.T) {
	var out bytes.Buffer
	clearSessionOverlay(&out, nil, overlayFrame{terminal: false})
	if got := out.String(); !strings.Contains(got, "ssherpa overlay closed") {
		t.Errorf("non-terminal fallback missing marker: %q", got)
	}
}
