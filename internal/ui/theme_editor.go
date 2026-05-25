package ui

import (
	"context"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type ThemeEditorOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Config      termstyle.ThemeConfig
	ConfigPath  string
	ThemeName   string
	Warning     string
}

type ThemeEditorResult struct {
	Config termstyle.ThemeConfig
	Theme  termstyle.Theme
	Path   string
}

func EditTheme(ctx context.Context, opts ThemeEditorOptions) (ThemeEditorResult, bool, error) {
	model := newThemeEditorModel(opts)
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
		return ThemeEditorResult{}, false, err
	}
	editor, ok := finalModel.(themeEditorModel)
	if !ok || editor.canceled || !editor.saved {
		return ThemeEditorResult{}, false, nil
	}
	cfg := editor.config()
	return ThemeEditorResult{
		Config: cfg,
		Theme:  editor.currentTheme(),
		Path:   editor.configPath,
	}, true, nil
}

type themeEditorModel struct {
	base        string
	values      map[termstyle.Role]string
	cursor      int
	noAltScreen bool
	noColor     bool
	configPath  string
	warning     string
	message     string
	editMode    bool
	editBuffer  string
	saved       bool
	canceled    bool
	width       int
	height      int
}

type themeRoleMeta struct {
	Role        termstyle.Role
	Label       string
	Description string
}

type stylePreset struct {
	Label string
	Spec  string
}

func newThemeEditorModel(opts ThemeEditorOptions) themeEditorModel {
	base := strings.TrimSpace(opts.Config.BaseName)
	if base == "" {
		base = strings.TrimSpace(opts.ThemeName)
	}
	if _, ok := termstyle.BuiltinTheme(base); !ok {
		base = "terminal"
	}
	values := make(map[termstyle.Role]string)
	for role, spec := range opts.Config.Specs {
		if strings.TrimSpace(spec) != "" {
			values[role] = strings.TrimSpace(spec)
		}
	}
	for role, code := range opts.Config.Codes {
		if _, ok := values[role]; !ok && strings.TrimSpace(code) != "" {
			values[role] = strings.TrimSpace(code)
		}
	}
	return themeEditorModel{
		base:        base,
		values:      values,
		noAltScreen: opts.NoAltScreen,
		noColor:     opts.NoColor,
		configPath:  opts.ConfigPath,
		warning:     opts.Warning,
		width:       104,
		height:      28,
	}
}

func (m themeEditorModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m themeEditorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		if m.editMode {
			return m.updateEdit(msg)
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.canceled = true
			return m, tea.Quit
		case "s", "ctrl+s":
			m.saved = true
			return m, tea.Quit
		case "up", "ctrl+p", "shift+tab":
			m.move(-1)
		case "down", "ctrl+n", "tab":
			m.move(1)
		case "left", "h":
			m.cycleCurrent(-1)
		case "right", "l":
			m.cycleCurrent(1)
		case "b":
			m.cycleBase(1)
		case "e", "enter":
			m.startEdit()
		case "d", "delete":
			m.clearCurrent()
		case "r":
			m.resetAll()
		}
	}
	return m, nil
}

func (m themeEditorModel) updateEdit(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.canceled = true
		return m, tea.Quit
	case "esc":
		m.editMode = false
		m.editBuffer = ""
		m.message = "edit cancelled"
	case "enter":
		if _, err := termstyle.ParseStyleSpec(m.editBuffer); err != nil {
			m.message = err.Error()
			return m, nil
		}
		if role, ok := m.selectedRole(); ok {
			m.values[role] = strings.TrimSpace(m.editBuffer)
			m.message = "updated " + string(role)
		}
		m.editMode = false
	case "backspace":
		if m.editBuffer != "" {
			runes := []rune(m.editBuffer)
			m.editBuffer = string(runes[:len(runes)-1])
		}
	case "ctrl+u":
		m.editBuffer = ""
	default:
		if msg.Key().Text != "" {
			m.editBuffer += msg.Key().Text
		}
	}
	return m, nil
}

