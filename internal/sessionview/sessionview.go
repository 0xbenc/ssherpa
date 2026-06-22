package sessionview

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/chrome"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/ssherpa/internal/transcript"
)

func WriteList(w io.Writer, stateDir string, records []state.SessionRecord) {
	active, exited := CountStatuses(records)
	fmt.Fprintln(w, "Supervised sessions")
	fmt.Fprintf(w, "state: %s\n", stateDir)
	fmt.Fprintf(w, "active: %d  exited: %d  total: %d\n", active, exited, len(records))
	if len(records) == 0 {
		fmt.Fprintln(w, "\nNo supervised sessions recorded.")
		return
	}

	writeListGroup(w, "Active", records, true)
	writeListGroup(w, "Exited", records, false)
}

type MapOptions struct {
	CurrentID     string
	IncludeExited bool
}

type ViewOptions struct {
	Title    string
	StateDir string
	Records  []state.SessionRecord
	Map      MapOptions
	Theme    termstyle.Theme
	Width    int
	Height   int
	Help     string
}

type ShowOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	View        ViewOptions
}

type MapAction string

const (
	MapActionBack          MapAction = "back"
	MapActionDeleteAllData MapAction = "delete_all_data"
)

type ShowMapResult struct {
	Action MapAction
}

type IdentityOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	StateDir    string
	Identity    state.MachineIdentity
	Theme       termstyle.Theme
}

type MetadataOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	Record      state.SessionRecord
	Theme       termstyle.Theme
}

type ListOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	StateDir    string
	Records     []state.SessionRecord
	Theme       termstyle.Theme
}

func MapLines(stateDir string, records []state.SessionRecord, currentID string) []string {
	return MapLinesWithOptions(stateDir, records, MapOptions{CurrentID: currentID})
}

func MapForest(records []state.SessionRecord) []state.SessionNode {
	return state.BuildSessionForest(displayRecordsForMap(records))
}

func MapLinesWithOptions(stateDir string, records []state.SessionRecord, opts MapOptions) []string {
	active, exited := CountStatuses(records)
	visible := records
	if !opts.IncludeExited {
		visible = ActiveRecords(records)
	}

	lines := []string{"Session route map", fmt.Sprintf("state: %s", stateDir)}
	if opts.IncludeExited {
		lines = append(lines, fmt.Sprintf("active: %d  exited: %d  total: %d", active, exited, len(records)))
	} else {
		lines = append(lines, fmt.Sprintf("active: %d", active))
	}
	lines = append(lines, "", "ROUTES")

	if len(visible) == 0 {
		if opts.IncludeExited {
			return append(lines, "No supervised sessions recorded.")
		}
		return append(lines, "No active supervised sessions.")
	}

	roots := MapForest(visible)
	theme := termstyle.TerminalTheme().WithNoColor(true)
	for i, root := range roots {
		lines = appendNodeLines(lines, root, "", i == len(roots)-1, opts.CurrentID, theme, 120)
	}
	return lines
}

func WriteMap(w io.Writer, stateDir string, records []state.SessionRecord) {
	WriteMapWithOptions(w, stateDir, records, MapOptions{})
}

func WriteMapWithOptions(w io.Writer, stateDir string, records []state.SessionRecord, opts MapOptions) {
	for _, line := range MapLinesWithOptions(stateDir, records, opts) {
		fmt.Fprintln(w, line)
	}
}

func MapView(opts ViewOptions) tea.View {
	width := clamp(opts.Width, 48, 140)
	height := max(8, opts.Height)
	theme := opts.Theme
	if theme.Codes == nil {
		theme = termstyle.TerminalTheme()
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "SSHERPA SESSION MAP"
	}
	innerWidth := max(12, width-4)
	active, exited := CountStatuses(opts.Records)
	visible := opts.Records
	if !opts.Map.IncludeExited {
		visible = ActiveRecords(opts.Records)
	}

	lines := []string{
		boxTop(theme, mapHeader(theme, title, opts.StateDir, active, exited, len(opts.Records), len(visible), opts.Map.IncludeExited), width),
	}

	reserved := 1
	if opts.Help != "" {
		reserved += 2
	}
	bodyBudget := max(1, height-len(lines)-reserved)
	body := mapBodyLines(visible, opts.Map.CurrentID, theme, innerWidth)
	if len(body) > bodyBudget {
		hidden := len(body) - bodyBudget + 1
		body = append(body[:max(0, bodyBudget-1)], theme.Style(termstyle.RoleMuted, fmt.Sprintf("... %d more line(s)", hidden)))
	}
	for _, line := range body {
		lines = append(lines, boxLine(theme, line, width))
	}
	if opts.Help != "" {
		lines = append(lines, boxDivider(theme, width))
		lines = append(lines, boxLine(theme, theme.Style(termstyle.RoleMuted, truncateVisible(opts.Help, innerWidth)), width))
	}
	lines = append(lines, boxBottom(theme, width))

	return tea.NewView(strings.Join(lines, "\n"))
}

func ShowMap(ctx context.Context, opts ShowOptions) error {
	_, err := ShowMapWithResult(ctx, opts)
	return err
}

func ShowMapWithResult(ctx context.Context, opts ShowOptions) (ShowMapResult, error) {
	model := mapModel{
		noAltScreen: opts.NoAltScreen,
		view:        opts.View,
		width:       96,
		height:      24,
	}
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return ShowMapResult{}, err
	}
	mapFinal, ok := final.(mapModel)
	if !ok {
		return ShowMapResult{Action: MapActionBack}, nil
	}
	if mapFinal.action == "" {
		mapFinal.action = MapActionBack
	}
	return ShowMapResult{Action: mapFinal.action}, nil
}

func ShowIdentity(ctx context.Context, opts IdentityOptions) error {
	theme := opts.Theme
	if theme.Codes == nil {
		theme = termstyle.TerminalTheme()
	}
	model := identityModel{
		noAltScreen: opts.NoAltScreen,
		stateDir:    opts.StateDir,
		identity:    opts.Identity,
		theme:       theme,
		width:       86,
		height:      14,
	}
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}

func ShowMetadata(ctx context.Context, opts MetadataOptions) error {
	theme := opts.Theme
	if theme.Codes == nil {
		theme = termstyle.TerminalTheme()
	}
	model := metadataModel{
		noAltScreen: opts.NoAltScreen,
		record:      opts.Record,
		theme:       theme,
		width:       100,
		height:      24,
	}
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}

func ShowList(ctx context.Context, opts ListOptions) error {
	theme := opts.Theme
	if theme.Codes == nil {
		theme = termstyle.TerminalTheme()
	}
	model := listModel{
		noAltScreen: opts.NoAltScreen,
		stateDir:    opts.StateDir,
		records:     append([]state.SessionRecord(nil), opts.Records...),
		theme:       theme,
		width:       110,
		height:      30,
		mode:        "all",
	}
	model.applyFilter()
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}

type listModel struct {
	noAltScreen bool
	stateDir    string
	records     []state.SessionRecord
	filtered    []int
	cursor      int
	scroll      int
	mode        string
	theme       termstyle.Theme
	width       int
	height      int
}

