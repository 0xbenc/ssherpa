// Package fuzzy is ssherpa's thin adapter over termnav's shared fuzzy matcher.
// The fzf-style integer scoring that ssherpa and passage each carried a
// byte-identical copy of now lives once in github.com/0xbenc/termnav; this
// package re-exports it so the call sites (picker ranking and highlight) are
// unchanged and a match scores identically across both apps.
package fuzzy

import "github.com/0xbenc/termnav"

// MinScorePerRune is the relevance threshold, re-exported from termnav.
const MinScorePerRune = termnav.MinScorePerRune

// Result is a successful match: its score and the ascending matched rune
// indices in the candidate. It is termnav's type, so a Result flows freely
// between this package and termnav.
type Result = termnav.Result

// Match reports whether query matches candidate as an order-preserving
// subsequence and, if so, the score and matched rune positions (smart-case).
func Match(query, candidate string) (Result, bool) {
	return termnav.MatchFuzzy(query, candidate)
}

// Relevant reports whether a match clears the per-rune relevance threshold for a
// query of the given rune length. An empty query is always relevant.
func Relevant(r Result, queryLen int) bool {
	return termnav.Relevant(r, queryLen)
}
