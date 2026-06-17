package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionRecordMuxerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	id := "20240101T000000.000000000Z-aabbccdd"
	rec := SessionRecord{ID: id, StartedAt: time.Now().UTC(), Muxer: &MuxerSpec{Type: "tmux", Detached: true}}
	if err := WriteRecord(dir, rec); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	got, err := ReadRecord(dir, id)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if got.Muxer == nil || got.Muxer.Type != "tmux" || !got.Muxer.Detached {
		t.Fatalf("round-tripped Muxer = %#v, want {tmux true}", got.Muxer)
	}
}

func TestSessionRecordMuxerBackwardCompat(t *testing.T) {
	// A record JSON written before the field existed reads as a nil Muxer.
	var old SessionRecord
	if err := json.Unmarshal([]byte(`{"id":"x","state_version":1}`), &old); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if old.Muxer != nil {
		t.Fatalf("Muxer = %#v, want nil for a record without the field", old.Muxer)
	}
	// A record with no multiplexer omits the field entirely (omitempty), so
	// it cannot bloat existing records on disk.
	data, err := json.Marshal(SessionRecord{ID: "y"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "muxer") {
		t.Fatalf("marshaled record without a muxer contains the key: %s", data)
	}
}