func (m listModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.ensureListCursor()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q", "Q":
			return m, tea.Quit
		case "up", "k":
			m.cursor--
		case "down", "j":
			m.cursor++
		case "pgup":
			m.cursor -= m.listHeight()
		case "pgdown":
			m.cursor += m.listHeight()
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = len(m.filtered) - 1
		case "a":
			m.mode = "all"
			m.applyFilter()
		case "l":
			m.mode = "local"
			m.applyFilter()
		case "i":
			m.mode = "imported"
			m.applyFilter()
		case "o":
			// Active filter. Moved off "v" so v=receive / X=escape-rope can be
			// reserved globally across the three map faces (G7).
			m.mode = "active"
			m.applyFilter()
		case "e":
			m.mode = "exited"
			m.applyFilter()
		}
		m.ensureListCursor()
	}
	return m, nil
}

func (m listModel) View() tea.View {
	width := max(72, m.width)
	height := max(16, m.height)
	inner := max(20, width-4)
	active, exited := CountStatuses(m.records)
	imported := 0
	for _, record := range m.records {
		if record.Import != nil {
			imported++
		}
	}
	title := joinVisible(
		m.theme.Style(termstyle.RoleForeground, "SESSIONS"),
		m.theme.Style(termstyle.RoleMuted, fmt.Sprintf("%s  active %d  exited %d  imported %d  total %d", strings.ToUpper(m.mode), active, exited, imported, len(m.records))),
		inner,
	)
	lines := []string{boxTop(m.theme, title, width)}
	listHeight := min(m.listHeight(), max(1, height-12))
	if len(m.filtered) == 0 {
		lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, "No sessions for this filter."), width))
	} else {
		m.ensureListCursor()
		end := min(len(m.filtered), m.scroll+listHeight)
		for row := m.scroll; row < end; row++ {
			record := m.records[m.filtered[row]]
			lines = append(lines, boxLine(m.theme, sessionListRow(record, row == m.cursor, m.theme, inner), width))
		}
		for row := end - m.scroll; row < listHeight; row++ {
			lines = append(lines, boxLine(m.theme, "", width))
		}
	}
	lines = append(lines, boxDivider(m.theme, width))
	if selected, ok := m.selectedRecord(); ok {
		for _, line := range sessionListDetailLines(selected, m.theme, inner, max(3, height-len(lines)-3)) {
			lines = append(lines, boxLine(m.theme, line, width))
		}
	} else {
		lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, "No session selected."), width))
	}
	lines = append(lines, boxDivider(m.theme, width))
	lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, chrome.Footer([]chrome.KeyHint{
		{Key: "arrows", Label: "move"},
		{Key: "a", Label: "all"},
		{Key: "o", Label: "active"},
		{Key: "e", Label: "exited"},
		{Key: "l", Label: "local"},
		{Key: "i", Label: "imported"},
		{Key: "q", Label: "back"},
	}, 0)), width))
	lines = append(lines, boxBottom(m.theme, width))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m *listModel) applyFilter() {
	m.filtered = m.filtered[:0]
	for i, record := range m.records {
		switch m.mode {
		case "local":
			if record.Import != nil {
				continue
			}
		case "imported":
			if record.Import == nil {
				continue
			}
		case "active":
			if !IsRouteActive(record) {
				continue
			}
		case "exited":
			if IsRouteActive(record) {
				continue
			}
		}
		m.filtered = append(m.filtered, i)
	}
	m.cursor = 0
	m.scroll = 0
}

func (m *listModel) ensureListCursor() {
	if len(m.filtered) == 0 {
		m.cursor = 0
		m.scroll = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	height := m.listHeight()
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+height {
		m.scroll = m.cursor - height + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m listModel) listHeight() int {
	return clamp(m.height/3, 5, 12)
}

func (m listModel) selectedRecord() (state.SessionRecord, bool) {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return state.SessionRecord{}, false
	}
	return m.records[m.filtered[m.cursor]], true
}

func sessionListRow(record state.SessionRecord, selected bool, theme termstyle.Theme, width int) string {
	marker := "  "
	role := statusRole(record, "")
	if selected {
		marker = "▶ "
		role = termstyle.RoleSelected
	}
	left := marker + theme.Style(role, StatusLabel(record)) + "  " + theme.Style(termstyle.RoleForeground, Target(record))
	if kind := KindBadge(record); kind != "" {
		left += " " + theme.Style(termstyle.RoleInfo, kind)
	}
	if record.Import != nil {
		left += " " + theme.Style(termstyle.RoleWarning, importBadge(record))
	}
	rightParts := []string{ShortSessionID(record.ID)}
	if !record.StartedAt.IsZero() {
		rightParts = append(rightParts, record.StartedAt.Local().Format("Jan 02 15:04"))
	}
	if record.Transcript != nil {
		rightParts = append(rightParts, humanBytes(record.Transcript.Bytes))
	}
	right := theme.Style(termstyle.RoleMuted, strings.Join(rightParts, "  "))
	return truncateVisible(joinVisible(left, right, width), width)
}

func sessionListDetailLines(record state.SessionRecord, theme termstyle.Theme, width int, maxLines int) []string {
	lines := []string{}
	lines = append(lines, detailLine(theme, "id", record.ID, width))
	lines = append(lines, detailLine(theme, "target", Target(record), width))
	lines = append(lines, detailLine(theme, "route", FormatRecordRoute(record), width))
	lines = append(lines, detailLine(theme, "status", StatusLabel(record), width))
	if !record.StartedAt.IsZero() {
		lines = append(lines, detailLine(theme, "started", record.StartedAt.Local().Format(time.RFC3339), width))
	}
	if record.EndedAt != nil {
		lines = append(lines, detailLine(theme, "ended", record.EndedAt.Local().Format(time.RFC3339), width))
	}
	if record.ExitCode != nil {
		lines = append(lines, detailLine(theme, "exit", fmt.Sprintf("%d", *record.ExitCode), width))
	}
	if record.Transcript != nil {
		transcriptParts := []string{record.Transcript.Path, humanBytes(record.Transcript.Bytes)}
		if record.Transcript.Truncated {
			transcriptParts = append(transcriptParts, "truncated")
		}
		lines = append(lines, detailLine(theme, "transcript", strings.Join(transcriptParts, " · "), width))
	}
	if record.RecordedBy != nil {
		lines = append(lines, detailLine(theme, "recorded by", shortMachineID(record.RecordedBy.MachineID)+" · "+defaultString(record.RecordedBy.SSHerpaVersion, "unknown"), width))
	}
	if record.Import != nil {
		lines = append(lines, detailLine(theme, "import", record.Import.OriginClass+" · source "+defaultString(record.Import.SourceSessionID, "unknown")+" · machine "+shortMachineID(record.Import.SourceMachineID), width))
	}
	if forward := ForwardSummary(record); forward != "" {
		lines = append(lines, detailLine(theme, "forward", forward, width))
	}
	if proxy := ProxySummary(record); proxy != "" {
		lines = append(lines, detailLine(theme, "proxy", proxy, width))
	}
	if remote := RemoteSummary(record); remote != "" {
		lines = append(lines, detailLine(theme, "remote", remote, width))
	}
	if health := HealthSummary(record); health != "" {
		lines = append(lines, detailLine(theme, "health", health, width))
	}
	if len(record.Events) > 0 {
		last := record.Events[len(record.Events)-1]
		lines = append(lines, detailLine(theme, "last event", last.Type+" · "+last.Message, width))
	}
	if len(lines) > maxLines {
		hidden := len(lines) - maxLines + 1
		lines = append(lines[:max(0, maxLines-1)], theme.Style(termstyle.RoleMuted, fmt.Sprintf("... %d more detail line(s)", hidden)))
	}
	return lines
}

func detailLine(theme termstyle.Theme, label string, value string, width int) string {
	labelText := theme.Style(termstyle.RoleAccent, termstyle.PadRight(label, 12))
	valueWidth := max(0, width-termstyle.VisibleWidth(labelText)-2)
	return labelText + "  " + theme.Style(termstyle.RoleForeground, truncateVisible(defaultString(cleanField(value), "-"), valueWidth))
}

func importBadge(record state.SessionRecord) string {
	if record.Import == nil {
		return ""
	}
	switch record.Import.OriginClass {
	case "imported_self":
		return "[imported self]"
	case "imported_other":
		return "[imported other]"
	default:
		return "[imported unknown]"
	}
}

type metadataModel struct {
	noAltScreen bool
	record      state.SessionRecord
	theme       termstyle.Theme
	width       int
	height      int
	scroll      int
}

func (m metadataModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m metadataModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.clampMetadataScroll()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q", "Q":
			return m, tea.Quit
		case "up", "k":
			m.scroll--
		case "down", "j":
			m.scroll++
		case "pgup":
			m.scroll -= m.metadataBodyHeight()
		case "pgdown":
			m.scroll += m.metadataBodyHeight()
		case "home", "g":
			m.scroll = 0
		case "end", "G":
			m.scroll = m.maxMetadataScroll()
		}
		m.clampMetadataScroll()
	}
	return m, nil
}

