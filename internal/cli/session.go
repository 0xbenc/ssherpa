package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/sessionview"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/ssherpa/internal/transcript"
	"github.com/0xbenc/ssherpa/internal/ui"
	"github.com/charmbracelet/x/term"
)

const sessionUsage = `Usage:
  ssherpa session list [--json] [--state-dir PATH]
  ssherpa session map [--json] [--all] [--state-dir PATH]
  ssherpa session show SESSION_ID [--json] [--state-dir PATH]
  ssherpa session log SESSION_ID [--raw] [--tail N] [--follow] [--state-dir PATH]
  ssherpa session replay SESSION_ID [--speed N] [--no-delay] [--state-dir PATH]
  ssherpa session grep SESSION_ID PATTERN [--ignore-case] [--json] [--state-dir PATH]
  ssherpa session export SESSION_ID [--format text|asciicast] [--output PATH] [--state-dir PATH]
  ssherpa session bundle export SESSION_ID --output PATH [--json] [--state-dir PATH]
  ssherpa session bundle import PATH [--json] [--state-dir PATH]
  ssherpa session identity [--json] [--state-dir PATH]
  ssherpa session browse [--state-dir PATH]
  ssherpa session stop-all [--json] [--state-dir PATH]
  ssherpa session prune [--older-than DURATION] [--dry-run] [--state-dir PATH]
`

type sessionMapOutput struct {
	StateDir string              `json:"state_dir"`
	Scope    string              `json:"scope"`
	Total    int                 `json:"total"`
	Active   int                 `json:"active"`
	Recorded int                 `json:"recorded"`
	Roots    []state.SessionNode `json:"roots"`
}

type sessionFlags struct {
	JSON      bool
	StateDir  string
	OlderThan time.Duration
	DryRun    bool
	All       bool
	Rest      []string
}

type sessionStopAllResult struct {
	Scanned  int                    `json:"scanned"`
	Matched  int                    `json:"matched"`
	Signaled int                    `json:"signaled"`
	Stopped  int                    `json:"stopped"`
	Pending  int                    `json:"pending"`
	Errors   int                    `json:"errors"`
	Records  []sessionStopAllRecord `json:"records"`
}

type sessionStopAllRecord struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func runSession(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 || hasHelpFlag(args) {
		fmt.Fprint(stdout, sessionUsage)
		return 0
	}

	switch args[0] {
	case "list":
		return runSessionList(args[1:], stdout, stderr)
	case "map":
		return runSessionMap(args[1:], stdout, stderr)
	case "show":
		return runSessionShow(args[1:], stdout, stderr)
	case "log":
		return runSessionLog(args[1:], stdout, stderr)
	case "replay":
		return runSessionReplay(args[1:], stdout, stderr)
	case "grep":
		return runSessionGrep(args[1:], stdout, stderr)
	case "export":
		return runSessionExport(args[1:], stdout, stderr)
	case "bundle":
		return runSessionBundle(args[1:], stdout, stderr, build)
	case "identity":
		return runSessionIdentity(args[1:], stdout, stderr, build)
	case "browse", "transcripts":
		return runSessionBrowse(args[1:], stdout, stderr)
	case "stop-all":
		return runSessionStopAll(args[1:], stdout, stderr)
	case "prune":
		return runSessionPrune(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown session command %q\n", args[0])
		return 1
	}
}

type transcriptFlags struct {
	JSON       bool
	StateDir   string
	Raw        bool
	Follow     bool
	NoDelay    bool
	IgnoreCase bool
	Tail       int
	Speed      float64
	Format     string
	OutputPath string
	Rest       []string
}

func runSessionLog(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseTranscriptFlags(args, stderr, "log")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session log requires exactly one session id")
		return 1
	}
	stateDir, record, rec, ok := loadTranscript(flags.StateDir, flags.Rest[0], stderr)
	if !ok {
		return 2
	}
	text := transcript.Text(rec, transcript.TextOptions{Raw: flags.Raw, Tail: flags.Tail})
	fmt.Fprint(stdout, text)
	if flags.Follow {
		return followTranscript(stdout, stderr, stateDir, record, flags.Raw)
	}
	return 0
}

func runSessionReplay(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseTranscriptFlags(args, stderr, "replay")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session replay requires exactly one session id")
		return 1
	}
	_, _, rec, ok := loadTranscript(flags.StateDir, flags.Rest[0], stderr)
	if !ok {
		return 2
	}
	if err := transcript.Replay(stdout, rec, flags.Speed, flags.NoDelay); err != nil {
		fmt.Fprintf(stderr, "ssherpa: replay transcript: %v\n", err)
		return 1
	}
	return 0
}

func runSessionGrep(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseTranscriptFlags(args, stderr, "grep")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 2 {
		fmt.Fprintln(stderr, "ssherpa: session grep requires SESSION_ID and PATTERN")
		return 1
	}
	_, _, rec, ok := loadTranscript(flags.StateDir, flags.Rest[0], stderr)
	if !ok {
		return 2
	}
	matches, err := transcript.Grep(rec, flags.Rest[1], flags.IgnoreCase)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: grep transcript: %v\n", err)
		return 1
	}
	if flags.JSON {
		writeJSON(stdout, matches)
		return boolToCode(len(matches) > 0)
	}
	for _, match := range matches {
		fmt.Fprintf(stdout, "%s:%d: %s\n", formatTranscriptOffset(match.Offset), match.LineNo, match.Text)
	}
	return boolToCode(len(matches) > 0)
}

