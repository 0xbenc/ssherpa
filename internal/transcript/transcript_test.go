package transcript

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterReadTextGrepAndReplay(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "session.cast")
	writer, spec, err := OpenWriter(WriterOptions{
		Path:    path,
		Started: started,
		Header:  Header{Title: "prod"},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if spec.Path != path || spec.Format != FormatAsciicast {
		t.Fatalf("spec = %#v", spec)
	}
	writer.WriteOutput(started.Add(500*time.Millisecond), []byte("\x1b[32mhello prod\x1b[0m\r\n"))
	writer.WriteMarker(started.Add(time.Second), "reconnect scheduled")
	writer.WriteOutput(started.Add(2*time.Second), []byte("error: disk full\n"))
	final, err := writer.Close(started.Add(3 * time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if final.Frames != 3 || final.Bytes <= 0 || final.EndedAt == nil {
		t.Fatalf("final spec = %#v", final)
	}

	rec, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	text := Text(rec, TextOptions{})
	if strings.Contains(text, "\x1b[") || !strings.Contains(text, "hello prod") || !strings.Contains(text, "error: disk full") {
		t.Fatalf("clean text = %q", text)
	}
	matches, err := Grep(rec, "disk", false)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Text != "error: disk full" {
		t.Fatalf("matches = %#v", matches)
	}
	var replay bytes.Buffer
	if err := Replay(&replay, rec, 1, true); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !strings.Contains(replay.String(), "\x1b[32mhello prod\x1b[0m") {
		t.Fatalf("replay = %q", replay.String())
	}
}

func TestWriterMaxBytesTruncates(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	writer, _, err := OpenWriter(WriterOptions{
		Path:     filepath.Join(dir, "tiny.cast"),
		Started:  started,
		Header:   Header{Title: "prod"},
		MaxBytes: 80,
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started, []byte(strings.Repeat("x", 1024)))
	final, err := writer.Close(started)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !final.Truncated {
		t.Fatalf("Truncated = false, want true; spec = %#v", final)
	}
}

func TestParseSize(t *testing.T) {
	got, err := ParseSize("2MB")
	if err != nil {
		t.Fatalf("ParseSize: %v", err)
	}
	if got != 2_000_000 {
		t.Fatalf("ParseSize = %d, want 2000000", got)
	}
}
