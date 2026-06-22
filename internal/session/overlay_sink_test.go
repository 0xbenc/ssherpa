package session

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// TestTruncateOverlayLineSanitizesOnOverflow pins S2 for the live-overlay chrome
// sink: an overflowing line with a raw C1 control must not leak it over the
// supervised stream, and must stay within the cell budget (S1).
func TestTruncateOverlayLineSanitizesOnOverflow(t *testing.T) {
	in := "route you -> " + string(rune(0x9b)) + "31m -> a-very-long-hostname-that-overflows"
	out := truncateOverlayLine(in, 16)
	if strings.ContainsRune(out, 0x9b) {
		t.Fatalf("C1 control leaked through truncateOverlayLine: %q", out)
	}
	if w := termstyle.VisibleWidth(out); w > 16 {
		t.Fatalf("truncateOverlayLine width = %d, exceeds 16: %q", w, out)
	}
}

func TestTruncateOverlayLineWideRuneWithinWidth(t *testing.T) {
	out := truncateOverlayLine("日本語のホスト名が長すぎる場合", 6)
	if w := termstyle.VisibleWidth(out); w > 6 {
		t.Fatalf("wide overlay line width = %d, exceeds 6: %q", w, out)
	}
}