func runSessionExport(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseTranscriptFlags(args, stderr, "export")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session export requires exactly one session id")
		return 1
	}
	_, _, rec, ok := loadTranscript(flags.StateDir, flags.Rest[0], stderr)
	if !ok {
		return 2
	}
	format := strings.TrimSpace(flags.Format)
	if format == "" {
		format = "text"
	}
	var out strings.Builder
	switch format {
	case "text":
		out.WriteString(transcript.Text(rec, transcript.TextOptions{}))
	case "asciicast", "cast":
		if err := transcript.ExportAsciicast(&out, rec); err != nil {
			fmt.Fprintf(stderr, "ssherpa: export transcript: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "ssherpa: unsupported transcript export format %q\n", format)
		return 1
	}
	if flags.OutputPath == "" {
		fmt.Fprint(stdout, out.String())
		return 0
	}
	if err := os.WriteFile(flags.OutputPath, []byte(out.String()), 0o600); err != nil {
		fmt.Fprintf(stderr, "ssherpa: write transcript export: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "exported transcript to %s\n", flags.OutputPath)
	return 0
}

func runSessionBundle(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 || hasHelpFlag(args) {
		fmt.Fprint(stdout, "Usage:\n  ssherpa session bundle export SESSION_ID --output PATH [--json] [--state-dir PATH]\n  ssherpa session bundle import PATH [--json] [--state-dir PATH]\n")
		return 0
	}
	switch args[0] {
	case "export":
		return runSessionBundleExport(args[1:], stdout, stderr, build)
	case "import":
		return runSessionBundleImport(args[1:], stdout, stderr, build)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown session bundle command %q\n", args[0])
		return 1
	}
}

func runSessionBundleExport(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	flags, ok := parseTranscriptFlags(args, stderr, "bundle-export")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session bundle export requires exactly one session id")
		return 1
	}
	if strings.TrimSpace(flags.OutputPath) == "" {
		fmt.Fprintln(stderr, "ssherpa: session bundle export requires --output PATH")
		return 1
	}
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	identity, err := state.EnsureMachineIdentity(stateDir, buildVersion(build), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity: %v\n", err)
		return 1
	}
	record, err := state.ReadRecord(stateDir, flags.Rest[0])
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: show session: %v\n", err)
		return 2
	}
	result, err := transcript.ExportBundle(stateDir, record, identity, flags.OutputPath, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: export bundle: %v\n", err)
		return 1
	}
	if flags.JSON {
		writeJSON(stdout, result)
		return 0
	}
	fmt.Fprintf(stdout, "exported session bundle to %s\n", result.Path)
	fmt.Fprintf(stdout, "source session: %s\n", result.Manifest.SourceSessionID)
	fmt.Fprintf(stdout, "source machine: %s\n", defaultString(result.Manifest.SourceMachineID, "unknown"))
	fmt.Fprintf(stdout, "transcript sha256: %s\n", result.Manifest.TranscriptSHA256)
	return 0
}

func runSessionBundleImport(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	flags, ok := parseTranscriptFlags(args, stderr, "bundle-import")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session bundle import requires exactly one bundle path")
		return 1
	}
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	identity, err := state.EnsureMachineIdentity(stateDir, buildVersion(build), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity: %v\n", err)
		return 1
	}
	result, err := transcript.ImportBundle(stateDir, flags.Rest[0], identity, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import bundle: %v\n", err)
		return 1
	}
	if flags.JSON {
		writeJSON(stdout, result)
		return 0
	}
	fmt.Fprintf(stdout, "imported session bundle as %s\n", result.Record.ID)
	fmt.Fprintf(stdout, "origin: %s\n", result.OriginClass)
	fmt.Fprintf(stdout, "source session: %s\n", result.Manifest.SourceSessionID)
	fmt.Fprintf(stdout, "source machine: %s\n", defaultString(result.Manifest.SourceMachineID, "unknown"))
	fmt.Fprintf(stdout, "transcript: %s\n", result.TranscriptPath)
	return 0
}

func runSessionIdentity(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session identity does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	identity, err := state.EnsureMachineIdentity(stateDir, buildVersion(build), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity: %v\n", err)
		return 1
	}
	if flags.JSON {
		writeJSON(stdout, identity)
		return 0
	}
	fmt.Fprintf(stdout, "machine_id:\t%s\n", identity.MachineID)
	fmt.Fprintf(stdout, "schema:\t%d\n", identity.SchemaVersion)
	fmt.Fprintf(stdout, "created:\t%s\n", identity.CreatedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(stdout, "created_by:\t%s\n", defaultString(identity.CreatedByVersion, "unknown"))
	fmt.Fprintf(stdout, "state:\t%s\n", stateDir)
	return 0
}

func runSessionBrowse(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseTranscriptFlags(args, stderr, "browse")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session browse does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	for {
		if !cleanupStaleSessionState(stateDir, stderr) {
			return 1
		}
		records, err := state.ListRecords(stateDir)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: list sessions: %v\n", err)
			return 1
		}
		items := transcriptSessionItems(stateDir, records)
		if len(items) == 0 {
			fmt.Fprintln(stdout, "No transcripts recorded.")
			return 0
		}
		item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			Title:       "Session transcripts",
			Mode:        "choose transcript",
			Summary:     fmt.Sprintf("%d transcript(s)", len(items)),
			Footer:      "enter select  /  type filter  /  arrows move  /  Q back",
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: transcript browser failed: %v\n", err)
			return 1
		}
		if !ok {
			return 0
		}
		record, err := state.ReadRecord(stateDir, item.Token)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: read session: %v\n", err)
			return 2
		}
		code, back := runTranscriptActionMenu(stateDir, record, stdout, stderr)
		if !back || code != 0 {
			return code
		}
	}
}

func transcriptSessionItems(stateDir string, records []state.SessionRecord) []ui.ManagementItem {
	now := time.Now()
	items := make([]ui.ManagementItem, 0, len(records))
	for _, record := range records {
		if record.RemoteMirror {
			continue
		}
		path := transcriptPathForRecord(stateDir, record)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemSessions,
			Token:       record.ID,
			Title:       sessionview.Target(record),
			Description: transcriptBrowserDescription(record, info.Size(), now),
			Detail:      transcriptBrowserDetail(record, path),
			Group:       originGroup(record),
			Badge:       originBadge(record),
			Action:      "Open this recorded session transcript",
		})
	}
	return items
}

func transcriptBrowserDescription(record state.SessionRecord, size int64, now time.Time) string {
	parts := []string{originLabel(record), sessionview.StatusLabel(record)}
	if !record.StartedAt.IsZero() {
		parts = append(parts, "started "+humanShortDuration(now.Sub(record.StartedAt))+" ago")
	}
	if record.EndedAt != nil {
		parts = append(parts, "duration "+humanShortDuration(record.EndedAt.Sub(record.StartedAt)))
	}
	parts = append(parts, humanBytesCLI(size))
	if record.Transcript != nil && record.Transcript.Truncated {
		parts = append(parts, "truncated")
	}
	return strings.Join(parts, " · ")
}

func transcriptBrowserDetail(record state.SessionRecord, path string) string {
	parts := []string{"route " + sessionview.FormatRecordRoute(record)}
	if record.ExitCode != nil {
		parts = append(parts, fmt.Sprintf("exit %d", *record.ExitCode))
	}
	if record.Import != nil {
		parts = append(parts, "source session "+defaultString(record.Import.SourceSessionID, "unknown"))
		parts = append(parts, "source machine "+shortMachineID(record.Import.SourceMachineID))
	}
	parts = append(parts, path)
	return strings.Join(parts, " · ")
}

