package transcript

import (
	"archive/zip"
	"bytes"
	"errors"
	"math"
	"os"
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

// fakeFile substitutes the on-disk file so the writer's error and sync
// paths can be exercised deterministically.
type fakeFile struct {
	buf       bytes.Buffer
	failOn    int // 1-based Write call that fails; 0 = never
	partial   int // bytes accepted by the failing write
	writes    int
	truncates []int64
	seeks     []int64
	syncs     int
	closed    bool
}

func (f *fakeFile) Write(p []byte) (int, error) {
	f.writes++
	if f.failOn != 0 && f.writes == f.failOn {
		n := f.partial
		if n > len(p) {
			n = len(p)
		}
		f.buf.Write(p[:n])
		return n, errors.New("no space left on device")
	}
	return f.buf.Write(p)
}

func (f *fakeFile) Truncate(size int64) error {
	f.truncates = append(f.truncates, size)
	f.buf.Truncate(int(size))
	return nil
}

func (f *fakeFile) Seek(offset int64, whence int) (int64, error) {
	f.seeks = append(f.seeks, offset)
	return offset, nil
}

func (f *fakeFile) Sync() error  { f.syncs++; return nil }
func (f *fakeFile) Close() error { f.closed = true; return nil }

func TestWriterWriteErrorTruncatesBackAndAppendsStopMarker(t *testing.T) {
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	file := &fakeFile{failOn: 2, partial: 3}
	w := &Writer{file: file, path: "fake.cast", started: started, maxBytes: DefaultMaxBytes}
	w.WriteOutput(started.Add(time.Second), []byte("first frame"))
	good := int64(file.buf.Len())
	w.WriteOutput(started.Add(2*time.Second), []byte("second frame"))
	if len(file.truncates) != 1 || file.truncates[0] != good {
		t.Fatalf("truncates = %v, want [%d]", file.truncates, good)
	}
	if len(file.seeks) != 1 || file.seeks[0] != good {
		t.Fatalf("seeks = %v, want [%d]", file.seeks, good)
	}
	if reason := w.StopReason(); !strings.Contains(reason, "write error: no space left on device") {
		t.Fatalf("StopReason = %q", reason)
	}
	lines := strings.Split(strings.TrimSuffix(file.buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("file lines = %d (%q), want 2", len(lines), file.buf.String())
	}
	for i, line := range lines {
		if _, err := parseFrame([]byte(line)); err != nil {
			t.Fatalf("line %d is torn: %v (%q)", i, err, line)
		}
	}
	if !strings.Contains(lines[1], "recording stopped: write error") {
		t.Fatalf("marker line = %q", lines[1])
	}
	w.WriteOutput(started.Add(3*time.Second), []byte("dropped after stop"))
	if strings.Contains(file.buf.String(), "dropped after stop") {
		t.Fatalf("frame written after stop: %q", file.buf.String())
	}
	spec, err := w.Close(started.Add(4 * time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !spec.Truncated {
		t.Fatalf("Truncated = false; spec = %#v", spec)
	}
	if spec.Bytes != int64(file.buf.Len()) {
		t.Fatalf("spec.Bytes = %d, file has %d", spec.Bytes, file.buf.Len())
	}
	if !file.closed || file.syncs == 0 {
		t.Fatalf("closed = %v, syncs = %d", file.closed, file.syncs)
	}
}

func TestWriterCapStopAppendsMarkerAndFileStaysParseable(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "cap.cast")
	writer, _, err := OpenWriter(WriterOptions{
		Path:     path,
		Started:  started,
		Header:   Header{Title: "cap"},
		MaxBytes: 4096,
	})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started.Add(time.Second), []byte(strings.Repeat("a", 3000)))
	writer.WriteOutput(started.Add(2*time.Second), []byte(strings.Repeat("b", 3000)))
	if reason := writer.StopReason(); reason != "size limit reached" {
		t.Fatalf("StopReason = %q", reason)
	}
	spec, err := writer.Close(started.Add(3 * time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !spec.Truncated {
		t.Fatalf("Truncated = false; spec = %#v", spec)
	}
	rec, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(rec.Frames) != 2 {
		t.Fatalf("frames = %d, want 2 (output + stop marker)", len(rec.Frames))
	}
	last := rec.Frames[len(rec.Frames)-1]
	if last.Stream != "m" || !strings.Contains(last.Data, "recording stopped: size limit reached") {
		t.Fatalf("last frame = %#v", last)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if spec.Bytes != info.Size() {
		t.Fatalf("spec.Bytes = %d, file size %d", spec.Bytes, info.Size())
	}
}

func TestWriterSyncsPeriodicallyAndOnClose(t *testing.T) {
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	file := &fakeFile{}
	w := &Writer{file: file, path: "fake.cast", started: started, maxBytes: DefaultMaxBytes}
	chunk := []byte(strings.Repeat("x", 64*1024))
	for i := 0; i < 8; i++ {
		w.WriteOutput(started.Add(time.Duration(i)*time.Second), chunk)
	}
	if file.syncs == 0 {
		t.Fatalf("no periodic sync after %d bytes", file.buf.Len())
	}
	before := file.syncs
	if _, err := w.Close(started.Add(time.Minute)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if file.syncs != before+1 || !file.closed {
		t.Fatalf("syncs = %d (want %d), closed = %v", file.syncs, before+1, file.closed)
	}
}

func TestReadTornTailSalvagesFrames(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "torn.cast")
	writer, _, err := OpenWriter(WriterOptions{Path: path, Started: started, Header: Header{Title: "torn"}})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started.Add(time.Second), []byte("frame one"))
	writer.WriteOutput(started.Add(2*time.Second), []byte("frame two"))
	writer.WriteOutput(started.Add(3*time.Second), []byte("frame three"))
	if _, err := writer.Close(started.Add(4 * time.Second)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(path, data[:len(data)-5], 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	rec, err := Read(path)
	if !errors.Is(err, ErrTornTail) {
		t.Fatalf("err = %v, want ErrTornTail", err)
	}
	if len(rec.Frames) != 2 || rec.Frames[1].Data != "frame two" {
		t.Fatalf("salvaged frames = %#v", rec.Frames)
	}
	if rec.Header.Title != "torn" {
		t.Fatalf("header = %#v", rec.Header)
	}
}

func TestReadSalvage(t *testing.T) {
	header := `{"version":2,"timestamp":1}`
	frame1 := `[0.5,"o","one"]`
	frame2 := `[1.0,"o","two"]`
	cases := []struct {
		name     string
		content  string
		frames   int
		skipped  int
		wantTorn bool
		wantErr  string
	}{
		{name: "header only", content: header + "\n", frames: 0},
		{name: "header only no newline", content: header, frames: 0},
		{name: "empty file", wantErr: "empty transcript"},
		{name: "torn header", content: `{"vers`, wantErr: "parse transcript header"},
		{name: "garbage mid file", content: header + "\n" + frame1 + "\n{nope\n" + frame2 + "\n", frames: 2, skipped: 1},
		{name: "two garbage lines mid file", content: header + "\nx\ny\n" + frame1 + "\n", frames: 1, skipped: 2},
		{name: "wrong field count mid file", content: header + "\n" + `[1,"o"]` + "\n" + frame2 + "\n", frames: 1, skipped: 1},
		{name: "unterminated final line", content: header + "\n" + frame1 + "\n" + `[1.0,"o","tw`, frames: 1, wantTorn: true},
		{name: "unparseable terminated final line", content: header + "\n" + frame1 + "\n{nope}\n", frames: 1, wantTorn: true},
		{name: "blank lines skipped", content: header + "\n\n" + frame1 + "\n\n", frames: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := read(strings.NewReader(tc.content))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				if errors.Is(err, ErrTornTail) {
					t.Fatalf("err = %v unexpectedly wraps ErrTornTail", err)
				}
				return
			}
			if tc.wantTorn {
				if !errors.Is(err, ErrTornTail) {
					t.Fatalf("err = %v, want ErrTornTail", err)
				}
			} else if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(rec.Frames) != tc.frames {
				t.Fatalf("frames = %d (%#v), want %d", len(rec.Frames), rec.Frames, tc.frames)
			}
			if rec.SkippedLines != tc.skipped {
				t.Fatalf("SkippedLines = %d, want %d", rec.SkippedLines, tc.skipped)
			}
		})
	}
}

func TestReplayFrameSleepClamped(t *testing.T) {
	cases := []struct {
		name  string
		delta float64
		speed float64
		want  time.Duration
	}{
		{name: "normal", delta: 0.5, speed: 1, want: 500 * time.Millisecond},
		{name: "speed divides", delta: 5, speed: 2, want: 2500 * time.Millisecond},
		{name: "negative", delta: -3, speed: 1, want: 0},
		{name: "zero", delta: 0, speed: 1, want: 0},
		{name: "nan", delta: math.NaN(), speed: 1, want: 0},
		{name: "above clamp", delta: 20, speed: 1, want: maxReplayFrameDelay},
		{name: "292 year offset", delta: 9.2e18, speed: 1, want: maxReplayFrameDelay},
		{name: "max float", delta: math.MaxFloat64, speed: 0.25, want: maxReplayFrameDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := replayFrameSleep(tc.delta, tc.speed); got != tc.want {
				t.Fatalf("replayFrameSleep(%v, %v) = %v, want %v", tc.delta, tc.speed, got, tc.want)
			}
		})
	}
}

func TestCleanStripsEscapeSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "sgr", in: "\x1b[32mhi\x1b[0m", want: "hi"},
		{name: "csi full final byte range", in: "a\x1b[5@b\x1b[?25lc", want: "abc"},
		{name: "csi intermediate byte", in: "a\x1b[0 qb", want: "ab"},
		{name: "csi unterminated", in: "a\x1b[12;3", want: "a"},
		{name: "osc bel", in: "a\x1b]0;title\ab", want: "ab"},
		{name: "osc st", in: "a\x1b]7;file://h/p\x1b\\b", want: "ab"},
		{name: "ris", in: "a\x1bcb", want: "ab"},
		{name: "decsc decrc", in: "a\x1b7b\x1b8c", want: "abc"},
		{name: "charset designator", in: "a\x1b(Bb", want: "ab"},
		{name: "ss3", in: "a\x1bOPb", want: "ab"},
		{name: "ss2", in: "a\x1bNxb", want: "ab"},
		{name: "dcs", in: "a\x1bPq#0;1;1#0~\x1b\\b", want: "ab"},
		{name: "dcs unterminated eats rest", in: "a\x1bPrest of data", want: "a"},
		{name: "apc", in: "a\x1b_payload\x1b\\b", want: "ab"},
		{name: "pm", in: "a\x1b^p\x1b\\b", want: "ab"},
		{name: "sos", in: "a\x1bXs\x1b\\b", want: "ab"},
		{name: "bare esc at end", in: "a\x1b", want: "a"},
		{name: "esc before newline", in: "a\x1b\nb", want: "a\nb"},
		{name: "esc before high byte", in: "a\x1b\xc3\xa9", want: "a\xc3\xa9"},
		{name: "cr normalization", in: "one\r\ntwo\rthree", want: "one\ntwo\nthree"},
		{name: "plain text untouched", in: "plain text", want: "plain text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Clean(tc.in)
			if got != tc.want {
				t.Fatalf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsRune(got, 0x1b) {
				t.Fatalf("Clean(%q) leaked ESC: %q", tc.in, got)
			}
		})
	}
}

