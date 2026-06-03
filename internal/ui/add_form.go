package ui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// AddAliasResult is the structured output from the Bubble Tea add-alias form.
// The CLI owns validation against sshconfig.AliasSpec and the actual write.
type AddAliasResult struct {
	Alias          string
	HostName       string
	User           string
	Port           string
	IdentityFile   string
	IdentitiesOnly bool
}

type AddAliasOptions struct {
	Input         io.Reader
	Output        io.Writer
	NoAltScreen   bool
	NoColor       bool
	Theme         termstyle.Theme
	ThemeName     string
	ThemeFile     string
	Initial       AddAliasResult
	IdentityFiles []string
}

func AddAliasForm(ctx context.Context, opts AddAliasOptions) (AddAliasResult, bool, error) {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return AddAliasResult{}, false, err
	}

	model := newAddAliasModel(opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}

	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return AddAliasResult{}, false, err
	}
	form, ok := final.(addAliasModel)
	if !ok || form.canceled {
		return AddAliasResult{}, false, nil
	}
	return form.result, true, nil
}

type addAliasStep int

const (
	addStepHost addAliasStep = iota
	addStepAlias
	addStepUser
	addStepPort
	addStepIdentity
	addStepIdentityCustom
	addStepIdentitiesOnly
	addStepReview
)

type addAliasModel struct {
	step addAliasStep

	hostBuf     string
	hostCursor  int
	hostError   string
	aliasBuf    string
	aliasCursor int
	aliasError  string
	userBuf     string
	userCursor  int
	userError   string
	portBuf     string
	portCursor  int
	portError   string
	idBuf       string
	idCursor    int
	idError     string
	idChoices   []string
	idCursorRow int
	idsOnly     bool

	canceled bool
	result   AddAliasResult

	theme       termstyle.Theme
	noAltScreen bool
	noColor     bool
	width       int
	height      int
}

func newAddAliasModel(opts AddAliasOptions, theme termstyle.Theme) addAliasModel {
	initial := opts.Initial
	if strings.TrimSpace(initial.Port) == "" {
		initial.Port = "22"
	}
	choices := addIdentityChoices(initial.IdentityFile, opts.IdentityFiles)
	return addAliasModel{
		hostBuf:     initial.HostName,
		hostCursor:  len([]rune(initial.HostName)),
		aliasBuf:    initial.Alias,
		aliasCursor: len([]rune(initial.Alias)),
		userBuf:     initial.User,
		userCursor:  len([]rune(initial.User)),
		portBuf:     initial.Port,
		portCursor:  len([]rune(initial.Port)),
		idBuf:       initial.IdentityFile,
		idCursor:    len([]rune(initial.IdentityFile)),
		idChoices:   choices,
		idCursorRow: addIdentityCursor(initial.IdentityFile, choices),
		idsOnly:     initial.IdentitiesOnly,
		theme:       theme,
		noAltScreen: opts.NoAltScreen,
		noColor:     opts.NoColor,
		width:       104,
		height:      24,
	}
}

func (m addAliasModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m addAliasModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.PasteMsg:
		return m.updatePaste(msg.String())
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		}
		switch m.step {
		case addStepHost:
			return m.updateHost(msg)
		case addStepAlias:
			return m.updateAlias(msg)
		case addStepUser:
			return m.updateUser(msg)
		case addStepPort:
			return m.updatePort(msg)
		case addStepIdentity:
			return m.updateIdentity(msg)
		case addStepIdentityCustom:
			return m.updateIdentityCustom(msg)
		case addStepIdentitiesOnly:
			return m.updateIdentitiesOnly(msg)
		case addStepReview:
			return m.updateReview(msg)
		}
	}
	return m, nil
}

