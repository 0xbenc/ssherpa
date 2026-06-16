package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

// Multiplexer kinds recorded on the session and used to gate the guard.
const (
	muxerKindTmux   = "tmux"
	muxerKindScreen = "screen"
)

// MuxerGuardSettings configures the terminal-multiplexer upstream guard
// for an interactive supervised session. The zero value is "enabled with
// defaults". Probe and Stat are test hooks; production leaves them nil and
// the supervisor wires the real tmux probe and tty stat.
type MuxerGuardSettings struct {
	// Disabled turns the guard off entirely (--no-muxer-guard or
	// SSHERPA_MUXER_GUARD in {0,false,no,off}). The session is still
	// tagged with its muxer kind for display.
	Disabled bool
	// Force lets the kill path engage even when ssherpa cannot prove it is
	// nested under an ssherpa parent (no SSHERPA_* lineage forwarded). It
	// is the user's explicit opt-in to the risk of tearing down an
	// outermost session running in a local tmux on an ssh-reached host.
	Force bool
	// Interval overrides the poll cadence; zero uses defaultMuxerGuardInterval.
	Interval time.Duration
	// Probe and Stat replace the production tmux probe and tty stat in tests.
	Probe muxerAttachProbe
	Stat  ttyStatFunc
}

// detectMuxer reports the terminal multiplexer the session is running
// inside, derived from the launching shell's environment. tmux exports
// $TMUX (and $TMUX_PANE); GNU screen exports $STY. Detection is presence
// only — the values are frozen at muxer-server launch and must not be used
// for any liveness decision.
func detectMuxer(env []string) (kind string, pane string, ok bool) {
	if strings.TrimSpace(sessionEnvValue(env, "TMUX")) != "" {
		return muxerKindTmux, strings.TrimSpace(sessionEnvValue(env, "TMUX_PANE")), true
	}
	if strings.TrimSpace(sessionEnvValue(env, "STY")) != "" {
		return muxerKindScreen, "", true
	}
	return "", "", false
}

// muxerGuardDeps carries the supervisor-side inputs to the guard gate.
type muxerGuardDeps struct {
	Env      []string
	Kind     string
	Pane     string
	Settings MuxerGuardSettings
	MetaKind string
	Detached bool
	Record   *state.SessionRecord
	RecordMu *sync.Mutex
	StateDir string
	Now      func() time.Time
	Stderr   io.Writer
	OnPanic  func(string, any)
	Teardown func()
}

// muxerGuardConfigFor decides whether the kill path engages and, if so,
// builds the guard config. It returns ok=false (no guard, the session is
// merely tagged) when:
//   - the guard is disabled, or
//   - the session is non-interactive / detached (tunnels, proxies, the
//     background daemon — they have no terminal a muxer could hold), or
//   - the muxer is not tmux (screen and others are detect-only in v1; no
//     reliable current-client tty query exists for them), or
//   - ssherpa cannot prove ssherpa ancestry (ParentID empty) and Force is
//     not set — without that proof a genuinely-nested session is
//     indistinguishable from an outermost session running in a local tmux
//     on an ssh-reached host, and tearing the latter down on a drop would
//     defeat the whole point of the muxer, or
//   - the live tmux context cannot be resolved (tmux not on PATH, bad pane id).
func muxerGuardConfigFor(d muxerGuardDeps) (muxerGuardConfig, bool) {
	if d.Settings.Disabled {
		return muxerGuardConfig{}, false
	}
	if d.Detached || !interactiveSessionKind(d.MetaKind) {
		return muxerGuardConfig{}, false
	}
	if d.Kind != muxerKindTmux {
		return muxerGuardConfig{}, false
	}
	if !d.Settings.Force && d.Record != nil && strings.TrimSpace(d.Record.ParentID) == "" {
		return muxerGuardConfig{}, false
	}

	probe := d.Settings.Probe
	stat := d.Settings.Stat
	if probe == nil {
		bin, sessionID, ok := resolveTmuxContext(d.Pane, d.Stderr)
		if !ok {
			return muxerGuardConfig{}, false
		}
		probe = buildTmuxAttachProbe(bin, sessionID)
	}
	if stat == nil {
		stat = ttyIdentity
	}

	interval := d.Settings.Interval
	if interval <= 0 {
		interval = defaultMuxerGuardInterval
	}
	return muxerGuardConfig{
		Interval: interval,
		Now:      d.Now,
		Probe:    probe,
		Stat:     stat,
		Record:   d.Record,
		RecordMu: d.RecordMu,
		StateDir: d.StateDir,
		Stderr:   d.Stderr,
		OnPanic:  d.OnPanic,
		Teardown: d.Teardown,
	}, true
}

