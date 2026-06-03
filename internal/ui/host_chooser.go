package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type HostChooserOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Mode        string
	Steps       []string
	CurrentStep int
	Footer      string
}

type JumpHopChooserOptions struct {
	Input        io.Reader
	Output       io.Writer
	NoAltScreen  bool
	NoColor      bool
	Theme        termstyle.Theme
	ThemeName    string
	ThemeFile    string
	Destination  string
	Hops         []string
	RouteSummary string
}

type JumpHopChoice struct {
	Done  bool
	Alias string
}

func ChooseHost(ctx context.Context, aliases []hostlist.Alias, opts HostChooserOptions) (string, bool, error) {
	if len(aliases) == 0 {
		return "", false, nil
	}
	model, err := newHostChooserModel(hostChooserItemsFromAliases(aliases), hostChooserBaseOptions{
		Input:       opts.Input,
		Output:      opts.Output,
		NoAltScreen: opts.NoAltScreen,
		NoColor:     opts.NoColor,
		Theme:       opts.Theme,
		ThemeName:   opts.ThemeName,
		ThemeFile:   opts.ThemeFile,
		Title:       opts.Title,
		Mode:        opts.Mode,
		Steps:       opts.Steps,
		CurrentStep: opts.CurrentStep,
		Footer:      opts.Footer,
	})
	if err != nil {
		return "", false, err
	}
	final, err := runHostChooserModel(ctx, model, opts.Input, opts.Output)
	if err != nil {
		return "", false, err
	}
	if final.canceled || final.selected < 0 {
		return "", false, nil
	}
	return final.items[final.selected].Token, true, nil
}

func ChooseJumpHop(ctx context.Context, aliases []hostlist.Alias, opts JumpHopChooserOptions) (JumpHopChoice, bool, error) {
	items := []hostChooserItem{{
		Kind:        hostChooserItemDone,
		Token:       "",
		Title:       "Finish route",
		Description: opts.RouteSummary,
		Badge:       "done",
		Group:       "Action",
	}}
	items = append(items, hostChooserItemsFromAliases(aliases)...)

	model, err := newHostChooserModel(items, hostChooserBaseOptions{
		Input:       opts.Input,
		Output:      opts.Output,
		NoAltScreen: opts.NoAltScreen,
		NoColor:     opts.NoColor,
		Theme:       opts.Theme,
		ThemeName:   opts.ThemeName,
		ThemeFile:   opts.ThemeFile,
		Title:       "SSHERPA JUMP ROUTE",
		Mode:        jumpHopMode(opts.Destination, opts.Hops),
		Steps:       []string{"destination", "first hop", "extra hops", "run"},
		CurrentStep: 2,
		Footer:      "enter select  /  type filter  /  arrows move  /  Q back",
	})
	if err != nil {
		return JumpHopChoice{}, false, err
	}
	final, err := runHostChooserModel(ctx, model, opts.Input, opts.Output)
	if err != nil {
		return JumpHopChoice{}, false, err
	}
	if final.canceled || final.selected < 0 {
		return JumpHopChoice{}, false, nil
	}
	item := final.items[final.selected]
	if item.Kind == hostChooserItemDone {
		return JumpHopChoice{Done: true}, true, nil
	}
	return JumpHopChoice{Alias: item.Token}, true, nil
}

type hostChooserBaseOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Mode        string
	Steps       []string
	CurrentStep int
	Footer      string
}

const (
	hostChooserItemHost = "host"
	hostChooserItemDone = "done"
)

type hostChooserItem struct {
	Kind        string
	Token       string
	Title       string
	Description string
	Detail      string
	Badge       string
	Group       string
}

type hostChooserModel struct {
	items        []hostChooserItem
	filtered     []int
	cursor       int
	scrollOffset int
	query        string
	selected     int
	canceled     bool
	noAltScreen  bool
	theme        termstyle.Theme
	title        string
	mode         string
	steps        []string
	currentStep  int
	footer       string
	width        int
	height       int
}