func (m themeEditorModel) View() tea.View {
	width := clamp(m.width, 64, 140)
	theme := pickerTheme{theme: m.currentTheme()}
	var b strings.Builder

	title := theme.logo("SSHERPA THEME BUILDER")
	b.WriteString(termstyle.PadRight(title, width))
	b.WriteByte('\n')
	if m.configPath != "" {
		b.WriteString("  ")
		b.WriteString(theme.summary(termstyle.Truncate("config  "+m.configPath, width-4)))
		b.WriteByte('\n')
	}
	if m.warning != "" {
		b.WriteString("  ")
		b.WriteString(theme.empty(termstyle.Truncate(m.warning, width-4)))
		b.WriteByte('\n')
	}
	if m.message != "" {
		b.WriteString("  ")
		b.WriteString(theme.summary(termstyle.Truncate(m.message, width-4)))
		b.WriteByte('\n')
	}
	b.WriteString(theme.rule(width))
	b.WriteByte('\n')

	body := m.renderBody(width, theme)
	for _, line := range body {
		b.WriteString(line)
		b.WriteByte('\n')
	}

	footer := "s save  /  arrows change  /  e edit raw  /  d inherit  /  r reset  /  q cancel"
	if m.editMode {
		footer = "Enter accept  /  Esc cancel  /  Backspace edit  /  Ctrl-U clear"
	}
	b.WriteString(theme.rule(width))
	b.WriteByte('\n')
	b.WriteString(theme.footer(termstyle.Truncate(footer, width)))
	b.WriteByte('\n')

	view := tea.NewView(b.String())
	view.AltScreen = !m.noAltScreen
	return view
}

func (m themeEditorModel) renderBody(width int, theme pickerTheme) []string {
	editorWidth := width
	previewWidth := 0
	if width >= 104 {
		editorWidth = 58
		previewWidth = width - editorWidth - 3
	}

	editor := m.renderEditorLines(editorWidth, theme)
	if previewWidth <= 0 {
		return append(editor, append([]string{""}, m.renderPreviewLines(width, theme)...)...)
	}
	preview := m.renderPreviewLines(previewWidth, theme)
	lines := max(len(editor), len(preview))
	out := make([]string, 0, lines)
	divider := theme.muted("|")
	for i := 0; i < lines; i++ {
		left := ""
		if i < len(editor) {
			left = editor[i]
		}
		right := ""
		if i < len(preview) {
			right = preview[i]
		}
		out = append(out, termstyle.PadRight(left, editorWidth)+" "+divider+" "+right)
	}
	return out
}

func (m themeEditorModel) renderEditorLines(width int, theme pickerTheme) []string {
	lines := []string{
		theme.groupHeader("Schema", width),
		m.renderBaseRow(width, theme),
	}
	for i, meta := range themeRoles {
		lines = append(lines, m.renderRoleRow(i+1, meta, width, theme))
	}
	if m.editMode {
		lines = append(lines, "")
		prompt := "raw " + m.selectedLabel() + " = " + m.editBuffer
		lines = append(lines, theme.search(termstyle.Truncate(prompt, width)))
	}
	return lines
}

func (m themeEditorModel) renderBaseRow(width int, theme pickerTheme) string {
	cursor := "  "
	labelText := theme.muted("base")
	valueText := theme.rowDesc(m.base, false)
	if m.cursor == 0 {
		cursor = ">>"
		labelText = theme.rowTitle("base", true)
		valueText = theme.rowTitle(m.base, true)
	}
	line := theme.cursor(cursor, m.cursor == 0) + " " +
		termstyle.PadRight(labelText, 13) + " " +
		valueText
	return termstyle.PadRight(line, width)
}

func (m themeEditorModel) renderRoleRow(index int, meta themeRoleMeta, width int, theme pickerTheme) string {
	selected := m.cursor == index
	cursor := "  "
	if selected {
		cursor = ">>"
	}
	label := meta.Label
	spec := m.values[meta.Role]
	value := "(inherit)"
	valueText := theme.muted(value)
	if strings.TrimSpace(spec) != "" {
		value = spec
		valueText = m.currentTheme().Style(meta.Role, termstyle.Truncate(value, 18))
	}
	if selected {
		valueText = theme.rowTitle(termstyle.Truncate(value, 18), true)
	}
	line := theme.cursor(cursor, selected) + " " +
		termstyle.PadRight(theme.rowTitle(label, selected), 13) + " " +
		termstyle.PadRight(termstyle.Truncate(valueText, 18), 18)
	if width >= 54 {
		line += " " + theme.muted(termstyle.Truncate(meta.Description, width-termstyle.VisibleWidth(line)-1))
	}
	return termstyle.PadRight(line, width)
}

