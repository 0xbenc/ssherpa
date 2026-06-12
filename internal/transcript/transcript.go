package transcript

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/state"
)

const (
	FormatAsciicast = "asciicast-v2"
	DefaultMaxBytes = 50 * 1024 * 1024

	// maxTranscriptLineBytes bounds a single .cast line during read; longer
	// lines are treated as unparseable and handled by the salvage logic.
	maxTranscriptLineBytes = 8 * 1024 * 1024
	// writerSyncBytes is how many appended bytes may accumulate before the
	// writer fsyncs the transcript, so a crash loses at most this much.
	writerSyncBytes = 256 * 1024
	// stopMarkerHeadroom is reserved under MaxBytes so the final
	// "recording stopped" marker frame still fits once the cap is reached.
	stopMarkerHeadroom = 512
	// maxReplayFrameDelay caps the per-frame sleep during replay so a
	// crafted offset in an imported cast cannot stall the terminal.
	maxReplayFrameDelay = 10 * time.Second
	// Bundles are untrusted input; cap the on-disk bundle size and each
	// entry's decompressed size so a zip bomb cannot exhaust memory or disk.
	maxBundleFileBytes       = 1 << 30 // 1 GiB
	maxBundleMetaEntryBytes  = 8 * 1024 * 1024
	maxBundleTranscriptBytes = 1 << 30 // 1 GiB
)

// ErrTornTail reports a transcript whose final line is incomplete or
// unparseable (crash, ENOSPC, power loss mid-write). The Recording returned
// alongside it carries every frame parsed before the tear, so callers can
// errors.Is(err, ErrTornTail) and continue with a warning.
var ErrTornTail = errors.New("transcript tail is incomplete")