func newHostChooserModel(items []hostChooserItem, opts hostChooserBaseOptions) (hostChooserModel, error) {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return hostChooserModel{}, err
	}
	model := hostChooserModel{
		items:       append([]hostChooserItem(nil), items...),
		selected:    -1,
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		title:       hostChooserTitle(opts.Title),
		mode:        strings.TrimSpace(opts.Mode),
		steps:       append([]string(nil), opts.Steps...),
		currentStep: opts.CurrentStep,
		footer:      opts.Footer,
		width:       96,
		height:      24,
	}
	if model.mode == "" {
		model.mode = "pick host"
	}
	model.applyFilter()
	return model, nil
}

func runHostChooserModel(ctx context.Context, model hostChooserModel, input io.Reader, output io.Writer) (hostChooserModel, error) {
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if input != nil {
		programOptions = append(programOptions, tea.WithInput(input))
	}
	if output != nil {
		programOptions = append(programOptions, tea.WithOutput(output))
	}
	finalModel, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return hostChooserModel{}, err
	}
	final, ok := finalModel.(hostChooserModel)
	if !ok {
		return hostChooserModel{}, nil
	}
	return final, nil
}

func (m hostChooserModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m hostChooserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.ensureCursorVisible()
	case tea.KeyPressMsg:
		key := msg.String()
		keystroke := msg.Key().Keystroke()
		switch {
		case key == "ctrl+c" || key == "esc" || key == "Q":
			m.canceled = true
			return m, tea.Quit
		case key == "enter":
			if len(m.filtered) == 0 {
				return m, nil
			}
			m.selected = m.filtered[m.cursor]
			return m, tea.Quit
		case key == "backspace":
			if m.query != "" {
				m.query = m.query[:len(m.query)-1]
				m.applyFilter()
			}
		case key == "home":
			m.cursor = 0
			m.ensureCursorVisible()
		case key == "end":
			if len(m.filtered) > 0 {
				m.cursor = len(m.filtered) - 1
			}
			m.ensureCursorVisible()
		case key == "pgup":
			m.moveCursor(-max(4, m.listWindowBudget()/2))
		case key == "pgdown":
			m.moveCursor(max(4, m.listWindowBudget()/2))
		case keystroke == "shift+up" || keystroke == "shift+left":
			m.jumpSection(-1)
		case keystroke == "shift+down" || keystroke == "shift+right":
			m.jumpSection(1)
		case key == "up" || key == "ctrl+p":
			m.moveCursor(-1)
		case key == "down" || key == "ctrl+n":
			m.moveCursor(1)
		case msg.Text != "" && !isControlKey(key):
			m.query += msg.Text
			m.applyFilter()
		}
	}
	return m, nil
}

func (m hostChooserModel) View() tea.View {
	width := max(48, m.width)
	theme := pickerTheme{theme: m.theme}
	footer := m.footer
	if footer == "" {
		footer = "enter select  /  type filter  /  arrows move  /  shift+arrows section  /  Q back"
	}

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:   m.title,
		Steps:   m.steps,
		Current: m.currentStep,
		Body:    m.renderBody(max(20, width-4), theme),
		Footer:  footer,
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m hostChooserModel) renderBody(width int, theme pickerTheme) []string {
	lines := []string{
		hostChooserMetaLine(theme, width, m.mode, m.countSummary()),
		hostChooserFilterLine(theme, width, m.query, len(m.filtered), len(m.items)),
		"",
	}

	listWidth := width
	previewWidth := 0
	if width >= 92 {
		listWidth = clamp(width*60/100, 54, 78)
		previewWidth = width - listWidth - 3
	}
	list := m.renderListLines(listWidth, theme)
	if previewWidth <= 0 {
		lines = append(lines, list...)
		if item, ok := m.currentItem(); ok {
			lines = append(lines, "", hostChooserCompactLine(item, width, theme))
		}
		return lines
	}

	preview := clipTransferBrowserLines(m.renderPreviewLines(previewWidth, theme), m.listWindowBudget(), previewWidth, theme)
	rows := max(len(list), len(preview))
	divider := theme.muted("│")
	for i := 0; i < rows; i++ {
		left := ""
		if i < len(list) {
			left = list[i]
		}
		right := ""
		if i < len(preview) {
			right = preview[i]
		}
		lines = append(lines, termstyle.PadRight(left, listWidth)+" "+divider+" "+right)
	}
	return lines
}

