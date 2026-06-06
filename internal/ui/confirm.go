package ui

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type ConfirmOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Message     string
	Danger      bool
}

func Confirm(ctx context.Context, opts ConfirmOptions) (bool, bool, error) {
	return runConfirm(ctx, opts)
}

func ConfirmDelete(ctx context.Context, opts ConfirmOptions) (bool, bool, error) {
	opts.Danger = true
	return runConfirm(ctx, opts)
}

func runConfirm(ctx context.Context, opts ConfirmOptions) (bool, bool, error) {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return false, false, err
	}
	model := newConfirmModel(opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}

	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return false, false, err
	}
	confirm, ok := final.(confirmModel)
	if !ok || confirm.canceled {
		return false, false, nil
	}
	return confirm.selectedYes, true, nil
}

type confirmModel struct {
	title       string
	message     string
	selectedYes bool
	answered    bool
	canceled    bool
	noAltScreen bool
	danger      bool
	theme       termstyle.Theme
	width       int
	height      int
}

func newConfirmModel(opts ConfirmOptions, theme termstyle.Theme) confirmModel {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "Confirm"
	}
	return confirmModel{
		title:       title,
		message:     strings.TrimSpace(opts.Message),
		selectedYes: !opts.Danger,
		noAltScreen: opts.NoAltScreen,
		danger:      opts.Danger,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		width:       72,
		height:      12,
	}
}

func (m confirmModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "ctrl+c", "esc", "Q":
			m.canceled = true
			return m, tea.Quit
		case "left", "right", "tab", "shift+tab", "h", "l":
			m.selectedYes = !m.selectedYes
		case "y", "Y":
			m.selectedYes = true
			m.answered = true
			return m, tea.Quit
		case "n", "N":
			m.selectedYes = false
			m.answered = true
			return m, tea.Quit
		case "enter", " ":
			m.answered = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) View() tea.View {
	width := clamp(m.width, 48, 96)
	innerWidth := width - 8
	theme := pickerTheme{theme: m.theme}
	var body strings.Builder

	if m.message != "" {
		for _, line := range wrapConfirmText(m.message, innerWidth) {
			body.WriteString("  ")
			body.WriteString(theme.theme.Style(termstyle.RoleForeground, line))
			body.WriteString("\n")
		}
		body.WriteString("\n")
	}
	body.WriteString("  ")
	body.WriteString(confirmButton(theme, "Yes", m.selectedYes, m.danger))
	body.WriteString("  ")
	body.WriteString(confirmButton(theme, "No", !m.selectedYes, false))
	body.WriteString("\n")

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:  m.title,
		Body:   workflowBodyLines(&body),
		Footer: "enter confirm  /  left-right choose  /  esc cancel",
		Danger: m.danger,
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func confirmButton(theme pickerTheme, label string, selected bool, danger bool) string {
	text := " " + label + " "
	if selected {
		return theme.theme.Style(termstyle.RoleSelected, "["+text+"]")
	}
	role := termstyle.RoleSecondary
	if danger {
		role = termstyle.RoleDanger
	}
	return theme.theme.Style(role, " "+text+" ")
}

func wrapConfirmText(value string, width int) []string {
	if width <= 0 {
		return []string{value}
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len([]rune(line))+1+len([]rune(word)) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	lines = append(lines, line)
	return lines
}
