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

// ForwardAlias is the lightweight projection of a hostlist.Alias the
// builder needs. The caller (internal/cli) flattens its inventory
// before handing it in so this package stays free of hostlist
// dependencies — that's important because hostlist transitively pulls
// in sshconfig, and ui already sits at the bottom of the dependency
// graph.
type ForwardAlias struct {
	Name        string
	Description string // optional one-line summary, e.g. "user@host:port"
}

// ForwardAction is the action the user picked on the summary screen.
// Translates 1:1 to a `ssherpa forward` command-line invocation in
// the caller.
type ForwardAction string

const (
	ForwardActionRun        ForwardAction = "run"
	ForwardActionBackground ForwardAction = "background"
	ForwardActionPrint      ForwardAction = "print"
	ForwardActionSave       ForwardAction = "save"
	ForwardActionCancel     ForwardAction = "cancel"
)

// ForwardResult is the wizard's structured output. Cancel returns
// ok=false and an empty result; the other actions return a fully
// resolved spec the caller passes through to runForward (or
// runForwardWith / printOrRunSSH).
type ForwardResult struct {
	Alias      string
	LocalBind  string
	LocalPort  int
	RemoteHost string
	RemotePort int
	Through    string
	Action     ForwardAction
	// SavedName is populated when Action == ForwardActionSave —
	// the kebab-case identifier the user chose for the saved
	// forward in stepSaveName. Empty for any other action.
	SavedName string
}

// LocalSpec renders the local side as a "[bind:]port" string ready for
// `--local`. Drops the bind when it's the default loopback so the CLI
// rendering matches what a hand-typed command would look like.
func (r ForwardResult) LocalSpec() string {
	if r.LocalBind == "" || r.LocalBind == sshcmd.DefaultForwardBind {
		return fmt.Sprintf("%d", r.LocalPort)
	}
	if strings.Contains(r.LocalBind, ":") {
		return fmt.Sprintf("[%s]:%d", r.LocalBind, r.LocalPort)
	}
	return fmt.Sprintf("%s:%d", r.LocalBind, r.LocalPort)
}

// RemoteSpec renders the remote side as a "host:port" string ready
// for `--remote`. Brackets IPv6 hosts so the spelling is unambiguous.
func (r ForwardResult) RemoteSpec() string {
	if strings.Contains(r.RemoteHost, ":") {
		return fmt.Sprintf("[%s]:%d", r.RemoteHost, r.RemotePort)
	}
	return fmt.Sprintf("%s:%d", r.RemoteHost, r.RemotePort)
}

type BuildForwardOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Aliases     []ForwardAlias
}

// ForwardActionOptions configures the compact picker shown when a
// saved forward is launched from the home page. Saved forwards already
// have a complete spec, so the user only needs to choose the launch
// mode.
type ForwardActionOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Name        string
	Description string
}

// BuildForward runs the multi-step wizard on the home-page picker's
// Forward action. Returns (result, true, nil) on a successful build,
// (zero, false, nil) on cancel, or (_, false, err) on a fatal program
// error. The caller decides what to do with the action — the wizard
// itself doesn't run ssh.
func BuildForward(ctx context.Context, opts BuildForwardOptions) (ForwardResult, bool, error) {
	if len(opts.Aliases) == 0 {
		return ForwardResult{}, false, nil
	}

	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return ForwardResult{}, false, err
	}

	model := newForwardBuilderModel(opts, theme)
	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.Input != nil {
		programOptions = append(programOptions, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Output))
	}

	final, err := tea.NewProgram(model, programOptions...).Run()
	if err != nil {
		return ForwardResult{}, false, err
	}
	builder, ok := final.(forwardBuilderModel)
	if !ok || builder.canceled {
		return ForwardResult{}, false, nil
	}
	return builder.result, true, nil
}