func (m addAliasModel) updatePaste(value string) (tea.Model, tea.Cmd) {
	value = normalizePasteLine(value)
	if value == "" {
		return m, nil
	}
	switch m.step {
	case addStepHost:
		m.hostBuf, m.hostCursor = insertTextAtCursor(m.hostBuf, m.hostCursor, value)
		m.hostError = ""
	case addStepAlias:
		m.aliasBuf, m.aliasCursor = insertTextAtCursor(m.aliasBuf, m.aliasCursor, value)
		m.aliasError = ""
	case addStepUser:
		m.userBuf, m.userCursor = insertTextAtCursor(m.userBuf, m.userCursor, value)
		m.userError = ""
	case addStepPort:
		m.portBuf, m.portCursor = insertTextAtCursor(m.portBuf, m.portCursor, value)
		m.portError = ""
	case addStepIdentityCustom:
		m.idBuf, m.idCursor = insertTextAtCursor(m.idBuf, m.idCursor, value)
		m.idError = ""
	}
	return m, nil
}

func (m addAliasModel) updateHost(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.hostBuf, m.hostCursor, m.hostError, validateRequired("HostName"))
	m.hostBuf, m.hostCursor, m.hostError = buf, cursor, errStr
	return m.applyTextAction(action, addStepHost, addStepAlias)
}

func (m addAliasModel) updateAlias(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.aliasBuf, m.aliasCursor, m.aliasError, validateAliasInput)
	m.aliasBuf, m.aliasCursor, m.aliasError = buf, cursor, errStr
	if action == textInputAdvance && strings.TrimSpace(m.aliasBuf) == "" {
		m.aliasBuf = suggestAddAlias(m.hostBuf)
		m.aliasCursor = len([]rune(m.aliasBuf))
	}
	return m.applyTextAction(action, addStepHost, addStepUser)
}

func (m addAliasModel) updateUser(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.userBuf, m.userCursor, m.userError, validateNoNewline("User"))
	m.userBuf, m.userCursor, m.userError = buf, cursor, errStr
	return m.applyTextAction(action, addStepAlias, addStepPort)
}

func (m addAliasModel) updatePort(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.portBuf, m.portCursor, m.portError, validatePortInput)
	m.portBuf, m.portCursor, m.portError = buf, cursor, errStr
	return m.applyTextAction(action, addStepUser, addStepIdentity)
}

func (m addAliasModel) updateIdentity(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "shift+tab":
		m.step = addStepPort
	case "up", "ctrl+p":
		if m.idCursorRow > 0 {
			m.idCursorRow--
		}
	case "down", "ctrl+n":
		if m.idCursorRow < len(m.idChoices)-1 {
			m.idCursorRow++
		}
	case "enter":
		choice := m.idChoices[m.idCursorRow]
		switch choice {
		case addIdentityNone:
			m.idBuf = ""
			m.idCursor = 0
			m.idsOnly = false
			m.step = addStepIdentitiesOnly
		case addIdentityCustom:
			m.step = addStepIdentityCustom
		default:
			m.idBuf = choice
			m.idCursor = len([]rune(choice))
			m.step = addStepIdentitiesOnly
		}
	}
	return m, nil
}

func (m addAliasModel) updateIdentityCustom(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.idBuf, m.idCursor, m.idError, validateNoNewline("IdentityFile"))
	m.idBuf, m.idCursor, m.idError = buf, cursor, errStr
	return m.applyTextAction(action, addStepIdentity, addStepIdentitiesOnly)
}

func (m addAliasModel) applyTextAction(action textInputAction, back addAliasStep, next addAliasStep) (tea.Model, tea.Cmd) {
	switch action {
	case textInputCancel:
		m.canceled = true
		return m, tea.Quit
	case textInputBack:
		m.step = back
	case textInputAdvance:
		if m.step == addStepHost && strings.TrimSpace(m.aliasBuf) == "" {
			m.aliasBuf = suggestAddAlias(m.hostBuf)
			m.aliasCursor = len([]rune(m.aliasBuf))
		}
		m.step = next
	}
	return m, nil
}

func (m addAliasModel) updateIdentitiesOnly(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "shift+tab":
		m.step = addStepIdentity
	case " ", "enter":
		if msg.String() == " " {
			m.idsOnly = !m.idsOnly
			return m, nil
		}
		m.step = addStepReview
	case "left", "right", "up", "down":
		m.idsOnly = !m.idsOnly
	}
	return m, nil
}

