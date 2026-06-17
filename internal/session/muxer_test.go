package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

func TestDetectMuxer(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		wantKind string
		wantPane string
		wantOK   bool
	}{
		{name: "tmux", env: []string{"TMUX=/tmp/tmux-1000/default,5166,0", "TMUX_PANE=%2"}, wantKind: "tmux", wantPane: "%2", wantOK: true},
		{name: "tmux without pane", env: []string{"TMUX=/tmp/x,1,0"}, wantKind: "tmux", wantOK: true},
		{name: "screen", env: []string{"STY=4242.pts-3.host"}, wantKind: "screen", wantOK: true},
		{name: "tmux wins over screen", env: []string{"STY=1.a.b", "TMUX=/tmp/x,1,0", "TMUX_PANE=%0"}, wantKind: "tmux", wantPane: "%0", wantOK: true},
		{name: "empty tmux ignored", env: []string{"TMUX=", "STY="}, wantOK: false},
		{name: "none", env: []string{"PATH=/bin"}, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, pane, ok := detectMuxer(tt.env)
			if ok != tt.wantOK || kind != tt.wantKind || pane != tt.wantPane {
				t.Fatalf("detectMuxer = (%q,%q,%v), want (%q,%q,%v)", kind, pane, ok, tt.wantKind, tt.wantPane, tt.wantOK)
			}
		})
	}
}

