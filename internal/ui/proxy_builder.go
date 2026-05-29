package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type ProxyResult struct {
	Alias     string
	Bind      string
	Port      int
	Action    ForwardAction
	SavedName string
}

func (r ProxyResult) ListenerSpec() string {
	if r.Bind == "" || r.Bind == sshcmd.DefaultForwardBind {
		return fmt.Sprintf("%d", r.Port)
	}
	if strings.Contains(r.Bind, ":") {
		return fmt.Sprintf("[%s]:%d", r.Bind, r.Port)
	}
	return fmt.Sprintf("%s:%d", r.Bind, r.Port)
}

type BuildProxyOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Aliases     []ForwardAlias
	Initial     ProxyResult
	EditMode    bool
}

type proxyBuilderStep int

const (
	proxyStepDestination proxyBuilderStep = iota
	proxyStepListener
	proxyStepSummary
	proxyStepSaveName
)

type proxyBuilderModel struct {
	aliases []ForwardAlias
	step    proxyBuilderStep

	destFilter  string
	destCursor  int
	destination ForwardAlias

	listenerBuf    string
	listenerCursor int
	listenerError  string

	summaryCursor  int
	saveNameBuf    string
	saveNameCursor int
	saveNameError  string

	canceled bool
	result   ProxyResult

	theme       termstyle.Theme
	noAltScreen bool
	noColor     bool
	editMode    bool
	width       int
	height      int
}

func BuildProxy(ctx context.Context, opts BuildProxyOptions) (ProxyResult, bool, error) {
	if len(opts.Aliases) == 0 {
		return ProxyResult{}, false, nil
	}
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return ProxyResult{}, false, err
	}
	model := newProxyBuilderModel(opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}
	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return ProxyResult{}, false, err
	}
	builder, ok := final.(proxyBuilderModel)
	if !ok || builder.canceled {
		return ProxyResult{}, false, nil
	}
	return builder.result, true, nil
}

func newProxyBuilderModel(opts BuildProxyOptions, theme termstyle.Theme) proxyBuilderModel {
	listener := "1080"
	destination := ForwardAlias{}
	destCursor := 0
	if opts.Initial.Alias != "" {
		destination = firstForwardAlias(opts.Aliases, opts.Initial.Alias)
		for i, alias := range opts.Aliases {
			if alias.Name == destination.Name {
				destCursor = i
				break
			}
		}
		listener = opts.Initial.ListenerSpec()
	}
	return proxyBuilderModel{
		aliases:        opts.Aliases,
		step:           proxyStepDestination,
		destCursor:     destCursor,
		destination:    destination,
		listenerBuf:    listener,
		listenerCursor: len([]rune(listener)),
		theme:          theme,
		noAltScreen:    opts.NoAltScreen,
		noColor:        opts.NoColor,
		editMode:       opts.EditMode,
		width:          104,
		height:         24,
	}
}

func (m proxyBuilderModel) Init() tea.Cmd { return tea.RequestWindowSize }

func (m proxyBuilderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.canceled = true
			return m, tea.Quit
		}
		switch m.step {
		case proxyStepDestination:
			return m.updateDestination(msg)
		case proxyStepListener:
			return m.updateListener(msg)
		case proxyStepSummary:
			return m.updateSummary(msg)
		case proxyStepSaveName:
			return m.updateSaveName(msg)
		}
	}
	return m, nil
}

func (m proxyBuilderModel) updateDestination(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	matches := m.destMatches()
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "enter":
		if len(matches) == 0 {
			return m, nil
		}
		if m.destCursor >= len(matches) {
			m.destCursor = len(matches) - 1
		}
		m.destination = m.aliases[matches[m.destCursor]]
		m.step = proxyStepListener
	case "up", "ctrl+p":
		if m.destCursor > 0 {
			m.destCursor--
		}
	case "down", "ctrl+n":
		if m.destCursor < len(matches)-1 {
			m.destCursor++
		}
	case "backspace":
		if m.destFilter != "" {
			runes := []rune(m.destFilter)
			m.destFilter = string(runes[:len(runes)-1])
			m.destCursor = 0
		}
	case "ctrl+u":
		m.destFilter = ""
		m.destCursor = 0
	default:
		if isPrintableInput(msg) {
			m.destFilter += msg.Key().Text
			m.destCursor = 0
		}
	}
	return m, nil
}