// ChooseForwardLaunchAction prompts for how to launch an already-saved
// forward preset. It deliberately offers only the runtime choices that
// make sense for a saved spec: active foreground supervision or the
// detached background daemon.
func ChooseForwardLaunchAction(ctx context.Context, opts ForwardActionOptions) (ForwardAction, bool, error) {
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

	model := newForwardActionModel(opts, theme)
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
	chooser, ok := final.(forwardActionModel)
	if !ok || chooser.canceled {
		return "", false, nil
	}
	return chooser.action, true, nil
}

type builderStep int

const (
	builderStepDestination builderStep = iota
	builderStepLocal
	builderStepRemote
	builderStepThrough
	builderStepSummary
	// builderStepSaveName collects the kebab-case name for the
	// saved forward. Entered only when the user picks "Save as
	// alias…" on the summary screen; Enter finalizes the result
	// with Action=ForwardActionSave and SavedName=<value>.
	builderStepSaveName
)

// summaryAction is one row on the final action-pick screen. The Label
// is what the user sees; Action is what flows into ForwardResult.
type summaryAction struct {
	Action ForwardAction
	Label  string
}

// builderSummaryActions is the Phase 2e action set. Save as alias
// transitions to stepSaveName to collect the name; the other
// actions quit the program with the action recorded on
// ForwardResult.Action.
var builderSummaryActions = []summaryAction{
	{ForwardActionRun, "Run (foreground supervised)"},
	{ForwardActionBackground, "Run in background (detached, auto-reconnect)"},
	{ForwardActionPrint, "Print command and exit"},
	{ForwardActionSave, "Save as alias…"},
	{ForwardActionCancel, "Cancel"},
}

var forwardLaunchActions = []summaryAction{
	{ForwardActionRun, "Run active (foreground supervised)"},
	{ForwardActionBackground, "Run in background (detached, auto-reconnect)"},
	{ForwardActionCancel, "Cancel"},
}

type forwardActionModel struct {
	name        string
	description string
	cursor      int
	canceled    bool
	action      ForwardAction
	theme       termstyle.Theme
	width       int
	height      int
}

func newForwardActionModel(opts ForwardActionOptions, theme termstyle.Theme) forwardActionModel {
	return forwardActionModel{
		name:        opts.Name,
		description: opts.Description,
		theme:       theme,
		width:       104,
		height:      18,
	}
}

func (m forwardActionModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m forwardActionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "ctrl+n":
			if m.cursor < len(forwardLaunchActions)-1 {
				m.cursor++
			}
		case "enter":
			action := forwardLaunchActions[m.cursor].Action
			if action == ForwardActionCancel {
				m.canceled = true
				return m, tea.Quit
			}
			m.action = action
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m forwardActionModel) View() tea.View {
	width := clamp(m.width, 64, 140)
	theme := pickerTheme{theme: m.theme}
	var b strings.Builder

	title := theme.logo("SSHERPA FORWARD PRESET")
	b.WriteString(termstyle.PadRight(title, width))
	b.WriteString("\n\n")
	b.WriteString("  ")
	b.WriteString(theme.summary("Pick how to launch this saved forward:"))
	b.WriteString("\n\n")
	previewKVLine(&b, theme, "preset", m.name)
	if m.description != "" {
		previewKVLine(&b, theme, "route", termstyle.Truncate(m.description, max(0, width-18)))
	}
	b.WriteByte('\n')
	for i, action := range forwardLaunchActions {
		cursor := "  "
		if i == m.cursor {
			cursor = "> "
		}
		row := cursor + action.Label
		if i == m.cursor {
			row = theme.rowTitle(row, true)
		}
		b.WriteString("  ")
		b.WriteString(row)
		b.WriteByte('\n')
	}
	b.WriteString("\n  ")
	b.WriteString(theme.muted("enter launch  /  up/down move  /  esc cancel"))
	b.WriteByte('\n')
	return tea.NewView(b.String())
}

