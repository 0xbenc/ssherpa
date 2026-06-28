package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/termnav"
	"github.com/0xbenc/termnav/teax"
)

// TransferBrowserOptions configures the file-transfer browser. The browser
// itself is now termnav-driven: navigation, async listing (a slow SFTP round
// trip shows a loading state instead of freezing), filtering, and cancelation
// all happen inside one program. The caller supplies the transport as a
// termnav.FileSource (a local lister or ssherpa's SFTP source).
type TransferBrowserOptions struct {
	Input         io.Reader
	Output        io.Writer
	NoAltScreen   bool
	NoColor       bool
	Theme         termstyle.Theme
	ThemeName     string
	ThemeFile     string
	Title         string
	Mode          string
	LocationLabel string
	Start         string
	Steps         []string
	CurrentStep   int
	Footer        string
}

// BrowseTransfer drives the file-transfer browser over src and returns the
// committed selection (a chosen file or a "use this folder"). ok=false on cancel.
func BrowseTransfer(ctx context.Context, src termnav.FileSource, opts TransferBrowserOptions) (termnav.Outcome, bool, error) {
	theme, err := resolvePickTheme(PickOptions{
		Output:    opts.Output,
		NoColor:   opts.NoColor,
		Theme:     opts.Theme,
		ThemeName: opts.ThemeName,
		ThemeFile: opts.ThemeFile,
	})
	if err != nil {
		return termnav.Outcome{}, false, err
	}
	theme = theme.WithNoColor(theme.NoColor || opts.NoColor)
	pt := pickerTheme{theme: theme}

	reserve := 4 + 5 + 1 // chrome + fixed body (meta/filter/blank/...) + safety
	if len(opts.Steps) > 0 {
		reserve += 2
	}
	navOpts := termnav.Options{
		Matcher:            termnav.Fuzzy{},
		MatchText:          transferMatchText,
		ReserveRows:        reserve,
		MinRows:            4,
		KeepCursorOnFilter: true,
	}

	render := func(m termnav.Model) tea.View {
		v := tea.NewView(renderTransferBrowser(m, opts, pt))
		v.AltScreen = !opts.NoAltScreen
		return v
	}
	input := opts.Input
	if input == nil {
		input = os.Stdin
	}
	return teax.Run(ctx, teax.Config{
		Source: src,
		Start:  opts.Start,
		Render: render,
		KeyMap: transferKeyMap,
	}, navOpts, teax.ProgramIO{Input: input, Output: opts.Output})
}

// transferKeyMap adds ssherpa's letter-free ctrl+q cancel to the shared key
// profile (esc/ctrl+c already cancel via teax.DefaultKey).
func transferKeyMap(msg tea.KeyPressMsg) (termnav.KeyEvent, bool) {
	if msg.String() == "ctrl+q" {
		return termnav.KeyEvent{Key: "cancel"}, true
	}
	return teax.DefaultKey(msg)
}

// transferMatchText is the string a row is fuzzy-filtered against — the same
// fields the old browser joined.
func transferMatchText(r termnav.Row) string {
	return strings.Join([]string{r.Title, r.Description, r.Detail, r.Token, r.Group, r.Badge}, "\t")
}

func renderTransferBrowser(m termnav.Model, opts TransferBrowserOptions, theme pickerTheme) string {
	width := max(64, m.Width())
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "SSHERPA FILE TRANSFER"
	}
	body := transferBody(m, opts, width, theme)
	footer := opts.Footer
	if footer == "" {
		footer = "enter open/select / type filter / arrows move / shift+arrows section / esc cancel"
	}
	return renderWorkflowShell(theme, width, workflowShell{
		Title:   title,
		Steps:   opts.Steps,
		Current: opts.CurrentStep,
		Body:    body,
		Footer:  footer,
	})
}

func transferBody(m termnav.Model, opts TransferBrowserOptions, width int, theme pickerTheme) []string {
	inner := max(20, width-4)
	lines := []string{
		transferMetaLine(m, opts, inner, theme),
		transferFilterLine(m, inner, theme),
		"",
	}
	if m.State() == termnav.Loading {
		return append(lines, "  "+theme.muted("loading…"))
	}
	if m.State() == termnav.Error {
		return append(lines, "  "+theme.empty("listing failed: "+termstyle.Sanitize(m.Notice())))
	}

	listWidth := inner
	previewWidth := 0
	if inner >= 92 {
		listWidth = clamp(inner*62/100, 54, 78)
		previewWidth = inner - listWidth - 3
	}
	listLines := transferListLines(m, listWidth, theme)
	if previewWidth <= 0 {
		lines = append(lines, listLines...)
		if item, ok := m.FocusedRow(); ok {
			lines = append(lines, "")
			lines = append(lines, theme.subtle(termstyle.Truncate(transferActionLabel(item)+"  "+transferPath(item), inner)))
		}
		return lines
	}

	previewLines := transferPreviewLines(m, previewWidth, theme)
	rowCount := max(len(listLines), len(previewLines))
	divider := theme.muted("│")
	for i := 0; i < rowCount; i++ {
		left := ""
		if i < len(listLines) {
			left = listLines[i]
		}
		right := ""
		if i < len(previewLines) {
			right = previewLines[i]
		}
		lines = append(lines, termstyle.PadRight(left, listWidth)+" "+divider+" "+right)
	}
	return lines
}

