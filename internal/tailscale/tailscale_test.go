package tailscale

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// runningFixture exercises the interesting cases:
//   - peerA: online, normal IPv4-first.
//   - peerB: offline, IPv6-FIRST in TailscaleIPs (IPv4 must still be picked).
//   - peerC: offline, IPv6-ONLY (must be skipped — no 100.x HostName).
//   - peerD/peerE: same OS HostName "DESKTOP-XX" but distinct DNSName labels
//     (must yield distinct, deduplicated aliases).
const runningFixture = `{
  "BackendState": "Running",
  "Self": {
    "HostName": "self-box",
    "DNSName": "self-box.tailnet.ts.net.",
    "TailscaleIPs": ["100.100.100.100", "fd7a:115c:a1e0::1"],
    "Online": true,
    "OS": "linux",
    "ID": "self1"
  },
  "Peer": {
    "nodekey:a": {
      "HostName": "alpha",
      "DNSName": "alpha.tailnet.ts.net.",
      "TailscaleIPs": ["100.0.0.10", "fd7a:115c:a1e0::a"],
      "Online": true,
      "OS": "linux",
      "ID": "a"
    },
    "nodekey:b": {
      "HostName": "beta",
      "DNSName": "beta.tailnet.ts.net.",
      "TailscaleIPs": ["fd7a:115c:a1e0::b", "100.0.0.20"],
      "Online": false,
      "OS": "macOS",
      "ID": "b"
    },
    "nodekey:c": {
      "HostName": "gamma",
      "DNSName": "gamma.tailnet.ts.net.",
      "TailscaleIPs": ["fd7a:115c:a1e0::c"],
      "Online": false,
      "OS": "linux",
      "ID": "c"
    },
    "nodekey:d": {
      "HostName": "DESKTOP-XX",
      "DNSName": "delta.tailnet.ts.net.",
      "TailscaleIPs": ["100.0.0.40"],
      "Online": false,
      "OS": "windows",
      "ID": "d"
    },
    "nodekey:e": {
      "HostName": "DESKTOP-XX",
      "DNSName": "delta-1.tailnet.ts.net.",
      "TailscaleIPs": ["100.0.0.50"],
      "Online": false,
      "OS": "windows",
      "ID": "e"
    }
  }
}`

const needsLoginFixture = `{"BackendState":"NeedsLogin","Peer":{}}`

const stoppedFixture = `{
  "BackendState": "Stopped",
  "Peer": {
    "nodekey:z": {
      "HostName": "zeta",
      "DNSName": "zeta.tailnet.ts.net.",
      "TailscaleIPs": ["100.0.0.99"],
      "Online": false,
      "OS": "linux",
      "ID": "z"
    }
  }
}`

func runReturning(out string, err error) func(context.Context, string, ...string) ([]byte, error) {
	return func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), err
	}
}

func okLookPath(string) (string, error) { return "/usr/bin/tailscale", nil }

func deviceNames(devices []Device) []string {
	out := make([]string, len(devices))
	for i, d := range devices {
		out[i] = d.Name
	}
	return out
}

func TestDevicesBinaryAbsent(t *testing.T) {
	res := Devices(context.Background(), Options{
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Run:      runReturning(runningFixture, nil),
	})
	if res.LoggedIn || len(res.Devices) != 0 {
		t.Fatalf("expected empty result when binary absent, got %+v", res)
	}
}

