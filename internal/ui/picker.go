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

type ItemKind string

const (
	ItemAlias    ItemKind = "alias"
	ItemAdd      ItemKind = "add"
	ItemEdit     ItemKind = "edit"
	ItemAuthkeys ItemKind = "authkeys"
	ItemProxy    ItemKind = "proxy"
	ItemJump     ItemKind = "jump"
	ItemSessions ItemKind = "sessions"
	ItemTheme    ItemKind = "theme"
)

type Item struct {
	Kind        ItemKind
	Token       string
	Title       string
	Description string
	Group       string
	Badge       string
	Detail      string
}

type PickOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Subtitle    string
	Summary     []string
	Footer      string
}

type BuildItemsOptions struct {
	SessionCount       int
	ActiveSessionCount int
}

func BuildItems(aliases []hostlist.Alias) []Item {
	return BuildItemsWithOptions(aliases, BuildItemsOptions{})
}

func BuildItemsWithOptions(aliases []hostlist.Alias, opts BuildItemsOptions) []Item {
	items := []Item{
		{Kind: ItemAdd, Token: "ADD", Title: "Add new alias", Description: "write a safe Host stanza", Group: "Actions", Badge: "add"},
		{Kind: ItemEdit, Token: "EDIT", Title: "Edit aliases or delete", Description: "update or remove config entries", Group: "Actions", Badge: "edit"},
		{Kind: ItemJump, Token: "JUMP", Title: "Jump via intermediate hops", Description: "build a ProxyJump route", Group: "Actions", Badge: "jump"},
		{Kind: ItemProxy, Token: "PROXY", Title: "Start SOCKS proxy", Description: "bind a local SOCKS port", Group: "Actions", Badge: "proxy"},
		{Kind: ItemAuthkeys, Token: "AUTHKEYS", Title: "Manage authorized_keys", Description: "add, merge, replace, or delete login keys", Group: "Actions", Badge: "keys"},
		{Kind: ItemSessions, Token: "SESSIONS", Title: "Sessions and route map", Description: sessionDescription(opts), Group: "Actions", Badge: "map"},
		{Kind: ItemTheme, Token: "THEME", Title: "Theme and colors", Description: "preview and save UI palette", Group: "Actions", Badge: "theme"},
	}

	for _, alias := range aliases {
		items = append(items, Item{
			Kind:        ItemAlias,
			Token:       alias.Name,
			Title:       alias.Name,
			Description: displayAlias(alias),
			Group:       "Hosts",
			Badge:       aliasBadge(alias),
			Detail:      aliasDetail(alias),
		})
	}

	return items
}

func Pick(ctx context.Context, items []Item, opts PickOptions) (Item, bool, error) {
	if len(items) == 0 {
		return Item{}, false, nil
	}

	theme, err := resolvePickTheme(opts)
	if err != nil {
		return Item{}, false, err
	}
	model := newPickerModelWithTheme(items, opts, theme)
	programOptions := []tea.ProgramOption{
		tea.WithContext(ctx),
	}
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

	picker, ok := finalModel.(pickerModel)
	if !ok || picker.canceled || picker.selected < 0 {
		return Item{}, false, nil
	}
	return picker.items[picker.selected], true, nil
}

type pickerModel struct {
	items       []Item
	filtered    []int
	cursor      int
	query       string
	selected    int
	canceled    bool
	noAltScreen bool
	theme       termstyle.Theme
	title       string
	subtitle    string
	summary     []string
	footer      string
	width       int
	height      int
}

func newPickerModel(items []Item, opts PickOptions) pickerModel {
	theme := opts.Theme
	if theme.IsZero() {
		theme = termstyle.TerminalTheme()
	}
	return newPickerModelWithTheme(items, opts, theme.WithNoColor(opts.NoColor))
}

func newPickerModelWithTheme(items []Item, opts PickOptions, theme termstyle.Theme) pickerModel {
	model := pickerModel{
		items:       append([]Item(nil), items...),
		selected:    -1,
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		title:       opts.Title,
		subtitle:    opts.Subtitle,
		summary:     append([]string(nil), opts.Summary...),
		footer:      opts.Footer,
		width:       88,
		height:      24,
	}
	model.applyFilter()
	return model
}

func resolvePickTheme(opts PickOptions) (termstyle.Theme, error) {
	if !opts.Theme.IsZero() {
		return opts.Theme.WithNoColor(opts.Theme.NoColor || opts.NoColor), nil
	}
	return termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    opts.ThemeName,
		File:    opts.ThemeFile,
		NoColor: opts.NoColor,
	})
}