func TestParseClientTTYs(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{name: "empty means zero clients", out: "", want: nil},
		{name: "single", out: "/dev/pts/2\n", want: []string{"/dev/pts/2"}},
		{name: "multiple", out: "/dev/pts/2\n/dev/pts/9\n", want: []string{"/dev/pts/2", "/dev/pts/9"}},
		{name: "blank lines dropped", out: "\n/dev/pts/2\n\n", want: []string{"/dev/pts/2"}},
		{name: "whitespace trimmed", out: "  /dev/pts/2  \n", want: []string{"/dev/pts/2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseClientTTYs(tt.out)
			if len(got) != len(tt.want) {
				t.Fatalf("parseClientTTYs(%q) = %#v, want %#v", tt.out, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseClientTTYs(%q)[%d] = %q, want %q", tt.out, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseClientTTYsCapsLines(t *testing.T) {
	var b []byte
	for i := 0; i < muxerProbeMaxLines+50; i++ {
		b = append(b, []byte("/dev/pts/2\n")...)
	}
	if got := parseClientTTYs(string(b)); len(got) > muxerProbeMaxLines {
		t.Fatalf("parseClientTTYs returned %d lines, want <= %d", len(got), muxerProbeMaxLines)
	}
}

func TestLooksLikePTSPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/dev/pts/2", true},
		{"/dev/pts/0", true},
		{"/dev/ttys003", true},
		{"/dev/null", false}, // char device but not a pts -> rejected
		{"/dev/pts/../null", false},
		{"/dev/pts//2", false}, // not clean
		{"dev/pts/2", false},   // not absolute
		{"/tmp/evil", false},
		{"", false},
		{"/dev/ptsx", false},
	}
	for _, tt := range tests {
		if got := looksLikePTSPath(tt.path); got != tt.want {
			t.Fatalf("looksLikePTSPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestTTYIdentityRejectsNonPTS(t *testing.T) {
	// A regular file is not a pts character device.
	reg := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(reg, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if _, _, ok := ttyIdentity(reg); ok {
		t.Fatal("ttyIdentity accepted a regular file")
	}
	// A path that is shaped like a pts but does not exist is rejected.
	if _, _, ok := ttyIdentity("/dev/pts/999999"); ok {
		t.Fatal("ttyIdentity accepted a non-existent pts path")
	}
	// A bogus non-pts path is rejected even if it exists (/dev/null).
	if _, _, ok := ttyIdentity("/dev/null"); ok {
		t.Fatal("ttyIdentity accepted /dev/null")
	}
}

func TestValidTmuxIDs(t *testing.T) {
	if !validTmuxPaneID("%0") || !validTmuxPaneID("%12") {
		t.Fatal("valid pane ids rejected")
	}
	for _, bad := range []string{"", "%", "0", "%x", "-0", "%-1", "$0"} {
		if validTmuxPaneID(bad) {
			t.Fatalf("validTmuxPaneID accepted %q", bad)
		}
	}
	if !validTmuxSessionID("$0") || !validTmuxSessionID("$7") {
		t.Fatal("valid session ids rejected")
	}
	for _, bad := range []string{"", "$", "0", "$x", "%0", "main"} {
		if validTmuxSessionID(bad) {
			t.Fatalf("validTmuxSessionID accepted %q", bad)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	c := &cappedBuffer{max: 4}
	n, _ := c.Write([]byte("ab"))
	if n != 2 || c.overflow {
		t.Fatalf("after 2 bytes: n=%d overflow=%v", n, c.overflow)
	}
	n, _ = c.Write([]byte("cdef"))
	if n != 4 || !c.overflow {
		t.Fatalf("after overflow write: n=%d overflow=%v", n, c.overflow)
	}
	if string(c.buf) != "abcd" {
		t.Fatalf("buf = %q, want capped to %q", c.buf, "abcd")
	}
}

func TestClampTelemetryDropsMuxer(t *testing.T) {
	rec := state.SessionRecord{ID: "20240101T000000.000000000Z-aabbccdd", Muxer: &state.MuxerSpec{Type: "tmux", Detached: true}}
	got, ok := clampTelemetryRecord(rec)
	if !ok {
		t.Fatal("clampTelemetryRecord rejected a valid record")
	}
	if got.Muxer != nil {
		t.Fatalf("clampTelemetryRecord kept Muxer = %#v, want dropped (machine-local)", got.Muxer)
	}
}

// writeFakeTmux drops an executable shell script named "tmux" into dir and
// returns its absolute path. The body switches on the tmux subcommand
// ($1 is display-message / list-clients), letting the production probe path
// run end-to-end against a deterministic stand-in instead of a real server.
func writeFakeTmux(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "tmux")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	return p
}

// TestResolveTmuxContextProductionPath drives the real (non-injected)
// resolution path: PATH lookup, the display-message session-id query, and a
// list-clients probe, asserting the #{session_id}/#{client_tty} format
// strings are parsed into a usable context and probe.
func TestResolveTmuxContextProductionPath(t *testing.T) {
	dir := t.TempDir()
	writeFakeTmux(t, dir, `case "$1" in
  display-message) printf '$3\n' ;;
  list-clients) printf '/dev/pts/2\n/dev/pts/7\n' ;;
  *) exit 2 ;;
esac
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stderr bytes.Buffer
	bin, sessionID, ok := resolveTmuxContext("%0", &stderr)
	if !ok {
		t.Fatalf("resolveTmuxContext ok=false (stderr=%q)", stderr.String())
	}
	if sessionID != "$3" {
		t.Fatalf("sessionID = %q, want %q", sessionID, "$3")
	}
	if !strings.HasSuffix(bin, "tmux") {
		t.Fatalf("resolved bin = %q, want the fake tmux", bin)
	}

	probe := buildTmuxAttachProbe(bin, sessionID)
	st := probe(context.Background())
	if !st.OK {
		t.Fatal("probe OK=false on a successful list-clients")
	}
	want := []string{"/dev/pts/2", "/dev/pts/7"}
	if len(st.Clients) != len(want) {
		t.Fatalf("probe clients = %#v, want %#v", st.Clients, want)
	}
	for i := range want {
		if st.Clients[i] != want[i] {
			t.Fatalf("probe clients[%d] = %q, want %q", i, st.Clients[i], want[i])
		}
	}
}

// TestResolveTmuxContextDegrades confirms every resolution failure mode
// returns ok=false (detect-only) rather than arming a guard on bad data.
func TestResolveTmuxContextDegrades(t *testing.T) {
	t.Run("invalid pane id", func(t *testing.T) {
		if _, _, ok := resolveTmuxContext("bogus", nil); ok {
			t.Fatal("accepted a malformed pane id")
		}
	})
	t.Run("tmux not on PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir()) // a dir with no tmux in it
		var stderr bytes.Buffer
		if _, _, ok := resolveTmuxContext("%0", &stderr); ok {
			t.Fatal("resolved a context with no tmux on PATH")
		}
		if !strings.Contains(stderr.String(), "tmux not found") {
			t.Fatalf("stderr = %q, want a 'tmux not found' note", stderr.String())
		}
	})
	t.Run("malformed session id", func(t *testing.T) {
		dir := t.TempDir()
		writeFakeTmux(t, dir, `[ "$1" = display-message ] && printf 'not-a-session\n'`)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if _, _, ok := resolveTmuxContext("%0", nil); ok {
			t.Fatal("accepted a session id that fails the $<digits> shape")
		}
	})
	t.Run("display-message exits non-zero", func(t *testing.T) {
		dir := t.TempDir()
		writeFakeTmux(t, dir, `exit 1`)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if _, _, ok := resolveTmuxContext("%0", nil); ok {
			t.Fatal("resolved a context when tmux exited non-zero")
		}
	})
}

// TestBuildTmuxAttachProbeDegrades exercises the probe's safe answers: a
// non-zero list-clients exit and oversized output both yield OK=false so the
// guard takes no destructive action that tick. The overflow case drives
// runMuxerCommand's cappedBuffer guard through a real exec.
func TestBuildTmuxAttachProbeDegrades(t *testing.T) {
	t.Run("list-clients failure", func(t *testing.T) {
		bin := writeFakeTmux(t, t.TempDir(), `exit 1`)
		if st := buildTmuxAttachProbe(bin, "$3")(context.Background()); st.OK {
			t.Fatal("probe OK=true when list-clients failed")
		}
	})
	t.Run("oversized output", func(t *testing.T) {
		bin := writeFakeTmux(t, t.TempDir(), `head -c 70000 /dev/zero | tr '\0' 'a'`)
		if st := buildTmuxAttachProbe(bin, "$3")(context.Background()); st.OK {
			t.Fatal("probe OK=true on output past the cap")
		}
	})
	t.Run("success parses clients", func(t *testing.T) {
		bin := writeFakeTmux(t, t.TempDir(), `printf '/dev/pts/2\n/dev/pts/7\n'`)
		st := buildTmuxAttachProbe(bin, "$3")(context.Background())
		if !st.OK || len(st.Clients) != 2 {
			t.Fatalf("probe = %+v, want OK with 2 clients", st)
		}
	})
}

// fakeTmuxContext provides a deterministic, mutable view of attached
// clients and live pts nodes shared between an injected attach probe and
// an injected tty stat, so the integration tests drive attach/detach/drop
// transitions without a real tmux server or device nodes.
type fakeTmuxContext struct {
	mu      sync.Mutex
	clients []string
	ttys    map[string][2]uint64
}

func (f *fakeTmuxContext) setClients(c ...string) {
	f.mu.Lock()
	f.clients = append([]string(nil), c...)
	f.mu.Unlock()
}

func (f *fakeTmuxContext) setTTY(path string, ino, rdev uint64) {
	f.mu.Lock()
	if f.ttys == nil {
		f.ttys = map[string][2]uint64{}
	}
	f.ttys[path] = [2]uint64{ino, rdev}
	f.mu.Unlock()
}

func (f *fakeTmuxContext) removeTTY(path string) {
	f.mu.Lock()
	delete(f.ttys, path)
	f.mu.Unlock()
}

func (f *fakeTmuxContext) probe(context.Context) muxerAttachState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return muxerAttachState{Clients: append([]string(nil), f.clients...), OK: true}
}

func (f *fakeTmuxContext) stat(path string) (uint64, uint64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.ttys[path]
	if !ok {
		return 0, 0, false
	}
	return id[0], id[1], true
}

func TestMuxerGuardConfigForGate(t *testing.T) {
	var mu sync.Mutex
	probe := func(context.Context) muxerAttachState { return muxerAttachState{OK: true} }
	stat := func(string) (uint64, uint64, bool) { return 0, 0, false }
	base := func(mut func(*muxerGuardDeps)) muxerGuardDeps {
		d := muxerGuardDeps{
			Kind:     muxerKindTmux,
			Pane:     "%0",
			Settings: MuxerGuardSettings{Probe: probe, Stat: stat},
			MetaKind: "",
			Record:   &state.SessionRecord{ParentID: "parent"},
			RecordMu: &mu,
			StateDir: "/run/ssherpa-state",
			Now:      time.Now,
			Teardown: func() {},
		}
		if mut != nil {
			mut(&d)
		}
		return d
	}
	tests := []struct {
		name string
		deps muxerGuardDeps
		want bool
	}{
		{name: "tmux nested interactive enables", deps: base(nil), want: true},
		{name: "force enables without ancestry", deps: base(func(d *muxerGuardDeps) {
			d.Record = &state.SessionRecord{}
			d.Settings.Force = true
			d.Settings.Probe = probe
			d.Settings.Stat = stat
		}), want: true},
		{name: "no ancestry disables", deps: base(func(d *muxerGuardDeps) { d.Record = &state.SessionRecord{} }), want: false},
		{name: "disabled flag", deps: base(func(d *muxerGuardDeps) { d.Settings.Disabled = true }), want: false},
		{name: "detached disables", deps: base(func(d *muxerGuardDeps) { d.Detached = true }), want: false},
		{name: "tunnel kind disables", deps: base(func(d *muxerGuardDeps) { d.MetaKind = state.KindTunnel }), want: false},
		{name: "proxy kind disables", deps: base(func(d *muxerGuardDeps) { d.MetaKind = state.KindProxy }), want: false},
		{name: "screen is detect-only", deps: base(func(d *muxerGuardDeps) { d.Kind = muxerKindScreen }), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, ok := muxerGuardConfigFor(tt.deps)
			if ok != tt.want {
				t.Fatalf("muxerGuardConfigFor ok = %v, want %v", ok, tt.want)
			}
			if !ok {
				return
			}
			// When the gate opens, the returned config must be fully
			// wired. startMuxerGuard silently no-ops the guard if Probe,
			// Stat or Teardown is nil, so a regression that opens the gate
			// with a missing dependency would disable the kill path without
			// any test noticing — assert the contract here. (Func values are
			// only comparable to nil in Go, hence the nil checks.)
			if cfg.Probe == nil {
				t.Error("config Probe is nil; startMuxerGuard would no-op the guard")
			}
			if cfg.Stat == nil {
				t.Error("config Stat is nil; startMuxerGuard would no-op the guard")
			}
			if cfg.Teardown == nil {
				t.Error("config Teardown is nil; startMuxerGuard would no-op the guard")
			}
			if cfg.Now == nil {
				t.Error("config Now is nil")
			}
			if cfg.Interval != defaultMuxerGuardInterval {
				t.Errorf("config Interval = %v, want default %v", cfg.Interval, defaultMuxerGuardInterval)
			}
			if cfg.Record != tt.deps.Record {
				t.Error("config Record not wired through to deps.Record")
			}
			if cfg.RecordMu != tt.deps.RecordMu {
				t.Error("config RecordMu not wired through to deps.RecordMu")
			}
			if cfg.StateDir != tt.deps.StateDir {
				t.Errorf("config StateDir = %q, want %q", cfg.StateDir, tt.deps.StateDir)
			}
		})
	}
}

// TestMuxerGuardConfigForInterval confirms a caller-supplied poll cadence
// survives the gate instead of being overwritten by the default.
func TestMuxerGuardConfigForInterval(t *testing.T) {
	var mu sync.Mutex
	const custom = 5 * time.Second
	cfg, ok := muxerGuardConfigFor(muxerGuardDeps{
		Kind: muxerKindTmux,
		Pane: "%0",
		Settings: MuxerGuardSettings{
			Interval: custom,
			Probe:    func(context.Context) muxerAttachState { return muxerAttachState{OK: true} },
			Stat:     func(string) (uint64, uint64, bool) { return 0, 0, false },
		},
		Record:   &state.SessionRecord{ParentID: "parent"},
		RecordMu: &mu,
		Now:      time.Now,
		Teardown: func() {},
	})
	if !ok {
		t.Fatal("gate did not open for a valid nested tmux session")
	}
	if cfg.Interval != custom {
		t.Fatalf("config Interval = %v, want caller override %v", cfg.Interval, custom)
	}
}
