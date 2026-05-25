# Phase 10: TUI Design Overhaul

Phase 10 makes the terminal UI feel like a deliberate SSH work surface
instead of a plain list.

## First Screen

The default picker now has:

- a stronger header and mode treatment;
- terminal-palette status and filter surfaces by default;
- clearer action and host grouping;
- higher-contrast selected rows;
- better spacing for host metadata;
- a wide-terminal selection preview;
- a strict `--no-color` fallback.

The keyboard flow is unchanged:

```text
type filter text
arrow keys move
enter select
q quit
```

## Overlays

The in-session map overlay and queued-input composer now share the same
visual language as the picker:

- colored title and help lines;
- calmer inactive/exited session rows;
- highlighted active/current session rows;
- styled composer buffer and hotkey labels.

The overlays are still local-only terminal output. They do not write
control bytes into the remote PTY beyond the explicit composer send
actions.

## Themes

Phase 10 now uses semantic roles instead of hardcoded colors. The
default `terminal` theme uses normal ANSI palette slots so the user's
terminal emulator supplies the actual colors. The previous RGB design
remains available as `vivid`.

Theme selection and overrides:

```sh
ssherpa --theme terminal
ssherpa --theme vivid
ssherpa --theme-file ~/.config/ssherpa/theme.conf
SSHERPA_THEME=vivid ssherpa
SSHERPA_THEME_FILE=/tmp/ssherpa-theme.conf ssherpa
```

Example config:

```text
theme = terminal
primary = cyan
secondary = blue
accent = yellow
muted = bright-black
foreground = default
selected = bold white
success = green
warning = yellow
danger = red
pill = bold reverse
```

Supported values include terminal color names, `bg-COLOR`, style tokens
such as `bold`, `dim`, `underline`, and `reverse`, or raw SGR codes.

## Layout Safety

The UI now uses ANSI-aware padding helpers so colored text does not break
row alignment. Tests cover:

- no-color picker output;
- colored picker output;
- terminal-palette default styling;
- custom theme role overrides;
- wide preview rendering;
- ANSI visible-width and padding behavior.

## Manual Check

```sh
SSHERPA_NO_ALT_SCREEN=1 ssherpa --no-color
SSHERPA_NO_ALT_SCREEN=1 ssherpa
SSHERPA_NO_ALT_SCREEN=1 ssherpa --theme vivid
SSHERPA_NO_ALT_SCREEN=1 ssherpa --theme-file ~/.config/ssherpa/theme.conf
ssherpa --select prod
```

Inside a supervised session, press `Ctrl-]` for the active session map
and `Ctrl-G` for the composer.