func (m pickerModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "ctrl+c", "esc", "q":
			m.canceled = true
			return m, tea.Quit
		case "enter":
			if len(m.filtered) == 0 {
				return m, nil
			}
			m.selected = m.filtered[m.cursor]
			return m, tea.Quit
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "ctrl+n":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
		case "backspace":
			if m.query != "" {
				m.query = m.query[:len(m.query)-1]
				m.applyFilter()
			}
		default:
			if msg.Text != "" && !isControlKey(msg.String()) {
				m.query += msg.Text
				m.applyFilter()
			}
		}
	}
	return m, nil
}

func (m pickerModel) View() tea.View {
	var b strings.Builder
	width := clamp(m.width, 56, 140)
	theme := pickerTheme{theme: m.theme}

	b.WriteString(m.renderHeader(width, theme))
	b.WriteByte('\n')
	for _, line := range m.renderBody(width, theme) {
		b.WriteString(line)
		b.WriteByte('\n')
	}

	footer := m.footer
	if footer == "" {
		footer = "enter select  /  type filter  /  arrows move  /  q quit"
	}
	b.WriteString(theme.rule(width))
	b.WriteByte('\n')
	b.WriteString(theme.footer(termstyle.Truncate(footer, width)))
	b.WriteByte('\n')

	view := tea.NewView(b.String())
	view.AltScreen = !m.noAltScreen
	return view
}

func (m pickerModel) renderHeader(width int, theme pickerTheme) string {
	var b strings.Builder
	title := strings.TrimSpace(m.title)
	if title == "" {
		title = "ssherpa"
	}
	header := theme.logo(strings.ToUpper(title))
	if m.subtitle != "" {
		header += " " + theme.pill(strings.ToUpper(m.subtitle))
	}
	b.WriteString(termstyle.PadRight(header, width))
	b.WriteByte('\n')
	for _, line := range m.summary {
		b.WriteString("  ")
		b.WriteString(theme.summary(termstyle.Truncate(line, width-4)))
		b.WriteByte('\n')
	}
	b.WriteString(theme.rule(width))
	b.WriteByte('\n')

	counter := fmt.Sprintf("%d/%d", len(m.filtered), len(m.items))
	label := theme.label("FILTER")
	counterText := theme.counter(counter)
	query := m.query
	if query == "" {
		query = "type to filter"
	}
	fieldWidth := max(8, width-termstyle.VisibleWidth(label)-termstyle.VisibleWidth(counterText)-8)
	field := "[" + termstyle.PadRight(termstyle.Truncate(query, fieldWidth), fieldWidth) + "]"
	if m.query == "" {
		field = theme.muted(field)
	} else {
		field = theme.search(field)
	}
	b.WriteString(label)
	b.WriteString("  ")
	b.WriteString(field)
	b.WriteString("  ")
	b.WriteString(counterText)
	b.WriteByte('\n')
	b.WriteString(theme.rule(width))
	return b.String()
}

func (m pickerModel) renderBody(width int, theme pickerTheme) []string {
	listWidth := width
	previewWidth := 0
	if width >= 100 && len(m.filtered) > 0 {
		listWidth = clamp(width*62/100, 58, 88)
		previewWidth = width - listWidth - 3
	}

	list := m.renderListLines(listWidth, theme)
	if previewWidth <= 0 {
		return list
	}
	preview := m.renderPreviewLines(previewWidth, theme)
	lines := max(len(list), len(preview))
	out := make([]string, 0, lines)
	divider := theme.muted("|")
	for i := 0; i < lines; i++ {
		left := ""
		if i < len(list) {
			left = list[i]
		}
		right := ""
		if i < len(preview) {
			right = preview[i]
		}
		out = append(out, termstyle.PadRight(left, listWidth)+" "+divider+" "+right)
	}
	return out
}

func (m pickerModel) renderListLines(width int, theme pickerTheme) []string {
	if len(m.filtered) == 0 {
		return []string{"", "  " + theme.empty("No matches")}
	}

	limit := visibleLimit(m.height, len(m.summary))
	lines := []string{""}
	lastGroup := ""
	rendered := 0
	for i := 0; i < len(m.filtered) && rendered < limit; i++ {
		index := m.filtered[i]
		item := m.items[index]
		if item.Group != "" && item.Group != lastGroup {
			if rendered > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, theme.groupHeader(item.Group, width))
			lastGroup = item.Group
		}
		lines = append(lines, m.renderRow(item, i == m.cursor, width, theme))
		rendered++
	}
	if len(m.filtered) > rendered {
		lines = append(lines, "  "+theme.muted(fmt.Sprintf("%d more hidden by terminal height", len(m.filtered)-rendered)))
	}
	return lines
}