func runTranscriptActionMenu(stateDir string, record state.SessionRecord, stdout io.Writer, stderr io.Writer) (int, bool) {
	items := transcriptActionItems(record)
	item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Transcript: " + sessionview.Target(record),
		Mode:        "choose action",
		Summary:     originLabel(record) + " · " + sessionview.FormatRecordRoute(record),
		Footer:      "enter select  /  arrows move  /  Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: transcript action failed: %v\n", err)
		return 1, false
	}
	if !ok || item.Token == "back" {
		return 0, true
	}
	switch item.Token {
	case "view":
		theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{NoColor: false})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1, false
		}
		err = sessionview.ShowTranscript(context.Background(), sessionview.TranscriptOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			StateDir:    stateDir,
			Record:      record,
			Theme:       theme,
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: transcript viewer failed: %v\n", err)
			return 1, false
		}
		return 0, true
	case "replay-raw":
		if record.Import != nil {
			ok, answered, err := ui.Confirm(context.Background(), ui.ConfirmOptions{
				Input:       os.Stdin,
				Output:      stderr,
				NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
				Title:       "Replay imported raw transcript",
				Message:     "Raw replay can emit terminal escape sequences from another machine's recording. Continue?",
			})
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: replay confirmation failed: %v\n", err)
				return 1, false
			}
			if !answered || !ok {
				return 0, true
			}
		}
		_, _, rec, ok := loadTranscript(stateDir, record.ID, stderr)
		if !ok {
			return 2, false
		}
		theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{NoColor: false})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1, false
		}
		if err := replayRawWithControls(os.Stdin, stderr, rec, theme); err != nil {
			fmt.Fprintf(stderr, "ssherpa: replay transcript: %v\n", err)
			return 1, false
		}
		return 0, true
	case "export-bundle":
		return runTranscriptBundleExportTUI(stateDir, record, stdout, stderr), true
	case "remove-import":
		return removeImportedTranscriptTUI(stateDir, record, stdout, stderr), true
	case "metadata":
		theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{NoColor: false})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1, false
		}
		err = sessionview.ShowMetadata(context.Background(), sessionview.MetadataOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			Record:      record,
			Theme:       theme,
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: metadata view failed: %v\n", err)
			return 1, false
		}
		return 0, true
	default:
		return 0, true
	}
}

func transcriptActionItems(record state.SessionRecord) []ui.ManagementItem {
	items := []ui.ManagementItem{
		{Kind: ui.ItemSessions, Token: "view", Title: "View transcript", Description: "scroll, search, follow, and toggle raw output", Group: "Actions", Badge: "view", Action: "Open transcript viewer"},
		{Kind: ui.ItemSessions, Token: "replay-raw", Title: "Replay raw", Description: "play terminal output with original timing", Group: "Actions", Badge: "raw", Action: "Replay raw terminal stream"},
		{Kind: ui.ItemSessions, Token: "export-bundle", Title: "Export bundle", Description: "write portable session bundle for another machine", Group: "Share", Badge: "export", Action: "Export transcript bundle"},
		{Kind: ui.ItemSessions, Token: "metadata", Title: "Show metadata", Description: "view route, times, exit code, events, and transcript path", Group: "Actions", Badge: "meta", Action: "Open session metadata view"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to transcript list", Group: "Navigation", Badge: "back", Action: "Return to transcript list"},
	}
	if record.Import == nil {
		return items
	}
	remove := ui.ManagementItem{Kind: ui.ItemConfirmDelete, Token: "remove-import", Title: "Remove imported transcript", Description: "delete imported session record and transcript file", Group: "Remove", Badge: "delete", Action: "Remove imported transcript after confirmation"}
	return append(items[:3], append([]ui.ManagementItem{remove}, items[3:]...)...)
}

const replayOverlayHotkey = byte(0x1e)

type replayCommand int

const (
	replayCommandNone replayCommand = iota
	replayCommandResume
	replayCommandRestart
	replayCommandBack
)

type replayKeyReader interface {
	ReadKey() (byte, bool, error)
}

type replayFileKeyReader struct {
	file *os.File
}

func (r replayFileKeyReader) ReadKey() (byte, bool, error) {
	if r.file == nil {
		return 0, false, nil
	}
	var buf [32]byte
	n, err := r.file.Read(buf[:])
	if n > 0 {
		return buf[0], true, nil
	}
	if err == nil || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, io.EOF) {
		return 0, false, nil
	}
	return 0, false, err
}

type replayControlOptions struct {
	Speed       float64
	NoDelay     bool
	Sleep       func(time.Duration)
	ShowOverlay func(replayProgress) replayCommand
}

type replayProgress struct {
	Index int
	Total int
	Next  transcript.Frame
}

func replayRawWithControls(stdin *os.File, output io.Writer, rec transcript.Recording, theme termstyle.Theme) error {
	restore, interactive, err := prepareReplayTerminal(stdin)
	if err != nil {
		return err
	}
	defer restore()
	if !interactive {
		return transcript.Replay(output, rec, 1, false)
	}
	reader := replayFileKeyReader{file: stdin}
	return replayRawControlled(output, rec, reader, replayControlOptions{
		Speed: 1,
		ShowOverlay: func(progress replayProgress) replayCommand {
			return showReplayOverlay(output, stdin, theme, progress)
		},
	})
}