// forwardBuilderModel is the bubbletea model for the wizard. One
// model carries every step's state; Update branches on m.step and
// delegates to per-step handlers. This keeps the program structure
// recognizable next to themeEditorModel — no router, no screen
// stack, just step-as-state.
type forwardBuilderModel struct {
	aliases []ForwardAlias

	step builderStep

	// Destination step: filtered list-pick.
	destFilter  string
	destCursor  int
	destination ForwardAlias

	// Local-side text input. Pre-populated with "5432" so the common
	// case is a single Enter press.
	localBuf    string
	localCursor int
	localError  string

	// Remote-side text input. Pre-populated with "127.0.0.1:5432" by
	// the same logic — same-port-on-loopback is the most common
	// "tunnel a remote thing to here" pattern.
	remoteBuf    string
	remoteCursor int
	remoteError  string

	// Through (optional jump hop) step: filtered list-pick with a
	// synthetic "(skip)" row at the top.
	throughFilter string
	throughCursor int
	through       string

	// Summary action picker.
	summaryCursor int

	// Save-name step (only entered from the summary's "Save as
	// alias…" action). Default suggestion is derived from the
	// destination alias in finalizeBeforeSummary.
	saveNameBuf    string
	saveNameCursor int
	saveNameError  string

	// End-state.
	canceled bool
	result   ForwardResult

	// Rendering.
	theme       termstyle.Theme
	noAltScreen bool
	noColor     bool
	width       int
	height      int
}

func newForwardBuilderModel(opts BuildForwardOptions, theme termstyle.Theme) forwardBuilderModel {
	return forwardBuilderModel{
		aliases:      opts.Aliases,
		step:         builderStepDestination,
		localBuf:     "5432",
		localCursor:  4,
		remoteBuf:    "127.0.0.1:5432",
		remoteCursor: len("127.0.0.1:5432"),
		theme:        theme,
		noAltScreen:  opts.NoAltScreen,
		noColor:      opts.NoColor,
		width:        104,
		height:       28,
	}
}

func (m forwardBuilderModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m forwardBuilderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		// Universal cancel keys always work, no matter which step.
		switch msg.String() {
		case "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		}
		switch m.step {
		case builderStepDestination:
			return m.updateDestination(msg)
		case builderStepLocal:
			return m.updateLocal(msg)
		case builderStepRemote:
			return m.updateRemote(msg)
		case builderStepThrough:
			return m.updateThrough(msg)
		case builderStepSummary:
			return m.updateSummary(msg)
		case builderStepSaveName:
			return m.updateSaveName(msg)
		}
	}
	return m, nil
}

func (m forwardBuilderModel) updateDestination(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		m.step = builderStepLocal
		return m, nil
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

func (m forwardBuilderModel) destMatches() []int {
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

func (m forwardBuilderModel) updateLocal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.localBuf, m.localCursor, m.localError, func(v string) error {
		_, _, err := sshcmd.ParseForwardLocal(v)
		return err
	})
	m.localBuf = buf
	m.localCursor = cursor
	m.localError = errStr
	switch action {
	case textInputCancel:
		m.canceled = true
		return m, tea.Quit
	case textInputBack:
		m.step = builderStepDestination
	case textInputAdvance:
		m.step = builderStepRemote
	}
	return m, nil
}

func (m forwardBuilderModel) updateRemote(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.remoteBuf, m.remoteCursor, m.remoteError, func(v string) error {
		_, _, err := sshcmd.ParseForwardRemote(v)
		return err
	})
	m.remoteBuf = buf
	m.remoteCursor = cursor
	m.remoteError = errStr
	switch action {
	case textInputCancel:
		m.canceled = true
		return m, tea.Quit
	case textInputBack:
		m.step = builderStepLocal
	case textInputAdvance:
		m.step = builderStepThrough
	}
	return m, nil
}

// textInputAction is the wizard's per-keystroke decision for what
// the surrounding step should do once the input field has been
// updated. Lets updateTextInputState stay pure — no model touching.
type textInputAction int

const (
	textInputContinue textInputAction = iota
	textInputCancel
	textInputBack
	textInputAdvance
)

