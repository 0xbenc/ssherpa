# Phase 7: Session UX And First Screen

Phase 7 makes the default interactive entry point feel like a real SSH
work surface instead of a plain list. It also adds the first route-map
view for supervised sessions.

## First Screen

Running `ssherpa` with no command opens an upgraded picker:

```sh
ssherpa
```

The first screen now shows:

- current mode: supervised, direct, or print;
- host count and warning count;
- active supervised-session count;
- config root path;
- grouped action rows;
- grouped host rows;
- a Sessions and route map action.

The picker still supports fuzzy typing, arrow navigation, cancellation,
`SSHERPA_NO_ALT_SCREEN=1`, and `--no-color`. Non-interactive `--select`
behavior is unchanged.

## Session Map

The new map command renders parent/child session lineage from local
state:

```sh
ssherpa session map
ssherpa session map --all
ssherpa session map --json
ssherpa session map --state-dir /tmp/ssherpa-state
```

Text output is intended for quick inspection:

```text
Session route map
state: /tmp/ssherpa-state
active: 1

+- prod [active] depth=1 id=child
   route: bastion -> prod
```

The default map is a live route view and only shows active sessions. Use
`--all` when you need historical exited records. JSON output keeps the
visible records and nested children:

```json
{
  "state_dir": "/tmp/ssherpa-state",
  "scope": "active",
  "total": 1,
  "active": 1,
  "recorded": 2,
  "roots": []
}
```

## Picker Session Entry

The top-level picker includes `Sessions and route map`. Selecting it
prints the same text map as:

```sh
ssherpa session map
```

This keeps the session view scriptable while still making it visible
from the default interactive flow.

## In-Session Overlay

Supervised sessions now have a local hotkey:

```text
Ctrl-]
```

Pressing `Ctrl-]` inside an active connection opens the local route-map
overlay. Connection flows are supervised by default as of Phase 7; use
`--direct` only to bypass session state and the overlay. The hotkey is
intercepted by ssherpa and is not sent to the remote PTY. While the
overlay is open:

```text
Ctrl-]  close
q       close
Esc     close
r       refresh
```

The overlay renders active local session state. It marks the current
session and blocks remote output from drawing over the overlay until the
overlay closes.

## Verification

```sh
go test ./...
go vet ./...
go build -trimpath -o ssherpa ./cmd/ssherpa
```

Use a disposable fake SSH binary to generate a record:

```sh
tmp="$(mktemp -d)"
{
  printf '%s\n' '#!/bin/sh'
  printf '%s\n' 'printf "fake supervised ssh: %s\n" "$*"'
  printf '%s\n' 'exit 0'
} > "$tmp/fake-ssh"
chmod +x "$tmp/fake-ssh"

go run ./cmd/ssherpa \
  --state-dir "$tmp/state" \
  --ssh-binary "$tmp/fake-ssh" \
  --select prod \
  --config internal/sshconfig/testdata/matrix/config

go run ./cmd/ssherpa session map --state-dir "$tmp/state"
go run ./cmd/ssherpa session map --all --state-dir "$tmp/state"
go run ./cmd/ssherpa session map --json --state-dir "$tmp/state"
```

## Known Limits

- The default map is built from active local session records. It does
  not inspect remote process trees. Use `--all` for historical lineage.
- The in-session overlay is local terminal output. Full-screen remote
  programs may repaint after the overlay closes.
- Latency monitoring and queued input remain later phases.
