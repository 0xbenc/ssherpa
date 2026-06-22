package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/chrome"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type TransferBrowserOptions struct {
	Input         io.Reader
	Output        io.Writer
	NoAltScreen   bool
	NoColor       bool
	Theme         termstyle.Theme
	ThemeName     string
	ThemeFile     string
	Title         string
	Mode          string
	LocationLabel string
	Location      string
	Steps         []string
	CurrentStep   int
	Footer        string
}

func BrowseTransfer(ctx context.Context, items []Item, opts TransferBrowserOptions) (Item, bool, error) {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return Item{}, false, err
	}
	model := newTransferBrowserModel(items, opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}

	finalModel, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return Item{}, false, err
	}
	browser, ok := finalModel.(transferBrowserModel)
	if !ok || browser.canceled || browser.selected < 0 {
		return Item{}, false, nil
	}
	return browser.items[browser.selected], true, nil
}

type transferBrowserModel struct {
	items         []Item
	filtered      []int
	cursor        int
	scrollOffset  int
	query         string
	selected      int
	canceled      bool
	noAltScreen   bool
	theme         termstyle.Theme
	title         string
	mode          string
	locationLabel string
	location      string
	steps         []string
	currentStep   int
	footer        string
	width         int
	height        int
}

func newTransferBrowserModel(items []Item, opts TransferBrowserOptions, theme termstyle.Theme) transferBrowserModel {
	model := transferBrowserModel{
		items:         append([]Item(nil), items...),
		selected:      -1,
		noAltScreen:   opts.NoAltScreen,
		theme:         theme.WithNoColor(theme.NoColor || opts.NoColor),
		title:         strings.TrimSpace(opts.Title),
		mode:          strings.TrimSpace(opts.Mode),
		locationLabel: strings.TrimSpace(opts.LocationLabel),
		location:      strings.TrimSpace(opts.Location),
		steps:         append([]string(nil), opts.Steps...),
		currentStep:   opts.CurrentStep,
		footer:        opts.Footer,
		width:         96,
		height:        24,
	}
	if model.title == "" {
		model.title = "SSHERPA FILE TRANSFER"
	}
	if model.locationLabel == "" {
		model.locationLabel = "PATH"
	}
	model.applyFilter()
	return model
}