func (m proxyBuilderModel) destMatches() []int {
	if m.destFilter == "" {
		out := make([]int, len(m.aliases))
		for i := range m.aliases {
			out[i] = i
		}
		return out
	}
	var out []int
	for i, a := range m.aliases {
		if fuzzyMatch(a.Name, m.destFilter) || fuzzyMatch(a.Description, m.destFilter) {
			out = append(out, i)
		}
	}
	return out
}

func (m proxyBuilderModel) updateListener(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.listenerBuf, m.listenerCursor, m.listenerError, func(v string) error {
		bind, port, err := sshcmd.ParseForwardLocal(v)
		if err != nil {
			return err
		}
		return sshcmd.ValidateProxy(m.destination.Name, bind, port)
	})
	m.listenerBuf = buf
	m.listenerCursor = cursor
	m.listenerError = errStr
	switch action {
	case textInputCancel:
		m.canceled = true
		return m, tea.Quit
	case textInputBack:
		m.step = proxyStepDestination
	case textInputAdvance:
		m.finalizeBeforeSummary()
		m.step = proxyStepSummary
	}
	return m, nil
}

func (m *proxyBuilderModel) finalizeBeforeSummary() {
	bind, port, _ := sshcmd.ParseForwardLocal(m.listenerBuf)
	m.result = ProxyResult{Alias: m.destination.Name, Bind: bind, Port: port}
}

func (m proxyBuilderModel) updateSummary(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	actions := m.summaryActions()
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "shift+tab":
		m.step = proxyStepListener
	case "up", "ctrl+p":
		if m.summaryCursor > 0 {
			m.summaryCursor--
		}
	case "down", "ctrl+n":
		if m.summaryCursor < len(actions)-1 {
			m.summaryCursor++
		}
	case "enter":
		action := actions[m.summaryCursor].Action
		if action == ForwardActionCancel {
			m.canceled = true
			return m, tea.Quit
		}
		if action == ForwardActionSave {
			if m.saveNameBuf == "" {
				m.saveNameBuf = defaultProxySaveName(m.destination.Name)
				m.saveNameCursor = len([]rune(m.saveNameBuf))
			}
			m.step = proxyStepSaveName
			return m, nil
		}
		m.result.Action = action
		return m, tea.Quit
	}
	return m, nil
}

func (m proxyBuilderModel) updateSaveName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.saveNameBuf, m.saveNameCursor, m.saveNameError, validateSaveName)
	m.saveNameBuf = buf
	m.saveNameCursor = cursor
	m.saveNameError = errStr
	switch action {
	case textInputCancel:
		m.canceled = true
		return m, tea.Quit
	case textInputBack:
		m.step = proxyStepSummary
	case textInputAdvance:
		m.result.Action = ForwardActionSave
		m.result.SavedName = strings.TrimSpace(buf)
		return m, tea.Quit
	}
	return m, nil
}

func (m proxyBuilderModel) View() tea.View {
	width := clamp(m.width, 64, 140)
	theme := pickerTheme{theme: m.theme}
	var b strings.Builder
	title := "SSHERPA PROXY BUILDER"
	if m.editMode {
		title = "SSHERPA PROXY EDITOR"
	}
	b.WriteString(termstyle.PadRight(theme.logo(title), width))
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.muted(proxyStepBreadcrumb(m.step)))
	b.WriteString("\n\n")
	switch m.step {
	case proxyStepDestination:
		m.viewDestination(&b, theme, width)
	case proxyStepListener:
		m.viewListener(&b, theme, width)
	case proxyStepSummary:
		m.viewSummary(&b, theme, width)
	case proxyStepSaveName:
		m.viewSaveName(&b, theme, width)
	}
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.muted(proxyStepFooter(m.step)))
	b.WriteByte('\n')
	return tea.NewView(b.String())
}

