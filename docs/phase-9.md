# Phase 9: Queued Input Composer

Phase 9 adds a local input composer for supervised sessions. It helps
with laggy connections by letting you compose text locally and send it
to the remote PTY as one intentional burst.

## Usage

Inside a supervised session:

```text
Ctrl-G    open composer
Enter     send buffer plus newline
Ctrl-G    send buffer without newline
Esc       cancel
Ctrl-C    cancel
Backspace edit buffer
Ctrl-U    clear buffer
```

`Ctrl-]` remains the active-session map overlay hotkey. Composer uses
`Ctrl-G` by default so the map remains stable.

Customize or disable it:

```sh
ssherpa --select prod --composer-key ctrl-r
ssherpa --select prod --no-composer
ssherpa jump --dest prod --hop bastion --composer-key ctrl-r
ssherpa proxy --select prod --no-composer
```

Composer flags are rejected with `--direct` because they require the
supervised PTY runner. `--composer-key` is also rejected with
`--no-composer`.

## Behavior

The composer intercepts input locally only after its hotkey is pressed.
Normal bytes outside composer mode still pass directly to the remote
PTY. While the composer is open, remote output is held behind the local
overlay writer lock so the local buffer is not overwritten by remote
terminal output.

The composer accepts printable ASCII and tab. It does not send the
composer hotkey, editing keys, or cancel key to the remote PTY.

## Safety

- The feature is supervised-mode only.
- It can be disabled completely with `--no-composer`.
- `Ctrl-]` stays dedicated to the active route map.
- Reserved keys such as `Ctrl-C`, `Esc`, Enter, Backspace, and `Ctrl-]`
  cannot be configured as the composer hotkey.

## Limits

Composer mode is intended for shell input and simple line-oriented
commands. Full-screen remote programs should keep working because normal
input is untouched unless the composer hotkey is pressed, but a composer
overlay is still local terminal output and may be repainted by remote
programs after it closes.
