# ssherpa Non-Interactive CLI Reference

This document covers the complete non-interactive surface area of `ssherpa`:
commands, positional arguments, flags, defaults, environment variables, output
modes, and examples intended for scripts or headless use.

For interactive use, run `ssherpa`, `ssherpa edit`, `ssherpa authkeys`, or
`ssherpa theme` without the required non-interactive arguments.

## Conventions

- `ALIAS` means an OpenSSH `Host` alias from the parsed config.
- `DURATION` uses Go duration syntax: `500ms`, `5s`, `30m`, `168h`.
- `--` stops ssherpa flag parsing where supported and passes the remaining
  arguments to `ssh`.
- `--json` emits pretty-printed JSON where supported.
- `--dry-run` prints the planned write and does not modify files.
- `--yes` or `-y` skips confirmation where supported.
- Unless stated otherwise, paths and aliases are case-sensitive.

## Defaults and State

| Item | Default |
| --- | --- |
| SSH config | `~/.ssh/config` |
| SSH binary | `ssh`, or `kitten ssh` when running inside Kitty unless disabled |
| SFTP binary | `sftp` |
| Linux state dir | `~/.local/state/ssherpa` |
| macOS state dir | `~/Library/Application Support/ssherpa` |
| Theme file | `~/.config/ssherpa/theme.conf` |
| Authorized keys | `~/.ssh/authorized_keys` |
| Proxy bind | `127.0.0.1` |
| Proxy port | `1080` |
| Forward bind | `127.0.0.1` |
| Check SSH timeout | `5s` |
| Check ICMP timeout | `2s` |
| Session prune age | `168h` |

## Environment Variables

| Variable | Effect |
| --- | --- |
| `SSHERPA_SSH_BINARY` | Default SSH binary when `--ssh-binary` is not set. |
| `SSHERPA_STATE_DIR` | Default state directory when `--state-dir` is not set. |
| `SSHERPA_INCOMING_DIR` | Incoming SSH marker runtime directory. |
| `SSHERPA_AUTHORIZED_KEYS_PATH` | Default authorized_keys path when `--path` is not set. |
| `SSHERPA_THEME_FILE` | Default theme config path when `--theme-file` is not set. |
| `SSHERPA_NO_COLOR` | Disable color styling. |
| `NO_COLOR` | Disable color styling. |
| `SSHERPA_NO_KITTY` | Disable Kitty SSH detection. |
| `SSHERPA_NO_ALT_SCREEN` | Disable alternate-screen UI behavior for prompts/pickers. |
| `SSHERPA_TRANSFER_TRANSPORT` | Internal/testing transfer transport override. |

Supervised SSH commands also send `SSHERPA_*` metadata through OpenSSH
`SendEnv`. Remote display of inherited lineage requires server-side
`AcceptEnv SSHERPA_*`.

## Exit Codes

Exit codes are intentionally simple in the current implementation:

| Code | Meaning |
| --- | --- |
| `0` | Success, no-op, cancelled interactive selection, or clean skip. |
| `1` | Usage error, validation error, failed write, failed process start, or generic failure. |
| `2` | Not found or failed check result for selected commands. |
| `3` | Config/inventory load failure for `check`. |
| `120` | Escape rope pulled from a supervised session. |

When ssherpa directly runs `ssh` or `sftp`, the child exit code may be returned.

## Global Help and Version

```sh
ssherpa help
ssherpa --help
ssherpa -h
ssherpa version
ssherpa --version
ssherpa -v
```

`version` prints:

```text
ssherpa VERSION
commit COMMIT
built  DATE
```

## Inventory Commands

Inventory commands parse OpenSSH config, including `Include` files.

### list

```sh
ssherpa list [--json] [--all] [--filter SUBSTR] [--user USER] [--config PATH]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--json` | Emit JSON aliases and diagnostics. |
| `--all` | Include wildcard and negated `Host` patterns. |
| `--filter SUBSTR` | Keep aliases whose name contains `SUBSTR`. |
| `--user USER` | Keep aliases whose parsed `User` is `USER`. |
| `--config PATH` | Parse this config root instead of `~/.ssh/config`. |

