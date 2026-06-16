package sessionview

import (
	"testing"

	"github.com/0xbenc/ssherpa/internal/state"
)

func TestMuxerSummary(t *testing.T) {
	tests := []struct {
		name   string
		record state.SessionRecord
		want   string
	}{
		{name: "none", record: state.SessionRecord{}, want: ""},
		{name: "empty type", record: state.SessionRecord{Muxer: &state.MuxerSpec{}}, want: ""},
		{name: "attached", record: state.SessionRecord{Muxer: &state.MuxerSpec{Type: "tmux"}}, want: "tmux"},
		{name: "detached", record: state.SessionRecord{Muxer: &state.MuxerSpec{Type: "tmux", Detached: true}}, want: "tmux  (detached)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MuxerSummary(tt.record); got != tt.want {
				t.Fatalf("MuxerSummary = %q, want %q", got, tt.want)
			}
		})
	}
}
