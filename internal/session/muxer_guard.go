package session

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

const (
	// defaultMuxerGuardInterval is how often the guard polls the
	// multiplexer for its attached-client state. Short enough that a lost
	// upstream link is noticed within a few seconds, long enough that the
	// per-tick `tmux list-clients` exec is negligible.
	defaultMuxerGuardInterval = 2 * time.Second
	// defaultMuxerGuardProbeTimeout bounds a single attach query so a
	// wedged or hostile tmux can never stall the guard goroutine; a
	// timed-out probe is treated as "unknown this tick" (no action).
	defaultMuxerGuardProbeTimeout = 2 * time.Second
	// muxerGuardDebounce is how many consecutive polls a classification
	// must hold before the guard acts. Applied symmetrically: a drop is
	// only torn down after two "pinned pts gone" polls (so a pts that
	// lingers one tick after a real drop is still caught), and a deliberate
	// detach is only latched after two "pinned pts alive but client gone"
	// polls (so a drop whose pts momentarily survives is never mislatched
	// as a detach and spared forever).
	muxerGuardDebounce = 2
	// muxerDetachedEvent marks the SessionEvent recorded when the guard
	// concludes the multiplexer client left deliberately while the upstream
	// link stayed alive — the session is intentionally left running for
	// reattach and the guard stops watching it.
	muxerDetachedEvent = "muxer_client_detached"
)

// muxerAttachState is one poll's answer to "which clients are attached to
// my multiplexer session". Clients holds each attached client's terminal
// (for tmux, #{client_tty} — the live sshd login pts). OK is false when
// the query could not be answered this tick (timeout, exit!=0, oversized
// or unparseable output); on !OK the guard takes no destructive action.
type muxerAttachState struct {
	Clients []string
	OK      bool
}

// muxerAttachProbe answers muxerAttachState for the current session. The
// production implementation shells out to tmux; tests inject a closure so
// the state machine is exercised deterministically with no real tmux.
type muxerAttachProbe func(ctx context.Context) muxerAttachState

// ttyStatFunc returns the identity (inode + device) of a terminal device
// path, or ok=false if the path is not a live pts character device. The
// (ino, rdev) pair distinguishes a re-used /dev/pts slot from the original
// (a reused pts keeps the same Rdev but gets a fresh Ino). Injectable so
// tests need no real device nodes.
type ttyStatFunc func(path string) (ino uint64, rdev uint64, ok bool)

// muxerGuardConfig bundles everything the multiplexer upstream guard
// needs. Probe and Stat are injectable (mirroring WatchdogOptions.RunProbe)
// so the state machine is unit tested with neither a real tmux nor real
// device nodes. Teardown performs the actual ssh-child teardown + record
// finalization (RunSupervised's pullMuxerRope) and is invoked at most once.
type muxerGuardConfig struct {
	Interval     time.Duration
	ProbeTimeout time.Duration
	Now          func() time.Time
	Probe        muxerAttachProbe
	Stat         ttyStatFunc
	Record       *state.SessionRecord
	RecordMu     *sync.Mutex
	StateDir     string
	Stderr       io.Writer
	OnPanic      func(string, any)
	Teardown     func()
}

// startMuxerGuard launches the guard goroutine and returns a stop function
// that cancels it and waits for it to exit. The stop function is
// idempotent (cancel is a no-op once called and the done channel stays
// closed), so RunSupervised can both call it synchronously before
// finalizing the record and defer it as a panic-path safety net. A config
// missing its injectable pieces yields a no-op guard.
func startMuxerGuard(cfg muxerGuardConfig) func() {
	if cfg.Probe == nil || cfg.Stat == nil || cfg.Teardown == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverSupervisorPanic("muxer guard", cfg.OnPanic)
		runMuxerGuard(ctx, cfg)
	}()
	return func() {
		cancel()
		<-done
	}
}