func TestDevicesTimeout(t *testing.T) {
	res := Devices(context.Background(), Options{
		LookPath: okLookPath,
		Timeout:  10 * time.Millisecond,
		Run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if res.LoggedIn || len(res.Devices) != 0 {
		t.Fatalf("expected empty result on timeout, got %+v", res)
	}
}

func TestDevicesNeedsLogin(t *testing.T) {
	res := Devices(context.Background(), Options{LookPath: okLookPath, Run: runReturning(needsLoginFixture, nil)})
	if res.LoggedIn {
		t.Fatalf("NeedsLogin must report LoggedIn=false")
	}
	if len(res.Devices) != 0 {
		t.Fatalf("expected no devices when logged out, got %v", deviceNames(res.Devices))
	}
}

func TestDevicesStoppedIsLoggedIn(t *testing.T) {
	res := Devices(context.Background(), Options{LookPath: okLookPath, Run: runReturning(stoppedFixture, nil)})
	if !res.LoggedIn {
		t.Fatalf("Stopped must report LoggedIn=true (logged in, tunnel down)")
	}
	if got := deviceNames(res.Devices); !reflect.DeepEqual(got, []string{"zeta"}) {
		t.Fatalf("expected [zeta], got %v", got)
	}
	if res.Devices[0].Online {
		t.Fatalf("stopped-tailnet peer should be offline")
	}
}

func TestDevicesRunningMappingAndOrder(t *testing.T) {
	res := Devices(context.Background(), Options{LookPath: okLookPath, Run: runReturning(runningFixture, nil)})
	if !res.LoggedIn {
		t.Fatalf("Running must be logged in")
	}
	// Self excluded by default; gamma (IPv6-only) dropped; online-first then
	// case-insensitive name then IPv4 ordering.
	want := []string{"alpha", "beta", "delta", "delta-1"}
	if got := deviceNames(res.Devices); !reflect.DeepEqual(got, want) {
		t.Fatalf("ordering/filtering wrong:\n got %v\nwant %v", got, want)
	}

	byName := map[string]Device{}
	for _, d := range res.Devices {
		byName[d.Name] = d
	}
	if d := byName["beta"]; d.IPv4 != "100.0.0.20" {
		t.Fatalf("IPv4 must be selected by ParseIP, not index 0; got %q", d.IPv4)
	}
	if !byName["alpha"].Online || byName["beta"].Online {
		t.Fatalf("online flags not carried through correctly")
	}
	if byName["delta"].IPv4 != "100.0.0.40" || byName["delta-1"].IPv4 != "100.0.0.50" {
		t.Fatalf("duplicate-OS-hostname peers must keep distinct IPv4/aliases")
	}
}

func TestDevicesIncludeSelf(t *testing.T) {
	res := Devices(context.Background(), Options{LookPath: okLookPath, IncludeSelf: true, Run: runReturning(runningFixture, nil)})
	var foundSelf bool
	for _, d := range res.Devices {
		if d.Self {
			foundSelf = true
			if d.Name != "self-box" {
				t.Fatalf("self name = %q", d.Name)
			}
		}
	}
	if !foundSelf {
		t.Fatalf("expected self node when IncludeSelf=true; got %v", deviceNames(res.Devices))
	}
}

func TestDevicesMalformedJSON(t *testing.T) {
	res := Devices(context.Background(), Options{LookPath: okLookPath, Run: runReturning("{not json", nil)})
	if res.LoggedIn || len(res.Devices) != 0 {
		t.Fatalf("malformed JSON must yield empty result without panic, got %+v", res)
	}
}

func TestDevicesNonZeroExitWithValidJSON(t *testing.T) {
	// A non-zero exit but with valid JSON on stdout must still parse.
	res := Devices(context.Background(), Options{
		LookPath: okLookPath,
		Run:      runReturning(stoppedFixture, &exec.ExitError{}),
	})
	if !res.LoggedIn || len(res.Devices) != 1 {
		t.Fatalf("non-zero exit with valid JSON should still parse, got %+v", res)
	}
}

func TestDevicesNonZeroExitNoOutput(t *testing.T) {
	res := Devices(context.Background(), Options{
		LookPath: okLookPath,
		Run:      runReturning("", &exec.ExitError{}),
	})
	if res.LoggedIn || len(res.Devices) != 0 {
		t.Fatalf("non-zero exit with empty stdout must be empty, got %+v", res)
	}
}

func TestParseStatusDeterministicOrder(t *testing.T) {
	// Map iteration is randomized; parsing the same bytes repeatedly must
	// always produce identical ordering.
	first := deviceNames(parseStatus([]byte(runningFixture), false).Devices)
	for i := 0; i < 20; i++ {
		got := deviceNames(parseStatus([]byte(runningFixture), false).Devices)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("non-deterministic order: %v vs %v", got, first)
		}
	}
}
