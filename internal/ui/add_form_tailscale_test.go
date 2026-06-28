package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func tsModel(loggedIn bool, devices ...TailscaleDevice) addAliasModel {
	return newAddAliasModel(AddAliasOptions{
		TailscaleLoggedIn: loggedIn,
		TailscaleDevices:  devices,
	}, termstyle.Theme{})
}

var tsTwoDevices = []TailscaleDevice{
	{Name: "alpha", IPv4: "100.0.0.10", OS: "linux", Online: true},
	{Name: "beta", IPv4: "100.0.0.20", OS: "macOS", Online: false},
}

func TestTailscalePickAndRoute(t *testing.T) {
	m := tsModel(true, tsTwoDevices...)
	// ctrl+t opens the picker but stays on the host step (sub-mode, not a step).
	m = updateAddAlias(m, keyPressCtrl('t'))
	if m.hostMode != hostModeTailscale {
		t.Fatalf("ctrl+t did not enter tailscale sub-mode")
	}
	if m.step != addStepHost {
		t.Fatalf("step changed to %d; must stay on host", m.step)
	}
	// Move down to beta and select it.
	m = updateAddAlias(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))
	if m.hostBuf != "100.0.0.20" {
		t.Fatalf("hostBuf = %q, want the device IPv4", m.hostBuf)
	}
	if m.aliasBuf != "beta" {
		t.Fatalf("aliasBuf = %q, want the device name", m.aliasBuf)
	}
	if !m.aliasFromTailscale {
		t.Fatalf("aliasFromTailscale should be set after a pick")
	}
	if m.step != addStepUser {
		t.Fatalf("step = %d, want user (alias step skipped)", m.step)
	}
	if m.hostMode != hostModeText {
		t.Fatalf("hostMode should reset to text after a pick")
	}
}

func TestTailscaleFilterNarrows(t *testing.T) {
	m := tsModel(true, tsTwoDevices...)
	m = updateAddAlias(m, keyPressCtrl('t'))
	// Type "bet" to filter down to beta.
	m = updateAddAlias(m, keyPress('b', "b"), keyPress('e', "e"), keyPress('t', "t"))
	visible := m.visibleDevices()
	if len(visible) != 1 || visible[0].Name != "beta" {
		t.Fatalf("filter did not narrow to beta: %+v", visible)
	}
	if m.tsCursor != 0 {
		t.Fatalf("tsCursor should reset to 0 on filter, got %d", m.tsCursor)
	}
	// Enter selects the only visible device.
	m = updateAddAlias(m, keyPress(tea.KeyEnter, ""))
	if m.aliasBuf != "beta" || m.hostBuf != "100.0.0.20" {
		t.Fatalf("filtered pick wrong: alias=%q host=%q", m.aliasBuf, m.hostBuf)
	}
}

func TestTailscaleEscReturnsToTypingPreservingBuffer(t *testing.T) {
	for _, back := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"esc", keyPress(tea.KeyEscape, "")},
		{"shift+tab", keyPressShiftTab()},
	} {
		m := tsModel(true, tsTwoDevices...)
		// Type a host, open picker, then back out.
		m = updateAddAlias(m, keyPress('h', "h"), keyPress('q', "q"))
		typed := m.hostBuf
		m = updateAddAlias(m, keyPressCtrl('t'), back.key)
		if m.hostMode != hostModeText {
			t.Fatalf("%s: did not return to text mode", back.name)
		}
		if m.canceled {
			t.Fatalf("%s: must not cancel the form", back.name)
		}
		if m.hostBuf != typed {
			t.Fatalf("%s: host buffer not preserved: %q != %q", back.name, m.hostBuf, typed)
		}
	}
}

func TestTailscaleNotOfferedWhenLoggedOut(t *testing.T) {
	m := tsModel(false, tsTwoDevices...)
	m = updateAddAlias(m, keyPress('h', "h"))
	before := m.hostBuf
	m = updateAddAlias(m, keyPressCtrl('t'))
	if m.hostMode != hostModeText {
		t.Fatalf("ctrl+t must be inert when not logged in")
	}
	if m.hostBuf != before {
		t.Fatalf("host buffer changed: %q", m.hostBuf)
	}
	text := m.View().Content
	if strings.Contains(text, "ctrl+t") {
		t.Fatalf("host view must not advertise ctrl+t when logged out:\n%s", text)
	}
}

func TestTailscaleEmptyDeviceState(t *testing.T) {
	m := tsModel(true) // logged in, zero devices
	m = updateAddAlias(m, keyPressCtrl('t'))
	if m.hostMode != hostModeTailscale {
		t.Fatalf("picker should still open with an empty list")
	}
	text := m.View().Content
	if !strings.Contains(text, "no other devices to pick") {
		t.Fatalf("expected empty-state message:\n%s", text)
	}
	// Enter on an empty list is a no-op.
	m = updateAddAlias(m, keyPress(tea.KeyEnter, ""))
	if m.step != addStepHost || m.hostMode != hostModeTailscale {
		t.Fatalf("enter on empty list should be a no-op")
	}
}

func TestTailscaleInvalidAliasLandsOnAliasStep(t *testing.T) {
	// A device whose derived name fails validateAliasInput (contains a
	// space) must route to the editable alias step, not skip to user.
	m := tsModel(true, TailscaleDevice{Name: "bad name", IPv4: "100.0.0.30", OS: "linux", Online: true})
	m = updateAddAlias(m, keyPressCtrl('t'), keyPress(tea.KeyEnter, ""))
	if m.step != addStepAlias {
		t.Fatalf("step = %d, want alias step for an invalid derived alias", m.step)
	}
	if m.hostBuf != "100.0.0.30" {
		t.Fatalf("host should still be set: %q", m.hostBuf)
	}
}

func TestTailscaleHintAndFooterWhenAvailable(t *testing.T) {
	m := tsModel(true, tsTwoDevices...)
	text := m.View().Content
	if !strings.Contains(text, "pick from your Tailscale tailnet (2 devices)") {
		t.Fatalf("host hint should show the device count:\n%s", text)
	}
	if !strings.Contains(text, "ctrl+t tailscale") {
		t.Fatalf("footer should advertise ctrl+t when available:\n%s", text)
	}
	// In the picker the footer is truthful about esc/shift+tab and ctrl+c.
	m = updateAddAlias(m, keyPressCtrl('t'))
	pick := m.View().Content
	if !strings.Contains(pick, "back to typing") || !strings.Contains(pick, "ctrl+c quit") {
		t.Fatalf("picker footer should be truthful:\n%s", pick)
	}
}

func TestTailscaleDerivedAliasSurfacedOnUserStep(t *testing.T) {
	m := tsModel(true, tsTwoDevices...)
	m = updateAddAlias(m, keyPressCtrl('t'), keyPress(tea.KeyEnter, "")) // pick alpha
	text := m.View().Content
	if !strings.Contains(text, "alias: alpha (from Tailscale)") {
		t.Fatalf("user step should surface the derived alias:\n%s", text)
	}
	// Editing the alias clears the marker.
	m.step = addStepAlias
	m = updateAddAlias(m, keyPress('x', "x"))
	if m.aliasFromTailscale {
		t.Fatalf("editing the alias should clear the from-Tailscale marker")
	}
}