func replayRawControlled(output io.Writer, rec transcript.Recording, keys replayKeyReader, opts replayControlOptions) error {
	frames := replayOutputFrames(rec)
	if len(frames) == 0 {
		return nil
	}
	speed := opts.Speed
	if speed <= 0 {
		speed = 1
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	showOverlay := opts.ShowOverlay
	if showOverlay == nil {
		showOverlay = func(replayProgress) replayCommand { return replayCommandResume }
	}

	index := 0
	var previous float64
	var delayRemaining time.Duration
	haveDelay := false
	for index < len(frames) {
		progress := replayProgress{Index: index, Total: len(frames), Next: frames[index]}
		cmd, err := replayPollCommand(keys, showOverlay, progress)
		if err != nil {
			return err
		}
		switch cmd {
		case replayCommandRestart:
			index = 0
			previous = 0
			delayRemaining = 0
			haveDelay = false
			_, _ = io.WriteString(output, "\x1b[0m\x1b[2J\x1b[H")
			continue
		case replayCommandBack:
			return nil
		}

		if !haveDelay {
			delayRemaining = replayFrameDelay(frames[index], previous, speed, opts.NoDelay)
			haveDelay = true
		}
		if delayRemaining > 0 {
			var waitCmd replayCommand
			waitCmd, delayRemaining, err = replayWait(delayRemaining, sleep, keys, showOverlay, progress)
			if err != nil {
				return err
			}
			switch waitCmd {
			case replayCommandRestart:
				index = 0
				previous = 0
				delayRemaining = 0
				haveDelay = false
				_, _ = io.WriteString(output, "\x1b[0m\x1b[2J\x1b[H")
				continue
			case replayCommandBack:
				return nil
			}
			if delayRemaining > 0 {
				continue
			}
		}

		if _, err := io.WriteString(output, frames[index].Data); err != nil {
			return err
		}
		previous = frames[index].Offset
		index++
		haveDelay = false
	}
	return nil
}

func replayOutputFrames(rec transcript.Recording) []transcript.Frame {
	frames := make([]transcript.Frame, 0, len(rec.Frames))
	for _, frame := range rec.Frames {
		if frame.Stream == "o" {
			frames = append(frames, frame)
		}
	}
	return frames
}

func replayFrameDelay(frame transcript.Frame, previous float64, speed float64, noDelay bool) time.Duration {
	if noDelay {
		return 0
	}
	delay := frame.Offset - previous
	if delay <= 0 {
		return 0
	}
	return time.Duration(delay / speed * float64(time.Second))
}

func replayWait(remaining time.Duration, sleep func(time.Duration), keys replayKeyReader, showOverlay func(replayProgress) replayCommand, progress replayProgress) (replayCommand, time.Duration, error) {
	const tick = 20 * time.Millisecond
	for remaining > 0 {
		cmd, err := replayPollCommand(keys, showOverlay, progress)
		if err != nil || cmd != replayCommandNone {
			return cmd, remaining, err
		}
		step := remaining
		if step > tick {
			step = tick
		}
		started := time.Now()
		sleep(step)
		elapsed := time.Since(started)
		if elapsed <= 0 {
			elapsed = step
		}
		remaining -= elapsed
	}
	return replayCommandNone, 0, nil
}

func replayPollCommand(keys replayKeyReader, showOverlay func(replayProgress) replayCommand, progress replayProgress) (replayCommand, error) {
	if keys == nil {
		return replayCommandNone, nil
	}
	key, ok, err := keys.ReadKey()
	if err != nil || !ok {
		return replayCommandNone, err
	}
	switch key {
	case replayOverlayHotkey:
		return showOverlay(progress), nil
	case 0x03:
		return replayCommandBack, nil
	default:
		return replayCommandNone, nil
	}
}

func prepareReplayTerminal(stdin *os.File) (func(), bool, error) {
	if stdin == nil || !term.IsTerminal(stdin.Fd()) {
		return func() {}, false, nil
	}
	fd := int(stdin.Fd())
	state, err := term.MakeRaw(uintptr(fd))
	if err != nil {
		return func() {}, false, err
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = term.Restore(uintptr(fd), state)
		return func() {}, false, err
	}
	restore := func() {
		_ = syscall.SetNonblock(fd, false)
		_ = term.Restore(uintptr(fd), state)
	}
	return restore, true, nil
}

type replayOverlayFrame struct {
	terminal bool
	startRow int
	lines    int
}

func showReplayOverlay(output io.Writer, stdin *os.File, theme termstyle.Theme, progress replayProgress) replayCommand {
	frame := drawReplayOverlay(output, stdin, theme, progress)
	defer clearReplayOverlay(output, frame)
	reader := replayFileKeyReader{file: stdin}
	for {
		key, ok, err := reader.ReadKey()
		if err != nil {
			return replayCommandBack
		}
		if !ok {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		switch key {
		case 'r', 'R':
			return replayCommandRestart
		case 'm', 'M', 'b', 'B', 0x03:
			return replayCommandBack
		case ' ', 'p', 'P', 'c', 'C', 'q', 'Q', '\r', '\n', 0x1b, replayOverlayHotkey:
			return replayCommandResume
		}
	}
}

func drawReplayOverlay(output io.Writer, stdin *os.File, theme termstyle.Theme, progress replayProgress) replayOverlayFrame {
	next := "done"
	if progress.Index < progress.Total {
		next = fmt.Sprintf("next frame %d/%d at %s", progress.Index+1, progress.Total, replayOffset(progress.Next.Offset))
	}
	lines := []string{
		theme.Style(termstyle.RoleForeground, "ssherpa raw replay"),
		theme.Style(termstyle.RoleWarning, "Playback is paused while this local overlay is open."),
		theme.Style(termstyle.RoleMuted, next),
		theme.Style(termstyle.RoleMuted, "space/q resume   r restart   m menus   Ctrl-C stop"),
	}
	width, height, ok := replayOverlaySize(stdin)
	if !ok {
		fmt.Fprintln(output)
		for _, line := range lines {
			fmt.Fprintln(output, termstyle.Strip(line))
		}
		return replayOverlayFrame{}
	}
	visibleLines := replayMin(len(lines), replayMax(3, height-2))
	startRow := replayMax(1, height-visibleLines+1)
	fmt.Fprint(output, "\x1b7\x1b[?25l")
	for i := 0; i < visibleLines; i++ {
		row := startRow + i
		fmt.Fprintf(output, "\x1b[%d;1H\x1b[2K%s", row, replayTruncateLine(lines[i], width))
	}
	return replayOverlayFrame{terminal: true, startRow: startRow, lines: visibleLines}
}

func clearReplayOverlay(output io.Writer, frame replayOverlayFrame) {
	if !frame.terminal {
		fmt.Fprintln(output, "----- ssherpa replay resumed -----")
		return
	}
	for i := 0; i < frame.lines; i++ {
		fmt.Fprintf(output, "\x1b[%d;1H\x1b[2K", frame.startRow+i)
	}
	fmt.Fprint(output, "\x1b8\x1b[?25h")
}

func replayOverlaySize(stdin *os.File) (int, int, bool) {
	if stdin == nil || !term.IsTerminal(stdin.Fd()) {
		return 80, 24, false
	}
	width, height, err := term.GetSize(stdin.Fd())
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24, true
	}
	return width, height, true
}

func replayTruncateLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if termstyle.VisibleWidth(value) <= width {
		return value
	}
	plain := termstyle.Strip(value)
	if len(plain) <= width {
		return plain
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(plain)
	if len(runes) <= width {
		return plain
	}
	return string(runes[:width-1]) + "…"
}

func replayOffset(offset float64) string {
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

func replayMin(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func replayMax(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func runTranscriptBundleExportTUI(stateDir string, record state.SessionRecord, stdout io.Writer, stderr io.Writer) int {
	defaultPath := defaultBundleExportPath(record)
	path, ok, err := ui.PromptText(context.Background(), ui.TextPromptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Export transcript bundle",
		Label:       "path",
		Initial:     defaultPath,
		Validate: func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("path is required")
			}
			return nil
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: export path prompt failed: %v\n", err)
		return 1
	}
	if !ok {
		return 0
	}
	identity, err := state.EnsureMachineIdentity(stateDir, "unknown", time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity: %v\n", err)
		return 1
	}
	result, err := transcript.ExportBundle(stateDir, record, identity, path, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: export bundle: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "exported session bundle to %s\n", result.Path)
	return 0
}

func runBundleImportTUI(flags connectFlags, stdout io.Writer, stderr io.Writer, build BuildInfo) (int, bool) {
	path, ok, err := ui.PromptText(context.Background(), ui.TextPromptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Import transcript bundle",
		Label:       "bundle",
		Validate: func(value string) error {
			value = strings.TrimSpace(value)
			if value == "" {
				return fmt.Errorf("bundle path is required")
			}
			info, err := os.Stat(value)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return fmt.Errorf("path is a directory")
			}
			return nil
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import path prompt failed: %v\n", err)
		return 1, false
	}
	if !ok {
		return 0, true
	}
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1, false
	}
	identity, err := state.EnsureMachineIdentity(stateDir, buildVersion(build), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity: %v\n", err)
		return 1, false
	}
	preview, err := transcript.PreviewBundle(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: preview bundle: %v\n", err)
		return 1, false
	}
	origin := state.OriginClass(identity, preview.Manifest.SourceMachineID)
	message := fmt.Sprintf("Import %s from %s?\n\nTarget: %s\nRoute: %s\nSource session: %s\nSource machine: %s\nOrigin: %s\nBundle SHA256: %s",
		filepath.Base(path),
		humanBytesCLI(preview.Bytes),
		defaultString(preview.Manifest.Target, "-"),
		sessionview.FormatRoute(preview.Manifest.Route),
		defaultString(preview.Manifest.SourceSessionID, "unknown"),
		defaultString(preview.Manifest.SourceMachineID, "unknown"),
		origin,
		preview.BundleSHA256,
	)
	confirmed, answered, err := ui.Confirm(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Confirm transcript import",
		Message:     message,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import confirmation failed: %v\n", err)
		return 1, false
	}
	if !answered || !confirmed {
		return 0, true
	}
	result, err := transcript.ImportBundle(stateDir, path, identity, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import bundle: %v\n", err)
		return 1, false
	}
	fmt.Fprintf(stdout, "imported session bundle as %s (%s)\n", result.Record.ID, result.OriginClass)
	theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    flags.ThemeName,
		File:    flags.ThemeFile,
		NoColor: flags.NoColor,
	})
	if err != nil {
		return 0, true
	}
	err = sessionview.ShowTranscript(context.Background(), sessionview.TranscriptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		StateDir:    stateDir,
		Record:      result.Record,
		Theme:       theme,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: transcript viewer failed: %v\n", err)
		return 1, false
	}
	return 0, true
}