func (m metadataModel) View() tea.View {
	width := clamp(m.width, 72, 120)
	height := max(14, m.height)
	inner := max(20, width-4)
	bodyHeight := max(1, height-4)
	all := metadataLines(m.record, m.theme, inner)
	end := min(len(all), m.scroll+bodyHeight)
	body := all[m.scroll:end]
	title := joinVisible(
		m.theme.Style(termstyle.RoleForeground, "SESSION METADATA"),
		m.theme.Style(termstyle.RoleMuted, ShortSessionID(m.record.ID)),
		inner,
	)
	lines := []string{boxTop(m.theme, title, width)}
	for _, line := range body {
		lines = append(lines, boxLine(m.theme, line, width))
	}
	for len(body) < bodyHeight {
		lines = append(lines, boxLine(m.theme, "", width))
		body = append(body, "")
	}
	lines = append(lines, boxDivider(m.theme, width))
	footer := fmt.Sprintf("arrows scroll / q back  %d/%d", min(m.scroll+1, max(1, len(all))), max(1, len(all)))
	lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, truncateVisible(footer, inner)), width))
	lines = append(lines, boxBottom(m.theme, width))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m metadataModel) metadataBodyHeight() int {
	return max(1, m.height-4)
}

func (m metadataModel) maxMetadataScroll() int {
	return max(0, len(metadataLines(m.record, m.theme, max(20, clamp(m.width, 72, 120)-4)))-m.metadataBodyHeight())
}

func (m *metadataModel) clampMetadataScroll() {
	if m.scroll < 0 {
		m.scroll = 0
	}
	if max := m.maxMetadataScroll(); m.scroll > max {
		m.scroll = max
	}
}

func metadataLines(record state.SessionRecord, theme termstyle.Theme, width int) []string {
	lines := []string{}
	add := func(label string, value string) {
		lines = append(lines, metadataLine(theme, label, value, width))
	}
	add("id", record.ID)
	add("status", StatusLabel(record))
	add("target", Target(record))
	add("depth", fmt.Sprintf("%d", record.Depth))
	add("route", FormatRecordRoute(record))
	add("hops", FormatRoute(record.Hops))
	if forward := ForwardSummary(record); forward != "" {
		add("forward", forward)
	}
	if proxy := ProxySummary(record); proxy != "" {
		add("proxy", proxy)
	}
	if muxer := MuxerSummary(record); muxer != "" {
		add("muxer", muxer)
	}
	if remote := RemoteSummary(record); remote != "" {
		add("remote", remote)
	}
	add("started", metadataTime(record.StartedAt))
	add("ended", metadataOptionalTime(record.EndedAt))
	if record.ExitCode != nil {
		add("exit code", fmt.Sprintf("%d", *record.ExitCode))
	}
	if record.Transcript != nil {
		add("transcript", record.Transcript.Path)
		add("format", record.Transcript.Format)
		add("bytes", humanBytes(record.Transcript.Bytes))
		add("frames", fmt.Sprintf("%d", record.Transcript.Frames))
		if record.Transcript.Truncated {
			add("truncated", "yes")
		}
	}
	if record.RecordedBy != nil {
		add("recorded by", record.RecordedBy.MachineID)
		add("identity", fmt.Sprintf("schema %d · version %s", record.RecordedBy.IdentitySchema, defaultString(record.RecordedBy.SSHerpaVersion, "unknown")))
	}
	if record.Import != nil {
		add("imported at", metadataTime(record.Import.ImportedAt))
		add("origin", record.Import.OriginClass)
		add("source", defaultString(record.Import.SourceSessionID, "unknown"))
		add("machine", defaultString(record.Import.SourceMachineID, "unknown"))
		add("bundle", record.Import.BundleSHA256)
	}
	if record.DisconnectReason != "" {
		add("disconnect", record.DisconnectReason)
	}
	add("local pid", fmt.Sprintf("%d", record.LocalPID))
	add("ssh pid", fmt.Sprintf("%d", record.SSHPID))
	add("runner", record.RunnerMode)
	add("argv", strings.Join(record.SSHArgv, " "))
	if len(record.Events) > 0 {
		lines = append(lines, theme.Style(termstyle.RoleAccent, "events"))
		for _, event := range record.Events {
			value := event.Time.Local().Format(time.RFC3339) + " · " + event.Type
			if event.LatencyMillis > 0 {
				value += fmt.Sprintf(" · latency %dms", event.LatencyMillis)
			}
			if event.ThresholdMillis > 0 {
				value += fmt.Sprintf(" · threshold %dms", event.ThresholdMillis)
			}
			if event.Message != "" {
				value += " · " + event.Message
			}
			lines = append(lines, wrapMetadataValue(theme, "event", value, width)...)
		}
	}
	return lines
}

func metadataLine(theme termstyle.Theme, label string, value string, width int) string {
	labelText := theme.Style(termstyle.RoleAccent, termstyle.PadRight(label, 13))
	valueWidth := max(0, width-termstyle.VisibleWidth(labelText)-2)
	return labelText + "  " + theme.Style(termstyle.RoleForeground, truncateVisible(defaultString(cleanField(value), "-"), valueWidth))
}

func wrapMetadataValue(theme termstyle.Theme, label string, value string, width int) []string {
	labelText := theme.Style(termstyle.RoleAccent, termstyle.PadRight(label, 13))
	valueWidth := max(1, width-termstyle.VisibleWidth(labelText)-2)
	wrapped := wrapIdentityText(defaultString(cleanField(value), "-"), valueWidth)
	out := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		currentLabel := labelText
		if i > 0 {
			currentLabel = strings.Repeat(" ", termstyle.VisibleWidth(labelText))
		}
		out = append(out, currentLabel+"  "+theme.Style(termstyle.RoleForeground, line))
	}
	return out
}

func metadataTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Local().Format(time.RFC3339)
}

func metadataOptionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return metadataTime(*value)
}

type identityModel struct {
	noAltScreen bool
	stateDir    string
	identity    state.MachineIdentity
	theme       termstyle.Theme
	width       int
	height      int
}

