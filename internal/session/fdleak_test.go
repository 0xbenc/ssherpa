package session

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
)

// countOpenFDs returns the number of open file descriptors below 1024
// for this process. Probing /dev/fd/<n> individually works on both
// macOS and Linux, where os.ReadDir("/dev/fd") can fail mid-iteration
// as its own directory fd churns the listing.
func countOpenFDs(t *testing.T) int {
	t.Helper()
	count := 0
	for fd := 0; fd < 1024; fd++ {
		if _, err := os.Stat(fmt.Sprintf("/dev/fd/%d", fd)); err == nil {
			count++
		}
	}
	return count
}

// TestAttemptOnceClosesPTYMasterOnFastPath guards the a112f3b
// regression: attemptOnce's fast path (output reader finished before
// the drain grace) returned without closing the PTY master, leaking
// one fd per reconnect attempt — a long-lived tunnel daemon bled fds
// until ssh could no longer spawn.
func TestAttemptOnceClosesPTYMasterOnFastPath(t *testing.T) {
	stateDir := t.TempDir()
	record := state.SessionRecord{
		ID:           "fd-leak",
		Kind:         state.KindTunnel,
		StartedAt:    time.Now().UTC(),
		LocalPID:     os.Getpid(),
		RunnerMode:   RunnerModeSupervised,
		StateVersion: state.StateVersion,
	}
	var recordMu sync.Mutex
	output := &lockedWriter{w: io.Discard}
	var startup atomic.Bool

	runAttempt := func() {
		t.Helper()
		ac := attemptContext{
			command:              sshcmd.Command{Argv: []string{"sh", "-c", "exit 0"}},
			stateDir:             stateDir,
			record:               &record,
			recordMu:             &recordMu,
			env:                  []string{"PATH=" + os.Getenv("PATH")},
			ptmxRef:              newPtmxRef(),
			procRef:              newProcRef(),
			output:               output,
			startupInterruptible: &startup,
			stderr:               io.Discard,
			now:                  time.Now,
		}
		if err := attemptOnce(ac); err != nil {
			t.Fatalf("attemptOnce returned error: %v", err)
		}
	}

	// Warm up once so any lazy fd initialization is out of the way.
	runAttempt()
	before := countOpenFDs(t)

	const attempts = 20
	for i := 0; i < attempts; i++ {
		runAttempt()
	}
	after := countOpenFDs(t)

	if leaked := after - before; leaked > 2 {
		t.Fatalf("open fds grew from %d to %d across %d attempts (leaked %d); attemptOnce must close the PTY master on every path", before, after, attempts, leaked)
	}
}
