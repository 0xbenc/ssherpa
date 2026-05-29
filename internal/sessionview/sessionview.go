package sessionview

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
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

func MapLines(stateDir string, records []state.SessionRecord, currentID string) []string {
	return MapLinesWithOptions(stateDir, records, MapOptions{CurrentID: currentID})
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
	lines = append(lines, "", "ROUTE")

	if len(visible) == 0 {
		if opts.IncludeExited {
			return append(lines, "No supervised sessions recorded.")
		}
		return append(lines, "No active supervised sessions.")
	}

	roots := state.BuildSessionForest(visible)
	for i, root := range roots {
		lines = appendNodeLines(lines, root, "", i == len(roots)-1, opts.CurrentID)
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
		theme.Style(termstyle.RoleBorder, "+"+strings.Repeat("-", innerWidth+2)+"+"),
		"| " + termstyle.PadRight(theme.Style(termstyle.RoleTitle, title), innerWidth) + " |",
		"| " + termstyle.PadRight(mapStats(theme, active, exited, len(opts.Records), len(visible), opts.Map.IncludeExited), innerWidth) + " |",
		"| " + termstyle.PadRight(theme.Style(termstyle.RoleMuted, "state ")+theme.Style(termstyle.RoleForeground, truncateVisible(opts.StateDir, innerWidth-6)), innerWidth) + " |",
		theme.Style(termstyle.RoleBorder, "+"+strings.Repeat("-", innerWidth+2)+"+"),
	}

	bodyBudget := max(1, height-len(lines)-2)
	if opts.Help != "" {
		bodyBudget--
	}
	body := mapBodyLines(visible, opts.Map.CurrentID, theme, innerWidth)
	if len(body) > bodyBudget {
		hidden := len(body) - bodyBudget + 1
		body = append(body[:max(0, bodyBudget-1)], theme.Style(termstyle.RoleMuted, fmt.Sprintf("... %d more line(s)", hidden)))
	}
	for _, line := range body {
		lines = append(lines, "| "+termstyle.PadRight(truncateVisible(line, innerWidth), innerWidth)+" |")
	}
	if opts.Help != "" {
		lines = append(lines, theme.Style(termstyle.RoleBorder, "+"+strings.Repeat("-", innerWidth+2)+"+"))
		lines = append(lines, "| "+termstyle.PadRight(theme.Style(termstyle.RoleMuted, truncateVisible(opts.Help, innerWidth)), innerWidth)+" |")
	}
	lines = append(lines, theme.Style(termstyle.RoleBorder, "+"+strings.Repeat("-", innerWidth+2)+"+"))

	return tea.NewView(strings.Join(lines, "\n"))
}

func ShowMap(ctx context.Context, opts ShowOptions) error {
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
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}

type mapModel struct {
	noAltScreen bool
	view        ViewOptions
	width       int
	height      int
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
		return m, tea.Quit
	}
	return m, nil
}

func (m mapModel) View() tea.View {
	opts := m.view
	opts.Width = m.width
	opts.Height = m.height
	if strings.TrimSpace(opts.Help) == "" {
		opts.Help = "press any key to return"
	}
	view := MapView(opts)
	view.AltScreen = !m.noAltScreen
	return view
}