func TestTextSanitizesMarkerFrames(t *testing.T) {
	rec := Recording{Frames: []Frame{{Offset: 1, Stream: "m", Data: "alert \x1b]0;evil\a\x1bc end\r\nmore"}}}
	text := Text(rec, TextOptions{})
	if strings.ContainsRune(text, 0x1b) {
		t.Fatalf("cleaned marker leaked ESC: %q", text)
	}
	if !strings.Contains(text, "alert  end more") {
		t.Fatalf("cleaned marker = %q", text)
	}
	raw := Text(rec, TextOptions{Raw: true})
	if !strings.Contains(raw, "\x1b]0;evil") {
		t.Fatalf("raw marker = %q", raw)
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
	if exported.Warning != "" || imported.Warning != "" {
		t.Fatalf("warnings on healthy bundle: export %q import %q", exported.Warning, imported.Warning)
	}
	if exported.Manifest.TranscriptTornTail {
		t.Fatalf("manifest flags healthy transcript as torn: %#v", exported.Manifest)
	}
}

// makeBundleFixture builds a state dir with one recorded session and returns
// the record, identity, and transcript path.
func makeBundleFixture(t *testing.T, stateDir string, started time.Time) (state.SessionRecord, state.MachineIdentity, string) {
	t.Helper()
	identity, err := state.EnsureMachineIdentity(stateDir, "v1.0.0", started)
	if err != nil {
		t.Fatalf("EnsureMachineIdentity: %v", err)
	}
	record := state.SessionRecord{
		ID:          state.NewSessionID(started),
		TargetAlias: "prod",
		StartedAt:   started,
		RunnerMode:  "supervised",
	}
	path := Path(stateDir, record.ID)
	writer, _, err := OpenWriter(WriterOptions{Path: path, Started: started, Header: Header{Title: "prod"}})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	writer.WriteOutput(started.Add(time.Second), []byte("alpha output"))
	writer.WriteOutput(started.Add(2*time.Second), []byte("beta output"))
	spec, err := writer.Close(started.Add(3 * time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	record.Transcript = &spec
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	return record, identity, path
}

func TestBundleExportImportFlagsTornTranscript(t *testing.T) {
	sourceDir := t.TempDir()
	destDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	record, identity, castPath := makeBundleFixture(t, sourceDir, started)
	data, err := os.ReadFile(castPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := os.WriteFile(castPath, data[:len(data)-5], 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "torn.ssherpa-session")
	exported, err := ExportBundle(sourceDir, record, identity, bundlePath, started)
	if err != nil {
		t.Fatalf("ExportBundle: %v", err)
	}
	if !exported.Manifest.TranscriptTornTail {
		t.Fatalf("manifest does not flag torn tail: %#v", exported.Manifest)
	}
	if !strings.Contains(exported.Warning, "transcript tail is incomplete") {
		t.Fatalf("export warning = %q", exported.Warning)
	}
	destIdentity, err := state.EnsureMachineIdentity(destDir, "v1.0.0", started)
	if err != nil {
		t.Fatalf("EnsureMachineIdentity(dest): %v", err)
	}
	imported, err := ImportBundle(destDir, bundlePath, destIdentity, started.Add(time.Minute))
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	if !strings.Contains(imported.Warning, "transcript tail is incomplete") {
		t.Fatalf("import warning = %q", imported.Warning)
	}
	rec, err := Read(imported.TranscriptPath)
	if !errors.Is(err, ErrTornTail) {
		t.Fatalf("Read(imported torn transcript) err = %v, want ErrTornTail", err)
	}
	if len(rec.Frames) != 1 || rec.Frames[0].Data != "alpha output" {
		t.Fatalf("salvaged frames = %#v", rec.Frames)
	}
	info, err := os.Stat(imported.TranscriptPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("imported transcript mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestExportBundleRejectsUnreadableTranscript(t *testing.T) {
	sourceDir := t.TempDir()
	started := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	record, identity, castPath := makeBundleFixture(t, sourceDir, started)
	if err := os.WriteFile(castPath, []byte("not a transcript\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bundlePath := filepath.Join(t.TempDir(), "bad.ssherpa-session")
	_, err := ExportBundle(sourceDir, record, identity, bundlePath, started)
	if err == nil || !strings.Contains(err.Error(), "transcript is unreadable") {
		t.Fatalf("ExportBundle err = %v, want unreadable transcript error", err)
	}
}

func TestReadZipEntryEnforcesLimit(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("transcript.cast")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte(strings.Repeat("a", 1024))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err := readZipEntry(reader, "transcript.cast", 1023); err == nil || !strings.Contains(err.Error(), "exceeds 1023 byte limit") {
		t.Fatalf("readZipEntry over limit err = %v", err)
	}
	data, err := readZipEntry(reader, "transcript.cast", 1024)
	if err != nil || len(data) != 1024 {
		t.Fatalf("readZipEntry at limit = %d bytes, err %v", len(data), err)
	}
}

func TestBundleFileSizeCapRejectsOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.ssherpa-session")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := file.Truncate(maxBundleFileBytes + 1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := PreviewBundle(path); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("PreviewBundle err = %v, want size limit error", err)
	}
	if _, err := ImportBundle(t.TempDir(), path, state.MachineIdentity{}, time.Time{}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("ImportBundle err = %v, want size limit error", err)
	}
}