func (m hostChooserModel) renderListLines(width int, theme pickerTheme) []string {
	if len(m.filtered) == 0 {
		return []string{"  " + theme.empty("No matching hosts")}
	}

	budget := m.listWindowBudget()
	lines := make([]string, 0, budget)
	start := m.normalizedScrollOffset()
	if start > 0 {
		lines = append(lines, "  "+theme.muted(fmt.Sprintf("%d more above", start)))
	}

	lastGroup := ""
	rendered := 0
	renderedUntil := start
	for i := start; i < len(m.filtered); i++ {
		index := m.filtered[i]
		if index < 0 || index >= len(m.items) {
			continue
		}
		item := m.items[index]
		newGroup := item.Group != "" && item.Group != lastGroup
		groupCost := 0
		if newGroup {
			groupCost = 1
			if rendered > 0 {
				groupCost++
			}
		}
		reserve := 0
		if len(m.filtered)-i-1 > 0 {
			reserve = 1
		}
		if len(lines)+groupCost+1+reserve > budget {
			break
		}
		if newGroup {
			if rendered > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, theme.groupHeader(item.Group, width))
			lastGroup = item.Group
		}
		lines = append(lines, hostChooserRow(item, i == m.cursor, width, theme))
		rendered++
		renderedUntil = i + 1
	}
	if renderedUntil < len(m.filtered) {
		lines = append(lines, "  "+theme.muted(fmt.Sprintf("%d more below", len(m.filtered)-renderedUntil)))
	}
	return lines
}

func (m hostChooserModel) renderPreviewLines(width int, theme pickerTheme) []string {
	item, ok := m.currentItem()
	if !ok {
		return nil
	}
	lines := []string{
		theme.groupHeader("Selection", width),
		theme.previewTitle(termstyle.Truncate(item.Title, width)),
	}
	lines = append(lines, previewKVLines(theme, width, "Type", strings.ToUpper(item.Badge), 2)...)
	if item.Description != "" {
		lines = append(lines, previewKVLines(theme, width, "Target", item.Description, 3)...)
	}
	if item.Detail != "" {
		lines = append(lines, previewKVLines(theme, width, "Details", item.Detail, 2)...)
	}
	lines = append(lines, previewKVLines(theme, width, "Action", hostChooserAction(item), 2)...)
	return lines
}

func hostChooserItemsFromAliases(aliases []hostlist.Alias) []hostChooserItem {
	items := make([]hostChooserItem, 0, len(aliases))
	for _, alias := range aliases {
		items = append(items, hostChooserItem{
			Kind:        hostChooserItemHost,
			Token:       alias.Name,
			Title:       alias.Name,
			Description: displayAlias(alias),
			Detail:      aliasDetail(alias),
			Badge:       aliasBadge(alias),
			Group:       "Hosts",
		})
	}
	return items
}

func hostChooserTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "SSHERPA HOST CHOOSER"
	}
	upper := strings.ToUpper(title)
	if strings.HasPrefix(upper, "SSHERPA") {
		return upper
	}
	upper = strings.ReplaceAll(upper, ":", "")
	upper = strings.ReplaceAll(upper, "PICK ", "")
	return "SSHERPA " + strings.Join(strings.Fields(upper), " ")
}