func removeImportedTranscriptTUI(stateDir string, record state.SessionRecord, stdout io.Writer, stderr io.Writer) int {
	if record.Import == nil {
		fmt.Fprintln(stderr, "ssherpa: only imported transcripts can be removed here")
		return 1
	}
	confirmed, answered, err := ui.ConfirmDelete(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Remove imported transcript",
		Message:     fmt.Sprintf("Remove imported transcript %s and its local record?", record.ID),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: remove confirmation failed: %v\n", err)
		return 1
	}
	if !answered || !confirmed {
		return 0
	}
	path := transcript.PathForRecord(stateDir, record)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "ssherpa: remove transcript: %v\n", err)
		return 1
	}
	if err := os.Remove(state.RecordPath(stateDir, record.ID)); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "ssherpa: remove session record: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed imported transcript %s\n", record.ID)
	return 0
}

func runSessionList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session list does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	if !cleanupStaleSessionState(stateDir, stderr) {
		return 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list sessions: %v\n", err)
		return 1
	}

	if flags.JSON {
		writeJSON(stdout, records)
		return 0
	}
	sessionview.WriteList(stdout, stateDir, records)
	return 0
}

func runSessionStopAll(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session stop-all does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list sessions: %v\n", err)
		return 1
	}

	result := stopAllLiveSessions(stateDir, records)
	if flags.JSON {
		writeJSON(stdout, result)
		return boolToCode(result.Errors == 0)
	}
	if result.Matched == 0 {
		fmt.Fprintln(stdout, "No active tracked sessions to stop.")
		return 0
	}
	fmt.Fprintf(stdout, "ssherpa: stop-all signaled %d active session(s); stopped %d, pending %d, errors %d\n",
		result.Signaled, result.Stopped, result.Pending, result.Errors)
	for _, rec := range result.Records {
		label := defaultString(rec.Kind, state.KindInteractive)
		target := defaultString(rec.Target, "-")
		line := fmt.Sprintf("%s\t%s\t%s\tpid=%d\t%s", rec.ID, label, target, rec.PID, rec.Status)
		if rec.Error != "" {
			line += "\t" + rec.Error
		}
		fmt.Fprintln(stdout, line)
	}
	return boolToCode(result.Errors == 0)
}