func (m pickerModel) renderPreviewLines(width int, theme pickerTheme) []string {
	item, ok := m.currentItem()
	if !ok {
		return nil
	}

	lines := []string{
		"",
		theme.groupHeader("Selection", width),
		theme.previewTitle(termstyle.Truncate(item.Title, width)),
	}
	if item.Badge != "" {
		lines = append(lines, previewKV(theme, width, "Type", strings.ToUpper(item.Badge)))
	}
	if item.Token != "" && item.Token != item.Title {
		lines = append(lines, previewKV(theme, width, "Token", item.Token))
	}
	if item.Description != "" {
		lines = append(lines, previewKV(theme, width, "Target", item.Description))
	}
	if item.Detail != "" {
		lines = append(lines, previewKV(theme, width, "Source", item.Detail))
	}
	lines = append(lines, "")
	lines = append(lines, theme.muted(termstyle.Truncate(selectionHint(item), width)))
	return lines
}

func (m pickerModel) currentItem() (Item, bool) {
	if len(m.filtered) == 0 || m.cursor < 0 || m.cursor >= len(m.filtered) {
		return Item{}, false
	}
	index := m.filtered[m.cursor]
	if index < 0 || index >= len(m.items) {
		return Item{}, false
	}
	return m.items[index], true
}