func transferMetaLine(m termnav.Model, opts TransferBrowserOptions, width int, theme pickerTheme) string {
	mode := transferBrowserModeLabel(opts.Mode)
	count := fmt.Sprintf("%d item%s", len(m.Rows()), pluralSuffix(len(m.Rows())))
	location := m.Cwd()
	if location == "" {
		location = "."
	}
	label := opts.LocationLabel
	if label == "" {
		label = "PATH"
	}
	labelWidth := clamp(width/5, 7, 18)
	prefix := theme.label(termstyle.PadRight(termstyle.Truncate(strings.ToUpper(label), labelWidth), labelWidth))
	meta := theme.summary(termstyle.Truncate(mode+"  "+count, max(0, width-termstyle.VisibleWidth(prefix)-4)))
	pathWidth := max(0, width-termstyle.VisibleWidth(prefix)-termstyle.VisibleWidth(meta)-4)
	path := theme.rowDesc(termstyle.Truncate(termstyle.Sanitize(location), pathWidth), false)
	return prefix + "  " + path + "  " + meta
}

func transferFilterLine(m termnav.Model, width int, theme pickerTheme) string {
	label := theme.label(termstyle.PadRight("FILTER", 7))
	counter := theme.counter(fmt.Sprintf("%d/%d", len(m.Filtered()), len(m.Rows())))
	query := termstyle.Sanitize(m.Query())
	if query == "" {
		query = "type to filter"
	}
	fieldWidth := max(8, width-termstyle.VisibleWidth(label)-termstyle.VisibleWidth(counter)-6)
	field := "[" + termstyle.PadRight(termstyle.Truncate(query, fieldWidth), fieldWidth) + "]"
	if m.Query() == "" {
		field = theme.muted(field)
	} else {
		field = theme.search(field)
	}
	return label + "  " + field + "  " + counter
}

func transferListLines(m termnav.Model, width int, theme pickerTheme) []string {
	filtered := m.Filtered()
	rows := m.Rows()
	if len(filtered) == 0 {
		if m.State() == termnav.Empty {
			return []string{"  " + theme.empty("empty folder")}
		}
		return []string{"  " + theme.empty("No matching files")}
	}
	budget := m.Budget()
	lines := make([]string, 0, budget)
	start := m.Scroll()
	if start < 0 {
		start = 0
	}
	if start > 0 {
		lines = append(lines, "  "+theme.muted(fmt.Sprintf("%d more above", start)))
	}
	lastGroup := ""
	rendered := 0
	renderedUntil := start
	for i := start; i < len(filtered); i++ {
		index := filtered[i]
		if index < 0 || index >= len(rows) {
			continue
		}
		row := rows[index]
		newGroup := row.Group != "" && row.Group != lastGroup
		groupCost := 0
		if newGroup {
			groupCost = 1
			if rendered > 0 {
				groupCost++
			}
		}
		reserve := 0
		if len(filtered)-i-1 > 0 {
			reserve = 1
		}
		if len(lines)+groupCost+1+reserve > budget {
			break
		}
		if newGroup {
			if rendered > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, theme.groupHeader(row.Group, width))
			lastGroup = row.Group
		}
		lines = append(lines, transferRow(row, i == m.Cursor(), width, theme))
		rendered++
		renderedUntil = i + 1
	}
	if renderedUntil < len(filtered) {
		lines = append(lines, "  "+theme.muted(fmt.Sprintf("%d more below", len(filtered)-renderedUntil)))
	}
	return lines
}

func transferRow(row termnav.Row, selected bool, width int, theme pickerTheme) string {
	cursor := "  "
	if selected {
		cursor = ">>"
	}
	badge := strings.ToUpper(strings.TrimSpace(row.Badge))
	if badge == "" {
		badge = strings.ToUpper(transferIntentLabel(row.Intent))
	}
	if len([]rune(badge)) > 6 {
		badge = termstyle.Truncate(badge, 6)
	}

	nameWidth := clamp(width*45/100, 18, 38)
	if width < 58 {
		nameWidth = max(12, width-18)
	}
	metaWidth := max(0, width-2-2-8-nameWidth-2)
	title := termstyle.Truncate(termstyle.Sanitize(row.Title), nameWidth)
	meta := transferRowMeta(row)
	line := theme.cursor(cursor, selected) + " " +
		termstyle.PadRight(transferBadge(theme, row.Intent, "["+badge+"]"), 8) + " " +
		termstyle.PadRight(theme.rowTitle(title, selected), nameWidth)
	if metaWidth > 0 {
		line += "  " + theme.rowDesc(termstyle.Truncate(termstyle.Sanitize(meta), metaWidth), selected)
	}
	return termstyle.PadRight(line, width)
}

