package cli

import (
	"reflect"
	"testing"

	"github.com/0xbenc/ssherpa/internal/tailscale"
	"github.com/0xbenc/ssherpa/internal/ui"
)

func TestMapTailscaleDevices(t *testing.T) {
	in := []tailscale.Device{
		{Name: "alpha", HostName: "ALPHA-PC", IPv4: "100.0.0.10", OS: "linux", ID: "a", Online: true},
		{Name: "beta", HostName: "beta", IPv4: "100.0.0.20", OS: "macOS", ID: "b", Online: false},
	}
	got := mapTailscaleDevices(in)
	want := []ui.TailscaleDevice{
		{Name: "alpha", IPv4: "100.0.0.10", OS: "linux", Online: true},
		{Name: "beta", IPv4: "100.0.0.20", OS: "macOS", Online: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mapTailscaleDevices:\n got %+v\nwant %+v", got, want)
	}
}

func TestDiscoverTailscaleSuppressedWithExplicitHost(t *testing.T) {
	// An explicit --host must suppress the picker so a pick cannot silently
	// overwrite the user-provided host (no exec is performed).
	loggedIn, devices := discoverTailscaleDevices("prod.example.com")
	if loggedIn || devices != nil {
		t.Fatalf("explicit host should suppress tailscale: loggedIn=%v devices=%v", loggedIn, devices)
	}
}