func (m identityModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m identityModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m identityModel) View() tea.View {
	width := clamp(m.width, 58, 110)
	inner := max(20, width-4)
	lines := []string{
		boxTop(m.theme, m.theme.Style(termstyle.RoleForeground, "MACHINE IDENTITY"), width),
		boxLine(m.theme, identityFieldLine(m.theme, "machine id", m.identity.MachineID, inner), width),
		boxLine(m.theme, identityFieldLine(m.theme, "schema", fmt.Sprintf("%d", m.identity.SchemaVersion), inner), width),
		boxLine(m.theme, identityFieldLine(m.theme, "created", m.identity.CreatedAt.Local().Format(time.RFC3339), inner), width),
		boxLine(m.theme, identityFieldLine(m.theme, "created by", defaultString(m.identity.CreatedByVersion, "unknown"), inner), width),
		boxLine(m.theme, identityFieldLine(m.theme, "state", m.stateDir, inner), width),
		boxDivider(m.theme, width),
	}
	for _, line := range wrapIdentityText("This UUID classifies imported bundles as this machine, another machine, or unknown origin. It is not cryptographic proof.", inner) {
		lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleForeground, line), width))
	}
	lines = append(lines,
		boxDivider(m.theme, width),
		boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, "press any key to return"), width),
		boxBottom(m.theme, width),
	)
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = !m.noAltScreen
	return view
}

func identityFieldLine(theme termstyle.Theme, label string, value string, width int) string {
	labelText := theme.Style(termstyle.RoleAccent, termstyle.PadRight(label, 11))
	valueWidth := max(0, width-termstyle.VisibleWidth(labelText)-2)
	return labelText + "  " + theme.Style(termstyle.RoleForeground, truncateVisible(defaultString(value, "-"), valueWidth))
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func wrapIdentityText(value string, width int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := []string{}
	current := words[0]
	for _, word := range words[1:] {
		candidate := current + " " + word
		if termstyle.VisibleWidth(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = word
	}
	lines = append(lines, current)
	return lines
}

type TranscriptOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	StateDir    string
	Record      state.SessionRecord
	Theme       termstyle.Theme
	Raw         bool
	Follow      bool
}

func ShowTranscript(ctx context.Context, opts TranscriptOptions) error {
	theme := opts.Theme
	if theme.Codes == nil {
		theme = termstyle.TerminalTheme()
	}
	model := transcriptModel{
		noAltScreen: opts.NoAltScreen,
		stateDir:    opts.StateDir,
		record:      opts.Record,
		theme:       theme,
		raw:         opts.Raw,
		follow:      opts.Follow,
		width:       100,
		height:      28,
	}
	model.reload()
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}

type transcriptTickMsg struct{}

type transcriptModel struct {
	noAltScreen bool
	stateDir    string
	record      state.SessionRecord
	theme       termstyle.Theme
	raw         bool
	follow      bool
	searching   bool
	rawWarning  bool
	query       string
	lines       []string
	errText     string
	warnText    string
	scroll      int
	width       int
	height      int
}

func (m transcriptModel) Init() tea.Cmd {
	return tea.Batch(tea.RequestWindowSize, transcriptTick())
}

func (m transcriptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.clampScroll()
	case transcriptTickMsg:
		if m.follow {
			m.reload()
			m.scroll = m.maxScroll()
		}
		return m, transcriptTick()
	case tea.KeyPressMsg:
		key := msg.String()
		if m.searching {
			switch key {
			case "esc":
				m.searching = false
			case "enter":
				m.searching = false
				m.jumpToQuery(1)
			case "backspace":
				if m.query != "" {
					m.query = m.query[:len(m.query)-1]
				}
			default:
				if msg.Text != "" {
					m.query += msg.Text
				}
			}
			return m, nil
		}
		switch key {
		case "ctrl+c", "esc", "q", "Q":
			return m, tea.Quit
		case "up", "k":
			m.scroll--
		case "down", "j":
			m.scroll++
		case "pgup":
			m.scroll -= m.bodyHeight()
		case "pgdown":
			m.scroll += m.bodyHeight()
		case "home", "g":
			m.scroll = 0
		case "end", "G":
			m.scroll = m.maxScroll()
		case "/":
			m.searching = true
			m.query = ""
		case "n":
			m.jumpToQuery(1)
		case "N":
			m.jumpToQuery(-1)
		case "r":
			if m.record.Import != nil && !m.raw && !m.rawWarning {
				m.rawWarning = true
				return m, nil
			}
			m.rawWarning = false
			m.raw = !m.raw
			m.reload()
		case "f":
			m.follow = !m.follow
			if m.follow {
				m.reload()
				m.scroll = m.maxScroll()
			}
		}
		m.clampScroll()
	}
	return m, nil
}

func (m transcriptModel) View() tea.View {
	width := max(64, m.width)
	height := max(12, m.height)
	inner := max(20, width-4)
	lines := []string{
		boxTop(m.theme, transcriptHeader(m, inner), width),
	}
	meta := transcriptMetaLine(m)
	if meta != "" {
		lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, truncateVisible(meta, inner)), width))
		lines = append(lines, boxDivider(m.theme, width))
	}
	if m.warnText != "" {
		lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleWarning, truncateVisible(m.warnText, inner)), width))
	}
	if m.errText != "" {
		lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleDanger, truncateVisible(m.errText, inner)), width))
	} else {
		bodyHeight := max(1, height-len(lines)-3)
		body := m.visibleLines(bodyHeight, inner)
		for _, line := range body {
			lines = append(lines, boxLine(m.theme, line, width))
		}
		for len(body) < bodyHeight {
			lines = append(lines, boxLine(m.theme, "", width))
			body = append(body, "")
		}
	}
	lines = append(lines, boxDivider(m.theme, width))
	footer := "arrows scroll / / search / n next / r raw / f follow / q back"
	if m.rawWarning {
		footer = "imported raw output is untrusted; press r again to show raw / q back"
	}
	if m.searching {
		footer = "/ " + m.query
	}
	lines = append(lines, boxLine(m.theme, m.theme.Style(termstyle.RoleMuted, truncateVisible(footer, inner)), width))
	lines = append(lines, boxBottom(m.theme, width))
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m *transcriptModel) reload() {
	path := transcriptPath(m.stateDir, m.record)
	rec, err := transcript.Read(path)
	if err != nil && !errors.Is(err, transcript.ErrTornTail) {
		m.errText = err.Error()
		m.warnText = ""
		m.lines = nil
		return
	}
	m.errText = ""
	var warnings []string
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("transcript tail is incomplete; showing %d frame(s)", len(rec.Frames)))
	}
	if rec.SkippedLines > 0 {
		warnings = append(warnings, fmt.Sprintf("%d unparseable line(s) skipped", rec.SkippedLines))
	}
	m.warnText = strings.Join(warnings, " · ")
	text := transcript.Text(rec, transcript.TextOptions{Raw: m.raw})
	m.lines = strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(m.lines) == 1 && m.lines[0] == "" {
		m.lines = nil
	}
	m.clampScroll()
}

func (m transcriptModel) visibleLines(count int, width int) []string {
	if len(m.lines) == 0 {
		return []string{m.theme.Style(termstyle.RoleMuted, "No transcript output recorded.")}
	}
	end := min(len(m.lines), m.scroll+count)
	out := make([]string, 0, end-m.scroll)
	for i := m.scroll; i < end; i++ {
		line := truncateVisible(m.lines[i], width)
		if m.query != "" && strings.Contains(strings.ToLower(termstyle.Strip(line)), strings.ToLower(m.query)) {
			line = m.theme.Style(termstyle.RoleSelected, line)
		}
		out = append(out, line)
	}
	return out
}