func (m addAliasModel) updateReview(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "shift+tab":
		m.step = addStepIdentitiesOnly
	case "enter":
		m.result = AddAliasResult{
			Alias:          strings.TrimSpace(m.aliasBuf),
			HostName:       strings.TrimSpace(m.hostBuf),
			User:           strings.TrimSpace(m.userBuf),
			Port:           strings.TrimSpace(m.portBuf),
			IdentityFile:   strings.TrimSpace(m.idBuf),
			IdentitiesOnly: m.idsOnly,
		}
		return m, tea.Quit
	}
	return m, nil
}

func (m addAliasModel) View() tea.View {
	width := clamp(m.width, 64, 140)
	theme := pickerTheme{theme: m.theme}
	var body strings.Builder

	switch m.step {
	case addStepHost:
		m.viewHost(&body, theme, width)
	case addStepAlias:
		m.viewAlias(&body, theme, width)
	case addStepUser:
		m.viewUser(&body, theme, width)
	case addStepPort:
		m.viewPort(&body, theme, width)
	case addStepIdentity:
		m.viewIdentity(&body, theme, width)
	case addStepIdentityCustom:
		m.viewIdentityCustom(&body, theme, width)
	case addStepIdentitiesOnly:
		m.viewIdentitiesOnly(&body, theme, width)
	case addStepReview:
		m.viewReview(&body, theme, width)
	}

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:   "SSHERPA ADD ALIAS",
		Steps:   addStepLabels(),
		Current: int(m.step),
		Body:    workflowBodyLines(&body),
		Footer:  addFooter(m.step),
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m addAliasModel) viewHost(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Where should this SSH alias connect?"))
	b.WriteString("\n\n")
	renderInput(b, theme, "HostName", m.hostBuf, m.hostCursor, m.hostError, width)
}

func (m addAliasModel) viewAlias(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Pick the short name you want to type from the homepage."))
	b.WriteString("\n\n")
	renderInput(b, theme, "Alias", m.aliasBuf, m.aliasCursor, m.aliasError, width)
}

func (m addAliasModel) viewUser(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Optional login user. Leave empty to let OpenSSH decide."))
	b.WriteString("\n\n")
	renderInput(b, theme, "User", m.userBuf, m.userCursor, m.userError, width)
}

func (m addAliasModel) viewPort(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("SSH port. Use 22 unless the server listens somewhere else."))
	b.WriteString("\n\n")
	renderInput(b, theme, "Port", m.portBuf, m.portCursor, m.portError, width)
}

func (m addAliasModel) viewIdentity(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Choose an identity file, or pick none to use OpenSSH defaults."))
	b.WriteString("\n\n")
	maxLines := clamp(m.height-10, 5, 14)
	from, to := windowRange(m.idCursorRow, len(m.idChoices), maxLines)
	for i := from; i < to; i++ {
		b.WriteString("  ")
		b.WriteString(identityChoiceLine(m.idChoices[i], i == m.idCursorRow, width, theme))
		b.WriteByte('\n')
	}
}

func identityChoiceLine(choice string, selected bool, width int, theme pickerTheme) string {
	label, desc := identityChoiceText(choice)
	cursor := "  "
	if selected {
		cursor = "> "
	}
	available := max(24, width-8)
	labelWidth := clamp(available/2, 18, 42)
	descWidth := max(0, available-len(cursor)-labelWidth-2)

	line := cursor + termstyle.PadRight(termstyle.Truncate(label, labelWidth), labelWidth)
	if desc != "" && descWidth >= 8 {
		line += "  " + theme.rowDesc(termstyle.Truncate(desc, descWidth), false)
	}
	if selected {
		line = theme.rowTitle(line, true)
	}
	return line
}

func identityChoiceText(choice string) (label string, desc string) {
	switch choice {
	case addIdentityNone:
		return "(none)", "do not write IdentityFile"
	case addIdentityCustom:
		return "Custom path...", "type another key path"
	default:
		return choice, "write IdentityFile"
	}
}

