package cli

import (
	"testing"

	"github.com/0xbenc/ssherpa/internal/state"
)

func TestSessionProjectionIncludesMuxer(t *testing.T) {
	out := sessionProjection(state.SessionRecord{ID: "x", Muxer: &state.MuxerSpec{Type: "tmux", Detached: true}})
	if out.Muxer == nil || out.Muxer.Type != "tmux" || !out.Muxer.Detached {
		t.Fatalf("projection Muxer = %#v, want {tmux true}", out.Muxer)
	}
	absent := sessionProjection(state.SessionRecord{ID: "y"})
	if absent.Muxer != nil {
		t.Fatalf("projection Muxer = %#v, want nil when absent", absent.Muxer)
	}
}
