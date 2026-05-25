# ssherpa Go Port Plan

Date: 2026-05-24

This is the implementation plan for turning the Bash Zoo `ssherpa`
script into a standalone Go project in this repository.

The original `bash-zoo/ssherpa-go-port.md` had the right direction, but
it was too high level for a safe port. The Bash implementation is not
just an SSH alias picker. It is also a config writer, edit/delete tool,
ProxyJump route builder, SOCKS proxy launcher, Kitty-aware SSH wrapper,
and `authorized_keys` manager. The Go port needs to treat those as real
product surface, preserve the parts users already rely on, and use Go to
make the risky parts safer.

## Executive Decisions

1. Keep OpenSSH as the source of truth.
   `ssherpa` should execute the local `ssh` binary against the selected
   alias. It should not become a native SSH protocol client in the first
   Go port. OpenSSH already owns host key policy, SSH agent behavior,
   PKCS11/FIDO support, ProxyJump, ControlMaster, certificates, config
   precedence, and user muscle memory.

2. Build compatibility before session-management features.
   The first shippable Go binary should replace the useful Bash behavior:
   list aliases, fuzzy-pick, print commands, connect, add/edit/delete
   aliases, jump, proxy, and manage `authorized_keys`. The session
   supervisor, latency warning, auto-disconnect, and queued input modes
   should land after the compatibility layer is stable.

3. Use a direct SSH runner by default, and a PTY runner only when needed.
   For normal connects, direct process execution with inherited
   stdin/stdout/stderr is simpler and closer to `ssh alias`. PTY wrapping
   is valuable for supervision and queued input, but it changes terminal
   behavior and should be opt-in until it is proven.

4. Treat SSH config mutation as a data-safety feature.
   Writing `~/.ssh/config` and `~/.ssh/authorized_keys` is the highest
   risk part of this project. The Go version should improve on Bash with
   parsed edits, backups, dry-run diffs, permissions handling, and tests
   around multi-alias stanzas.

5. Do not require Gum in the Go version.
   Bubble Tea/Bubbles/Lip Gloss should replace Gum for interactive UI.
   That removes a runtime dependency and makes releases simpler.

6. Be explicit about OpenSSH config limits.
   Full OpenSSH config evaluation is complex, especially `Match`,
   conditional `Include`, canonicalization, and first-value-wins behavior.
   `ssherpa` should enumerate aliases from parsed files, but effective
   connection details should be resolved with `ssh -G` when accuracy
   matters.

## Audit of the Current Bash Version

Source files reviewed:

- `bash-zoo/scripts/ssherpa.sh`
- `bash-zoo/docs/ssherpa.md`
- `bash-zoo/ssherpa-go-port.md`
- `bash-zoo/setup/registry.tsv`
- `bash-zoo/install.sh`
- `bash-zoo/scripts/bash-zoo.sh`

Current repository state:

- `ssherpa` is a new standalone repo with license, planning,
  development bootstrap docs, the Phase 0 Go module/CLI foundation, and
  Phase 1 read-only SSH config inventory commands. Phase 2 adds the
  Bubble Tea picker, print mode, direct SSH runner, Kitty command
  resolver, and fake SSH integration tests.
- Local `go version` was not available when this plan was written. The
  workspace is now bootstrapped with a user-local Go `1.26.3` install;
  see `docs/development.md`.
- Local OpenSSH is available and reports OpenSSH 9.6p1 on Ubuntu.

### Existing CLI Contract

Default mode:

```text
ssherpa [--all] [--print|--exec] [--filter SUBSTR] [--user USER]
        [--no-color] [--config PATH]
        [--] [ssh-args...]
```

Subcommands:

```text
ssherpa add [--alias NAME] [--host HOST] [--user USER] [--port 22]
            [--identity PATH] [--config PATH] [--dry-run] [--yes]

ssherpa edit [--config PATH] [--all] [--filter SUBSTR] [--user USER]

ssherpa authkeys
```

Important environment variables:

```text
SSHERPA_DEBUG
SSHERPA_IGNORE_USER_GIT
SSHERPA_AUTHORIZED_KEYS_PATH
KITTY_WINDOW_ID
KITTY_PID
TERM
```

### Current Behavior Inventory

