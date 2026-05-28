package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// keyPress builds a synthetic bubbletea KeyPressMsg for tests so we
// can drive the wizard's Update() directly without spinning up a
// tea.Program (which would need a TTY). Use this for printable
// characters (Text != "") and for special keys whose Code is a
// known constant like tea.KeyEnter.
func keyPress(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Text: text})
}

// keyPressCtrl mimics how a real terminal delivers a Ctrl+key event:
// the byte arrives without printable Text. Setting Text here would
// make Key.String() return just the literal character (e.g. "c")
// instead of "ctrl+c", and the Update switch wouldn't match.
func keyPressCtrl(c rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: c, Mod: tea.ModCtrl})
}

func keyPressShiftTab() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})
}

func newTestBuilder(t *testing.T) forwardBuilderModel {
	t.Helper()
	return newForwardBuilderModel(BuildForwardOptions{
		Aliases: []ForwardAlias{
			{Name: "pgbox", Description: "farmer@pg.example.com"},
			{Name: "bastion", Description: "farmer@bastion.example.com"},
			{Name: "redis", Description: "farmer@redis.example.com"},
		},
	}, termstyle.Theme{})
}

func updateBuilder(m forwardBuilderModel, msgs ...tea.Msg) forwardBuilderModel {
	for _, msg := range msgs {
		newModel, _ := m.Update(msg)
		m = newModel.(forwardBuilderModel)
	}
	return m
}

func typeText(text string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(text))
	for _, r := range text {
		msgs = append(msgs, keyPress(r, string(r)))
	}
	return msgs
}

func TestForwardBuilderHappyPath(t *testing.T) {
	m := newTestBuilder(t)
	msgs := typeText("pg")
	msgs = append(msgs, keyPress(tea.KeyEnter, ""))
	msgs = append(msgs, keyPress(tea.KeyEnter, "")) // local default 5432
	msgs = append(msgs, keyPress(tea.KeyEnter, "")) // remote default 127.0.0.1:5432
	msgs = append(msgs, keyPress(tea.KeyEnter, "")) // through skip
	msgs = append(msgs, keyPress(tea.KeyEnter, "")) // summary Run

	m = updateBuilder(m, msgs...)

	if m.canceled {
		t.Fatalf("builder canceled unexpectedly")
	}
	if m.result.Alias != "pgbox" {
		t.Fatalf("result.Alias = %q, want pgbox", m.result.Alias)
	}
	if m.result.LocalBind != "127.0.0.1" || m.result.LocalPort != 5432 {
		t.Fatalf("local = %s:%d, want 127.0.0.1:5432", m.result.LocalBind, m.result.LocalPort)
	}
	if m.result.RemoteHost != "127.0.0.1" || m.result.RemotePort != 5432 {
		t.Fatalf("remote = %s:%d, want 127.0.0.1:5432", m.result.RemoteHost, m.result.RemotePort)
	}
	if m.result.Through != "" {
		t.Fatalf("through = %q, want empty (skip selected)", m.result.Through)
	}
	if m.result.Action != ForwardActionRun {
		t.Fatalf("action = %q, want %q", m.result.Action, ForwardActionRun)
	}
}

func TestForwardBuilderThroughHopAndBackground(t *testing.T) {
	m := newTestBuilder(t)
	m = updateBuilder(m, append(typeText("pg"), keyPress(tea.KeyEnter, ""))...)
	// Replace default "5432" with "5433"
	for i := 0; i < 4; i++ {
		m = updateBuilder(m, keyPress(tea.KeyBackspace, ""))
	}
	m = updateBuilder(m, typeText("5433")...)
	m = updateBuilder(m, keyPress(tea.KeyEnter, ""))
	m = updateBuilder(m, keyPress(tea.KeyEnter, "")) // remote default
	// through: cursor on "(skip)", press down to first real alias (bastion)
	m = updateBuilder(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))
	// summary: down once to "Run in background", Enter
	m = updateBuilder(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))

	if m.canceled {
		t.Fatalf("builder canceled unexpectedly")
	}
	if m.result.LocalPort != 5433 {
		t.Fatalf("local port = %d, want 5433", m.result.LocalPort)
	}
	if m.result.Through != "bastion" {
		t.Fatalf("through = %q, want bastion", m.result.Through)
	}
	if m.result.Action != ForwardActionBackground {
		t.Fatalf("action = %q, want background", m.result.Action)
	}
}

