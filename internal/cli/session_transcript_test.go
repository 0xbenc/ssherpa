package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/transcript"
)

func TestRunSessionLogGrepAndExportTranscript(t *testing.T) {
	stateDir := t.TempDir()
	id := "20260604T120000.000000000Z-test"
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	path := transcript.Path(stateDir, id)
	writer, spec, err := transcript.OpenWriter(transcript.WriterOptions{
		Path:    path,
		Started: started,
		Header:  transcript.Header{Title: "prod"},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started, []byte("\x1b[31mpermission denied\x1b[0m\n"))
	spec, err = writer.Close(started.Add(time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	record := state.SessionRecord{
		ID:          id,
		TargetAlias: "prod",
		Route:       []string{"prod"},
		StartedAt:   started,
		EndedAt:     spec.EndedAt,
		RunnerMode:  "supervised",
		Transcript:  &spec,
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"session", "log", id, "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("log code = %d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); strings.Contains(got, "\x1b[") || !strings.Contains(got, "permission denied") {
		t.Fatalf("log stdout = %q", got)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"session", "grep", id, "permission", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("grep code = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "permission denied") {
		t.Fatalf("grep stdout = %q", stdout.String())
	}

	outPath := filepath.Join(t.TempDir(), "prod.txt")
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"session", "export", id, "--format", "text", "--output", outPath, "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("export code = %d stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile(export): %v", err)
	}
	if !strings.Contains(string(data), "permission denied") {
		t.Fatalf("export data = %q", string(data))
	}
}
