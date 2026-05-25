# Escape Rope

The escape rope is a single deliberate action that disconnects **every
nested supervised session at once** — no matter how many `ssherpa → ssh →
ssherpa → ssh …` layers deep you are — and drops you back at the outermost
shell.

It runs entirely in the local supervisor. ssh never sees the keystrokes
that trigger it, and it never injects commands into any remote shell.

## Usage

In any supervised session (`ssherpa --select`, `ssherpa jump`, …):

1. Press **`Ctrl-]`** to open the session overlay.
2. Press **`X`** — "escape rope (quit all layers)".

The outermost session tears down immediately. ssherpa prints

```
ssherpa: escape rope pulled — disconnecting all downstream sessions
```

and exits with code **120** (`session.EscapeRopeExitCode`) so a wrapper
can tell a deliberate bail-out apart from a normal logout or an ssh error
(255). The torn-down session is recorded with `disconnect_reason:
"escape_rope"`.

## How it works

Two existing properties of supervised mode do all the work.

### 1. Keystrokes are intercepted outermost-first

In supervised mode ssherpa runs ssh under a PTY and reads your input one
byte at a time (`internal/session/session.go`, `copyInput`). Control
chords like `Ctrl-]` are consumed locally and **never forwarded to ssh**.

Your bytes pipeline outermost-first:

```
keyboard → L0 copyInput → ssh→A → A shell → L1 copyInput → ssh→B → …
```

So the **outermost ssherpa is always the first program to see the chord.**
When it intercepts `Ctrl-]`+`X` and does not forward it, only the top
layer reacts.

### 2. Connection teardown cascades downward for free

When an ssh *client* process dies, its TCP connection drops. The remote
`sshd` sends `SIGHUP` to that session's process group, which kills the
remote `ssherpa` and *its* ssh child, dropping the next connection, and so
on down the stack. You do not have to reach down the chain with a message
— the kernel and `sshd` do it.

```
L0 ── X pressed: SIGHUP its ssh→A child  →  connection drops
      A's sshd HUPs L1  →  L1's ssh→B dies  →  B HUPs L2  →  … collapse
```

So the whole feature is: **catch the chord at the top, kill the top ssh
child, let SIGHUP roll downhill.** No upward signalling, no cross-host
protocol, no dependence on the session-lineage metadata.

The teardown marks the record (`DisconnectReason = "escape_rope"` plus an
`escape_rope` session event for audit), then `SIGHUP`s the child's whole
**process group** — under a PTY the child is a session leader, so the
group also covers a wrapper like `kitten ssh` and the ssh it forks, which
a single-PID signal would orphan. If the group has not exited after a
short grace (`escapeRopeKillGrace`, 750 ms) it is `SIGKILL`ed, guaranteeing
a prompt local return even if ssh ignores the hangup. The normal exit path
then restores the terminal and writes the final record.

## The boundary: from the outermost ssherpa, downward only

ssherpa can only tear down **from the outermost ssherpa down.** Anything
*above* your first ssherpa is out of reach — a process cannot kill a parent
it did not spawn.

- First hop launched with ssherpa → the rope returns you to your laptop.
- First hop was plain `ssh`, ssherpa only deeper → plain ssh just forwards
  the chord to the first ssherpa, which becomes the effective top; the rope
  returns you to that plain-ssh shell, not the laptop.

**Practical rule: launch your first hop through ssherpa and every layer is
reachable.**

## Failure modes

- **`SIGHUP`-ignoring layers** (`tmux`, `screen`, `nohup`, `disown`): the
  cascade stalls there and anything below survives as a detached
  server-side session. This is arguably correct — you would not want the
  rope to nuke a detached multiplexer.
- **Black-holed TCP:** the rope guarantees your **local** return is instant
  (killing the local ssh client is a local operation). Remote orphans are
  reaped best-effort by `ServerAliveInterval` / `ServerAliveCountMax`.
- **Non-supervised (direct) layers:** they cannot intercept the chord, but
  the `SIGHUP` cascade still flows *through* them. Only the outermost layer
  needs to be supervised for the rope to exist.
- **Lineage metadata is for display only:** `SSHERPA_DEPTH` / `ROUTE` /
  `PARENT` (`state.EnvForRecord`) only cross the ssh boundary if
  `SendEnv`/`AcceptEnv SSHERPA_*` is configured, so the overlay's view may
  not see remote depth. The rope's teardown is physical and does **not**
  depend on this.

## Optional hardening (not yet implemented)

- **`-e none` on supervised ssh.** The rope chord is `Ctrl-]` (`0x1d`),
  which ssh's `~`-based escape never consumes, so this is not required for
  correctness. Adding `ssh -e none` would make ssherpa the sole in-band
  controller and remove any chance of ssh's own `~.` interfering. It must
  be injected **before the destination** in the argv (ssh ignores options
  after the hostname) and **only in the supervised path** — direct mode
  relies on `~.` as the user's only escape. The natural place is the
  `sshcmd.Build*` constructors, gated to supervised use.
- **Panic triple-tap.** A `Ctrl-]` ×3 within ~500 ms could fire the rope
  immediately without opening the overlay, for when a layer is wedged and
  you cannot see. Single tap stays the overlay.
- **Cascade byte.** Forward a sentinel byte downstream so each ssherpa
  independently marks `escape_rope` and tears itself down even where
  `SIGHUP` is blocked. Trade-off: forwarding then killing races the
  disconnect, so it needs a brief flush/grace and is slightly slower than
  the `SIGHUP`-only path.
