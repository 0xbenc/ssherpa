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
	ItemAlias         ItemKind = "alias"
	ItemAdd           ItemKind = "add"
	ItemEdit          ItemKind = "edit"
	ItemAuthkeys      ItemKind = "authkeys"
	ItemProxy         ItemKind = "proxy"
	ItemJump          ItemKind = "jump"
	ItemForward       ItemKind = "forward"
	ItemForwardSaved  ItemKind = "forward_saved"
	ItemForwardActive ItemKind = "forward_active"
	ItemProxySaved    ItemKind = "proxy_saved"
	ItemProxyActive   ItemKind = "proxy_active"
	ItemCheck         ItemKind = "check"
	ItemSessions      ItemKind = "sessions"
	ItemTheme         ItemKind = "theme"
	ItemDocs          ItemKind = "docs"
	ItemConfirmDelete ItemKind = "confirm_delete"
	ItemConfirmCancel ItemKind = "confirm_cancel"
)

// SavedForwardItem is the picker-facing projection of a saved
// forward catalog entry. The caller (cli.runConnect) flattens
// state.StoredForward records into this so the ui package stays
// free of internal/state.
type SavedForwardItem struct {
	Name        string
	Description string
}

// ActiveTunnelItem is the picker-facing projection of a live
// background tunnel (KindTunnel session whose daemon PID is still
// responding to signal 0). Selecting one on the home page calls
// `ssherpa forward stop <SessionID>` — the picker is the operator's
// one-tap "kill this tunnel" surface. The caller derives the
// Title/Description and passes the canonical SessionID via Token so
// the dispatcher knows exactly which record to signal.
type ActiveTunnelItem struct {
	SessionID   string // full session record ID; matched by `forward stop`
	Title       string // short, recognizable label (saved alias / target / id tail)
	Description string // local→remote · uptime · daemon pid
}

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
	// Version, when set, renders as a muted tag immediately after
	// the title logo (e.g. "SSHERPA v1.1.0  [SUPERVISED MODE]"). Set
	// only for the home-page picker; sub-pickers (jump, proxy,
	// through-hop, etc.) leave it empty.
	Version  string
	Subtitle string
	Summary  []string
	Footer   string
}

type BuildItemsOptions struct {
	SessionCount       int
	ActiveSessionCount int
	// SavedForwards renders as a "Saved Forwards" group above the
	// standard action rows so daily-use one-tap launches get
	// prominence — decision #3 in docs/forward-phase-2.md.
	SavedForwards []SavedForwardItem
	SavedProxies  []SavedForwardItem
	// ActiveTunnels renders as an "Active Tunnels" group above
	// Saved Forwards — they're more actionable (you want to stop
	// them right now) than launches. Picker dispatch calls
	// `ssherpa forward stop` when one is selected. The count
	// shown in the subtitle is derived from len(ActiveTunnels).
	ActiveTunnels []ActiveTunnelItem
	ActiveProxies []ActiveTunnelItem
}

func BuildItems(aliases []hostlist.Alias) []Item {
	return BuildItemsWithOptions(aliases, BuildItemsOptions{})
}