Examples:

```sh
ssherpa list
ssherpa list --json --filter prod --user alice
ssherpa list --all --config ~/.ssh/config.work
```

### show

```sh
ssherpa show ALIAS [--json] [--all] [--filter SUBSTR] [--user USER] [--config PATH]
```

`show` prints one parsed alias and returns `2` when the alias is not found.

Examples:

```sh
ssherpa show prod
ssherpa show prod --json
```

## Connect

```sh
ssherpa [connect-flags] [-- ssh-args...]
ssherpa --select ALIAS [connect-flags] [-- ssh-args...]
```

Without `--select`, ssherpa opens the picker. For non-interactive use, pass
`--select`.

Connect flags:

| Flag | Meaning |
| --- | --- |
| `--print` | Print the SSH command instead of running it. |
| `--exec` | Run the SSH command. This is the default. |
| `--select ALIAS` | Select an alias without opening the picker. |
| `--ssh-binary PATH` | Use this SSH binary. |
| `--supervise` | Run under the PTY supervisor. This is the default. |
| `--direct`, `--no-supervise` | Run ssh directly, without overlay, state, composer, or watchdog. |
| `--state-dir PATH` | Override session state directory. |
| `--latency-warn DURATION` | Warn when sidecar SSH probe latency exceeds the threshold. |
| `--latency-disconnect DURATION` | Disconnect after sustained unhealthy probes. Requires `--latency-warn`. |
| `--composer-key KEY` | Change queued-input composer control key. Example: `ctrl-r`. |
| `--no-composer` | Disable the queued-input composer. |
| `--overlay-key KEY` | Change the session-map overlay (and escape rope) control key; default `ctrl-^`. |
| `--no-record` | Disable overlay-controlled transcript recording for this supervised session. |
| `--record-max-bytes BYTES` | Cap the transcript file size once recording is started. Accepts suffixes like `50MB` or `100MiB`; default `50MB`. |
| `--no-kitty` | Disable Kitty SSH command detection. |
| `--no-color` | Disable color styling. |
| `--theme VALUE` | Deprecated compatibility flag; theme files are the active source. |
| `--theme-file PATH` | Load UI theme config from this path. |
| `--json` | Supported only with `--print` for connect mode. |
| `--all`, `--filter SUBSTR`, `--user USER`, `--config PATH` | Inventory filtering/config flags. |

Examples:

```sh
ssherpa --select prod
ssherpa --print --select prod
ssherpa --select prod -- -L 8080:localhost:8080
ssherpa --select prod --direct
ssherpa --select prod --latency-warn 2s --latency-disconnect 30s
ssherpa --print --json --select prod
```

Supervised-session keys:

| Key | Action |
| --- | --- |
| `Ctrl-^` | Open or close the local session-map overlay. |
| `Ctrl-^`, `T` | Start transcript recording. Press `T` again in the overlay to pause or resume. |
| `Ctrl-^`, `X`, `X` | Pull the escape rope after confirmation. |
| `Ctrl-^` three times quickly | Panic escape rope, no confirmation. |
| `Ctrl-G` | Open queued-input composer by default. |

`Ctrl-^` is `Ctrl-Shift-6` on US layouts; most terminals also send the same
byte for plain `Ctrl-6`. The key was chosen because almost nothing else uses
it (mosh picked the same byte for its escape character for the same reason).
If it collides with something you need — vim's alternate-file toggle, or mosh
running inside a supervised session — rebind it with `--overlay-key KEY`, for
example `--overlay-key 'ctrl-]'`. Versions before 1.8 used `Ctrl-]`, which
conflicted with telnet's escape character and vim's jump-to-tag.