| Area | Bash behavior | Go requirement | Improvement target |
| --- | --- | --- | --- |
| Config discovery | Starts at `~/.ssh/config`, breadth-first follows `Include`, avoids duplicates | Discover config graph from default or `--config` | Match OpenSSH include order more closely; track source file and line |
| Config parsing | Parses `Host`, `HostName`, `User`, `Port`, first `IdentityFile`; ignores `Match` | Parse enough for inventory and safe writes | Support `key=value`, quoted args, comments outside quotes, multiple identities, source positions |
| Alias list | Always prepends synthetic Add/Edit/Authkeys/Proxy/Jump rows | Preserve default interactive experience | Add non-interactive `list` and explicit `jump`/`proxy` subcommands |
| Pattern hosts | Hides `*` and `?` unless `--all` | Preserve | Also detect negated patterns and explain why they are hidden |
| Git users | Hides entries with `User git` unless `SSHERPA_IGNORE_USER_GIT=0` or `--user` is used | Preserve | Make this visible in `doctor` and docs |
| Selection UI | Gum filter | Bubble Tea/Bubbles list | No Gum runtime dependency; consistent help and cancellation |
| SSH command | Uses `kitten ssh` in Kitty when available, else `ssh` | Preserve command resolver | Add `--ssh-binary`, `--no-kitty`, and debug output |
| Print mode | Prints command with selected alias | Preserve | Use shell-safe quoting and exact argv display |
| Connect mode | Runs SSH and propagates exit status | Preserve | Improve signal forwarding and supervised mode separation |
| Add alias | Interactive 5-step form, optional IdentityFile, optional IdentitiesOnly | Preserve | Validate port range, alias syntax, host presence, and destination config |
| Edit alias | Pick alias, edit fields, or delete | Preserve | Avoid deleting unrelated aliases in multi-alias Host stanzas |
| Delete all | Deletes all listed aliases | Preserve only with strong confirmation | Add dry-run diff and backup for every touched file |
| Jump mode | Pick destination, then one or more hops, run `ssh -J hop1,hop2 dest` | Preserve | Add explicit `ssherpa jump`; validate no duplicate hops |
| Proxy mode | Prompt port, pick host, run `ssh -D port -C -N host` | Preserve | Bind localhost by default; add `ExitOnForwardFailure`; validate port range |
| Authkeys | Add single key, merge from dir, replace from dir, delete keys | Preserve | Preserve authorized_keys options; support cert key types; always backup on destructive writes |
| No aliases | Offers Add, Manage authorized_keys, Learn more, Exit | Preserve | Keep help in built-in view; add `doctor` for setup issues |

### Gaps in the Original Port Note

The old plan correctly named Go, Bubble Tea, PTY support, and packaging,
but it missed several details needed before implementation:

- No precise compatibility contract for the existing CLI.
- No inventory of current helper flows.
- No config writer safety model.
- No treatment of OpenSSH first-value-wins semantics.
- No decision between parsing config directly and asking `ssh -G`.
- No handling for `authorized_keys`.
- No migration path from Bash Zoo's installer/update model.
- No risk plan for PTY wrapping interactive SSH.
- No schema for session state, route metadata, latency, or queued input.
- No test fixture matrix.

## Product Shape

`ssherpa` should become a practical SSH workflow tool with three layers:

1. Alias workflow layer.
   Find, filter, inspect, create, edit, delete, and connect to OpenSSH
   aliases.

2. Connection helper layer.
   Build common OpenSSH invocations for ProxyJump routes and SOCKS
   proxies without hiding the actual SSH command.

3. Optional session layer.
   Supervise local SSH processes, record route metadata, warn on degraded
   latency, and provide lag-friendly input composition.

The first two layers should work without any remote changes. The session
layer may use environment propagation when available, but must remain
best effort because remote SSH servers may reject client-provided
environment variables.

### Non-Goals

- Do not reimplement interactive SSH using `golang.org/x/crypto/ssh` in
  the initial port.
- Do not parse or mutate system-wide `/etc/ssh/ssh_config` by default.
- Do not silently rewrite large user config files without backups.
- Do not require users to modify remote shell startup files for basic
  functionality.
- Do not make auto-disconnect a default behavior.
- Do not add known_hosts browsing in the first parity release. The Bash
  script intentionally stays alias-only.

## Proposed Command Surface

Keep the current default command behavior:

```text
ssherpa
ssherpa --print
ssherpa --filter prod --user alice
ssherpa --all
ssherpa --config ~/.ssh/config.work
ssherpa -- -L 8080:localhost:8080
```

Keep current subcommands:

```text
ssherpa add
ssherpa edit
ssherpa authkeys
```

Add explicit commands for flows that currently exist only as synthetic
picker rows:

```text
ssherpa jump [flags] [-- ssh-args...]
ssherpa proxy [flags] [-- ssh-args...]
```

Add inspection and support commands:

```text
ssherpa list [--json] [--all] [--filter SUBSTR] [--user USER]
ssherpa show ALIAS [--json]
ssherpa doctor
ssherpa version
```

Add session commands after the supervised runner exists:

```text
ssherpa session list
ssherpa session show SESSION_ID
ssherpa session prune
```

### Flag Contract

Compatibility flags:

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--all` | picker, list, edit, jump, proxy | Include wildcard or pattern hosts |
| `--print` | picker, jump, proxy | Print the resolved command instead of running |
| `--exec` | picker, jump, proxy | Run the command; default for picker |
| `--filter SUBSTR` | picker, list, edit, jump, proxy | Pre-filter aliases |
| `--user USER` | picker, list, edit, jump, proxy | Filter by parsed/effective user |
| `--config PATH` | all config flows | Use this config root instead of `~/.ssh/config` |
| `--no-color` | all UI output | Disable color styling |
| `--dry-run` | mutations | Show what would change |
| `--yes`, `-y` | mutations | Skip confirmation where safe |

New flags:

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--json` | list, show, doctor, session | Emit machine-readable output |
| `--ssh-binary PATH` | connect flows | Use a specific SSH binary |
| `--no-kitty` | connect flows | Disable Kitty `kitten ssh` detection |
| `--bind ADDR` | proxy | SOCKS bind address; default `127.0.0.1` |
| `--port PORT` | proxy, add | Local proxy port or SSH port depending on command |
| `--supervise` | connect flows | Use PTY runner and record live session state |
| `--latency-warn DURATION` | supervised connect | Warning threshold |
| `--latency-disconnect DURATION` | supervised connect | Opt-in disconnect threshold |
| `--state-dir PATH` | session flows | Override state directory |

