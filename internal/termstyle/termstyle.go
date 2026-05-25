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
		if value[i] == '\x1b' && i+1 < len(value) && value[i+1] == '[' {
			i = skipANSI(value, i)
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
		if value[i] == '\x1b' && i+1 < len(value) && value[i+1] == '[' {
			i = skipANSI(value, i)
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

func skipANSI(value string, start int) int {
	i := start + 2
	for i < len(value) {
		b := value[i]
		i++
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			break
		}
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

func Truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "~"
}