func (m addAliasModel) viewIdentityCustom(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Type a custom identity file path. Leave empty for none."))
	b.WriteString("\n\n")
	renderInput(b, theme, "IdentityFile", m.idBuf, m.idCursor, m.idError, width)
}

func (m addAliasModel) viewIdentitiesOnly(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Limit authentication to this identity file?"))
	b.WriteString("\n\n  ")
	box := "[ ]"
	if m.idsOnly {
		box = "[x]"
	}
	b.WriteString(theme.rowTitle(box+" IdentitiesOnly yes", true))
	b.WriteByte('\n')
	if strings.TrimSpace(m.idBuf) == "" {
		b.WriteString("  ")
		b.WriteString(theme.muted("No identity file is set, so this can usually stay off."))
		b.WriteByte('\n')
	}
	_ = width
}

func (m addAliasModel) viewReview(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Review the Host stanza before saving:"))
	b.WriteString("\n\n")
	previewKVLine(b, theme, "alias", strings.TrimSpace(m.aliasBuf))
	previewKVLine(b, theme, "HostName", strings.TrimSpace(m.hostBuf))
	if strings.TrimSpace(m.userBuf) != "" {
		previewKVLine(b, theme, "User", strings.TrimSpace(m.userBuf))
	}
	if strings.TrimSpace(m.portBuf) != "" {
		previewKVLine(b, theme, "Port", strings.TrimSpace(m.portBuf))
	}
	if strings.TrimSpace(m.idBuf) != "" {
		previewKVLine(b, theme, "IdentityFile", strings.TrimSpace(m.idBuf))
		previewKVLine(b, theme, "IdentitiesOnly", yesNo(m.idsOnly))
	}
	_ = width
}

func addStepLabels() []string {
	return []string{"host", "alias", "user", "port", "identity", "custom", "auth", "review"}
}

func addFooter(step addAliasStep) string {
	switch step {
	case addStepIdentity:
		return "enter select  /  up/down move  /  shift+tab back  /  esc cancel"
	case addStepIdentityCustom:
		return "enter advance  /  shift+tab back to identity choices  /  type to edit  /  esc cancel"
	case addStepIdentitiesOnly:
		return "space toggle  /  enter review  /  shift+tab back  /  esc cancel"
	case addStepReview:
		return "enter save  /  shift+tab back  /  esc cancel"
	default:
		return "enter advance  /  shift+tab back  /  type to edit  /  esc cancel"
	}
}

func validateRequired(name string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
		return validateNoNewline(name)(value)
	}
}

func normalizePasteLine(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.Join(strings.Fields(value), " ")
}

func validateAliasInput(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("alias is required")
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("alias cannot contain whitespace")
	}
	return nil
}

func validateNoNewline(name string) func(string) error {
	return func(value string) error {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s cannot contain a newline", name)
		}
		return nil
	}
}

func validatePortInput(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("Port must be an integer from 1 to 65535")
	}
	return nil
}

const (
	addIdentityNone   = "\x00none"
	addIdentityCustom = "\x00custom"
)

func addIdentityChoices(initial string, discovered []string) []string {
	seen := map[string]bool{}
	choices := []string{addIdentityNone}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		choices = append(choices, value)
	}
	add(initial)
	sort.Strings(discovered)
	for _, value := range discovered {
		add(value)
	}
	choices = append(choices, addIdentityCustom)
	return choices
}

func addIdentityCursor(initial string, choices []string) int {
	initial = strings.TrimSpace(initial)
	if initial == "" {
		return 0
	}
	for i, choice := range choices {
		if choice == initial {
			return i
		}
	}
	return 0
}

func suggestAddAlias(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if i := strings.Index(host, "@"); i >= 0 && i+1 < len(host) {
		host = host[i+1:]
	}
	host = strings.Trim(host, "[]")
	if i := strings.Index(host, ":"); i > 0 {
		host = host[:i]
	}
	parts := strings.Split(host, ".")
	if parts[0] != "" {
		return parts[0]
	}
	return host
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
