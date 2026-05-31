package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type TransferCompleteOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	LocalPath   string
	Alias       string
	RemotePath  string
	Size        string
	Direction   string
	ReturnLabel string
}

func ShowTransferComplete(ctx context.Context, opts TransferCompleteOptions) error {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return err
	}
	model := transferCompleteModel{
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		localPath:   opts.LocalPath,
		alias:       opts.Alias,
		remotePath:  opts.RemotePath,
		size:        opts.Size,
		direction:   opts.Direction,
		returnLabel: opts.ReturnLabel,
		width:       96,
	}
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	_, err = tea.NewProgram(model, programOptions...).Run()
	return err
}

type transferCompleteModel struct {
	noAltScreen bool
	theme       termstyle.Theme
	localPath   string
	alias       string
	remotePath  string
	size        string
	direction   string
	returnLabel string
	width       int
}

func (m transferCompleteModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m transferCompleteModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
	case tea.KeyPressMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m transferCompleteModel) View() tea.View {
	width := clamp(m.width, 64, 120)
	theme := pickerTheme{theme: m.theme}
	var b strings.Builder

	title := "SSHERPA " + strings.ToUpper(transferCompleteDirection(m.direction)) + " COMPLETE"
	b.WriteString(termstyle.PadRight(theme.logo(title), width))
	b.WriteString("\n")
	b.WriteString(theme.rule(width))
	b.WriteString("\n\n")

	b.WriteString("  ")
	b.WriteString(theme.theme.Style(termstyle.RoleSuccess, transferCompleteStatus(m.direction)))
	if m.size != "" {
		b.WriteString(theme.muted("  " + m.size))
	}
	b.WriteString("\n\n")

	for _, line := range transferDetailLines(theme, width, "Local", m.localPath) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	remote := m.remotePath
	if m.alias != "" {
		remote = m.alias + ":" + m.remotePath
	}
	for _, line := range transferDetailLines(theme, width, "Remote", remote) {
		b.WriteString(line)
		b.WriteByte('\n')
	}

	b.WriteString("\n")
	b.WriteString("  ")
	label := m.returnLabel
	if strings.TrimSpace(label) == "" {
		label = "press any key to return home"
	}
	b.WriteString(theme.muted(label))
	b.WriteString("\n")

	view := tea.NewView(b.String())
	view.AltScreen = !m.noAltScreen
	return view
}

func transferCompleteDirection(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "receive", "received":
		return "receive"
	default:
		return "send"
	}
}

func transferCompleteStatus(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "receive", "received":
		return "RECEIVED"
	default:
		return "SENT"
	}
}

func transferDetailLines(theme pickerTheme, width int, label string, value string) []string {
	labelText := theme.label(termstyle.PadRight(label+":", 8))
	available := max(12, width-13)
	wrapped := wrapPlain(value, available, 3)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		if i == 0 {
			lines = append(lines, fmt.Sprintf("  %s %s", labelText, theme.rowDesc(termstyle.Truncate(line, available), false)))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s %s", strings.Repeat(" ", 8), theme.rowDesc(termstyle.Truncate(line, available), false)))
	}
	return lines
}