func (m *transcriptModel) jumpToQuery(direction int) {
	q := strings.ToLower(strings.TrimSpace(m.query))
	if q == "" || len(m.lines) == 0 {
		return
	}
	start := m.scroll
	for step := 1; step <= len(m.lines); step++ {
		index := (start + step*direction) % len(m.lines)
		if index < 0 {
			index += len(m.lines)
		}
		if strings.Contains(strings.ToLower(termstyle.Strip(m.lines[index])), q) {
			m.scroll = index
			return
		}
	}
}

func (m transcriptModel) bodyHeight() int {
	return max(1, m.height-6)
}

func (m transcriptModel) maxScroll() int {
	return max(0, len(m.lines)-m.bodyHeight())
}

func (m *transcriptModel) clampScroll() {
	if m.scroll < 0 {
		m.scroll = 0
	}
	if max := m.maxScroll(); m.scroll > max {
		m.scroll = max
	}
}

func transcriptTick() tea.Cmd {
	return tea.Tick(750*time.Millisecond, func(time.Time) tea.Msg {
		return transcriptTickMsg{}
	})
}

func transcriptHeader(m transcriptModel, width int) string {
	target := Target(m.record)
	mode := "clean"
	if m.raw {
		mode = "raw"
	}
	if m.follow {
		mode += " follow"
	}
	if m.record.Import != nil {
		mode = importedHeaderLabel(m.record) + "  " + mode
	}
	left := strings.ToUpper("transcript " + target)
	right := mode + "  " + strconv.Itoa(m.scroll+1) + "/" + strconv.Itoa(max(1, len(m.lines)))
	return joinVisible(m.theme.Style(termstyle.RoleForeground, left), m.theme.Style(termstyle.RoleMuted, right), width)
}

func transcriptMetaLine(m transcriptModel) string {
	parts := []string{
		"id " + ShortSessionID(m.record.ID),
		StatusLabel(m.record),
		"route " + FormatRecordRoute(m.record),
	}
	if m.record.Import != nil {
		parts = append(parts, "source "+ShortSessionID(m.record.Import.SourceSessionID))
		parts = append(parts, "machine "+shortMachineID(m.record.Import.SourceMachineID))
		parts = append(parts, cleanField(m.record.Import.OriginClass))
	}
	if m.record.Transcript != nil {
		parts = append(parts, humanBytes(m.record.Transcript.Bytes))
		if m.record.Transcript.Truncated {
			parts = append(parts, "truncated")
		}
	} else if info, err := os.Stat(transcriptPath(m.stateDir, m.record)); err == nil {
		parts = append(parts, humanBytes(info.Size()))
	}
	return strings.Join(parts, " · ")
}

func importedHeaderLabel(record state.SessionRecord) string {
	if record.Import == nil {
		return ""
	}
	switch record.Import.OriginClass {
	case "imported_self":
		return "IMPORTED SELF"
	case "imported_other":
		return "IMPORTED OTHER"
	default:
		return "IMPORTED UNKNOWN"
	}
}

func shortMachineID(id string) string {
	id = strings.TrimSpace(cleanField(id))
	if id == "" {
		return "unknown"
	}
	if len(id) <= 12 {
		return id
	}
	return id[:8]
}

func transcriptPath(stateDir string, record state.SessionRecord) string {
	if record.Transcript != nil && strings.TrimSpace(record.Transcript.Path) != "" {
		return record.Transcript.Path
	}
	return filepath.Join(state.SessionsDir(stateDir), record.ID+".cast")
}

func humanBytes(n int64) string {
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

type mapModel struct {
	noAltScreen bool
	view        ViewOptions
	width       int
	height      int
	action      MapAction
}

func (m mapModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m mapModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "d", "D":
			m.action = MapActionDeleteAllData
		default:
			m.action = MapActionBack
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m mapModel) View() tea.View {
	opts := m.view
	opts.Width = m.width
	opts.Height = m.height
	if strings.TrimSpace(opts.Help) == "" {
		opts.Help = "q back / D delete all local data"
	}
	view := MapView(opts)
	view.AltScreen = !m.noAltScreen
	return view
}

func CountStatuses(records []state.SessionRecord) (active int, exited int) {
	for _, record := range records {
		if IsRouteActive(record) {
			active++
		} else {
			exited++
		}
	}
	return active, exited
}

func ActiveRecords(records []state.SessionRecord) []state.SessionRecord {
	active := make([]state.SessionRecord, 0, len(records))
	for _, record := range records {
		if IsRouteActive(record) {
			active = append(active, record)
		}
	}
	return active
}

func IsActive(record state.SessionRecord) bool {
	return state.ProcessAlive(record)
}

func IsRouteActive(record state.SessionRecord) bool {
	return IsActive(record) || (record.RemoteMirror && record.EndedAt == nil)
}

func StatusLabel(record state.SessionRecord) string {
	if record.Inherited {
		return "inherited"
	}
	if record.Import != nil && record.EndedAt == nil {
		return "imported"
	}
	if IsActive(record) {
		return "active"
	}
	if record.RemoteMirror && record.EndedAt == nil {
		return "remote"
	}
	if record.EndedAt == nil {
		return "orphan"
	}
	if record.ExitCode != nil {
		return fmt.Sprintf("exit %d", *record.ExitCode)
	}
	return "exited"
}

// KindBadge returns a short bracketed tag distinguishing operator-facing
// session types so they stand out in list / map output.
func KindBadge(record state.SessionRecord) string {
	if record.Inherited {
		return ""
	}
	switch record.Kind {
	case state.KindTunnel:
		return "[forward]"
	case state.KindProxy:
		return "[proxy]"
	default:
		if len(record.Hops) > 0 || len(record.Route) > 1 {
			return "[jump]"
		}
		return ""
	}
}

// cleanField sanitizes a session-record string for terminal rendering.
// Record fields are untrusted at the render boundary — a hostile remote
// (telemetry mirrors) or an imported bundle controls them — so every
// sink neutralizes escape sequences AND raw C0/C1 control bytes
// (U+009B is CSI to xterm-class terminals) regardless of any
// parse-time cleaning upstream. The scan below only routes strings
// that could carry something unsafe through Sanitize: ESC, C0 (except
// tab), DEL, or a 0xC2 lead byte (the UTF-8 prefix of every C1 rune).
func cleanField(value string) string {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b == 0x1b || (b < 0x20 && b != '\t') || b == 0x7f || b == 0xc2 {
			return termstyle.Sanitize(value)
		}
	}
	return value
}

func Target(record state.SessionRecord) string {
	if alias := strings.TrimSpace(cleanField(record.TargetAlias)); alias != "" {
		return alias
	}
	if len(record.Route) > 0 {
		if last := strings.TrimSpace(cleanField(record.Route[len(record.Route)-1])); last != "" {
			return last
		}
	}
	return "-"
}

func FormatRoute(route []string) string {
	if len(route) == 0 {
		return "-"
	}
	parts := make([]string, len(route))
	for i, part := range route {
		parts[i] = cleanField(part)
	}
	return strings.Join(parts, " -> ")
}

func FormatDisplayRoute(route []string) string {
	return FormatRoute(displayRouteParts(route))
}

func FormatRecordRoute(record state.SessionRecord) string {
	return FormatRoute(displayRecordRouteParts(record))
}

