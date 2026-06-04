package sessionview

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
			record.ID,
			StatusLabel(record),
			record.Depth,
			target,
			FormatRecordRoute(record),
			record.StartedAt.Local().Format(time.RFC3339),
		)
		if health := HealthSummary(record); health != "" {
			fmt.Fprintf(w, "\thealth=%s\n", health)
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
	right := theme.Style(termstyle.RoleMuted, fmt.Sprintf("depth %d  id %s", record.Depth, ShortSessionID(record.ID)))
	return truncateVisible(joinVisible(left, right, width), width)
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
	id = strings.TrimSpace(id)
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
	if mode := strings.TrimSpace(record.RunnerMode); mode != "" {
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
	parts := append([]string(nil), route...)
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
		part = strings.TrimSpace(part)
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

func ProxySummary(record state.SessionRecord) string {
	if record.Kind != state.KindProxy || record.Proxy == nil {
		return ""
	}
	summary := "SOCKS " + forwardEndpoint(record.Proxy.Bind, record.Proxy.Port)
	var parts []string
	if record.Proxy.SavedAlias != "" {
		parts = append(parts, "saved "+record.Proxy.SavedAlias)
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

func RemoteSummary(record state.SessionRecord) string {
	var parts []string
	if record.RemoteCWD != "" {
		cwd := record.RemoteCWD
		if record.RemoteHost != "" {
			cwd = record.RemoteHost + ":" + cwd
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

func boxTop(theme termstyle.Theme, label string, width int) string {
	return boxEdge(theme, "╭", "╮", label, width)
}

func boxDivider(theme termstyle.Theme, width int) string {
	return boxEdge(theme, "├", "┤", "", width)
}

func boxBottom(theme termstyle.Theme, width int) string {
	return boxEdge(theme, "╰", "╯", "", width)
}

func boxEdge(theme termstyle.Theme, left string, right string, label string, width int) string {
	inner := max(0, width-2)
	if inner == 0 {
		return theme.Style(termstyle.RoleBorder, left+right)
	}
	fill := strings.Repeat("─", inner)
	label = strings.TrimSpace(label)
	if label != "" {
		label = " " + truncateVisible(label, max(0, inner-2)) + " "
		if termstyle.VisibleWidth(label) < inner {
			fill = label + strings.Repeat("─", inner-termstyle.VisibleWidth(label))
		} else {
			fill = truncateVisible(label, inner)
		}
	}
	return theme.Style(termstyle.RoleBorder, left) + fill + theme.Style(termstyle.RoleBorder, right)
}

func boxLine(theme termstyle.Theme, line string, width int) string {
	inner := max(0, width-4)
	content := termstyle.PadRight(truncateVisible(line, inner), inner)
	return theme.Style(termstyle.RoleBorder, "│ ") + content + theme.Style(termstyle.RoleBorder, " │")
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
