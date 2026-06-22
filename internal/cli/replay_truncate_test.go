package cli

import (
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// TestReplayTruncateLineWideRune pins that an emoji/CJK transcript-replay line
// is cell-accurately truncated and never exceeds the render width (S1).
func TestReplayTruncateLineWideRune(t *testing.T) {
	for _, w := range []int{3, 6, 10} {
		out := replayTruncateLine("日本語🔐 transcript line that is quite long", w)
		if got := termstyle.VisibleWidth(out); got > w {
			t.Fatalf("replayTruncateLine width %d, exceeds %d: %q", got, w, out)
		}
	}
}