func writeListGroup(w io.Writer, title string, records []state.SessionRecord, active bool) {
	first := true
	for _, record := range records {
		if IsActive(record) != active {
			continue
		}
		if first {
			fmt.Fprintf(w, "\n%s\n", title)
			first = false
		}
		target := Target(record)
		if badge := KindBadge(record); badge != "" {
			target = target + " " + badge
		}
		fmt.Fprintf(w, "%s\t%s\tdepth=%d\ttarget=%s\troute=%s\tstarted=%s\n",
			cleanField(record.ID),
			StatusLabel(record),
			record.Depth,
			target,
			FormatRecordRoute(record),
			record.StartedAt.Local().Format(time.RFC3339),
		)
		if health := HealthSummary(record); health != "" {
			fmt.Fprintf(w, "\thealth=%s\n", health)
		}
		if record.Transcript != nil {
			fmt.Fprintf(w, "\ttranscript=%s\tbytes=%s\n", cleanField(record.Transcript.Path), humanBytes(record.Transcript.Bytes))
		}
		if record.Import != nil {
			fmt.Fprintf(w, "\timport=%s\tsource_session=%s\tsource_machine=%s\n", cleanField(record.Import.OriginClass), cleanField(record.Import.SourceSessionID), shortMachineID(record.Import.SourceMachineID))
		}
	}
}

func appendNodeLines(lines []string, node state.SessionNode, prefix string, last bool, currentID string, theme termstyle.Theme, width int) []string {
	return appendPlainNodeLines(lines, node, prefix, last, currentID, theme, width, map[string]bool{})
}

func appendPlainNodeLines(lines []string, node state.SessionNode, prefix string, last bool, currentID string, theme termstyle.Theme, width int, managedTargets map[string]bool) []string {
	record := node.Record
	if record.Inherited {
		nextManaged := inheritedManagedTargets(record, managedTargets)
		for i, child := range node.Children {
			lines = appendPlainNodeLines(lines, child, prefix, i == len(node.Children)-1, currentID, theme, width, nextManaged)
		}
		return lines
	}
	linePrefix, detailPrefix, nextPrefix := treePrefixes(prefix, last)
	lines = append(lines, sessionSummaryLine(linePrefix, record, currentID, theme, width))
	lines = append(lines, routeSpineLines(detailPrefix, displayRecordRouteParts(record), record, managedTargets, theme, width)...)
	lines = append(lines, sessionDetailLines(detailPrefix, record, theme, width)...)
	nextManaged := recordManagedTargets(record, managedTargets)
	for i, child := range node.Children {
		lines = appendPlainNodeLines(lines, child, nextPrefix, i == len(node.Children)-1, currentID, theme, width, nextManaged)
	}
	return lines
}

func mapBodyLines(records []state.SessionRecord, currentID string, theme termstyle.Theme, width int) []string {
	if len(records) == 0 {
		return []string{theme.Style(termstyle.RoleMuted, "No active supervised sessions.")}
	}
	roots := MapForest(records)
	lines := []string{}
	for i, root := range roots {
		lines = appendStyledNodeLines(lines, root, "", i == len(roots)-1, currentID, theme, width, map[string]bool{})
	}
	return lines
}

func appendStyledNodeLines(lines []string, node state.SessionNode, prefix string, last bool, currentID string, theme termstyle.Theme, width int, managedTargets map[string]bool) []string {
	record := node.Record
	if record.Inherited {
		nextManaged := inheritedManagedTargets(record, managedTargets)
		for i, child := range node.Children {
			lines = appendStyledNodeLines(lines, child, prefix, i == len(node.Children)-1, currentID, theme, width, nextManaged)
		}
		return lines
	}
	linePrefix, detailPrefix, nextPrefix := treePrefixes(prefix, last)
	lines = append(lines, sessionSummaryLine(linePrefix, record, currentID, theme, width))
	lines = append(lines, routeSpineLines(detailPrefix, displayRecordRouteParts(record), record, managedTargets, theme, width)...)
	lines = append(lines, sessionDetailLines(detailPrefix, record, theme, width)...)
	nextManaged := recordManagedTargets(record, managedTargets)
	for i, child := range node.Children {
		lines = appendStyledNodeLines(lines, child, nextPrefix, i == len(node.Children)-1, currentID, theme, width, nextManaged)
	}
	return lines
}

func treePrefixes(prefix string, last bool) (linePrefix string, detailPrefix string, nextPrefix string) {
	if prefix == "" {
		return "", "  ", "  "
	}
	connector := "├─ "
	continuation := "│  "
	if last {
		connector = "└─ "
		continuation = "   "
	}
	return prefix + connector, prefix + continuation, prefix + continuation
}

func sessionSummaryLine(prefix string, record state.SessionRecord, currentID string, theme termstyle.Theme, width int) string {
	left := prefix + theme.Style(statusRole(record, currentID), statusMarker(record, currentID)) + "  " + theme.Style(termstyle.RoleForeground, Target(record))
	if kind := KindBadge(record); kind != "" {
		left += " " + theme.Style(termstyle.RoleInfo, kind)
	}
	left += livenessBadges(record, theme)
	right := theme.Style(termstyle.RoleMuted, fmt.Sprintf("depth %d  id %s", record.Depth, ShortSessionID(record.ID)))
	return truncateVisible(joinVisible(left, right, width), width)
}

