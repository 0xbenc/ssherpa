package ui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type CheckResult struct {
	Kind            string
	Name            string
	Status          string
	SSHRttMillis    int64
	SSHError        string
	ICMPStatus      string
	ICMPRttMillis   int64
	LocalBindStatus string
	Message         string
}

type CheckResultsOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	CheckedAt   time.Time
	OK          bool
	Results     []CheckResult
}

func ShowCheckResults(ctx context.Context, opts CheckResultsOptions) error {
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
	model := checkResultsModel{
		noAltScreen: opts.NoAltScreen,
		theme:       theme.WithNoColor(theme.NoColor || opts.NoColor),
		checkedAt:   opts.CheckedAt,
		ok:          opts.OK,
		results:     append([]CheckResult(nil), opts.Results...),
		width:       112,
		height:      28,
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

type checkResultsModel struct {
	noAltScreen bool
	theme       termstyle.Theme
	checkedAt   time.Time
	ok          bool
	results     []CheckResult
	width       int
	height      int
}

func (m checkResultsModel) Init() tea.Cmd {
	return tea.RequestWindowSize
}

func (m checkResultsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyPressMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m checkResultsModel) View() tea.View {
	width := max(56, m.width)
	theme := pickerTheme{theme: m.theme}

	title := "SSHERPA CHECK RESULTS"

	view := tea.NewView(renderWorkflowShell(theme, width, workflowShell{
		Title:  title,
		Body:   m.renderBody(max(20, width-4), theme),
		Footer: "press any key to return",
		Danger: !m.ok,
	}))
	view.AltScreen = !m.noAltScreen
	return view
}

func (m checkResultsModel) renderBody(width int, theme pickerTheme) []string {
	width = max(20, width)
	lines := []string{
		checkSummaryLine(theme, width, m.ok, m.results, m.checkedAt),
		"",
		theme.groupHeader("Results", width),
	}
	lines = append(lines, m.renderResultLines(width, theme)...)
	return lines
}

func (m checkResultsModel) renderResultLines(width int, theme pickerTheme) []string {
	if len(m.results) == 0 {
		return []string{"  " + theme.empty("No results to show.")}
	}

	maxLines := checkResultsLineBudget(m.height)
	lines := []string{}
	for i, result := range m.results {
		candidate := checkResultLines(theme, width, result)
		if len(lines)+len(candidate) > maxLines {
			hidden := len(m.results) - i
			if len(lines) == 0 && maxLines > 1 {
				lines = append(lines, candidate[0])
				hidden--
			}
			if hidden > 0 {
				notice := "  " + theme.muted(fmt.Sprintf("%d more result%s", hidden, pluralSuffix(hidden)))
				if len(lines) < maxLines {
					lines = append(lines, notice)
				} else if len(lines) > 0 {
					lines[len(lines)-1] = notice
				}
			}
			break
		}
		lines = append(lines, candidate...)
	}
	return lines
}

func checkResultsLineBudget(height int) int {
	if height <= 0 {
		return 16
	}
	// Shell chrome is top + divider/footer + bottom. Body overhead is
	// status, spacer, and section header. Leave one row as a safety margin.
	budget := height - 4 - 3 - 1
	if budget < 3 {
		budget = 3
	}
	return budget
}

func checkSummaryLine(theme pickerTheme, width int, ok bool, results []CheckResult, checkedAt time.Time) string {
	status := "✓ OK"
	role := termstyle.RoleSuccess
	if !ok {
		status = "× FAILED"
		role = termstyle.RoleDanger
	}

	total := len(results)
	issues := checkIssueCount(results)
	parts := []string{theme.theme.Style(role, status)}
	if issues > 0 {
		parts = append(parts, theme.muted(fmt.Sprintf("%d issue%s / %d checked", issues, pluralSuffix(issues), total)))
	} else {
		parts = append(parts, theme.muted(fmt.Sprintf("%d checked", total)))
	}
	if !checkedAt.IsZero() {
		parts = append(parts, theme.muted(checkedAt.Local().Format("15:04:05")))
	}
	return checkKVLineStyled(theme, width, "Status", strings.Join(parts, theme.muted("  ")))
}

func checkIssueCount(results []CheckResult) int {
	count := 0
	for _, result := range results {
		if strings.TrimSpace(result.Status) != "ok" {
			count++
		}
	}
	return count
}

func checkResultLines(theme pickerTheme, width int, result CheckResult) []string {
	lines := []string{checkResultSummaryLine(theme, width, result)}
	for _, line := range wrapPlain(checkResultMessage(result), max(8, width-4), 2) {
		lines = append(lines, "    "+theme.rowDesc(termstyle.Truncate(line, max(8, width-4)), false))
	}
	return lines
}

func checkResultSummaryLine(theme pickerTheme, width int, result CheckResult) string {
	marker, role := checkStatusMarker(result.Status)
	kind := checkKindLabel(result.Kind)
	name := strings.TrimSpace(result.Name)
	if name == "" {
		name = "-"
	}

	kindWidth := 8
	nameWidth := clamp(width/3, 12, 28)
	if width < 72 {
		nameWidth = clamp(width/4, 10, 18)
	}
	probeWidth := max(8, width-2-2-kindWidth-2-nameWidth-2)
	probes := strings.Join(checkProbeParts(result), "  ")
	if probes == "" {
		probes = strings.TrimSpace(result.Status)
	}
	if probes == "" {
		probes = "-"
	}

	line := "  " + theme.theme.Style(role, marker) + " " +
		theme.rowDesc(termstyle.PadRight(termstyle.Truncate(kind, kindWidth), kindWidth), false) + "  " +
		theme.theme.Style(termstyle.RoleForeground, termstyle.PadRight(termstyle.Truncate(name, nameWidth), nameWidth)) + "  " +
		theme.rowDesc(termstyle.Truncate(probes, probeWidth), false)
	return termstyle.PadRight(line, width)
}

func checkStatusMarker(status string) (string, termstyle.Role) {
	switch strings.TrimSpace(status) {
	case "ok":
		return "✓", termstyle.RoleSuccess
	case "invalid":
		return "!", termstyle.RoleWarning
	case "failed":
		return "×", termstyle.RoleDanger
	default:
		return "!", termstyle.RoleWarning
	}
}

func checkKindLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "saved_forward":
		return "forward"
	case "":
		return "check"
	default:
		return kind
	}
}

