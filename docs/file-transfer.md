# File Transfer ("Beam")

> **Status: foundation in progress.** Phase 0 passive stream sniffing is
> implemented: supervised sessions observe OSC 7 cwd markers and OSC 133 prompt
> markers, persist the latest observed state on the session record, and show it
> in the session map. Public `send` / `receive` commands and the home-page
> **Send file** action now cover the direct SFTP path, including local and
> remote folder choosers plus a post-send confirmation for send. The session
> overlay has a direct-SFTP `send` action wired for interactive sessions.
> Overlay `receive` and deep in-band/wormhole transports are still design.

A single overlay action — **Beam file** — lets you pick a file anywhere on the
local machine and drop it into **the current working directory of the deepest
session in the stack**, no matter how many `ssherpa → ssh → ssherpa → ssh …`
hops deep you are.

The hard part is not the picker. It is that "the deepest session" and "its cwd"
are two things the supervisor does not currently know, and that the deepest
session may not be reachable by any out-of-band network path. This document
explains how we answer both questions and then transfer the bytes, degrading
from *fast and robust* to *always works* as the topology demands.

---

## Table of contents

- [The two questions](#the-two-questions)
- [Passive stream sniffing (the keystone)](#passive-stream-sniffing-the-keystone)
- [The three transports](#the-three-transports)
  - [Transport A — scp/sftp over the reused connection](#transport-a--scpsftp-over-the-reused-connection)
  - [Transport B — the embedded-wormhole beam](#transport-b--the-embedded-wormhole-beam)
  - [Transport C — hardened in-band stream](#transport-c--hardened-in-band-stream)
- [Transport selection](#transport-selection)
- [User experience](#user-experience)
- [Security model](#security-model)
- [Audit and session lineage](#audit-and-session-lineage)
- [Failure modes](#failure-modes)
- [Phasing](#phasing)
- [Open questions](#open-questions)

---

## The two questions

The supervisor (`internal/session/session.go`, `RunSupervised`) wraps **exactly
one** local `ssh` process in a PTY and proxies bytes both ways:

```
keyboard → copyInput → ptmx → ssh → … → deepest remote shell
deepest remote shell → … → ssh → ptmx → io.Copy → your screen
```

It knows the ssh argv, the target alias, the hops, and the route
(`session.Metadata`). It does **not** know:

1. **Is the deepest layer sitting at a shell prompt right now?** Or is it inside
   `vim`, a pager, a `sudo` password prompt, a TUI — somewhere that injected
   bytes would corrupt the display or, far worse, *be executed as a password or
   editor command*?
2. **What is the deepest shell's current working directory?**

Every transport gets better when these are answered, and the riskiest transport
(in-band injection) only becomes *safe* when question 1 is answered. So we solve
them first, and we solve them **passively** — the supervisor already reads every
byte the remote sends back.

---

## Passive stream sniffing (the keystone)

We add a small, allocation-light state machine that tees the remote→local byte
stream (the `io.Copy(output, ptmx)` at `session.go:215`) and watches for two
escape-sequence families. It **never injects** anything; it only observes.

### OSC 7 — working directory

Shells configured for it emit, on every prompt:

```
ESC ] 7 ; file://<host>/<path> BEL
```

Tracking the last one gives a live **last-known remote cwd** for free. This
answers question 2. Because escapes are rewritten as they pass up through each
nested layer, the *last* OSC 7 we see is the *innermost* shell's — exactly "the
end session."

### OSC 133 — prompt state (FinalTerm / shell integration)

Shells with shell integration emit prompt markers:

| Marker | Meaning |
| ------ | ------- |
| `ESC ] 133 ; A ST` | prompt start |
| `ESC ] 133 ; B ST` | prompt end — user input begins |
| `ESC ] 133 ; C ST` | command begins executing |
| `ESC ] 133 ; D[;exit] ST` | command finished |

If the last marker seen is `B` or `D`, the deepest shell is **idle at a
prompt** — safe to inject. If it is `C`, a command (possibly a full-screen TUI)
is running — unsafe. This is the **injection interlock**, and it is the single
most important safety property in this design.

### When the sniffers are blind

Neither sequence is universal; both depend on the remote shell's configuration
(kitty/iTerm/WezTerm shell integration, modern distro bash/zsh, fish, starship,
etc. emit them; a bare `sh` may not). When we have **not** seen the markers:

- cwd defaults to remote `$HOME` (or a path the user confirms);
- the prompt interlock falls back to an **explicit user confirmation** ("I am at
  a normal shell prompt") plus an optional active probe (inject a uniquely
  tagged `printf` and confirm we see it echoed back cleanly before proceeding).

The sniffers are independently useful beyond transfer: the session-map overlay
can show each layer's live cwd.

---

## The three transports

All three present **one** user-facing action. ssherpa picks the transport from
detected topology and capability, shows which one it chose and why, previews the
exact command, and only then proceeds.

### Transport A — scp/sftp over the reused connection

**Use when** the live session terminates at the host the local `ssh` actually
dialed: a direct `ssh host`, or `ssh -J jump… host`, with **no manual hops past
that host**. Detect by comparing the deepest session record's host against the
host in `command.Argv`, and/or the absence of deeper nested ssherpa layers
(`internal/state`).

**Mechanism.** Spawn `scp`/`sftp` as a separate process to the same alias. It
reads the same `~/.ssh/config`, so the route is reproduced for free. To avoid a
second authentication (password / 2FA) appearing *inside the overlay*, the
supervised ssh is launched with connection multiplexing so the transfer opens a
new channel on the **existing** socket:

```
-o ControlMaster=auto -o ControlPath=<per-session socket> -o ControlPersist=…
```

These are injected into the original invocation in the `sshcmd.Build*`
constructors (`internal/sshcmd/sshcmd.go`), gated to the supervised path, so the
control socket is guaranteed to exist when the user beams a file.

**Destination.** Default to the OSC-7 cwd (`sftp` `cd` then `put`), else remote
`$HOME`, else a confirmed path.

**Properties.** Binary-safe, fast, large files fine, zero shell pollution,
cannot be corrupted by a remote TUI, never touches the byte stream. This is the
**gold path** — strongly preferred whenever topology allows.

**Limitation.** Physically cannot reach a manually-hopped end session: the
local ssh terminates at the first hop / ProxyJump destination, and scp can only
reach what ssh can dial. For everything deeper, use B or C.

> A note on `kitten ssh`: when the supervised command is `kitten ssh`
> (`sshcmd.Resolve`), the transfer still uses plain `scp`/`sftp` against the
> alias — the kitty wrapper neither helps nor hinders the file copy.

### Transport B — the embedded-wormhole beam

This is the marquee path, and it is uniquely enabled by something ssherpa
already assumes.

**The insight:** do not push file bytes through the PTY. Push a tiny *command*
through the PTY, and send the bytes over the network via a relay.

**Mechanism.**

1. ssherpa **embeds a pure-Go magic-wormhole implementation**
   (`github.com/psanford/wormhole-william` — no cgo, small, matches the existing
   dependency profile). The **local** side needs no external install.
2. Two new subcommands, slotted into the dispatch at `internal/cli/cli.go`:
   `ssherpa send <file>` and `ssherpa recv [code]`, both backed by the embedded
   library.
3. On the beam action, the supervisor runs the **sender** out-of-band and
   obtains a short single-use code (e.g. `7-crossover-clockwork`) via SPAKE2.
4. Gated by the **OSC-133 prompt interlock**, the supervisor injects **one short
   line** into the deepest shell:
   `ssherpa recv -o '<dest>' <code>`. It runs *in that shell*, so the file lands
   in **its** cwd automatically — satisfying "the cwd of the end session" at any
   depth, through arbitrary manual hops.
5. Bulk data flows host ↔ relay ↔ remote, end-to-end encrypted (SPAKE2 + NaCl).
   **Nothing large touches the PTY**; no size limit; binary-safe; the screen
   stays clean.

**Why this is special for ssherpa specifically.**

- **The receiver is already installed.** Nested supervision *already requires
  ssherpa on every hop* — that is how the session lineage (`SSHERPA_DEPTH` /
  `ROUTE` / `PARENT`, see `state.EnvForRecord`) propagates. The same install
  that draws the session map provides `ssherpa recv`. **No chicken-and-egg, no
  external Python, no "is wormhole on this box."**
- **We own the relay configuration.** Default to the public rendezvous/transit
  relay, but expose `--rendezvous-url` / `--transit-relay` and a config setting
  so regulated environments self-host. On-brand with ssherpa's "everything
  previewable, OpenSSH stays the source of truth" stance.
- **It composes with the escape rope.** The transfer is a separate process plus
  a one-line injection; pulling the rope tears everything down regardless.

**Hard requirement.** The deepest host needs outbound reachability to the
rendezvous/transit relay. Bastion-isolated hosts often do not have it — and that
is exactly the deep-hop case. So B is preferred for *deep but
internet-connected* hosts, and C remains the offline floor.

> **Future flourish (Phase 4+):** if the deep host cannot reach a public relay
> but the local machine is reachable via SSH reverse port-forward, ssherpa can
> host a transit relay locally and forward it down the existing tunnel —
> turning the SSH chain itself into the relay path. `wormhole-william` supports
> custom transit, so this is mechanically possible; it is deferred for
> complexity.

### Transport C — hardened in-band stream

The universal floor. It **always works** — even fully air-gapped behind three
bastions with no internet — because the bytes ride the SSH channel itself. The
naive "base64 heredoc" is fragile; this is the version that is actually robust.

**Preconditions.** Proceed only if the OSC-133 interlock says we are idle at a
prompt (or the user explicitly confirms and an active probe succeeds). Refuse
above a size cap (target ~2–5 MB) and recommend A or B instead.

**Protocol.**

1. **Capability probe.** One injected line whose echoed sentinel we parse from
   the stream, confirming `base64`, a checksum tool (`sha256sum` / `shasum` /
   `openssl`), and that the tty is toggleable.

2. **Harden the tty and arm a length-bounded reader**, all in one pipeline so
   cleanup always runs:

   ```sh
   ( stty -echo -ixon -icanon 2>/dev/null;
     head -c <B64LEN> | base64 -d > "$tmp";
     stty sane 2>/dev/null )
   ```

   - `-echo` stops the payload from bouncing back up the stream (no screen spew,
     no self-inflicted flood).
   - `-ixon` disables XON/XOFF flow control so no byte is interpreted as
     `Ctrl-S`/`Ctrl-Q`.
   - `head -c <B64LEN>` provides **deterministic framing**. A PTY never sends
     EOF, so the read is bounded by an exact byte count. The local side emits
     `base64 -w0` (no line wrapping) so `B64LEN = 4 * ceil(N / 3)` precisely.

3. **Stream** the base64 in chunks via `ptmx.Write`, relying on the PTY's write
   backpressure for pacing — the `os.File` write blocks when the line-discipline
   buffer fills, which is natural flow control. Modest chunk sizes.

4. **Commit and verify.** One more injected line, watched for in the stream:

   ```sh
   mv "$tmp" '<dest>' && printf 'SSHERPA_DONE %s %s\n' "$?" "$(sha256sum '<dest>' | cut -c1-64)"
   ```

   Compare the remote sha256 against the locally computed digest. On mismatch,
   report failure — never claim a success we did not verify.

5. **Always restore** the tty (the `( … )` subshell plus the trailing
   `stty sane`), and keep a one-key "reset terminal" injection available in case
   the user aborts mid-stream and the tty is left in `-echo`.

Filenames receive the existing single-quote escaping (`sshcmd.quoteArg`).

**Properties.** Binary-safe (base64), integrity-checked (sha256),
echo-suppressed, flow-controlled, exactly framed, prompt-gated. Genuinely good
for text and small binaries, and the only path that needs zero network and
nothing on the remote beyond a POSIX shell and `base64`.

---

## Transport selection

One action, auto-selected, always previewed:

| Condition (auto-detected) | Transport |
| ------------------------- | --------- |
| Live session ends at the dialed host (direct / ProxyJump), control socket available | **A — scp/sftp** |
| Deeper than the dialed host, `ssherpa recv` present and a relay reachable, prompt idle | **B — wormhole beam** |
| Any POSIX shell idle at a prompt, no relay reachable | **C — in-band stream** |
| Not at a prompt / inside a TUI | **Refuse**, explain the OSC-133 interlock |

The overlay states the chosen transport and the reason in plain language, e.g.:

- *"scp over the existing connection to `prod-db`."*
- *"Beaming via relay — injecting `ssherpa recv` into the shell 3 hops deep."*
- *"No relay reachable; streaming inline, 412 KB, sha256-verified."*

This transparency mirrors the dry-run diffs ssherpa already shows for config
edits.

---

## User experience

```
Ctrl-]  →  [Beam file]  →  local file picker  →  confirm destination
        →  ssherpa detects topology + capability
        →  shows chosen transport + exact command
        →  go (live progress)  →  result + sha256
```

**The local picker.** Reusing the bubbletea picker (`internal/ui/picker.go`,
`Pick`) for a *filesystem browser* has one wrinkle: bubbletea wants the alt
screen and owns I/O, which conflicts with the raw-mode, hand-rolled overlays
(`showSessionOverlay`, `showComposer`). Because the overlay already holds stdin
and the output lock while it is open, we can hand the floor to a short-lived
bubbletea program for the picker and then return to raw mode. Recommended split:

- **File picker** — hand off to bubbletea (fuzzy filter, preview, already
  built).
- **Transfer progress** — hand-rolled bottom frame in the style of
  `drawComposer` / `drawBottomFrame`, so it coexists with the live session.

---

## Security model

- **Injection interlock is mandatory.** Never inject without the OSC-133
  "at prompt" signal or an explicit confirmation. Injecting into a `sudo`/SSH
  password prompt or a remote `vim` is the catastrophic failure this design
  exists to prevent.
- **Wormhole codes are secrets.** Single-use, short-TTL (SPAKE2). Do not log
  them. The injected `recv` line lands in remote shell history — mitigate by
  prefixing the injected command with a space (`HISTCONTROL=ignorespace`) and/or
  passing the code via an fd/env rather than argv.
- **Relay trust.** Wormhole data is end-to-end encrypted, but offer a
  self-hosted rendezvous/transit relay for regulated environments, and document
  it.
- **Overwrite and path safety.** Confirm before clobbering an existing file;
  resolve `..`; honor the sniffed cwd but always show the absolute target before
  proceeding.
- **Never block the escape rope.** Transfers run asynchronously; pulling the
  rope (or any teardown signal in `forwardSignals`) aborts an in-flight
  transfer and returns control immediately.

---

## Audit and session lineage

Each transfer is recorded as a `state.SessionEvent` (`internal/state/state.go`)
on the session record — transport used, byte size, sha256, and destination path
— consistent with how `latency_warning`, `latency_disconnect`, and
`escape_rope` events are logged today. Transfers become part of the session's
recorded lineage rather than an invisible side effect.

---

## Failure modes

- **No shell integration (OSC 7 / 133 absent):** cwd falls back to `$HOME` or a
  confirmed path; the prompt interlock falls back to explicit confirmation plus
  an active probe. Transport B/C still work; they just ask more of the user.
- **Deepest layer is in a TUI / password prompt:** the interlock refuses to
  inject (B and C). Transport A is unaffected (it never touches the shell).
- **Manual hop chain, no relay reachability:** A cannot reach the end session
  and B's relay is blocked → fall back to C, which rides the SSH channel.
- **Remote lacks `base64`:** C's capability probe fails; surface the reason and
  recommend B (or A where applicable). A pure-shell decoder fallback is possible
  but slow — out of scope for v1.
- **`ssherpa` absent on the deep host:** B is unavailable (no receiver); fall
  back to C. This is the same install assumption the session map already
  depends on, so a populated session map implies B is viable.
- **Transfer interrupted mid-stream (C):** the tty may be left in `-echo`; the
  one-key reset injection (`stty sane`) recovers it, and the temp file is left
  behind un-committed (never `mv`-d over the destination) so nothing is half
  written at the target path.
- **Escape rope pulled during a transfer:** the transfer process is killed and
  the session tears down; partial data may remain on the remote (temp file for
  C, partial `recv` for B) but the target path is only written on verified
  completion.

---

## Phasing

0. **Sniffers.** OSC 7 (cwd) and OSC 133 (prompt state) passive trackers on the
   output stream. Low risk, independently useful, the foundation for everything.
   **Implemented.**
1. **Transport A.** scp/sftp over an auto-enabled `ControlMaster` socket, with
   the local picker and destination confirmation. Fast value, common topology.
   **Partially implemented:** public `send` / `receive` SFTP commands and the
   home-page **Send file** action exist, with local and remote folder choosers
   plus a post-send confirmation for send. The `Ctrl-]` overlay can launch send
   against the current interactive session and start the remote picker from the
   tracked remote cwd. ControlMaster reuse is still pending.
2. **Transport C.** The hardened in-band stream as the universal fallback, gated
   by the Phase-0 interlock.
3. **Transport B.** Embed `wormhole-william`; add `ssherpa send` / `ssherpa
   recv`; wire the inject-the-receiver orchestration and relay configuration.
   The marquee feature.
4. **Unify** under one "Beam file" action with auto-selection and previews.
   Optional: local-relay-over-reverse-forward for air-gapped deep hosts.

**Rationale.** B is the genuinely novel piece — the only design that cleanly
hits "anywhere → the cwd of the deepest session," uniquely enabled by ssherpa's
every-hop install model. A is the fastest win; C is the floor that makes the
feature *trustworthy* because it always works. Build A → C → B; lead with B.

---

## Open questions

- **Reverse direction (remote → local "grab"):** the same machinery runs
  backwards (sender on the remote, `ssherpa recv` locally, or `scp` pull). Worth
  designing the action symmetrically from the start?
- **Multiple files / directories:** picker multi-select + `tar` framing for C,
  native directory support for A (`scp -r`) and B (wormhole supports
  directories). v1 scope: single file?
- **Binary size / dependency cost of embedding `wormhole-william`:** spike
  before committing to B; confirm it stays cgo-free and within an acceptable
  binary-size delta.
- **Default relay:** ship pointing at the public rendezvous, or refuse B until
  the user configures a relay? Trade-off between out-of-box magic and not
  silently routing bytes through a third party.
- **`-e none` interaction:** if the supervised ssh adopts `-e none` (see
  `docs/escape-rope.md` "Optional hardening"), confirm it does not interfere
  with the injected receiver lines for B/C.