func stopAllLiveSessions(stateDir string, records []state.SessionRecord) sessionStopAllResult {
	result := sessionStopAllResult{Scanned: len(records)}
	indexByID := map[string]int{}
	for _, record := range records {
		if !state.ProcessAlive(record) {
			continue
		}
		entry := sessionStopAllRecord{
			ID:     record.ID,
			Kind:   defaultString(record.Kind, state.KindInteractive),
			Target: record.TargetAlias,
			PID:    record.LocalPID,
			Status: "signaled",
		}
		result.Matched++
		if err := syscall.Kill(record.LocalPID, syscall.SIGHUP); err != nil {
			entry.Status = "error"
			entry.Error = err.Error()
			result.Errors++
		} else {
			result.Signaled++
		}
		indexByID[entry.ID] = len(result.Records)
		result.Records = append(result.Records, entry)
	}
	if result.Signaled == 0 {
		return result
	}

	deadline := time.Now().Add(forwardStopWait)
	for time.Now().Before(deadline) {
		allDone := true
		for _, entry := range result.Records {
			if entry.Status == "signaled" {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		for id, index := range indexByID {
			if result.Records[index].Status != "signaled" {
				continue
			}
			record, err := state.ReadRecord(stateDir, id)
			if err != nil {
				continue
			}
			if record.EndedAt != nil || !state.ProcessAlive(record) {
				result.Records[index].Status = "stopped"
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, entry := range result.Records {
		switch entry.Status {
		case "stopped":
			result.Stopped++
		case "signaled":
			result.Pending++
		}
	}
	return result
}

func boolToCode(ok bool) int {
	if ok {
		return 0
	}
	return 1
}

func runSessionMap(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, true)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session map does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	if !cleanupStaleSessionState(stateDir, stderr) {
		return 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: map sessions: %v\n", err)
		return 1
	}

	visible := records
	scope := "active"
	if !flags.All {
		visible = sessionview.ActiveRecords(records)
	} else {
		scope = "all"
	}
	roots := sessionview.MapForest(visible)
	active, _ := sessionview.CountStatuses(records)
	if flags.JSON {
		writeJSON(stdout, sessionMapOutput{
			StateDir: stateDir,
			Scope:    scope,
			Total:    len(visible),
			Active:   active,
			Recorded: len(records),
			Roots:    roots,
		})
		return 0
	}
	sessionview.WriteMapWithOptions(stdout, stateDir, records, sessionview.MapOptions{IncludeExited: flags.All})
	return 0
}

func runSessionMapViewer(flags connectFlags, output io.Writer, stderr io.Writer) (int, bool) {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1, false
	}
	if !cleanupStaleSessionState(stateDir, stderr) {
		return 1, false
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: map sessions: %v\n", err)
		return 1, false
	}
	theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    flags.ThemeName,
		File:    flags.ThemeFile,
		NoColor: flags.NoColor,
		Env:     os.Environ(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, false
	}
	result, err := sessionview.ShowMapWithResult(context.Background(), sessionview.ShowOptions{
		Input:       os.Stdin,
		Output:      output,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		View: sessionview.ViewOptions{
			Title:    "ssherpa session map",
			StateDir: stateDir,
			Records:  records,
			Map:      sessionview.MapOptions{},
			Theme:    theme.WithNoColor(theme.NoColor || flags.NoColor),
			Help:     "q back / D delete all local data",
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: session map failed: %v\n", err)
		return 1, false
	}
	if result.Action == sessionview.MapActionDeleteAllData {
		return runDeleteAllLocalDataTUI(flags, output, stderr)
	}
	return 0, true
}

func runSessionListViewer(flags connectFlags, stderr io.Writer) (int, bool) {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1, false
	}
	if !cleanupStaleSessionState(stateDir, stderr) {
		return 1, false
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list sessions: %v\n", err)
		return 1, false
	}
	theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    flags.ThemeName,
		File:    flags.ThemeFile,
		NoColor: flags.NoColor,
		Env:     os.Environ(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, false
	}
	err = sessionview.ShowList(context.Background(), sessionview.ListOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		StateDir:    stateDir,
		Records:     records,
		Theme:       theme.WithNoColor(theme.NoColor || flags.NoColor),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: session list failed: %v\n", err)
		return 1, false
	}
	return 0, true
}

func cleanupStaleSessionState(stateDir string, stderr io.Writer) bool {
	result, err := state.CleanupStaleRemoteMirrors(stateDir, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: cleanup stale session records: %v\n", err)
		return false
	}
	if len(result.RemoteMirrors) > 0 {
		fmt.Fprintf(stderr, "ssherpa: cleaned up %d stale remote session mirror(s)\n", len(result.RemoteMirrors))
	}
	return true
}

func runDeleteAllLocalDataTUI(flags connectFlags, stdout io.Writer, stderr io.Writer) (int, bool) {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1, false
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list sessions: %v\n", err)
		return 1, false
	}
	message := fmt.Sprintf("Delete all local ssherpa data in %s?\n\nThis removes session records, transcripts, imported bundles, machine identity, saved forwards, and saved proxies. Tracked live local sessions will be stopped first. SSH config is not removed.", stateDir)
	confirmed, answered, err := ui.ConfirmDelete(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Delete all local ssherpa data",
		Message:     message,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: delete confirmation failed: %v\n", err)
		return 1, false
	}
	if !answered || !confirmed {
		return 0, true
	}
	stopResult, err := deleteAllLocalData(stateDir, records)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: delete local data: %v\n", err)
		return 1, false
	}
	fmt.Fprintf(stdout, "deleted local ssherpa data in %s\n", stateDir)
	if stopResult.Signaled > 0 || stopResult.Errors > 0 {
		fmt.Fprintf(stdout, "stopped %d tracked local session(s); pending %d; errors %d\n", stopResult.Stopped, stopResult.Pending, stopResult.Errors)
	}
	return boolToCode(stopResult.Errors == 0), true
}

func deleteAllLocalData(stateDir string, records []state.SessionRecord) (sessionStopAllResult, error) {
	stopResult := stopAllLiveSessions(stateDir, records)
	if err := os.RemoveAll(stateDir); err != nil {
		return stopResult, err
	}
	return stopResult, nil
}

func runSessionToolsPicker(flags connectFlags, stdout io.Writer, stderr io.Writer, build BuildInfo) (int, bool) {
	items := []ui.ManagementItem{
		{Kind: ui.ItemSessions, Token: "transcripts", Title: "Browse transcripts", Description: "select a recorded session and view, search, follow, or replay its transcript", Group: "Sessions", Badge: "logs", Action: "Open transcript browser"},
		{Kind: ui.ItemSessions, Token: "import", Title: "Import transcript bundle", Description: "validate and import a portable recording from this or another machine", Group: "Sessions", Badge: "import", Action: "Import transcript bundle"},
		{Kind: ui.ItemSessions, Token: "map", Title: "Route map", Description: "show active supervised session lineage", Group: "Sessions", Badge: "map", Action: "Open session route map"},
		{Kind: ui.ItemSessions, Token: "list", Title: "List sessions", Description: "browse recorded session metadata", Group: "Sessions", Badge: "list", Action: "Open session list"},
		{Kind: ui.ItemSessions, Token: "identity", Title: "Machine identity", Description: "show this machine's ssherpa recording identity", Group: "Sessions", Badge: "id", Action: "Show machine identity"},
		{Kind: ui.ItemConfirmDelete, Token: "delete-local-data", Title: "Delete all local data", Description: "stop tracked local sessions and remove ssherpa state data", Group: "Danger", Badge: "delete", Action: "Delete local ssherpa state after confirmation"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to the home screen", Group: "Navigation", Badge: "back", Action: "Return without opening a session tool"},
	}
	item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Sessions",
		Mode:        "choose session tool",
		Footer:      "enter select  /  arrows move  /  Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: sessions picker failed: %v\n", err)
		return 1, false
	}
	if !ok || item.Token == "back" {
		return 0, true
	}
	switch item.Token {
	case "transcripts":
		args := []string{"browse"}
		if flags.StateDir != "" {
			args = append(args, "--state-dir", flags.StateDir)
		}
		return runSession(args, stdout, stderr, build), true
	case "import":
		return runBundleImportTUI(flags, stdout, stderr, build)
	case "map":
		return runSessionMapViewer(flags, stderr, stderr)
	case "list":
		return runSessionListViewer(flags, stderr)
	case "identity":
		return runMachineIdentityTUI(flags, stderr, build), true
	case "delete-local-data":
		return runDeleteAllLocalDataTUI(flags, stdout, stderr)
	default:
		return 0, true
	}
}

func runMachineIdentityTUI(flags connectFlags, stderr io.Writer, build BuildInfo) int {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	identity, err := state.EnsureMachineIdentity(stateDir, buildVersion(build), time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity: %v\n", err)
		return 1
	}
	theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    flags.ThemeName,
		File:    flags.ThemeFile,
		NoColor: flags.NoColor,
		Env:     os.Environ(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	err = sessionview.ShowIdentity(context.Background(), sessionview.IdentityOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		StateDir:    stateDir,
		Identity:    identity,
		Theme:       theme.WithNoColor(theme.NoColor || flags.NoColor),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity view failed: %v\n", err)
		return 1
	}
	return 0
}

func runSessionShow(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session show requires exactly one session id")
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	record, err := state.ReadRecord(stateDir, flags.Rest[0])
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: show session: %v\n", err)
		return 2
	}

	if flags.JSON {
		writeJSON(stdout, record)
		return 0
	}
	printSessionRecord(stdout, record)
	return 0
}

func runSessionPrune(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, true, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session prune does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	if flags.OlderThan == 0 {
		flags.OlderThan = state.DefaultPrune
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	result, err := state.PruneRecords(stateDir, flags.OlderThan, time.Now(), flags.DryRun)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: prune sessions: %v\n", err)
		return 1
	}

	if flags.JSON {
		writeJSON(stdout, result)
		return 0
	}
	prefix := "removed"
	if flags.DryRun {
		prefix = "would remove"
	}
	fmt.Fprintf(stdout, "%s %d session record(s)\n", prefix, len(result.Records))
	for _, record := range result.Records {
		fmt.Fprintf(stdout, "%s\tended=%s\ttarget=%s\n",
			record.ID,
			formatOptionalTime(record.EndedAt),
			defaultString(record.TargetAlias, "-"),
		)
	}
	return 0
}

func parseSessionFlags(args []string, stderr io.Writer, allowPruneFlags bool, allowAllFlag bool) (sessionFlags, bool) {
	flags := sessionFlags{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.Rest = append(flags.Rest, args[i+1:]...)
			return flags, true
		case arg == "--json":
			flags.JSON = true
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--older-than" && allowPruneFlags:
			value, ok := nextArg(args, &i, stderr, "--older-than")
			if !ok {
				return flags, false
			}
			olderThan, ok := parseDuration(value, stderr, "--older-than")
			if !ok {
				return flags, false
			}
			flags.OlderThan = olderThan
		case strings.HasPrefix(arg, "--older-than=") && allowPruneFlags:
			olderThan, ok := parseDuration(strings.TrimPrefix(arg, "--older-than="), stderr, "--older-than")
			if !ok {
				return flags, false
			}
			flags.OlderThan = olderThan
		case arg == "--dry-run" && allowPruneFlags:
			flags.DryRun = true
		case arg == "--all" && allowAllFlag:
			flags.All = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown session flag %q\n", arg)
			return flags, false
		default:
			flags.Rest = append(flags.Rest, arg)
		}
	}
	return flags, true
}