Supervised sessions do not record immediately. Open the local session-map
overlay with `Ctrl-^` and press `T` to start, pause, or resume visible terminal
output recording. Recording writes to `STATE_DIR/sessions/SESSION_ID.cast` in an
asciinema-v2-compatible JSONL format with `0600` permissions. Local input is not
recorded. `--no-record` removes this opt-in recording path for the session.

## Config Mutation

Mutations operate on real OpenSSH config and write atomically with backups.
They refuse to silently overwrite changed files between plan and write.

Shared mutation flags:

| Flag | Meaning |
| --- | --- |
| `--config PATH` | Mutate this config root. |
| `--dry-run` | Print the diff without writing. |
| `--yes`, `-y` | Skip confirmation. |

### add

```sh
ssherpa add --alias NAME --host HOST [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--alias NAME` | Host alias to add or update. |
| `--host HOST`, `--hostname HOST` | `HostName` value. |
| `--user USER` | `User` value. |
| `--port PORT` | `Port` value. |
| `--identity PATH` | `IdentityFile` value. |
| `--identities-only` | Set `IdentitiesOnly yes`. |
| `--config PATH`, `--dry-run`, `--yes`, `-y` | Shared mutation flags. |

Examples:

```sh
ssherpa add --alias prod --host prod.example.com --user alice --dry-run
ssherpa add --alias prod --host prod.example.com --identity ~/.ssh/prod --identities-only --yes
```

### edit set

```sh
ssherpa edit set ALIAS [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--host HOST`, `--hostname HOST` | Replace `HostName`. |
| `--user USER` | Replace `User`. |
| `--clear-user` | Remove `User`. |
| `--port PORT` | Replace `Port`. |
| `--clear-port` | Remove `Port`. |
| `--identity PATH` | Replace `IdentityFile`. |
| `--clear-identity` | Remove `IdentityFile`. |
| `--identities-only` | Set `IdentitiesOnly yes`. |
| `--no-identities-only` | Set `IdentitiesOnly no`. |
| `--config PATH`, `--dry-run`, `--yes`, `-y` | Shared mutation flags. |

Examples:

```sh
ssherpa edit set prod --port 2222 --yes
ssherpa edit set prod --clear-user --clear-identity --dry-run
```

### edit delete

```sh
ssherpa edit delete ALIAS [--all-sources] [--delete-patterns] [--state-dir PATH] [--config PATH] [--dry-run] [--yes]
ssherpa edit remove ALIAS [...]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--all-sources` | Delete all matching source stanzas instead of the first target. |
| `--delete-patterns` | Allow deleting wildcard or negated `Host` patterns. |
| `--state-dir PATH` | Also clean saved forward/proxy references in this state dir. |
| `--config PATH`, `--dry-run`, `--yes`, `-y` | Shared mutation flags. |

### edit delete-all

```sh
ssherpa edit delete-all [--all] [--filter SUBSTR] [--user USER] [--config PATH] [--state-dir PATH] [--dry-run] [--yes] [--confirm TEXT] [--delete-patterns]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--all` | Include wildcard and negated `Host` patterns. |
| `--filter SUBSTR` | Delete matching aliases only. |
| `--user USER` | Delete aliases for this parsed user only. |
| `--confirm "delete N aliases"` | Required confirmation phrase for bulk deletes unless dry-running. |
| `--delete-patterns` | Allow deleting wildcard or negated `Host` patterns. |
| `--state-dir PATH` | Also clean saved forward/proxy references in this state dir. |
| `--config PATH`, `--dry-run`, `--yes`, `-y` | Shared mutation flags. |

Examples:

```sh
ssherpa edit delete prod --dry-run
ssherpa edit delete-all --filter scratch --dry-run
ssherpa edit delete-all --filter scratch --confirm "delete 3 aliases"
```

## Jump Routes

```sh
ssherpa jump --dest DEST --hop HOP [--hop HOP] [connect-flags] [-- ssh-args...]
ssherpa jump DEST HOP [HOP...] [connect-flags] [-- ssh-args...]
```

Non-interactive jump requires a destination and at least one hop.

Jump-specific flags:

| Flag | Meaning |
| --- | --- |
| `--dest DEST`, `--destination DEST` | Final alias. |
| `--hop HOP` | ProxyJump hop. May be repeated. Comma-separated values are accepted. |

Also supports connect flags: `--print`, `--exec`, `--ssh-binary`, `--direct`,
`--supervise`, `--state-dir`, latency flags, composer flags, color/theme flags,
`--no-kitty`, inventory flags, and pass-through `-- ssh-args...`.

Examples:

```sh
ssherpa jump --dest prod --hop bastion --print
ssherpa jump --dest prod --hop bastion,edge -- -A
ssherpa jump prod bastion edge --direct
```

## SOCKS Proxy

### Launch

```sh
ssherpa proxy --select ALIAS_OR_SAVED [--bind ADDR] [--port PORT] [flags] [-- ssh-args...]
ssherpa proxy ALIAS_OR_SAVED [--bind ADDR] [--port PORT] [flags] [-- ssh-args...]
```

If `--select` matches a saved proxy name and `--port` is not set, the saved
proxy supplies `--select`, `--bind`, and `--port`.

Flags:

| Flag | Meaning |
| --- | --- |
| `--select ALIAS_OR_SAVED` | SSH alias or saved proxy name. |
| `--bind ADDR` | Local bind address. Default `127.0.0.1`. |
| `--port PORT` | Local SOCKS port. Default `1080`. |
| `--background` | Detach a tracked supervised proxy session. |
| `--print` | Print the SSH command. Mutually exclusive with `--background`. |
| `--direct`, `--supervise`, `--state-dir`, `--ssh-binary` | Connect/session flags. |
| `--all`, `--filter`, `--user`, `--config` | Inventory flags. |
| `--no-kitty`, `--no-color`, `--theme`, `--theme-file` | UI/style flags. |

Examples:

```sh
ssherpa proxy --select prod --port 1080 --print
ssherpa proxy --select corp --background
```

### Management

```sh
ssherpa proxy list [--json] [--state-dir PATH]
ssherpa proxy status SESSION_ID_OR_NAME [--json] [--state-dir PATH]
ssherpa proxy stop SESSION_ID_OR_NAME [--yes] [--state-dir PATH]
```

`SESSION_ID_OR_NAME` may be a session ID or a saved proxy name attached to an
active/proxied record.

### Saved Proxies

```sh
ssherpa proxy saved list [--json] [--state-dir PATH]
ssherpa proxy saved show NAME [--json] [--state-dir PATH]
ssherpa proxy saved save NAME --select ALIAS --port PORT [--bind ADDR] [--description TEXT] [--config PATH] [--state-dir PATH] [--yes]
ssherpa proxy saved edit NAME [--select ALIAS] [--port PORT] [--bind ADDR] [--description TEXT | --clear-description] [--config PATH] [--state-dir PATH]
ssherpa proxy saved delete NAME [--state-dir PATH] [--yes]
ssherpa proxy saved rename OLD NEW [--state-dir PATH] [--yes]
```

Notes:

- `save` requires `NAME`, `--select`, and `--port`.
- `delete` requires `--yes`.
- `rename` overwrites an existing target only with `--yes`.

## Local Port Forwards

### Launch

```sh
ssherpa forward --select ALIAS_OR_SAVED --local [BIND:]PORT --remote HOST:PORT [flags] [-- ssh-args...]
ssherpa forward ALIAS_OR_SAVED --local [BIND:]PORT --remote HOST:PORT [flags] [-- ssh-args...]
```

If `--select` matches a saved forward name and neither `--local` nor `--remote`
is set, the saved forward supplies the target and tunnel settings.

Flags:

| Flag | Meaning |
| --- | --- |
| `--select ALIAS_OR_SAVED` | SSH alias or saved forward name. |
| `--local [BIND:]PORT` | Local listener. Bind defaults to `127.0.0.1`. Required unless loaded from a saved forward. |
| `--remote HOST:PORT` | Remote destination. Required unless loaded from a saved forward. |
| `--through HOP` | Use `ssh -J HOP` to reach the selected alias. |
| `--background` | Detach a tracked supervised tunnel session. |
| `--no-reconnect` | Disable reconnect loop for supervised tunnel sessions. |
| `--reconnect-max N` | Maximum reconnect attempts. `0` means unlimited. |
| `--reconnect-backoff DURATION` | Initial reconnect backoff. |
| `--reconnect-max-backoff DURATION` | Maximum reconnect backoff. |
| `--print` | Print the SSH command. Mutually exclusive with `--background`. |
| `--direct`, `--supervise`, `--state-dir`, `--ssh-binary` | Connect/session flags. |
| `--all`, `--filter`, `--user`, `--config` | Inventory flags. |
| `--no-kitty`, `--no-color`, `--theme`, `--theme-file` | UI/style flags. |

Examples:

```sh
ssherpa forward --select pgbox --local 5432 --remote 127.0.0.1:5432 --print
ssherpa forward --select pgbox --local 127.0.0.1:5433 --remote db.internal:5432 --through bastion
ssherpa forward --select pg --background --reconnect-max 10
```

### Management

```sh
ssherpa forward list [--json] [--state-dir PATH]
ssherpa forward status SESSION_ID_OR_NAME [--json] [--state-dir PATH]
ssherpa forward stop SESSION_ID_OR_NAME [--yes] [--state-dir PATH]
```

### Saved Forwards

```sh
ssherpa forward saved list [--json] [--state-dir PATH]
ssherpa forward saved show NAME [--json] [--state-dir PATH]
ssherpa forward saved save NAME --select ALIAS --local [BIND:]PORT --remote HOST:PORT [--through HOP] [--description TEXT] [--config PATH] [--state-dir PATH] [--yes]
ssherpa forward saved edit NAME [--select ALIAS] [--local [BIND:]PORT] [--remote HOST:PORT] [--through HOP | --clear-through] [--description TEXT | --clear-description] [--config PATH] [--state-dir PATH]
ssherpa forward saved delete NAME [--state-dir PATH] [--yes]
ssherpa forward saved rename OLD NEW [--state-dir PATH] [--yes]
```

Notes:

- `save` requires `NAME`, `--select`, `--local`, and `--remote`.
- `--through` and `--clear-through` are mutually exclusive.
- `--description` and `--clear-description` are mutually exclusive.
- `rename` overwrites an existing target only with `--yes`.

## File Transfer

Transfers use OpenSSH SFTP. Directory transfer is not supported yet.
Destinations are not overwritten unless `--force` is set or an interactive
overwrite confirmation is available from the UI path.

### send

```sh
ssherpa send LOCAL_FILE --select ALIAS [--remote REMOTE_PATH] [--force] [--print] [--config PATH] [--sftp-binary PATH]
ssherpa send LOCAL_FILE ALIAS [...]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--select ALIAS` | Target SSH alias. |
| `--remote REMOTE_PATH` | Remote destination path. Defaults to basename in print mode or picker selection interactively. |
| `--force` | Allow overwrite. |
| `--print` | Print the `sftp` command and generated batch. |
| `--config PATH` | SSH config root for SFTP. |
| `--sftp-binary PATH` | Use this SFTP binary. |
| `--all`, `--filter SUBSTR`, `--user USER` | Inventory selection flags. |
| `--no-color`, `--theme`, `--theme-file` | UI/style flags. |

Examples:

```sh
ssherpa send ./build.tar.gz --select prod --remote /tmp/build.tar.gz
ssherpa send ./build.tar.gz prod --print
```

### receive