func runMuxerGuard(ctx context.Context, cfg muxerGuardConfig) {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultMuxerGuardInterval
	}
	probeTimeout := cfg.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = defaultMuxerGuardProbeTimeout
	}

	state := &muxerGuardState{}
	for {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		attach := cfg.Probe(probeCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}

		switch state.observe(attach, cfg.Stat) {
		case muxerActionDetached:
			cfg.markDetached(now)
		case muxerActionTeardown:
			cfg.Teardown()
			return
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// markDetached records, under the record lock, that the multiplexer client
// left deliberately. The session is left running (the guard has latched
// "protected" and will not tear it down), so the only effect is to flag the
// record for honest display and leave a breadcrumb for post-mortem.
func (cfg muxerGuardConfig) markDetached(now func() time.Time) {
	if cfg.RecordMu == nil || cfg.Record == nil {
		return
	}
	withLock(cfg.RecordMu, func() {
		if cfg.Record.Muxer != nil {
			// Copy-on-write: publish a fresh MuxerSpec rather than mutating
			// the shared one in place. A telemetry/WriteRecord copy
			// (recordForTelemetry := *record) shallow-copies the Muxer
			// pointer under the lock and may marshal it outside the lock, so
			// the pointed-to struct must be immutable once published.
			updated := *cfg.Record.Muxer
			updated.Detached = true
			cfg.Record.Muxer = &updated
		}
		cfg.Record.Events = append(cfg.Record.Events, state.SessionEvent{
			Time:    now().UTC(),
			Type:    muxerDetachedEvent,
			Message: "multiplexer client detached while the upstream link stayed alive; leaving this session for reattach",
		})
		_ = state.WriteRecord(cfg.StateDir, *cfg.Record)
	})
}

type muxerAction int

const (
	muxerActionNone muxerAction = iota
	muxerActionDetached
	muxerActionTeardown
)

// muxerGuardState is the discriminator. It pins the first attached client
// it can stat (the user's upstream login pts) and follows only that one,
// independent of how many other clients attach later. When the pinned
// client leaves the attached set it classifies by the pts's liveness:
// still present (matching identity) is a deliberate detach (spare and stop
// watching); gone or reused is the upstream link dying while we were
// attached (tear down). Both verdicts are debounced.
type muxerGuardState struct {
	armed      bool
	pinnedTTY  string
	pinnedIno  uint64
	pinnedRdev uint64
	protected  bool
	detachRun  int
	dropRun    int
}

func (g *muxerGuardState) observe(st muxerAttachState, stat ttyStatFunc) muxerAction {
	if g.protected {
		// A deliberate detach was already confirmed; the session is left
		// for reattach and is never torn down by the guard thereafter.
		return muxerActionNone
	}
	if !st.OK {
		// Unknown this tick (timeout / error / oversized output). Do not
		// advance either streak — a single bad probe must not nudge us
		// toward a teardown or a spurious detach latch.
		return muxerActionNone
	}
	if !g.armed {
		// Pin the first currently-attached client whose pts we can stat.
		// Until a client is observed there is no upstream to lose, so the
		// guard simply waits (a session started detached, e.g.
		// `tmux new-session -d`, never arms and is never torn down).
		for _, tty := range st.Clients {
			if ino, rdev, ok := stat(tty); ok {
				g.armed = true
				g.pinnedTTY = tty
				g.pinnedIno = ino
				g.pinnedRdev = rdev
				break
			}
		}
		return muxerActionNone
	}

	if containsString(st.Clients, g.pinnedTTY) {
		// Still attached through our pinned client: healthy, reset streaks.
		g.detachRun = 0
		g.dropRun = 0
		return muxerActionNone
	}

	// The pinned client is no longer attached. Classify by whether its
	// login pts is still the same live device.
	ino, rdev, ok := stat(g.pinnedTTY)
	alive := ok && ino == g.pinnedIno && rdev == g.pinnedRdev
	if alive {
		// pts still present and unchanged => you detached but are still
		// logged in at the host shell. Confirm across the debounce window
		// (so a drop whose pts lingers one tick is not mislatched), then
		// latch protected and leave the session for reattach.
		g.dropRun = 0
		g.detachRun++
		if g.detachRun >= muxerGuardDebounce {
			g.protected = true
			return muxerActionDetached
		}
		return muxerActionNone
	}

	// pts gone (or reused: same Rdev, different Ino) => the sshd session
	// that carried our client ended. This is the escape rope or a hard
	// drop while attached. Tear down after the debounce window.
	g.detachRun = 0
	g.dropRun++
	if g.dropRun >= muxerGuardDebounce {
		return muxerActionTeardown
	}
	return muxerActionNone
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