func TestForwardBuilderRejectsInvalidLocal(t *testing.T) {
	m := newTestBuilder(t)
	m = updateBuilder(m, append(typeText("pg"), keyPress(tea.KeyEnter, ""))...)
	for i := 0; i < 4; i++ {
		m = updateBuilder(m, keyPress(tea.KeyBackspace, ""))
	}
	m = updateBuilder(m, typeText("99999")...)
	m = updateBuilder(m, keyPress(tea.KeyEnter, ""))

	if m.step != builderStepLocal {
		t.Fatalf("step = %d, want %d (should not advance on invalid input)", m.step, builderStepLocal)
	}
	if m.localError == "" {
		t.Fatalf("expected localError to be set")
	}
	if !strings.Contains(m.localError, "local port") {
		t.Fatalf("localError = %q, want substring 'local port'", m.localError)
	}
}

func TestForwardBuilderShiftTabGoesBack(t *testing.T) {
	m := newTestBuilder(t)
	m = updateBuilder(m, append(typeText("pg"), keyPress(tea.KeyEnter, ""))...)
	if m.step != builderStepLocal {
		t.Fatalf("setup: step = %d, want local", m.step)
	}
	m = updateBuilder(m, keyPressShiftTab())
	if m.step != builderStepDestination {
		t.Fatalf("step after shift+tab = %d, want destination", m.step)
	}
}

func TestForwardBuilderCancelEscape(t *testing.T) {
	m := newTestBuilder(t)
	m = updateBuilder(m, keyPress(tea.KeyEscape, ""))
	if !m.canceled {
		t.Fatalf("Esc should cancel the wizard")
	}
}

func TestForwardBuilderCancelCtrlC(t *testing.T) {
	m := newTestBuilder(t)
	m = updateBuilder(m, append(typeText("pg"), keyPress(tea.KeyEnter, ""))...)
	if m.step != builderStepLocal {
		t.Fatalf("setup: step != local, got %d", m.step)
	}
	m = updateBuilder(m, keyPressCtrl('c'))
	if !m.canceled {
		t.Fatalf("Ctrl+C should cancel the wizard from any step")
	}
}

func TestForwardResultSpecRendering(t *testing.T) {
	cases := []struct {
		name       string
		result     ForwardResult
		wantLocal  string
		wantRemote string
	}{
		{
			name:       "default loopback drops the bind",
			result:     ForwardResult{LocalBind: "127.0.0.1", LocalPort: 5432, RemoteHost: "127.0.0.1", RemotePort: 5432},
			wantLocal:  "5432",
			wantRemote: "127.0.0.1:5432",
		},
		{
			name:       "explicit bind survives",
			result:     ForwardResult{LocalBind: "0.0.0.0", LocalPort: 5433, RemoteHost: "db.internal", RemotePort: 5432},
			wantLocal:  "0.0.0.0:5433",
			wantRemote: "db.internal:5432",
		},
		{
			name:       "ipv6 host bracketed",
			result:     ForwardResult{LocalBind: "::1", LocalPort: 5432, RemoteHost: "::1", RemotePort: 5432},
			wantLocal:  "[::1]:5432",
			wantRemote: "[::1]:5432",
		},
		{
			name:       "empty bind treated as loopback",
			result:     ForwardResult{LocalBind: "", LocalPort: 5432, RemoteHost: "host", RemotePort: 22},
			wantLocal:  "5432",
			wantRemote: "host:22",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.result.LocalSpec(); got != c.wantLocal {
				t.Fatalf("LocalSpec = %q, want %q", got, c.wantLocal)
			}
			if got := c.result.RemoteSpec(); got != c.wantRemote {
				t.Fatalf("RemoteSpec = %q, want %q", got, c.wantRemote)
			}
		})
	}
}
