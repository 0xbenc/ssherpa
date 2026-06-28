// Package tailscale detects a logged-in Tailscale install and lists the
// tailnet devices a user can turn into an SSH alias. All process
// interaction goes through injectable seams (LookPath/Run) so the package
// is unit-testable from fixtures with no real tailnet.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Device is one pickable tailnet node.
type Device struct {
	// Name is the assigned device name (the first label of DNSName, e.g.
	// "my-laptop" from "my-laptop.tailnet.ts.net."). It is unique and
	// DNS-safe and is what we use as the SSH alias.
	Name string
	// HostName is the raw OS-reported hostname, kept for display only. It
	// can collide across devices or carry uppercase, so it is NEVER used
	// as the SSH HostName.
	HostName string
	// IPv4 is the stable Tailscale 100.x address; used as the SSH HostName.
	IPv4   string
	OS     string
	ID     string
	Online bool
	Self   bool
}

// Result is the outcome of a status probe.
type Result struct {
	LoggedIn     bool
	BackendState string
	Devices      []Device
}

// Options carries injectable seams and tuning. The zero value is usable:
// it shells out to the real `tailscale` binary with a default timeout and
// excludes the local node.
type Options struct {
	// LookPath resolves the tailscale binary; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Run executes a command and returns its stdout; defaults to
	// exec.CommandContext(...).Output().
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Timeout bounds the status call; defaults to defaultTimeout.
	Timeout time.Duration
	// IncludeSelf includes the local node in the device list.
	IncludeSelf bool
}

const defaultTimeout = 2 * time.Second

// Devices detects Tailscale and, when the user is logged in, returns the
// pickable device list. Any failure (binary absent, timeout, unparseable
// output) yields a zero Result so callers simply omit the feature.
func Devices(ctx context.Context, opts Options) Result {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("tailscale"); err != nil {
		// Binary absent: the feature is simply not offered.
		return Result{}
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run := opts.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}

	out, err := run(ctx2, "tailscale", "status", "--json")
	if err != nil {
		// A hung tailscaled must never stall `ssherpa add` past the timeout.
		if errors.Is(ctx2.Err(), context.DeadlineExceeded) {
			return Result{}
		}
		// exec.Output captures stdout even on a non-zero exit; some
		// tailscale versions exit non-zero in transitional states while
		// still emitting parseable JSON. Only give up if there is nothing
		// usable to parse.
		if len(out) == 0 {
			return Result{}
		}
	}
	return parseStatus(out, opts.IncludeSelf)
}

// tsStatus and tsPeer are decode-only views of `tailscale status --json`;
// unknown fields are ignored.
type tsStatus struct {
	BackendState string            `json:"BackendState"`
	Self         *tsPeer           `json:"Self"`
	Peer         map[string]tsPeer `json:"Peer"`
}

type tsPeer struct {
	HostName     string   `json:"HostName"`
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	Online       bool     `json:"Online"`
	OS           string   `json:"OS"`
	ID           string   `json:"ID"`
}

// isLoggedIn reports whether a BackendState means the user has
// authenticated to a tailnet. Running (up), Stopped (logged in but tunnel
// down), and Starting (coming up after login) all qualify: assigned
// Tailscale 100.x IPs are stable across these, so an alias built from them
// is valid. NeedsLogin/NoState/unknown do not qualify.
func isLoggedIn(state string) bool {
	switch state {
	case "Running", "Stopped", "Starting":
		return true
	default:
		return false
	}
}

// parseStatus is pure: it decodes status JSON into a deterministic Result.
func parseStatus(data []byte, includeSelf bool) Result {
	var status tsStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return Result{}
	}
	res := Result{
		BackendState: status.BackendState,
		LoggedIn:     isLoggedIn(status.BackendState),
	}
	if !res.LoggedIn {
		// Never surface a (possibly stale) cached peer list while logged out.
		return res
	}

	var devices []Device
	for _, peer := range status.Peer {
		if d, ok := deviceFromPeer(peer, false); ok {
			devices = append(devices, d)
		}
	}
	if includeSelf && status.Self != nil {
		if d, ok := deviceFromPeer(*status.Self, true); ok {
			devices = append(devices, d)
		}
	}
	sortDevices(devices)
	res.Devices = devices
	return res
}

func deviceFromPeer(peer tsPeer, self bool) (Device, bool) {
	ipv4 := firstIPv4(peer.TailscaleIPs)
	if ipv4 == "" {
		// No 100.x address to serve as the SSH HostName: not pickable.
		return Device{}, false
	}
	name := deviceName(peer)
	if name == "" {
		// No usable alias label: skip rather than fail late at validation.
		return Device{}, false
	}
	return Device{
		Name:     name,
		HostName: strings.TrimSpace(peer.HostName),
		IPv4:     ipv4,
		OS:       peer.OS,
		ID:       peer.ID,
		Online:   peer.Online,
		Self:     self,
	}, true
}

// firstIPv4 returns the first address that parses as IPv4, never assuming
// index 0 — the list may be IPv6-first across tailscale versions.
func firstIPv4(ips []string) string {
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
			return ip
		}
	}
	return ""
}

// deviceName returns the name Tailscale assigns: the first label of
// DNSName (deduplicated and DNS-safe), falling back to the raw OS HostName
// only when DNSName is empty.
func deviceName(peer tsPeer) string {
	dns := strings.TrimSuffix(strings.TrimSpace(peer.DNSName), ".")
	if dns != "" {
		if i := strings.IndexByte(dns, '.'); i >= 0 {
			dns = dns[:i]
		}
		if dns != "" {
			return dns
		}
	}
	return strings.TrimSpace(peer.HostName)
}

// sortDevices imposes a total order so output is deterministic despite
// randomized map iteration: online first, then case-insensitive name,
// then IPv4 as a deterministic tiebreaker.
func sortDevices(devices []Device) {
	sort.SliceStable(devices, func(i, j int) bool {
		a, b := devices[i], devices[j]
		if a.Online != b.Online {
			return a.Online
		}
		an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if an != bn {
			return an < bn
		}
		return a.IPv4 < b.IPv4
	})
}
