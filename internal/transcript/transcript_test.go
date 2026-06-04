package transcript

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
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

func TestBundleExportImportClassifiesOrigin(t *testing.T) {
	sourceDir := t.TempDir()
	destDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	sourceIdentity, err := state.EnsureMachineIdentity(sourceDir, "v1.0.0", started)
	if err != nil {
		t.Fatalf("EnsureMachineIdentity(source): %v", err)
	}
	destIdentity, err := state.EnsureMachineIdentity(destDir, "v1.0.0", started)
	if err != nil {
		t.Fatalf("EnsureMachineIdentity(dest): %v", err)
	}
	record := state.SessionRecord{
		ID:          state.NewSessionID(started),
		TargetAlias: "prod",
		Route:       []string{"bastion", "prod"},
		StartedAt:   started,
		RunnerMode:  "supervised",
	}
	origin := state.RecordingOriginForIdentity(sourceIdentity, "v1.0.0")
	record.RecordedBy = &origin
	writer, spec, err := OpenWriter(WriterOptions{
		Path:    Path(sourceDir, record.ID),
		Started: started,
		Header:  Header{Title: "prod"},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started, []byte("hello from prod\n"))
	spec, err = writer.Close(started.Add(time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	record.Transcript = &spec
	if err := state.WriteRecord(sourceDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "prod.ssherpa-session")
	exported, err := ExportBundle(sourceDir, record, sourceIdentity, bundlePath, started)
	if err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	if exported.Manifest.SourceMachineID != sourceIdentity.MachineID || exported.Manifest.SourceSessionID != record.ID {
		t.Fatalf("manifest = %#v", exported.Manifest)
	}
	preview, err := PreviewBundle(bundlePath)
	if err != nil {
		t.Fatalf("PreviewBundle: %v", err)
	}
	if preview.Manifest.SourceMachineID != sourceIdentity.MachineID || preview.BundleSHA256 == "" {
		t.Fatalf("preview = %#v", preview)
	}
	imported, err := ImportBundle(destDir, bundlePath, destIdentity, started.Add(2*time.Second))
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	if imported.OriginClass != "imported_other" {
		t.Fatalf("OriginClass = %q, want imported_other", imported.OriginClass)
	}
	if imported.Record.ID == record.ID || imported.Record.Import.SourceSessionID != record.ID {
		t.Fatalf("imported record IDs = local %q source %q", imported.Record.ID, imported.Record.Import.SourceSessionID)
	}
	rec, err := Read(imported.TranscriptPath)
	if err != nil {
		t.Fatalf("Read(imported transcript): %v", err)
	}
	if !strings.Contains(Text(rec, TextOptions{}), "hello from prod") {
		t.Fatalf("imported transcript text = %q", Text(rec, TextOptions{}))
	}
}
