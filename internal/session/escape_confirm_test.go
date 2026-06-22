package session

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestEscapeConfirmEnumeratesActiveDescendants(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_600_000_000, 0)
	ended := now
	pid := os.Getpid() // a live PID so ListRecords keeps these active
	write := func(r state.SessionRecord) {
		if err := state.WriteRecord(dir, r); err != nil {
			t.Fatalf("WriteRecord: %v", err)
		}
	}
	// root (current) -> c1 (active) -> gc (active); plus an exited child.
	write(state.SessionRecord{ID: "root", TargetAlias: "bastion", StartedAt: now, LocalPID: pid})
	write(state.SessionRecord{ID: "c1", ParentID: "root", TargetAlias: "prod-db1", StartedAt: now, LocalPID: pid})
	write(state.SessionRecord{ID: "gc", ParentID: "c1", TargetAlias: "db1-shadow", StartedAt: now, LocalPID: pid})
	write(state.SessionRecord{ID: "dead", ParentID: "root", TargetAlias: "old-exited", StartedAt: now, LocalPID: pid, EndedAt: &ended})

	var buf bytes.Buffer
	drawEscapeConfirm(&buf, nil, dir, "root", termstyle.TerminalTheme().WithNoColor(true))
	out := buf.String()

	if !strings.Contains(out, "2 supervised sessions") {
		t.Fatalf("confirm should count 2 active descendants:\n%s", out)
	}
	for _, want := range []string{"prod-db1", "db1-shadow"} {
		if !strings.Contains(out, want) {
			t.Fatalf("confirm missing active descendant %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "old-exited") {
		t.Fatalf("confirm must not count the exited session:\n%s", out)
	}
}
