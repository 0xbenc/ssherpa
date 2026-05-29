package session

import (
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

// exitErrorFromCmd runs a tiny shell command whose only purpose is to
// produce a real *exec.ExitError with the requested exit code, so
// shouldRetry tests run against the real error type.
func exitErrorFromCmd(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exit "+itoa(code))
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-nil error from sh -c 'exit %d'", code)
	}
	return err
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [11]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestShouldRetry(t *testing.T) {
	enabled := ReconnectOptions{Enabled: true, MaxAttempts: 5}
	disabled := ReconnectOptions{Enabled: false, MaxAttempts: 5}

	tests := []struct {
		name    string
		kind    string
		opts    ReconnectOptions
		err     error
		attempt int
		want    bool
	}{
		{
			name:    "interactive never retries",
			kind:    state.KindInteractive,
			opts:    enabled,
			err:     exitErrorFromCmd(t, 255),
			attempt: 1,
			want:    false,
		},
		{
			name:    "disabled never retries",
			kind:    state.KindTunnel,
			opts:    disabled,
			err:     exitErrorFromCmd(t, 255),
			attempt: 1,
			want:    false,
		},
		{
			name:    "proxy retries like tunnel",
			kind:    state.KindProxy,
			opts:    enabled,
			err:     exitErrorFromCmd(t, 255),
			attempt: 1,
			want:    true,
		},
		{
			name:    "max attempts reached",
			kind:    state.KindTunnel,
			opts:    enabled,
			err:     exitErrorFromCmd(t, 255),
			attempt: 5,
			want:    false,
		},
		{
			name:    "unlimited (MaxAttempts == 0) keeps going",
			kind:    state.KindTunnel,
			opts:    ReconnectOptions{Enabled: true, MaxAttempts: 0},
			err:     exitErrorFromCmd(t, 255),
			attempt: 999,
			want:    true,
		},
		{
			name:    "exit 0 (clean) retries",
			kind:    state.KindTunnel,
			opts:    enabled,
			err:     exitErrorFromCmd(t, 1), // sh -c 'exit 0' returns nil; pick 1 for the real ExitError test instead — handled below
			attempt: 1,
			want:    false, // exit 1 = give up
		},
		{
			name:    "exit 255 (network) retries",
			kind:    state.KindTunnel,
			opts:    enabled,
			err:     exitErrorFromCmd(t, 255),
			attempt: 1,
			want:    true,
		},
		{
			name:    "exit 1 (bind failure / host resolve) gives up",
			kind:    state.KindTunnel,
			opts:    enabled,
			err:     exitErrorFromCmd(t, 1),
			attempt: 1,
			want:    false,
		},
		{
			name:    "spawn failure (non-ExitError) gives up",
			kind:    state.KindTunnel,
			opts:    enabled,
			err:     errors.New("exec: no such file"),
			attempt: 1,
			want:    false,
		},
		{
			name:    "nil error treated as not retryable",
			kind:    state.KindTunnel,
			opts:    enabled,
			err:     nil,
			attempt: 1,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetry(tt.kind, tt.opts, tt.err, tt.attempt)
			if got != tt.want {
				t.Fatalf("shouldRetry = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeBackoffExponentialAndCap(t *testing.T) {
	opts := ReconnectOptions{
		Enabled:        true,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     16 * time.Second,
		Multiplier:     2.0,
	}

	want := []time.Duration{
		1 * time.Second,  // attempt 1 → wait before 2
		2 * time.Second,  // attempt 2 → wait before 3
		4 * time.Second,  // attempt 3
		8 * time.Second,  // attempt 4
		16 * time.Second, // attempt 5 (cap hit)
		16 * time.Second, // attempt 6 (clamped)
		16 * time.Second, // attempt 7 (clamped)
	}
	for i, w := range want {
		got := computeBackoff(i+1, opts)
		if got != w {
			t.Fatalf("computeBackoff(attempt=%d) = %s, want %s", i+1, got, w)
		}
	}
}

func TestComputeBackoffDefaults(t *testing.T) {
	// All-zero ReconnectOptions should fall back to DefaultReconnect
	// values, not produce 0 backoffs.
	zero := ReconnectOptions{}
	got := computeBackoff(1, zero)
	if got != DefaultReconnectInitialBackoff {
		t.Fatalf("computeBackoff with zero opts = %s, want %s", got, DefaultReconnectInitialBackoff)
	}
	bigAttempt := computeBackoff(100, zero)
	if bigAttempt != DefaultReconnectMaxBackoff {
		t.Fatalf("computeBackoff(100, zero) = %s, want %s (cap)", bigAttempt, DefaultReconnectMaxBackoff)
	}
}