// resolveTmuxContext resolves the tmux binary and our own session id once,
// up front. The binary is taken from PATH (the same tmux the user is
// already attached through) and stored absolute; the session id is read
// via the validated pane id and must match tmux's own `$<digits>` form.
// Any failure means the guard runs detect-only.
func resolveTmuxContext(pane string, stderr io.Writer) (bin string, sessionID string, ok bool) {
	if !validTmuxPaneID(pane) {
		return "", "", false
	}
	bin, err := exec.LookPath("tmux")
	if err != nil {
		if stderr != nil {
			fmt.Fprintln(stderr, "ssherpa: muxer guard inactive: tmux not found on PATH")
		}
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultMuxerGuardProbeTimeout)
	defer cancel()
	out, err := runMuxerCommand(ctx, bin, "display-message", "-p", "-t", pane, "#{session_id}")
	if err != nil {
		return "", "", false
	}
	sessionID = strings.TrimSpace(out)
	if !validTmuxSessionID(sessionID) {
		return "", "", false
	}
	return bin, sessionID, true
}

// buildTmuxAttachProbe returns a probe that lists the client ttys attached
// to our session. Empty output (exit 0) means zero clients attached; a
// non-zero exit (no server / session gone) or oversized output yields
// OK=false so the guard takes no action that tick.
func buildTmuxAttachProbe(bin string, sessionID string) muxerAttachProbe {
	return func(ctx context.Context) muxerAttachState {
		out, err := runMuxerCommand(ctx, bin, "list-clients", "-t", sessionID, "-F", "#{client_tty}")
		if err != nil {
			return muxerAttachState{OK: false}
		}
		return muxerAttachState{Clients: parseClientTTYs(out), OK: true}
	}
}

const (
	muxerProbeMaxBytes = 64 * 1024
	muxerProbeMaxLines = 256
)

// runMuxerCommand runs a multiplexer query with a bounded stdout and a
// discarded stderr. Output past muxerProbeMaxBytes is an error: a hostile
// multiplexer on a nested host must not be able to balloon the guard's
// memory or hang it (the caller passes a timeout context).
func runMuxerCommand(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	out := &cappedBuffer{max: muxerProbeMaxBytes}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	if out.overflow {
		return "", fmt.Errorf("muxer command output exceeded %d bytes", muxerProbeMaxBytes)
	}
	return string(out.buf), nil
}

func parseClientTTYs(out string) []string {
	var clients []string
	for i, line := range strings.Split(out, "\n") {
		if i >= muxerProbeMaxLines {
			break
		}
		tty := strings.TrimSpace(line)
		if tty != "" {
			clients = append(clients, tty)
		}
	}
	return clients
}

// ttyIdentity returns the (inode, device) identity of a terminal device
// path, or ok=false unless the path is an absolute, non-symlink pts
// character device. The pts-shape and char-device checks keep a hostile
// tmux from defeating teardown by returning a trivially-stable path
// (/dev/null, a regular file, a symlink it controls): such a path fails
// the checks, yields ok=false, and is treated as "upstream gone" rather
// than a fake deliberate detach. The (Ino,Rdev) pair is read via uint64()
// conversion because Stat_t.Rdev is int32 on darwin and uint64 on linux.
func ttyIdentity(path string) (ino uint64, rdev uint64, ok bool) {
	if !looksLikePTSPath(path) {
		return 0, 0, false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return 0, 0, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Ino), uint64(st.Rdev), true
}

// looksLikePTSPath accepts only an absolute, already-clean path under the
// pts device tree (/dev/pts/ on linux, /dev/ttys on darwin) with no parent
// traversal — the universe of paths tmux's #{client_tty} legitimately
// reports for an ssh login.
func looksLikePTSPath(path string) bool {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return false
	}
	if strings.Contains(path, "..") {
		return false
	}
	return strings.HasPrefix(path, "/dev/pts/") || strings.HasPrefix(path, "/dev/ttys")
}

func validTmuxPaneID(s string) bool {
	return len(s) >= 2 && s[0] == '%' && allDigits(s[1:])
}

func validTmuxSessionID(s string) bool {
	return len(s) >= 2 && s[0] == '$' && allDigits(s[1:])
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// cappedBuffer collects writes up to max bytes, then silently discards the
// rest while latching overflow. It never errors on Write so the child
// process is not killed by a broken pipe mid-run; the caller checks
// overflow after the command completes.
type cappedBuffer struct {
	buf      []byte
	max      int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.overflow {
		return len(p), nil
	}
	if len(c.buf)+len(p) > c.max {
		c.overflow = true
		if room := c.max - len(c.buf); room > 0 {
			c.buf = append(c.buf, p[:room]...)
		}
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}
