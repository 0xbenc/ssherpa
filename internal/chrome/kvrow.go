package chrome

import (
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/termchrome"
	"github.com/0xbenc/termtheme"
)

// KVRow renders an aligned "label   value" row (shim over termchrome.KVRow).
func KVRow(theme termstyle.Theme, label, value string, gutter int) string {
	return termchrome.KVRow(termtheme.Theme(theme), label, value, gutter)
}
