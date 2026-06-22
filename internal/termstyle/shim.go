package termstyle

import "github.com/0xbenc/termtheme"

// This package is ssherpa's thin adapter over the shared termtheme engine. The
// cross-compat-critical data layer — the semantic roles, the theme.conf parser,
// the style-spec interpreter, and the SGR/grapheme-cluster render helpers — is
// re-exported from termtheme so every app speaks the same theme format and a
// theme file interchanges across apps. ssherpa keeps its own builtin palettes,
// Theme resolution, and config-path/env handling local, since those
// legitimately differ per app.
//
// Note the role set: ssherpa renders 15 roles. RoleSelectedBar is a recognized
// universal role (so a passage theme parses and round-trips losslessly) but is
// deliberately absent from Roles(), because ssherpa paints no selection bar.

// Role is the shared semantic styling slot.
type Role = termtheme.Role

const (
	RoleTitle       = termtheme.RoleTitle
	RolePrimary     = termtheme.RolePrimary
	RoleSecondary   = termtheme.RoleSecondary
	RoleAccent      = termtheme.RoleAccent
	RoleMuted       = termtheme.RoleMuted
	RoleSubtle      = termtheme.RoleSubtle
	RoleForeground  = termtheme.RoleForeground
	RoleSelected    = termtheme.RoleSelected
	RoleSelectedBar = termtheme.RoleSelectedBar
	RoleBorder      = termtheme.RoleBorder
	RoleSuccess     = termtheme.RoleSuccess
	RoleWarning     = termtheme.RoleWarning
	RoleDanger      = termtheme.RoleDanger
	RoleInfo        = termtheme.RoleInfo
	RoleSearch      = termtheme.RoleSearch
	RolePill        = termtheme.RolePill
)

// ThemeConfig is the parsed theme file (base name + per-role overrides).
type ThemeConfig = termtheme.ThemeConfig

// Engine functions re-exported from termtheme. Sharing these is what makes a
// theme written by any sibling app parse and render identically here.
var (
	ParseThemeConfig = termtheme.ParseThemeConfig
	ParseStyleSpec   = termtheme.ParseStyleSpec
	Apply            = termtheme.Apply
	VisibleWidth     = termtheme.VisibleWidth
	Strip            = termtheme.Strip
	Sanitize         = termtheme.Sanitize
	PadRight         = termtheme.PadRight
	Truncate         = termtheme.Truncate
	TruncateWith     = termtheme.TruncateWith
)

// ThemeMeta is the header/version info recovered from a portable .theme file.
type ThemeMeta = termtheme.Meta

// ExportTheme serializes the resolved theme to the portable .theme format. It
// dumps ssherpa's 15 rendered roles plus any role the config carries that
// ssherpa does not paint (e.g. selected_bar from a passage theme), so a
// round-trip is lossless. ssherpa Theme and termtheme.Theme share an identical
// layout (Role is a type alias), so the conversion is free.
func ExportTheme(cfg ThemeConfig, base Theme, app, version string) []byte {
	return termtheme.Marshal(cfg, termtheme.Theme(base), termtheme.MarshalOptions{
		App:        app,
		AppVersion: version,
		Roles:      Roles(),
	})
}

// ImportTheme parses a portable .theme file into a config plus its metadata.
var ImportTheme = termtheme.Unmarshal