### Environment Contract

Preserve existing variables:

| Variable | Meaning |
| --- | --- |
| `SSHERPA_DEBUG` | Enables debug logging |
| `SSHERPA_IGNORE_USER_GIT` | Defaults to hiding `User git`; set `0`, `false`, `no`, or `off` to include |
| `SSHERPA_AUTHORIZED_KEYS_PATH` | Overrides `~/.ssh/authorized_keys` |
| `KITTY_WINDOW_ID`, `KITTY_PID`, `TERM` | Used for Kitty SSH command detection |

Add new variables:

| Variable | Meaning |
| --- | --- |
| `SSHERPA_SSH_BINARY` | Default SSH binary override |
| `SSHERPA_NO_KITTY` | Disable Kitty command resolver |
| `SSHERPA_STATE_DIR` | Override session state directory |
| `SSHERPA_LOG_DIR` | Override debug/session log directory |
| `SSHERPA_NO_ALT_SCREEN` | Disable alternate-screen UI where possible |
| `SSHERPA_SESSION_ID` | Current supervised session ID |
| `SSHERPA_PARENT_SESSION_ID` | Parent session ID for nested route tracking |
| `SSHERPA_ROUTE` | Comma-separated route metadata |
| `SSHERPA_DEPTH` | Nested session depth |

Remote propagation of `SSHERPA_*` variables should be best effort via
OpenSSH options such as `SendEnv` and `SetEnv`, but it must not be
required for local functionality.

### Exit Codes

| Code | Meaning |
| --- | --- |
| `0` | Success or user-cancelled interactive flow |
| `1` | Usage error, validation error, or failed mutation |
| `2` | No matching hosts or parse/diagnostic issue that did not mutate files |
| SSH exit code | Connect flows should return the SSH process exit code |

Interactive cancellation currently returns success in most paths. Keep
that behavior for compatibility, but make `--json` and non-interactive
commands distinguish empty results cleanly.

## Architecture

Recommended layout:

```text
cmd/ssherpa/main.go
internal/app
internal/cli
internal/sshconfig
internal/hostlist
internal/sshcmd
internal/ui
internal/authkeys
internal/session
internal/state
internal/fsutil
internal/logging
internal/testutil
testdata/
docs/
```

Package responsibilities:

| Package | Responsibility |
| --- | --- |
| `cmd/ssherpa` | Thin entrypoint; version injection |
| `internal/app` | Application services and dependency wiring |
| `internal/cli` | Cobra command tree, flag parsing, help, completions |
| `internal/sshconfig` | Parse files, expand includes, build source graph, mutate config |
| `internal/hostlist` | Convert parsed config into user-facing aliases and filters |
| `internal/sshcmd` | Resolve `ssh`/`kitten ssh`, build argv, quote print output, run processes |
| `internal/ui` | Bubble Tea models for picker, forms, route builder, authkeys menu |
| `internal/authkeys` | Parse, validate, merge, replace, delete authorized_keys entries |
| `internal/session` | PTY runner, session lifecycle, route/depth metadata |
| `internal/state` | JSON state files, locks, pruning |
| `internal/fsutil` | Atomic writes, backups, permissions, path expansion |
| `internal/logging` | Debug logs and optional structured logs |
| `internal/testutil` | Fake HOME, fake ssh, golden fixtures, terminal harness helpers |

### Dependency Recommendation

Use a small dependency set and isolate it behind internal packages:

| Dependency | Use | Notes |
| --- | --- | --- |
| `charm.land/bubbletea/v2` | TUI runtime | Current Bubble Tea docs use the v2 import path |
| `github.com/charmbracelet/bubbles/v2` | List, text input, viewport, help, file picker | The list component already provides fuzzy filtering |
| `charm.land/lipgloss/v2` | Terminal styling | Use sparingly; honor `--no-color` |
| `github.com/spf13/cobra` | CLI command tree and completions | Do not add Viper; keep config explicit |
| `github.com/creack/pty` | Supervised PTY runner | Do not use for default direct connect until needed |

Dependency to evaluate but not blindly adopt:

| Dependency | Possible use | Concern |
| --- | --- | --- |
| `github.com/kevinburke/ssh_config` | Parsing and comment-preserving AST | It may help for reading, but custom source graph and safe multi-file mutation are still needed |
| `github.com/gofrs/flock` or similar | Cross-process file locks | Useful if a simple lockfile is insufficient |
| GoReleaser/nFPM | Release and `.deb` packaging | Use after the CLI is stable |

The CLI parser must preserve `--` SSH passthrough behavior. If Cobra
gets in the way of exact compatibility, keep the command tree in Cobra
but parse the default connect command arguments with a small custom
adapter and unit tests.

## Data Model

Core alias model:

```go
type HostAlias struct {
    Name           string
    SourcePath     string
    SourceLine     int
    RawPatterns    []string
    IsPattern      bool
    IsNegatedOnly  bool
    IsConditional  bool
    HostName       string
    User           string
    Port           string
    IdentityFiles  []string
    Effective      EffectiveConfig
    Warnings       []string
}
```

Config graph model:

```go
type ConfigGraph struct {
    RootPath string
    Files    []ConfigFile
    Edges    []IncludeEdge
    Warnings []Diagnostic
}
```

Session model:

```go
type SessionRecord struct {
    ID              string
    ParentID        string
    Depth           int
    Route           []string
    TargetAlias     string
    SSHArgv         []string
    StartedAt       time.Time
    EndedAt         *time.Time
    LocalPID        int
    SSHPID          int
    ExitCode        *int
    RunnerMode      string
    LatencySummary  *LatencySummary
    StateVersion    int
}
```

Authorized key model:

```go
type AuthorizedKey struct {
    Options []string
    Type    string
    Blob    string
    Comment string
    Source  string
    Line    int
}
```

The Bash parser effectively uses `Type + Blob` as the fingerprint for
duplicates. The Go version should keep that duplicate key, but preserve
options and comments in the rendered line.

## SSH Config Parsing and Resolution

### Goals

- Enumerate user-facing `Host` aliases from the config root and includes.
- Preserve file and line positions for edit/delete.
- Parse enough syntax to avoid corrupting config files.
- Use OpenSSH itself for effective values when exact behavior matters.
- Avoid executing `Match exec` commands while merely listing aliases.

### Parser Requirements

The parser should support:

- Empty lines and comments.
- Inline comments outside quotes.
- Case-insensitive keywords.
- `Keyword value` and `Keyword=value` forms.
- Double-quoted arguments.
- `Host` with multiple patterns.
- Negated patterns beginning with `!`.
- `Include` with absolute paths, `~`, relative paths, and globs.
- Missing include globs without hard failure.
- Duplicate include detection.
- Include cycles with diagnostics rather than infinite recursion.
- `Match` blocks as parsed scopes, even if not fully evaluated.
- Multiple `IdentityFile` values.
- CRLF input.

OpenSSH config details that matter:

- OpenSSH uses first obtained value for each parameter.
- More host-specific declarations should appear earlier than general
  defaults.
- `Host` and `Match` delimit sections.
- `Include` may appear inside `Host` or `Match`, so the graph must track
  whether an included file came from a conditional scope.

### Inventory Algorithm

1. Parse the root file. If it does not exist, return an empty graph with
   a diagnostic, not a hard error.
2. Expand global `Include` directives inline in OpenSSH-like order.
3. Parse conditional `Include` directives too, but mark aliases from them
   as conditional unless the condition can be proven unconditional.
4. Build alias candidates from `Host` patterns.
5. Hide wildcard and negated-only patterns unless `--all`.
6. Apply `SSHERPA_IGNORE_USER_GIT` and `--user`.
7. Apply substring filter.
8. Resolve display details:
   - Prefer parsed values for fast list rendering.
   - Lazily call `ssh -G alias` for selected aliases, `show`, JSON output,
     or when parsed values are ambiguous.
   - Cache `ssh -G` results for the process lifetime.

### Why Use `ssh -G`

The config parser can inventory aliases, but OpenSSH knows the final
effective settings. `ssh -G alias` should be used to display or validate
effective `hostname`, `user`, `port`, identity files, `proxyjump`, and
other details when correctness matters. This avoids reimplementing all
OpenSSH precedence, canonicalization, token expansion, and `Match`
behavior.

Do not use `ssh -G` for every list item by default unless performance is
acceptable on large configs. Make it lazy or optional.

## Config Mutation Plan

### Write Safety Rules

Every mutation to SSH config or `authorized_keys` should follow this
sequence:

1. Parse the target file.
2. Validate the intended change.
3. Render the new file to memory.
4. Re-parse the rendered file.
5. Show a dry-run diff when requested.
6. Create a timestamped backup before destructive writes.
7. Write a temp file in the same directory.
8. Preserve existing permissions where possible.
9. `fsync` the temp file.
10. Rename atomically.
11. `fsync` the parent directory where supported.
12. Report exactly which file changed.

Suggested backup names:

```text
~/.ssh/config.ssherpa-backup.20260524T194500Z
~/.ssh/authorized_keys.ssherpa-backup.20260524T194500Z
```

### Add or Update Alias

Inputs:

- Alias name.
- HostName.
- Optional User.
- Optional Port.
- Optional IdentityFile.
- Optional IdentitiesOnly.
- Target config path.

Validation:

- Alias must be non-empty.
- Alias must not contain whitespace.
- Alias should not contain `*`, `?`, or leading `!` unless an explicit
  advanced flag is added later.
- HostName must be non-empty.
- Port, if present, must be integer 1-65535.
- IdentityFile path may be non-existent, but the UI should warn.

Behavior:

- If alias does not exist, append a new stanza to the target file.
- If alias exists in a single-alias stanza, update that stanza in place
  when practical.
- If alias exists in a multi-alias stanza, do not delete unrelated alias
  tokens. Prefer one of:
  - Split the target alias into its own stanza, preserving the original
    stanza for remaining aliases.
  - Append an overriding earlier stanza only if the user confirms the
    precedence implication.
- If the alias exists in an included file and `--config` was not provided,
  edit the source file where the alias was found by default. The UI must
  show the path before writing.
- If duplicate aliases exist in multiple files, warn and ask which source
  to edit unless `--yes` plus an explicit `--config` resolves it.

### Delete Alias

The Bash version deletes an entire `Host` stanza when the alias token is
present. That can remove unrelated aliases from a multi-alias stanza.

Go behavior:

- If the `Host` line contains only the target alias, remove the stanza.
- If the `Host` line contains multiple patterns, remove only the target
  token and keep the rest of the stanza.
- If deleting a concrete alias from a stanza that also contains wildcard
  patterns, require confirmation.
