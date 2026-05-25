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
)

type Item struct {
	Kind        ItemKind
	Token       string
	Title       string
	Description string
}

type PickOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
}

func BuildItems(aliases []hostlist.Alias) []Item {
	items := []Item{
		{Kind: ItemAdd, Token: "ADD", Title: "Add new alias", Description: "write a safe Host stanza"},
		{Kind: ItemEdit, Token: "EDIT", Title: "Edit aliases or delete", Description: "update or remove config entries"},
		{Kind: ItemAuthkeys, Token: "AUTHKEYS", Title: "Manage authorized_keys on this device", Description: "available in a later phase"},
		{Kind: ItemProxy, Token: "PROXY", Title: "Start SOCKS proxy", Description: "available in a later phase"},
		{Kind: ItemJump, Token: "JUMP", Title: "Jump via intermediate hops", Description: "available in a later phase"},
	}

	for _, alias := range aliases {
		items = append(items, Item{
			Kind:        ItemAlias,
			Token:       alias.Name,
			Title:       alias.Name,
			Description: displayAlias(alias),
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
}

func newPickerModel(items []Item, opts PickOptions) pickerModel {
	model := pickerModel{
		items:       append([]Item(nil), items...),
		selected:    -1,
		noAltScreen: opts.NoAltScreen,
		noColor:     opts.NoColor,
	}
	model.applyFilter()
	return model
}

func (m pickerModel) Init() tea.Cmd {
	return nil
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
	b.WriteString("Pick an SSH alias\n")
	b.WriteString("Filter: ")
	b.WriteString(m.query)
	b.WriteString("\n\n")

	if len(m.filtered) == 0 {
		b.WriteString("No matches\n")
	} else {
		limit := min(len(m.filtered), 12)
		for i := 0; i < limit; i++ {
			item := m.items[m.filtered[i]]
			cursor := "  "
			if i == m.cursor {
				cursor = "> "
			}
			fmt.Fprintf(&b, "%s%s", cursor, item.Title)
			if item.Description != "" {
				fmt.Fprintf(&b, "\t%s", item.Description)
			}
			b.WriteByte('\n')
		}
		if len(m.filtered) > limit {
			fmt.Fprintf(&b, "  ... %d more\n", len(m.filtered)-limit)
		}
	}

	b.WriteString("\nenter select  /  type filter  /  up/down move  /  q cancel\n")

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
