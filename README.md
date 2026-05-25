# ssherpa

`ssherpa` is a Go port of the Bash Zoo SSH helper. The port keeps
OpenSSH config as the source of truth while growing toward a safer,
testable SSH workflow tool.

Current status: Phase 10 TUI design overhaul. The repository has a Go module,
tested SSH config inventory, `ssherpa list`, `ssherpa show`, a Bubble Tea
alias picker, print mode, direct SSH execution, safe add/edit/delete
config mutations, jump routing, SOCKS proxy launching, safe
`authorized_keys` management, supervised PTY sessions by default with
local session records, a session route map, an upgraded first-screen
picker, opt-in sidecar latency warnings, explicit latency disconnects,
a local queued input composer, a responsive styled TUI, CI, contributor
notes, terminal-palette-first theme support with a live TUI editor, and
a draft GoReleaser config with publishing disabled.

The implementation plan lives in [`PORT_PLAN.md`](PORT_PLAN.md).

## What Works Now

```sh
go run ./cmd/ssherpa version
go run ./cmd/ssherpa list --json
go run ./cmd/ssherpa show ALIAS --json
go run ./cmd/ssherpa --print --select ALIAS
go run ./cmd/ssherpa add --alias ALIAS --host HOST --dry-run
go run ./cmd/ssherpa edit set ALIAS --host HOST --dry-run
go run ./cmd/ssherpa edit delete ALIAS --dry-run
go run ./cmd/ssherpa jump --dest DEST --hop HOP --print
go run ./cmd/ssherpa proxy --select ALIAS --port 1080 --print
go run ./cmd/ssherpa authkeys list --json
go run ./cmd/ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub --dry-run
go run ./cmd/ssherpa --select ALIAS
go run ./cmd/ssherpa theme
go run ./cmd/ssherpa session list
go run ./cmd/ssherpa session map
go run ./cmd/ssherpa session map --all
go run ./cmd/ssherpa session show SESSION_ID
go test ./...
go vet ./...
```

`ssherpa` without a command opens the Bubble Tea alias picker and runs
the selected alias with local OpenSSH. SSH config inventory supports
`Include`, source positions, duplicate warnings, wildcard hiding,
git-user hiding, and basic parsed effective values.

Config mutation uses dry-run diffs, backups for writes to existing
files, temp-file atomic renames, permission preservation, and safeguards
for multi-alias or wildcard `Host` stanzas. Jump/proxy flows are
available. `authorized_keys` management supports list, add, merge,
replace, delete, dry-run diffs, backups, option preservation, cert key
types, and `SSHERPA_AUTHORIZED_KEYS_PATH`. Connection flows run under a
supervised PTY by default, propagate basic `SSHERPA_SESSION_*` metadata
into the child process, and write JSON records under the platform state
directory. The default picker opens with a styled status surface,
grouped actions, host rows, a responsive detail preview on wider
terminals, and a Sessions route map entry. While inside a
supervised session, press `Ctrl-]` to open the local active-session map
overlay; press `Ctrl-]`, `q`, or `Esc` to return to the remote session.
From the overlay, press `X` (then `X` again to confirm) to pull the
**escape rope**, which tears down every nested supervised session at once
and drops you back at the outermost shell (exit code 120); mash `Ctrl-]`
three times for a no-confirm panic exit. See `docs/escape-rope.md`.
`ssherpa session map` shows active sessions by default; use
`ssherpa session map --all` only when you want historical exited records
in the lineage view. Press `Ctrl-G` to open the local queued-input
composer; Enter sends the buffer plus a newline, `Ctrl-G` sends without
a newline, and `Esc` cancels. Use `--no-composer` to disable it or
`--composer-key KEY` to choose a different control-key hotkey. Use
`--latency-warn DURATION` to enable the opt-in sidecar SSH health probe
and record local warning events. Use `--latency-disconnect DURATION`
only when you explicitly want ssherpa to terminate a session after
sustained unhealthy probes; it requires `--latency-warn`. Use
`--direct` only when you need the old unsupervised runner. Use
`--state-dir PATH` or `SSHERPA_STATE_DIR` for disposable testing.

## UI Themes