func checkProbeParts(result CheckResult) []string {
	var parts []string
	status := strings.TrimSpace(result.Status)
	switch {
	case status == "ok":
		parts = append(parts, "ssh "+checkDurationOrOK(result.SSHRttMillis))
	case status == "failed":
		parts = append(parts, checkFailedSSHPart(result))
	case status == "invalid":
		parts = append(parts, "invalid")
	default:
		parts = append(parts, status)
	}

	if bind := strings.TrimSpace(result.LocalBindStatus); bind != "" && bind != "skipped" {
		if bind == "ok" {
			parts = append(parts, "bind ok")
		} else {
			parts = append(parts, "bind "+bind)
		}
	}

	if icmp := checkICMPPart(result); icmp != "" {
		parts = append(parts, icmp)
	}
	return parts
}

func checkFailedSSHPart(result CheckResult) string {
	if strings.TrimSpace(result.SSHError) != "" {
		return "ssh failed"
	}
	if result.SSHRttMillis > 0 {
		return "ssh " + formatMillis(result.SSHRttMillis)
	}
	if bind := strings.TrimSpace(result.LocalBindStatus); bind != "" && bind != "skipped" && bind != "ok" {
		return "ssh ok"
	}
	return "ssh failed"
}

func checkDurationOrOK(ms int64) string {
	if ms <= 0 {
		return "ok"
	}
	return formatMillis(ms)
}

func checkICMPPart(result CheckResult) string {
	switch strings.TrimSpace(result.ICMPStatus) {
	case "", "skipped":
		return ""
	case "ok":
		if result.ICMPRttMillis > 0 {
			return "icmp " + formatMillis(result.ICMPRttMillis)
		}
		return "icmp ok"
	default:
		return "icmp " + result.ICMPStatus
	}
}

func checkResultMessage(result CheckResult) string {
	if message := strings.TrimSpace(result.Message); message != "" {
		return message
	}
	if bind := strings.TrimSpace(result.LocalBindStatus); bind != "" && bind != "skipped" && bind != "ok" {
		return "local bind " + bind
	}
	return strings.TrimSpace(result.SSHError)
}

func checkKVLineStyled(theme pickerTheme, width int, label string, value string) string {
	labelWidth := 8
	valueWidth := max(8, width-2-labelWidth-2)
	labelText := theme.label(termstyle.PadRight(label, labelWidth))
	return "  " + labelText + "  " + truncateStyled(value, valueWidth)
}

func formatMillis(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", ms)
}