- If the same alias appears in multiple files, show all sources and
  support delete-one or delete-all.
- Always backup before delete.

### Edit Alias

Edit should be modeled as a safe update operation:

- Load the source stanza.
- Present current parsed fields and effective fields if they differ.
- Let the user edit HostName, User, Port, IdentityFile, and IdentitiesOnly.
- Preserve unrelated KVs in the stanza.
- Preserve comments where possible.
- Back up before write.
- Re-render and re-parse before committing.

### Delete All Listed Aliases

Keep the feature, but make it harder to run accidentally:

- Require an exact typed confirmation such as the number of aliases or
  `delete N aliases`.
- Show all touched files.
- Produce a dry-run diff first unless `--yes` is combined with
  `--dry-run=false` or equivalent explicit confirmation.
- Back up every touched file.
- Never delete wildcard stanzas unless `--all` and an additional
  confirmation are used.

## SSH Command Runner

### Command Resolution

Default command:

```text
ssh
```

Kitty-aware command:

```text
kitten ssh
kitty +kitten ssh
```

Resolution order:

1. `--ssh-binary`, if provided.
2. `SSHERPA_SSH_BINARY`, if set.
3. Kitty detection unless disabled:
   - `SSHERPA_NO_KITTY` set.
   - `--no-kitty`.
4. Plain `ssh`.

Debug output should show the final argv without leaking secrets from
unrelated environment variables.

### Print Mode

Print mode should produce two forms:

- Human output:

```text
[print] ssh -J bastion prod
```

- JSON output:

```json
{"argv":["ssh","-J","bastion","prod"],"alias":"prod"}
```

The human output must use shell-safe quoting. Internally, always keep
argv as `[]string`.

### Direct Runner

Use direct execution for default connections:

- Attach `os.Stdin`, `os.Stdout`, and `os.Stderr`.
- Preserve terminal behavior.
- Forward process exit status.
- Forward relevant signals where needed.
- Avoid allocating a second PTY.

Implementation options:

- `exec.Command` with inherited stdio is easiest and keeps `ssherpa`
  around for exit handling.
- `syscall.Exec` is closest to replacing the process, but prevents
  post-exit state recording. Use it only if needed.

### PTY Runner

Use PTY mode for supervised sessions:

- Start SSH with `creack/pty`.
- Put local terminal in raw mode.
- Copy stdin to PTY and PTY to stdout.
- Handle `SIGWINCH` with PTY resize.
- Forward `SIGINT`, `SIGTERM`, and `SIGHUP` appropriately.
- Restore terminal state on every exit path.
- Record session state.

PTY mode should be behind `--supervise` or a config setting until enough
manual testing proves it is safe for daily interactive SSH.

## TUI Plan

Use Bubble Tea for all interactive screens:

- Alias picker.
- Add alias wizard.
- Edit/delete picker.
- Jump route builder.
- Proxy setup.
- Authkeys menu.
- Directory/file picker for key import.
- Doctor results.
- Session list after supervisor is implemented.

### Picker Behavior

The main picker should include synthetic rows at the top:

```text
Add new alias
Edit aliases or delete
Manage authorized_keys on this device
Start SOCKS proxy
Jump via intermediate hops
```

Then show aliases with display info:

```text
prod-web        alice@203.0.113.42:22 [~/.ssh/id_ed25519]
db              db.internal:22
```

The UI should support:

- Fuzzy filtering.
- Keyboard navigation.
- Clear cancel.
- Help footer.
- Small terminal widths without broken layout.
- `--no-color`.
- No alternate screen when `SSHERPA_NO_ALT_SCREEN=1`.

### Add/Edit Forms

Fields:

- HostName.
- Alias.
- User.
- Port.
- IdentityFile.
- IdentitiesOnly.

Identity picker:

- Prefer private keys with valid matching `.pub` files.
- Fall back to `~/.ssh/id_*` private key scan.
- Include `None` and `Other path`.

Validation should happen before moving to the review screen.

### Non-Interactive Support

Interactive UI should not be the only way to test or automate behavior.
Provide:

```text
ssherpa list --json
ssherpa show ALIAS --json
ssherpa add --alias a --host h --user u --yes
ssherpa add --alias a --host h --dry-run
ssherpa authkeys add --key-file file.pub
ssherpa authkeys merge --from-dir dir --dry-run
```

Interactive `authkeys` can remain the default, but subcommands make the
core logic testable.

## Jump and Proxy

### Jump

Current behavior:

- Pick destination.
- Pick first hop.
- Optionally pick more hops.
- Build `ssh -J hop1,hop2 destination`.

Go behavior:

- Preserve the picker flow.
- Add `ssherpa jump` as an explicit command.
- Prevent duplicate hops.
- Prevent destination being selected as a hop.
- Support `--print`.
- Preserve SSH passthrough args.
- Store route metadata in supervised mode.

### Proxy

Current behavior:

- Prompt local SOCKS port, default 1080.
- Pick alias.
- Run `ssh -D port -C -N alias`.

Go behavior:

- Bind to `127.0.0.1` by default:

```text
ssh -D 127.0.0.1:1080 -C -N -o ExitOnForwardFailure=yes alias
```

- Add `--bind` for users who want another bind address.
- Validate port 1-65535.
- Keep `--print`.
- Do not use remote `LocalCommand` for success messaging.
- In supervised mode, record the proxy session and target alias.