```sh
ssherpa receive REMOTE_PATH --select ALIAS [--local LOCAL_PATH] [--force] [--print] [--config PATH] [--sftp-binary PATH]
ssherpa recv REMOTE_PATH --select ALIAS [...]
ssherpa receive REMOTE_PATH ALIAS [...]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--select ALIAS` | Source SSH alias. |
| `--local LOCAL_PATH` | Local destination path or directory. Defaults to remote basename. |
| `--force` | Allow overwrite. |
| `--print` | Print the `sftp` command and generated batch. |
| `--config PATH` | SSH config root for SFTP. |
| `--sftp-binary PATH` | Use this SFTP binary. |
| `--all`, `--filter SUBSTR`, `--user USER` | Inventory selection flags. |
| `--no-color`, `--theme`, `--theme-file` | UI/style flags. |

Examples:

```sh
ssherpa receive /var/log/app.log --select prod --local ./app.log
ssherpa recv /tmp/build.tar.gz prod --force
```

## Reachability Checks

```sh
ssherpa check ALIAS... [flags]
ssherpa check --filter SUBSTR [flags]
ssherpa check --user USER [flags]
ssherpa check --saved-forward NAME [flags]
ssherpa check --saved-forwards [flags]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--json` | Emit JSON summary and results. |
| `--all` | Include wildcard and negated aliases in filtered checks. |
| `--filter SUBSTR` | Check matching aliases. |
| `--user USER` | Check aliases for a parsed user. |
| `--config PATH` | SSH config root. |
| `--state-dir PATH` | State dir for saved-forward lookup. |
| `--ssh-binary PATH` | SSH binary for probes. |
| `--timeout DURATION` | SSH probe timeout. Default `5s`. |
| `--icmp-timeout DURATION` | ICMP probe timeout. Default `2s`. |
| `--no-icmp` | Skip ICMP probe. |
| `--saved-forward NAME` | Check one saved forward. |
| `--saved-forwards` | Check all saved forwards. |

Examples:

```sh
ssherpa check prod
ssherpa check prod db --json --no-icmp
ssherpa check --filter prod --timeout 3s
ssherpa check --saved-forward pg --json
ssherpa check --saved-forwards --no-icmp
```

## Incoming SSH Visibility

```sh
ssherpa incoming list [--json] [--runtime-dir PATH]
ssherpa incoming mark [--watch-parent PID] [--quiet] [--runtime-dir PATH]
ssherpa incoming hook [--shell sh|bash|zsh|fish]
```

Subcommands:

| Command | Meaning |
| --- | --- |
| `list` | List current interactive SSH logins into this machine. |
| `mark` | Write an incoming-session marker, optionally watching a parent PID. |
| `hook` | Print a shell hook that calls `incoming mark`. |

