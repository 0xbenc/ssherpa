package chrome

import "github.com/0xbenc/termchrome"

// Shim over termchrome's canonical footer grammar. KeyHint is an alias and
// Footer/FooterSep are re-exported, so ssherpa call sites are unchanged while the
// "key label / key label" + "+N" overflow logic lives in exactly one place.

// FooterSep is the one canonical key-hint separator.
const FooterSep = termchrome.FooterSep

// KeyHint is one footer affordance: a key (or chord) and what it does.
type KeyHint = termchrome.KeyHint

// Footer renders key hints in the canonical grammar with "+N" overflow.
var Footer = termchrome.Footer