// livenessBadges appends compact per-node liveness markers read synchronously
// at paint time — only where the record actually carries the data, so no badge
// is ever fabricated. tmux/screen hold comes from the muxer spec; REC reports
// only that a recording exists (disk-derived, conservative) — never a
// transcript byte. Each field is cleanField'd, never raw.
func livenessBadges(record state.SessionRecord, theme termstyle.Theme) string {
	var parts []string
	if record.Muxer != nil && strings.TrimSpace(record.Muxer.Type) != "" {
		label := cleanField(record.Muxer.Type)
		if record.Muxer.Detached {
			label += "·detached"
		}
		parts = append(parts, theme.Style(termstyle.RoleInfo, label))
	}
	if record.RecordedBy != nil {
		parts = append(parts, theme.Style(termstyle.RoleWarning, "REC"))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func statusMarker(record state.SessionRecord, currentID string) string {
	current := currentID != "" && record.ID == currentID
	switch {
	case current && IsActive(record):
		return "● current"
	case record.RemoteMirror && record.EndedAt == nil:
		return "◆ ssherpa"
	case current:
		return "◌ current"
	case IsActive(record):
		return "● active"
	case record.EndedAt == nil:
		return "◌ orphan"
	case record.ExitCode != nil:
		return "× " + StatusLabel(record)
	default:
		return "○ exited"
	}
}

func statusRole(record state.SessionRecord, currentID string) termstyle.Role {
	current := currentID != "" && record.ID == currentID
	switch {
	case current && IsActive(record):
		return termstyle.RoleSelected
	case record.RemoteMirror && record.EndedAt == nil:
		return termstyle.RoleInfo
	case IsActive(record):
		return termstyle.RoleSuccess
	case record.EndedAt == nil:
		return termstyle.RoleWarning
	case record.ExitCode != nil && *record.ExitCode != 0:
		return termstyle.RoleDanger
	default:
		return termstyle.RoleMuted
	}
}

func ShortSessionID(id string) string {
	id = strings.TrimSpace(cleanField(id))
	if id == "" || len(id) <= 12 {
		return id
	}
	if _, tail, ok := strings.Cut(id, "-"); ok && len(tail) >= 6 && len(tail) <= 12 {
		return tail
	}
	return id[len(id)-8:]
}

func routeSpineLines(prefix string, parts []string, record state.SessionRecord, managedTargets map[string]bool, theme termstyle.Theme, width int) []string {
	if len(parts) == 0 {
		return nil
	}
	lines := []string{}
	current := prefix + styledRoutePart(parts[0], 0, len(parts), managedTargets, theme)
	for i := 1; i < len(parts); i++ {
		connector := routeConnector(parts[i], i, len(parts), managedTargets, theme)
		segment := connector + styledRoutePart(parts[i], i, len(parts), managedTargets, theme)
		if termstyle.VisibleWidth(current+segment) > width && termstyle.VisibleWidth(current) > termstyle.VisibleWidth(prefix) {
			lines = append(lines, truncateVisible(current, width))
			current = prefix + theme.Style(termstyle.RoleBorder, "└▶ ") + styledRoutePart(parts[i], i, len(parts), managedTargets, theme)
			continue
		}
		current += segment
	}
	lines = append(lines, truncateVisible(current, width))
	return lines
}

func routeConnector(part string, index int, total int, managedTargets map[string]bool, theme termstyle.Theme) string {
	role := termstyle.RoleBorder
	connector := " ─▶ "
	if isManagedRoutePart(part, managedTargets) {
		role = termstyle.RoleInfo
		connector = " ═▶ "
	} else if index == total-1 {
		role = termstyle.RoleSuccess
		connector = " ━▶ "
	}
	return theme.Style(role, connector)
}

func styledRoutePart(part string, index int, total int, managedTargets map[string]bool, theme termstyle.Theme) string {
	switch {
	case index == 0 && total > 1:
		return theme.Style(termstyle.RolePrimary, "⌂ "+part) + theme.Style(termstyle.RoleMuted, " local")
	case isManagedRoutePart(part, managedTargets):
		return theme.Style(termstyle.RoleInfo, "◆ "+part) + theme.Style(termstyle.RoleMuted, " ssherpa")
	case index == total-1:
		return theme.Style(termstyle.RoleSuccess, "● "+part) + theme.Style(termstyle.RoleMuted, " target")
	default:
		return theme.Style(termstyle.RoleForeground, "· "+part) + theme.Style(termstyle.RoleMuted, " hop")
	}
}

func isManagedRoutePart(part string, managedTargets map[string]bool) bool {
	return managedTargets[strings.TrimSpace(part)]
}

func inheritedManagedTargets(record state.SessionRecord, inherited map[string]bool) map[string]bool {
	next := copyManagedTargets(inherited)
	if record.Depth > 0 {
		addManagedName(next, Target(record))
	}
	return next
}

func recordManagedTargets(record state.SessionRecord, inherited map[string]bool) map[string]bool {
	next := copyManagedTargets(inherited)
	addManagedName(next, Target(record))
	if len(record.Route) > 0 {
		addManagedName(next, record.Route[len(record.Route)-1])
	}
	return next
}

func copyManagedTargets(values map[string]bool) map[string]bool {
	copied := make(map[string]bool, len(values)+2)
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func addManagedName(values map[string]bool, name string) {
	name = strings.TrimSpace(name)
	if name != "" && name != "-" {
		values[name] = true
	}
}

func sessionDetailLines(prefix string, record state.SessionRecord, theme termstyle.Theme, width int) []string {
	parts := []string{}
	if mode := strings.TrimSpace(cleanField(record.RunnerMode)); mode != "" {
		if record.RemoteMirror {
			mode = "remote " + mode
		}
		parts = append(parts, mode)
	}
	if !record.StartedAt.IsZero() {
		parts = append(parts, "started "+record.StartedAt.Local().Format("15:04:05"))
	}
	if forward := ForwardSummary(record); forward != "" {
		parts = append(parts, "forward "+forward)
	}
	if proxy := ProxySummary(record); proxy != "" {
		parts = append(parts, "proxy "+proxy)
	}
	if remote := RemoteSummary(record); remote != "" {
		parts = append(parts, "remote "+remote)
	}
	if health := HealthSummary(record); health != "" {
		parts = append(parts, "health "+health)
	}
	if len(parts) == 0 {
		return nil
	}
	return dotJoinLines(prefix, parts, theme, width)
}

func dotJoinLines(prefix string, parts []string, theme termstyle.Theme, width int) []string {
	sep := theme.Style(termstyle.RoleMuted, " · ")
	lines := []string{}
	current := prefix + theme.Style(termstyle.RoleMuted, parts[0])
	for _, part := range parts[1:] {
		next := sep + theme.Style(termstyle.RoleMuted, part)
		if termstyle.VisibleWidth(current+next) > width && termstyle.VisibleWidth(current) > termstyle.VisibleWidth(prefix) {
			lines = append(lines, truncateVisible(current, width))
			current = prefix + theme.Style(termstyle.RoleMuted, part)
			continue
		}
		current += next
	}
	lines = append(lines, truncateVisible(current, width))
	return lines
}

func routeDiagramLines(prefix string, route []string, theme termstyle.Theme, width int) []string {
	parts := displayRouteParts(route)
	return routeDiagramPartLines(prefix, parts, theme, width)
}

func routeDiagramPartLines(prefix string, parts []string, theme termstyle.Theme, width int) []string {
	if len(parts) == 0 {
		return nil
	}
	lines := make([]string, 0, len(parts)+1)
	lines = append(lines, truncateVisible(prefix+theme.Style(termstyle.RoleAccent, "PATH"), width))
	for i, part := range parts {
		connector := "● "
		role := termstyle.RolePrimary
		if i > 0 {
			connector = "└─▶ "
			role = termstyle.RoleForeground
		}
		if i == len(parts)-1 && i > 0 {
			role = termstyle.RoleSuccess
		}
		indent := strings.Repeat("     ", i)
		if i == 0 {
			indent = "  "
		}
		line := prefix + indent + theme.Style(termstyle.RoleBorder, connector) + theme.Style(role, part)
		if i == 0 && len(parts) > 1 {
			line += theme.Style(termstyle.RoleMuted, "  local")
		}
		lines = append(lines, truncateVisible(line, width))
	}
	return lines
}

func displayRecordRouteParts(record state.SessionRecord) []string {
	if strings.TrimSpace(record.OriginHost) != "" {
		return lineageParts(record)
	}
	return displayRouteParts(record.Route)
}

func displayRouteParts(route []string) []string {
	if len(route) == 0 {
		return []string{"here"}
	}
	parts := make([]string, len(route))
	for i, part := range route {
		parts[i] = cleanField(part)
	}
	if parts[0] != "here" {
		parts = append([]string{"here"}, parts...)
	}
	return parts
}

func displayRecordsForMap(records []state.SessionRecord) []state.SessionRecord {
	if len(records) == 0 {
		return records
	}

	localIDs := make(map[string]bool, len(records))
	for _, record := range records {
		if record.ID != "" {
			localIDs[record.ID] = true
		}
	}

	display := append([]state.SessionRecord(nil), records...)
	virtualByPrefix := map[string]string{}
	for i := range display {
		record := display[i]
		if record.ParentID == "" || localIDs[record.ParentID] {
			continue
		}
		if strings.TrimSpace(record.OriginHost) == "" {
			continue
		}
		lineage := lineageParts(record)
		if len(lineage) < 2 {
			continue
		}

		parentID := ""
		for idx := 0; idx < len(lineage)-1; idx++ {
			prefix := lineage[:idx+1]
			key := strings.Join(prefix, "\x00")
			id, ok := virtualByPrefix[key]
			if !ok {
				id = inheritedNodeID(prefix)
				virtualByPrefix[key] = id
				started := record.StartedAt
				display = append(display, state.SessionRecord{
					ID:           id,
					ParentID:     parentID,
					Depth:        idx,
					OriginHost:   firstPart(prefix),
					Route:        append([]string(nil), prefix[1:]...),
					TargetAlias:  prefix[len(prefix)-1],
					StartedAt:    started,
					EndedAt:      &started,
					RunnerMode:   "inherited",
					StateVersion: state.StateVersion,
					Inherited:    true,
				})
			}
			parentID = id
		}
		if parentID != "" {
			record.ParentID = parentID
			display[i] = record
		}
	}
	return display
}

func lineageParts(record state.SessionRecord) []string {
	var parts []string
	appendPart := func(part string) {
		part = strings.TrimSpace(cleanField(part))
		if part == "" {
			return
		}
		if len(parts) > 0 && parts[len(parts)-1] == part {
			return
		}
		parts = append(parts, part)
	}
	appendPart(record.OriginHost)
	for _, part := range record.Route {
		appendPart(part)
	}
	if len(parts) == 0 {
		appendPart(Target(record))
	}
	return parts
}

func inheritedNodeID(prefix []string) string {
	sum := sha1.Sum([]byte(strings.Join(prefix, "\x00")))
	return "inherited-" + hex.EncodeToString(sum[:6])
}

func firstPart(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func ForwardSummary(record state.SessionRecord) string {
	if record.Kind != state.KindTunnel || record.Forward == nil {
		return ""
	}
	summary := "local " + forwardEndpoint(record.Forward.LocalBind, record.Forward.LocalPort) + " -> remote " + forwardEndpoint(record.Forward.RemoteHost, record.Forward.RemotePort)
	var parts []string
	if record.Forward.SavedAlias != "" {
		parts = append(parts, "saved "+cleanField(record.Forward.SavedAlias))
	}
	if record.Forward.Detached {
		parts = append(parts, "background")
	}
	if record.Forward.RetryCount > 0 {
		parts = append(parts, fmt.Sprintf("retries %d", record.Forward.RetryCount))
	}
	if len(parts) > 0 {
		summary += "  (" + strings.Join(parts, ", ") + ")"
	}
	return summary
}

func ProxySummary(record state.SessionRecord) string {
	if record.Kind != state.KindProxy || record.Proxy == nil {
		return ""
	}
	summary := "SOCKS " + forwardEndpoint(record.Proxy.Bind, record.Proxy.Port)
	var parts []string
	if record.Proxy.SavedAlias != "" {
		parts = append(parts, "saved "+cleanField(record.Proxy.SavedAlias))
	}
	if record.Proxy.Detached {
		parts = append(parts, "background")
	}
	if record.Proxy.RetryCount > 0 {
		parts = append(parts, fmt.Sprintf("retries %d", record.Proxy.RetryCount))
	}
	if len(parts) > 0 {
		summary += "  (" + strings.Join(parts, ", ") + ")"
	}
	return summary
}

// MuxerSummary renders the terminal multiplexer a session is held by, if
// any (e.g. "tmux" or "tmux  (detached)"), so the session map explains why
// a session running inside tmux/screen persists across an upstream drop.
func MuxerSummary(record state.SessionRecord) string {
	if record.Muxer == nil || record.Muxer.Type == "" {
		return ""
	}
	summary := cleanField(record.Muxer.Type)
	if record.Muxer.Detached {
		summary += "  (detached)"
	}
	return summary
}

func RemoteSummary(record state.SessionRecord) string {
	var parts []string
	if record.RemoteCWD != "" {
		cwd := cleanField(record.RemoteCWD)
		if record.RemoteHost != "" {
			cwd = cleanField(record.RemoteHost) + ":" + cwd
		}
		parts = append(parts, "cwd "+cwd)
	}
	if prompt := promptSummary(record.RemotePrompt); prompt != "" {
		parts = append(parts, "prompt "+prompt)
	}
	return strings.Join(parts, "  ")
}

func promptSummary(value string) string {
	switch value {
	case state.RemotePromptPrompt:
		return "idle"
	case state.RemotePromptRunning:
		return "running"
	case state.RemotePromptPromptStart:
		return "drawing"
	default:
		return ""
	}
}

func forwardEndpoint(host string, port int) string {
	host = strings.TrimSpace(cleanField(host))
	if host == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		if host == "127.0.0.1" {
			return ":?"
		}
		return host + ":?"
	}
	if host == "127.0.0.1" {
		return fmt.Sprintf(":%d", port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func mapHeader(theme termstyle.Theme, title string, stateDir string, active int, exited int, total int, visible int, includeExited bool) string {
	parts := []string{
		theme.Style(termstyle.RoleTitle, title),
		mapStats(theme, active, exited, total, visible, includeExited),
		theme.Style(termstyle.RoleMuted, "state ") + theme.Style(termstyle.RoleForeground, compactStatePath(stateDir)),
	}
	return strings.Join(parts, theme.Style(termstyle.RoleBorder, " · "))
}

func mapStats(theme termstyle.Theme, active int, exited int, total int, visible int, includeExited bool) string {
	stats := []string{
		theme.Style(termstyle.RoleSuccess, fmt.Sprintf("active %d", active)),
	}
	if includeExited {
		stats = append(stats, theme.Style(termstyle.RoleMuted, fmt.Sprintf("exited %d", exited)))
	}
	stats = append(stats, theme.Style(termstyle.RoleMuted, fmt.Sprintf("shown %d", visible)))
	stats = append(stats, theme.Style(termstyle.RoleMuted, fmt.Sprintf("recorded %d", total)))
	return strings.Join(stats, "  ")
}

func compactStatePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(home)
		clean := filepath.Clean(path)
		if clean == home {
			return "~"
		}
		if rel, err := filepath.Rel(home, clean); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(filepath.Join("~", rel))
		}
	}
	return filepath.ToSlash(path)
}

// The box builders now render through the shared internal/chrome geometry, so
// the map overlay and the picker can never disagree on border drawing. The
// raw-transcript truncation policy is preserved by passing truncateVisible (it
// Strips, not Sanitizes — see S2) as the Truncator.
func boxTop(theme termstyle.Theme, label string, width int) string {
	return chrome.Top(theme, label, width, truncateVisible)
}

func boxDivider(theme termstyle.Theme, width int) string {
	return chrome.Divider(theme, width)
}

func boxBottom(theme termstyle.Theme, width int) string {
	return chrome.Bottom(theme, width)
}

func boxLine(theme termstyle.Theme, line string, width int) string {
	return chrome.Line(theme, line, width, truncateVisible)
}

func joinVisible(left string, right string, width int) string {
	if strings.TrimSpace(right) == "" {
		return left
	}
	gap := width - termstyle.VisibleWidth(left) - termstyle.VisibleWidth(right)
	if gap < 2 {
		return left + "  " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func truncateVisible(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if termstyle.VisibleWidth(value) <= width {
		return value
	}
	return termstyle.Truncate(termstyle.Strip(value), width)
}

func clamp(value int, low int, high int) int {
	return min(max(value, low), high)
}

func HealthSummary(record state.SessionRecord) string {
	if record.DisconnectReason != "" {
		return "disconnected: " + cleanField(record.DisconnectReason)
	}
	if len(record.Events) == 0 {
		return ""
	}
	last := record.Events[len(record.Events)-1]
	switch last.Type {
	case "latency_warning":
		return cleanField(last.Message)
	case "latency_disconnect":
		return "disconnected: " + cleanField(last.Message)
	}
	return ""
}