Flags:

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--json` | `list` | Emit JSON. |
| `--runtime-dir PATH` | `list`, `mark` | Runtime marker directory. |
| `--watch-parent PID` | `mark` | Remove marker when the parent process exits. |
| `--quiet` | `mark` | Suppress marker path output and write errors. |
| `--shell sh|bash|zsh|fish` | `hook` | Shell syntax to print. |

Examples:

```sh
ssherpa incoming list
ssherpa incoming list --json
ssherpa incoming hook --shell zsh
ssherpa incoming mark --watch-parent $$ --quiet
```

SSHerpa labels are advisory. Clients can spoof accepted `SSHERPA_*`
environment values.

## Authorized Keys

Authorized-keys mutations preserve key options and cert types where possible,
validate keys with `ssh-keygen`, write atomically, and create backups.
Key comments containing control characters are rejected so rendered
`authorized_keys` entries cannot span multiple lines.
Running `ssherpa authkeys` with no subcommand opens the interactive
authorized-keys manager. Its **View current keys** action opens a searchable TUI
list of the current entries and a scrollable detail view for the selected key,
including fingerprint, source line, options, comment, and the full rendered key.

Shared authkeys mutation flags:

| Flag | Meaning |
| --- | --- |
| `--path PATH` | Authorized keys file. |
| `--dry-run` | Print planned diff without writing. |
| `--yes`, `-y` | Skip confirmation. |
| `--ssh-keygen PATH` | Use this `ssh-keygen` binary for validation. |

### list

```sh
ssherpa authkeys list [--json] [--path PATH]
```

### add

```sh
ssherpa authkeys add --key "ssh-ed25519 ..." [shared flags]
ssherpa authkeys add --key-file PATH.pub [shared flags]
```

Exactly one of `--key` or `--key-file` is required.

### merge

```sh
ssherpa authkeys merge --from-dir DIR [shared flags]
```

Reads keys from `DIR`, adding new valid keys while preserving existing keys.

### replace

```sh
ssherpa authkeys replace --from-dir DIR [shared flags]
```

Replaces the file with valid keys found in `DIR`.

### delete

```sh
ssherpa authkeys delete --fingerprint SHA256:... [--fingerprint SHA256:...] [shared flags]
ssherpa authkeys delete SHA256:... [SHA256:...] [shared flags]
ssherpa authkeys remove ...
```

Examples:

```sh
ssherpa authkeys list --json
ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub --dry-run
ssherpa authkeys merge --from-dir ./keys --yes
ssherpa authkeys replace --from-dir ./keys --dry-run
ssherpa authkeys delete --fingerprint SHA256:abc... --yes
```

## Sessions

Supervised sessions, background forwards, and background proxies write session
records in the state directory.

```sh
ssherpa session list [--json] [--state-dir PATH]
ssherpa session map [--json] [--all] [--state-dir PATH]
ssherpa session show SESSION_ID [--json] [--state-dir PATH]
ssherpa session log SESSION_ID [--raw] [--tail N] [--follow] [--state-dir PATH]
ssherpa session replay SESSION_ID [--speed N] [--no-delay] [--state-dir PATH]
ssherpa session grep SESSION_ID PATTERN [--ignore-case] [--json] [--state-dir PATH]
ssherpa session export SESSION_ID [--format text|asciicast] [--output PATH] [--state-dir PATH]
ssherpa session bundle export SESSION_ID --output PATH [--json] [--state-dir PATH]
ssherpa session bundle import PATH [--json] [--state-dir PATH]
ssherpa session identity [--json] [--state-dir PATH]
ssherpa session browse [--state-dir PATH]
ssherpa session stop-all [--json] [--state-dir PATH]
ssherpa session prune [--older-than DURATION] [--dry-run] [--json] [--state-dir PATH]
```

Subcommands:

| Command | Meaning |
| --- | --- |
| `list` | Flat list of recorded sessions. |
| `map` | Tree of active sessions by default. |
| `show` | One session record. |
| `log` | Print a readable cleaned transcript, or raw terminal output with `--raw`. |
| `replay` | Replay recorded terminal output with original timing. |
| `grep` | Search cleaned transcript output and print timestamped matches. |
| `export` | Export transcript text or the original asciicast stream. |
| `bundle export` | Export a portable `.ssherpa-session` bundle containing manifest, session metadata, and transcript. |
| `bundle import` | Import a portable bundle as a new local session record. |
| `identity` | Show or create this machine's local ssherpa recording identity. |
| `browse` | Open the TUI transcript browser and viewer. |
| `stop-all` | Signal every active tracked session. |
| `prune` | Remove ended records older than a duration. |

Flags:

| Flag | Applies to | Meaning |
| --- | --- | --- |
| `--json` | all | Emit JSON. |
| `--state-dir PATH` | all | Override state dir. |
| `--all` | `map` | Include exited sessions. |
| `--raw` | `log` | Print raw recorded terminal output instead of cleaned text. |
| `--tail N` | `log` | Show only the last N output frames. |
| `--follow` | `log` | Continue printing while the session is active. |
| `--speed N` | `replay` | Replay speed multiplier. Default `1`. |
| `--no-delay` | `replay` | Replay as fast as possible. |
| `--ignore-case`, `-i` | `grep` | Case-insensitive regular-expression search. |
| `--format text\|asciicast` | `export` | Export cleaned text or asciinema-compatible JSONL. |
| `--output PATH` | `export` | Write export output to this path. |
| `--older-than DURATION` | `prune` | Age threshold. Default `168h`. |
| `--dry-run` | `prune` | Show what would be removed. |

Examples:

```sh
ssherpa session list --json
ssherpa session map
ssherpa session map --all
ssherpa session show 20260529T090238.208041000Z-c8eb1976 --json
ssherpa session browse
ssherpa session log 20260529T090238.208041000Z-c8eb1976 --tail 100
ssherpa session grep 20260529T090238.208041000Z-c8eb1976 "permission denied" -i
ssherpa session replay 20260529T090238.208041000Z-c8eb1976 --speed 2
ssherpa session export 20260529T090238.208041000Z-c8eb1976 --format asciicast --output prod.cast
ssherpa session bundle export 20260529T090238.208041000Z-c8eb1976 --output prod.ssherpa-session
ssherpa session bundle import prod.ssherpa-session
ssherpa session identity
ssherpa session stop-all
ssherpa session prune --older-than 720h --dry-run
```

Portable bundles preserve the source machine ID and source session ID but are
imported under a new local session ID to avoid collisions. Imported transcripts
are classified as `imported_self`, `imported_other`, or `imported_unknown` by
comparing the bundle's source machine ID with this machine's
`STATE_DIR/identity.json`. The classification is advisory; it is not a
cryptographic authenticity guarantee.

The interactive Sessions TUI supports browsing local/imported transcripts,
importing bundles with a preview and confirmation, exporting a selected
transcript as a bundle, viewing machine identity, and showing imported-origin
metadata in the transcript viewer. It also includes a confirmed
delete-all-local-data action, available from the Sessions menu and from the
route map with `D`; this removes ssherpa state data but does not remove SSH
config. Recording itself is started, paused, and resumed from the in-session
`Ctrl-^` map overlay. Raw replay of imported recordings is treated as untrusted
terminal output. During interactive raw replay, press `Ctrl-^` to pause playback
and open local replay controls: `space` or `q` resumes, `r` restarts from the
beginning, and `m` returns to the ssherpa menus. `Ctrl-C` also stops playback and
returns to the menus.

## Theme

`theme` is primarily interactive, but it accepts non-positional flags:

```sh
ssherpa theme [--theme-file PATH] [--no-color]
```

Flags:

| Flag | Meaning |
| --- | --- |
| `--theme-file PATH` | Edit/write this theme config. |
| `--no-color` | Disable color styling while editing. |
| `--theme VALUE` | Deprecated no-op accepted for compatibility. |

The resulting config is written atomically with a backup.

## JSON-Capable Commands

Commands supporting `--json`:

- `ssherpa list --json`
- `ssherpa show ALIAS --json`
- `ssherpa --print --json --select ALIAS`
- `ssherpa proxy list --json`
- `ssherpa proxy status TARGET --json`
- `ssherpa proxy saved list --json`
- `ssherpa proxy saved show NAME --json`
- `ssherpa forward list --json`
- `ssherpa forward status TARGET --json`
- `ssherpa forward saved list --json`
- `ssherpa forward saved show NAME --json`
- `ssherpa check ... --json`
- `ssherpa incoming list --json`
- `ssherpa authkeys list --json`
- `ssherpa session list --json`
- `ssherpa session map --json`
- `ssherpa session show SESSION_ID --json`
- `ssherpa session grep SESSION_ID PATTERN --json`
- `ssherpa session bundle export SESSION_ID --json`
- `ssherpa session bundle import PATH --json`
- `ssherpa session identity --json`
- `ssherpa session stop-all --json`
- `ssherpa session prune --json`

Commands that reject `--json`: `jump`, `proxy` launch, `forward` launch, and
mutation commands.

## Install-Time Extras

Release archives and packages include:

- `completions/ssherpa.bash`
- `completions/ssherpa.zsh`
- `completions/ssherpa.fish`
- `man/ssherpa.1`

The Homebrew cask and Linux packages install the binary, completions, and man
page where the package manager expects them.