The TUI defaults to the terminal palette, so `primary`, `secondary`,
`accent`, and status colors inherit the user's terminal emulator theme.
Use the `Theme and colors` row on the first screen, or run
`ssherpa theme`, to build a schema with a live picker/overlay preview.
Use `--theme vivid` or `SSHERPA_THEME=vivid` for the explicit truecolor
palette from the Phase 10 design pass.

Custom role overrides can live at `~/.config/ssherpa/theme.conf`, or in
a path set with `--theme-file PATH` or `SSHERPA_THEME_FILE=PATH`:

```text
theme = terminal
primary = cyan
secondary = blue
accent = yellow
muted = bright-black
success = green
warning = yellow
danger = red
pill = bold reverse
```

Values may be terminal color names, style tokens such as `bold` and
`reverse`, or raw SGR codes such as `38;2;96;221;255`. `--no-color`,
`SSHERPA_NO_COLOR=1`, and `NO_COLOR=1` disable styling.

Theme editor keys:

```text
arrows / h l  change selection or value
b             switch base theme
e / enter     edit the selected role as raw text
d             clear a role override so it inherits from the base theme
r             reset to terminal defaults
s             save
q / Esc       cancel
```

## Examples

```sh
ssherpa list --json
ssherpa list --json --config ~/.ssh/config.work
ssherpa list --all --filter prod --user alice
ssherpa show prod --json
ssherpa --print --select prod -- -L 8080:localhost:8080
ssherpa --select prod --ssh-binary /tmp/fake-ssh
ssherpa add --alias prod --host prod.example.com --user alice --dry-run
ssherpa add --alias prod --host prod.example.com --user alice --yes
ssherpa edit set prod --port 2222 --identity ~/.ssh/prod --yes
ssherpa edit delete prod --all-sources --dry-run
ssherpa edit delete-all --filter scratch --dry-run
ssherpa jump --dest prod --hop bastion --hop edge --print
ssherpa proxy --select prod --bind 127.0.0.1 --port 1080 --print
ssherpa authkeys list --json
ssherpa authkeys add --key "ssh-ed25519 AAAA... user@host" --yes
ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub --dry-run
ssherpa authkeys merge --from-dir ./keys --dry-run
ssherpa authkeys replace --from-dir ./keys --yes
ssherpa authkeys delete --fingerprint SHA256:... --yes
ssherpa --select prod
ssherpa --select prod --composer-key ctrl-r
ssherpa --select prod --no-composer
ssherpa --select prod --latency-warn 2s
ssherpa --select prod --latency-warn 2s --latency-disconnect 30s
ssherpa theme
ssherpa --theme vivid
ssherpa --theme-file ~/.config/ssherpa/theme.conf
ssherpa --direct --select prod
ssherpa jump --dest prod --hop bastion
ssherpa session list
ssherpa session map
ssherpa session map --all
ssherpa session map --json
ssherpa session show 20260524T120000.000000000Z-abcd1234
ssherpa session prune --older-than 168h --dry-run
```

Default inventory reads `~/.ssh/config`. Use `--config PATH` for a
different root. Authorized key operations read
`~/.ssh/authorized_keys` by default. Use `SSHERPA_AUTHORIZED_KEYS_PATH`
or `--path PATH` to operate on a disposable file.

Session records default to `~/.local/state/ssherpa` on Linux and
`~/Library/Application Support/ssherpa` on macOS. They are local JSON
files with mode `0600`.

## Compatibility Reference

Until the Go port reaches parity, the Bash Zoo implementation remains
the behavior reference:

- Local sibling checkout: `../bash-zoo/scripts/ssherpa.sh`
- Upstream source:
  https://github.com/0xbenc/bash-zoo/blob/main/scripts/ssherpa.sh
- Bash Zoo user docs:
  https://github.com/0xbenc/bash-zoo/blob/main/docs/ssherpa.md

## Development

This workspace uses an official user-local Go install rather than the
Ubuntu package:

- Go version: `go1.26.3`
- Install root: `~/.local/share/go/1.26.3`
- PATH symlinks: `~/.local/bin/go`, `~/.local/bin/gofmt`

See [`docs/development.md`](docs/development.md) for the bootstrap and
upgrade process, and [`CONTRIBUTING.md`](CONTRIBUTING.md) for the local
checks expected before commit.
