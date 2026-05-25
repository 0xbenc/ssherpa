# Phase 2 Picker And Direct Connect

Phase 2 adds the interactive alias picker, print mode, direct SSH
execution, and command resolution. It does not add config mutation,
jump/proxy flows, `authorized_keys`, or supervised PTY sessions.

## Commands

Interactive picker:

```sh
ssherpa [--all] [--filter SUBSTR] [--user USER] [--config PATH] [-- ssh-args...]
```

Print mode:

```sh
ssherpa --print --select prod
ssherpa --print --json --select prod
ssherpa --print --select prod -- -L 8080:localhost:8080
```

Non-interactive selection:

```sh
ssherpa --select prod
```

The `--select` flag is useful for tests, scripts, and CI. Without it,
`ssherpa` opens the Bubble Tea picker.

## Picker

The picker is implemented with Bubble Tea and prepends these synthetic
rows:

- Add new alias
- Edit aliases or delete
- Manage authorized_keys on this device
- Start SOCKS proxy
- Jump via intermediate hops

Those rows are visible so the default interaction matches the Bash
script, but selecting them returns a clear not-yet-implemented error
until later phases add the underlying flows.

Picker controls:

- Type to fuzzy-filter.
- Up/down or `ctrl+p`/`ctrl+n` to move.
- Enter to select.
- `q`, `esc`, or `ctrl+c` to cancel.

Set `SSHERPA_NO_ALT_SCREEN=1` to keep the picker out of the alternate
screen.

## SSH Command Resolution

Resolution order:

1. `--ssh-binary PATH`
2. `SSHERPA_SSH_BINARY`
3. Kitty-aware wrapper when inside Kitty:
   - `kitten ssh`
   - `kitty +kitten ssh`
4. Plain `ssh`

Disable Kitty detection with `--no-kitty` or `SSHERPA_NO_KITTY=1`.

## Print Mode

Human print mode uses shell-safe quoting:

```text
[print] ssh prod -L 8080:localhost:8080
```

JSON print mode emits the argv array used internally:

```json
{"argv":["ssh","prod"],"alias":"prod"}
```

## Direct Runner

Direct execution uses `exec.Command` with inherited stdin/stdout/stderr
behavior and returns the SSH process exit code.

## Acceptance

```sh
go test ./...
go run ./cmd/ssherpa --print --select prod --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa --print --json --select prod --config internal/sshconfig/testdata/matrix/config
```

Fake SSH execution and exit-code propagation are covered by
`internal/cli` and `internal/sshcmd` tests.

## Known Limits

- Synthetic picker rows are placeholders until their implementation
  phases.
- The default direct runner does not yet supervise sessions or record
  state.
- Signal-forwarding behavior is the standard `exec.Command` inherited
  terminal behavior for now; explicit supervisor behavior starts in the
  PTY/session phase.