// updateTextInputState is the shared keyboard handler for the two
// text-input steps (local + remote). It is a pure function over
// (buf, cursor, errStr) so the caller can copy the returned values
// back onto the model. The earlier version took pointers across
// method boundaries which silently broke under bubbletea's value-
// receiver convention — mutations landed on a copy rather than the
// model the program loop sees.
func updateTextInputState(msg tea.KeyPressMsg, buf string, cursor int, errStr string, validate func(string) error) (textInputAction, string, int, string) {
	switch msg.String() {
	case "esc":
		return textInputCancel, buf, cursor, errStr
	case "shift+tab":
		return textInputBack, buf, cursor, errStr
	case "enter":
		if err := validate(buf); err != nil {
			return textInputContinue, buf, cursor, err.Error()
		}
		return textInputAdvance, buf, cursor, ""
	case "backspace":
		runes := []rune(buf)
		if cursor > 0 && cursor <= len(runes) {
			runes = append(runes[:cursor-1], runes[cursor:]...)
			buf = string(runes)
			cursor--
		}
		return textInputContinue, buf, cursor, ""
	case "delete":
		runes := []rune(buf)
		if cursor < len(runes) {
			runes = append(runes[:cursor], runes[cursor+1:]...)
			buf = string(runes)
		}
		return textInputContinue, buf, cursor, ""
	case "left", "ctrl+b":
		if cursor > 0 {
			cursor--
		}
		return textInputContinue, buf, cursor, errStr
	case "right", "ctrl+f":
		runes := []rune(buf)
		if cursor < len(runes) {
			cursor++
		}
		return textInputContinue, buf, cursor, errStr
	case "home", "ctrl+a":
		cursor = 0
		return textInputContinue, buf, cursor, errStr
	case "end", "ctrl+e":
		cursor = len([]rune(buf))
		return textInputContinue, buf, cursor, errStr
	case "ctrl+u":
		return textInputContinue, "", 0, ""
	default:
		if isPrintableInput(msg) {
			runes := []rune(buf)
			text := msg.Key().Text
			insert := []rune(text)
			if cursor > len(runes) {
				cursor = len(runes)
			}
			runes = append(runes[:cursor], append(insert, runes[cursor:]...)...)
			buf = string(runes)
			cursor += len(insert)
			errStr = ""
		}
	}
	return textInputContinue, buf, cursor, errStr
}

func (m forwardBuilderModel) updateThrough(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	matches := m.throughChoices()
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "shift+tab":
		m.step = builderStepRemote
		return m, nil
	case "enter":
		if len(matches) == 0 {
			m.through = ""
			m.step = builderStepSummary
			m.finalizeBeforeSummary()
			return m, nil
		}
		if m.throughCursor >= len(matches) {
			m.throughCursor = len(matches) - 1
		}
		idx := matches[m.throughCursor]
		if idx < 0 {
			m.through = ""
		} else {
			m.through = m.aliases[idx].Name
		}
		m.step = builderStepSummary
		m.finalizeBeforeSummary()
		return m, nil
	case "up", "ctrl+p":
		if m.throughCursor > 0 {
			m.throughCursor--
		}
	case "down", "ctrl+n":
		if m.throughCursor < len(matches)-1 {
			m.throughCursor++
		}
	case "backspace":
		if m.throughFilter != "" {
			runes := []rune(m.throughFilter)
			m.throughFilter = string(runes[:len(runes)-1])
			m.throughCursor = 0
		}
	case "ctrl+u":
		m.throughFilter = ""
		m.throughCursor = 0
	default:
		if isPrintableInput(msg) {
			m.throughFilter += msg.Key().Text
			m.throughCursor = 0
		}
	}
	return m, nil
}

// throughChoices returns an index slice where -1 represents the
// synthetic "(skip — no jump hop)" sentinel and other indices map
// back into m.aliases (excluding the destination). The sentinel is
// always first so a single Enter on the through screen builds a
// no-jump tunnel.
func (m forwardBuilderModel) throughChoices() []int {
	out := []int{-1}
	for i, a := range m.aliases {
		if a.Name == m.destination.Name {
			continue
		}
		if m.throughFilter == "" || fuzzyMatch(a.Name, m.throughFilter) || fuzzyMatch(a.Description, m.throughFilter) {
			out = append(out, i)
		}
	}
	return out
}

