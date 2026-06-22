package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestConnectHintShownThenSuppressed(t *testing.T) {
	dir := t.TempDir()
	env := []string{}
	var buf bytes.Buffer
	// First N connects show the hint; after that it goes quiet.
	shows := 0
	for i := 0; i < connectHintMaxShows+2; i++ {
		buf.Reset()
		maybePrintConnectHint(&buf, dir, env, 0)
		if strings.Contains(buf.String(), "Ctrl-^") {
			shows++
		}
	}
	if shows != connectHintMaxShows {
		t.Fatalf("hint shown %d times, want %d", shows, connectHintMaxShows)
	}
}

func TestConnectHintGated(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	// Depth > 0 (nested) -> no hint.
	maybePrintConnectHint(&buf, dir, nil, 1)
	if buf.Len() != 0 {
		t.Fatalf("nested session should get no hint: %q", buf.String())
	}
	// Silenced via env.
	buf.Reset()
	maybePrintConnectHint(&buf, dir, []string{"SSHERPA_NO_CONNECT_HINT=1"}, 0)
	if buf.Len() != 0 {
		t.Fatalf("silenced hint should print nothing: %q", buf.String())
	}
}
