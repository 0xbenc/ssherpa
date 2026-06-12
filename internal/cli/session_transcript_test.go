package cli

import (
	"bytes"
	"encoding/json"
	"math"
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

	// grep(1) convention: no match exits 1, an invalid pattern is an
	// error and exits 2 — the two must not be confusable.
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"session", "grep", id, "no-such-needle", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("grep no-match code = %d, want 1; stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"session", "grep", id, "[invalid", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("grep invalid-pattern code = %d, want 2; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "grep transcript:") {
		t.Fatalf("grep invalid-pattern stderr = %q", stderr.String())
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

func TestDeleteAllLocalDataRemovesStateDirectory(t *testing.T) {
	stateDir := t.TempDir()
	if err := state.WriteRecord(stateDir, state.SessionRecord{
		ID:         "delete-test",
		StartedAt:  time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
		RunnerMode: "supervised",
	}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if _, err := state.EnsureMachineIdentity(stateDir, "test", time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnsureMachineIdentity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "extra.txt"), []byte("local data"), 0o600); err != nil {
		t.Fatalf("write extra local data: %v", err)
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	result, err := deleteAllLocalData(stateDir, records)
	if err != nil {
		t.Fatalf("deleteAllLocalData: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("stop result = %#v, want no errors", result)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("state dir stat err = %v, want removed", err)
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

// writeSessionWithCast persists a session record plus a raw .cast file at the
// transcript's canonical path, bypassing transcript.Writer so tests can plant
// torn or garbled content.
func writeSessionWithCast(t *testing.T, stateDir string, record state.SessionRecord, cast string) string {
	t.Helper()
	path := transcript.Path(stateDir, record.ID)
	if record.Transcript == nil {
		record.Transcript = &state.TranscriptSpec{Path: path, Format: "asciicast-v2"}
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	if err := os.WriteFile(path, []byte(cast), 0o600); err != nil {
		t.Fatalf("write cast: %v", err)
	}
	return path
}

// TestRunSessionLogSalvagesTornTail pins the torn-tail consumer adoption
// (audit §15.4 critical): a transcript whose final line was torn by a crash
// must still render its complete frames with a warning instead of failing
// the whole recording.
func TestRunSessionLogSalvagesTornTail(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	writeSessionWithCast(t, stateDir, state.SessionRecord{
		ID:         "torn-tail",
		StartedAt:  started,
		EndedAt:    &ended,
		RunnerMode: "supervised",
	}, `{"version":2,"width":80,"height":24}`+"\n"+
		`[0.1,"o","salvaged output\r\n"]`+"\n"+
		`[0.2,"o","torn fra`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"session", "log", "torn-tail", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("session log returned %d, want salvage; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "transcript tail is incomplete; showing 1 frame(s)")
	assertContains(t, stdout.String(), "salvaged output")
}

func TestRunSessionLogSurfacesSkippedLines(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	writeSessionWithCast(t, stateDir, state.SessionRecord{
		ID:         "skipped-lines",
		StartedAt:  started,
		EndedAt:    &ended,
		RunnerMode: "supervised",
	}, `{"version":2,"width":80,"height":24}`+"\n"+
		`not json at all`+"\n"+
		`[0.2,"o","good frame\r\n"]`+"\n")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"session", "log", "skipped-lines", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("session log returned %d, want tolerant read; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "skipped 1 unparseable transcript line(s)")
	assertContains(t, stdout.String(), "good frame")
}

// TestFollowTranscriptLoopRetriesTransientReadFailures pins the follow-mode
// contract: the follower races the live writer, so a transient failed read
// retries on the next tick instead of exiting 1.
func TestFollowTranscriptLoopRetriesTransientReadFailures(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	record := state.SessionRecord{
		ID:         "follow-retry",
		StartedAt:  started,
		EndedAt:    &ended, // not alive: the loop ends after one good read
		RunnerMode: "supervised",
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	path := transcript.Path(stateDir, record.ID)

	var stdout, stderr bytes.Buffer
	ticks := 0
	code := followTranscriptLoop(&stdout, &stderr, stateDir, record, false, func() {
		ticks++
		if ticks == 3 {
			// The "writer" lands the file after two failed reads.
			content := `{"version":2,"width":80,"height":24}` + "\n" +
				`[0.1,"o","follow payload\r\n"]` + "\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write cast: %v", err)
			}
		}
		if ticks > 10 {
			t.Fatalf("follow loop did not terminate")
		}
	})
	if code != 0 {
		t.Fatalf("followTranscriptLoop returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if ticks != 3 {
		t.Fatalf("ticks = %d, want 2 retried failures then success", ticks)
	}
	assertContains(t, stdout.String(), "follow payload")
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want transient failures kept quiet", stderr.String())
	}
}

func TestFollowTranscriptLoopGivesUpAfterRepeatedFailures(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	record := state.SessionRecord{
		ID:         "follow-gone",
		StartedAt:  started,
		RunnerMode: "supervised",
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	// No cast file is ever written: every read fails.

	var stdout, stderr bytes.Buffer
	ticks := 0
	code := followTranscriptLoop(&stdout, &stderr, stateDir, record, false, func() {
		ticks++
		if ticks > followTranscriptMaxFailures+1 {
			t.Fatalf("follow loop kept retrying past the failure budget")
		}
	})
	if code != 1 {
		t.Fatalf("followTranscriptLoop returned %d, want 1 after persistent failures", code)
	}
	if ticks != followTranscriptMaxFailures {
		t.Fatalf("ticks = %d, want %d consecutive failures before giving up", ticks, followTranscriptMaxFailures)
	}
	assertContains(t, stderr.String(), "follow transcript")
}

// TestFollowTranscriptLoopToleratesTornTailWhileWriterAppends simulates the
// live race directly: the first read sees a torn frame, the writer completes
// it, and the follower emits the completed payload instead of aborting.
func TestFollowTranscriptLoopToleratesTornTailWhileWriterAppends(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	record := state.SessionRecord{
		ID:         "follow-torn",
		StartedAt:  started,
		EndedAt:    &ended,
		RunnerMode: "supervised",
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	path := transcript.Path(stateDir, record.ID)
	if err := os.WriteFile(path, []byte(`{"version":2,"width":80,"height":24}`+"\n"+
		`[0.1,"o","first frame\r\n"]`+"\n"+
		`[0.2,"o","second `), 0o600); err != nil {
		t.Fatalf("write torn cast: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := followTranscriptLoop(&stdout, &stderr, stateDir, record, false, func() {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("open cast for append: %v", err)
		}
		if _, err := file.WriteString(`frame\r\n"]` + "\n"); err != nil {
			t.Fatalf("complete torn frame: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close cast: %v", err)
		}
	})
	if code != 0 {
		t.Fatalf("followTranscriptLoop returned %d, want 0; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "second frame")
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want torn tail tolerated silently", stderr.String())
	}
}

// TestImportedRawEmitGuard pins the CLI half of the imported-raw guard the
// TUI already enforces: replay and `log --raw` of an IMPORTED transcript are
// unfiltered byte emitters, so without an interactive terminal to show the
// default-No danger confirm they must refuse. Local recordings and the
// cleaned log path stay unaffected.
func TestImportedRawEmitGuard(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	cast := `{"version":2,"width":80,"height":24}` + "\n" +
		`[0.1,"o","imported bytes\r\n"]` + "\n"
	writeSessionWithCast(t, stateDir, state.SessionRecord{
		ID:         "imported",
		StartedAt:  started,
		EndedAt:    &ended,
		RunnerMode: "supervised",
		Import: &state.ImportSpec{
			ImportedAt:      started,
			OriginClass:     "imported_other",
			SourceSessionID: "src",
			SourceMachineID: "feedface",
		},
	}, cast)
	writeSessionWithCast(t, stateDir, state.SessionRecord{
		ID:         "local",
		StartedAt:  started,
		EndedAt:    &ended,
		RunnerMode: "supervised",
	}, cast)

	// go test runs without a terminal on stdin, so the guard fails closed.
	t.Run("imported replay refuses", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"session", "replay", "imported", "--no-delay", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
		if code != 1 {
			t.Fatalf("session replay returned %d, want 1", code)
		}
		assertContains(t, stderr.String(), "refusing imported raw output")
		if strings.Contains(stdout.String(), "imported bytes") {
			t.Fatalf("raw bytes leaked despite refusal:\n%s", stdout.String())
		}
	})
	t.Run("imported log --raw refuses", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"session", "log", "imported", "--raw", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
		if code != 1 {
			t.Fatalf("session log --raw returned %d, want 1", code)
		}
		assertContains(t, stderr.String(), "refusing imported raw output")
		if strings.Contains(stdout.String(), "imported bytes") {
			t.Fatalf("raw bytes leaked despite refusal:\n%s", stdout.String())
		}
	})
	t.Run("imported cleaned log passes", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"session", "log", "imported", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
		if code != 0 {
			t.Fatalf("session log returned %d, want 0; stderr=%q", code, stderr.String())
		}
		assertContains(t, stdout.String(), "imported bytes")
	})
	t.Run("local replay passes", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"session", "replay", "local", "--no-delay", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
		if code != 0 {
			t.Fatalf("session replay returned %d, want 0; stderr=%q", code, stderr.String())
		}
		assertContains(t, stdout.String(), "imported bytes")
	})
}

// TestReplayFrameDelayClampsUntrustedOffsets pins the controlled-replay
// delay math against imported casts: absurd offsets clamp to the same 10s
// cap transcript.Replay uses, and NaN/negative offsets or non-positive
// speeds never poison the duration.
func TestReplayFrameDelayClampsUntrustedOffsets(t *testing.T) {
	tests := []struct {
		name     string
		offset   float64
		previous float64
		speed    float64
		noDelay  bool
		want     time.Duration
	}{
		{name: "normal delta", offset: 1.5, previous: 1.0, speed: 1, want: 500 * time.Millisecond},
		{name: "speed scales", offset: 2.0, previous: 1.0, speed: 2, want: 500 * time.Millisecond},
		{name: "absurd offset clamps", offset: 9.2e18, previous: 0, speed: 1, want: maxReplayFrameDelay},
		{name: "infinite offset clamps", offset: math.Inf(1), previous: 0, speed: 1, want: maxReplayFrameDelay},
		{name: "nan offset is zero", offset: math.NaN(), previous: 0, speed: 1, want: 0},
		{name: "negative delta is zero", offset: 1.0, previous: 2.0, speed: 1, want: 0},
		{name: "zero speed is zero", offset: 5.0, previous: 0, speed: 0, want: 0},
		{name: "negative speed is zero", offset: 5.0, previous: 0, speed: -1, want: 0},
		{name: "nan speed is zero", offset: 5.0, previous: 0, speed: math.NaN(), want: 0},
		{name: "tiny speed clamps", offset: 1.0, previous: 0, speed: 1e-12, want: maxReplayFrameDelay},
		{name: "no delay wins", offset: 9.2e18, previous: 0, speed: 1, noDelay: true, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := transcript.Frame{Offset: tt.offset, Stream: "o"}
			got := replayFrameDelay(frame, tt.previous, tt.speed, tt.noDelay)
			if got != tt.want {
				t.Fatalf("replayFrameDelay = %v, want %v", got, tt.want)
			}
			if got < 0 || got > maxReplayFrameDelay {
				t.Fatalf("replayFrameDelay = %v, outside [0, %v]", got, maxReplayFrameDelay)
			}
		})
	}
}

// TestPrintSessionRecordStripsEscapeInjectedFields pins the render-boundary
// sanitization for `session show`: every field a remote or an imported
// bundle controls is stripped of terminal escapes at print time, regardless
// of any parse-time cleaning upstream.
func TestPrintSessionRecordStripsEscapeInjectedFields(t *testing.T) {
	ended := time.Date(2026, 6, 4, 12, 30, 0, 0, time.UTC)
	exitCode := 1
	record := state.SessionRecord{
		ID:          "evil\x1b[2J",
		TargetAlias: "prod\x1b]0;owned\x07",
		Route:       []string{"bastion\x1b[2J", "prod\x1b[31m"},
		Hops:        []string{"bastion\x1bc"},
		SSHArgv:     []string{"ssh", "-o", "Proxy\x1b[9999;9999H"},
		StartedAt:   ended.Add(-time.Hour),
		EndedAt:     &ended,
		ExitCode:    &exitCode,
		RunnerMode:  "supervised\x1b[1m",
		Transcript: &state.TranscriptSpec{
			Path:       "/tmp/evil\x1b[2J.cast",
			Format:     "asciicast-v2\x1b[7m",
			Bytes:      2048,
			Frames:     3,
			StopReason: "write error: \x1b[31mdisk full",
		},
		RecordedBy: &state.RecordingOrigin{
			MachineID:      "machine\x1b[2J-1234567890",
			SSHerpaVersion: "v1\x1b[31m",
		},
		Import: &state.ImportSpec{
			ImportedAt:      ended.Add(-time.Minute),
			OriginClass:     "imported_other\x1b[2J",
			SourceSessionID: "src\x1b]0;t\x07-session",
			SourceMachineID: "feed\x1b[2Jface-1234567890",
			BundleSHA256:    "abc\x1b[31m123",
		},
		DisconnectReason: "latency\x1b[2J timeout",
		Events: []state.SessionEvent{
			{Time: ended.Add(-time.Minute), Type: "latency_warning\x1b[2J", Message: "probe\x1b]0;x\x07 slow"},
		},
	}

	var stdout bytes.Buffer
	printSessionRecord(&stdout, record)

	if strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("session show output contains a raw escape byte:\n%q", stdout.String())
	}
	for _, want := range []string{"prod", "bastion", "supervised", "imported_other", "latency", "probe", "disk full"} {
		assertContains(t, stdout.String(), want)
	}
}

// TestRunSessionListWarnsOnSkippedAndStaleRecords pins the tolerant-listing
// surfacing: one corrupt record file is reported and skipped (not fatal),
// and crashed local sessions finalized by the cleanup are counted.
func TestRunSessionListWarnsOnSkippedAndStaleRecords(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now().UTC()
	if err := state.WriteRecord(stateDir, state.SessionRecord{
		ID:          "crashed",
		TargetAlias: "prod",
		StartedAt:   now.Add(-2 * time.Hour),
		LocalPID:    99999999, // certainly dead
		RunnerMode:  "supervised",
	}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	corrupt := filepath.Join(state.SessionsDir(stateDir), "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"session", "list", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("session list returned %d, want tolerant listing; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "finalized 1 stale local session record(s)")
	assertContains(t, stderr.String(), "left 1 unreadable session record file(s) untouched")
	assertContains(t, stderr.String(), "skipping unreadable session record")
	assertContains(t, stderr.String(), corrupt)
	assertContains(t, stdout.String(), "crashed")
	if _, err := os.Stat(corrupt); err != nil {
		t.Fatalf("corrupt record file was removed: %v", err)
	}
}

// TestRunSessionBundleWarningsSurfaceTornTranscript pins the Warning
// plumbing end to end: exporting a torn recording warns instead of
// certifying poison, and importing that bundle warns the recipient.
func TestRunSessionBundleWarningsSurfaceTornTranscript(t *testing.T) {
	sourceDir := t.TempDir()
	destDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 10, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	if _, err := state.EnsureMachineIdentity(sourceDir, "v1.2.3", started); err != nil {
		t.Fatalf("EnsureMachineIdentity: %v", err)
	}
	writeSessionWithCast(t, sourceDir, state.SessionRecord{
		ID:          "torn-bundle",
		TargetAlias: "prod",
		StartedAt:   started,
		EndedAt:     &ended,
		RunnerMode:  "supervised",
	}, `{"version":2,"width":80,"height":24}`+"\n"+
		`[0.1,"o","complete frame\r\n"]`+"\n"+
		`[0.2,"o","torn fra`)

	bundlePath := filepath.Join(t.TempDir(), "torn.ssherpa-session")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"session", "bundle", "export", "torn-bundle", "--output", bundlePath, "--state-dir", sourceDir}, &stdout, &stderr, BuildInfo{Version: "v1.2.3"})
	if code != 0 {
		t.Fatalf("bundle export returned %d, want 0; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "ssherpa: warning:")
	assertContains(t, stderr.String(), "transcript tail is incomplete")

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"session", "bundle", "import", bundlePath, "--state-dir", destDir}, &stdout, &stderr, BuildInfo{Version: "v1.2.3"})
	if code != 0 {
		t.Fatalf("bundle import returned %d, want 0; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "ssherpa: warning:")
	assertContains(t, stderr.String(), "transcript tail is incomplete")
}

// TestRunSessionShowJSONProjection pins the public `session show --json`
// contract (WP10): a schema_version 1 envelope around the projection —
// keeping ids, lineage, route, timing, transcript summary (including
// stop_reason) and import labels while dropping internal operational fields.
func TestRunSessionShowJSONProjection(t *testing.T) {
	stateDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	exitCode := 130
	record := state.SessionRecord{
		ID:          "projected",
		ParentID:    "parent",
		Depth:       1,
		TargetAlias: "prod",
		Route:       []string{"bastion", "prod"},
		Hops:        []string{"bastion"},
		SSHArgv:     []string{"ssh", "--", "prod"},
		ControlPath: "/tmp/ssherpa-control",
		Kind:        state.KindInteractive,
		StartedAt:   started,
		EndedAt:     &ended,
		ExitCode:    &exitCode,
		LocalPID:    100,
		SSHPID:      101,
		RunnerMode:  "supervised",
		Transcript: &state.TranscriptSpec{
			Path:       "/tmp/projected.cast",
			Format:     "asciicast-v2",
			Bytes:      4096,
			Frames:     7,
			StopReason: "size limit reached",
		},
		Import: &state.ImportSpec{
			ImportedAt:      ended,
			OriginClass:     "imported_other",
			SourceSessionID: "src-session",
			SourceMachineID: "feedface",
		},
		DisconnectReason: "latency timeout",
		Events: []state.SessionEvent{
			{Time: ended, Type: "latency_warning", Message: "probe slow"},
		},
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	var stdout bytes.Buffer
	code := Run([]string{"session", "show", "projected", "--json", "--state-dir", stateDir}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session show --json returned %d, want 0", code)
	}

	var got sessionShowJSON
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
	session := got.Session
	if session.ID != "projected" || session.ParentID != "parent" || session.Depth != 1 {
		t.Fatalf("identity fields = %+v", session)
	}
	if session.TargetAlias != "prod" || strings.Join(session.Route, ",") != "bastion,prod" {
		t.Fatalf("route fields = %+v", session)
	}
	if session.ExitCode == nil || *session.ExitCode != 130 || session.DisconnectReason != "latency timeout" {
		t.Fatalf("exit fields = %+v", session)
	}
	if session.Origin != "imported_other" {
		t.Fatalf("origin = %q, want imported_other", session.Origin)
	}
	if session.Import == nil || session.Import.SourceSessionID != "src-session" {
		t.Fatalf("import = %+v", session.Import)
	}
	if session.Transcript == nil || session.Transcript.StopReason != "size limit reached" || session.Transcript.Frames != 7 {
		t.Fatalf("transcript = %+v", session.Transcript)
	}
	// Internal operational fields must not leak into the public contract.
	for _, internal := range []string{`"ssh_argv"`, `"control_path"`, `"events"`, `"state_version"`} {
		if strings.Contains(stdout.String(), internal) {
			t.Fatalf("session show --json leaks %s:\n%s", internal, stdout.String())
		}
	}
}

// TestSessionOriginJSON pins the origin label projection.
func TestSessionOriginJSON(t *testing.T) {
	tests := []struct {
		name   string
		record state.SessionRecord
		want   string
	}{
		{name: "local", record: state.SessionRecord{}, want: "local"},
		{name: "imported other", record: state.SessionRecord{Import: &state.ImportSpec{OriginClass: "imported_other"}}, want: "imported_other"},
		{name: "imported self", record: state.SessionRecord{Import: &state.ImportSpec{OriginClass: "imported_self"}}, want: "imported_self"},
		{name: "imported blank class", record: state.SessionRecord{Import: &state.ImportSpec{}}, want: "imported_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionOriginJSON(tt.record); got != tt.want {
				t.Fatalf("sessionOriginJSON = %q, want %q", got, tt.want)
			}
		})
	}
}
