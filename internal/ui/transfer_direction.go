package ui

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

const (
	TransferDirectionSend    = "send"
	TransferDirectionReceive = "receive"
)

type TransferDirectionOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
}

func ChooseTransferDirection(ctx context.Context, opts TransferDirectionOptions) (string, bool, error) {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return "", false, err
	}
	model := transferDirectionModel{
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		width:       88,
		height:      18,
	}
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}

	finalModel, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return "", false, err
	}
	direction, ok := finalModel.(transferDirectionModel)
	if !ok || direction.canceled || direction.selected == "" {
		return "", false, nil
	}
	return direction.selected, true, nil
}

type transferDirectionModel struct {
	noAltScreen bool
	theme       termstyle.Theme
	cursor      int
	selected    string
	canceled    bool
	width       int
	height      int
}

func (m transferDirectionModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m transferDirectionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "ctrl+c", "esc", "Q", "q":
			m.canceled = true
			return m, tea.Quit
		case "enter":
			m.selected = transferDirectionAt(m.cursor)
			return m, tea.Quit
		case "s", "S":
			m.selected = TransferDirectionSend
			return m, tea.Quit
		case "r", "R":
			m.selected = TransferDirectionReceive
			return m, tea.Quit
		case "up", "left", "shift+tab":
			m.cursor = 0
		case "down", "right", "tab":
			m.cursor = 1
		}
	}
	return m, nil
}

func (m transferDirectionModel) View() tea.View {
	width := max(60, m.width)
	theme := pickerTheme{theme: m.theme}

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:   "SSHERPA FILE TRANSFER",
		Steps:   transferDirectionSteps(),
		Current: 0,
		Body:    m.renderBody(max(20, width-4), theme),
		Footer:  "enter select  /  s send  /  r receive  /  arrows move  /  Q back",
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m transferDirectionModel) renderBody(width int, theme pickerTheme) []string {
	lines := []string{
		transferDirectionMetaLine(width, theme),
		"",
		theme.groupHeader("Direction", width),
	}
	for i, option := range transferDirectionOptions() {
		lines = append(lines, transferDirectionRow(option, i == m.cursor, width, theme))
	}
	if width >= 78 {
		lines = append(lines, "", transferDirectionPreviewLine(transferDirectionOptions()[m.cursor], width, theme))
	}
	return lines
}

func transferDirectionSteps() []string {
	return []string{"direction", "source", "host", "target", "run"}
}

type transferDirectionChoice struct {
	Direction string
	Badge     string
	Title     string
	Flow      string
	Detail    string
}

func transferDirectionOptions() []transferDirectionChoice {
	return []transferDirectionChoice{
		{
			Direction: TransferDirectionSend,
			Badge:     "send",
			Title:     "Send file",
			Flow:      "local -> remote",
			Detail:    "choose local file, host, then remote folder",
		},
		{
			Direction: TransferDirectionReceive,
			Badge:     "recv",
			Title:     "Receive file",
			Flow:      "remote -> local",
			Detail:    "choose host, remote file, then local folder",
		},
	}
}

func transferDirectionAt(index int) string {
	choices := transferDirectionOptions()
	if index < 0 || index >= len(choices) {
		return TransferDirectionSend
	}
	return choices[index].Direction
}

func transferDirectionMetaLine(width int, theme pickerTheme) string {
	label := theme.label(termstyle.PadRight("MODE", 7))
	value := theme.summary("choose direction")
	return label + "  " + termstyle.PadRight(value, max(0, width-termstyle.VisibleWidth(label)-2))
}

func transferDirectionRow(choice transferDirectionChoice, selected bool, width int, theme pickerTheme) string {
	cursor := "  "
	if selected {
		cursor = ">>"
	}
	badgeWidth := 8
	titleWidth := clamp(width/3, 14, 24)
	if width < 68 {
		titleWidth = clamp(width/4, 12, 18)
	}
	detailWidth := max(0, width-2-2-badgeWidth-2-titleWidth-2)

	line := theme.cursor(cursor, selected) + " " +
		termstyle.PadRight(theme.badge(directionItemKind(choice.Direction), "["+strings.ToUpper(choice.Badge)+"]"), badgeWidth) + " " +
		termstyle.PadRight(theme.rowTitle(termstyle.Truncate(choice.Title, titleWidth), selected), titleWidth)
	if detailWidth > 0 {
		line += "  " + theme.rowDesc(termstyle.Truncate(choice.Flow, detailWidth), selected)
	}
	return termstyle.PadRight(line, width)
}

func transferDirectionPreviewLine(choice transferDirectionChoice, width int, theme pickerTheme) string {
	label := theme.label(termstyle.PadRight("Next", 7))
	valueWidth := max(8, width-termstyle.VisibleWidth(label)-2)
	return label + "  " + theme.rowDesc(termstyle.Truncate(choice.Detail, valueWidth), false)
}

func directionItemKind(direction string) ItemKind {
	switch direction {
	case TransferDirectionReceive:
		return ItemReceiveFile
	default:
		return ItemSendFile
	}
}
