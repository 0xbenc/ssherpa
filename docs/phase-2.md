# Phase 2 Picker And Direct Connect

Phase 2 added the interactive alias picker, print mode, direct SSH
execution, and command resolution. Config mutation arrived in Phase 3;
jump/proxy flows arrived in Phase 4, `authorized_keys` arrived in
Phase 5, and supervised PTY sessions remain a later phase.

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
script. Add/edit rows are wired in Phase 3, jump/proxy rows are wired in
Phase 4, and authkeys is wired in Phase 5.

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

- Authkeys is implemented in Phase 5. Proxy and jump are implemented in
  Phase 4.
- The default direct runner does not yet supervise sessions or record
  state.
- Signal-forwarding behavior is the standard `exec.Command` inherited
  terminal behavior for now; explicit supervisor behavior starts in the
  PTY/session phase.