## Authorized Keys Plan

The Bash `authkeys` feature is valuable, but the Go port should fix two
important safety issues:

1. Authorized_keys options must be preserved.
   Lines like `from="10.0.0.0/8",no-agent-forwarding ssh-ed25519 ...`
   should not lose the options prefix.

2. Destructive replace/delete must create backups.

### Key Parsing

Support:

- Leading options before key type.
- Key types:
  - `ssh-ed25519`
  - `ssh-ed25519-cert-v01@openssh.com`
  - `ssh-rsa`
  - `ssh-rsa-cert-v01@openssh.com`
  - `rsa-sha2-256`
  - `rsa-sha2-512`
  - `ecdsa-sha2-*`
  - `ecdsa-sha2-*-cert-v01@openssh.com`
  - `sk-ssh-ed25519@openssh.com`
  - `sk-ssh-ed25519-cert-v01@openssh.com`
  - `sk-ecdsa-sha2-nistp256@openssh.com`
  - `sk-ecdsa-sha2-nistp256-cert-v01@openssh.com`

Validation:

- Prefer `ssh-keygen -lf -` when available.
- Fall back to structural validation only if `ssh-keygen` is missing.
- Duplicate detection should key on type plus blob.
- If the same key appears with different options, report it as a
  duplicate-with-different-options warning rather than silently dropping
  policy.

### Operations

Interactive menu:

```text
Add single key (paste)
Add keys from directory (merge)
Replace keys from directory (overwrite)
Delete keys
Back
```

Non-interactive commands:

```text
ssherpa authkeys add --key "ssh-ed25519 ..."
ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub
ssherpa authkeys merge --from-dir ./keys
ssherpa authkeys replace --from-dir ./keys
ssherpa authkeys delete --fingerprint SHA256:...
ssherpa authkeys list --json
```

Directory import behavior:

- If `DIR/authorized_keys` is a directory, read files under it.
- Otherwise read `DIR/*.pub`.
- Preserve source path per key in diagnostics.
- Report valid, invalid, duplicate, and already-present counts.

File permissions:

- Ensure `~/.ssh` is mode 700 when created.
- Ensure `authorized_keys` is mode 600 when created or modified.
- If chmod fails, warn rather than hiding the write result.

## Session Manager Plan

This is the part that justifies Go after parity is achieved.

### Route and Depth Metadata

Local state should capture:

- Session ID.
- Parent session ID.
- Depth.
- Route.
- Target alias.
- Hops.
- Start/end times.
- Local PID and SSH PID.
- Exit code.
- Runner mode.

Nested route awareness:

- If `SSHERPA_SESSION_ID` exists locally, new supervised sessions should
  record it as parent.
- Increment `SSHERPA_DEPTH`.
- Append target alias to `SSHERPA_ROUTE`.
- Attempt to propagate metadata to remote sessions with OpenSSH env
  options, but do not depend on it.

State location:

```text
Linux:  ~/.local/state/ssherpa/
macOS:  ~/Library/Application Support/ssherpa/
```

Allow `SSHERPA_STATE_DIR` and `--state-dir` overrides.

### Latency Monitoring

Avoid injecting probe commands into the user's interactive shell. That
would corrupt terminal programs and command prompts.

Acceptable probe strategies:

1. Pre-connect latency:
   Measure TCP connect or `ssh -G`/DNS timing for display only.

2. Sidecar SSH probe:
   Periodically run a separate `ssh -o BatchMode=yes target true` or
   equivalent only when explicitly enabled. This is accurate enough for
   SSH reachability but can be expensive.

3. Transport-level heuristic:
   In PTY mode, measure time between local writes and remote output only
   as a rough responsiveness signal. Do not treat it as network latency.

Warning behavior:

- Show warnings locally outside the remote PTY stream where possible.
- Log warning events to session state.
- Do not disconnect by default.

Auto-disconnect:

- Must be opt-in.
- Require both a threshold and sustained duration.
- Warn before killing the SSH process.
- Record reason in session state.

### Queued Input / Composer Mode

Goal:

- Help users compose input locally during high-latency sessions, then
  send it as one burst.

Constraints:

- Must not break full-screen remote programs like vim, tmux, htop, or
  curses apps.
- Must not intercept common terminal shortcuts unexpectedly.
- Must be easy to disable.

Proposed behavior:

- Available only in supervised PTY mode.
- Toggle with a configurable escape sequence, default `Ctrl-]`.
- While active, keystrokes edit a local composer buffer.
- Enter sends the buffer plus newline.
- Escape cancels.
- `Ctrl-G` sends without newline.
- Render composer status locally.

This should be a late feature after PTY mode is stable.

## Packaging and Installation

### Release Targets

Build:

- Linux amd64.
- Linux arm64.
- macOS amd64.
- macOS arm64.

Package:

- Tarballs/zip archives.
- `.deb` via GoReleaser/nFPM.
- Checksums.
- SBOM if GoReleaser config makes it easy.

Future:

- Homebrew tap or formula.
- Bash Zoo installer integration.

### Bash Zoo Migration

Keep Bash Zoo's script as stable fallback until the Go binary has parity.

Recommended migration stages:

1. Standalone repo pre-release.
   Users install Go binary manually or with release artifacts.

2. Bash Zoo optional binary install.
   Bash Zoo can detect platform and install `ssherpa` from release
   artifacts, while keeping the Bash script available as `ssherpa-bash`
   or documented fallback.