func hostChooserMetaLine(theme pickerTheme, width int, mode string, summary string) string {
	label := theme.label(termstyle.PadRight("MODE", 7))
	value := strings.TrimSpace(mode)
	if value == "" {
		value = "pick host"
	}
	text := theme.summary(termstyle.Truncate(value+"  "+summary, max(0, width-termstyle.VisibleWidth(label)-2)))
	return label + "  " + termstyle.PadRight(text, max(0, width-termstyle.VisibleWidth(label)-2))
}

func hostChooserFilterLine(theme pickerTheme, width int, query string, shown int, total int) string {
	label := theme.label(termstyle.PadRight("FILTER", 7))
	counter := theme.counter(fmt.Sprintf("%d/%d", shown, total))
	text := query
	if text == "" {
		text = "type to filter"
	}
	fieldWidth := max(8, width-termstyle.VisibleWidth(label)-termstyle.VisibleWidth(counter)-6)
	field := "[" + termstyle.PadRight(termstyle.Truncate(text, fieldWidth), fieldWidth) + "]"
	if query == "" {
		field = theme.muted(field)
	} else {
		field = theme.search(field)
	}
	return label + "  " + field + "  " + counter
}

func hostChooserRow(item hostChooserItem, selected bool, width int, theme pickerTheme) string {
	cursor := "  "
	if selected {
		cursor = ">>"
	}
	badge := strings.ToUpper(strings.TrimSpace(item.Badge))
	if badge == "" {
		badge = strings.ToUpper(item.Kind)
	}
	badge = termstyle.Truncate(badge, 6)
	titleWidth := clamp(width/3, 14, 28)
	if width < 68 {
		titleWidth = clamp(width/4, 10, 18)
	}
	descWidth := max(0, width-2-2-8-2-titleWidth-2)
	line := theme.cursor(cursor, selected) + " " +
		termstyle.PadRight(theme.badge(hostChooserItemKind(item), "["+badge+"]"), 8) + " " +
		termstyle.PadRight(theme.rowTitle(termstyle.Truncate(item.Title, titleWidth), selected), titleWidth)
	if descWidth > 0 {
		line += "  " + theme.rowDesc(termstyle.Truncate(item.Description, descWidth), selected)
	}
	return termstyle.PadRight(line, width)
}

func hostChooserCompactLine(item hostChooserItem, width int, theme pickerTheme) string {
	text := hostChooserAction(item)
	if item.Description != "" {
		text += "  " + item.Description
	}
	return theme.subtle(termstyle.Truncate(text, width))
}

func hostChooserAction(item hostChooserItem) string {
	if item.Kind == hostChooserItemDone {
		return "Finish this jump route"
	}
	return "Select this host"
}

func hostChooserItemKind(item hostChooserItem) ItemKind {
	if item.Kind == hostChooserItemDone {
		return ItemCheck
	}
	return ItemAlias
}

func (m *hostChooserModel) applyFilter() {
	m.filtered = m.filtered[:0]
	for i, item := range m.items {
		value := strings.Join([]string{item.Title, item.Description, item.Detail, item.Token, item.Group, item.Badge}, "\t")
		if m.query == "" || fuzzyMatch(value, m.query) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
	m.ensureCursorVisible()
}

func (m *hostChooserModel) moveCursor(delta int) {
	if len(m.filtered) == 0 || delta == 0 {
		return
	}
	m.cursor = clamp(m.cursor+delta, 0, len(m.filtered)-1)
	m.ensureCursorVisible()
}

func (m *hostChooserModel) jumpSection(delta int) {
	if len(m.filtered) == 0 || delta == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return
	}
	currentGroup := m.filteredGroup(m.cursor)
	if delta > 0 {
		currentEnd := m.cursor
		for i := m.cursor + 1; i < len(m.filtered); i++ {
			if m.filteredGroup(i) != currentGroup {
				m.cursor = i
				m.ensureCursorVisible()
				return
			}
			currentEnd = i
		}
		if currentEnd > m.cursor {
			m.cursor = currentEnd
			m.ensureCursorVisible()
		}
		return
	}
	currentStart := m.cursor
	for currentStart > 0 && m.filteredGroup(currentStart-1) == currentGroup {
		currentStart--
	}
	if currentStart < m.cursor {
		m.cursor = currentStart
		m.ensureCursorVisible()
		return
	}
	for i := currentStart - 1; i >= 0; i-- {
		group := m.filteredGroup(i)
		for i > 0 && m.filteredGroup(i-1) == group {
			i--
		}
		m.cursor = i
		m.ensureCursorVisible()
		return
	}
}

