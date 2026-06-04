package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
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

func TestRunSessionBundleExportImport(t *testing.T) {
	sourceDir := t.TempDir()
	destDir := t.TempDir()
	id := "20260604T121000.000000000Z-bundle"
	started := time.Date(2026, 6, 4, 12, 10, 0, 0, time.UTC)
	identity, err := state.EnsureMachineIdentity(sourceDir, "v1.2.3", started)
	if err != nil {
		t.Fatalf("EnsureMachineIdentity: %v", err)
	}
	path := transcript.Path(sourceDir, id)
	writer, spec, err := transcript.OpenWriter(transcript.WriterOptions{
		Path:    path,
		Started: started,
		Header:  transcript.Header{Title: "prod"},
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started, []byte("portable output\n"))
	spec, err = writer.Close(started.Add(time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	origin := state.RecordingOriginForIdentity(identity, "v1.2.3")
	record := state.SessionRecord{
		ID:          id,
		TargetAlias: "prod",
		Route:       []string{"prod"},
		StartedAt:   started,
		EndedAt:     spec.EndedAt,
		RunnerMode:  "supervised",
		Transcript:  &spec,
		RecordedBy:  &origin,
	}
	if err := state.WriteRecord(sourceDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "prod.ssherpa-session")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"session", "bundle", "export", id, "--output", bundlePath, "--state-dir", sourceDir}, &stdout, &stderr, BuildInfo{Version: "v1.2.3"})
	if code != 0 {
		t.Fatalf("bundle export code = %d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"session", "bundle", "import", bundlePath, "--state-dir", destDir}, &stdout, &stderr, BuildInfo{Version: "v1.2.3"})
	if code != 0 {
		t.Fatalf("bundle import code = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "imported_other") {
		t.Fatalf("import stdout = %q", stdout.String())
	}
	records, err := state.ListRecords(destDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 || records[0].Import == nil || records[0].Import.SourceSessionID != id {
		t.Fatalf("imported records = %#v", records)
	}
}

func TestTranscriptActionItemsOmitRedundantCleanReplay(t *testing.T) {
	items := transcriptActionItems(state.SessionRecord{})
	tokens := make([]string, 0, len(items))
	for _, item := range items {
		tokens = append(tokens, item.Token)
	}
	if containsToken(tokens, "replay") {
		t.Fatalf("tokens = %#v, want no cleaned replay action in the TUI", tokens)
	}
	for _, want := range []string{"view", "replay-raw", "export-bundle", "metadata", "back"} {
		if !containsToken(tokens, want) {
			t.Fatalf("tokens = %#v, missing %q", tokens, want)
		}
	}

	imported := transcriptActionItems(state.SessionRecord{Import: &state.ImportSpec{OriginClass: "imported_other"}})
	importedTokens := make([]string, 0, len(imported))
	for _, item := range imported {
		importedTokens = append(importedTokens, item.Token)
	}
	if !containsToken(importedTokens, "remove-import") {
		t.Fatalf("imported tokens = %#v, want remove-import", importedTokens)
	}
}

func TestReplayRawControlledRestartBackAndCtrlC(t *testing.T) {
	rec := transcript.Recording{Frames: []transcript.Frame{
		{Offset: 0, Stream: "o", Data: "one"},
		{Offset: 1, Stream: "o", Data: "two"},
	}}

	var restarted bytes.Buffer
	err := replayRawControlled(&restarted, rec, &scriptedReplayKeys{
		events: []scriptedReplayKey{{}, {key: replayOverlayHotkey, ok: true}},
	}, replayControlOptions{
		NoDelay: true,
		ShowOverlay: func(replayProgress) replayCommand {
			return replayCommandRestart
		},
	})
	if err != nil {
		t.Fatalf("restart replay returned error: %v", err)
	}
	if got := termstyle.Strip(restarted.String()); got != "oneonetwo" {
		t.Fatalf("restart replay output = %q, want first frame then restarted replay", got)
	}

	var backed bytes.Buffer
	err = replayRawControlled(&backed, rec, &scriptedReplayKeys{
		events: []scriptedReplayKey{{}, {key: replayOverlayHotkey, ok: true}},
	}, replayControlOptions{
		NoDelay: true,
		ShowOverlay: func(replayProgress) replayCommand {
			return replayCommandBack
		},
	})
	if err != nil {
		t.Fatalf("back replay returned error: %v", err)
	}
	if got := termstyle.Strip(backed.String()); got != "one" {
		t.Fatalf("back replay output = %q, want only first frame before returning to menus", got)
	}

	var interrupted bytes.Buffer
	err = replayRawControlled(&interrupted, rec, &scriptedReplayKeys{
		events: []scriptedReplayKey{{}, {key: 0x03, ok: true}},
	}, replayControlOptions{NoDelay: true})
	if err != nil {
		t.Fatalf("ctrl-c replay returned error: %v", err)
	}
	if got := termstyle.Strip(interrupted.String()); got != "one" {
		t.Fatalf("ctrl-c replay output = %q, want only first frame before stop", got)
	}
}

type scriptedReplayKey struct {
	key byte
	ok  bool
}

type scriptedReplayKeys struct {
	events []scriptedReplayKey
}

func (s *scriptedReplayKeys) ReadKey() (byte, bool, error) {
	if len(s.events) == 0 {
		return 0, false, nil
	}
	event := s.events[0]
	s.events = s.events[1:]
	return event.key, event.ok, nil
}

func containsToken(tokens []string, want string) bool {
	for _, token := range tokens {
		if token == want {
			return true
		}
	}
	return false
}
