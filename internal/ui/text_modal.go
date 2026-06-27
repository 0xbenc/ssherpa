package ui

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type TextPromptOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Label       string
	Initial     string
	Validate    func(string) error
	// Secret masks the typed value on screen (each rune shown as •). Set via
	// PromptSecret; the buffer itself is kept and returned verbatim.
	Secret bool
}

func PromptText(ctx context.Context, opts TextPromptOptions) (string, bool, error) {
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
	model := newTextPromptModel(opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return "", false, err
	}
	prompt, ok := final.(textPromptModel)
	if !ok || prompt.canceled || !prompt.done {
		return "", false, nil
	}
	return strings.TrimSpace(prompt.buf), true, nil
}

// PromptSecret is PromptText with the typed value masked on screen and returned
// verbatim — no surrounding-whitespace trimming, since a passphrase may
// legitimately contain leading or trailing spaces, and it may be empty.
func PromptSecret(ctx context.Context, opts TextPromptOptions) (string, bool, error) {
	opts.Secret = true
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
	model := newTextPromptModel(opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return "", false, err
	}
	prompt, ok := final.(textPromptModel)
	if !ok || prompt.canceled || !prompt.done {
		return "", false, nil
	}
	return prompt.buf, true, nil
}

type textPromptModel struct {
	title       string
	label       string
	buf         string
	cursor      int
	errStr      string
	validate    func(string) error
	secret      bool
	done        bool
	canceled    bool
	noAltScreen bool
	theme       termstyle.Theme
	width       int
	height      int
}

func newTextPromptModel(opts TextPromptOptions, theme termstyle.Theme) textPromptModel {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "Input"
	}
	label := strings.TrimSpace(opts.Label)
	if label == "" {
		label = "value"
	}
	return textPromptModel{
		title:       title,
		label:       label,
		buf:         opts.Initial,
		cursor:      len([]rune(opts.Initial)),
		validate:    opts.Validate,
		secret:      opts.Secret,
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		width:       72,
		height:      12,
	}
}

func (m textPromptModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m textPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.PasteMsg:
		value := normalizePasteLine(msg.String())
		if value != "" {
			m.buf, m.cursor = insertTextAtCursor(m.buf, m.cursor, value)
			m.errStr = ""
		}
	case tea.KeyPressMsg:
		action, buf, cursor, errStr := updateTextInputState(msg, m.buf, m.cursor, m.errStr, func(value string) error {
			if m.validate == nil {
				return nil
			}
			return m.validate(value)
		})
		m.buf, m.cursor, m.errStr = buf, cursor, errStr
		switch action {
		case textInputCancel:
			m.canceled = true
			return m, tea.Quit
		case textInputAdvance:
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m textPromptModel) View() tea.View {
	width := clamp(m.width, 48, 96)
	innerWidth := width - 8
	theme := pickerTheme{theme: m.theme}
	var body strings.Builder
	display := m.buf
	if m.secret {
		// Mask with one • per rune so the cursor position (rune-indexed) and
		// width still line up, while the real value never reaches the screen.
		display = strings.Repeat("•", len([]rune(m.buf)))
	}
	renderInput(&body, theme, m.label, display, m.cursor, m.errStr, innerWidth)
	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:  m.title,
		Body:   workflowBodyLines(&body),
		Footer: "enter save / type to edit / esc cancel",
	}))
	view.AltScreen = !m.noAltScreen
	return view
}