func (m hostChooserModel) filteredGroup(pos int) string {
	if pos < 0 || pos >= len(m.filtered) {
		return ""
	}
	index := m.filtered[pos]
	if index < 0 || index >= len(m.items) {
		return ""
	}
	return m.items[index].Group
}

func (m *hostChooserModel) ensureCursorVisible() {
	if len(m.filtered) == 0 {
		m.cursor = 0
		m.scrollOffset = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
	if m.scrollOffset >= len(m.filtered) {
		m.scrollOffset = len(m.filtered) - 1
	}
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	budget := m.listWindowBudget()
	for m.scrollOffset < m.cursor && !hostChooserWindowContainsCursor(m.items, m.filtered, m.scrollOffset, m.cursor, budget) {
		m.scrollOffset++
	}
	if !hostChooserWindowContainsCursor(m.items, m.filtered, m.scrollOffset, m.cursor, budget) {
		m.scrollOffset = m.cursor
	}
}

func (m hostChooserModel) currentItem() (hostChooserItem, bool) {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return hostChooserItem{}, false
	}
	index := m.filtered[m.cursor]
	if index < 0 || index >= len(m.items) {
		return hostChooserItem{}, false
	}
	return m.items[index], true
}

func (m hostChooserModel) countSummary() string {
	hosts := 0
	for _, item := range m.items {
		if item.Kind == hostChooserItemHost {
			hosts++
		}
	}
	if hosts == len(m.items) {
		return fmt.Sprintf("%d host%s", hosts, pluralSuffix(hosts))
	}
	return fmt.Sprintf("%d choice%s  %d host%s", len(m.items), pluralSuffix(len(m.items)), hosts, pluralSuffix(hosts))
}

func (m hostChooserModel) normalizedScrollOffset() int {
	if len(m.filtered) == 0 || m.scrollOffset < 0 {
		return 0
	}
	if m.scrollOffset >= len(m.filtered) {
		return len(m.filtered) - 1
	}
	return m.scrollOffset
}

func (m hostChooserModel) listWindowBudget() int {
	chrome := 4
	if len(m.steps) > 0 {
		chrome += 2
	}
	fixedBody := 5
	safety := 1
	budget := m.height - chrome - fixedBody - safety
	if budget < 4 {
		budget = 4
	}
	return budget
}

func hostChooserWindowContainsCursor(items []hostChooserItem, filtered []int, start int, cursor int, budget int) bool {
	if len(filtered) == 0 || cursor < start || cursor < 0 || cursor >= len(filtered) {
		return false
	}
	lines := 0
	if start > 0 {
		lines++
	}
	lastGroup := ""
	rendered := 0
	for i := start; i < len(filtered); i++ {
		index := filtered[i]
		if index < 0 || index >= len(items) {
			continue
		}
		item := items[index]
		groupCost := 0
		newGroup := item.Group != "" && item.Group != lastGroup
		if newGroup {
			groupCost = 1
			if rendered > 0 {
				groupCost++
			}
		}
		reserve := 0
		if len(filtered)-i-1 > 0 {
			reserve = 1
		}
		if lines+groupCost+1+reserve > budget {
			return false
		}
		if i == cursor {
			return true
		}
		lines += groupCost + 1
		if newGroup {
			lastGroup = item.Group
		}
		rendered++
	}
	return false
}

func jumpHopMode(destination string, hops []string) string {
	route := append([]string(nil), hops...)
	route = append(route, destination)
	if len(route) == 0 {
		return "build route"
	}
	return "route " + strings.Join(route, " -> ")
}