3. Bash Zoo default switch.
   Once Go parity and packaging are stable, Bash Zoo can install the Go
   binary by default.

4. Bash script retirement.
   Only after at least one stable release cycle with clear rollback docs.

The Go binary should expose:

```text
ssherpa version
```

Version output should include:

- Version tag.
- Commit.
- Build date.
- Go version.
- Platform.

## Testing Strategy

Testing should be built before feature work goes too far. This port is
mostly about preserving behavior and avoiding data loss.

### Unit Tests

Config parser fixtures:

- Missing root config.
- Empty config.
- Simple Host stanza.
- Multiple aliases on one Host line.
- Wildcard Host.
- Negated Host.
- `Host *` defaults.
- Duplicate alias in same file.
- Duplicate alias across included files.
- `User git` filtering.
- `Keyword value`.
- `Keyword=value`.
- Double-quoted values.
- Inline comments outside quotes.
- `#` inside quotes.
- Multiple `IdentityFile` values.
- Relative Include.
- Absolute Include.
- Tilde Include.
- Glob Include.
- Missing Include.
- Include cycle.
- Include inside Host.
- Include inside Match.
- Match blocks ignored for inventory but preserved in AST.
- CRLF line endings.

Writer golden tests:

- Add to missing config.
- Add to existing config.
- Update single-alias stanza.
- Update multi-alias stanza without deleting other aliases.
- Delete single-alias stanza.
- Delete one alias from multi-alias stanza.
- Delete duplicate aliases across files.
- Preserve comments.
- Preserve unknown directives.
- Preserve file permissions.
- Dry-run produces no write.
- Backup created before destructive write.

Authkeys tests:

- Parse plain public key.
- Parse key with options.
- Parse comments.
- Parse cert key types.
- Reject invalid base64/blob.
- Validate with fake `ssh-keygen`.
- Merge from `*.pub`.
- Merge from `authorized_keys/` directory.
- Preserve options.
- Detect duplicate exact key.
- Detect duplicate key with different options.
- Replace creates backup.
- Delete preserves comments and unrelated lines.

SSH command tests:

- Plain ssh resolution.
- Kitty `kitten ssh` resolution.
- `--no-kitty`.
- `SSHERPA_SSH_BINARY`.
- Print mode quoting.
- Proxy argv.
- Jump argv.
- SSH passthrough args after `--`.
- Exit code propagation with fake ssh.

Session tests:

- Session record creation.
- Parent/depth inheritance.
- Route append.
- State file locking.
- Prune old sessions.
- PTY runner terminal restore paths with fakes where possible.

### Integration Tests

Use temp HOME directories:

- Generate fake `~/.ssh/config`.
- Run `ssherpa list --json`.
- Run `ssherpa add --dry-run`.
- Run `ssherpa add --yes`.
- Run `ssherpa edit` through non-interactive command variants.
- Run `ssherpa authkeys merge`.

Use fake binaries on PATH:

- Fake `ssh` that records argv and returns configured exit code.
- Fake `kitten`.
- Fake `kitty`.
- Fake `ssh-keygen`.

Avoid real network by default. Real SSH tests should be manual or
explicitly opt-in.

### TUI Tests

Bubble Tea models should be testable without a real terminal:

- Initialize model with aliases.
- Send key messages.
- Assert selected action.
- Assert form validation.
- Assert cancellation behavior.
- Assert narrow-width rendering does not panic.

Golden snapshots for TUI text should be minimal and stable. Prefer
model-state assertions over brittle full-screen text snapshots.

### Manual QA Matrix

Run before first stable release:

| Platform | Shell | Terminal | Cases |
| --- | --- | --- | --- |
| macOS arm64 | zsh | Terminal.app | picker, add, edit, jump, proxy |
| macOS arm64 | zsh | Kitty | Kitty command resolution |
| Linux amd64 | bash | GNOME Terminal | picker, authkeys, packaging |
| Linux amd64 | zsh | Kitty | supervised mode, resize |
| Linux arm64 | bash | basic TTY | binary startup and list |

Manual destructive tests must use disposable temp HOME directories first,
then a reviewed real config.

## Implementation Phases

### Phase 0: Repository Foundation

Deliverables:

- `go.mod`.
- `cmd/ssherpa/main.go`.
- Basic `ssherpa version`.
- CI for test/lint/build.
- `README.md` with current project status.
- `CONTRIBUTING.md` or short developer notes.
- GoReleaser draft config without publishing.

Acceptance:

- `go test ./...` passes.
- `go run ./cmd/ssherpa version` works.
- CI builds Linux and macOS targets.

### Phase 1: Config Inventory and JSON Output

Deliverables:

- Config parser.
- Include graph.
- Alias inventory.
- Filters for `--all`, `--filter`, `--user`, and git-user hiding.
- `ssherpa list`.
- `ssherpa show ALIAS`.
- Optional lazy `ssh -G` resolver.

Acceptance:

- Parser fixture matrix passes.
- `ssherpa list --json` works against temp HOME.
- No writes happen in this phase.

### Phase 2: Picker and Direct Connect

Deliverables:

- Bubble Tea alias picker.
- Synthetic rows.
- `--print`.
- Direct SSH runner.
- Kitty command resolver.
- Fake SSH integration tests.

Acceptance:

- Default `ssherpa` can pick an alias and invoke fake ssh.
- `--print` prints shell-safe argv.
- SSH exit code is propagated.
- No Gum dependency.

### Phase 3: Add/Edit/Delete Config

Deliverables:

- Add alias wizard and non-interactive add.
- Edit alias flow.
- Delete alias flow.
- Delete-all with strict confirmation.
- Atomic writer.
- Backups.
- Dry-run diff.

Acceptance:

- Writer golden tests pass.
- Multi-alias Host deletion is safe.
- Backups are created for destructive writes.
- Temp HOME integration tests pass.

### Phase 4: Jump and Proxy

Deliverables:

- Interactive jump route builder.
- Explicit `ssherpa jump`.
- Interactive proxy flow.
- Explicit `ssherpa proxy`.
- Route/proxy argv tests.

Acceptance:

- Jump prints and runs fake ssh with expected `-J` argv.
- Proxy uses `127.0.0.1:PORT`, `-C`, `-N`, and
  `ExitOnForwardFailure=yes`.
- Duplicate hops are impossible in UI and command logic.

### Phase 5: Authorized Keys

Deliverables:

- Interactive authkeys menu.
- Non-interactive authkeys subcommands.
- Parser that preserves options.
- Merge, replace, delete, list.
- Permission handling.
- Backup handling.

Acceptance:

- Authkeys fixture matrix passes.
- Replace/delete always create backups.
- Options are preserved exactly.
- Invalid keys are reported without corrupting output.

### Phase 6: Packaging and Bash Zoo Integration Prep

Deliverables:

- GoReleaser config.
- nFPM `.deb` config.
- Release archive smoke test.
- Install docs.
- Bash Zoo migration note.

Acceptance:

- Local snapshot release builds artifacts.
- `.deb` installs and uninstalls cleanly in a test container or VM.
- README documents Bash fallback.

### Phase 7: Supervised PTY Sessions

Deliverables:

- `--supervise`.
- PTY runner.
- Session state records.
- `ssherpa session list/show/prune`.
- Signal and resize handling.
- Terminal restore safeguards.

Acceptance:

- Supervised fake shell exits cleanly and restores terminal.
- Session state records start/end/exit code.
- Nested local sessions record parent/depth.

### Phase 8: Latency Watchdog

Deliverables:

- Opt-in sidecar latency probe.
- Warning threshold.
- Session log events.
- Optional disconnect threshold.

Acceptance:

- Latency warnings do not write into the remote shell stream.
- Auto-disconnect is impossible unless explicitly configured.
- State records warning and disconnect reason.

### Phase 9: Queued Input Composer

Deliverables:

- Supervised-mode composer.
- Toggle key.
- Buffer editing.
- Send/cancel behavior.
- Configurable keybinding.

Acceptance:

- Composer can be toggled without corrupting normal input.
- Full-screen remote app smoke tests pass.
- Feature can be disabled completely.

## Risk Register

| Risk | Impact | Mitigation |
| --- | --- | --- |
| OpenSSH config semantics are hard to clone | Incorrect display or unsafe edits | Use parser for inventory, `ssh -G` for effective values, and direct `ssh alias` for connection |
| Config writer deletes user data | High trust damage | AST edits, backups, dry-run diff, golden tests, multi-alias safeguards |
| PTY runner breaks terminal behavior | Bad interactive experience | Default to direct runner; PTY opt-in until stable |
| Authkeys parser drops restrictions | Security regression | Preserve options and test option-heavy fixtures |
| Auto-disconnect kills important work | Severe user harm | Never default; require explicit threshold and sustained condition |
| Remote env propagation fails | Nested metadata incomplete | Treat as best effort; keep local state correct |
| Bubble Tea UI blocks automation | Harder tests and scripts | Provide JSON/non-interactive commands for core logic |
| Dependency API churn | Build instability | Pin versions, isolate dependencies, update intentionally |
| Packaging differs from Bash Zoo | Install confusion | Keep Bash fallback and document migration stages |
| No local Go toolchain | Cannot build yet in this workspace | Install Go or use CI as Phase 0 prerequisite |

## First Pull Requests

PR 1:

- Add Go module.
- Add `version`.
- Add `README.md`.
- Add CI.

PR 2:

- Implement parser lexer.
- Add parser fixture tests.
- Add `list --json`.

PR 3:

- Add host filtering.
- Add `show`.
- Add fake `ssh -G` resolver tests.

PR 4:

- Add Bubble Tea picker.
- Add direct runner and print mode.
- Add fake ssh integration tests.

PR 5:

- Add config writer and `add --dry-run`.
- Add writer golden tests and backups.

Continue only after each PR has tests and a short manual verification
note. This project touches SSH configuration, so small reviewed changes
are better than a single large rewrite.

## References Checked

- OpenSSH `ssh_config(5)` manual: https://man.openbsd.org/ssh_config.5
- Bubble Tea docs and README: https://github.com/charmbracelet/bubbletea
- Bubbles README: https://github.com/charmbracelet/bubbles
- Lip Gloss package docs: https://pkg.go.dev/charm.land/lipgloss/v2
- `creack/pty` README: https://github.com/creack/pty
- `kevinburke/ssh_config` package docs: https://pkg.go.dev/github.com/kevinburke/ssh_config
- Cobra package docs: https://pkg.go.dev/github.com/spf13/cobra
- GoReleaser nFPM docs: https://goreleaser.com/customization/nfpm/
