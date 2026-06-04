package ui

import (
	"context"
	"io"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

type ManagementChooserOptions struct {
	Input       io.Reader
	Output      io.Writer
	NoAltScreen bool
	NoColor     bool
	Theme       termstyle.Theme
	ThemeName   string
	ThemeFile   string
	Title       string
	Mode        string
	Steps       []string
	CurrentStep int
	Footer      string
	Summary     string
}

type ManagementItem struct {
	Kind        ItemKind
	Token       string
	Title       string
	Description string
	Detail      string
	Badge       string
	Group       string
	Action      string
}

func ChooseManagement(ctx context.Context, items []ManagementItem, opts ManagementChooserOptions) (ManagementItem, bool, error) {
	if len(items) == 0 {
		return ManagementItem{}, false, nil
	}

	model, err := newHostChooserModel(managementChooserItems(items), hostChooserBaseOptions{
		Input:       opts.Input,
		Output:      opts.Output,
		NoAltScreen: opts.NoAltScreen,
		NoColor:     opts.NoColor,
		Theme:       opts.Theme,
		ThemeName:   opts.ThemeName,
		ThemeFile:   opts.ThemeFile,
		Title:       opts.Title,
		Mode:        opts.Mode,
		Steps:       opts.Steps,
		CurrentStep: opts.CurrentStep,
		Footer:      opts.Footer,
		Summary:     opts.Summary,
		EmptyLabel:  "No matching choices",
	})
	if err != nil {
		return ManagementItem{}, false, err
	}

	final, err := runHostChooserModel(ctx, model, opts.Input, opts.Output)
	if err != nil {
		return ManagementItem{}, false, err
	}
	if final.canceled || final.selected < 0 {
		return ManagementItem{}, false, nil
	}
	item := final.items[final.selected]
	return ManagementItem{
		Kind:        item.StyleKind,
		Token:       item.Token,
		Title:       item.Title,
		Description: item.Description,
		Detail:      item.Detail,
		Badge:       item.Badge,
		Group:       item.Group,
		Action:      item.Action,
	}, true, nil
}

func managementChooserItems(items []ManagementItem) []hostChooserItem {
	out := make([]hostChooserItem, 0, len(items))
	for _, item := range items {
		kind := item.Kind
		if kind == "" {
			kind = ItemEdit
		}
		out = append(out, hostChooserItem{
			Kind:        string(kind),
			StyleKind:   kind,
			Token:       item.Token,
			Title:       item.Title,
			Description: item.Description,
			Detail:      item.Detail,
			Badge:       item.Badge,
			Group:       item.Group,
			Action:      item.Action,
		})
	}
	return out
}