// finalizeBeforeSummary parses local/remote into the result struct so
// the summary screen can render a real argv preview. By this point
// both inputs have already passed validation on their step's Enter
// press, so the parse should never fail — guard with an error reset
// just in case the user edited then back-tabbed.
func (m *forwardBuilderModel) finalizeBeforeSummary() {
	bind, port, _ := sshcmd.ParseForwardLocal(m.localBuf)
	host, rport, _ := sshcmd.ParseForwardRemote(m.remoteBuf)
	m.result = ForwardResult{
		Alias:      m.destination.Name,
		LocalBind:  bind,
		LocalPort:  port,
		RemoteHost: host,
		RemotePort: rport,
		Through:    m.through,
	}
}

func (m forwardBuilderModel) updateSummary(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.canceled = true
		return m, tea.Quit
	case "shift+tab":
		m.step = builderStepThrough
		return m, nil
	case "up", "ctrl+p":
		if m.summaryCursor > 0 {
			m.summaryCursor--
		}
	case "down", "ctrl+n":
		if m.summaryCursor < len(builderSummaryActions)-1 {
			m.summaryCursor++
		}
	case "enter":
		action := builderSummaryActions[m.summaryCursor].Action
		if action == ForwardActionCancel {
			m.canceled = true
			return m, tea.Quit
		}
		if action == ForwardActionSave {
			// Transition to the save-name step; pre-fill a sensible
			// default kebab-name derived from the destination so a
			// single Enter accepts it. The user can edit before Enter.
			if m.saveNameBuf == "" {
				m.saveNameBuf = defaultSaveName(m.destination.Name)
				m.saveNameCursor = len([]rune(m.saveNameBuf))
			}
			m.step = builderStepSaveName
			return m, nil
		}
		m.result.Action = action
		return m, tea.Quit
	}
	return m, nil
}

func (m forwardBuilderModel) updateSaveName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, buf, cursor, errStr := updateTextInputState(msg, m.saveNameBuf, m.saveNameCursor, m.saveNameError, validateSaveName)
	m.saveNameBuf = buf
	m.saveNameCursor = cursor
	m.saveNameError = errStr
	switch action {
	case textInputCancel:
		m.canceled = true
		return m, tea.Quit
	case textInputBack:
		m.step = builderStepSummary
	case textInputAdvance:
		m.result.Action = ForwardActionSave
		m.result.SavedName = strings.TrimSpace(buf)
		return m, tea.Quit
	}
	return m, nil
}

// defaultSaveName turns an SSH alias into a sensible default catalog
// name. We don't bother with sophisticated kebab-casing — the alias
// itself is usually already a valid identifier; just append "-tunnel"
// if it isn't an obvious tunnel name already.
func defaultSaveName(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "tunnel"
	}
	if strings.HasSuffix(alias, "-tunnel") || strings.HasSuffix(alias, "_tunnel") {
		return alias
	}
	return alias + "-tunnel"
}

// validateSaveName mirrors the file-system rules state.ValidateForwardName
// applies on the persistence side. Duplicated (not imported) to keep
// the ui package free of internal/state.
func validateSaveName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("name is required")
	}
	if name != trimmed {
		return fmt.Errorf("no leading or trailing whitespace")
	}
	if strings.ContainsAny(name, " \t\r\n\x00/\\") {
		return fmt.Errorf("no whitespace, slashes, or NUL")
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("cannot start with a dot")
	}
	return nil
}