func (m transferBrowserModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m transferBrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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

func (m transferBrowserModel) View() tea.View {
	width := max(64, m.width)
	theme := pickerTheme{theme: m.theme}
	body := m.renderBody(width, theme)
	footer := m.footer
	if footer == "" {
		footer = "enter open/select / type filter / arrows move / shift+arrows section / Q cancel"
	}

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:   m.title,
		Steps:   m.steps,
		Current: m.currentStep,
		Body:    body,
		Footer:  footer,
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m transferBrowserModel) renderBody(width int, theme pickerTheme) []string {
	inner := max(20, width-4)
	lines := []string{
		m.metaLine(inner, theme),
		m.filterLine(inner, theme),
		"",
	}

	listWidth := inner
	previewWidth := 0
	if inner >= 92 {
		listWidth = clamp(inner*62/100, 54, 78)
		previewWidth = inner - listWidth - 3
	}
	listLines := m.renderListLines(listWidth, theme)
	if previewWidth <= 0 {
		lines = append(lines, listLines...)
		if item, ok := m.currentItem(); ok {
			lines = append(lines, "")
			lines = append(lines, m.compactSelectionLine(item, inner, theme))
		}
		return lines
	}

	previewLines := m.renderPreviewLines(previewWidth, theme)
	previewLines = clipTransferBrowserLines(previewLines, m.listWindowBudget(), previewWidth, theme)
	rowCount := max(len(listLines), len(previewLines))
	divider := theme.muted("│")
	for i := 0; i < rowCount; i++ {
		left := ""
		if i < len(listLines) {
			left = listLines[i]
		}
		right := ""
		if i < len(previewLines) {
			right = previewLines[i]
		}
		lines = append(lines, termstyle.PadRight(left, listWidth)+" "+divider+" "+right)
	}
	return lines
}

func clipTransferBrowserLines(lines []string, budget int, width int, theme pickerTheme) []string {
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	if budget == 1 {
		return []string{theme.muted(termstyle.Truncate("more hidden", width))}
	}
	clipped := append([]string(nil), lines[:budget-1]...)
	clipped = append(clipped, theme.muted(termstyle.Truncate(fmt.Sprintf("%d more hidden", len(lines)-len(clipped)), width)))
	return clipped
}

func (m transferBrowserModel) metaLine(width int, theme pickerTheme) string {
	mode := transferBrowserModeLabel(m.mode)
	count := fmt.Sprintf("%d item%s", len(m.items), pluralSuffix(len(m.items)))
	location := m.location
	if location == "" {
		location = "."
	}
	labelWidth := clamp(width/5, 7, 18)
	prefix := theme.label(termstyle.PadRight(termstyle.Truncate(strings.ToUpper(m.locationLabel), labelWidth), labelWidth))
	meta := theme.summary(termstyle.Truncate(mode+"  "+count, max(0, width-termstyle.VisibleWidth(prefix)-4)))
	pathWidth := max(0, width-termstyle.VisibleWidth(prefix)-termstyle.VisibleWidth(meta)-4)
	path := theme.rowDesc(termstyle.Truncate(location, pathWidth), false)
	return prefix + "  " + path + "  " + meta
}

func (m transferBrowserModel) filterLine(width int, theme pickerTheme) string {
	label := theme.label(termstyle.PadRight("FILTER", 7))
	counter := theme.counter(fmt.Sprintf("%d/%d", len(m.filtered), len(m.items)))
	query := m.query
	if query == "" {
		query = "type to filter"
	}
	fieldWidth := max(8, width-termstyle.VisibleWidth(label)-termstyle.VisibleWidth(counter)-6)
	field := "[" + termstyle.PadRight(termstyle.Truncate(query, fieldWidth), fieldWidth) + "]"
	if m.query == "" {
		field = theme.muted(field)
	} else {
		field = theme.search(field)
	}
	return label + "  " + field + "  " + counter
}

func (m transferBrowserModel) renderListLines(width int, theme pickerTheme) []string {
	if len(m.filtered) == 0 {
		return []string{"  " + theme.empty("No matching files")}
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
		lines = append(lines, transferBrowserRow(item, i == m.cursor, width, theme))
		rendered++
		renderedUntil = i + 1
	}
	if renderedUntil < len(m.filtered) {
		lines = append(lines, "  "+theme.muted(fmt.Sprintf("%d more below", len(m.filtered)-renderedUntil)))
	}
	return lines
}

func transferBrowserRow(item Item, selected bool, width int, theme pickerTheme) string {
	cursor := "  "
	if selected {
		cursor = ">>"
	}
	badge := strings.ToUpper(strings.TrimSpace(item.Badge))
	if badge == "" {
		badge = strings.ToUpper(string(item.Kind))
	}
	if len([]rune(badge)) > 6 {
		badge = termstyle.Truncate(badge, 6)
	}

	nameWidth := clamp(width*45/100, 18, 38)
	if width < 58 {
		nameWidth = max(12, width-18)
	}
	metaWidth := max(0, width-2-2-8-nameWidth-2)
	title := termstyle.Truncate(item.Title, nameWidth)
	meta := transferBrowserRowMeta(item)
	line := theme.cursor(cursor, selected) + " " +
		termstyle.PadRight(theme.badge(item.Kind, "["+badge+"]"), 8) + " " +
		termstyle.PadRight(theme.rowTitle(title, selected), nameWidth)
	if metaWidth > 0 {
		line += "  " + theme.rowDesc(termstyle.Truncate(meta, metaWidth), selected)
	}
	return termstyle.PadRight(line, width)
}

func transferBrowserRowMeta(item Item) string {
	if strings.TrimSpace(item.Description) != "" {
		return item.Description
	}
	if strings.TrimSpace(item.Detail) != "" {
		return item.Detail
	}
	if strings.TrimSpace(item.Token) != "" && item.Token != item.Title {
		return item.Token
	}
	return ""
}

func (m transferBrowserModel) renderPreviewLines(width int, theme pickerTheme) []string {
	item, ok := m.currentItem()
	if !ok {
		return nil
	}
	lines := []string{
		theme.groupHeader("Selection", width),
		theme.previewTitle(termstyle.Truncate(item.Title, width)),
	}
	lines = append(lines, previewKVLines(theme, width, "Type", transferBrowserTypeLabel(item), 3)...)
	if path := transferBrowserPath(item); path != "" {
		lines = append(lines, previewKVLines(theme, width, "Path", path, 4)...)
	}
	if item.Description != "" && item.Description != transferBrowserPath(item) {
		lines = append(lines, previewKVLines(theme, width, "Info", item.Description, 2)...)
	}
	lines = append(lines, previewKVLines(theme, width, "Action", transferBrowserActionLabel(item), 3)...)
	return lines
}

func (m transferBrowserModel) compactSelectionLine(item Item, width int, theme pickerTheme) string {
	path := transferBrowserPath(item)
	if path == "" {
		path = item.Title
	}
	text := transferBrowserActionLabel(item) + "  " + path
	return theme.subtle(termstyle.Truncate(text, width))
}

func (m transferBrowserModel) currentItem() (Item, bool) {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return Item{}, false
	}
	index := m.filtered[m.cursor]
	if index < 0 || index >= len(m.items) {
		return Item{}, false
	}
	return m.items[index], true
}