func CountStatuses(records []state.SessionRecord) (active int, exited int) {
	for _, record := range records {
		if record.Status() == "active" {
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
		if record.Status() == "active" {
			active = append(active, record)
		}
	}
	return active
}

func StatusLabel(record state.SessionRecord) string {
	if record.Status() == "active" {
		return "active"
	}
	if record.ExitCode != nil {
		return fmt.Sprintf("exit %d", *record.ExitCode)
	}
	return "exited"
}

// KindBadge returns a short bracketed tag distinguishing operator-facing
// session types so they stand out in list / map output.
func KindBadge(record state.SessionRecord) string {
	switch record.Kind {
	case state.KindTunnel:
		return "[forward]"
	default:
		if len(record.Hops) > 0 || len(record.Route) > 1 {
			return "[jump]"
		}
		return ""
	}
}

func Target(record state.SessionRecord) string {
	if strings.TrimSpace(record.TargetAlias) != "" {
		return record.TargetAlias
	}
	if len(record.Route) > 0 {
		return record.Route[len(record.Route)-1]
	}
	return "-"
}

func FormatRoute(route []string) string {
	if len(route) == 0 {
		return "-"
	}
	return strings.Join(route, " -> ")
}

func FormatDisplayRoute(route []string) string {
	return FormatRoute(displayRouteParts(route))
}

func writeListGroup(w io.Writer, title string, records []state.SessionRecord, active bool) {
	first := true
	for _, record := range records {
		if (record.Status() == "active") != active {
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
			record.ID,
			StatusLabel(record),
			record.Depth,
			target,
			FormatDisplayRoute(record.Route),
			record.StartedAt.Local().Format(time.RFC3339),
		)
		if health := HealthSummary(record); health != "" {
			fmt.Fprintf(w, "\thealth=%s\n", health)
		}
	}
}

func appendNodeLines(lines []string, node state.SessionNode, prefix string, last bool, currentID string) []string {
	connector := "+- "
	nextPrefix := prefix + "|  "
	if last {
		nextPrefix = prefix + "   "
	}
	record := node.Record
	current := ""
	if currentID != "" && record.ID == currentID {
		current = "  current"
	}
	kind := KindBadge(record)
	if kind != "" {
		kind = " " + kind
	}
	lines = append(lines, fmt.Sprintf("%s%s%s%s [%s] depth=%d id=%s%s",
		prefix,
		connector,
		Target(record),
		kind,
		StatusLabel(record),
		record.Depth,
		record.ID,
		current,
	))
	if len(record.Route) > 0 {
		lines = append(lines, fmt.Sprintf("%s   path: %s", prefix, FormatDisplayRoute(record.Route)))
	}
	if forward := ForwardSummary(record); forward != "" {
		lines = append(lines, fmt.Sprintf("%s   forward: %s", prefix, forward))
	}
	if health := HealthSummary(record); health != "" {
		lines = append(lines, fmt.Sprintf("%s   health: %s", prefix, health))
	}
	for i, child := range node.Children {
		lines = appendNodeLines(lines, child, nextPrefix, i == len(node.Children)-1, currentID)
	}
	return lines
}

func mapBodyLines(records []state.SessionRecord, currentID string, theme termstyle.Theme, width int) []string {
	if len(records) == 0 {
		return []string{theme.Style(termstyle.RoleMuted, "No active supervised sessions.")}
	}
	roots := state.BuildSessionForest(records)
	lines := []string{theme.Style(termstyle.RoleAccent, "ROUTE")}
	for i, root := range roots {
		lines = appendStyledNodeLines(lines, root, "", i == len(roots)-1, currentID, theme, width)
	}
	return lines
}

func appendStyledNodeLines(lines []string, node state.SessionNode, prefix string, last bool, currentID string, theme termstyle.Theme, width int) []string {
	connector := "+- "
	nextPrefix := prefix + "|  "
	if last {
		nextPrefix = prefix + "   "
	}
	record := node.Record
	target := theme.Style(termstyle.RoleForeground, Target(record))
	statusRole := termstyle.RoleMuted
	if record.Status() == "active" {
		statusRole = termstyle.RoleSuccess
	}
	status := theme.Style(statusRole, "["+StatusLabel(record)+"]")
	meta := fmt.Sprintf("depth %d  id %s", record.Depth, record.ID)
	if currentID != "" && record.ID == currentID {
		meta += "  current"
	}
	kind := KindBadge(record)
	if kind != "" {
		kind = " " + theme.Style(termstyle.RoleInfo, kind)
	}
	line := fmt.Sprintf("%s%s%s%s %s  %s", prefix, connector, target, kind, status, theme.Style(termstyle.RoleMuted, meta))
	lines = append(lines, truncateVisible(line, width))
	if len(record.Route) > 0 {
		lines = append(lines, routeDiagramLines(prefix+"   ", record.Route, theme, width)...)
	}
	if forward := ForwardSummary(record); forward != "" {
		lines = append(lines, truncateVisible(prefix+"   "+theme.Style(termstyle.RoleAccent, "FORWARD ")+theme.Style(termstyle.RoleForeground, forward), width))
	}
	if health := HealthSummary(record); health != "" {
		lines = append(lines, truncateVisible(prefix+"   "+theme.Style(termstyle.RoleWarning, "health ")+theme.Style(termstyle.RoleForeground, health), width))
	}
	for i, child := range node.Children {
		lines = appendStyledNodeLines(lines, child, nextPrefix, i == len(node.Children)-1, currentID, theme, width)
	}
	return lines
}

func routeDiagramLines(prefix string, route []string, theme termstyle.Theme, width int) []string {
	parts := displayRouteParts(route)
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

func displayRouteParts(route []string) []string {
	if len(route) == 0 {
		return []string{"here"}
	}
	parts := append([]string(nil), route...)
	if parts[0] != "here" {
		parts = append([]string{"here"}, parts...)
	}
	return parts
}

func ForwardSummary(record state.SessionRecord) string {
	if record.Kind != state.KindTunnel || record.Forward == nil {
		return ""
	}
	summary := "local " + forwardEndpoint(record.Forward.LocalBind, record.Forward.LocalPort) + " -> remote " + forwardEndpoint(record.Forward.RemoteHost, record.Forward.RemotePort)
	var parts []string
	if record.Forward.SavedAlias != "" {
		parts = append(parts, "saved "+record.Forward.SavedAlias)
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

func forwardEndpoint(host string, port int) string {
	host = strings.TrimSpace(host)
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
		return "disconnected: " + record.DisconnectReason
	}
	if len(record.Events) == 0 {
		return ""
	}
	last := record.Events[len(record.Events)-1]
	switch last.Type {
	case "latency_warning":
		return last.Message
	case "latency_disconnect":
		return "disconnected: " + last.Message
	}
	return ""
}
