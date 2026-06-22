package chrome

import (
	"strings"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// FooterSep is the one canonical key-hint separator. Screens historically drifted
// between " / " and "  /  "; this is the single source of truth.
const FooterSep = " / "

// KeyHint is one footer affordance: a key (or chord) and what it does.
type KeyHint struct {
	Key   string
	Label string
}

func (h KeyHint) string() string {
	switch {
	case h.Key == "":
		return h.Label
	case h.Label == "":
		return h.Key
	default:
		return h.Key + " " + h.Label
	}
}

// Footer renders key hints in the canonical grammar ("key label / key label").
// When the hints exceed width it drops trailing ones and appends a "+N" marker
// (progressive disclosure) rather than letting the shell silently truncate with
// "~". width <= 0 means no overflow handling.
func Footer(hints []KeyHint, width int) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		if s := strings.TrimSpace(h.string()); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	full := strings.Join(parts, FooterSep)
	if width <= 0 || termstyle.VisibleWidth(full) <= width {
		return full
	}
	// Drop trailing hints until "head … +N" fits.
	for keep := len(parts) - 1; keep >= 1; keep-- {
		dropped := len(parts) - keep
		candidate := strings.Join(parts[:keep], FooterSep) + FooterSep + "+" + itoa(dropped)
		if termstyle.VisibleWidth(candidate) <= width {
			return candidate
		}
	}
	// Even one hint plus the marker doesn't fit; let the shell truncate the
	// single head hint.
	return parts[0]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
