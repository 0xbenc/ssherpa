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
	width := clamp(m.width, 72, 140)
	theme := pickerTheme{theme: m.theme}
	var b strings.Builder

	title := "SSHERPA CHECK RESULTS"
	b.WriteString(termstyle.PadRight(theme.logo(title), width))
	b.WriteString("\n")
	b.WriteString(theme.theme.Style(termstyle.RoleBorder, strings.Repeat("-", width)))
	b.WriteString("\n")

	status := "OK"
	statusRole := termstyle.RoleSuccess
	if !m.ok {
		status = "FAILED"
		statusRole = termstyle.RoleDanger
	}
	checkedAt := ""
	if !m.checkedAt.IsZero() {
		checkedAt = "  " + m.checkedAt.Local().Format("15:04:05")
	}
	b.WriteString("  ")
	b.WriteString(theme.theme.Style(statusRole, status))
	b.WriteString(theme.muted(checkedAt))
	b.WriteString("\n\n")

	nameWidth := clamp(maxCheckNameWidth(m.results), 16, max(16, width-74))
	fmt.Fprintf(&b, "  %s  %s  %s  %s  %s  %s\n",
		theme.label(termstyle.PadRight("KIND", 8)),
		theme.label(termstyle.PadRight("NAME", nameWidth)),
		theme.label(termstyle.PadRight("STATUS", 8)),
		theme.label(fmt.Sprintf("%6s", "SSH")),
		theme.label(termstyle.PadRight("ICMP", 8)),
		theme.label("MESSAGE"),
	)
	b.WriteString("  ")
	b.WriteString(theme.theme.Style(termstyle.RoleBorder, strings.Repeat("-", min(width-4, 8+2+nameWidth+2+8+2+6+2+8+2+24))))
	b.WriteString("\n")

	maxRows := clamp(m.height-9, 4, 24)
	for i, result := range m.results {
		if i >= maxRows {
			fmt.Fprintf(&b, "  %s\n", theme.muted(fmt.Sprintf("... %d more result(s)", len(m.results)-i)))
			break
		}
		statusText := theme.theme.Style(checkStatusRole(result.Status), termstyle.PadRight(result.Status, 8))
		icmp := result.ICMPStatus
		if result.ICMPStatus == "ok" && result.ICMPRttMillis > 0 {
			icmp = fmt.Sprintf("%dms", result.ICMPRttMillis)
		}
		message := result.Message
		if message == "" && result.LocalBindStatus != "" && result.LocalBindStatus != "skipped" && result.LocalBindStatus != "ok" {
			message = "local bind " + result.LocalBindStatus
		}
		if message == "" {
			message = result.SSHError
		}
		fmt.Fprintf(&b, "  %s  %s  %s  %s  %s  %s\n",
			theme.rowDesc(termstyle.PadRight(result.Kind, 8), false),
			theme.theme.Style(termstyle.RoleForeground, termstyle.PadRight(termstyle.Truncate(result.Name, nameWidth), nameWidth)),
			statusText,
			theme.rowDesc(fmt.Sprintf("%6s", formatMillis(result.SSHRttMillis)), false),
			theme.rowDesc(termstyle.PadRight(termstyle.Truncate(icmp, 8), 8), false),
			theme.rowDesc(termstyle.Truncate(message, max(0, width-nameWidth-40)), false),
		)
	}

	b.WriteString("\n")
	b.WriteString("  ")
	b.WriteString(theme.muted("press any key to return"))
	b.WriteString("\n")

	view := tea.NewView(b.String())
	view.AltScreen = !m.noAltScreen
	return view
}

func maxCheckNameWidth(results []CheckResult) int {
	width := len("NAME")
	for _, result := range results {
		if n := len([]rune(result.Name)); n > width {
			width = n
		}
	}
	return width
}

func checkStatusRole(status string) termstyle.Role {
	switch status {
	case "ok":
		return termstyle.RoleSuccess
	case "invalid", "failed":
		return termstyle.RoleDanger
	default:
		return termstyle.RoleWarning
	}
}

func formatMillis(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", ms)
}