func parseDuration(value string, stderr io.Writer, flag string) (time.Duration, bool) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		fmt.Fprintf(stderr, "ssherpa: %s must be a positive duration like 168h or 30m\n", flag)
		return 0, false
	}
	return duration, true
}

func parseTranscriptFlags(args []string, stderr io.Writer, command string) (transcriptFlags, bool) {
	flags := transcriptFlags{Speed: 1}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.Rest = append(flags.Rest, args[i+1:]...)
			return flags, true
		case arg == "--json" && (command == "grep" || strings.HasPrefix(command, "bundle-")):
			flags.JSON = true
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--raw" && command == "log":
			flags.Raw = true
		case arg == "--follow" && command == "log":
			flags.Follow = true
		case arg == "--tail" && command == "log":
			value, ok := nextArg(args, &i, stderr, "--tail")
			if !ok {
				return flags, false
			}
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				fmt.Fprintln(stderr, "ssherpa: --tail must be a non-negative integer")
				return flags, false
			}
			flags.Tail = n
		case strings.HasPrefix(arg, "--tail=") && command == "log":
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--tail="))
			if err != nil || n < 0 {
				fmt.Fprintln(stderr, "ssherpa: --tail must be a non-negative integer")
				return flags, false
			}
			flags.Tail = n
		case arg == "--no-delay" && command == "replay":
			flags.NoDelay = true
		case arg == "--speed" && command == "replay":
			value, ok := nextArg(args, &i, stderr, "--speed")
			if !ok {
				return flags, false
			}
			speed, err := strconv.ParseFloat(value, 64)
			if err != nil || speed <= 0 {
				fmt.Fprintln(stderr, "ssherpa: --speed must be a positive number")
				return flags, false
			}
			flags.Speed = speed
		case strings.HasPrefix(arg, "--speed=") && command == "replay":
			speed, err := strconv.ParseFloat(strings.TrimPrefix(arg, "--speed="), 64)
			if err != nil || speed <= 0 {
				fmt.Fprintln(stderr, "ssherpa: --speed must be a positive number")
				return flags, false
			}
			flags.Speed = speed
		case (arg == "--ignore-case" || arg == "-i") && command == "grep":
			flags.IgnoreCase = true
		case arg == "--format" && command == "export":
			value, ok := nextArg(args, &i, stderr, "--format")
			if !ok {
				return flags, false
			}
			flags.Format = value
		case strings.HasPrefix(arg, "--format=") && command == "export":
			flags.Format = strings.TrimPrefix(arg, "--format=")
		case arg == "--output" && (command == "export" || command == "bundle-export"):
			value, ok := nextArg(args, &i, stderr, "--output")
			if !ok {
				return flags, false
			}
			flags.OutputPath = value
		case strings.HasPrefix(arg, "--output=") && (command == "export" || command == "bundle-export"):
			flags.OutputPath = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown session %s flag %q\n", command, arg)
			return flags, false
		default:
			flags.Rest = append(flags.Rest, arg)
		}
	}
	return flags, true
}

func loadTranscript(stateDirOverride string, id string, stderr io.Writer) (string, state.SessionRecord, transcript.Recording, bool) {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return "", state.SessionRecord{}, transcript.Recording{}, false
	}
	record, err := state.ReadRecord(stateDir, id)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: show session: %v\n", err)
		return "", state.SessionRecord{}, transcript.Recording{}, false
	}
	path := transcriptPathForRecord(stateDir, record)
	rec, err := transcript.Read(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read transcript for %s: %v\n", id, err)
		return "", state.SessionRecord{}, transcript.Recording{}, false
	}
	return stateDir, record, rec, true
}

func transcriptPathForRecord(stateDir string, record state.SessionRecord) string {
	if record.Transcript != nil && strings.TrimSpace(record.Transcript.Path) != "" {
		return record.Transcript.Path
	}
	return filepath.Join(state.SessionsDir(stateDir), record.ID+".cast")
}

