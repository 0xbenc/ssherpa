package session

import (
	"context"
	"os"
	"path/filepath"
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
			_, ok := muxerGuardConfigFor(tt.deps)
			if ok != tt.want {
				t.Fatalf("muxerGuardConfigFor ok = %v, want %v", ok, tt.want)
			}
		})
	}
}
