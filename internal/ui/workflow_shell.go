package ui

import (
	"strings"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type workflowShell struct {
	Title   string
	Steps   []string
	Current int
	Body    []string
	Footer  string
	Danger  bool
}

func renderWorkflowShell(theme pickerTheme, width int, opts workflowShell) string {
	width = max(48, width)
	var lines []string

	titleRole := termstyle.RoleTitle
	if opts.Danger {
		titleRole = termstyle.RoleDanger
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "ssherpa"
	}
	lines = append(lines, workflowEdge(theme, "╭", "╮", theme.theme.Style(titleRole, title), width))
	if len(opts.Steps) > 0 {
		lines = append(lines, workflowLine(theme, workflowProgress(theme, opts.Steps, opts.Current, width-4), width))
		lines = append(lines, workflowDivider(theme, width))
	}
	for _, line := range opts.Body {
		lines = append(lines, workflowLine(theme, line, width))
	}
	if opts.Footer != "" {
		lines = append(lines, workflowDivider(theme, width))
		lines = append(lines, workflowLine(theme, theme.muted(opts.Footer), width))
	}
	lines = append(lines, workflowEdge(theme, "╰", "╯", "", width))
	return strings.Join(lines, "\n") + "\n"
}

func workflowProgress(theme pickerTheme, steps []string, current int, width int) string {
	parts := make([]string, 0, len(steps))
	for i, step := range steps {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		switch {
		case i < current:
			parts = append(parts, theme.theme.Style(termstyle.RoleSuccess, "✓ "+step))
		case i == current:
			parts = append(parts, theme.theme.Style(termstyle.RoleSelected, "● "+step))
		default:
			parts = append(parts, theme.theme.Style(termstyle.RoleMuted, "○ "+step))
		}
	}
	return fitWorkflowProgress(theme, parts, current, width)
}

func fitWorkflowProgress(theme pickerTheme, parts []string, current int, width int) string {
	if len(parts) == 0 {
		return ""
	}
	if current < 0 {
		current = 0
	}
	if current >= len(parts) {
		current = len(parts) - 1
	}
	if width <= 0 {
		return joinWorkflowProgress(theme, parts)
	}
	full := joinWorkflowProgress(theme, parts)
	if termstyle.VisibleWidth(full) <= width {
		return full
	}

	for start := 1; start <= current; start++ {
		candidate := joinWorkflowProgress(theme, append([]string{theme.muted("…")}, parts[start:]...))
		if termstyle.VisibleWidth(candidate) <= width {
			return candidate
		}
	}

	currentAndFuture := append([]string{theme.muted("…")}, parts[current:]...)
	candidate := joinWorkflowProgress(theme, currentAndFuture)
	if termstyle.VisibleWidth(candidate) <= width {
		return candidate
	}
	return joinWorkflowProgress(theme, parts[current:])
}

func joinWorkflowProgress(theme pickerTheme, parts []string) string {
	return strings.Join(parts, theme.muted(" › "))
}

func workflowEdge(theme pickerTheme, left string, right string, label string, width int) string {
	inner := max(0, width-2)
	if inner == 0 {
		return theme.theme.Style(termstyle.RoleBorder, left+right)
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return theme.theme.Style(termstyle.RoleBorder, left+strings.Repeat("─", inner)+right)
	}
	label = " " + truncateStyled(label, max(0, inner-2)) + " "
	remaining := inner - termstyle.VisibleWidth(label)
	if remaining < 0 {
		remaining = 0
	}
	return theme.theme.Style(termstyle.RoleBorder, left) + label + theme.theme.Style(termstyle.RoleBorder, strings.Repeat("─", remaining)+right)
}

func workflowDivider(theme pickerTheme, width int) string {
	inner := max(0, width-2)
	return theme.theme.Style(termstyle.RoleBorder, "├"+strings.Repeat("─", inner)+"┤")
}

func workflowLine(theme pickerTheme, line string, width int) string {
	inner := max(0, width-4)
	content := termstyle.PadRight(truncateStyled(line, inner), inner)
	return theme.theme.Style(termstyle.RoleBorder, "│ ") + content + theme.theme.Style(termstyle.RoleBorder, " │")
}

func workflowBodyLines(b *strings.Builder) []string {
	text := strings.TrimRight(b.String(), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func truncateStyled(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if termstyle.VisibleWidth(value) <= width {
		return value
	}
	// On overflow the styling is dropped anyway, so Sanitize (not just Strip)
	// to additionally neutralize raw C0/C1/DEL — defense-in-depth at the
	// trusted-chrome boundary an operator reads mid-incident. The fit path
	// keeps the role styling; untrusted content is sanitized upstream
	// (cleanField/sanitizeRemoteString) before it reaches the chrome.
	return termstyle.Truncate(termstyle.Sanitize(value), width)
}