func BuildItemsWithOptions(aliases []hostlist.Alias, opts BuildItemsOptions) []Item {
	items := []Item{}

	// Active tunnels lead — they're the "kill this right now"
	// surface. Selecting one calls `forward stop SESSION_ID`,
	// signaling the daemon and finalizing the record.
	for _, at := range opts.ActiveTunnels {
		items = append(items, Item{
			Kind:        ItemForwardActive,
			Token:       at.SessionID,
			Title:       at.Title,
			Description: at.Description,
			Group:       "Active Tunnels",
			Badge:       "stop",
		})
	}
	for _, ap := range opts.ActiveProxies {
		items = append(items, Item{
			Kind:        ItemProxyActive,
			Token:       ap.SessionID,
			Title:       ap.Title,
			Description: ap.Description,
			Group:       "Active Proxies",
			Badge:       "stop",
		})
	}

	// Saved forwards next — explicit "I set this up to reuse"
	// entries. Selecting one launches the saved spec.
	for _, sf := range opts.SavedForwards {
		items = append(items, Item{
			Kind:        ItemForwardSaved,
			Token:       sf.Name,
			Title:       sf.Name,
			Description: sf.Description,
			Group:       "Saved Forwards",
			Badge:       "forward",
		})
	}
	for _, sp := range opts.SavedProxies {
		items = append(items, Item{
			Kind:        ItemProxySaved,
			Token:       sp.Name,
			Title:       sp.Name,
			Description: sp.Description,
			Group:       "Saved Proxies",
			Badge:       "proxy",
		})
	}

	items = append(items,
		Item{Kind: ItemAdd, Token: "ADD", Title: "Add new alias", Group: "Actions", Badge: "add"},
		Item{Kind: ItemEdit, Token: "EDIT", Title: "Edit aliases and forwards", Group: "Actions", Badge: "edit"},
		Item{Kind: ItemJump, Token: "JUMP", Title: "Jump via intermediate hops", Group: "Actions", Badge: "jump"},
		Item{Kind: ItemProxy, Token: "PROXY", Title: "Start SOCKS proxy", Group: "Actions", Badge: "proxy"},
		Item{Kind: ItemForward, Token: "FORWARD", Title: "Open port-forward tunnel", Group: "Actions", Badge: "forward"},
		Item{Kind: ItemCheck, Token: "CHECK", Title: "Check reachability", Group: "Actions", Badge: "check"},
		Item{Kind: ItemAuthkeys, Token: "AUTHKEYS", Title: "Manage authorized_keys", Group: "Actions", Badge: "keys"},
		Item{Kind: ItemSessions, Token: "SESSIONS", Title: "Sessions and route map", Group: "Actions", Badge: "map"},
		Item{Kind: ItemTheme, Token: "THEME", Title: "Theme and colors", Group: "Actions", Badge: "theme"},
		Item{Kind: ItemDocs, Token: "DOCS", Title: "Completions and manpage", Group: "Actions", Badge: "docs"},
	)

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
	version     string
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
		version:     opts.Version,
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
	width := max(56, m.width)
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
	if v := strings.TrimSpace(m.version); v != "" {
		header += " " + theme.versionTag(v)
	}
	if m.subtitle != "" {
		header += " " + theme.pill(strings.ToUpper(m.subtitle))
	}

	summary := m.summary
	if len(summary) > 0 && termstyle.VisibleWidth(header)+2+termstyle.VisibleWidth(summary[0]) <= width {
		gap := width - termstyle.VisibleWidth(header) - termstyle.VisibleWidth(summary[0])
		b.WriteString(header)
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(theme.summary(summary[0]))
		b.WriteByte('\n')
		summary = summary[1:]
	} else {
		b.WriteString(termstyle.PadRight(header, width))
		b.WriteByte('\n')
	}
	for _, line := range summary {
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
		listWidth = max(requiredListWidth(m.items, m.filtered), clamp(width*45/100, 46, 72))
		if listWidth > width-28 {
			listWidth = width - 28
		}
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

func requiredListWidth(items []Item, filtered []int) int {
	width := 46
	for _, index := range filtered {
		if index < 0 || index >= len(items) {
			continue
		}
		item := items[index]
		if item.Description != "" || item.Detail != "" {
			continue
		}
		titleWidth := len([]rune(item.Title))
		if titleWidth == 0 {
			continue
		}
		// renderRow's fixed columns: cursor(2), separators(3), badge(10).
		width = max(width, 15+titleWidth)
	}
	return width
}

func (m pickerModel) renderListLines(width int, theme pickerTheme) []string {
	if len(m.filtered) == 0 {
		return []string{"", "  " + theme.empty("No matches")}
	}

	// Budget the list against the actual terminal height. Each candidate row is
	// charged its own line plus any group header/separator it introduces, and a
	// line is reserved for the "N more hidden" notice while items remain, so the
	// list grows to fill a tall terminal instead of stopping at a fixed cap.
	budget := listBudget(m.height, len(m.summary))
	lines := []string{""}
	lastGroup := ""
	rendered := 0
	for i := 0; i < len(m.filtered); i++ {
		index := m.filtered[i]
		item := m.items[index]
		groupCost := 0
		newGroup := item.Group != "" && item.Group != lastGroup
		if newGroup {
			groupCost = 1 // group header
			if rendered > 0 {
				groupCost++ // blank separator before the header
			}
		}
		reserve := 0
		if len(m.filtered)-i-1 > 0 {
			reserve = 1 // room for the "N more hidden" notice
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
		lines = append(lines, previewKVLines(theme, width, "Type", strings.ToUpper(item.Badge), 2)...)
	}
	if item.Token != "" && item.Token != item.Title {
		lines = append(lines, previewKVLines(theme, width, "Token", item.Token, 2)...)
	}
	if item.Description != "" {
		lines = append(lines, previewKVLines(theme, width, "Target", item.Description, 2)...)
	}
	if item.Detail != "" {
		lines = append(lines, previewKVLines(theme, width, "Source", item.Detail, 2)...)
	}
	lines = append(lines, "")
	for _, line := range wrapPlain(selectionHint(item), width, 2) {
		lines = append(lines, theme.subtle(line))
	}
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

	badgeWidth := 10
	titleWidth := clamp(width/3, 16, 28)
	if item.Description == "" && item.Detail == "" {
		titleWidth = max(10, width-len(cursor)-badgeWidth-3)
	}
	descWidth := max(10, width-len(cursor)-badgeWidth-titleWidth-3)
	title := termstyle.Truncate(item.Title, titleWidth)
	desc := termstyle.Truncate(item.Description, descWidth)
	if item.Kind == ItemAlias {
		titleWidth = max(10, width-len(cursor)-badgeWidth-3)
		title = termstyle.Truncate(item.Title, titleWidth)
		desc = ""
	}
	if item.Kind != ItemAlias && item.Detail != "" && descWidth > 24 {
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

// listBudget returns how many lines the list column may occupy (including its
// leading blank, group headers, rows, and the truncation notice). It reserves
// the fixed chrome around the list: the header (logo + summary + rule + filter
// + rule = 4 + summaryLines) and the footer (rule + help = 2), plus a one-line
// safety margin so the view does not spill past the last terminal row.
func listBudget(height int, summaryLines int) int {
	if height <= 0 {
		return 13
	}
	budget := height - (4 + summaryLines) - 2 - 1
	if budget < 8 {
		budget = 8
	}
	return budget
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

func (t pickerTheme) subtle(value string) string {
	return t.theme.Style(termstyle.RoleSubtle, value)
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
		role = termstyle.RoleSuccess
	case ItemEdit:
		role = termstyle.RoleWarning
	case ItemJump:
		role = termstyle.RoleInfo
	case ItemProxy:
		role = termstyle.RoleDanger
	case ItemForward:
		role = termstyle.RolePrimary
	case ItemForwardSaved:
		role = termstyle.RolePrimary
	case ItemForwardActive, ItemProxyActive:
		role = termstyle.RoleDanger
	case ItemCheck:
		role = termstyle.RoleInfo
	case ItemAuthkeys:
		role = termstyle.RoleWarning
	case ItemSessions:
		role = termstyle.RoleSecondary
	case ItemTheme:
		role = termstyle.RoleAccent
	case ItemDocs:
		role = termstyle.RoleSecondary
	case ItemConfirmDelete:
		role = termstyle.RoleDanger
	case ItemConfirmCancel:
		role = termstyle.RoleSecondary
	}
	return t.theme.Style(role, value)
}

// versionTag renders the build version (e.g. "v1.1.0" / "dev") in a
// muted accent style so it sits visibly next to the SSHERPA logo
// without competing with the prominent subtitle pill.
func (t pickerTheme) versionTag(value string) string {
	return t.theme.Style(termstyle.RoleAccent, value)
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
	keyText := termstyle.PadRight(theme.subtle(key), 8)
	valueText := theme.subtle(termstyle.Truncate(value, max(0, width-9)))
	return keyText + " " + valueText
}

func previewKVLines(theme pickerTheme, width int, key string, value string, maxLines int) []string {
	valueWidth := max(0, width-9)
	wrapped := wrapPlain(value, valueWidth, maxLines)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	out := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		keyText := strings.Repeat(" ", 8)
		if i == 0 {
			keyText = theme.subtle(key)
		}
		out = append(out, termstyle.PadRight(keyText, 8)+" "+theme.subtle(line))
	}
	return out
}

func wrapPlain(value string, width int, maxLines int) []string {
	value = strings.TrimSpace(value)
	if value == "" || width <= 0 || maxLines <= 0 {
		return nil
	}

	words := strings.Fields(value)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	current := ""
	for i := 0; i < len(words); i++ {
		word := words[i]
		if current == "" {
			current = word
		} else if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
		} else {
			lines = append(lines, current)
			current = word
			if len(lines) == maxLines-1 {
				remaining := strings.Join(words[i:], " ")
				lines = append(lines, termstyle.Truncate(remaining, width))
				break
			}
		}
	}
	if current != "" && len(lines) < maxLines {
		lines = append(lines, current)
	}
	for i, line := range lines {
		lines[i] = termstyle.Truncate(line, width)
	}
	return lines
}

func selectionHint(item Item) string {
	switch item.Kind {
	case ItemAlias:
		return "Connects with local OpenSSH under supervised mode."
	case ItemAdd:
		return "Adds a new SSH alias to your config."
	case ItemEdit:
		return "Updates or removes existing Host entries."
	case ItemJump:
		return "Builds a ProxyJump route through selected hops."
	case ItemProxy:
		return "Starts a local SOCKS proxy through an SSH alias."
	case ItemProxySaved:
		return "Launches a saved SOCKS proxy preset."
	case ItemProxyActive:
		return "Stops this running SOCKS proxy."
	case ItemForward:
		return "Builds an ssh -L port-forward tunnel — pick destination, ports, optional jump hop."
	case ItemForwardSaved:
		return "Launches a saved port-forward tunnel from your ssherpa catalog."
	case ItemForwardActive:
		return "Stops this running tunnel — signals the daemon and finalizes the record."
	case ItemCheck:
		return "Runs SSH and ICMP reachability checks for hosts or saved forwards."
	case ItemAuthkeys:
		return "Manages authorized_keys on this device."
	case ItemSessions:
		return "Opens the active session route map."
	case ItemTheme:
		return "Builds and saves a UI color schema."
	case ItemDocs:
		return "Shows installed shell completion and manpage artifact paths."
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
