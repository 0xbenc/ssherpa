package cli

import (
	"context"
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
)

const sessionUsage = `Usage:
  ssherpa session list [--json] [--state-dir PATH]
  ssherpa session map [--json] [--all] [--state-dir PATH]
  ssherpa session show SESSION_ID [--json] [--state-dir PATH]
  ssherpa session log SESSION_ID [--raw] [--tail N] [--follow] [--state-dir PATH]
  ssherpa session replay SESSION_ID [--speed N] [--no-delay] [--state-dir PATH]
  ssherpa session grep SESSION_ID PATTERN [--ignore-case] [--json] [--state-dir PATH]
  ssherpa session export SESSION_ID [--format text|asciicast] [--output PATH] [--state-dir PATH]
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

func runSession(args []string, stdout io.Writer, stderr io.Writer) int {
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
	for {
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
			Group:       "Transcripts",
			Badge:       "log",
			Action:      "Open this recorded session transcript",
		})
	}
	return items
}

func transcriptBrowserDescription(record state.SessionRecord, size int64, now time.Time) string {
	parts := []string{sessionview.StatusLabel(record)}
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
	parts = append(parts, path)
	return strings.Join(parts, " · ")
}

func runTranscriptActionMenu(stateDir string, record state.SessionRecord, stdout io.Writer, stderr io.Writer) (int, bool) {
	items := []ui.ManagementItem{
		{Kind: ui.ItemSessions, Token: "view", Title: "View transcript", Description: "scroll, search, follow, and toggle raw output", Group: "Actions", Badge: "view", Action: "Open transcript viewer"},
		{Kind: ui.ItemSessions, Token: "replay", Title: "Replay transcript", Description: "play terminal output with original timing", Group: "Actions", Badge: "play", Action: "Replay recorded terminal output"},
		{Kind: ui.ItemSessions, Token: "metadata", Title: "Show metadata", Description: "show route, times, exit code, events, and transcript path", Group: "Actions", Badge: "meta", Action: "Print session metadata"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to transcript list", Group: "Navigation", Badge: "back", Action: "Return to transcript list"},
	}
	item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Transcript: " + sessionview.Target(record),
		Mode:        "choose action",
		Summary:     sessionview.FormatRecordRoute(record),
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
	case "replay":
		_, _, rec, ok := loadTranscript(stateDir, record.ID, stderr)
		if !ok {
			return 2, false
		}
		if err := transcript.Replay(stderr, rec, 1, false); err != nil {
			fmt.Fprintf(stderr, "ssherpa: replay transcript: %v\n", err)
			return 1, false
		}
		return 0, true
	case "metadata":
		printSessionRecord(stdout, record)
		return 0, true
	default:
		return 0, true
	}
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
	err = sessionview.ShowMap(context.Background(), sessionview.ShowOptions{
		Input:       os.Stdin,
		Output:      output,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		View: sessionview.ViewOptions{
			Title:    "ssherpa session map",
			StateDir: stateDir,
			Records:  records,
			Map:      sessionview.MapOptions{},
			Theme:    theme.WithNoColor(theme.NoColor || flags.NoColor),
			Help:     "press any key to return",
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: session map failed: %v\n", err)
		return 1, false
	}
	return 0, true
}

func runSessionToolsPicker(flags connectFlags, stdout io.Writer, stderr io.Writer) (int, bool) {
	items := []ui.ManagementItem{
		{Kind: ui.ItemSessions, Token: "transcripts", Title: "Browse transcripts", Description: "select a recorded session and view, search, follow, or replay its transcript", Group: "Sessions", Badge: "logs", Action: "Open transcript browser"},
		{Kind: ui.ItemSessions, Token: "map", Title: "Route map", Description: "show active supervised session lineage", Group: "Sessions", Badge: "map", Action: "Open session route map"},
		{Kind: ui.ItemSessions, Token: "list", Title: "List sessions", Description: "print recorded session metadata", Group: "Sessions", Badge: "list", Action: "Print session list"},
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
		return runSession(args, stdout, stderr), true
	case "map":
		return runSessionMapViewer(flags, stderr, stderr)
	case "list":
		args := []string{"list"}
		if flags.StateDir != "" {
			args = append(args, "--state-dir", flags.StateDir)
		}
		return runSession(args, stdout, stderr), true
	default:
		return 0, true
	}
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
		case arg == "--json" && command == "grep":
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
		case arg == "--output" && command == "export":
			value, ok := nextArg(args, &i, stderr, "--output")
			if !ok {
				return flags, false
			}
			flags.OutputPath = value
		case strings.HasPrefix(arg, "--output=") && command == "export":
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
