package termstyle

import (
	"strings"
	"unicode/utf8"
)

const reset = "\x1b[0m"

func Apply(noColor bool, code string, value string) string {
	if noColor || code == "" || value == "" {
		return value
	}
	return "\x1b[" + code + "m" + value + reset
}

func VisibleWidth(value string) int {
	width := 0
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			i = skipEscape(value, i)
			continue
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			i++
			continue
		}
		width++
		i += size
	}
	return width
}

func Strip(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			i = skipEscape(value, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			i++
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// Sanitize neutralizes untrusted text for terminal display: it strips
// escape sequences like Strip and additionally drops raw control
// characters — C0 (except tab), DEL, and the C1 range U+0080–U+009F,
// which xterm-class terminals treat as escape introducers (U+009B is
// CSI) and which Strip alone passes through. Use it on any string a
// remote host or an imported bundle may have influenced.
func Sanitize(value string) string {
	stripped := Strip(value)
	clean := true
	for _, r := range stripped {
		if isUnsafeControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return stripped
	}
	var b strings.Builder
	for _, r := range stripped {
		if isUnsafeControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isUnsafeControl(r rune) bool {
	if r == '\t' {
		return false
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// skipEscape returns the index just past the escape sequence starting at
// value[start], which must be an ESC byte. It recognizes the escape forms
// real PTYs emit, per ECMA-48:
//
//	CSI    ESC [ params(0x30-0x3F) intermediates(0x20-0x2F) final(0x40-0x7E)
//	OSC    ESC ] ... terminated by BEL or ST (ESC \)
//	DCS/SOS/PM/APC  ESC P / X / ^ / _ ... terminated by ST (ESC \)
//	SS3    ESC O <byte>                          (e.g. ESC O P for F1)
//	nF     ESC intermediates(0x20-0x2F) final(0x30-0x7E)  (e.g. ESC ( B)
//	Fp/Fe/Fs  ESC final(0x30-0x7E)               (e.g. ESC =, ESC 7, ESC c)
//
// A sequence truncated by end of input is consumed to the end; a malformed
// byte inside a sequence ends it without being consumed, so following text
// is never eaten. A bare ESC before an unrecognized byte (or at end of
// input) is consumed alone, dropping just the ESC.
func skipEscape(value string, start int) int {
	i := start + 1
	if i >= len(value) {
		return i
	}
	switch value[i] {
	case '[':
		return skipCSI(value, i+1)
	case ']':
		return skipEscString(value, i+1, true)
	case 'P', 'X', '^', '_':
		return skipEscString(value, i+1, false)
	case 'O':
		// SS3: the following character names a key. Consume a whole
		// rune so a multibyte character is not split into replacement
		// bytes.
		if i+1 < len(value) {
			_, size := utf8.DecodeRuneInString(value[i+1:])
			return i + 1 + size
		}
		return i + 1
	}
	for i < len(value) && value[i] >= 0x20 && value[i] <= 0x2f {
		i++
	}
	if i < len(value) && value[i] >= 0x30 && value[i] <= 0x7e {
		return i + 1
	}
	if i > start+1 {
		return i // intermediates with a malformed or missing final byte
	}
	return start + 1
}

// skipCSI consumes a CSI body starting just past "ESC [": parameter bytes
// (0x30-0x3F) and intermediate bytes (0x20-0x2F), then exactly one final
// byte in 0x40-0x7E — which includes non-letter finals such as '@' (ICH),
// '`' (HPA), and '~' (keypad keys). Embedded C0 controls (other than ESC)
// are skipped, as ECMA-48 terminals execute them and continue the
// sequence; any other byte ends the sequence without being consumed.
func skipCSI(value string, i int) int {
	for i < len(value) {
		b := value[i]
		switch {
		case b >= 0x20 && b <= 0x3f:
			i++
		case b < 0x20 && b != 0x1b:
			i++
		case b >= 0x40 && b <= 0x7e:
			return i + 1
		default:
			return i
		}
	}
	return i
}

// skipEscString consumes an OSC/DCS/SOS/PM/APC string body starting just
// past its two-byte introducer. All string types end at ST (ESC \); only
// OSC may also be terminated by BEL. An ESC that does not begin ST aborts
// the string and is left for the caller to scan as a new sequence; an
// unterminated string consumes to end of input.
func skipEscString(value string, i int, belTerminates bool) int {
	for i < len(value) {
		switch {
		case belTerminates && value[i] == 0x07:
			return i + 1
		case value[i] == '\x1b':
			if i+1 < len(value) && value[i+1] == '\\' {
				return i + 2
			}
			return i
		}
		i++
	}
	return i
}

func PadRight(value string, width int) string {
	padding := width - VisibleWidth(value)
	if padding <= 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}

// Truncate shortens value to at most width visible runes, marking cut text
// with a trailing "~". Like VisibleWidth and PadRight it is escape-aware:
// escape sequences do not count toward the width and are never split, and
// if the kept portion leaves SGR styling active a reset is appended so
// styling cannot leak past the truncation.
func Truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if VisibleWidth(value) <= width {
		return value
	}
	keep := width - 1
	marker := "~"
	if width == 1 {
		keep = 1
		marker = ""
	}
	var b strings.Builder
	styled := false
	visible := 0
	for i := 0; i < len(value) && visible < keep; {
		if value[i] == '\x1b' {
			next := skipEscape(value, i)
			seq := value[i:next]
			b.WriteString(seq)
			if isSGR(seq) {
				styled = !isSGRReset(seq)
			}
			i = next
			continue
		}
		r, size := utf8.DecodeRuneInString(value[i:])
		if size <= 0 {
			i++
			continue
		}
		b.WriteRune(r)
		visible++
		i += size
	}
	b.WriteString(marker)
	if styled {
		b.WriteString(reset)
	}
	return b.String()
}

func isSGR(seq string) bool {
	return strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m")
}

func isSGRReset(seq string) bool {
	params := seq[2 : len(seq)-1]
	return params == "" || params == "0"
}
