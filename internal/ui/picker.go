package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/hostlist"
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

	model := newPickerModel(items, opts)
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
	noColor     bool
	title       string
	subtitle    string
	summary     []string
	footer      string
	width       int
	height      int
}

func newPickerModel(items []Item, opts PickOptions) pickerModel {
	model := pickerModel{
		items:       append([]Item(nil), items...),
		selected:    -1,
		noAltScreen: opts.NoAltScreen,
		noColor:     opts.NoColor,
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
	width := clamp(m.width, 48, 120)
	title := m.title
	if title == "" {
		title = "ssherpa"
	}
	b.WriteString(title)
	if m.subtitle != "" {
		b.WriteString("  ")
		b.WriteString(truncate(m.subtitle, max(0, width-len(title)-2)))
	}
	b.WriteByte('\n')
	if len(m.summary) > 0 {
		for _, line := range m.summary {
			b.WriteString("  ")
			b.WriteString(truncate(line, width-2))
			b.WriteByte('\n')
		}
	}
	b.WriteString(strings.Repeat("-", width))
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Search  %-*s  %d/%d\n", max(0, width-22), truncate(m.query, max(0, width-22)), len(m.filtered), len(m.items))
	b.WriteString("\n\n")

	if len(m.filtered) == 0 {
		b.WriteString("No matches\n")
	} else {
		limit := visibleLimit(m.height, len(m.summary))
		lastGroup := ""
		rendered := 0
		for i := 0; i < len(m.filtered) && rendered < limit; i++ {
			index := m.filtered[i]
			item := m.items[index]
			if item.Group != "" && item.Group != lastGroup {
				if rendered > 0 {
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "%s\n", item.Group)
				lastGroup = item.Group
			}
			b.WriteString(m.renderRow(item, i == m.cursor, width))
			rendered++
		}
		if len(m.filtered) > rendered {
			fmt.Fprintf(&b, "  ... %d more\n", len(m.filtered)-rendered)
		}
	}

	footer := m.footer
	if footer == "" {
		footer = "enter select  /  type search  /  arrows move  /  q quit"
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat("-", width))
	b.WriteByte('\n')
	b.WriteString(footer)
	b.WriteByte('\n')

	view := tea.NewView(b.String())
	view.AltScreen = !m.noAltScreen
	return view
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

func (m pickerModel) renderRow(item Item, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = "> "
	}

	badge := item.Badge
	if badge == "" {
		badge = string(item.Kind)
	}

	titleWidth := clamp(width/3, 16, 30)
	badgeWidth := 7
	descWidth := max(10, width-len(cursor)-badgeWidth-titleWidth-6)
	title := truncate(item.Title, titleWidth)
	desc := truncate(item.Description, descWidth)
	if item.Detail != "" && descWidth > 24 {
		detailWidth := min(18, descWidth/3)
		desc = truncate(item.Description, descWidth-detailWidth-3) + "  " + truncate(item.Detail, detailWidth)
	}
	return fmt.Sprintf("%s%-*s %-*s %s\n", cursor, badgeWidth, badge, titleWidth, title, desc)
}

func visibleLimit(height int, summaryLines int) int {
	if height <= 0 {
		return 14
	}
	limit := height - summaryLines - 10
	return clamp(limit, 8, 18)
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

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "~"
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