// transferBadge colors a row's badge by its canonical NavIntent (never by an app
// Kind literal), preserving the old per-kind colors: a leaf reads foreground, a
// directory info, the parent secondary, and "use this folder" success.
func transferBadge(theme pickerTheme, intent termnav.NavIntent, value string) string {
	role := termstyle.RoleForeground
	switch intent {
	case termnav.IntentDescend:
		role = termstyle.RoleInfo
	case termnav.IntentAscend:
		role = termstyle.RoleSecondary
	case termnav.IntentUseContainer:
		role = termstyle.RoleSuccess
	case termnav.IntentSelectLeaf, termnav.IntentReference:
		role = termstyle.RoleForeground
	}
	return theme.theme.Style(role, value)
}

func transferRowMeta(row termnav.Row) string {
	if strings.TrimSpace(row.Description) != "" {
		return row.Description
	}
	if strings.TrimSpace(row.Detail) != "" {
		return row.Detail
	}
	if strings.TrimSpace(row.Token) != "" && row.Token != row.Title {
		return row.Token
	}
	return ""
}

func transferPreviewLines(m termnav.Model, width int, theme pickerTheme) []string {
	row, ok := m.FocusedRow()
	if !ok {
		return nil
	}
	lines := []string{
		theme.groupHeader("Selection", width),
		theme.previewTitle(termstyle.Truncate(termstyle.Sanitize(row.Title), width)),
	}
	lines = append(lines, previewKVLines(theme, width, "Type", transferTypeLabel(row), 3)...)
	if path := transferPath(row); path != "" {
		lines = append(lines, previewKVLines(theme, width, "Path", termstyle.Sanitize(path), 4)...)
	}
	if row.Description != "" && row.Description != transferPath(row) {
		lines = append(lines, previewKVLines(theme, width, "Info", termstyle.Sanitize(row.Description), 2)...)
	}
	lines = append(lines, previewKVLines(theme, width, "Action", transferActionLabel(row), 3)...)
	return lines
}

func transferIntentLabel(intent termnav.NavIntent) string {
	switch intent {
	case termnav.IntentDescend:
		return "dir"
	case termnav.IntentAscend:
		return "up"
	case termnav.IntentUseContainer:
		return "use"
	default:
		return "file"
	}
}

func transferTypeLabel(row termnav.Row) string {
	if b := strings.TrimSpace(row.Badge); b != "" {
		return strings.ToUpper(b)
	}
	if row.Group != "" {
		return row.Group
	}
	return transferIntentLabel(row.Intent)
}

func transferPath(row termnav.Row) string {
	if strings.TrimSpace(row.Detail) != "" {
		return row.Detail
	}
	if strings.TrimSpace(row.Description) != "" && strings.ContainsAny(row.Description, `/\`) {
		return row.Description
	}
	if strings.TrimSpace(row.Token) != "" {
		return row.Token
	}
	return ""
}

func transferActionLabel(row termnav.Row) string {
	switch row.Intent {
	case termnav.IntentUseContainer:
		return "Use the current folder"
	case termnav.IntentAscend:
		return "Move up one folder"
	case termnav.IntentDescend:
		return "Open this folder"
	case termnav.IntentSelectLeaf:
		return "Select this file"
	default:
		return "Select this entry"
	}
}

func transferBrowserModeLabel(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "local-file":
		return "choose local file"
	case "local-folder", "local-directory":
		return "choose local folder"
	case "remote-file":
		return "choose remote file"
	case "remote-folder", "remote-directory":
		return "choose remote folder"
	default:
		if mode == "" {
			return "browse files"
		}
		return mode
	}
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// clipTransferBrowserLines caps a block of rendered lines to budget, replacing
// the overflow with a "N more hidden" marker. Shared with the host chooser's
// preview pane.
func clipTransferBrowserLines(lines []string, budget int, width int, theme pickerTheme) []string {
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	if budget == 1 {
		return []string{theme.muted(termstyle.Truncate("more hidden", width))}
	}
	clipped := append([]string(nil), lines[:budget-1]...)
	clipped = append(clipped, theme.muted(termstyle.Truncate(fmt.Sprintf("%d more hidden", len(lines)-len(clipped)), width)))
	return clipped
}