func (m proxyBuilderModel) viewDestination(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Pick the SSH host that will carry the SOCKS proxy:"))
	b.WriteByte('\n')
	if m.destFilter != "" {
		b.WriteString("  filter  ")
		b.WriteString(theme.previewTitle(m.destFilter))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	matches := m.destMatches()
	maxLines := clamp(m.height-10, 6, 20)
	from, to := windowRange(m.destCursor, len(matches), maxLines)
	for i := from; i < to; i++ {
		idx := matches[i]
		alias := m.aliases[idx]
		cursor := "  "
		if i == m.destCursor {
			cursor = "> "
		}
		line := cursor + termstyle.PadRight(alias.Name, 28) + theme.rowDesc(termstyle.Truncate(alias.Description, max(0, width-32)), false)
		if i == m.destCursor {
			line = theme.rowTitle(line, true)
		}
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func (m proxyBuilderModel) viewListener(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("SOCKS listener — [BIND:]PORT (default bind is 127.0.0.1):"))
	b.WriteByte('\n')
	b.WriteString("  destination  ")
	b.WriteString(theme.previewTitle(m.destination.Name))
	b.WriteString("\n\n")
	renderInput(b, theme, "--port", m.listenerBuf, m.listenerCursor, m.listenerError, width)
}

func (m proxyBuilderModel) viewSummary(b *strings.Builder, theme pickerTheme, width int) {
	actions := m.summaryActions()
	b.WriteString("  ")
	b.WriteString(theme.summary("Review and pick an action:"))
	b.WriteString("\n\n")
	previewKVLine(b, theme, "destination", m.destination.Name)
	previewKVLine(b, theme, "listener", m.result.ListenerSpec())
	previewKVLine(b, theme, "preview", "ssh -D "+m.result.ListenerSpec()+" -C -N -o ExitOnForwardFailure=yes "+m.result.Alias)
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.summary("Actions:"))
	b.WriteByte('\n')
	for i, action := range actions {
		cursor := "  "
		if i == m.summaryCursor {
			cursor = "> "
		}
		row := cursor + action.Label
		if i == m.summaryCursor {
			row = theme.rowTitle(row, true)
		}
		b.WriteString("  ")
		b.WriteString(row)
		b.WriteByte('\n')
	}
	_ = width
}

func (m proxyBuilderModel) viewSaveName(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Save this proxy preset to ssherpa's catalog under what name?"))
	b.WriteString("\n\n")
	renderInput(b, theme, "name", m.saveNameBuf, m.saveNameCursor, m.saveNameError, width)
}

func (m proxyBuilderModel) summaryActions() []summaryAction {
	if m.editMode {
		return builderEditSummaryActions
	}
	return builderSummaryActions
}

func proxyStepBreadcrumb(step proxyBuilderStep) string {
	steps := []string{"destination", "listener", "summary"}
	if step == proxyStepSaveName {
		steps = append(steps, "save name")
	}
	highlighted := []string{}
	for i, s := range steps {
		if i == int(step) {
			highlighted = append(highlighted, "["+s+"]")
		} else {
			highlighted = append(highlighted, " "+s+" ")
		}
	}
	return strings.Join(highlighted, " -> ")
}

func proxyStepFooter(step proxyBuilderStep) string {
	switch step {
	case proxyStepDestination:
		return "enter select  /  up-down move  /  type to filter  /  esc cancel"
	case proxyStepListener:
		return "enter advance  /  shift+tab back  /  type to edit  /  esc cancel"
	case proxyStepSummary:
		return "enter fire  /  up-down move  /  shift+tab back  /  esc cancel"
	case proxyStepSaveName:
		return "enter save  /  shift+tab back to summary  /  type to edit  /  esc cancel"
	default:
		return ""
	}
}

func defaultProxySaveName(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "proxy"
	}
	if strings.HasSuffix(alias, "-proxy") || strings.HasSuffix(alias, "_proxy") {
		return alias
	}
	return alias + "-proxy"
}