type Header struct {
	Version   int               `json:"version"`
	Width     int               `json:"width,omitempty"`
	Height    int               `json:"height,omitempty"`
	Timestamp int64             `json:"timestamp,omitempty"`
	Command   string            `json:"command,omitempty"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

type Frame struct {
	Offset float64
	Stream string
	Data   string
}

// writerFile is the subset of *os.File the Writer needs; tests substitute a
// failing implementation to exercise the error paths.
type writerFile interface {
	io.Writer
	io.Closer
	Truncate(size int64) error
	Seek(offset int64, whence int) (int64, error)
	Sync() error
}

type Writer struct {
	mu         sync.Mutex
	file       writerFile
	path       string
	started    time.Time
	maxBytes   int64
	bytes      int64
	frames     int64
	unsynced   int64
	closed     bool
	truncated  bool
	stopReason string
}

type WriterOptions struct {
	Path     string
	Started  time.Time
	Header   Header
	MaxBytes int64
}

func Path(stateDir string, id string) string {
	return filepath.Join(state.SessionsDir(stateDir), id+".cast")
}

func OpenWriter(opts WriterOptions) (*Writer, state.TranscriptSpec, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return nil, state.TranscriptSpec{}, errors.New("transcript path is required")
	}
	started := opts.Started
	if started.IsZero() {
		started = time.Now().UTC()
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, state.TranscriptSpec{}, fmt.Errorf("create transcript directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, state.TranscriptSpec{}, fmt.Errorf("open transcript: %w", err)
	}
	header := opts.Header
	header.Version = 2
	if header.Timestamp == 0 {
		header.Timestamp = started.Unix()
	}
	data, err := json.Marshal(header)
	if err != nil {
		_ = file.Close()
		return nil, state.TranscriptSpec{}, fmt.Errorf("marshal transcript header: %w", err)
	}
	data = append(data, '\n')
	n, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return nil, state.TranscriptSpec{}, fmt.Errorf("write transcript header: %w", err)
	}
	writer := &Writer{
		file:     file,
		path:     path,
		started:  started,
		maxBytes: maxBytes,
		bytes:    int64(n),
	}
	spec := state.TranscriptSpec{
		Path:      path,
		Format:    FormatAsciicast,
		StartedAt: started.UTC(),
		Bytes:     writer.bytes,
		MaxBytes:  maxBytes,
	}
	return writer, spec, nil
}

func (w *Writer) WriteOutput(at time.Time, data []byte) {
	w.writeFrame(at, "o", string(data))
}

func (w *Writer) WriteInput(at time.Time, data []byte) {
	w.writeFrame(at, "i", string(data))
}

func (w *Writer) WriteMarker(at time.Time, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	w.writeFrame(at, "m", message)
}

func (w *Writer) Snapshot() state.TranscriptSpec {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshotLocked(nil)
}

// StopReason reports why recording stopped early: "" while healthy,
// "size limit reached" once MaxBytes is hit, or "write error: ..." after an
// I/O failure. The same text is appended to the cast as a final marker.
func (w *Writer) StopReason() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopReason
}

func (w *Writer) Close(ended time.Time) (state.TranscriptSpec, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		spec := w.snapshotLocked(&ended)
		return spec, nil
	}
	w.closed = true
	syncErr := w.file.Sync()
	if syncErr != nil && isIgnorableSyncError(syncErr) {
		syncErr = nil
	}
	closeErr := w.file.Close()
	spec := w.snapshotLocked(&ended)
	if closeErr != nil {
		return spec, fmt.Errorf("close transcript: %w", closeErr)
	}
	if syncErr != nil {
		return spec, fmt.Errorf("sync transcript: %w", syncErr)
	}
	return spec, nil
}

func (w *Writer) writeFrame(at time.Time, stream string, data string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.file == nil || data == "" || w.truncated {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	offset := at.Sub(w.started).Seconds()
	if offset < 0 {
		offset = 0
	}
	line, err := json.Marshal([]any{offset, stream, data})
	if err != nil {
		return
	}
	line = append(line, '\n')
	limit := w.maxBytes - stopMarkerHeadroom
	if limit <= 0 {
		limit = w.maxBytes
	}
	if w.bytes+int64(len(line)) > limit {
		w.stopLocked(offset, "size limit reached")
		return
	}
	if err := w.appendLocked(line); err != nil {
		w.stopLocked(offset, stopReasonForError(err))
	}
}

// appendLocked writes line at the current end of the transcript. On failure
// it trims the file back to the last known-good offset so no torn partial
// line is left behind, and repositions for any later best-effort write.
func (w *Writer) appendLocked(line []byte) error {
	n, err := w.file.Write(line)
	if err != nil {
		_ = w.file.Truncate(w.bytes)
		_, _ = w.file.Seek(w.bytes, io.SeekStart)
		return err
	}
	w.bytes += int64(n)
	w.frames++
	w.unsynced += int64(n)
	if w.unsynced >= writerSyncBytes {
		_ = w.file.Sync()
		w.unsynced = 0
	}
	return nil
}

// stopLocked latches the stopped state and appends a final marker frame
// noting why recording stopped, if it fits under MaxBytes, so the cast
// itself records the reason instead of ending silently.
func (w *Writer) stopLocked(offset float64, reason string) {
	w.truncated = true
	w.stopReason = reason
	line, err := json.Marshal([]any{offset, "m", "recording stopped: " + reason})
	if err == nil {
		line = append(line, '\n')
		if w.bytes+int64(len(line)) <= w.maxBytes {
			_ = w.appendLocked(line)
		}
	}
	_ = w.file.Sync()
	w.unsynced = 0
}

func stopReasonForError(err error) string {
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return "write error: " + msg
}

func isIgnorableSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP)
}

func (w *Writer) snapshotLocked(ended *time.Time) state.TranscriptSpec {
	spec := state.TranscriptSpec{
		Path:      w.path,
		Format:    FormatAsciicast,
		StartedAt: w.started.UTC(),
		Bytes:     w.bytes,
		Frames:    w.frames,
		MaxBytes:  w.maxBytes,
		Truncated: w.truncated,
	}
	if ended != nil && !ended.IsZero() {
		value := ended.UTC()
		spec.EndedAt = &value
	}
	return spec
}

type Recording struct {
	Header Header
	Frames []Frame
	// SkippedLines counts mid-file lines that could not be parsed and were
	// skipped during read.
	SkippedLines int
}

type BundleManifest struct {
	BundleVersion       int       `json:"bundle_version"`
	ExportedAt          time.Time `json:"exported_at"`
	ExportedByMachineID string    `json:"exported_by_machine_id,omitempty"`
	SourceMachineID     string    `json:"source_machine_id,omitempty"`
	SourceSessionID     string    `json:"source_session_id"`
	Target              string    `json:"target,omitempty"`
	Route               []string  `json:"route,omitempty"`
	TranscriptSHA256    string    `json:"transcript_sha256"`
	RecordSHA256        string    `json:"record_sha256"`
	// TranscriptTornTail flags a transcript whose final line is incomplete
	// (the recorder crashed or ran out of disk). Additive to bundle_version 1.
	TranscriptTornTail bool `json:"transcript_torn_tail,omitempty"`
}

type BundleExportResult struct {
	Path     string         `json:"path"`
	Manifest BundleManifest `json:"manifest"`
	Bytes    int64          `json:"bytes"`
	Warning  string         `json:"warning,omitempty"`
}

type BundleImportResult struct {
	Record         state.SessionRecord `json:"record"`
	Manifest       BundleManifest      `json:"manifest"`
	OriginClass    string              `json:"origin_class"`
	BundleSHA256   string              `json:"bundle_sha256"`
	TranscriptPath string              `json:"transcript_path"`
	Warning        string              `json:"warning,omitempty"`
}

type BundlePreview struct {
	Manifest     BundleManifest `json:"manifest"`
	BundleSHA256 string         `json:"bundle_sha256"`
	Bytes        int64          `json:"bytes"`
}

func Read(path string) (Recording, error) {
	file, err := os.Open(path)
	if err != nil {
		return Recording{}, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()
	return read(file)
}

func ExportBundle(stateDir string, record state.SessionRecord, identity state.MachineIdentity, outputPath string, now time.Time) (BundleExportResult, error) {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return BundleExportResult{}, errors.New("output path is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	transcriptPath := PathForRecord(stateDir, record)
	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		return BundleExportResult{}, fmt.Errorf("read transcript: %w", err)
	}
	parsed, parseErr := read(bytes.NewReader(transcriptBytes))
	tornTail := false
	warning := ""
	switch {
	case errors.Is(parseErr, ErrTornTail):
		tornTail = true
		warning = fmt.Sprintf("transcript tail is incomplete; bundle carries %d complete frames", len(parsed.Frames))
	case parseErr != nil:
		return BundleExportResult{}, fmt.Errorf("transcript is unreadable: %w", parseErr)
	}
	if parsed.SkippedLines > 0 {
		if warning != "" {
			warning += "; "
		}
		warning += fmt.Sprintf("%d unparseable transcript lines skipped", parsed.SkippedLines)
	}
	recordForExport := record
	if recordForExport.Transcript != nil {
		copySpec := *recordForExport.Transcript
		copySpec.Path = "transcript.cast"
		recordForExport.Transcript = &copySpec
	}
	recordBytes, err := json.MarshalIndent(recordForExport, "", "  ")
	if err != nil {
		return BundleExportResult{}, fmt.Errorf("marshal session record: %w", err)
	}
	recordBytes = append(recordBytes, '\n')
	sourceMachineID := identity.MachineID
	sourceSessionID := record.ID
	if record.RecordedBy != nil && strings.TrimSpace(record.RecordedBy.MachineID) != "" {
		sourceMachineID = record.RecordedBy.MachineID
	}
	if record.Import != nil {
		if strings.TrimSpace(record.Import.SourceMachineID) != "" {
			sourceMachineID = record.Import.SourceMachineID
		}
		if strings.TrimSpace(record.Import.SourceSessionID) != "" {
			sourceSessionID = record.Import.SourceSessionID
		}
	}
	manifest := BundleManifest{
		BundleVersion:       1,
		ExportedAt:          now.UTC(),
		ExportedByMachineID: identity.MachineID,
		SourceMachineID:     sourceMachineID,
		SourceSessionID:     sourceSessionID,
		Target:              record.TargetAlias,
		Route:               append([]string(nil), record.Route...),
		TranscriptSHA256:    state.SHA256Hex(transcriptBytes),
		RecordSHA256:        state.SHA256Hex(recordBytes),
		TranscriptTornTail:  tornTail,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BundleExportResult{}, fmt.Errorf("marshal manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return BundleExportResult{}, fmt.Errorf("create export directory: %w", err)
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return BundleExportResult{}, fmt.Errorf("create bundle: %w", err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifestBytes},
		{"session.json", recordBytes},
		{"transcript.cast", transcriptBytes},
	} {
		w, err := zw.Create(entry.name)
		if err != nil {
			_ = zw.Close()
			_ = file.Close()
			return BundleExportResult{}, fmt.Errorf("create bundle entry %s: %w", entry.name, err)
		}
		if _, err := w.Write(entry.data); err != nil {
			_ = zw.Close()
			_ = file.Close()
			return BundleExportResult{}, fmt.Errorf("write bundle entry %s: %w", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		_ = file.Close()
		return BundleExportResult{}, fmt.Errorf("finalize bundle: %w", err)
	}
	if err := file.Close(); err != nil {
		return BundleExportResult{}, fmt.Errorf("close bundle: %w", err)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return BundleExportResult{}, fmt.Errorf("stat bundle: %w", err)
	}
	return BundleExportResult{Path: outputPath, Manifest: manifest, Bytes: info.Size(), Warning: warning}, nil
}

func ImportBundle(stateDir string, bundlePath string, identity state.MachineIdentity, now time.Time) (BundleImportResult, error) {
	bundleBytes, err := readBundleFile(bundlePath)
	if err != nil {
		return BundleImportResult{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	reader, err := zip.NewReader(bytes.NewReader(bundleBytes), int64(len(bundleBytes)))
	if err != nil {
		return BundleImportResult{}, fmt.Errorf("open bundle: %w", err)
	}
	manifestBytes, err := readZipEntry(reader, "manifest.json", maxBundleMetaEntryBytes)
	if err != nil {
		return BundleImportResult{}, err
	}
	recordBytes, err := readZipEntry(reader, "session.json", maxBundleMetaEntryBytes)
	if err != nil {
		return BundleImportResult{}, err
	}
	transcriptBytes, err := readZipEntry(reader, "transcript.cast", maxBundleTranscriptBytes)
	if err != nil {
		return BundleImportResult{}, err
	}
	var manifest BundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return BundleImportResult{}, fmt.Errorf("parse bundle manifest: %w", err)
	}
	if manifest.BundleVersion != 1 {
		return BundleImportResult{}, fmt.Errorf("unsupported bundle version %d", manifest.BundleVersion)
	}
	if manifest.TranscriptSHA256 != "" && manifest.TranscriptSHA256 != state.SHA256Hex(transcriptBytes) {
		return BundleImportResult{}, errors.New("transcript hash mismatch")
	}
	if manifest.RecordSHA256 != "" && manifest.RecordSHA256 != state.SHA256Hex(recordBytes) {
		return BundleImportResult{}, errors.New("session record hash mismatch")
	}
	parsed, parseErr := read(bytes.NewReader(transcriptBytes))
	warning := ""
	switch {
	case errors.Is(parseErr, ErrTornTail):
		warning = fmt.Sprintf("transcript tail is incomplete; %d complete frames imported", len(parsed.Frames))
	case parseErr != nil:
		return BundleImportResult{}, fmt.Errorf("bundled transcript is unreadable: %w", parseErr)
	}
	if parsed.SkippedLines > 0 {
		if warning != "" {
			warning += "; "
		}
		warning += fmt.Sprintf("%d unparseable transcript lines skipped", parsed.SkippedLines)
	}
	if warning == "" && manifest.TranscriptTornTail {
		warning = "exporter flagged the transcript tail as incomplete"
	}
	var source state.SessionRecord
	if err := json.Unmarshal(recordBytes, &source); err != nil {
		return BundleImportResult{}, fmt.Errorf("parse bundled session record: %w", err)
	}
	sourceID := strings.TrimSpace(manifest.SourceSessionID)
	if sourceID == "" {
		sourceID = source.ID
	}
	newID := state.NewSessionID(now.UTC())
	localTranscriptPath := Path(stateDir, newID)
	if _, err := fsutil.AtomicWriteFile(localTranscriptPath, transcriptBytes, fsutil.WriteOptions{Mode: 0o600}); err != nil {
		return BundleImportResult{}, fmt.Errorf("write imported transcript: %w", err)
	}
	record := source
	record.ID = newID
	record.ParentID = ""
	record.LocalPID = 0
	record.SSHPID = 0
	record.Inherited = false
	record.RemoteMirror = false
	if record.Transcript == nil {
		record.Transcript = &state.TranscriptSpec{Format: FormatAsciicast}
	}
	record.Transcript.Path = localTranscriptPath
	record.Transcript.Bytes = int64(len(transcriptBytes))
	originClass := state.OriginClass(identity, manifest.SourceMachineID)
	record.Import = &state.ImportSpec{
		ImportedAt:      now.UTC(),
		ImportedBy:      identity.MachineID,
		SourceMachineID: manifest.SourceMachineID,
		SourceSessionID: sourceID,
		OriginClass:     originClass,
		BundleSHA256:    state.SHA256Hex(bundleBytes),
	}
	if record.RecordedBy == nil && strings.TrimSpace(manifest.SourceMachineID) != "" {
		record.RecordedBy = &state.RecordingOrigin{
			MachineID:      manifest.SourceMachineID,
			IdentitySchema: state.IdentitySchemaVersion,
		}
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		return BundleImportResult{}, err
	}
	return BundleImportResult{
		Record:         record,
		Manifest:       manifest,
		OriginClass:    originClass,
		BundleSHA256:   record.Import.BundleSHA256,
		TranscriptPath: localTranscriptPath,
		Warning:        warning,
	}, nil
}

func PreviewBundle(bundlePath string) (BundlePreview, error) {
	bundleBytes, err := readBundleFile(bundlePath)
	if err != nil {
		return BundlePreview{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(bundleBytes), int64(len(bundleBytes)))
	if err != nil {
		return BundlePreview{}, fmt.Errorf("open bundle: %w", err)
	}
	manifestBytes, err := readZipEntry(reader, "manifest.json", maxBundleMetaEntryBytes)
	if err != nil {
		return BundlePreview{}, err
	}
	var manifest BundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return BundlePreview{}, fmt.Errorf("parse bundle manifest: %w", err)
	}
	return BundlePreview{
		Manifest:     manifest,
		BundleSHA256: state.SHA256Hex(bundleBytes),
		Bytes:        int64(len(bundleBytes)),
	}, nil
}

func PathForRecord(stateDir string, record state.SessionRecord) string {
	if record.Transcript != nil && strings.TrimSpace(record.Transcript.Path) != "" {
		return record.Transcript.Path
	}
	return Path(stateDir, record.ID)
}

// readBundleFile loads a bundle from disk, refusing files larger than
// maxBundleFileBytes before reading them into memory.
func readBundleFile(bundlePath string) ([]byte, error) {
	info, err := os.Stat(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("stat bundle: %w", err)
	}
	if info.Size() > maxBundleFileBytes {
		return nil, fmt.Errorf("bundle is %d bytes; limit is %d", info.Size(), int64(maxBundleFileBytes))
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	return data, nil
}

// readZipEntry decompresses one bundle entry, streaming through a hard size
// limit so a crafted zip cannot expand without bound.
func readZipEntry(reader *zip.Reader, name string, limit int64) ([]byte, error) {
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open bundle entry %s: %w", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, limit+1))
		if err != nil {
			return nil, fmt.Errorf("read bundle entry %s: %w", name, err)
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("bundle entry %s exceeds %d byte limit", name, limit)
		}
		return data, nil
	}
	return nil, fmt.Errorf("bundle missing %s", name)
}

// read parses an asciicast stream with salvage tolerance: unparseable lines
// followed by more data are skipped and counted in Recording.SkippedLines,
// while an unparseable or unterminated final line returns every frame parsed
// before it together with an error wrapping ErrTornTail.
func read(r io.Reader) (Recording, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	headerLine, terminated, tooLong, err := readLine(br)
	if err != nil {
		return Recording{}, fmt.Errorf("read transcript header: %w", err)
	}
	if len(headerLine) == 0 && !terminated && !tooLong {
		return Recording{}, errors.New("empty transcript")
	}
	if tooLong {
		return Recording{}, fmt.Errorf("parse transcript header: line exceeds %d bytes", maxTranscriptLineBytes)
	}
	var header Header
	if err := json.Unmarshal(headerLine, &header); err != nil {
		return Recording{}, fmt.Errorf("parse transcript header: %w", err)
	}
	rec := Recording{Header: header}
	lineNo := 1
	pendingLine := 0
	var pendingErr error
	for {
		line, terminated, tooLong, err := readLine(br)
		if err != nil {
			return rec, fmt.Errorf("read transcript: %w", err)
		}
		if len(line) == 0 && !terminated && !tooLong {
			break
		}
		lineNo++
		if pendingErr != nil {
			// The previous bad line was followed by more data: mid-file
			// garbage, skip it and keep going.
			rec.SkippedLines++
			pendingErr = nil
		}
		if !tooLong && len(bytes.TrimSpace(line)) == 0 {
			if !terminated {
				break
			}
			continue
		}
		var parseErr error
		if tooLong {
			parseErr = fmt.Errorf("line exceeds %d bytes", maxTranscriptLineBytes)
		} else {
			var frame Frame
			frame, parseErr = parseFrame(line)
			if parseErr == nil {
				rec.Frames = append(rec.Frames, frame)
			}
		}
		if parseErr != nil {
			pendingLine = lineNo
			pendingErr = parseErr
		}
		if !terminated {
			break
		}
	}
	if pendingErr != nil {
		return rec, fmt.Errorf("parse transcript line %d: %w (%v)", pendingLine, ErrTornTail, pendingErr)
	}
	return rec, nil
}

// readLine reads the next physical line from br. It returns the line without
// its terminator, whether a newline terminator was found, and whether the
// line exceeded maxTranscriptLineBytes (in which case the line content is
// discarded but the rest of the physical line is consumed). An end of input
// is reported as an empty, unterminated, not-too-long line.
func readLine(br *bufio.Reader) (line []byte, terminated bool, tooLong bool, err error) {
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong && len(chunk) > 0 {
			line = append(line, chunk...)
			if len(line) > maxTranscriptLineBytes {
				tooLong = true
				line = nil
			}
		}
		switch {
		case err == nil:
			if !tooLong {
				line = line[:len(line)-1]
			}
			return line, true, tooLong, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return line, false, tooLong, nil
		default:
			return line, false, tooLong, err
		}
	}
}

func parseFrame(data []byte) (Frame, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Frame{}, err
	}
	if len(raw) != 3 {
		return Frame{}, fmt.Errorf("frame has %d fields, want 3", len(raw))
	}
	var frame Frame
	if err := json.Unmarshal(raw[0], &frame.Offset); err != nil {
		return Frame{}, err
	}
	if err := json.Unmarshal(raw[1], &frame.Stream); err != nil {
		return Frame{}, err
	}
	if err := json.Unmarshal(raw[2], &frame.Data); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

type TextOptions struct {
	Raw     bool
	Stream  string
	Tail    int
	Since   time.Duration
	Context int
}

func Text(rec Recording, opts TextOptions) string {
	frames := filteredFrames(rec.Frames, opts)
	if opts.Tail > 0 && len(frames) > opts.Tail {
		frames = frames[len(frames)-opts.Tail:]
	}
	var b strings.Builder
	for _, frame := range frames {
		if frame.Stream != "o" && frame.Stream != "m" {
			continue
		}
		if frame.Stream == "m" {
			data := frame.Data
			if !opts.Raw {
				data = strings.ReplaceAll(Clean(data), "\n", " ")
			}
			fmt.Fprintf(&b, "\n[%s] %s\n", formatOffset(frame.Offset), data)
			continue
		}
		if opts.Raw {
			b.WriteString(frame.Data)
		} else {
			b.WriteString(Clean(frame.Data))
		}
	}
	return b.String()
}

func filteredFrames(frames []Frame, opts TextOptions) []Frame {
	if opts.Stream == "" && opts.Since <= 0 {
		return frames
	}
	out := make([]Frame, 0, len(frames))
	for _, frame := range frames {
		if opts.Stream != "" && frame.Stream != opts.Stream {
			continue
		}
		if opts.Since > 0 && frame.Offset < opts.Since.Seconds() {
			continue
		}
		out = append(out, frame)
	}
	return out
}

// Clean strips terminal escape sequences from recorded output and normalizes
// carriage returns to newlines. Transcript frames are untrusted (a hostile
// remote, or an imported bundle, controls the bytes), so the stripper covers
// the full ESC grammar — CSI, OSC, DCS/SOS/PM/APC strings, SS2/SS3, charset
// designators, single-byte sequences like RIS, and bare ESC bytes — rather
// than only color codes.
func Clean(value string) string {
	value = stripEscapes(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return value
}

// stripEscapes removes every ESC-introduced sequence from value via a small
// state machine over the ECMA-48 escape grammar. Unterminated sequences at
// the end of input are dropped (fail closed).
func stripEscapes(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); {
		c := value[i]
		if c != 0x1b {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(value) {
			break
		}
		switch next := value[i]; {
		case next == '[':
			// CSI: parameter/intermediate bytes, then one final byte @-~.
			i++
			for i < len(value) {
				final := value[i]
				i++
				if final >= 0x40 && final <= 0x7e {
					break
				}
			}
		case next == ']':
			// OSC string, terminated by BEL or ST.
			i = skipEscapeString(value, i+1)
		case next == 'P', next == 'X', next == '^', next == '_':
			// DCS, SOS, PM, APC strings, terminated by ST (BEL accepted).
			i = skipEscapeString(value, i+1)
		case next == 'N', next == 'O':
			// SS2/SS3 shift the next single character.
			i++
			if i < len(value) {
				i++
			}
		case next >= 0x20 && next <= 0x2f:
			// Intermediate byte(s) then a final byte, e.g. charset
			// designators such as ESC ( B.
			i++
			for i < len(value) && value[i] >= 0x20 && value[i] <= 0x2f {
				i++
			}
			if i < len(value) {
				i++
			}
		case next >= 0x30 && next <= 0x7e:
			// Two-byte sequence, e.g. RIS (ESC c), DECSC (ESC 7).
			i++
		default:
			// ESC before a control or high byte: drop the bare ESC and
			// process the following byte normally.
		}
	}
	return b.String()
}

// skipEscapeString consumes a control-string payload starting at i and
// returns the index just past its BEL or ST (ESC \) terminator, or the end
// of input when unterminated.
func skipEscapeString(value string, i int) int {
	for i < len(value) {
		if value[i] == '\a' {
			return i + 1
		}
		if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}

func ExportAsciicast(w io.Writer, rec Recording) error {
	header, err := json.Marshal(rec.Header)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, string(header)); err != nil {
		return err
	}
	for _, frame := range rec.Frames {
		line, err := json.Marshal([]any{frame.Offset, frame.Stream, frame.Data})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, string(line)); err != nil {
			return err
		}
	}
	return nil
}

type Match struct {
	LineNo int     `json:"line"`
	Offset float64 `json:"offset"`
	Text   string  `json:"text"`
}

func Grep(rec Recording, pattern string, ignoreCase bool) ([]Match, error) {
	if pattern == "" {
		return nil, errors.New("pattern is required")
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	var out []Match
	lineNo := 0
	for _, frame := range rec.Frames {
		if frame.Stream != "o" {
			continue
		}
		for _, line := range strings.Split(Clean(frame.Data), "\n") {
			if line == "" {
				continue
			}
			lineNo++
			if re.MatchString(line) {
				out = append(out, Match{LineNo: lineNo, Offset: frame.Offset, Text: line})
			}
		}
	}
	return out, nil
}

func Replay(w io.Writer, rec Recording, speed float64, noDelay bool) error {
	if speed <= 0 {
		speed = 1
	}
	var previous float64
	for _, frame := range rec.Frames {
		if frame.Stream != "o" {
			continue
		}
		if !noDelay {
			if d := replayFrameSleep(frame.Offset-previous, speed); d > 0 {
				time.Sleep(d)
			}
		}
		if _, err := io.WriteString(w, frame.Data); err != nil {
			return err
		}
		previous = frame.Offset
	}
	return nil
}

// replayFrameSleep computes the inter-frame sleep, clamped to
// maxReplayFrameDelay: frame offsets come from potentially imported
// (untrusted) casts, so an absurd offset must not stall replay indefinitely.
func replayFrameSleep(delta float64, speed float64) time.Duration {
	if !(delta > 0) || !(speed > 0) {
		return 0
	}
	seconds := delta / speed
	if seconds >= maxReplayFrameDelay.Seconds() {
		return maxReplayFrameDelay
	}
	return time.Duration(seconds * float64(time.Second))
}

func formatOffset(offset float64) string {
	if offset < 0 {
		offset = 0
	}
	total := int(offset)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func ParseSize(value string) (int64, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return 0, errors.New("size is required")
	}
	mult := int64(1)
	for _, suffix := range []struct {
		name string
		mult int64
	}{
		{"gib", 1024 * 1024 * 1024},
		{"gb", 1000 * 1000 * 1000},
		{"mib", 1024 * 1024},
		{"mb", 1000 * 1000},
		{"kib", 1024},
		{"kb", 1000},
		{"b", 1},
	} {
		if strings.HasSuffix(value, suffix.name) {
			mult = suffix.mult
			value = strings.TrimSpace(strings.TrimSuffix(value, suffix.name))
			break
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	return n * mult, nil
}