func (m themeEditorModel) renderPreviewLines(width int, theme pickerTheme) []string {
	sample := pickerModel{theme: m.currentTheme()}
	action := Item{Kind: ItemAdd, Token: "ADD", Title: "Add new alias", Description: "write a safe Host stanza", Badge: "add"}
	host := Item{Kind: ItemAlias, Token: "prod", Title: "prod", Description: "alice@prod.example.com:22", Badge: "host", Detail: ".ssh/config:12"}

	lines := []string{
		theme.groupHeader("Preview", width),
		theme.logo("SSHERPA") + " " + theme.pill(strings.ToUpper(m.base)),
		theme.summary(termstyle.Truncate("2 hosts  0 warning(s)  1 active session(s)", width)),
		theme.rule(width),
		theme.label("FILTER") + "  " + theme.search("[prod                ]") + "  " + theme.counter("3/8"),
		theme.groupHeader("Actions", width),
		sample.renderRow(action, true, width, theme),
		theme.groupHeader("Hosts", width),
		sample.renderRow(host, false, width, theme),
		"",
		theme.groupHeader("Overlay", width),
		theme.logo("ssherpa session map"),
		m.currentTheme().Style(termstyle.RoleSuccess, "+- prod [active] current"),
		theme.muted("Ctrl-]/q/Esc close   r refresh"),
	}
	return lines
}

func (m themeEditorModel) config() termstyle.ThemeConfig {
	cfg := termstyle.ThemeConfig{
		BaseName: m.base,
		Codes:    make(map[termstyle.Role]string),
		Specs:    make(map[termstyle.Role]string),
	}
	for _, meta := range themeRoles {
		spec := strings.TrimSpace(m.values[meta.Role])
		if spec == "" {
			continue
		}
		code, err := termstyle.ParseStyleSpec(spec)
		if err != nil {
			continue
		}
		cfg.Codes[meta.Role] = code
		cfg.Specs[meta.Role] = spec
	}
	return cfg
}

func (m themeEditorModel) currentTheme() termstyle.Theme {
	theme, ok := termstyle.BuiltinTheme(m.base)
	if !ok {
		theme = termstyle.TerminalTheme()
	}
	theme = theme.Normalized()
	theme.NoColor = m.noColor
	for _, meta := range themeRoles {
		spec := strings.TrimSpace(m.values[meta.Role])
		if spec == "" {
			continue
		}
		code, err := termstyle.ParseStyleSpec(spec)
		if err == nil {
			theme.Codes[meta.Role] = code
		}
	}
	return theme
}

func (m *themeEditorModel) move(delta int) {
	rows := len(themeRoles) + 1
	m.cursor = (m.cursor + delta + rows) % rows
	m.message = ""
}

func (m *themeEditorModel) cycleCurrent(delta int) {
	if m.cursor == 0 {
		m.cycleBase(delta)
		return
	}
	role, ok := m.selectedRole()
	if !ok {
		return
	}
	current := strings.TrimSpace(m.values[role])
	index := presetIndex(current)
	next := (index + delta + len(stylePresets)) % len(stylePresets)
	m.values[role] = stylePresets[next].Spec
	m.message = string(role) + " = " + stylePresets[next].Label
}

func (m *themeEditorModel) cycleBase(delta int) {
	index := 0
	for i, base := range themeBases {
		if base == m.base {
			index = i
			break
		}
	}
	index = (index + delta + len(themeBases)) % len(themeBases)
	m.base = themeBases[index]
	m.message = "base = " + m.base
}

func (m *themeEditorModel) startEdit() {
	if m.cursor == 0 {
		m.cycleBase(1)
		return
	}
	role, ok := m.selectedRole()
	if !ok {
		return
	}
	m.editMode = true
	m.editBuffer = m.values[role]
	m.message = "editing " + string(role)
}

func (m *themeEditorModel) clearCurrent() {
	if role, ok := m.selectedRole(); ok {
		delete(m.values, role)
		m.message = string(role) + " inherits from " + m.base
	}
}

