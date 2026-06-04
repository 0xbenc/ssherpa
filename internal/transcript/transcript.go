package transcript

import (
	"bufio"
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
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

const (
	FormatAsciicast = "asciicast-v2"
	DefaultMaxBytes = 50 * 1024 * 1024
)

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

type Writer struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	started   time.Time
	maxBytes  int64
	bytes     int64
	frames    int64
	closed    bool
	truncated bool
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

func (w *Writer) Close(ended time.Time) (state.TranscriptSpec, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		spec := w.snapshotLocked(&ended)
		return spec, nil
	}
	w.closed = true
	err := w.file.Close()
	spec := w.snapshotLocked(&ended)
	if err != nil {
		return spec, fmt.Errorf("close transcript: %w", err)
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
	if w.bytes+int64(len(line)) > w.maxBytes {
		w.truncated = true
		return
	}
	n, err := w.file.Write(line)
	if err != nil {
		w.truncated = true
		return
	}
	w.bytes += int64(n)
	w.frames++
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
}

func Read(path string) (Recording, error) {
	file, err := os.Open(path)
	if err != nil {
		return Recording{}, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()
	return read(file)
}

func read(r io.Reader) (Recording, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Recording{}, fmt.Errorf("read transcript header: %w", err)
		}
		return Recording{}, errors.New("empty transcript")
	}
	var header Header
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return Recording{}, fmt.Errorf("parse transcript header: %w", err)
	}
	rec := Recording{Header: header}
	lineNo := 1
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		frame, err := parseFrame(line)
		if err != nil {
			return Recording{}, fmt.Errorf("parse transcript line %d: %w", lineNo, err)
		}
		rec.Frames = append(rec.Frames, frame)
	}
	if err := scanner.Err(); err != nil {
		return Recording{}, fmt.Errorf("read transcript: %w", err)
	}
	return rec, nil
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
			fmt.Fprintf(&b, "\n[%s] %s\n", formatOffset(frame.Offset), frame.Data)
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

func Clean(value string) string {
	value = termstyle.Strip(value)
	value = stripOSC(value)
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return value
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
			delay := frame.Offset - previous
			if delay > 0 {
				time.Sleep(time.Duration(delay / speed * float64(time.Second)))
			}
		}
		if _, err := io.WriteString(w, frame.Data); err != nil {
			return err
		}
		previous = frame.Offset
	}
	return nil
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

func stripOSC(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] == '\x1b' && i+1 < len(value) && value[i+1] == ']' {
			i += 2
			for i < len(value) {
				if value[i] == '\a' {
					i++
					break
				}
				if value[i] == '\x1b' && i+1 < len(value) && value[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		b.WriteByte(value[i])
		i++
	}
	return b.String()
}
