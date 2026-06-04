package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type TextViewOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Steps       []string
	CurrentStep int
	Summary     string
	Lines       []string
	Footer      string
}

func ShowTextView(ctx context.Context, opts TextViewOptions) error {
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
	model := newTextViewModel(opts, theme)
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

type textViewModel struct {
	noAltScreen bool
	theme       termstyle.Theme
	title       string
	steps       []string
	currentStep int
	summary     string
	lines       []string
	footer      string
	scroll      int
	width       int
	height      int
}

func newTextViewModel(opts TextViewOptions, theme termstyle.Theme) textViewModel {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "ssherpa"
	}
	footer := strings.TrimSpace(opts.Footer)
	if footer == "" {
		footer = "up/down scroll  /  pgup/pgdn page  /  q back"
	}
	return textViewModel{
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		title:       title,
		steps:       append([]string(nil), opts.Steps...),
		currentStep: opts.CurrentStep,
		summary:     strings.TrimSpace(opts.Summary),
		lines:       append([]string(nil), opts.Lines...),
		footer:      footer,
		width:       96,
		height:      28,
	}
}

func (m textViewModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m textViewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		page := max(1, m.bodyBudget()-2)
		switch msg.String() {
		case "q", "Q", "esc", "enter":
			return m, tea.Quit
		case "up", "ctrl+p":
			m.scroll--
		case "down", "ctrl+n":
			m.scroll++
		case "pgup":
			m.scroll -= page
		case "pgdown":
			m.scroll += page
		case "home":
			m.scroll = 0
		case "end":
			m.scroll = m.maxScroll()
		}
		m.normalizeScroll()
	}
	return m, nil
}

func (m textViewModel) View() tea.View {
	width := max(56, m.width)
	theme := pickerTheme{theme: m.theme}

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:   m.title,
		Steps:   m.steps,
		Current: m.currentStep,
		Body:    m.renderBody(max(20, width-4), theme),
		Footer:  m.footer,
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m textViewModel) renderBody(width int, theme pickerTheme) []string {
	lines := []string{}
	if m.summary != "" {
		lines = append(lines, textViewKVLine(theme, width, "Summary", m.summary))
		lines = append(lines, "")
	}
	lines = append(lines, theme.groupHeader("Details", width))

	wrapped := m.wrappedLinesForWidth(width)
	if len(wrapped) == 0 {
		lines = append(lines, "  "+theme.empty("No details to show."))
		return lines
	}

	budget := m.bodyBudget()
	start := clamp(m.scroll, 0, max(0, len(wrapped)-1))
	if start > 0 && budget > 0 {
		lines = append(lines, "  "+theme.muted(stringCountLabel(start, "line")+" above"))
		budget--
	}
	end := min(len(wrapped), start+budget)
	if end < len(wrapped) && end > start {
		end--
	}
	for _, line := range wrapped[start:end] {
		lines = append(lines, "  "+theme.foreground(termstyle.Truncate(line, max(1, width-2))))
	}
	if end < len(wrapped) {
		lines = append(lines, "  "+theme.muted(stringCountLabel(len(wrapped)-end, "line")+" below"))
	}
	return lines
}

func (m textViewModel) bodyBudget() int {
	budget := m.height - 7
	if m.summary != "" {
		budget -= 2
	}
	if len(m.steps) > 0 {
		budget -= 2
	}
	if budget < 4 {
		return 4
	}
	return budget
}

func (m *textViewModel) normalizeScroll() {
	m.scroll = clamp(m.scroll, 0, m.maxScroll())
}

func (m textViewModel) maxScroll() int {
	wrapped := m.wrappedLines()
	page := m.bodyBudget()
	if page <= 1 {
		return max(0, len(wrapped)-1)
	}
	return max(0, len(wrapped)-(page-1))
}

func (m textViewModel) wrappedLines() []string {
	return m.wrappedLinesForWidth(max(20, m.width-4))
}

func (m textViewModel) wrappedLinesForWidth(width int) []string {
	lineWidth := max(8, width-2)
	out := make([]string, 0, len(m.lines))
	for _, line := range m.lines {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapTextViewLine(line, lineWidth)...)
	}
	return out
}

func wrapTextViewLine(line string, width int) []string {
	if width <= 0 {
		return nil
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	current := ""
	for _, word := range words {
		for len([]rune(word)) > width {
			chunk := string([]rune(word)[:width])
			word = string([]rune(word)[width:])
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			lines = append(lines, chunk)
		}
		if current == "" {
			current = word
			continue
		}
		if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func textViewKVLine(theme pickerTheme, width int, label string, value string) string {
	labelWidth := 8
	valueWidth := max(8, width-2-labelWidth-2)
	labelText := theme.label(termstyle.PadRight(label, labelWidth))
	valueText := theme.summary(termstyle.Truncate(value, valueWidth))
	return labelText + "  " + valueText
}

func stringCountLabel(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