func (m *themeEditorModel) resetAll() {
	m.base = "terminal"
	m.values = make(map[termstyle.Role]string)
	m.message = "reset to terminal defaults"
}

func (m themeEditorModel) selectedRole() (termstyle.Role, bool) {
	index := m.cursor - 1
	if index < 0 || index >= len(themeRoles) {
		return "", false
	}
	return themeRoles[index].Role, true
}

func (m themeEditorModel) selectedLabel() string {
	if m.cursor == 0 {
		return "base"
	}
	index := m.cursor - 1
	if index < 0 || index >= len(themeRoles) {
		return ""
	}
	return themeRoles[index].Label
}

func presetIndex(spec string) int {
	spec = normalizeSpec(spec)
	for i, preset := range stylePresets {
		if normalizeSpec(preset.Spec) == spec {
			return i
		}
	}
	return 0
}

func normalizeSpec(spec string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(spec)), " "))
}

var themeBases = []string{"terminal", "vivid"}

var themeRoles = []themeRoleMeta{
	{Role: termstyle.RoleTitle, Label: "title", Description: "app name and overlay titles"},
	{Role: termstyle.RolePrimary, Label: "primary", Description: "main brand/action color"},
	{Role: termstyle.RoleSecondary, Label: "secondary", Description: "summary and supporting emphasis"},
	{Role: termstyle.RoleAccent, Label: "accent", Description: "labels and group headers"},
	{Role: termstyle.RoleMuted, Label: "muted", Description: "help text and inactive rows"},
	{Role: termstyle.RoleSubtle, Label: "subtle", Description: "low-contrast dividers"},
	{Role: termstyle.RoleForeground, Label: "foreground", Description: "normal row text"},
	{Role: termstyle.RoleSelected, Label: "selected", Description: "selected row title"},
	{Role: termstyle.RoleBorder, Label: "border", Description: "rules and separators"},
	{Role: termstyle.RoleSuccess, Label: "success", Description: "active sessions and host badges"},
	{Role: termstyle.RoleWarning, Label: "warning", Description: "edit/warning status"},
	{Role: termstyle.RoleDanger, Label: "danger", Description: "proxy/delete risk color"},
	{Role: termstyle.RoleInfo, Label: "info", Description: "jump and informational badges"},
	{Role: termstyle.RoleSearch, Label: "search", Description: "typed filter and raw entry"},
	{Role: termstyle.RolePill, Label: "pill", Description: "mode badge at top of screen"},
}

var stylePresets = []stylePreset{
	{Label: "inherit", Spec: ""},
	{Label: "default", Spec: "default"},
	{Label: "black", Spec: "black"},
	{Label: "red", Spec: "red"},
	{Label: "green", Spec: "green"},
	{Label: "yellow", Spec: "yellow"},
	{Label: "blue", Spec: "blue"},
	{Label: "magenta", Spec: "magenta"},
	{Label: "cyan", Spec: "cyan"},
	{Label: "white", Spec: "white"},
	{Label: "bright black", Spec: "bright-black"},
	{Label: "bright red", Spec: "bright-red"},
	{Label: "bright green", Spec: "bright-green"},
	{Label: "bright yellow", Spec: "bright-yellow"},
	{Label: "bright blue", Spec: "bright-blue"},
	{Label: "bright magenta", Spec: "bright-magenta"},
	{Label: "bright cyan", Spec: "bright-cyan"},
	{Label: "bright white", Spec: "bright-white"},
	{Label: "bold default", Spec: "bold default"},
	{Label: "bold red", Spec: "bold red"},
	{Label: "bold green", Spec: "bold green"},
	{Label: "bold yellow", Spec: "bold yellow"},
	{Label: "bold blue", Spec: "bold blue"},
	{Label: "bold magenta", Spec: "bold magenta"},
	{Label: "bold cyan", Spec: "bold cyan"},
	{Label: "bold white", Spec: "bold white"},
	{Label: "dim", Spec: "dim"},
	{Label: "underline", Spec: "underline"},
	{Label: "reverse", Spec: "reverse"},
	{Label: "bold reverse", Spec: "bold reverse"},
	{Label: "plain", Spec: "plain"},
}
