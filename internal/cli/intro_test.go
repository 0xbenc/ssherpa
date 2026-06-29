package cli

import "testing"

func TestShouldPlayIntroPrecedence(t *testing.T) {
	// base is the canonical "first interactive launch on a TTY" case: the
	// intro plays because the current version was never seen.
	base := introDecision{
		IsTerminal:     true,
		LastSeen:       "",
		CurrentVersion: "v1.2.3",
	}

	tests := []struct {
		name string
		in   introDecision
		want bool
	}{
		{"first launch on tty plays", base, true},
		{
			"same version already seen stays quiet",
			func() introDecision { d := base; d.LastSeen = "v1.2.3"; return d }(),
			false,
		},
		{
			"new version replays",
			func() introDecision { d := base; d.LastSeen = "v1.0.0"; return d }(),
			true,
		},
		{
			"not a terminal never plays",
			func() introDecision { d := base; d.IsTerminal = false; return d }(),
			false,
		},
		{
			"select never plays",
			func() introDecision { d := base; d.Select = "prod"; return d }(),
			false,
		},
		{
			"print never plays",
			func() introDecision { d := base; d.Print = true; return d }(),
			false,
		},
		{
			"json never plays",
			func() introDecision { d := base; d.JSON = true; return d }(),
			false,
		},
		{
			"suppressed never plays even on first launch",
			func() introDecision { d := base; d.Suppressed = true; return d }(),
			false,
		},
		{
			"forced replays even when already seen",
			func() introDecision { d := base; d.LastSeen = "v1.2.3"; d.Forced = true; return d }(),
			true,
		},
		{
			"suppressed beats forced",
			func() introDecision { d := base; d.Forced = true; d.Suppressed = true; return d }(),
			false,
		},
		{
			"suppressed beats forced on non-tty too",
			func() introDecision {
				d := base
				d.Forced = true
				d.Suppressed = true
				d.IsTerminal = false
				return d
			}(),
			false,
		},
		{
			"forced still gated by tty",
			func() introDecision { d := base; d.Forced = true; d.IsTerminal = false; return d }(),
			false,
		},
		{
			"forced still gated by print",
			func() introDecision { d := base; d.Forced = true; d.Print = true; return d }(),
			false,
		},
		{
			"forced still gated by json",
			func() introDecision { d := base; d.Forced = true; d.JSON = true; return d }(),
			false,
		},
		{
			"forced still gated by select",
			func() introDecision { d := base; d.Forced = true; d.Select = "prod"; return d }(),
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPlayIntro(tt.in); got != tt.want {
				t.Fatalf("shouldPlayIntro(%#v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