func (m *pickerModel) applyFilter() {
	m.filtered = m.filtered[:0]
	for i, item := range m.items {
		if m.query == "" || fuzzyMatch(item.Title+"\t"+item.Description, m.query) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m pickerModel) renderRow(item Item, selected bool, width int, theme pickerTheme) string {
	cursor := "  "
	if selected {
		cursor = ">>"
	}

	badge := item.Badge
	if badge == "" {
		badge = string(item.Kind)
	}

	titleWidth := clamp(width/3, 16, 28)
	badgeWidth := 10
	descWidth := max(10, width-len(cursor)-badgeWidth-titleWidth-8)
	title := termstyle.Truncate(item.Title, titleWidth)
	desc := termstyle.Truncate(item.Description, descWidth)
	if item.Detail != "" && descWidth > 24 {
		detailWidth := min(18, descWidth/3)
		desc = termstyle.Truncate(item.Description, descWidth-detailWidth-3) + "  " + termstyle.Truncate(item.Detail, detailWidth)
	}
	cursor = theme.cursor(cursor, selected)
	badgeText := theme.badge(item.Kind, "["+strings.ToUpper(badge)+"]")
	titleText := theme.rowTitle(title, selected)
	descText := theme.rowDesc(desc, selected)
	line := cursor + " " + termstyle.PadRight(badgeText, badgeWidth) + " " + termstyle.PadRight(titleText, titleWidth) + " " + descText
	return termstyle.PadRight(line, width)
}

func visibleLimit(height int, summaryLines int) int {
	if height <= 0 {
		return 12
	}
	limit := height - summaryLines - 11
	return clamp(limit, 7, 18)
}

type pickerTheme struct {
	theme termstyle.Theme
}

func (t pickerTheme) logo(value string) string {
	return t.theme.Style(termstyle.RoleTitle, value)
}

func (t pickerTheme) pill(value string) string {
	return t.theme.Style(termstyle.RolePill, " "+value+" ")
}

func (t pickerTheme) summary(value string) string {
	return t.theme.Style(termstyle.RoleSecondary, value)
}

func (t pickerTheme) rule(width int) string {
	return t.theme.Style(termstyle.RoleBorder, strings.Repeat("-", width))
}

func (t pickerTheme) label(value string) string {
	return t.theme.Style(termstyle.RoleAccent, value)
}

func (t pickerTheme) counter(value string) string {
	return t.theme.Style(termstyle.RoleSuccess, value)
}

func (t pickerTheme) search(value string) string {
	return t.theme.Style(termstyle.RoleSearch, value)
}

func (t pickerTheme) muted(value string) string {
	return t.theme.Style(termstyle.RoleMuted, value)
}

func (t pickerTheme) empty(value string) string {
	return t.theme.Style(termstyle.RoleWarning, value)
}

func (t pickerTheme) footer(value string) string {
	return t.theme.Style(termstyle.RoleMuted, value)
}

func (t pickerTheme) groupHeader(value string, width int) string {
	label := " " + strings.ToUpper(value) + " "
	if termstyle.VisibleWidth(label) >= width {
		return t.theme.Style(termstyle.RoleAccent, termstyle.Truncate(strings.ToUpper(value), width))
	}
	line := label + strings.Repeat("-", width-termstyle.VisibleWidth(label))
	return t.theme.Style(termstyle.RoleAccent, line)
}

func (t pickerTheme) cursor(value string, selected bool) string {
	if selected {
		return t.theme.Style(termstyle.RoleSuccess, value)
	}
	return t.theme.Style(termstyle.RoleBorder, value)
}

func (t pickerTheme) badge(kind ItemKind, value string) string {
	role := termstyle.RolePrimary
	switch kind {
	case ItemAlias:
		role = termstyle.RoleSuccess
	case ItemAdd:
		role = termstyle.RolePrimary
	case ItemEdit:
		role = termstyle.RoleWarning
	case ItemJump:
		role = termstyle.RoleInfo
	case ItemProxy:
		role = termstyle.RoleDanger
	case ItemAuthkeys:
		role = termstyle.RoleSecondary
	case ItemSessions:
		role = termstyle.RoleSuccess
	case ItemTheme:
		role = termstyle.RoleInfo
	}
	return t.theme.Style(role, value)
}

func (t pickerTheme) rowTitle(value string, selected bool) string {
	if selected {
		return t.theme.Style(termstyle.RoleSelected, value)
	}
	return t.theme.Style(termstyle.RoleForeground, value)
}

func (t pickerTheme) rowDesc(value string, selected bool) string {
	if selected {
		return t.theme.Style(termstyle.RoleForeground, value)
	}
	return t.theme.Style(termstyle.RoleMuted, value)
}

func (t pickerTheme) previewTitle(value string) string {
	return t.theme.Style(termstyle.RoleSelected, value)
}

func previewKV(theme pickerTheme, width int, key string, value string) string {
	keyText := termstyle.PadRight(theme.muted(key), 8)
	valueText := theme.rowDesc(termstyle.Truncate(value, max(0, width-9)), false)
	return keyText + " " + valueText
}

func selectionHint(item Item) string {
	switch item.Kind {
	case ItemAlias:
		return "Connects with local OpenSSH under supervised mode."
	case ItemAdd:
		return "Creates a new Host stanza with safe write behavior."
	case ItemEdit:
		return "Updates or removes existing Host entries."
	case ItemJump:
		return "Builds a ProxyJump route through selected hops."
	case ItemProxy:
		return "Starts a local SOCKS proxy through an SSH alias."
	case ItemAuthkeys:
		return "Manages authorized_keys on this device."
	case ItemSessions:
		return "Opens the active session route map."
	case ItemTheme:
		return "Builds and saves a UI color schema."
	default:
		return "Ready."
	}
}

func fuzzyMatch(value string, query string) bool {
	valueRunes := []rune(strings.ToLower(value))
	queryRunes := []rune(strings.ToLower(query))
	pos := 0
	for _, r := range queryRunes {
		found := false
		for pos < len(valueRunes) {
			if valueRunes[pos] == r {
				pos++
				found = true
				break
			}
			pos++
		}
		if !found {
			return false
		}
	}
	return true
}

func isControlKey(key string) bool {
	switch key {
	case "tab", "shift+tab", "left", "right", "home", "end", "pgup", "pgdown", "delete":
		return true
	default:
		return strings.HasPrefix(key, "ctrl+") || strings.HasPrefix(key, "alt+")
	}
}

func sessionDescription(opts BuildItemsOptions) string {
	if opts.ActiveSessionCount == 0 {
		if opts.SessionCount == 0 {
			return "no active sessions"
		}
		return fmt.Sprintf("no active sessions (%d recorded)", opts.SessionCount)
	}
	if opts.SessionCount == opts.ActiveSessionCount {
		return fmt.Sprintf("%d active sessions", opts.ActiveSessionCount)
	}
	return fmt.Sprintf("%d active sessions (%d recorded)", opts.ActiveSessionCount, opts.SessionCount)
}

func aliasBadge(alias hostlist.Alias) string {
	switch {
	case alias.IsConditional:
		return "match"
	case alias.IsPattern:
		return "pattern"
	case alias.User == "git":
		return "git"
	default:
		return "host"
	}
}

func aliasDetail(alias hostlist.Alias) string {
	var parts []string
	if alias.SourcePath != "" {
		parts = append(parts, fmt.Sprintf("%s:%d", shortPath(alias.SourcePath), alias.SourceLine))
	}
	if len(alias.Warnings) > 0 {
		parts = append(parts, fmt.Sprintf("%d warning", len(alias.Warnings)))
	}
	return strings.Join(parts, "  ")
}

func shortPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) <= 2 {
		return path
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

func clamp(value int, low int, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func displayAlias(alias hostlist.Alias) string {
	var b strings.Builder
	if alias.User != "" {
		b.WriteString(alias.User)
		b.WriteByte('@')
	}
	if alias.HostName != "" {
		b.WriteString(alias.HostName)
	}
	if alias.Port != "" {
		b.WriteByte(':')
		b.WriteString(alias.Port)
	}
	if len(alias.IdentityFiles) > 0 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('[')
		b.WriteString(strings.Join(alias.IdentityFiles, ", "))
		b.WriteByte(']')
	}
	if b.Len() == 0 {
		return "(no HostName in config)"
	}
	return b.String()
}