func transferBrowserTypeLabel(item Item) string {
	badge := strings.TrimSpace(item.Badge)
	if badge != "" {
		return strings.ToUpper(badge)
	}
	if item.Group != "" {
		return item.Group
	}
	return string(item.Kind)
}

func transferBrowserPath(item Item) string {
	if strings.TrimSpace(item.Detail) != "" {
		return item.Detail
	}
	if strings.TrimSpace(item.Description) != "" && strings.ContainsAny(item.Description, `/\`) {
		return item.Description
	}
	if strings.TrimSpace(item.Token) != "" {
		return item.Token
	}
	return ""
}

func transferBrowserActionLabel(item Item) string {
	switch strings.ToLower(strings.TrimSpace(item.Badge)) {
	case "use":
		return "Use the current folder"
	case "up":
		return "Move up one folder"
	case "dir":
		return "Open this folder"
	case "file":
		return "Select this file"
	default:
		if strings.EqualFold(item.Title, "Use this folder") {
			return "Use the current folder"
		}
		if item.Title == ".." {
			return "Move up one folder"
		}
		if strings.HasSuffix(item.Title, "/") {
			return "Open this folder"
		}
		return "Select this entry"
	}
}

func transferBrowserModeLabel(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "local-file":
		return "choose local file"
	case "local-folder", "local-directory":
		return "choose local folder"
	case "remote-file":
		return "choose remote file"
	case "remote-folder", "remote-directory":
		return "choose remote folder"
	default:
		if mode == "" {
			return "browse files"
		}
		return mode
	}
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func (m *transferBrowserModel) applyFilter() {
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

func (m *transferBrowserModel) moveCursor(delta int) {
	if len(m.filtered) == 0 || delta == 0 {
		return
	}
	m.cursor = clamp(m.cursor+delta, 0, len(m.filtered)-1)
	m.ensureCursorVisible()
}

func (m *transferBrowserModel) jumpSection(delta int) {
	if next := chrome.JumpSection(len(m.filtered), m.cursor, delta, m.filteredGroup); next != m.cursor {
		m.cursor = next
		m.ensureCursorVisible()
	}
}

func (m transferBrowserModel) filteredGroup(pos int) string {
	if pos < 0 || pos >= len(m.filtered) {
		return ""
	}
	index := m.filtered[pos]
	if index < 0 || index >= len(m.items) {
		return ""
	}
	return m.items[index].Group
}

// groupAt is the chrome.ListView callback: the group label of filtered row i,
// ok=false for stale indices (skipped from the line budget).
func (m transferBrowserModel) groupAt(i int) (string, bool) {
	if i < 0 || i >= len(m.filtered) {
		return "", false
	}
	index := m.filtered[i]
	if index < 0 || index >= len(m.items) {
		return "", false
	}
	return m.items[index].Group, true
}

func (m *transferBrowserModel) ensureCursorVisible() {
	budget := m.listWindowBudget()
	contains := func(start, cursor int) bool {
		return chrome.WindowContainsCursor(len(m.filtered), start, cursor, budget, m.groupAt)
	}
	m.cursor, m.scrollOffset = chrome.ClampWindow(len(m.filtered), m.cursor, m.scrollOffset, contains)
}

func (m transferBrowserModel) normalizedScrollOffset() int {
	if len(m.filtered) == 0 || m.scrollOffset < 0 {
		return 0
	}
	if m.scrollOffset >= len(m.filtered) {
		return len(m.filtered) - 1
	}
	return m.scrollOffset
}

func (m transferBrowserModel) listWindowBudget() int {
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