func (m forwardBuilderModel) View() tea.View {
	width := clamp(m.width, 64, 140)
	theme := pickerTheme{theme: m.theme}
	var b strings.Builder

	title := theme.logo("SSHERPA FORWARD BUILDER")
	b.WriteString(termstyle.PadRight(title, width))
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.muted(stepBreadcrumb(m.step)))
	b.WriteString("\n\n")

	switch m.step {
	case builderStepDestination:
		m.viewDestination(&b, theme, width)
	case builderStepLocal:
		m.viewLocal(&b, theme, width)
	case builderStepRemote:
		m.viewRemote(&b, theme, width)
	case builderStepThrough:
		m.viewThrough(&b, theme, width)
	case builderStepSummary:
		m.viewSummary(&b, theme, width)
	case builderStepSaveName:
		m.viewSaveName(&b, theme, width)
	}

	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.muted(stepFooter(m.step)))
	b.WriteByte('\n')

	return tea.NewView(b.String())
}

func (m forwardBuilderModel) viewDestination(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Pick the SSH destination for the tunnel:"))
	b.WriteByte('\n')
	if m.destFilter != "" {
		b.WriteString("  filter  ")
		b.WriteString(theme.previewTitle(m.destFilter))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	matches := m.destMatches()
	if len(matches) == 0 {
		b.WriteString("  ")
		b.WriteString(theme.muted("(no matching hosts)"))
		b.WriteByte('\n')
		return
	}
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

func (m forwardBuilderModel) viewLocal(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Local listener — [BIND:]PORT (default bind is 127.0.0.1):"))
	b.WriteByte('\n')
	b.WriteString("  destination  ")
	b.WriteString(theme.previewTitle(m.destination.Name))
	b.WriteByte('\n')
	b.WriteByte('\n')
	renderInput(b, theme, "--local", m.localBuf, m.localCursor, m.localError, width)
	_ = width
}

func (m forwardBuilderModel) viewRemote(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Remote endpoint — HOST:PORT to forward to:"))
	b.WriteByte('\n')
	b.WriteString("  destination  ")
	b.WriteString(theme.previewTitle(m.destination.Name))
	b.WriteByte('\n')
	b.WriteString("  local        ")
	b.WriteString(theme.rowDesc(m.localBuf, false))
	b.WriteByte('\n')
	b.WriteByte('\n')
	renderInput(b, theme, "--remote", m.remoteBuf, m.remoteCursor, m.remoteError, width)
}

func (m forwardBuilderModel) viewThrough(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Optional ProxyJump hop — pick (skip) for a direct connection:"))
	b.WriteByte('\n')
	if m.throughFilter != "" {
		b.WriteString("  filter  ")
		b.WriteString(theme.previewTitle(m.throughFilter))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	matches := m.throughChoices()
	maxLines := clamp(m.height-12, 6, 20)
	from, to := windowRange(m.throughCursor, len(matches), maxLines)
	for i := from; i < to; i++ {
		idx := matches[i]
		var name, desc string
		if idx < 0 {
			name = "(skip — no jump hop)"
			desc = "use the alias's existing ProxyJump (or direct)"
		} else {
			alias := m.aliases[idx]
			name = alias.Name
			desc = alias.Description
		}
		cursor := "  "
		if i == m.throughCursor {
			cursor = "> "
		}
		line := cursor + termstyle.PadRight(name, 28) + theme.rowDesc(termstyle.Truncate(desc, max(0, width-32)), false)
		if i == m.throughCursor {
			line = theme.rowTitle(line, true)
		}
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func (m forwardBuilderModel) viewSummary(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Review and pick an action:"))
	b.WriteByte('\n')
	b.WriteByte('\n')
	previewKVLine(b, theme, "destination", m.destination.Name)
	previewKVLine(b, theme, "local", m.result.LocalSpec())
	previewKVLine(b, theme, "remote", m.result.RemoteSpec())
	if m.through != "" {
		previewKVLine(b, theme, "through", m.through)
	}
	b.WriteByte('\n')
	preview := buildForwardPreview(m.result)
	previewKVLine(b, theme, "preview", preview)
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.summary("Actions:"))
	b.WriteByte('\n')
	for i, action := range builderSummaryActions {
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

// renderInput draws a single text-input field plus an optional
// inline error line. The cursor is rendered as a styled space
// inside the buffer text.
func renderInput(b *strings.Builder, theme pickerTheme, label, buf string, cursor int, errStr string, width int) {
	b.WriteString("  ")
	b.WriteString(theme.muted(label))
	b.WriteString("  ")
	b.WriteString(theme.previewTitle(insertCursor(buf, cursor)))
	b.WriteByte('\n')
	if errStr != "" {
		b.WriteString("  ")
		b.WriteString(theme.theme.Style(termstyle.RoleDanger, "error: "+errStr))
		b.WriteByte('\n')
	}
	_ = width
}

// insertCursor places a visible block at the cursor position so the
// user can see where typing will land. A '|' character is enough —
// no styling needed; the surrounding text already uses the preview
// (highlighted) role.
func insertCursor(buf string, cursor int) string {
	runes := []rune(buf)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	return string(runes[:cursor]) + "|" + string(runes[cursor:])
}

func previewKVLine(b *strings.Builder, theme pickerTheme, key, value string) {
	b.WriteString("  ")
	b.WriteString(termstyle.PadRight(theme.muted(key), 14))
	b.WriteString(theme.rowDesc(value, false))
	b.WriteByte('\n')
}

func buildForwardPreview(r ForwardResult) string {
	parts := []string{"ssh"}
	if r.Through != "" {
		parts = append(parts, "-J", r.Through)
	}
	parts = append(parts, "-L", fmt.Sprintf("%s:%d:%s:%d", r.LocalBind, r.LocalPort, r.RemoteHost, r.RemotePort))
	parts = append(parts, "-N", "-o", "ExitOnForwardFailure=yes", r.Alias)
	return strings.Join(parts, " ")
}

func stepBreadcrumb(step builderStep) string {
	steps := []string{"destination", "local", "remote", "through", "summary"}
	if step == builderStepSaveName {
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

func stepFooter(step builderStep) string {
	switch step {
	case builderStepDestination:
		return "enter select  /  up/down move  /  type to filter  /  esc cancel"
	case builderStepLocal, builderStepRemote:
		return "enter advance  /  shift+tab back  /  type to edit  /  esc cancel"
	case builderStepThrough:
		return "enter select  /  up/down move  /  type to filter  /  shift+tab back  /  esc cancel"
	case builderStepSummary:
		return "enter fire  /  up/down move  /  shift+tab back  /  esc cancel"
	case builderStepSaveName:
		return "enter save  /  shift+tab back to summary  /  type to edit  /  esc cancel"
	default:
		return ""
	}
}

func (m forwardBuilderModel) viewSaveName(b *strings.Builder, theme pickerTheme, width int) {
	b.WriteString("  ")
	b.WriteString(theme.summary("Save this forward to ssherpa's catalog under what name?"))
	b.WriteByte('\n')
	b.WriteString("  ")
	b.WriteString(theme.muted("(kebab-case; this becomes the handle for `ssherpa forward stop NAME`)"))
	b.WriteByte('\n')
	b.WriteByte('\n')
	renderInput(b, theme, "name", m.saveNameBuf, m.saveNameCursor, m.saveNameError, width)
}

// windowRange picks a viewport range [from, to) that keeps the cursor
// visible inside a fixed-height list. Mirrors pickerModel's approach
// so the keyboard feel is identical between this wizard and the
// home-page picker.
func windowRange(cursor, total, maxLines int) (int, int) {
	if total <= maxLines {
		return 0, total
	}
	from := cursor - maxLines/2
	if from < 0 {
		from = 0
	}
	to := from + maxLines
	if to > total {
		to = total
		from = to - maxLines
		if from < 0 {
			from = 0
		}
	}
	return from, to
}

// isPrintableInput reports whether a KeyPressMsg carries text the
// builder should treat as user input (vs. a control key like Tab or
// arrows that already have explicit handlers above). Empty text
// means the key didn't produce input characters — a modifier, a
// navigation key, etc.
func isPrintableInput(msg tea.KeyPressMsg) bool {
	text := msg.Key().Text
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
