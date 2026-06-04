package ui

import (
	"context"
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
	width := max(64, m.width)
	theme := pickerTheme{theme: m.theme}
	title := "SSHERPA " + strings.ToUpper(transferCompleteDirection(m.direction)) + " COMPLETE"

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:   title,
		Steps:   transferCompleteSteps(m.direction),
		Current: 4,
		Body:    m.renderBody(max(20, width-4), theme),
		Footer:  transferCompleteFooter(m.returnLabel),
	}))
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

func transferCompleteSteps(direction string) []string {
	switch transferCompleteDirection(direction) {
	case "receive":
		return []string{"direction", "host", "remote", "local", "complete"}
	default:
		return []string{"direction", "local", "host", "remote", "complete"}
	}
}

func transferCompleteFooter(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "press any key to return home"
	}
	return label
}

func (m transferCompleteModel) renderBody(width int, theme pickerTheme) []string {
	width = max(20, width)
	localPath := strings.TrimSpace(m.localPath)
	remotePath := strings.TrimSpace(m.remotePath)
	remoteLabel := transferCompleteRemoteLabel(m.alias)
	sourceLabel, sourceScope, sourcePath := transferCompleteSource(m.direction, remoteLabel, remotePath, localPath)
	targetLabel, targetScope, targetPath := transferCompleteTarget(m.direction, remoteLabel, remotePath, localPath)

	lines := []string{
		transferCompleteStatusLine(theme, width, m.direction, m.size),
		"",
		theme.groupHeader("Transfer", width),
	}
	lines = append(lines, transferCompleteEndpointLines(theme, width, sourceLabel, sourceScope, sourcePath)...)
	lines = append(lines, transferCompleteEndpointLines(theme, width, targetLabel, targetScope, targetPath)...)
	return lines
}

func transferCompleteStatusLine(theme pickerTheme, width int, direction string, size string) string {
	status := theme.theme.Style(termstyle.RoleSuccess, "✓ "+transferCompleteStatus(direction))
	if strings.TrimSpace(size) != "" {
		status += theme.muted("  " + strings.TrimSpace(size))
	}
	return transferCompleteKVLineStyled(theme, width, "Status", status)
}

func transferCompleteSource(direction string, remoteLabel string, remotePath string, localPath string) (string, string, string) {
	if transferCompleteDirection(direction) == "receive" {
		return "Source", remoteLabel, remotePath
	}
	return "Source", "local", localPath
}

func transferCompleteTarget(direction string, remoteLabel string, remotePath string, localPath string) (string, string, string) {
	if transferCompleteDirection(direction) == "receive" {
		return "Target", "local", localPath
	}
	return "Target", remoteLabel, remotePath
}

func transferCompleteRemoteLabel(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "remote"
	}
	return alias
}

func transferCompleteEndpointLines(theme pickerTheme, width int, label string, scope string, value string) []string {
	labelWidth := 8
	scopeWidth := clamp(width/6, 7, 16)
	valueWidth := max(8, width-2-labelWidth-2-scopeWidth-2)
	wrapped := wrapPlain(value, valueWidth, 3)
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		labelText := strings.Repeat(" ", labelWidth)
		scopeText := strings.Repeat(" ", scopeWidth)
		if i == 0 {
			labelText = theme.label(termstyle.PadRight(label, labelWidth))
			scopeText = theme.summary(termstyle.PadRight(termstyle.Truncate(scope, scopeWidth), scopeWidth))
		}
		lines = append(lines, "  "+labelText+"  "+scopeText+"  "+theme.rowDesc(termstyle.Truncate(line, valueWidth), false))
	}
	return lines
}

func transferCompleteKVLineStyled(theme pickerTheme, width int, label string, value string) string {
	labelWidth := 8
	valueWidth := max(8, width-2-labelWidth-2)
	labelText := theme.label(termstyle.PadRight(label, labelWidth))
	return "  " + labelText + "  " + truncateStyled(value, valueWidth)
}