func followTranscript(stdout io.Writer, stderr io.Writer, stateDir string, record state.SessionRecord, raw bool) int {
	path := transcriptPathForRecord(stateDir, record)
	lastText := ""
	if rec, err := transcript.Read(path); err == nil {
		lastText = transcript.Text(rec, transcript.TextOptions{Raw: raw})
	}
	for {
		time.Sleep(500 * time.Millisecond)
		rec, err := transcript.Read(path)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: follow transcript: %v\n", err)
			return 1
		}
		text := transcript.Text(rec, transcript.TextOptions{Raw: raw})
		if len(text) > len(lastText) {
			fmt.Fprint(stdout, text[len(lastText):])
			lastText = text
		}
		latest, err := state.ReadRecord(stateDir, record.ID)
		if err != nil || !state.ProcessAlive(latest) {
			return 0
		}
	}
}

func formatTranscriptOffset(offset float64) string {
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

func printSessionRecord(stdout io.Writer, record state.SessionRecord) {
	fmt.Fprintf(stdout, "id:\t%s\n", record.ID)
	fmt.Fprintf(stdout, "status:\t%s\n", record.Status())
	fmt.Fprintf(stdout, "target:\t%s\n", defaultString(record.TargetAlias, "-"))
	fmt.Fprintf(stdout, "depth:\t%d\n", record.Depth)
	fmt.Fprintf(stdout, "route:\t%s\n", sessionview.FormatDisplayRoute(record.Route))
	fmt.Fprintf(stdout, "hops:\t%s\n", sessionview.FormatRoute(record.Hops))
	if forward := sessionview.ForwardSummary(record); forward != "" {
		fmt.Fprintf(stdout, "forward:\t%s\n", forward)
	}
	if proxy := sessionview.ProxySummary(record); proxy != "" {
		fmt.Fprintf(stdout, "proxy:\t%s\n", proxy)
	}
	fmt.Fprintf(stdout, "started:\t%s\n", record.StartedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(stdout, "ended:\t%s\n", formatOptionalTime(record.EndedAt))
	if record.ExitCode != nil {
		fmt.Fprintf(stdout, "exit_code:\t%d\n", *record.ExitCode)
	}
	if record.Transcript != nil {
		fmt.Fprintf(stdout, "transcript:\t%s\n", record.Transcript.Path)
		fmt.Fprintf(stdout, "transcript_format:\t%s\n", record.Transcript.Format)
		fmt.Fprintf(stdout, "transcript_bytes:\t%d\n", record.Transcript.Bytes)
		fmt.Fprintf(stdout, "transcript_frames:\t%d\n", record.Transcript.Frames)
		if record.Transcript.Truncated {
			fmt.Fprintln(stdout, "transcript_truncated:\tyes")
		}
	}
	if record.RecordedBy != nil {
		fmt.Fprintf(stdout, "recorded_by_machine:\t%s\n", record.RecordedBy.MachineID)
		fmt.Fprintf(stdout, "recorded_by_schema:\t%d\n", record.RecordedBy.IdentitySchema)
		fmt.Fprintf(stdout, "recorded_by_version:\t%s\n", defaultString(record.RecordedBy.SSHerpaVersion, "unknown"))
	}
	if record.Import != nil {
		fmt.Fprintf(stdout, "imported_at:\t%s\n", record.Import.ImportedAt.Local().Format(time.RFC3339))
		fmt.Fprintf(stdout, "import_origin:\t%s\n", record.Import.OriginClass)
		fmt.Fprintf(stdout, "source_session:\t%s\n", defaultString(record.Import.SourceSessionID, "unknown"))
		fmt.Fprintf(stdout, "source_machine:\t%s\n", defaultString(record.Import.SourceMachineID, "unknown"))
		fmt.Fprintf(stdout, "bundle_sha256:\t%s\n", record.Import.BundleSHA256)
	}
	if record.DisconnectReason != "" {
		fmt.Fprintf(stdout, "disconnect_reason:\t%s\n", record.DisconnectReason)
	}
	fmt.Fprintf(stdout, "local_pid:\t%d\n", record.LocalPID)
	fmt.Fprintf(stdout, "ssh_pid:\t%d\n", record.SSHPID)
	fmt.Fprintf(stdout, "runner:\t%s\n", record.RunnerMode)
	fmt.Fprintf(stdout, "argv:\t%s\n", strings.Join(record.SSHArgv, " "))
	if len(record.Events) > 0 {
		fmt.Fprintln(stdout, "events:")
		for _, event := range record.Events {
			fmt.Fprintf(stdout, "- %s\t%s", event.Time.Local().Format(time.RFC3339), event.Type)
			if event.LatencyMillis > 0 {
				fmt.Fprintf(stdout, "\tlatency=%dms", event.LatencyMillis)
			}
			if event.ThresholdMillis > 0 {
				fmt.Fprintf(stdout, "\tthreshold=%dms", event.ThresholdMillis)
			}
			if event.Message != "" {
				fmt.Fprintf(stdout, "\t%s", event.Message)
			}
			fmt.Fprintln(stdout)
		}
	}
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Local().Format(time.RFC3339)
}

func humanBytesCLI(n int64) string {
	switch {
	case n >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1024*1024*1024))
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func buildVersion(build BuildInfo) string {
	normalized := build.normalized()
	return normalized.Version
}

func originGroup(record state.SessionRecord) string {
	if record.Import == nil {
		return "Local"
	}
	switch record.Import.OriginClass {
	case "imported_self":
		return "Imported - This Machine"
	case "imported_other":
		return "Imported - Other Machines"
	default:
		return "Imported - Unknown Origin"
	}
}

func originBadge(record state.SessionRecord) string {
	if record.Import == nil {
		return "local"
	}
	switch record.Import.OriginClass {
	case "imported_self":
		return "self"
	case "imported_other":
		return "other"
	default:
		return "unknown"
	}
}

func originLabel(record state.SessionRecord) string {
	if record.Import == nil {
		return "local"
	}
	switch record.Import.OriginClass {
	case "imported_self":
		return "imported self"
	case "imported_other":
		return "imported other"
	default:
		return "imported unknown"
	}
}

func shortMachineID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "unknown"
	}
	if len(id) <= 12 {
		return id
	}
	return id[:8]
}

func defaultBundleExportPath(record state.SessionRecord) string {
	target := strings.TrimSpace(sessionview.Target(record))
	if target == "" || target == "-" {
		target = "session"
	}
	target = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, target)
	stamp := time.Now().Format("20060102")
	if !record.StartedAt.IsZero() {
		stamp = record.StartedAt.Local().Format("20060102")
	}
	return target + "-" + stamp + ".ssherpa-session"
}
