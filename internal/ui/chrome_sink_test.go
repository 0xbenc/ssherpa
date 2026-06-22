package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// TestTruncateStyledSanitizesOnOverflow pins S2: an overflowing chrome line
// carrying a raw C1 control (U+009B, a CSI introducer) must not leak it into
// the trusted chrome, and must stay within the cell budget.
func TestTruncateStyledSanitizesOnOverflow(t *testing.T) {
	in := "host-" + string(rune(0x9b)) + "31mEVIL-with-enough-text-to-overflow"
	out := truncateStyled(in, 12)
	if strings.ContainsRune(out, 0x9b) {
		t.Fatalf("C1 control leaked through truncateStyled: %q", out)
	}
	if w := termstyle.VisibleWidth(out); w > 12 {
		t.Fatalf("truncateStyled width = %d, exceeds 12: %q", w, out)
	}
}

// TestTruncateStyledEmojiWithinWidth pins that a wide-rune line never exceeds
// the cell budget once truncated.
func TestTruncateStyledEmojiWithinWidth(t *testing.T) {
	out := truncateStyled("🔐🔑🛡️ secret-host-name-that-overflows", 8)
	if w := termstyle.VisibleWidth(out); w > 8 {
		t.Fatalf("emoji line width = %d, exceeds 8: %q", w, out)
	}
}
