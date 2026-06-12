# ssherpa — Stability & Mass-Adoption Readiness Audit

**Date:** 2026-06-11 · **Tree audited:** `main` @ `a112f3b` (v1.7.1; origin has released through v1.9.1 — see §9 caveat) · **Scope:** full codebase (~44k LOC Go), CI/release pipeline, distribution, docs, governance

**Purpose.** The maintainer intends to feature-freeze ssherpa and enter a long, low-touch maintenance phase while adoption grows. This report answers one question: **what must be true before the lock so the frozen thing is worth freezing** — and what can safely wait.

---

## 1. Executive summary

**Verdict: ssherpa is close to freezable, but not freezable today.** The architecture is sound, the dependency tree is exceptionally freeze-friendly (3 direct deps), the test suite is green and race-clean, the docs are unusually good, and the release pipeline is automated end-to-end. What stands between here and a defensible "stable" label is a specific, bounded set of work: roughly **two weeks of focused fixes** concentrated in four places — the `~/.ssh/config` mutation engine, the terminal trust boundary, transcript durability, and the release/contract plumbing that makes a freeze mean something.

The audit ran 422 agents over every subsystem and produced **155 adversarially confirmed findings** (1 critical, 26 high, 112 medium, 16 low) plus 84 unverified observations. Notably, **zero confirmed findings were refuted** by the adversarial panels — but 46 had their severity recalibrated, mostly downward (§13). The headline discoveries:

1. **The config mutation engine can corrupt the user's real `~/.ssh/config`.** A value containing a single quote renders unquoted and makes *every subsequent ssh invocation fatal* on modern OpenSSH ("invalid quotes"). Editing a mixed-case alias appends a duplicate stanza instead of updating. Deleting an alias can take unrelated trailing comments — and `Include` directives, silently wiping every host they define. Editing an alias out of a multi-pattern stanza drops all its unmanaged options (`ForwardAgent`, `LocalForward`, …). For a tool whose pitch is "keeps OpenSSH as the source of truth," this cluster is the single most important thing to fix before lock (§12, WP5).
2. **The remote host can attack the operator through ssherpa's own UI.** A leading-dash `Host` pattern in a (shared/synced) config becomes an argv option to ssh — `-oProxyCommand=...` is arbitrary code execution. A compromised remote can inject raw escape sequences into the trusted Ctrl-] overlay via percent-encoded OSC 7, forge session-map records via the telemetry channel, and poison transcript replay/export. None of this is exotic for an SSH supervisor; all of it is fixable with one `--` separator and sink-side sanitization (§6, WP1–WP3).
3. **Recording — a headline feature — fails brittle.** One torn trailing line (crash, ENOSPC, power loss) makes an entire transcript unreadable across replay/log/grep/export; bundle export then SHA-256-certifies the corrupt artifact and ships it; `session prune` never deletes `.cast` files, so transcript storage grows forever (the audit's only **critical** finding plus three highs, §15.4).
4. **The session map silently degrades on virtually every real server.** Nested-session lineage rides `-o SendEnv=SSHERPA_*`, which stock sshd `AcceptEnv` rejects — verified against a real OpenSSH 10.2 sshd. Remote-side lineage, incoming labels, and exported transcript metadata are wrong on default-config servers, with zero detection or documentation; the local telemetry fallback masks the breakage from the operator and mis-attributes chains deeper than two hops (§15.5).
5. **Releases are not yet trustworthy artifacts.** Tag-push publishes without running tests; binaries lack `-trimpath` (not reproducible, runner paths embedded); nothing is signed or attested; archives omit the LICENSE; shipped shell completions are missing 7 of 12 `session` subcommands; the Homebrew cask ships un-notarized macOS binaries; and the known-vulnerable `x/sys` pin is one bump away from clean (§9–10, WP11).

**What's already healthy** deserves explicit credit, because it's most of the freeze case: atomic-write discipline with backups everywhere user files are touched; quote-aware SFTP batch escaping; newline-rejecting validators on config and authkeys writes; a bounded, well-designed OSC parser; a 3-dependency module graph with immutable pins; green `-race` runs; CI smoke tests that actually exercise the CLI surface; and a 99-item self-authored improvement backlog that this audit largely validated (4 items already shipped, per triage).

**The shape of the plan (§14):** eleven must-do work packages before the lock (estimated 2–3 weeks of focused work), two should-do clusters that make the maintenance phase cheap (CI gates + governance pack), an explicit *rejected* list (Windows/ConPTY, plugins, new transports — declining these is part of locking), and everything else into the post-lock backlog that `docs/IMPROVEMENTS.md` already does a good job of holding.

---

## 2. Methodology

This audit was executed as a multi-agent workflow totaling **422 agent runs across two sessions**:

1. **Map** — 10 independent readers each built a structural map of one subsystem (`cli`, `session`, `sshconfig`, `state`, `transfer`, `authkeys`, `ui`, `transcript`, `incoming`, `termstyle`).
2. **Hunt** — each map fed a dedicated bug-hunter for that subsystem; concurrently, 12 cross-cutting auditors ran: four security lenses (argument/config injection, filesystem safety, the remote-terminal trust boundary, supply chain), test-suite health (coverage, race detector, staticcheck, govulncheck — actually executed), a public-contract inventory, docs drift (~45 documented examples actually executed against a built binary), release & distribution (including forensics on a downloaded release binary), dependency health (live proxy/API checks), governance, performance at scale (measured against synthetic 100–10,000-host configs), and UX paper-cuts (binary exercised under controlled `$HOME`, PTYs, and non-TTY stdin).
3. **Verify** — every non-low finding went to an adversarial panel of 1–3 independent verifiers whose default stance was *refuted*; critical/high/security findings required a majority of distinct lenses (correctness, reproduction, impact) to survive. Only findings marked **confirmed** below survived this process.
4. **Triage & gap sweep** — confirmed findings were unified with the existing 99-item `docs/IMPROVEMENTS.md` backlog into a single must/should/post-lock plan; a completeness critic then named five areas the audit had missed, and five gap-sweep agents investigated them (several with live `sshd` experiments), yielding some of the most consequential findings in this report (§15).

Every claim below is grounded in code an agent read (cited as `file:line` against `a112f3b`) or behavior an agent reproduced. §13 describes what the adversarial panels changed.

---

## 3. Architecture snapshot

What you are freezing, in one pass. (Condensed from the subsystem maps; line references are to `a112f3b`.)

- **`cmd/ssherpa` + `internal/cli`** — a 21-line main shim into `cli.Run`, which dispatches a hidden `--__supervisor` re-exec path (the daemon IPC, `cli.go:171`) and then a string-switch over ~16 subcommands. Anything unrecognized falls through to connect mode (picker + ssh passthrough args). All flag parsing is hand-rolled: five near-identical ~150-case switches (`parseConnectFlags`, `parseJumpFlags`, `parseProxyFlags`, `parseForwardFlags`, `parseCheckFlags`) plus three argv re-serializers that must be kept in sync by hand. Execution converges on `printOrRunSSH` (`route.go:1482`).
- **`internal/session` + `sessionview`** — the PTY supervisor. Raw mode + deferred restore, per-attempt spawn under `creack/pty`, ControlMaster injection (`/tmp/ssherpa-<uid>/cm/*.sock`), a one-byte-at-a-time stdin interceptor (Ctrl-] overlay, Ctrl-G composer), an OSC tracker parsing OSC 7/133 plus an `0x1e`-framed base64-JSON telemetry protocol that lets nested ssherpa instances mirror session records upward, a latency watchdog, escape rope (SIGHUP cascade, exit 120), and reconnect with capped backoff for tunnel/proxy kinds.
- **`internal/sshconfig` + `hostlist`** — a read-only loader (Include recursion with cycle detection; `Match` block options deliberately dropped) and a separate plan-based mutation engine that round-trips raw lines, splits multi-pattern stanzas on edit, and refuses wildcard mutations without `AllowPatterns`. `hostlist.Build` flattens blocks into the alias inventory with first-value-wins glob matching.
- **`internal/state` + `fsutil`** — one JSON file per entity (sessions/forwards/proxies/identity) under a resolved state dir. Durability is `AtomicWriteFile` (temp + fsync + rename + dir-fsync, `O_EXCL` timestamped backups, unified-diff dry-runs). **No file locking anywhere**; cross-process safety is last-writer-wins. `state_version: 1` is stamped on write and checked by no reader.
- **transfer (`sshcmd` + `inband` + `cli/transfer.go`)** — two transports: `sftp -b -` batches with string-parsed `ls -la` overwrite probes, and an in-band base64-over-PTY fallback (probe → receiver one-liner → 4 KiB chunks → SHA256-verified commit refusing existing destinations).
- **`internal/authkeys`** — parse/plan/write for `authorized_keys` with structural validation, optional `ssh-keygen` validation, fingerprint-based delete, idempotent add/merge, document round-trip preserving comments.
- **`internal/ui`** — ~20 Bubble Tea v2 models (home picker, choosers, wizards, confirms, theme editor) sharing a workflow-shell frame; all width math via `termstyle` (rune-counting, not wcwidth-aware).
- **`internal/transcript`** — asciicast-v2 recording (mutex-guarded writer, 50 MiB default cap), replay/grep/export, and zip bundles with a `bundle_version` gate — the only versioned-and-enforced format in the codebase.
- **`internal/incoming`** — login-hook markers + `who` parsing for incoming-session display, plus the detached daemon (`--background` re-exec with `Setsid`) and saved forward/proxy preset catalogs.
- **`internal/termstyle`** — hand-rolled SGR theming (15 roles, raw codes), `ResolveTheme` precedence chain, and the width helpers used by both the UI and transcript cleaning. No terminal-capability detection.

**Where the structural risk concentrates:** `session.go` (~2,300 lines, five concerns), `cli/session.go` (1,992 lines), the five duplicated flag parsers, and the lockless multi-writer `state` layer. These are exactly the places the bug hunts paid off.

---

## 4. The contract you are about to freeze

A feature freeze is a promise. Today the promise is fuzzier than the code. The full inventory (every command, flag, JSON shape, exit code, env var, file format, and keybinding) is in §4.1 below — but five contract decisions need to be made *deliberately* before the lock, because right now they're accidents:

1. **JSON outputs carry no version envelope, and two of them leak internals.** `session list/show --json` emits the raw internal `state.SessionRecord` — including `ssh_argv`, `control_path`, and the events list (`cli/session.go:1136,1564`). Freezing that freezes your internal struct forever. Decide a public projection now.
2. **`state_version`/`schema_version` are stamped but never read** (`state.go:222` et al.). Old binaries silently drop unknown fields on rewrite; new binaries silently accept anything. The transcript bundle (`bundle_version != 1 → error`, `transcript.go:375`) is the only format with a real gate — make the others match it (read-check + refuse/migrate) before files written by v1.x must survive into v2 territory.
3. **The exit-code scheme has collisions**: `check` returns 3 for inventory failure where everything else returns 2; ssh child exit codes pass through and can collide with ssherpa's own 1/2/120; `session grep` returns 1 both for "no match" and for real errors. Scripts will be written against whatever you ship — fix the collisions before they're load-bearing.
4. **`-o SendEnv=SSHERPA_*` couples your entire env namespace to the wire** (`sshcmd.go:22,125`) — and the gap sweep proved the deeper problem: **stock sshd rejects it all anyway** (§15.5). Enumerate the lineage variables explicitly, and treat remote-lineage degradation as a detectable, documentable condition rather than a silent one.
5. **The hidden `--__supervisor`/`--__detached-*` argv is an unversioned IPC protocol** between ssherpa processes that can be different versions on disk mid-upgrade (`daemon.go:18-23`). It needs either a version token or a documented stability guarantee.

Also undocumented-but-load-bearing: `SSHERPA_SFTP_BINARY`, `SSHERPA_IGNORE_USER_GIT` (which silently hides `User git` hosts like github.com from `list` and the picker — a guaranteed "where did my host go" ticket), `SSHERPA_HOST_LABEL`, the `recv` alias, and the `session transcripts` alias. Document or kill each one; an undocumented surface you keep is still a surface you must maintain.

### 4.1 Full contract inventory

#### 1. Command surface (internal/cli/cli.go:179-225 dispatch)

| Command | Subcommands / aliases | Notes |
|---|---|---|
| *(none / unknown arg)* | connect: picker or `--select` | unknown first args become ssh-args |
| `add` | — | config mutation |
| `edit` | `set`, `delete`/`remove`, `delete-all`, *(none)*=interactive | |
| `jump` | — | positional `DEST HOP...` or `--dest/--hop` |
| `proxy` | launch, `list`, `status`, `stop`, `saved {list,show,save,edit,delete/remove,rename}` | |
| `forward` | same shape as proxy | subcommand words shadow alias names (route.go:287-298) |
| `send` / `receive`,`recv` | — | SFTP transfer |
| `check` | — | |
| `incoming` | `list`, `mark`, `hook` | |
| `list`, `show` | — | inventory |
| `authkeys` | *(none)*=interactive, `list`, `add`, `merge`, `replace`, `delete`/`remove` | |
| `theme` | — | interactive editor |
| `session` | `list`,`map`,`show`,`log`,`replay`,`grep`,`export`,`bundle {export,import}`,`identity`,`browse`/`transcripts`,`stop-all`,`prune` | `transcripts` alias undocumented |
| `version`/`--version`/`-v`, `help`/`--help`/`-h` | — | |
| **HIDDEN** `--__supervisor` | with `--__detached-id`, `--__detached-state-dir`, `--__detached-log-path`, then `forward|proxy` | daemon.go:18-23; dispatched before everything (cli.go:171) |

#### 2. Flags

Shared connect/inventory (parsed per-command, hand-rolled, all support `--flag value` and `--flag=value`): `--json --all --filter --user --config --print --exec --select --ssh-binary --supervise --direct|--no-supervise --state-dir --latency-warn --latency-disconnect --composer-key --no-composer --no-record --record-max-bytes --no-kitty --no-color --theme`(deprecated no-op)` --theme-file --` (ssh passthrough). Command-specific: jump `--dest|--destination --hop`(repeatable, comma-split); proxy `--bind --port --background`; forward `--local --remote --through --background --no-reconnect --reconnect-max --reconnect-backoff --reconnect-max-backoff`; mgmt list/status/stop `--json --state-dir --yes`(stop: accepted but **never read** — forward_management.go:238); saved `--select --local --remote --bind --port --through|--clear-through --description|--clear-description --yes`; send/receive `--remote --local --force --sftp-binary`; check `--timeout --icmp-timeout --no-icmp --saved-forward --saved-forwards --state-dir --ssh-binary`; incoming `--runtime-dir --quiet --watch-parent --shell`; authkeys `--path --key --key-file --from-dir --fingerprint --ssh-keygen --dry-run --yes|-y`; add/edit `--alias --host|--hostname --user --port --identity --identities-only --no-identities-only --clear-user --clear-port --clear-identity --all-sources --delete-patterns --confirm`; session `--raw --tail --follow --speed --no-delay --ignore-case|-i --format text|asciicast|cast`(`cast` undocumented)` --output --older-than --dry-run`.

#### 3. JSON output shapes — none carry a version envelope

| Command | Shape |
|---|---|
| `list --json` | `{config:{root_path,files,includes},aliases:[hostlist.Alias],diagnostics}` |
| `show --json` | same with `alias` (null if missing, still exit 2) |
| `--print --json` (connect only) | `{"argv":[...],"alias":"..."}` — **compact**, all others pretty-print (sshcmd.go:461) |
| `forward list/status` | flat entry `{id,status:active\|orphan\|exited,target,saved_alias,local,remote,through,detached,started_at,ended_at,uptime,retries,local_pid,ssh_pid,forward:{...}}` |
| `proxy list/status` | same with `bind,port,listener,proxy:{...}` |
| `forward/proxy saved list/show` | raw `StoredForward`/`StoredProxy` incl. `state_version` |
| `check --json` | `{ok,checked_at,results:[{kind,name,ssh_alias,status,ssh_rtt_ms,ssh_exit_code,ssh_error,icmp_status,icmp_rtt_ms,local_bind_status,message}]}` |
| `incoming list --json` | `{runtime_dir,count,sessions:[...]}` |
| `authkeys list --json` | `{path,keys:[{options,type,blob,comment,fingerprint,source,line}],diagnostics}` |
| `session list/show --json` | **raw internal `state.SessionRecord`** incl. `ssh_argv`, `control_path`, `events`, `state_version` (session.go:1136,1564) |
| `session map` | `{state_dir,scope,total,active,recorded,roots}` |
| `session grep --json` | `[{line,offset,text}]`; `null` when no match (nil slice, transcript.go:636) |
| `session bundle export/import`, `identity`, `stop-all`, `prune` | result structs as listed in code |

#### 4. Exit codes (documented in docs/non-interactive.md:58-70)

0 success/cancel/no-op; 1 usage/validation/generic, also `session grep` no-match AND grep errors, `stop-all` with errors; 2 not-found (alias/session/saved) AND `check` failed results; **3** check inventory-load failure only (check.go:110) vs 2 everywhere else; **120** escape rope (session.go:44-48); ssh/sftp child exit code passthrough in direct+supervised modes (can collide with 1/2/120; ssh uses 255).

#### 5. Environment variables

Documented: `SSHERPA_SSH_BINARY, SSHERPA_STATE_DIR, SSHERPA_INCOMING_DIR, SSHERPA_AUTHORIZED_KEYS_PATH, SSHERPA_THEME_FILE, SSHERPA_NO_COLOR, NO_COLOR, SSHERPA_NO_KITTY, SSHERPA_NO_ALT_SCREEN, SSHERPA_TRANSFER_TRANSPORT`. **Undocumented but read**: `SSHERPA_SFTP_BINARY` (binary.go:38), `SSHERPA_IGNORE_USER_GIT` (cli.go:1420), `SSHERPA_HOST_LABEL` (state.go:616), plus `XDG_STATE_HOME`, `XDG_RUNTIME_DIR`, `TMPDIR`, `USER`, `KITTY_WINDOW_ID/KITTY_PID`. Lineage wire protocol: `SSHERPA_SESSION_ID, SSHERPA_PARENT_SESSION_ID, SSHERPA_DEPTH, SSHERPA_ROUTE, SSHERPA_ORIGIN_HOST` — forwarded via `-o SendEnv=SSHERPA_*` **wildcard** (sshcmd.go:22,125), so every present-and-future `SSHERPA_*` config var is transmitted to remotes.

#### 6. On-disk formats & version behavior

| File | Format | Versioned? | New-reads-old / old-reads-new |
|---|---|---|---|
| `STATE_DIR/sessions/<id>.json` | SessionRecord JSON | writes `state_version:1` | **no reader checks it**; old binary silently drops unknown fields |
| `sessions/<id>.cast` | asciicast-v2 JSONL, header `{"version":2}`, frames `[offset,"o\|i\|m",data]`, 0600 | yes (asciicast) | reader ignores header version |
| `sessions/<id>.log` | detached daemon stderr/stdout | n/a | |
| `forwards/<name>.json`, `proxies/<name>.json` | StoredForward/StoredProxy | `state_version:1`, never checked | same silent behavior |
| `identity.json` | `schema_version:1` | read only checks `machine_id` non-empty (state.go:518) | |
| `*.ssherpa-session` bundle | zip{manifest.json, session.json, transcript.cast} | `bundle_version:1` **enforced** `!=1 → error` (transcript.go:375) | only format with a hard gate |
| `~/.config/ssherpa/theme.conf` | `key=value` roles | unversioned; **unknown key = hard parse error** (termstyle/theme.go:190) | forward-incompatible |
| incoming markers | JSON, `state_version:1` | unchecked | |

STATE_DIR resolution: override → `SSHERPA_STATE_DIR` → darwin `~/Library/Application Support/ssherpa` / `$XDG_STATE_HOME/ssherpa` / `~/.local/state/ssherpa` (state.go:196-216).

#### 7. TUI key namespace (supervised PTY)

`Ctrl-]` (0x1d, session.go:33) opens overlay; inside: `q/Q/Enter/Esc/Ctrl-C` close, `X`→`X` escape rope (confirm), `Ctrl-]`×3 within 400ms panic rope, `r/R` refresh, `t/T` recording toggle, `s/S` send, `v/V` receive. Composer default `Ctrl-G`, rebindable via `--composer-key`; reserved: Ctrl-Space/C/D/Enter/LF/Esc/DEL/Ctrl-] (cli.go:701). Raw replay: `Ctrl-]` pause; `space/q/p/c/Enter/Esc` resume, `r` restart, `m/b` back, `Ctrl-C` stop (session.go:828-834).

#### 8. stdio conventions

Data/JSON/`[print]`/`[batch]` → stdout; pickers+TUI render on **stderr**; `[supervise]`/`[exec]` announcements → stderr. Inconsistent: `[skipped]` cancellations go to stdout in authkeys/forward_saved/check-picker but stderr in connect/jump/proxy/forward; `forward stop`/`stop-all` human status lines prefixed `ssherpa:` go to stdout. `version` prints `commit:`/`built:` with colons (cli.go:1510) while docs show colon-less. Docs (docs/non-interactive.md) are otherwise accurate and unusually complete; `ssherpa help` text still says "Phase 10:" and omits session log/replay/grep/export/bundle/identity/browse.

---

## 5. Test-suite health

All commands run from /Users/xbenc/0xbenc/ssherpa with -count=1.

**1. `go test ./... -count=1`: GREEN** — all 14 testable packages pass (cli slowest at 14.8s; total ~36s).

**2. Per-package coverage** (`go test ./... -cover -count=1`):

| package | coverage |
|---|---|
| cmd/ssherpa | 0.0% (no test files; 21-line main, covered by CI smoke tests) |
| internal/authkeys | 76.6% |
| internal/cli | **41.8%** |
| internal/fsutil | 71.4% |
| internal/hostlist | 90.3% |
| internal/inband | 83.6% |
| internal/incoming | 74.2% |
| internal/session | 72.5% |
| internal/sessionview | **56.3%** |
| internal/sshcmd | 75.5% |
| internal/sshconfig | 83.2% |
| internal/state | 65.0% |
| internal/termstyle | 68.0% |
| internal/transcript | 66.1% |
| internal/ui | **55.6%** |

**3. `go test -race ./... -count=1`: GREEN**, zero data races (session 4.06s, cli 15.9s, state 1.95s). But CI (.github/workflows/ci.yml) runs only plain `go test ./...` — the race detector never runs in CI. **4. `go vet ./...`: clean.** **5. staticcheck v0.7.0** (via `go run ...@latest`, network worked): 23 findings — 14 dead functions/vars (U1000, incl. parseSFTPLongDirectoryName, oscTracker.feed, 5 picker helpers), SA4008+SA4004 logic smell in host_chooser.go:584-591 (group-jump loop that unconditionally returns), S1029/SA6003 []rune loop in picker.go:1165, 2x ST1005. **govulncheck: 0 vulnerabilities** in called code (1 in required-but-uncalled modules).

**Weakest packages, read in depth.** cli (41.8%) has 142 zero-coverage functions — the entire interactive layer: transcript replay raw-terminal control (34/64 funcs in session.go at 0%, incl. replayRawWithControls, prepareReplayTerminal), bundle export/import TUIs, interactive edit/authkeys menus, SFTP browse drivers. The pure logic underneath is well-tested (parseRemoteListing 100%, sshconfig.mutate 83.2%); the gap is the glue, which also never runs in CI (smoke tests are all --print/--dry-run/--yes). ui (55.6%): proxy_builder.go has **no test file at all** (0/21 functions) while its sibling forward_builder has 12 tests; add_form's validation/save steps (updateHost/updatePort/updateReview) are 0% — its tests only cover identity selection, paste, and ANSI styling. state (65%): machine-identity read/write/ensure error paths 0% in-package (only the transcript bundle roundtrip touches them). transcript (66.1%): ExportAsciicast, WriteInput, Snapshot at 0%; ImportBundle — an untrusted-zip parser — has exactly one happy-path test; none of its hash-mismatch/version/missing-entry defenses are tested.

**Highest-risk single gap:** the in-band transfer PTY driver. Commit 4280600 deleted the flaky e2e (TestRunSupervisedOverlayInbandSendWritesRemoteFile) and nothing replaced it: newInbandSendFunc is at **1.3%**, waitForInbandOutput **0%** — ~120 lines that type shell commands into the user's live remote shell with Ctrl-C/reset rollback on every failure path.

**Timing sensitivity (remaining flake risk):** session PTY tests use 2-3s select budgets with event-driven buffers (good design, thin margins); the latency-watchdog tests race a real `sleep 0.05` child against first-probe scheduling (session_test.go:1303) — tightest window in the suite; forward stop tests layer 3s test windows on a hardcoded 2s forwardStopWait poll (1s headroom, and they burn a real 2s each — the suite's two slowest tests); cli_test.go:1748 (50ms sleep/1s budget) and forward_catalog_test.go:45 (2ms sleep) are minor. `go test ./internal/session/ -count=5` passed (5.8s). No t.Parallel anywhere (serial, safe with 18 t.Setenv uses).

**Platform gaps:** CI = ubuntu+macos, release = linux+darwin amd64/arm64 (README consistent; no Windows claim — syscall.Kill/Setsid code confirms; the GOOS!=windows check in sshcmd/binary.go:133 is vestigial). Never runs anywhere in CI: parsePingRTT (0%, must parse both BSD and iputils ping formats; darwin `-W` is ms vs linux seconds — check.go:440), fsutil.isUnsupportedSync (0%, network-FS fsync fallback), incoming.RuntimeDir /run/user branch, and any real sshd (every ssh/sftp in tests is a fake script).

**Pre-freeze verdict:** the suite is green, race-clean, and the core mutation/parsing engines are genuinely well-tested. Two items are lock-gating: add `-race` to CI (measured cost ~45s) and restore deterministic coverage of the in-band transfer driver. The rest — proxy_builder tests, ImportBundle error-path tests, widened timing budgets, parsePingRTT/ExportAsciicast unit tests, dead-code deletion + staticcheck in CI — are cheap, high-value hardening that should land before the freeze while the code is still allowed to move.

---

## 6. Security posture

ssherpa sits on three trust boundaries: the local filesystem it mutates (`~/.ssh`), the argv it hands to OpenSSH, and — easy to forget — **the remote host's byte stream**, which is attacker-controlled the moment you connect to a compromised box. Four dedicated auditors covered these; every finding below survived a 2–3-lens adversarial panel.

### 6.1 Injection & argument construction

**Attacker model.** ssherpa runs as the local user and execs OpenSSH directly via `exec.Command` (`internal/sshcmd/sshcmd.go:430`, `RunDirect`) — there is no shell, so classic `;`/backtick/`$()` shell-metacharacter injection is structurally out of scope for argv. The realistic adversary instead controls *data that becomes argv elements or in-band command streams*: the contents of `~/.ssh/config` and Included files (a co-tenant, a synced dotfiles repo, a malicious snippet pasted by the user), authorized_keys material imported from disk, and path/host strings typed into builders. The trust boundary that matters is "config text → argv," because ssherpa reads the user's *real* config and treats every `Host` pattern as a connectable alias.

**Leading-dash alias argument injection (confirmed HIGH).** Alias names flow verbatim from `Host` patterns into `Alias.Name` with only `TrimSpace` (`internal/hostlist/hostlist.go:48-54`); there is no rejection of names beginning with `-`. Those names are then appended *positionally* into argv by `BuildDirect` (`sshcmd.go:111-116`), `BuildProxy` (`:166-177`), `BuildForward` (`:186-199`), `BuildSFTP` (`:219-235`) and `BuildProbe` (`:201-217`) — none of which insert a `--` end-of-options guard before the alias. Consequently a config containing `Host -oProxyCommand=...` (or `-F`, `-E`, etc.) yields an "alias" that OpenSSH parses as an option, not a hostname, turning config-read into argument-injection / arbitrary-command execution via `ProxyCommand`. Notably the `--` guard *is* used correctly when ssherpa re-invokes itself with user `SSHArgs` (`route.go:1871`), which makes its absence on the alias position a consistency gap rather than an unknown technique. Operationally: any path that surfaces a hostile/shared config (including imported or wildcard-expanded inventories) can be coerced into running attacker-chosen commands the moment the user "connects." Remediation is a single positional `--` (or an alias-name validator rejecting a leading `-`) applied uniformly across all five builders.

**What held up well.**
- **Config mutation newline injection — solid.** `ValidateAliasSpec` rejects `\r`/`\n` in every written field — HostName, User, Port, IdentityFile, ProxyJump (`mutate.go:224-234`) — and `validateAliasName` rejects whitespace and NUL in alias names (`:244-258`). Rendering quotes/escapes values (`renderValue`, `:594-604`) and the mutation is re-parsed for validity before write (`validateRenderedDocument`, `:631-642`). A crafted field cannot inject a new directive line.
- **authkeys validation — thorough.** `ValidateStructural` (`authkeys.go:202-219`) constrains key type to an allowlist (`IsSupportedKeyType`, `:330-348`), forbids whitespace in the blob, requires valid base64 (`decodeBlob`), and `validateComment` (`:221-231`) rejects control characters (including newlines) in comments. An optional `ssh-keygen -lf -` second-pass (`Validator.Validate`, `:164-200`) feeds the key on stdin, not argv. Render (`:38-52`) reuses validated fields, so an authorized_keys entry cannot smuggle a second line or option.
- **SFTP batch quoting — protected.** SFTP is the one place a *textual command stream* is assembled (`BuildSFTPBatch` emits `put`/`get` lines, `sshcmd.go:268-275`, fed via `sftp -b -` on stdin, `:223`). Both paths pass through `quoteSFTPPath` (`:521-529`), which double-quotes and explicitly escapes `\`, `"`, **`\n` and `\r`**, defeating newline-injection of extra SFTP commands. The argv side stays clean because exec bypasses the shell.
- **Forward/proxy spec validation — sound.** `ValidateForward`/`ValidateProxy` (`:320-361`) range-check ports (1–65535) and require non-empty bind/host; `ParseForwardLocal`/`ParseForwardRemote` (`:371-418`) parse via `net.SplitHostPort` rather than string-splitting, and `ValidateJumpRoute` (`:295-318`) blocks empty/duplicate/self-referential hops. Binary resolution is independently gated (`ValidateBinary`, `binary.go:95-120`) rejecting whitespace-laden or missing executables.

**Net.** The injection surface is well-disciplined everywhere a developer was thinking about *injection per se* — newlines, control chars, batch quoting, port ranges. The one confirmed high is an *argv-position* flaw: untrusted config data reaching the alias slot without a `--` separator, exploitable because ssherpa deliberately reads real, potentially-shared SSH config. It is low-effort to fix and should be closed before feature-freeze, since the maintenance phase anticipates wider adoption (more shared/multi-tenant configs).

### 6.2 Filesystem safety

The atomic-write core (`internal/fsutil/write.go`) is mostly sound: `writeTempRename` uses `os.CreateTemp` (random name, `O_EXCL`, 0600), `Chmod`, `Sync`, atomic `Rename`, then `syncDir`; a `defer` removes the temp on any failure (ENOSPC, rename failure) so no partial target is left. `createBackup` uses `O_EXCL` so it cannot clobber/follow a pre-planted symlink at the backup path. New ssh-config / authorized_keys files default to 0600 and parent dirs are created 0700. Transcripts (`transcript.go:80,83`), session records, identity, markers, and daemon logs are all written 0600 inside 0700 dirs. So the headline "0600 in 0700" invariant holds for the *live* files.

Two concrete defects surfaced under experiment.

**(1) Backups under ~/.ssh are NOT hardened — they keep the original (possibly world-readable) mode.** In `AtomicWriteFile`, `mode` is captured from the existing file at write.go:63 (`mode = stat.Mode().Perm()`) and handed to `createBackup` at :65 *before* the `opts.Mode` override at :72-74. So when callers force 0600 (authkeys passes `Mode: authkeys.DefaultFileMode`), the live file is hardened but the backup keeps the pre-existing mode. Live proof — starting from a 0644 `authorized_keys`:
```
-rw-------  authorized_keys                              (live, hardened)
-rw-r--r--  authorized_keys.ssherpa-backup.20260611T...  (backup, world-readable)
```
A world-readable copy of the same key material is left in ~/.ssh indefinitely. One-line fix: pass the effective (post-override) mode to `createBackup`, or cap backups at 0600.

**(2) Control-master sockets land in a predictable shared-/tmp path with no ownership check and ignore XDG_RUNTIME_DIR.** `prepareControlMaster` (session.go:625) always uses `filepath.Join(os.TempDir(), "ssherpa-<uid>", "cm")` + `MkdirAll(...,0700)`. On Linux `os.TempDir()` is `/tmp`; `MkdirAll` neither fails nor fixes perms if another local user pre-creates `/tmp/ssherpa-<uid>` (or symlinks it). The socket multiplexes an *authenticated* ssh connection, so hijacking it hijacks the session. macOS is safe (per-user private `$TMPDIR`). Note the `incoming` package *does* prefer XDG_RUNTIME_DIR; the control-master path does not.

**(3) Symlinked ~/.ssh/config is silently de-referenced.** `readExisting` follows the symlink (`os.Stat`), then `writeTempRename` replaces the link name with a regular file. Live proof: after `ssherpa add`, `~/.ssh/config` became a plain file and the original symlink target was left stale. This is *safer* than writing through the link (no arbitrary-path write) but it silently breaks dotfile-managed (symlinked) configs — should be documented/decided before freeze.

Transfer paths: directory traversal is handled — remote filenames are reduced via `remoteBase`/`filepath.Base`, and the in-band committer refuses overwrite/symlink at the destination (`[ -e dest ] || [ -L dest ]` → rc=73, commitCommand inband.go:151). SFTP receive overwrite protection is advisory + TOCTOU (stat then unconditional `sftp get`), but the default CLI path aborts unless `--force`, so it is not a silent-clobber. Lower-priority: ssh-config writes never normalize an existing loose mode (mutate.go:1525 passes no `Mode`), and `ImportBundle` writes the transcript via non-atomic `os.WriteFile` (transcript.go:397, follows symlink/`O_TRUNC`).

### 6.3 The remote-terminal trust boundary

**Attacker model.** Once `ssh` is launched under PTY supervision, every byte the remote host writes is attacker-controlled if that host is compromised, malicious, or MITM'd. ssherpa does not merely pass those bytes to the user's terminal — it *parses* a privileged subset (OSC 7/133/777 and a 0x1e-framed telemetry channel) and reflects derived state back into its own trusted UI (the Ctrl-] overlay / session map) and into persisted `SessionRecord`s, transcripts, and exportable bundles. So the trust boundary is not "remote → my terminal emulator" but "remote → ssherpa's own chrome and on-disk artifacts." `internal/session/osc_tracker.go` is the demarcation line, and `copyOutput` (`internal/session/session.go:1997`) is where remote bytes first cross it via `tracker.ObserveAndFilter` (`session.go:2007`).

**What held up well.** The OSC byte-state machine (`feedOSC`, osc_tracker.go:144-178) is a clean, bounded ground/ESC/OSC/OSC-ESC FSM that correctly handles both BEL (`0x07`) and ST (`ESC \`) terminators and re-emits a stray ESC mid-OSC. Payloads are length-capped: OSC at `oscMaxPayload=8192` with reset-on-overflow (`appendOSCByte`, :209), telemetry at `telemetryMaxPayload=16384` with replay-on-overflow (`feedTelemetry`, :131). The 777/0x1e telemetry decoders (`parseSessionTelemetryPayload`, :311) require valid base64, valid JSON, and a non-empty `ID` before accepting a record, and unparseable frames are replayed verbatim rather than silently dropped. OSC 133 is whitelisted to four markers (:342). Bundle import (`ImportBundle`, transcript.go:347) verifies both `TranscriptSHA256` and `RecordSHA256` against manifest, pins `BundleVersion==1`, and quarantines provenance by regenerating the local ID and zeroing PIDs/parentage (:401-406). This is careful, defensive parsing — the boundary was clearly designed, not accidental.

**Confirmed HIGH — OSC7 cwd escape injection into the overlay.** `parseOSC7` (osc_tracker.go:326) accepts `file://...`, runs it through `url.Parse`, and takes `u.Path` (:335) — which **percent-decodes**, so a remote sending `ESC]7;file://host/%1b]0;PWNED%07ELSEWHERE` yields a `RemoteCWD` containing a *raw ESC*. That value flows unfiltered through `applyRemoteStateToRecord` (:356) into `record.RemoteCWD`/`RemoteHost`, is persisted, and is rendered by `RemoteSummary` (sessionview.go:1802-1809) as `"cwd "+host+":"+cwd`. The only sanitizer on that render path, `truncateVisible` (sessionview.go:1932-1941), calls `termstyle.Strip` **only when the string exceeds the column width** — a short payload passes through verbatim. The escape thus executes *inside ssherpa's own overlay frame*, the one chrome the user trusts to be ssherpa-controlled, enabling spoofed status lines, cursor/title manipulation, and content masking of the very escape-rope/session-map UI a user relies on during an incident. Operationally: a hostile remote can lie to the local operator through the local tool's trusted surface.

**Confirmed — remote-forged telemetry SessionRecords.** The 777-OSC and 0x1e-frame channels let a remote *inject* a fully-attacker-shaped `state.SessionRecord` into `observed.Mirrors` (osc_tracker.go:87, 264). ID-non-empty and size caps are the only gates; the remote dictates the JSON. These become mirrored child records (`mirrorRemoteSessionRecord`, session.go:2024) — i.e. the remote populates ssherpa's session map with records of its choosing.

**Confirmed — Clean leaks; raw replay.** Transcript `Clean` (transcript.go:591) only runs `termstyle.Strip` (CSI `ESC[` sequences, termstyle.go:35-51) plus `stripOSC` (transcript.go:723). It does **not** neutralize RIS (`ESC c`), charset designators (`ESC ( B`), DCS/SOS/PM/APC strings, or bare lone ESC bytes — all survive into "cleaned" `Text`/`Grep` output (transcript.go:551, 625). And `Replay` (transcript.go:655-676) writes `frame.Data` **raw** to the operator's terminal: replaying an attacker-recorded transcript re-executes every captured escape sequence, making transcript review itself an injection vector.

**Bundle import as untrusted input.** Hash-checks bind the *bundle's own* manifest to its payload — they do not authenticate the source. `record.json` deserializes attacker-controlled fields (RemoteCWD etc.) that then ride the same unsanitized overlay/replay paths; OriginClass (:412) only *labels* foreignness, it does not constrain it.

**Bottom line.** The parser is robust; the *sinks* are not. Sanitization must move to every render-into-chrome and replay-to-terminal boundary, not depend on width-triggered truncation.

### 6.4 Supply chain & release integrity

**Scope read:** `.github/workflows/ci.yml` (141 lines), `.github/workflows/release.yml` (37 lines), `.goreleaser.yaml` (102 lines), `go.mod`/`go.sum`, `cmd/ssherpa/main.go`, `internal/cli/cli.go` (printVersion), `README.md`, `SECURITY.md`. `.github/` contains only the two workflows — no dependabot.yml, no CODEOWNERS, no scanning config.

**What's healthy.** CI is least-privilege (`permissions: contents: read`, ci.yml:10-11); no `pull_request_target` anywhere; release.yml grants only `contents: write`. The dependency graph is admirably small: 3 direct deps, 40-line go.sum, zero `replace` directives, and local `go mod verify` returns "all modules verified". The ultraviolet pseudo-version pin (go.mod:13) is exactly what bubbletea v2.0.6 requires per `go mod graph` — upstream's pin, not a local override. The root `ssherpa` binary is gitignored, not tracked. CI smoke-tests are unusually thorough for a TUI.

**Critical pre-lock gaps.**

1. **Mutable action pins + secrets exposure (high).** All actions use tag pins (`actions/checkout@v6`, `actions/setup-go@v6`, `goreleaser/goreleaser-action@v7`), and goreleaser itself floats (`version: "~> v2"`). release.yml:34-36 hands the unpinned goreleaser action both `GITHUB_TOKEN` (contents:write) and `TAP_GITHUB_TOKEN` (PAT writing to 0xbenc/homebrew-tap@main). A retargeted action tag or malicious goreleaser release = full release + Homebrew hijack. Pin to SHAs, pin goreleaser exactly, scope the PAT fine-grained.

2. **Zero signing/provenance (high).** Only an unsigned `checksums.txt` (.goreleaser.yaml:83-84). No `signs:`, no `sbom:`, no cosign/GPG, no `id-token: write`, no attest-build-provenance. deb/rpm unsigned, and README tells users `sudo dpkg -i`. IMPROVEMENTS.md #40 already flags this. GitHub artifact attestations are the cheapest fix and should land before the freeze.

3. **`go mod tidy` as the release before-hook (.goreleaser.yaml:8)** can rewrite go.mod/go.sum at release time, so shipped binaries may embed a dep graph CI never tested. Go's default `-mod=readonly` protects CI itself (no explicit GOFLAGS needed), but this hook is an explicit writer. Replace with `go mod verify`.

4. **Reproducibility / verifiability.** Release builds lack `-trimpath` (no `flags:` stanza) while *both* CI builds (ci.yml:105,140) and README from-source instructions use it — released bits are built differently than tested bits and embed runner paths. ldflags inject wall-clock `{{.Date}}`; no `mod_timestamp`. `ssherpa version` prints version/commit/built via ldflags (cmd/ssherpa/main.go:9-13 → internal/cli/cli.go:1507), so a user can see a claimed tag but cannot cryptographically verify it or rebuild byte-identically.

5. **Release gating.** Tag push → goreleaser directly; no tests in release.yml, no CI dependency, and `release.draft: false` — a tag on a red commit publishes to GitHub + Homebrew in one shot.

6. **Tap path.** Cask pushed to homebrew-tap@main via the PAT (.goreleaser.yaml:74-78). Single point of trust with no branch protection requirement, and no drift detection if the cask push partially fails. Also: the cask ships an un-notarized binary with no quarantine hook — modern macOS Gatekeeper may block first launch of the primary documented install path; test on a clean Mac before lock.

7. **Maintenance-phase blindness.** No dependabot (gomod or github-actions ecosystems) and no govulncheck job — during a long freeze, CVEs in x/sys or the charm stack surface only by accident. SHA-pinning (fix #1) *requires* dependabot to stay current.

8. **SECURITY.md (line 19)** demands private reporting but gives no channel — no email, no GHPR link. One-line fix, adoption-facing.

**Cache poisoning:** `setup-go cache: true` in the release job restores main-branch-scoped caches into the release build; go.sum is only checked at download time, not on cache reuse. With a 40-line go.sum, `cache: false` in release costs seconds and removes the path.

**Lock-gating set:** SHA-pin actions + scope PAT; add attestations/signing; replace `go mod tidy` hook; add dependabot + govulncheck; add a security contact. All are config-only changes with no feature-freeze impact.

---

## 7. Performance at scale

Build: `go build -o /tmp/ssherpa-perf ./cmd/ssherpa` OK. All numbers measured on this machine (darwin/arm64, APFS).

#### Inventory load (`list --json`, synthetic configs, 2-3 runs each)

| config | hosts | wall time |
|---|---|---|
| flat | 100 | 0.00-0.01s |
| flat | 1000 | 0.04-0.05s |
| flat | 2000 | 0.18-0.19s |
| flat | 3000 | 0.41s |
| flat | 5000 | 1.13-1.14s |
| flat | 10000 | 4.53-4.54s |
| 50 chained Include files | 5000 | 1.01-1.02s |

Perfect quadratic fit (t ≈ 4.56e-8·n²). Instrumented split shows the parser is **linear and fast** while `hostlist.Build` is the quadratic term:

| hosts | sshconfig.Load (parse) | hostlist.Build |
|---|---|---|
| 1000 | 3.2ms | 70.6ms |
| 5000 | 7.8ms | 1.096s |
| 10000 | 14.3ms | 4.402s |

Cause: for each of n aliases, `applyParsedEffectiveValues` (hostlist.go:128) rescans **all** n blocks calling `filepath.Match` per pattern (hostlist.go:192) — n² glob matches. This cost is paid by `list`, `show`, `check`, every TUI launch, and — because `runConnect`'s home loop calls `loadInventory` on every return to the home picker (cli.go:273-288) — after **every** action/refresh.

#### Component benchmarks

| path | result |
|---|---|
| oscTracker.ObserveAndFilter, 32KB plain text | 174 MB/s |
| same, SGR-escape-heavy | 173 MB/s |
| transcript.WriteOutput 32KB frames | 791 MB/s |
| state.WriteRecord (changed record) | **6.03 ms/op** (fsync temp + fsync dir, write.go:175/188) |
| state.ListRecords @ 5000 records | **107.9 ms/op** |
| picker applyFilter per keystroke @1k / @10k items | 0.20ms / 2.05ms (linear — OK) |
| 1-byte read+write copy pattern (pipes) | 0.78-0.81 MB/s (~1.26 µs/byte) |

#### End-to-end PTY throughput (200MB `cat` via fake ssh binary under `script`)

| scenario | wall | MB/s |
|---|---|---|
| raw `script` + cat (baseline) | 0.91-0.93s | ~217 |
| ssherpa supervised | 2.38-2.68s | ~78-84 |

Supervision costs ~2.6x vs a bare PTY — the per-byte OSC state machine (174 MB/s ceiling) plus the second PTY hop dominate. 83 MB/s comfortably exceeds WAN ssh; only fast-LAN bulk transfers would notice. Transcript recording is opt-in (overlay toggle) and benchmarks at 791 MB/s — not a bottleneck.

**OSC-spam resistance (good result):** a session emitting 400 OSC 133 prompt transitions in 6.5KB completed in 0.03-0.27s — no 6ms-per-transition amplification, because `copyOutput` aggregates `RemoteChanged` per 32KB read chunk and calls `recordRemoteState` (→ 6ms double-fsync `WriteRecord`) at most once per chunk (session.go:2007-2025). The write is still synchronous in the output loop; with shell integration it adds ~6-12ms per command — acceptable today, worth knowing before freeze. The latency watchdog writes only on warn/recover edges (session.go:1446-1485), contradicting IMPROVEMENTS #59's premise — no fix needed there.

**Stdin path:** `copyInput` reads stdin **1 byte per syscall** and writes each byte to the pty (session.go:747-761). Measured floor 0.8 MB/s — a 1MB paste burns ~1.3s+ of pure syscall time (a naive 5MB pipe rig stalled for minutes, also hampered by PTY canonical buffering). Fine for typing, bad for large pastes.

**Session-record scale:** the home picker assembles its data via **four independent `ListRecords` directory scans** (`pickerSessionCounts`, `pickerStoppableSessionCount`, `pickerActiveTunnels`, `pickerActiveProxies` — cli.go:718-723), each reading + JSON-decoding every record file (108ms @5k). Records are **never auto-pruned** — only the manual `session prune` (session.go:1589) — so a busy operator accumulates one JSON per session forever, and every home render/refresh degrades linearly ×4.

**check:** probes run strictly sequentially (check.go:117-128) with 5s SSH + 2s ICMP default timeouts (check.go:150) — 100 unreachable hosts ≈ 12 minutes wall.

**Transcript inspection:** `transcript.read` accumulates every frame of a cast into memory (transcript.go:504-515); `session text/grep/replay/export` on a 50MB (default cap; raisable via `--record-max-bytes`) cast holds the whole decoded recording in RAM. Structural, low severity.

#### Bottom line for the freeze
The single must-fix is the O(n²) `hostlist.Build` — it converts the headline "works with your real ssh config" promise into a 1-4.5s freeze at fleet scale, re-paid on every home-loop pass. Second priority: collapse the 4x `ListRecords` scans and decide an auto-prune/retention policy before records accumulate in the wild. Everything else (1-byte stdin, sequential check, 83 MB/s supervised ceiling, 6ms fsync writes) is quantified, bounded, and can be scheduled into the maintenance phase.

---

## 8. UX & support-load

Every confusing first-run moment becomes a GitHub issue at scale; this section is effectively a forecast of your issue tracker.

Built `/tmp/ssherpa-ux` from `cmd/ssherpa` and exercised it against a temp `HOME`, controlled `--config`/`--state-dir`, a PTY driver (python pty), and piped/closed stdin. All claims verified against code.

#### First run
With NO `~/.ssh/config`, empty config, or `Host *`-only config, `ssherpa list` prints **nothing and exits 0**; `--json` reveals a diagnostic ("config file does not exist") that the human path silently drops (`runList` never prints `inventory.Diagnostics`). The interactive picker is fine: it opens with 0 hosts, a full Actions menu starting with ADD, and "0 hosts 1 warning" in the header — but the warning text itself is unviewable. Esc/Ctrl-C/`Q` all quit cleanly; lowercase `q` types into the filter ("No matches"), footer does say `Q quit`.

#### Worst finding: telemetry garbage over plain ssh
`shouldEmitSessionTelemetry` (session.go:2065) fires whenever `SSH_CONNECTION`/`SSH_TTY` is set — i.e. for **anyone running ssherpa on a box they reached with plain ssh**. The `\x1e`-framed fallback (osc_tracker.go:284) is plain printable base64, so the user's terminal shows two ~900-char `ssherpa-session:eyJ...` blobs wrapping every supervised session's output (reproduced under PTY: the real error "ssh: Could not resolve hostname a" was buried between two blobs). Only an upstream ssherpa supervisor filters these. This is a top-volume "ssherpa prints garbage" ticket and the frame format is a compat surface about to be frozen.

#### Non-TTY behavior
`ui.Pick` (picker.go:249) and all choosers run bubbletea unconditionally. With stdin at EOF (`</dev/null`, cron, CI) the picker **hangs forever** after writing alt-screen ANSI to stderr (reproduced; process alive after kill -0 check). With piped bytes, keys are interpreted as navigation. No `IsTerminal` guard exists anywhere in the CLI entry paths (only inside session/confirm-stdin code). Even `--print` mode opens the TUI when `--select` is absent.

#### Unknown subcommand = remote ssh command
`Run`'s `default:` falls through to `runConnect` (cli.go:222), and unrecognized positionals become `SSHArgs`. Measured: `ssherpa lst --select a --print` → `[print] ssh ... a lst` — a typo'd subcommand is silently executed **on the remote host**. On a TTY it inexplicably opens the picker; on non-TTY it hangs.

#### Help system
11 of 14 subcommands' `--help` print the identical 124-line global usage; only `incoming`, `theme`, `session` have dedicated text; `proxy saved --help` prints one line while sibling `forward saved --help` prints the global blob. `ssherpa help add` errors: "help does not accept arguments" (cli.go:215). The global usage ends with an internal "Phase 10:" development-phase paragraph (cli.go:137), lists only 5 of 13 `session` subcommands, and omits the parsed `--theme` flag and `recv` alias.

#### Error message quality (measured)
Good: missing ssh binary → "required SSH client binary ... does not exist; install an OpenSSH client, or pass --ssh-binary PATH / set SSHERPA_SSH_BINARY" (exit 1). `--select nope` → `alias "nope" not found` (exit 2). Mutations show dry-run diffs and announce backups. Bad: `check` failures render an **empty MESSAGE column** — `checkAlias` sets `SSHError` but not `Message`, the table prints only `Message`, and `defaultRunSSHCheckProbe` discards ssh's stderr entirely, so "Could not resolve hostname" is never seen anywhere (JSON shows only "exit status 255").

#### NO_COLOR / TERM=dumb
`NO_COLOR` is honored (termstyle/theme.go:158). `TERM=dumb` is not: the picker still emits kitty-keyboard-protocol queries, bracketed paste, and box-drawing.

#### Destructive confirms
`confirmModel` defaults `selectedYes: !opts.Danger` (confirm.go:87). Default-No: delete alias, delete key, delete saved forward/proxy, transfer overwrite (Danger:true, transfer.go:449), delete local data; delete-all requires a typed phrase. **Inconsistent**: `authkeys replace` — which can wipe every authorized key (lockout) — uses default-Yes `confirmActionChoice` (authkeys.go:236), and the "raw replay can emit escape sequences from another machine" warning also defaults Yes (session.go:515). Cancelled delete-all prints `confirmation did not match "delete 1 aliases"` (grammar) and exits 0.

#### Discoverability
Ctrl-]/escape rope: README only. No banner at supervised-session start; man page never mentions the overlay, hotkey, or rope. Completions: packaging installs them correctly, but the only in-product pointer — the TUI "Completions and manpage" picker — resolves `completions/ssherpa.bash` via `filepath.Abs` against the **user's cwd** (tui_features.go:283), printing a nonexistent path for every installed binary, with a "source this file" hint.

Bonus dead code: `--theme NAME` and the `theme =` config key are parsed (cli.go:591, theme.go:186) but `ResolveTheme` never reads `opts.Name`/`cfg.BaseName` — both are silent no-ops.

---

## 9. Docs, release & distribution

> **Caveat:** the audited tree is v1.7.1 while origin has released through v1.9.1. All code claims are against the local tree; released-artifact forensics used the published v1.7.1 binaries. Re-check anything marked release-sensitive against the tip before acting.

### 9.1 Docs drift

**Method.** Read the full CLI dispatch surface (`internal/cli/cli.go`, `route.go`, `session.go`, `mutate.go`, `authkeys.go`, `transfer.go`, `check.go`, `incoming.go`, `theme.go`) and executed ~45 documented examples with a locally built binary (`go build ./cmd/ssherpa`) against `internal/sshconfig/testdata/matrix/config` and temp configs/state dirs.

**What works (verified by execution).** `list/show` (text+JSON, exit 2 on miss), connect `--print/--json/--select`, `jump` (flag + positional forms), `forward`/`proxy` launch `--print` (correct `-L`/`-D` argv), `add`/`edit delete`/`edit delete-all --dry-run`, full saved forward/proxy CRUD (save/list/show/edit/rename/delete, `delete` correctly refuses without `--yes`), `check` alias + `--saved-forward` (exit 2 on failure), all `authkeys` subcommands incl. positional-fingerprint delete and backups, `session list/map/identity/prune/stop-all/show` (exit 2 on missing ID), `incoming list/hook`, send/receive `--print` incl. `recv` alias, `--composer-key ctrl-r`, latency-flag validation errors, deprecated `--theme` no-op. Defaults table checked against code: proxy port 1080, prune 168h, state dirs, Kitty detection — all accurate. Install docs verified externally: `0xbenc/homebrew-tap` exists with the documented cask command, and release `v1.9.1` assets exactly match README naming (`ssherpa_1.9.1_linux_amd64.deb` etc.) and `.goreleaser.yaml` ships completions+man as `docs/non-interactive.md` claims. Note: this checkout's tags end at v1.7.1 while GitHub's latest release is v1.9.1 — the audited tree may trail what users currently download.

**Main drift clusters.**
1. **Shell completions are a release behind the CLI.** All three completion files only know `session list map show stop-all prune`; the shipped `session` command has 7 more subcommands (`log replay grep export bundle identity browse`) that are headline README features. Forward reconnect flags are partially/fully missing, zsh has no `authkeys` handling at all, fish lacks `check --filter/--user/--all` and the `recv` alias, and no shell completes the top-level connect flags (`--select`, `--print`, `--latency-warn`, ...). Since `.goreleaser.yaml` installs these into bash/zsh/fish system dirs, every package user gets stale completions.
2. **docs/non-interactive.md inaccuracies.** Theme-file default is wrong on macOS (`os.UserConfigDir()` → `~/Library/Application Support/...`, not `~/.config/...`); `--all-sources` on the destructive `edit delete` is described as "delete all matching source stanzas instead of the first target" but actually gates multi-*file* deletion (all same-file stanzas are always deleted — verified by dry-run on the matrix config's duplicate `prod`); the overlay key table omits `S`/`V` (in-overlay file send/receive); the env-var table omits `SSHERPA_SFTP_BINARY` (which ssherpa's own error hint tells users to set) and `SSHERPA_IGNORE_USER_GIT` (whose default silently hides `User git` hosts like github.com from `list` and the picker); `version` output format and `--record-max-bytes` default (50 MiB vs "50MB") are slightly off; exit code 3 for check is effectively unreachable (unreadable config degrades to a diagnostic + exit 2 "alias not found").
3. **Man page is the thinnest surface**: it omits `list`/`show` entirely, presents `session` as only records/maps/stop-all (no recording/replay/export/bundle, which README advertises), and contains zero supervised-session keybinding documentation.
4. **Built-in `ssherpa help` drift**: the Session Commands block lists only 5 of 12 subcommands, and the help ends with an internal "Phase 10:" development-phase paragraph that should not ship in a stable, frozen 1.x.
5. **Undocumented behavior**: overlay send auto-falls back to in-band base64 PTY transfer when direct SFTP fails, while `SSHERPA_TRANSFER_TRANSPORT` is documented as "Internal/testing" and README says transfers happen "over OpenSSH SFTP".

**New-user assessment.** Install instructions are accurate and verified end-to-end; the first-run interactive path (run `ssherpa`, pick a host) is discoverable, and the overlay footer self-documents its keys. The biggest first-touch gaps for mass adoption: README has no quick-start/usage section (it jumps from feature blurbs to install to a CLI-reference link), `man ssherpa` undersells half the product, and tab-completion actively misleads on the session/recording feature set. docs/non-interactive.md is genuinely excellent and ~95% accurate — it should be promoted as the canonical reference once the inaccuracies above are fixed, and the completions should be regenerated (or generated from a single source of truth) before the freeze.

### 9.2 Release & distribution

Audit caveat: local checkout is at v1.7.1 but `git ls-remote` shows origin already at v1.9.1 and the published tap cask is 1.9.1 — all code claims below are from the local tree; the released-artifact inspection used v1.7.1 binaries (config has not changed materially per the generated cask).

#### Platform matrix: ship vs test

| Target | Released as | CI coverage |
|---|---|---|
| linux/amd64 | tar.gz + deb + rpm | full `go test` + 8 smoke steps (ubuntu-latest) |
| linux/arm64 | tar.gz + deb + rpm | **compile-only** (ci.yml cross-build) |
| darwin/arm64 | tar.gz + cask | full test (macos-latest = arm64) |
| darwin/amd64 | tar.gz + cask | **compile-only** |
| freebsd/amd64 | none | none — but `GOOS=freebsd go build` succeeds clean (verified locally) |
| windows | none | none (PTY supervision; correctly unshipped, but undocumented) |

Pure Go: `CGO_ENABLED=0` in `.goreleaser.yaml:15` and confirmed in the published binary (`build CGO_ENABLED=0`). Half of what ships is never executed under test; `ubuntu-24.04-arm` runners are free for public repos, making the linux/arm64 gap cheap to close. `go test` runs without `-race`.

#### Binary forensics (downloaded v1.7.1 linux_amd64, `go version -m`)

```
ldflags="-s -w -X main.version=1.7.1 -X main.commit=a112f3b... -X main.date=2026-06-06T00:50:21Z"
vcs.modified=false   (no -trimpath line present)
strings: /home/runner/work/ssherpa/ssherpa/internal/sshcmd/binary.go ...
```

Releases are **not built with `-trimpath`** (CI and the README from-source instructions both use it — release binaries are the odd ones out), embed GitHub runner paths, and stamp wall-clock build time, so they are not reproducible. Fix is two lines (`flags: [-trimpath]`, `mod_timestamp: "{{ .CommitTimestamp }}"` + `{{.CommitDate}}` for `main.date`).

#### Supply chain

Release assets per GitHub API: archives + deb/rpm + `checksums.txt` only. No `signs:`, `sboms:` in goreleaser; no `attest-build-provenance` step; release.yml grants only `contents: write` (no `id-token`). An unsigned checksums.txt hosted next to the artifacts it checksums provides zero tamper protection. For an SSH supervisor heading into wide adoption, keyless cosign or GitHub attestations are essentially free.

#### Homebrew flow

Cask publishing is fully automated via goreleaser (`homebrew_casks`, TAP_GITHUB_TOKEN PAT) and the generated cask correctly installs the binary, man page, and all three completions (verified by fetching `Casks/ssherpa.rb` from 0xbenc/homebrew-tap). Two gaps: (1) binaries are not signed/notarized and the cask has no quarantine-removal `postflight` hook — exactly the case goreleaser's homebrew_casks docs call out for Gatekeeper blocks on modern macOS; (2) the generated `on_linux` stanza is dead weight — Homebrew on Linux does not support casks, so Linuxbrew users have no brew path at all (a `brews:`/formula or documented limitation is needed). The PAT is also a single point of failure mid-release: artifacts publish, then the cask push fails with no retry.

#### Changelog / versioning practice

23 local (25 remote) tags; no CHANGELOG.md anywhere. Release bodies are raw commit lists (`* 718a078b... Improve identity auth mode UX`); tag annotations are single commit subjects; v1.7.0's annotation is "Fix metadata modal wrap assertion" — a pure fix shipped as a **minor** bump. SECURITY.md supports only "the latest minor release line", so semver discipline directly determines who gets security fixes; before freeze, the versioning contract and a human-readable changelog (or at least goreleaser commit-prefix grouping) should be nailed down.

#### Update path

brew users: fine. Tarball users: manual. deb/rpm users: **no apt/yum repo, no self-update, no version-check** — they will silently run stale builds forever while SECURITY.md "expects users to upgrade to the latest available minor version". The deb/rpm also omit a `dependencies: [openssh-client]` declaration despite ssherpa hard-requiring `ssh` at runtime.

#### Shipped-docs drift (in every artifact)

All three completions list session subcommands `list map show stop-all prune`; the CLI dispatch (internal/cli/session.go:83-106) also has `log replay grep export bundle identity browse` — the README's headline transcript features are uncompletable, and the man page gives `session` one line. The only guard is `TestGeneratedShellAndManArtifactsExist`, which asserts the files contain the string "ssherpa". Archives and nfpm packages also omit the LICENSE (the `files:` override drops goreleaser's default LICENSE/README inclusion; MIT requires shipping the license text).

#### Ranking for the lock

1. Add `-trimpath`/reproducibility flags (one-time binary change — do before freeze so post-freeze patches diff cleanly).
2. Sync completions/man with the frozen CLI surface + add a drift test (the frozen surface is the docs).
3. Ship LICENSE in archives/packages.
4. Attestations/signing + SBOM.
5. Gate release.yml on tests; add `goreleaser check`/snapshot job to CI.
6. CHANGELOG + semver policy statement; document update path for deb/rpm users.
7. Test linux/arm64 in CI; decide and document the BSD/Windows story.

---

## 10. Dependency health & freeze policy

**Module graph is exceptionally small**: 3 direct deps, 15 modules total, no vendor dir, go.sum 40 lines. `go list -m -u all` succeeded from /Users/xbenc/0xbenc/ssherpa.

| Dep | Pinned | Latest | Last release | Upstream activity | API surface used | 2-yr freeze risk |
|---|---|---|---|---|---|---|
| charm.land/bubbletea/v2 | v2.0.6 | **v2.0.7** (2026-06-01) | v2.0.0 only 2026-02-24 | pushed 2026-06-08, 176 issues | core v2: Model/Cmd/Msg, KeyPressMsg, NewProgram/NewView, With{Input,Output,Context}, RequestWindowSize (25 files) | **Medium** — young major, 7 patches in 14 weeks |
| charmbracelet/x/term | v0.2.2 | v0.2.2 (2025-10-31) | monorepo pushed 2026-06-08 | IsTerminal/MakeRaw/Restore/GetSize only | **Low** — frozen-stable v0 surface |
| creack/pty | v1.1.24 | v1.1.24 (2024-10-31) | pushed 2026-06-01, untagged fixes on master | pty.Start (session.go:1930), pty.InheritSize (session.go:1638) | **Low** — 2-call surface, stable for years |

**Ultraviolet pseudo-version, root-caused**: it is purely transitive (`go mod why -m`: sessionview → bubbletea/v2 → ultraviolet; no direct import in ssherpa). bubbletea v2.0.6's own go.mod requires `github.com/charmbracelet/ultraviolet v0.0.0-20260416155717-489999b90468`, and **v2.0.7 still requires a pseudo-version** (`v0.0.0-20260525132238`). `go list -m -versions github.com/charmbracelet/ultraviolet` returns **zero tags ever** — bubbletea's "stable" v2 sits on an unreleased renderer. Freeze-safety of the pin itself is fine (pseudo-versions are immutable, proxy.golang.org caches forever). The real risks: when ultraviolet tags v0.1.0 it sorts above every v0.0.0- pseudo-version, so `go get -u ./...` or a lone dependabot bump would advance the renderer past bubbletea's tested commit; and renderer behavior changes ship inside bubbletea patch releases with no independent changelog. Rule for the freeze: **only move ultraviolet by moving bubbletea**, never independently.

**Security**: govulncheck — "Your code is affected by 0 vulnerabilities", but one module-level hit: **GO-2026-5024** (x/sys@v0.43.0, NewNTUnicodeString overflow, Windows-only, fixed v0.44.0, uncalled; goreleaser builds linux/darwin only). Bumping bubbletea to v2.0.7 brings x/sys v0.45.0; latest is v0.46.0. Fix before tagging.

**Go toolchain**: go.mod pins `go 1.26.3`; CI and release both use `setup-go` with `go-version-file: go.mod` — so releases compile with exactly 1.26.3 while 1.26.4 is already out (local `go version` confirms 1.26.4). Go supports the latest two majors: 1.26 (Feb 2026) loses security patches when 1.28 ships (~Feb 2027) — **inside the freeze window**. Recommendation: CI/release should use `go-version: stable` + `check-latest: true`; demote go.mod to the language floor `go 1.26.0` (the patch-level pin also forces toolchain auto-download and breaks GOTOOLCHAIN=local distro builds); plan one deliberate minor bump per ~12 months.

**Automation gap (the gating issue)**: no dependabot.yml, no govulncheck in CI, no scheduled CI trigger. During a low-touch period nothing would ever tell the maintainer about a CVE. ci.yml's only triggers are push/PR/dispatch — zero heartbeat without commits.

**charm.land vanity path**: verified charm.land serves `go-import → github.com/charmbracelet/bubbletea`. proxy.golang.org's immutable cache insulates builds if the domain lapses; expect other charm libs (ultraviolet, x/*) to migrate paths during the freeze — another reason to take deps only via bubbletea bumps.

#### Recommended dependency policy for the freeze
1. **Pre-freeze (do now)**: bubbletea v2.0.6→v2.0.7, x/sys→v0.46.0, `go mod tidy`, full test + manual TUI smoke. Clears GO-2026-5024 and starts the freeze one renderer-generation fresher.
2. **Pin**: everything stays pinned via go.mod/go.sum (already committed). **Do not vendor** — 15 modules all proxy-cached and immutable; vendoring adds diff noise without supply-chain benefit. Add an SBOM to goreleaser instead so consumers can self-audit frozen releases.
3. **Dependabot**: gomod weekly, direct deps only, grouped patch/minor, ignore majors and ignore `github.com/charmbracelet/ultraviolet` (security PRs exempt, manually smoke-tested); plus github-actions ecosystem. Add govulncheck to ci.yml with a weekly `schedule:`.
4. **Cadence commitment**: quarterly bubbletea v2.0.x patch-bump with 10-minute TUI smoke (overlay, resize, paste, session map); immediate action on any govulncheck hit; x/term and creack/pty bumped only when tagged; Go toolchain floats with `stable` in CI, go-directive bump yearly.
5. **Refuse during freeze**: bubbletea v3, any ultraviolet path/identity migration, any `go get -u ./...`.

Bottom line: the dependency tree is among the most freeze-friendly possible — three direct deps, two of them with single-digit-function API surfaces. The risk is not the deps; it is the absence of any automated signal (dependabot + scheduled govulncheck) and a toolchain pin that goes EOL mid-freeze. Fix those two and the 2-year posture is sound.

---

## 11. Governance & the maintenance-mode operating model

Note: `gh` CLI is not installed in this environment, so live repo stats, labels, branch protection, and whether GitHub Private Vulnerability Reporting is enabled could NOT be verified. Everything below is grounded in the working tree at /Users/xbenc/0xbenc/ssherpa.

#### What exists vs. what's missing

| Asset | Status |
|---|---|
| LICENSE (MIT, (c) 2026 Ben Chapman) | present, fine |
| README.md | good install/feature docs; no support/versioning section |
| CONTRIBUTING.md | 32 lines: local checks + safety rules only |
| SECURITY.md | present, but **no reporting channel** (see findings) |
| docs/non-interactive.md | strong 815-line CLI reference incl. exit-code table |
| .github/workflows/{ci,release}.yml | present |
| ISSUE_TEMPLATE/, PR template, dependabot.yml, CODEOWNERS, FUNDING.yml, SUPPORT.md, CODE_OF_CONDUCT.md, CHANGELOG.md, release runbook | **all absent** (`find` across repo confirms only .goreleaser.yaml and release.yml match) |

#### Bus factor = 1, release pipeline has one undocumented secret

`git shortlog -sne` shows 108/108 commits by Ben Chapman. Release is tag-push → GoReleaser, which is good automation, but the Homebrew cask publish depends on a personal `TAP_GITHUB_TOKEN` secret (release.yml:36, .goreleaser.yaml:78) that is documented nowhere; if it expires, releases half-fail at tag time. Tags are lightweight (`git for-each-ref` → `commit`), there is no RELEASING.md, and CONTRIBUTING only mentions `goreleaser check` as a local smoke test. Nobody but the maintainer could currently cut a correct release.

#### Cadence and support policy are mutually incoherent

22 tags shipped between 2026-05-24 and 2026-06-05 (v1.3.0→v1.7.1 in 8 days). SECURITY.md says "Only the latest minor release line is eligible to receive security updates" — at the observed cadence that is a support window of ~1–2 days. Entering feature freeze, neither the freeze, the meaning of minor vs. patch, nor any deprecation process is written down anywhere, even though the project has real compatibility surfaces users will script against (documented exit codes at docs/non-interactive.md:58, `--json` outputs, state dir layout, transcript bundle format, and already one deprecated flag: `--theme VALUE | Deprecated no-op accepted for compatibility`, docs/non-interactive.md:770).

#### Triage and automation gaps

No issue templates exist, despite the project having exactly the tool templates need: `ssherpa version` prints version/commit/date (internal/cli/cli.go:1507-1512). No dependabot.yml (IMPROVEMENTS.md item #49 already flags this), no stale handling, and CI (ci.yml) runs gofmt/vet/test/smoke but no `go test -race` (PTY/session/state concurrency code), no `govulncheck`, and no `goreleaser check` — so the release config is only validated when a tag is already pushed. Actions are tag-pinned (`actions/checkout@v6`), not SHA-pinned. Release artifacts are unsigned (no cosign/GPG, no SBOM, no GitHub artifact attestation).

#### Proposed maintenance-mode operating model

1. **Declare the freeze in writing.** Add a "Project status: maintenance" section to README + CONTRIBUTING: bugfix/security/docs PRs accepted; features go to discussion first; link IMPROVEMENTS.md as non-binding (and fix its false "Not committed" footer; items #18/#35/#37 are already shipped per commits 2daf46d/f92c7e7/5448f8d — prune it).
2. **Publish a 1-page POLICY section** (SECURITY.md or new SUPPORT.md): patch releases on demand for bugs/CVEs; minor releases at most quarterly; latest minor supported (now coherent because minors are rare); frozen surfaces = CLI commands/flags, exit codes, `--json` shapes, state schema, transcript bundle format; deprecations get one minor of warning.
3. **Fix the security contact today**: enable GitHub Private Vulnerability Reporting and name it in SECURITY.md, plus a fallback email.
4. **Write RELEASING.md**: annotated tag → CI green → push tag → verify cask/deb/rpm artifacts; document TAP_GITHUB_TOKEN scope, owner, expiry, and rotation. This is the cheapest bus-factor mitigation available.
5. **Automation for a solo maintainer**: dependabot (gomod weekly + github-actions weekly), `go test -race` + `govulncheck` + `goreleaser check` jobs in ci.yml, branch protection requiring the `test` matrix, and 2 issue templates (bug: require `ssherpa version` output, OS, terminal, `ssh -V`, redacted config snippet; feature: auto-label and state freeze policy) + a config.yml routing questions to Discussions. Skip stale-bots; at this scale they create noise, not leverage.
6. Optional: FUNDING.yml and SHA-pinned actions; sign checksums.txt (GitHub attestations are zero-key-management) — worth doing before adoption grows since this tool edits `authorized_keys`.

---

## 12. Confirmed findings register

155 findings survived adversarial verification. Critical and high findings get full detail; mediums are tabulated; lows are listed. 84 additional lower-confidence observations (single-pass, never adversarially verified) are listed at the end — treat them as leads, not conclusions.

### 12.1 Critical & high findings (full detail)

#### CRITICAL-01 · transcript read() aborts entire recording on one torn trailing line — total data loss across replay/log/grep/export
*Area:* gap sweep (§15) · *Category:* bug · *Location:* `internal/transcript/transcript.go:510`

read() hard-fails the whole Recording the moment any line fails parseFrame, with no salvage of frames before it (transcript.go:504-519, error at 510-513). bufio.Scanner yields the final unterminated token, so a half-written last line always reaches parseFrame and kills the read. Every user-facing transcript consumer funnels through loadTranscript -> transcript.Read (internal/cli/session.go:1771, called from runSessionLog:136, runSessionReplay:157, runSessionGrep:177, runSessionExport:205, browse raw-replay:530) and the TUI viewer (internal/sessionview/sessionview.go:1029-1034, which blanks all lines and shows only errText). One bad byte at the tail of a multi-hour recording makes the product's core artifact completely unrecoverable through every interface. Torn tails are realistic: ENOSPC short write leaves a partial line (transcript.go:180-184), there is no fsync ever (Close at 142-156 just closes the fd, so power loss can tear a cleanly-closed file), and the audit already confirmed supervisor goroutines panic without recover(). This is the same hard-fail-on-one-bad-record pattern as the confirmed ListRecords finding, applied to the .cast reader.

> **Evidence:** Empirically verified: generated a valid 200-frame cast via the package's own Writer (transcript.OpenWriter/WriteOutput/Close) in a temp state dir, confirmed `session log --tail 2` and `session grep` worked (exit 0), then removed 5 trailing bytes (mid-JSON-array tail: `[39.8,"o","line 199 ...output\r` with no closing `"]`). After that, ALL of `go run ./cmd/ssherpa session log <id>`, `session replay

**Fix:** Make read() salvage-tolerant like asciinema's player: on parseFrame failure of the final line (no complete newline-terminated line after it), return the Recording with all frames parsed so far plus a torn-tail indication (e.g. add Recording.TornTail bool or return a typed ErrTornTail wrapping the partial Recording); consumers (loadTranscript, sessionview reload, followTranscript) print a one-line warning ('transcript tail is incomplete; showing N frames') instead of dying. For mid-file garbage, skip the line and count warnings rather than aborting. Add regression tests: cast cut mid-last-line, garbage mid-file, header-only file.

#### HIGH-02 · authorized_keys options field is never validated for control characters — newline injection corrupts the file (the comment-fix bypass)
*Area:* `authkeys` · *Category:* security · *Location:* `internal/authkeys/authkeys.go:202`

The control-char hardening added in 5448f8d / IMPROVEMENTS #37 only covers the COMMENT field. ValidateStructural (authkeys.go:202-219) calls validateComment(key.Comment) but NEVER validates key.Options. scanFields (authkeys.go:288-328) is quote/escape aware, so any control character (including \n and \r) placed INSIDE a quoted option value (e.g. command="...") is swallowed verbatim into the options token instead of splitting fields. Render() (authkeys.go:38-52) writes Options out byte-for-byte, so an embedded newline becomes a second physical line in authorized_keys. Reachable via `authkeys add --key` (argv can carry newlines, e.g. shell $'\n') and the interactive paste prompt (cli/authkeys.go:325-342 passes the pasted line straight to --key). I confirmed it end-to-end with the built binary: with ssh-keygen ABSENT the default validator silently degrades to structural-only (Validate returns StructuralOnly when exec.LookPath fails, authkeys.go:174-177) and the malicious line is written. The file is then ALSO corrupted: on re-parse the original key is split across the injected newline and is no longer recognized (`list` reports 0 keys + warnings), i.e. silent data loss plus a smuggled forced-command / extra options line. NOTE: when ssh-keygen IS present, `ssh-keygen -lf -` rejects the multi-line render, so the practical blast radius is ssh-keygen-absent hosts and any --ssh-keygen shim that passes; but the structural validator — the documented fallback — is the broken layer, and it is the same injection class the comment fix tried to close.

> **Evidence:** validateComment only touches Comment:
  if err := validateComment(key.Comment); err != nil { return err }  // authkeys.go:215
(no equivalent line for key.Options anywhere in ValidateStructural)

Probe via ParsePublicKeyLine + PlanAdd(SkipSSHKeygen):
  Options="command=\"echo a\nrm -rf /\""  Comment="alice@example c"
  PLAN NewData = "# Created by ssherpa authkeys\ncommand=\"echo a\nrm -rf /\" ssh-ed25519 AAAA...…

**Fix:** Add an options content check to ValidateStructural that rejects ASCII control characters (and certainly \n, \r, \x00, \x7f) in key.Options, mirroring validateComment. Apply the same rule inside ParsePublicKeyLine so crafted file lines are also rejected. Do not rely on ssh-keygen as the only guard since it is an optional, silently-degraded dependency.

#### HIGH-03 · Typo'd or unknown subcommand silently becomes an ssh remote command on the picked host
*Area:* `cli` — dispatch, flags, exit codes · *Category:* ux · *Location:* `internal/cli/cli.go:222`

cli.Run's dispatcher falls through to runConnect for any unrecognized first arg (cli.go:222-223), and parseConnectFlags appends every bare non-flag positional to flags.SSHArgs (cli.go:610-611) even though the documented synopsis is 'ssherpa [connect-flags] [-- ssh-args...]' (docs/non-interactive.md:134 only allows ssh-args after --). sshcmd.BuildDirect places SSHArgs after the alias, where OpenSSH treats them as the remote command. So 'ssherpa lst' (typo of list) opens the interactive picker and, once the user picks a host, executes 'lst' as a remote command on that host; 'ssherpa exot', 'ssherpa delete web', etc. behave the same. Verified with a built binary: './ssherpa lst --select web --print --config ...' prints '[print] ssh -o SendEnv=SSHERPA_* -o ConnectTimeout=10 web lst'. No test covers the bare-positional path (tests only pass ssh-args after --, e.g. TestRunConnectPrint cli_test.go:187). Once users start depending on accidental 'ssherpa HOST CMD' passthrough this cannot be changed post-freeze, so the surface must be decided now.

> **Evidence:** cli.go:222-223 'default:\n\t\treturn runConnect(args, stdout, stderr, build)'; cli.go:610-611 'default:\n\t\t\tflags.SSHArgs = append(flags.SSHArgs, arg)'; experiment: '$ ssherpa lst --select web --print --config config' -> '[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 web lst' (exit 0)

**Fix:** In runConnect, reject bare positionals before '--' with 'ssherpa: unknown command %q (use -- to pass ssh args)' — or, if positional-host shorthand is desired, treat exactly one positional as --select and reject the rest. Decide and document before the freeze.

#### HIGH-04 · Launching a saved proxy silently overrides an explicit --bind with the catalog value
*Area:* `cli` — dispatch, flags, exit codes · *Category:* bug · *Location:* `internal/cli/route.go:1798`

runProxyWith does the saved-catalog lookup whenever --select matches a saved proxy and --port was not set (route.go:213-221), and applyProxyCatalogDefaults unconditionally overwrites flags.Bind with the stored bind (route.go:1799-1802). proxyFlags has PortSet but no BindSet, and the default bind is '127.0.0.1', so an explicit --bind is indistinguishable from the default and is silently clobbered. Verified: with saved proxy 'mysock' (127.0.0.1:1234), 'ssherpa proxy --select mysock --bind 0.0.0.0 --print' prints '-D 127.0.0.1:1234'. The reverse direction is security-relevant: a saved proxy stored with bind 0.0.0.0 launched with an explicit '--bind 127.0.0.1' will silently listen on all interfaces, exposing the SOCKS proxy to the network against the user's explicit request. forwardFlags avoids the equivalent for --through ('if flags.Through == "" { flags.Through = saved.Through }', route.go:1783-1785) but proxy bind has no such guard.

> **Evidence:** route.go:1798-1806 'func applyProxyCatalogDefaults(flags *proxyFlags, saved state.StoredProxy) {\n\tflags.Bind = saved.Bind\n\tif flags.Bind == "" {\n\t\tflags.Bind = defaultProxyBind\n\t}...'; experiment: '$ ssherpa proxy --select mysock --bind 0.0.0.0 --print' -> '[print] ssh ... -D 127.0.0.1:1234 -C -N -o ExitOnForwardFailure=yes web'

**Fix:** Add a BindSet bool to proxyFlags (set in --bind/--bind= cases) and only apply saved.Bind when !flags.BindSet, mirroring PortSet. Add a regression test.

#### HIGH-05 · Every supervised session orphans an authenticated ControlMaster for 10 minutes; ssherpa unlinks the socket instead of issuing ssh -O exit
*Area:* gap sweep (§15) · *Category:* reliability · *Location:* `internal/session/session.go:288`

RunSupervised injects '-o ControlMaster=auto -o ControlPath=<tmp> -o ControlPersist=10m' into every interactive session (internal/sshcmd/sshcmd.go:138-142 via internal/session/session.go:279-289, gated only by interactiveSessionKind at session.go:621-622, i.e. the default path). The only teardown is 'defer func() { _ = os.Remove(controlPath) }()' at session.go:288. No 'ssh -O exit' or '-O stop' exists anywhere (grep -rn '"-O"\|O exit\|-O stop' internal cmd returns nothing). Result: when the user exits, OpenSSH backgrounds a persist master that keeps the authenticated TCP connection open for 10 more minutes, and ssherpa immediately unlinks its control socket, making the master unaddressable ('ssh -O check/-O exit' impossible) and invisible (the cm/ dir looks empty). The user believes they disconnected; an authenticated channel to the server survives behind their back. Because the socket is always removed at exit, the 10-minute persist also provides zero reuse benefit (nothing can ever find the master again) -- it is pure cost. Note: the overlay SFTP mux reuse that motivates the ControlPath (session.go:1079, sshcmd.go:224-225) only runs while the session is live and does not need ControlPersist at all. This contradicts ssherpa's own stated invariant at session.go:380-385 ('both must die or we leak an orphaned connection').

> **Evidence:** Verified on macOS against local sshd: ran './ssherpa --select auditbox --config <testcfg> --ssh-binary <wrapper named ssh> --state-dir <tmp>', typed exit; ssherpa returned 0. Immediately after: pgrep -fl '[mux]' -> '33161 ssh: /var/folders/.../T/ssherpa-501/cm/572b867eb49f42ed.sock [mux]' still running; ls of .../ssherpa-501/cm/ -> empty (socket already unlinked); 'ssh -O check -o ControlPath=<pat

**Fix:** On final teardown (after the supervisor/reconnect loop returns, before removing the socket), if the socket still exists run the resolved ssh binary with '-O exit -o ControlPath=<controlPath> <alias>' (preserving -J/-F context from the original argv), wait briefly for the master to exit, then os.Remove as a fallback only. Alternatively drop ControlPersist=10m entirely (ControlPersist is useless here since the socket is unlinked at exit anyway): with ControlPersist unset/no, the master dies with the session and no orphan is possible.

#### HIGH-06 · Lingering orphan master holds the user's LocalForward/RemoteForward ports; relaunching the same host within 10 minutes silently loses its forwards
*Area:* gap sweep (§15) · *Category:* bug · *Location:* `internal/sshcmd/sshcmd.go:141`

Because the orphaned persist master (see ControlPersist=10m at sshcmd.go:141, teardown gap at session.go:288) keeps every LocalForward/RemoteForward from the user's own Host block bound for up to 10 minutes after exit, relaunching the same alias creates a second master (fresh per-record ControlPath, session.go:629-630) that fails to bind the same ports. For interactive sessions ssherpa does not set ExitOnForwardFailure, so the new session comes up with the forward silently dead -- ssh prints only a warning that scrolls past in the PTY. A user who exits a session and immediately reconnects to fix/use a tunnel gets a session whose tunnel no longer works, with no ssherpa-surfaced error and no way to kill the holder (socket already unlinked).

> **Evidence:** Test Host block contained 'LocalForward 18080 localhost:18080'. After exiting the first supervised session, lsof showed the orphan master (pid 33161) still in LISTEN on 127.0.0.1:18080 and [::1]:18080. Immediate relaunch of the same alias logged in ssh -v: 'bind [::1]:18080: Address already in use', 'bind [127.0.0.1]:18080: Address already in use', 'Could not request local forwarding.' -- while th

**Fix:** Fixing finding 1 (issue 'ssh -O exit' on teardown, or drop ControlPersist) removes the port holder. Additionally consider reusing a stable per-(stateDir,alias) control path instead of a per-record one so a quick relaunch would attach to the existing master rather than colliding with it, and surface ssh's 'Could not request local forwarding' stderr as a session event/warning in the overlay.

#### HIGH-07 · Triple Ctrl-] panic-tap tears down the entire nested session chain with no confirmation
*Area:* gap sweep (§15) · *Category:* ux · *Location:* `internal/session/session.go:834`

Inside the session overlay, each additional OverlayHotkey press within 400ms of the previous one increments a tap counter; at taps>=3 pull() fires immediately (session.go:830-839, constants escapeRopePanicWindow=400ms / escapeRopePanicTaps=3 at session.go:1348-1349), bypassing the deliberate X-then-X confirmation path (session.go:851-855). The press that opened the overlay counts as tap 1 (taps=1 at session.go:801, openedAt passed from copyInput at session.go:755), so exactly three physical presses in under ~0.8s collapse everything. pullRope by design SIGHUPs the whole process group so the remote sshd HUPs every nested layer (design comment session.go:346-351, signalSessionGroup at session.go:386), escalating to SIGKILL after 750ms (escapeRopeKillGrace, session.go:1338). Because Ctrl-] is vim's jump-to-tag (and telnet's escape, emacs abort-recursive-edit), a vim user hammering Ctrl-] to chase tags will destroy their whole SSH stack — remote shells, jobs, and REPLs get HUP'd with unsaved state. There is no tap-count/window configuration and no way to disable the panic path.

> **Evidence:** Code: session.go:830-839 (if key == OverlayHotkey { if time.Since(lastTap) <= escapeRopePanicWindow { taps++ ... if taps >= escapeRopePanicTaps { pull(); ...return } } }); constants session.go:1348-1349; deliberate confirm path session.go:851-855 ('X' sets confirming=true, second 'X' at 819 pulls). Experiment (temporary test, since removed): fed a real supervised PTY session three 0x1D bytes 150ms

**Fix:** Before freeze: (a) raise escapeRopePanicTaps to 5 and/or require the final tap to be followed by ~300ms of silence ('settle to fire'), (b) after tap 2 flash a one-line overlay warning ('1 more press disconnects ALL layers — any other key cancels') so the blind-panic use case survives but accidental hammering gets a visible off-ramp, and (c) expose --panic-taps N / --no-panic-tap (N=0) so users who never want unconfirmed teardown can opt out. The blind-wedged-terminal rationale in the comment at session.go:1344-1347 is preserved by (a)+(c).

#### HIGH-08 · Overlay hotkey 0x1D is hardcoded, unremappable, and not disableable — no --overlay-key / --no-overlay, only the all-or-nothing --direct
*Area:* gap sweep (§15) · *Category:* ux · *Location:* `internal/session/session.go:33`

OverlayHotkey is a package const (byte 0x1d, session.go:33) consumed unconditionally in copyInput (session.go:754). grep -rn 'overlay-key|no-overlay' across the repo returns nothing; the connect flag parser (internal/cli/cli.go) offers --composer-key/--no-composer (cli.go:539-558, usage lines 65-67) but no overlay equivalent, and parseControlKey even hard-rejects remapping the composer onto 0x1D as 'reserved' (cli.go:701). The only way to stop ssherpa from eating Ctrl-] is --direct / --no-supervise (cli.go:59, 496), which forfeits the entire supervised feature set — session map, recording, escape rope, transfers, and the latency watchdog (validateLatencyFlags rejects --direct at cli.go:622-625; composer flags rejected with --direct at cli.go:638-641). For vim (jump-to-tag), telnet (escape char), and emacs (abort-recursive-edit) users, every supervised session permanently breaks a standard key with no per-key escape hatch — a sharp asymmetry given the composer key IS remappable. Adding flags after the feature freeze would violate it, so this must land now or wait out the maintenance phase.

> **Evidence:** session.go:33 (OverlayHotkey = byte(0x1d)); copyInput dispatch session.go:751-762 — case buf[0] == OverlayHotkey at 754 opens the overlay and only the default branch at 761 writes to the PTY. `grep -rn 'overlay-key\|no-overlay' /Users/xbenc/0xbenc/ssherpa --include='*.go'` → no matches (exit 1). cli.go:701: `if key == ... || key == session.OverlayHotkey { fmt.Fprintf(stderr, "%s cannot use reserve

**Fix:** Mirror the composer flags: add --overlay-key KEY (reusing parseControlKey, with mutual-conflict checks against the composer key in both directions) and --no-overlay (keep supervision/recording/state but forward all bytes; escape rope then reachable only via external `ssherpa session` commands or a SIGUSR-style signal). Plumb through session.Options the same way ComposerOptions already is (Options.Composer at session.go:84, wired from route.go:111-112). parseControlKey and the validation scaffolding already exist, so this is low-effort pre-freeze.

#### HIGH-09 · Silent link death never triggers reconnect: no ServerAliveInterval/CountMax injection and supervision is purely exit-driven
*Area:* gap sweep (§15) · *Category:* reliability · *Location:* `internal/session/session.go:1981`

VERIFIED (code + live experiment). ssherpa never sets ServerAliveInterval, ServerAliveCountMax, or TCPKeepAlive anywhere: `grep -rn 'ServerAlive\|TCPKeepAlive'` across the repo (Go + md) returns zero hits. The only liveness option injected is ConnectTimeout=10 (internal/sshcmd/sshcmd.go:147-157 WithConnectTimeout, applied at internal/cli/route.go:1483; DefaultConnectTimeoutSeconds=10 at sshcmd.go:27), which only bounds connection ESTABLISHMENT. The reconnect loop (session.go:447-524) engages exclusively when attemptOnce's `waitErr := proc.Wait()` (session.go:1981) returns a retryable exit (shouldRetry, session.go:2214-2236). On a silent link drop (NAT/state-table timeout, laptop sleep/wake, Wi-Fi roam) with a stock user config, ssh never exits: proc.Wait() blocks forever and the documented reconnect loop (docs/non-interactive.md:412-415: '--no-reconnect | Disable reconnect loop for supervised tunnel sessions', plus retry knobs; ReconnectOptions doc session.go:108-120) never runs. Best case the kernel TCP keepalive (OpenSSH default TCPKeepAlive=yes, OS default idle ~7200s) errors the socket after ~2h+, so a `forward --background` tunnel is silently dead for hours before the first retry. Live demo with an instrumented fake ssh (`--ssh-binary` pointing at a script): (a) exit-255 mode produced 'session attempt 1 ended (exit status 255); retrying in 50ms' through 3 attempts — reconnect works for exits; (b) hang mode (emit one line then sleep forever, the analog of ssh blocked on a dead TCP) produced exactly 1 logged attempt, 0 'retrying' lines, and a still-alive supervisor after 8s — supervision is inert for stalls. README.md contains no reconnect claims (grep clean); the promise lives in docs/non-interactive.md:412-415 and the session.go:108-120 doc comment, and neither is met for the most common real-world failure mode.

> **Evidence:** grep -rn 'ServerAlive\|TCPKeepAlive' /Users/xbenc/0xbenc/ssherpa --include='*.go' --include='*.md' => NO HITS. session.go:1981 `waitErr := proc.Wait()`; retry gate session.go:481-486 + shouldRetry session.go:2214-2236 (only acts on a returned waitErr). Contrast injection precedent: sshcmd.go:147-157 WithConnectTimeout applied at route.go:1483. Experiment: fake ssh in hang mode under `ssherpa forwa

**Fix:** Mirror the ConnectTimeout precedent: add sshcmd.WithServerAlive(cmd, interval, countMax) that injects `-o ServerAliveInterval=15 -o ServerAliveCountMax=3` (skip when hasSSHOption already finds ServerAliveInterval= in argv, same guard style as sshcmd.go:132/148 so explicit user args win), and apply it at route.go:1483 next to WithConnectTimeout — at minimum for KindTunnel/KindProxy sessions where reconnect is the contract. Note the trade-off: a command-line -o overrides ssh_config values, exactly as the existing ConnectTimeout injection already does. With keepalives in place, a dead link makes ssh exit 255 within ~45s and the existing retry loop engages.

#### HIGH-10 · writeFrame leaves a torn partial line in the .cast on write error and never fsyncs
*Area:* gap sweep (§15) · *Category:* reliability · *Location:* `internal/transcript/transcript.go:180`

writeFrame performs a single unsynced w.file.Write(line) (transcript.go:180); on error — e.g. ENOSPC partway through a frame that can exceed 32KiB after JSON escaping (PTY chunks are read in 32KiB buffers, internal/session/session.go:1999, fed straight to WriteOutput at session.go:2016) — the partially-written line stays in the file, w.truncated is latched, and recording silently stops (181-184). w.bytes is not advanced by the partial count, so the persisted spec.Bytes also understates the real file size. Close() (142-156) only calls file.Close with no Sync and no trim-back to the last good offset, so even a 'clean' shutdown leaves the tear, and power loss can tear a file that closed cleanly. Because read() (finding 1) has zero tolerance, this writer behavior converts a transient disk blip into total loss of the entire recording.

> **Evidence:** transcript.go:180-184: `n, err := w.file.Write(line); if err != nil { w.truncated = true; return }` — no Truncate(w.bytes) to remove the partial line, no Sync. Close at 142-156: `err := w.file.Close()` only. Frame size: session.go:1999 `buf := make([]byte, 32*1024)` then session.go:2016 `ac.transcript.WriteOutput(time.Now().UTC(), clean)` — a single frame line can span many filesystem pages, makin

**Fix:** On write error, truncate the file back to the last known-good offset: `_ = w.file.Truncate(w.bytes)` before latching w.truncated, and append a short 'm' marker frame if space allows. Add an fsync policy (Sync on Close, plus periodic Sync every N seconds or M bytes) so cleanly-closed transcripts survive power loss. Optionally trim-on-open: when OpenWriter ever gains an append mode, or when Read encounters a torn tail, offer repair by truncating to the last complete newline.

#### HIGH-11 · Bundle export/import silently propagate a torn transcript — SHA-256 'verifies' a corrupt, unreadable artifact
*Area:* gap sweep (§15) · *Category:* bug · *Location:* `internal/transcript/transcript.go:261`

ExportBundle does a raw os.ReadFile of the .cast (transcript.go:261) and computes TranscriptSHA256 over the corrupt bytes (297) — it never parses the transcript, so exporting a torn recording succeeds and ships the poison. ImportBundle verifies the hash (378-380) and writes the bytes unparsed to the destination state dir (397), so the corruption imports 'clean' and every viewer on the receiving machine fails identically. The integrity manifest thus certifies an unreadable artifact: the portable-bundle feature's whole promise (share a recording with another machine) breaks end to end while reporting success at both ends. Note: contrary to a naive reading, `session bundle export` does NOT hard-fail on a torn cast — it succeeds, which is worse because the failure surfaces only on the recipient's machine with no hint of when the tear happened.

> **Evidence:** Empirically verified with the torn fixture: `session bundle export <id> --output /tmp/audit-bundle.zip` exited 0 and printed the transcript sha256 of the corrupt bytes; `session bundle import` into a fresh state dir exited 0; `session log <imported-id>` on the fresh state dir then failed with `parse transcript line 201: unexpected end of JSON input` (exit 1). Code: transcript.go:261 `transcriptByt

**Fix:** Largely remediated by the read() salvage fix (imported torn casts become viewable with a warning). Additionally, ExportBundle should attempt a parse before packaging and either warn ('transcript tail incomplete; exporting N frames') or offer to trim the torn tail so the recipient gets a self-consistent artifact; ImportBundle can surface the same warning in its result.

#### HIGH-12 · Nested-session metadata via SendEnv SSHERPA_* is silently dropped by every stock sshd; remote-side lineage degrades with zero detection or warning
*Area:* gap sweep (§15) · *Category:* compat · *Location:* `internal/sshcmd/sshcmd.go:125`

ssherpa injects `-o SendEnv=SSHERPA_*` (internal/sshcmd/sshcmd.go:22,118-128) and puts SSHERPA_SESSION_ID/PARENT_SESSION_ID/DEPTH/ROUTE/ORIGIN_HOST into the child ssh env (internal/state/state.go:593-602, applied at internal/session/session.go:639-641 and :1925). The next hop derives ParentID/Depth/Route from those vars (state.go:625-648, consumed in buildRecord at session.go:578-607). OpenSSH servers only accept client env matching sshd_config AcceptEnv, and no stock config accepts SSHERPA_*. VERIFIED EXPERIMENTALLY against a real sshd (OpenSSH 10.2p1) with default config: `ssh -o SendEnv=SSHERPA_*` delivered nothing (NO_SSHERPA_ENV_ARRIVED); the identical run against an sshd with `AcceptEnv SSHERPA_*` delivered all five vars. In a real nested supervised run (ssherpa->hopA, then ssherpa->hopB inside it) through the default sshd: (a) `env | grep SSHERPA` in the inner shell was empty; (b) the remote ssherpa's own record JSON was {parent_id: null, depth: 0, route: ['hopB'], origin_host: <remote hostname>} — it believes it is a root session; (c) the remote `session map` showed 'local -> hopB target, depth 0' with no inherited lineage, while the AcceptEnv contrast run showed the correct 'Bens-Mac-mini local => okA ssherpa -> okB target, depth 1' (inherited pseudo-node rendering requires record.ParentID != "", internal/sessionview/sessionview.go:1682). Everything derived from the remote record degrades too: `incoming mark` markers lose ssherpa_session_id/depth/route/origin_host (internal/incoming/incoming.go:218-222; verified: marker through default sshd had all five fields null, through AcceptEnv sshd all populated), and transcripts/bundles exported on the remote host embed the wrong route/depth/parent (internal/transcript/transcript.go:296 manifest Route and session.json = full record). The telemetry fallback (session.go:2123-2143) repairs the LOCAL mirror only — verified: local state dir held the mirror with parent_id/depth 1/route ['hopA','hopB'] correct — which masks the breakage from the local operator while every remote-side surface stays wrong. There is no runtime detection anywhere: the parent silently back-fills the mirror at session.go:2127-2131 even though that branch firing is positive proof the env was stripped. Good news: the escape rope is unaffected — it SIGHUPs the local ssh process group (session.go:346-396), so 'Disconnect ALL nested sessions' (session.go:1102) stays true without env.

> **Evidence:** Experiment on OpenSSH 10.2p1: default sshd -> remote shell env empty, remote record {parent_id:null, depth:0, route:['hopB']}; AcceptEnv sshd -> remote record {parent_id:'2026...9a27edb6', depth:1, route:['okA','okB']}. Injection: sshcmd.go:22 `const SessionEnvPattern = "SSHERPA_*"`, :125 `argv = append(argv, "-o", "SendEnv="+SessionEnvPattern)`. Derivation: state.go:626 `parentID = strings.TrimSp

**Fix:** 1) Detect at the parent: the remoteMirrorRecord backfill branch (session.go:2127) fires exactly when a downstream ssherpa reported no parent despite this supervisor having sent SSHERPA_* — append a state.SessionEvent ('nested metadata blocked: remote sshd lacks AcceptEnv SSHERPA_*') and surface a one-time overlay/map notice. 2) Longer term, stop depending on AcceptEnv: bootstrap the metadata in-band (the telemetry channel already proves an in-band path works upstream; a downstream equivalent could be written to the remote shell on connect). 3) Document the requirement prominently (see docs finding).

#### HIGH-13 · `forward stop` during a reconnect backoff kills the daemon without finalizing — permanent unrecoverable orphan, reported as exit 0
*Area:* `incoming` — daemon, presets, incoming sessions · *Category:* reliability · *Location:* `internal/session/session.go:1979`

forwardSignals (which installs the SIGHUP/SIGTERM->pullRope handler) is installed inside attemptOnce and torn down via stopSignals() when attemptOnce returns after proc.Wait(). Between attempts, while the supervisor sleeps in the reconnect backoff `select` (session.go:516-523, up to 60s with `forward`'s defaults since forwardReconnectOptions sets Enabled=!NoReconnect), NO signal handler is installed in detached mode (the overlay/pullRope path is interactive-only). `forward stop` sends syscall.Kill(LocalPID, SIGHUP) (forward_management.go:265); with no Notify subscription Go's default SIGHUP disposition terminates the daemon immediately, so RunSupervised never reaches the EndedAt write (session.go:534-538). The record is stuck with ended_at=null forever: `forward stop` can never finalize it, and `session prune` skips it because PruneRecords requires `record.EndedAt != nil` (state.go:373). forward stop also returns 0 on the 'did not finalize' path (forward_management.go:284-288), so scripts cannot detect the failure.

> **Evidence:** Clean repro with the built binary (ssh stub exits 255, 30s backoff):
  session id: 20260612T001324...-dbb51a0b
events before stop: ['reconnect_scheduled']
$ ssherpa forward stop <id> --state-dir state3
ssherpa: forward <id> signaled (pid 12802) but did not finalize within 2s
stop-exit=0
ended_at= None exit_code= None
$ ssherpa forward status <id>
status      orphan (detached, pid 12802)

Code: forwardSignals is…

**Fix:** Install a persistent SIGHUP/SIGTERM/SIGQUIT handler at the RunSupervised level (covering the inter-attempt backoff window) that calls pullRope, independent of per-attempt forwardSignals; and have forward stop / proxy stop return non-zero on the 'signaled but did not finalize' path so callers can detect it.

#### HIGH-14 · hostlist.Build is O(n²) in Host blocks — 1.1s at 5k hosts, 4.4s at 10k, paid on every TUI home-loop pass
*Area:* performance · *Category:* perf · *Location:* `internal/hostlist/hostlist.go:128`

Build iterates every alias (one per Host pattern) and for each calls applyParsedEffectiveValues, which rescans ALL graph.Blocks and runs filepath.Match per pattern (hostlist.go:169-194) — n aliases × n blocks glob matches. Measured: `list --json` wall time 0.045s@1000 hosts, 0.41s@3000, 1.14s@5000, 4.53s@10000 (perfect t≈4.56e-8·n² fit); instrumented split shows parse is linear (14.3ms@10k) while Build alone is 4.40s@10k. The cost hits `list`, `show`, `check`, every interactive launch, and is re-paid on EVERY return to the home picker because runConnect's loop calls loadInventory each iteration (cli.go:273-288). At fleet-scale configs the TUI freezes for seconds after every completed action.

> **Evidence:** hostlist.go:62 `applyParsedEffectiveValues(&alias, graph)` inside the per-alias loop; hostlist.go:128 `for _, block := range graph.Blocks {` full rescan; hostlist.go:192 `ok, err := filepath.Match(pattern, name)`. Measured: parse-only 14.3ms vs Build 4.402s for 10000 blocks (`blocks=10000 aliases=10000 parse=14.335875ms build=4.401980708s`).

**Fix:** Index blocks once before the alias loop: a map[string][]*HostBlock for exact (non-glob, non-negated) patterns and a small slice of wildcard/negated blocks. Per alias, look up the exact map and scan only the wildcard slice — O(n·p) where p = wildcard block count (typically <10). Drops 10k-host Build from 4.4s to ~15ms.

#### HIGH-15 · ssh argument injection: config alias beginning with dash runs arbitrary code via ProxyCommand
*Area:* security — injection · *Category:* security · *Location:* `internal/sshcmd/sshcmd.go:215`

Build functions append the alias as a bare ssh positional with no leading-dash guard. ssh has no end-of-options separator, so -oProxyCommand=value parses as an option. BuildProbe appends a trailing true and BuildDirect appends a remote command, giving ssh a destination so the injected ProxyCommand runs.

> **Evidence:** sshcmd.go:215 appends alias,true; sshcmd.go:113 appends alias; hostlist.go:54 Name=pattern. Verified RCE with -oProxyCommand=touch.

**Fix:** Reject destinations beginning with dash.

#### HIGH-16 · Cask-installed binary is unsigned/un-notarized with no quarantine handling — Gatekeeper may block it on modern macOS
*Area:* security — supply chain · *Category:* compat · *Location:* `.goreleaser.yaml:60`

homebrew_casks (.goreleaser.yaml:60-81) ships a raw Go binary with no Apple codesigning/notarization anywhere in the pipeline (no notarize config, no signing step in release.yml) and no cask `hooks:` to clear the quarantine xattr. Homebrew quarantines cask downloads; on recent macOS, Gatekeeper can kill an unnotarized quarantined binary on first launch. GoReleaser's own cask docs flag exactly this and recommend notarization or a documented quarantine-removal hook. Since the cask is the README's primary install path, this is a first-run adoption risk that should be tested on a clean macOS 15+ machine before the freeze.

> **Evidence:** .goreleaser.yaml:60-81 homebrew_casks stanza contains only name/ids/binaries/manpages/completions/homepage/description/repository/commit_author — no `hooks:` and no signing/notarization configuration anywhere in the repo.

**Fix:** Verify first-run behavior of the cask on a clean macOS 15/26 install; either notarize the darwin binaries or add the goreleaser-documented cask post-install quarantine hook, and document the workaround in README.

#### HIGH-17 · OSC7 cwd escape injection into overlay and session map
*Area:* security — terminal boundary · *Category:* security · *Location:* `internal/session/osc_tracker.go:335`

parseOSC7 returns url.Parse percent-decoded path so remote injects ESC into RemoteCWD rendered in overlay and CLI map

> **Evidence:** url.Parse file scheme Path decoded to contain ESC bytes

**Fix:** strip control bytes

#### HIGH-18 · renderValue does not quote single quotes; ssherpa-written values containing ' make every ssh invocation fail (OpenSSH >= 8.7 fatals with 'invalid quotes')
*Area:* `sshconfig` + `hostlist` — config parsing & mutation · *Category:* bug · *Location:* `internal/sshconfig/mutate.go:598`

renderValue only wraps a value in double quotes when it contains space/tab/#/"/backslash — a bare single quote passes through unquoted. Modern OpenSSH (8.7+ argv_split tokenizer; verified on OpenSSH 10.2) treats an unmatched ' as a quote-start and terminates with 'invalid quotes ... bad configuration options' — for EVERY host, not just the affected stanza. So one add/edit with an apostrophe in User, HostName, ProxyJump, IdentityFile (e.g. macOS home dir /Users/o'brien/.ssh/key), or the alias name itself (validateAliasName also permits ') bricks the user's entire ssh config until manually repaired. ssherpa's own safety net validateRenderedDocument (mutate.go:631) cannot catch it because splitFields (sshconfig.go:268-319) treats ' as an ordinary character. Double-quoting fixes it: verified `User "o'brien"` parses correctly in ssh.

> **Evidence:** mutate.go:597-599: `if !strings.ContainsAny(value, " \t#\"\\") {\n\t\treturn value\n\t}`. Repro: PlanAddOrUpdate{User: "o'brien", IdentityFile: "/Users/o'brien/.ssh/id"} rendered `  User o'brien` / `  IdentityFile /Users/o'brien/.ssh/id` with no error. Feeding that exact file to ssh: `ssh -G -F c10 p` -> `c10 line 5: invalid quotes / c10 line 6: invalid quotes / c10: terminating, 2 bad configuration options`; same…

**Fix:** Add ' to renderValue's quote-trigger set (strings.ContainsAny(value, " \t#\"\\'")) — double quotes protect apostrophes in modern ssh. Also teach splitFields to recognize single-quoted tokens (read parity) so validateRenderedDocument would catch any future variant, and add a regression test that round-trips every written value through `ssh -G -F` in CI or at least through a tokenizer that models argv_split.

#### HIGH-19 · Forced lowercase on write vs case-sensitive lookup: editing a mixed-case alias appends a duplicate stanza instead of updating, and the edit silently has no effect
*Area:* `sshconfig` + `hostlist` — config parsing & mutation · *Category:* bug · *Location:* `internal/sshconfig/mutate.go:264`

normalizeAliasSpec lowercases the alias at the single chokepoint before rendering, but every lookup (findExactAliasBlocks mutate.go:468, ExistingAliasSpec, FindAliasOccurrences mutate.go:196, chooseAddTarget cli/mutate.go:1445) matches the original case exactly. The TUI edit flow (cli/mutate.go:522 `spec.Alias = alias`) passes the inventory's original-case name, e.g. "Prod"; PlanAddOrUpdate folds it to "prod", finds no match, and APPENDS a new `Host prod` stanza while `Host Prod` keeps its old values. OpenSSH Host matching is case-sensitive (verified: with `Host PROD`, `ssh -G PROD` matched, `ssh -G prod` did not — ssh does NOT lowercase the CLI host), so connecting to the listed alias "Prod" still uses the OLD stanza: the user's edit silently does nothing, and the config accumulates near-duplicate stanzas. The TUI applies with `Yes: true` (cli/mutate.go:533), so there is no confirmation step that would reveal the 'added' action.

> **Evidence:** Repro test against the package: config `Host Prod\n  HostName old.example.com\n  User old\n`; ExistingAliasSpec(path, "Prod") then PlanAddOrUpdate with HostName changed produced `action=added alias=prod` and NewData containing BOTH `Host Prod\n  HostName old.example.com` and `Host prod\n  HostName new.example.com`. mutate.go:264: `spec.Alias = strings.ToLower(strings.TrimSpace(spec.Alias))`. ssh experiment: `printf…

**Fix:** When the spec comes from an existing stanza, look up with the original name and update that stanza in place (preserve its case, or rename its Host token explicitly). If the lowercase policy must hold, PlanAddOrUpdate should treat a case-insensitive match as the update target and rewrite the Host line, never append a sibling. Add tests for mixed-case edit/delete round-trips.

#### HIGH-20 · Deleting an alias removes unrelated trailing content: comments documenting the NEXT stanza, end-of-file comments, and Include directives (wiping all hosts they define)
*Area:* `sshconfig` + `hostlist` — config parsing & mutation · *Category:* bug · *Location:* `internal/sshconfig/mutate.go:510`

Document.reparse sets each DocumentBlock's End to the index of the next Host/Match line, or len(d.Lines) for the last block. Everything between a stanza's last option and the next Host line — blank lines, comments that visually document the FOLLOWING stanza, Include directives, and any other lines — is inside the deleted range. Verified three silent data-loss shapes: (a) a `# keep's documentation comment` line directly above `Host keep` is deleted when deleting the preceding `prod` stanza, (b) trailing comments after the last stanza are deleted, (c) an `Include work.conf` after the last stanza is deleted — and since Host lines inside an included file define hosts that apply beyond the enclosing block context (ssherpa's own loader parses them into inventory, sshconfig.go:174), this removes entire host sets from ssh's view. The confirmation prompt says only 'Delete SSH alias prod from 1 file(s)' (cli/mutate.go:311); the diff is shown only in --dry-run. A timestamped backup exists but nothing tells the user extra content was removed.

> **Evidence:** mutate.go:507-511: `block := DocumentBlock{ Start: i, End: len(d.Lines), ... }` (End only tightened at the next host/match keyword, mutate.go:504-505/517-518). Repro 1: input `Host prod\n  HostName x\n\n# keep's documentation comment\nHost keep\n  HostName y\n` -> PlanDeleteAlias("prod") NewData = `Host keep\n  HostName y` (comment gone). Repro 2: `...Host prod\n  HostName x\n\n# TODO migrate bastions next…

**Fix:** Tighten the deletion range: end the removed region at the stanza's last recognized option line (skipping back over trailing blank/comment/Include lines), or at minimum stop before comment lines that immediately precede the next Host line and before Include directives. Surface a per-plan warning (MutationPlan.Warnings exists but is never populated) listing non-option lines that would be removed, and show it in the confirm prompt.

#### HIGH-21 · Editing an alias out of a multi-pattern stanza silently drops all unmanaged options for that alias (ForwardAgent, LocalForward, ServerAliveInterval, ...)
*Area:* `sshconfig` + `hostlist` — config parsing & mutation · *Category:* bug · *Location:* `internal/sshconfig/mutate.go:360`

When the edited alias lives in a multi-pattern stanza (e.g. `Host prod web`), PlanAddOrUpdate splits it: the alias is removed from the shared Host line and a fresh stanza is appended via renderStanzaLines(spec). But spec only carries the six managed keys (specFromBlock mutate.go:425-461 reads hostname/user/port/identityfile/identitiesonly/proxyjump), so every unmanaged option in the shared stanza is silently absent from the new stanza. A user who opens the edit form and changes only the HostName loses ForwardAgent, ServerAliveInterval, LocalForward, StrictHostKeyChecking, etc. for that host — a real behavior change on the next connection with no warning. (Single-pattern edits are safe: replaceBlockFields mutate.go:333 preserves unmanaged lines.) MutationPlan.Warnings (mutate.go:40) is never populated anywhere, so there is no channel to tell the user.

> **Evidence:** Repro: input `Host prod web\n  HostName shared.example.com\n  ForwardAgent yes\n  ServerAliveInterval 30\n`; ExistingAliasSpec + PlanAddOrUpdate changing only HostName produced: `Host web\n  HostName shared.example.com\n  ForwardAgent yes\n  ServerAliveInterval 30\n\nHost prod\n  HostName prod.example.com` — prod lost both unmanaged options. splitAliasBlock (mutate.go:360-380) inserts only `renderStanzaLines(spec)`.

**Fix:** On split, copy the source block's unmanaged option lines into the new stanza (then apply managed replacements), or refuse the split with a message like the wildcard refusal, or at minimum populate MutationPlan.Warnings with the options that will not carry over and display them before applying.

#### HIGH-22 · skipANSI terminates only on ASCII letters, so CSI sequences with non-letter final bytes ('@', '~', '`', '{', '|', '}') over-consume following text; transcript clean export/grep silently lose session text
*Area:* `termstyle` — theming & width math · *Category:* bug · *Location:* `internal/termstyle/termstyle.go:53`

skipANSI breaks only when it consumes a byte in [A-Za-z], but real CSI final bytes span 0x40-0x7E. Sequences such as ICH '\x1b[1@' (xterm terminfo ich=\E[%p1%d@, emitted by readline/zsh when inserting characters mid-line) and HPA '\x1b[5`' do not end on a letter, so skipANSI keeps consuming every subsequent byte — digits, punctuation, whitespace, and all multi-byte UTF-8 characters (none are ASCII letters) — until it eats the first ASCII letter it finds. Both Strip and VisibleWidth share this loop. transcript.Clean (transcript.go:592) runs termstyle.Strip on raw PTY output for the plain-text transcript export and for transcript Grep (transcript.go:642), so recorded sessions whose output contains these sequences silently lose arbitrary runs of text in exports and search results. Repro against the real code: Strip("abc\x1b[1@123 def") = "abcef" (lost '123 d'); Strip("a\x1b[5`99,99 b") = "a" (lost everything after the sequence); Strip("\x1b[1@日本語x") = "" (entire CJK string swallowed). The raw recording is intact, but every 'clean' text export/grep over it is corrupted, which is the flagship transcript feature.

> **Evidence:** termstyle.go:53-63:
func skipANSI(value string, start int) int {
	i := start + 2
	for i < len(value) {
		b := value[i]
		i++
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
			break
		}
	}
	return i
}

Repro output (go run inside module):
ICH    Strip("abc\x1b[1@123 def") = "abcef"
HPA`   Strip("a\x1b[5`99,99 b") = "a"
CJK    Strip("\x1b[1@日本語x") = ""
width  VisibleWidth("\x1b[1@123 def") =…

**Fix:** In skipANSI, after the '\x1b[' introducer, skip parameter bytes (0x30-0x3F) and intermediate bytes (0x20-0x2F), then consume exactly one final byte in the range 0x40-0x7E and stop. Add table-driven tests covering '@', '~', '`', intermediate-byte sequences like '\x1b[2 q', and UTF-8 text following each.

#### HIGH-23 · Marker ('m') frame data is emitted RAW in the cleaned text path — escape injection from imported transcripts
*Area:* `transcript` — recording & bundles · *Category:* security · *Location:* `internal/transcript/transcript.go:562`

Text() (used by `session log` default, `session export --format text`, and the TUI transcript viewer's reload) advertises 'cleaned' output: output frames go through Clean() which strips CSI/OSC. But marker frames bypass cleaning entirely and are printed verbatim. An imported bundle's transcript.cast is fully attacker-controlled, and asciicast 'm' frames are legitimate, so an attacker can place arbitrary terminal escape sequences in a marker's data field; the victim sees them executed by their terminal even when using the supposedly-safe non-raw view. I confirmed marker escapes leak through Text(TextOptions{}). The TUI viewer's truncateVisible only strips when VisibleWidth exceeds the box width, so short escape payloads pass through there too.

> **Evidence:** transcript.go:562 `fmt.Fprintf(&b, "\n[%s] %s\n", formatOffset(frame.Offset), frame.Data)` — frame.Data printed raw. Test output: clean path produced "hello\n\n[0:01] \x1b[2J\x1b]0;pwned\aevil marker\n" — the \x1b[2J (clear screen) and OSC title-set survived. sessionview.go:1051 truncateVisible returns the value unstripped when it fits.

**Fix:** Run marker data through Clean()/termstyle.Strip before printing in Text() (and only emit raw escapes under opts.Raw). Defense-in-depth: strip control chars from marker text on write in WriteMarker.

#### HIGH-24 · Bundle import/preview decompress zip entries with no size limit (zip-bomb → memory/disk exhaustion)
*Area:* `transcript` — recording & bundles · *Category:* security · *Location:* `internal/transcript/transcript.go:480`

ImportBundle and PreviewBundle read the entire bundle into memory with os.ReadFile, then readZipEntry decompresses each entry via io.ReadAll with no cap. A small, highly compressible bundle can decompress to arbitrary size; the decompressed transcript.cast is then written verbatim into the sessions dir (no size check), so a small file can exhaust RAM and/or fill disk. Bundle import is explicitly designed to accept untrusted input from other machines. I confirmed a 51 KB bundle whose transcript.cast decompresses to 50 MB passes Preview/Import with no guard; a denser bomb scales to GBs.

> **Evidence:** transcript.go:348 `bundleBytes, err := os.ReadFile(bundlePath)`; transcript.go:480 `data, err := io.ReadAll(rc)` (no limit); transcript.go:397 `os.WriteFile(localTranscriptPath, transcriptBytes, 0o600)`. Repro: "compressed bundle on disk: 51446 bytes; transcript.cast decompresses to 52428800 bytes via io.ReadAll with no cap" and PreviewBundle returned ok.

**Fix:** Cap each entry with io.LimitReader (e.g. transcript ≤ DefaultMaxBytes, manifest/session.json ≤ a few MiB) and reject if the limit is exceeded. Also bound the on-disk bundle size before os.ReadFile.

#### HIGH-25 · session show / browse render imported session metadata verbatim — terminal escape injection via TargetAlias, Route, Argv, DisconnectReason, Events
*Area:* `transcript` — recording & bundles · *Category:* security · *Location:* `internal/cli/session.go:1828`

ImportBundle stores nearly all session.json fields verbatim (record := source, transcript.go:400). printSessionRecord then prints TargetAlias, Route, SSHArgv, DisconnectReason, and each event Message to the terminal with no sanitization, and the TUI browser title uses sessionview.Target(record) which returns raw TargetAlias. So `ssherpa session show <imported-id>` (and the browser list) emit attacker-controlled escape sequences from another machine's bundle. Same untrusted-data class as the marker bug but through metadata instead of transcript frames.

> **Evidence:** session.go:1828 `fmt.Fprintf(stdout, "target:\t%s\n", defaultString(record.TargetAlias, "-"))`; session.go:1870 `fmt.Fprintf(stdout, "argv:\t%s\n", strings.Join(record.SSHArgv, " "))`; session.go:1882 prints `event.Message` raw. sessionview.go:1300 Target returns `record.TargetAlias` unmodified. transcript.go:400 `record := source` (no field validation/sanitization on import).

**Fix:** Sanitize untrusted string fields (strip control chars / run termstyle.Strip) when printing imported-record metadata, or sanitize on import before WriteRecord. JSON output is safe; only the human text paths need it.

#### HIGH-26 · In-band sentinel matching fires on the PTY echo of the command itself; probe is a no-op and payload streams before remote raw mode
*Area:* transfer — SFTP & in-band · *Category:* bug · *Location:* `internal/session/session.go:952`

The probe and READY watchers use plain substring matching over the PTY output, but the commands written to the remote shell literally contain the sentinels, and the remote tty/readline echoes the typed command back before executing it. ProbeCommand() (inband.go:111) contains both `printf 'SSHERPA_C_PROBE ok\n'` and `... fail\n'`; the matcher at session.go:952-955 checks Contains(text, "SSHERPA_C_PROBE ok") then "... fail". Which substring survives intact in the echo depends on where readline/zle inserts ' \r' wrap sequences at the terminal width, so the capability probe nondeterministically (a) false-passes on hosts lacking base64/head -c/stty, (b) false-fails ("remote shell lacks base64...") on fully capable hosts, or (c) accidentally waits for the real result. Worse, the READY matcher (session.go:970-971, Contains(text, inband.ReadyPrefix)) matches the echoed receiver command — empirically proven: in a PTY repro with PATH=/nonexistent (no stty/head/base64 at all), READY matched at byte offset 241, purely from echo. The sender then immediately streams up to ~6.6 MB of base64 (session.go:983-991) before the remote shell has executed `stty -echo -ixon -icanon`, racing shell startup latency against one network RTT. On low-RTT links with any shell hook latency the payload hits a canonical-mode tty (4096/1024-byte line limit, excess bytes discarded), `head -c` comes up short, and the transfer hangs to the 30s timeout; if the receiver failed to start at all, megabytes of base64 are typed into the user's live shell prompt and submitted as a command line. Failures are only ever caught later by sha mismatch or timeout.

> **Evidence:** inband.go:111: `... && stty -a >/dev/null 2>&1 && printf '" + ProbePrefix + " ok\\n' || printf '" + ProbePrefix + " fail\\n'"`; session.go:952: `case strings.Contains(text, inband.ProbePrefix+" ok"):`; session.go:971: `return strings.Contains(text, inband.ReadyPrefix), nil`. PTY repro (bash --norc -i, 80 cols, PATH=/nonexistent): `PROBE: matcher-ok-index=-1 real-result-fail=true` with echo `"printf 'SSHERP…

**Fix:** Make the sentinels impossible to echo: build them with shell concatenation so the typed command never contains the literal string (e.g. `printf '%s\n' 'SSHERPA_''C_READY'`), and anchor matches to line starts ("\nSSHERPA_C_READY\r"). Apply the same to PROBE ok/fail and DONE. Additionally, only begin streaming after a deliberate post-READY guard (e.g. require READY to appear after the echoed newline of the receiver command).

#### HIGH-27 · Supervised sessions print raw base64 telemetry blobs when run inside any plain SSH session
*Area:* UX / support load · *Category:* ux · *Location:* `internal/session/session.go:2065`

shouldEmitSessionTelemetry emits frames whenever SSH_CONNECTION or SSH_TTY is set, i.e. whenever ssherpa runs on a host the user reached via plain ssh (no ssherpa parent). The RS-framed fallback (osc_tracker.go:284) is plain printable base64, so the user's terminal displays ~900-char 'ssherpa-session:eyJ...' blobs at session start and end; only an upstream ssherpa supervisor strips them. Reproduced under PTY: the actual ssh error was sandwiched between two blobs. Very common topology (laptop -> plain ssh -> server running ssherpa) and the frame format is a compat surface about to be frozen.

> **Evidence:** session.go:2069 `return sessionEnvValue(env, "SSH_CONNECTION") != "" || sessionEnvValue(env, "SSH_TTY") != ""`; osc_tracker.go:284 `return []byte("\x1essherpa-session:" + payload + "\x1e"), true`; PTY capture: `ssherpa-session:eyJpZCI6IjIwMjYwNjExVDIzNTA0OC4...==ssh: Could not resolve hostname a: nodename nor servname provided, or not known\nssherpa-session:eyJpZCI6...`

**Fix:** Gate the visible RS-frame on a confirmed ssherpa parent (SSHERPA_SESSION_ID present in env / record.ParentID != "") and keep only the terminal-invisible OSC 777 variant for the SSH_CONNECTION heuristic; decide before freezing the frame protocol.


### 12.2 Medium findings (confirmed)

**`sshconfig` + `hostlist` — config parsing & mutation**

| Finding | Location | Summary |
|---|---|---|
| Editing an alias with multiple IdentityFile lines silently collapses them to the first one | `internal/sshconfig/mutate.go:446` | IdentityFile is a multi-valued, accumulating option in OpenSSH (the test fixture itself uses two: testdata/matrix/config, and hostlist collects them all into Alias.IdentityFiles). But AliasSpec holds… |
| Options inside Match blocks are silently dropped, so inventory values diverge from what ssh actually applies (even for 'Match all') | `internal/sshconfig/sshconfig.go:177` | The loader's default case appends options only when `section == sectionHost`; after a `match` keyword currentBlock is -1 and section is sectionMatch, so every option under any Match block — including… |
| Relative Include paths resolved against the including file's directory; OpenSSH resolves them against ~/.ssh (and also expands ${ENV} vars ssherpa treats literally) | `internal/sshconfig/sshconfig.go:356` | normalizePath joins a relative Include against baseDir = filepath.Dir(including file) (sshconfig.go:133, 355-358). OpenSSH resolves relative includes in user configs against ~/.ssh regardless of… |
| Tokenizer dialect divergences from OpenSSH corrupt values on the read side, and the edit round-trip writes the misparse back into the config | `internal/sshconfig/sshconfig.go:297` | splitFields diverges from OpenSSH's argv_split in several verified ways: (1) mid-line `#` in option VALUES — ssh keeps `HostName foo#bar` whole (verified `hostname foo#bar`), ssherpa truncates to… |
| hostlist.Build is O(n^2): 1s stall at 5k hosts, 4s at 10k, on every command that loads inventory | `internal/hostlist/hostlist.go:62` | Build calls applyParsedEffectiveValues once per pattern occurrence, and that function scans every block (and every block's options, with filepath.Match per pattern) — quadratic in host count.… |
| Test gap: mutation round-trips are untested for exactly the shapes that fail (trailing comments/Includes, mixed-case, multi-IdentityFile, multi-pattern unmanaged options, CRLF), and there is no fuzz or ssh-parity coverage | `internal/sshconfig/mutate_test.go:1` | mutate_test.go covers the happy paths (add/update/split/delete, wildcard refusal, duplicate removal) but none of the verified failure shapes: no test deletes a stanza followed by a comment/Include,… |

**`session` — PTY supervision**

| Finding | Location | Summary |
|---|---|---|
| SIGTERM/SIGHUP during reconnect backoff kills daemon without finalizing the session record | `internal/session/session.go:516` | forwardSignals (the only signal.Notify in the supervisor) is installed inside attemptOnce and removed by stopSignals() at line 1983 before each attempt returns. The reconnect loop then sleeps in… |
| No recover(): a panic in any supervisor goroutine leaves the user's terminal in raw mode | `internal/session/session.go:325` | Raw-mode restore is only a `defer restoreTerminal()` registered in RunSupervised's own goroutine (line 325). There is no recover() anywhere in internal/session (grep confirms zero matches).… |
| Unsynchronized closure variable restoreTerminal read/written across goroutines | `internal/session/session.go:334` | `restoreTerminal` is a captured closure variable. The main goroutine reads it in its deferred call (line 325). The copyInput goroutine reassigns it inside suspendTerminal (line 334) when an overlay… |
| Reconnect loop respawn path (where the fd leak lives) is never exercised for resource correctness | `internal/session/reconnect_test.go:47` | shouldRetry and computeBackoff are unit-tested, and cli_test.go TestRunForwardReconnectsOnTransientExit drives 3 real attempts, but no test asserts anything about resources held across attempts (fds,… |

**`state` + `fsutil` — persistence**

| Finding | Location | Summary |
|---|---|---|
| PruneRecords deletes by JSON-internal id, not filename: path-escape from sessions/ and wrong-file deletion | `internal/state/state.go:380` | PruneRecords removes records with `os.Remove(RecordPath(dir, record.ID))` where record.ID is the *content* id parsed from the JSON, never re-validated against the on-disk filename. ListRecords… |
| Cross-process read-modify-write on session records is last-writer-wins with no locking: closed records get resurrected and events/metadata lost | `internal/state/state.go:218` | There is no file locking anywhere in the state/fsutil layer — the only O_EXCL/flock-style call in the package is the backup-file create in fsutil/write.go:107. AtomicWriteFile guarantees byte-level… |
| ListRecords/ListForwards/ListProxies hard-fail on a single unparseable file, taking down list/prune/cleanup entirely | `internal/state/state.go:267` | ListRecords returns an error for the *whole* directory if any single .json file fails to parse, hiding every valid record. Because `session list` runs cleanupStaleSessionState ->… |
| Crashed local (non-mirror) sessions are never reaped and PID reuse reports them as 'active' | `internal/state/state.go:373` | Stale-record reaping only covers RemoteMirror records: CleanupStaleRemoteMirrors short-circuits non-mirrors (`!record.RemoteMirror -> return false`, state.go:335), and PruneRecords only touches… |
| No schema-version gating: forward-compat fields silently dropped and state_version reset on every rewrite | `internal/state/state.go:222` | Heading into a feature-freeze/schema-lock, the version field is purely decorative. WriteRecord unconditionally stamps `record.StateVersion = StateVersion` (=1) on every write, and no reader anywhere… |
| Concurrency, cross-process state mutation, and the prune path-escape are untested | `internal/state/state_test.go:1` | The state test suite covers the happy path (write/read/list, prune of well-formed records, cleanup of mirrors, forest building) but has no coverage for the failure modes this audit confirmed: (1)… |

**transfer — SFTP & in-band**

| Finding | Location | Summary |
|---|---|---|
| Receiver failure sentinel is unparseable: every remote head/decode failure surfaces as a generic 30s timeout | `internal/inband/inband.go:141` | receiverCommand's failure branches emit `printf 'SSHERPA_C_DONE %s %s\n' "$rc" ""` — with an empty third argument this prints `SSHERPA_C_DONE <rc> \n`, which strings.Fields reduces to 2 fields.… |
| Remote temp files leak on every in-band failure path; failed direct SFTP put also poisons the in-band fallback | `internal/inband/inband.go:151` | commitCommand removes the temp file only on the success path (mv): on sha mismatch (rc=1), overwrite refusal (rc=73), and mv failure, `<dest>.ssherpa.<nonce>.tmp` is left on the remote with no… |
| Any remote path whose basename is literally "download" cannot be sent to or received | `internal/cli/transfer.go:474` | remoteBase() returns the literal string "download" as a fallback sentinel for unresolvable paths (transfer.go:1517-1531), and remotePathInfo treats that sentinel as an error — but it collides with… |
| SFTP batch quoting of CR/LF mangles filenames: \n escape is unescaped by sftp to a plain 'n' | `internal/sshcmd/sshcmd.go:528` | quoteSFTPPath (sshcmd.go:521-529) and its duplicate quoteSFTPBatchPath (transfer.go:1594-1602) escape newline as `\n` and CR as `\r` inside double quotes, but OpenSSH sftp's batch argument parser… |
| Send overwrite gate mishandles directory destinations: prompts about the directory while the real overwritten file is never checked | `internal/cli/transfer.go:399` | confirmTransferSafety's send branch only checks whether the remote destination path exists; it has no IsDir handling (the receive branch does, transfer.go:418-421). When the destination is an… |
| Overwrite protection and remote pickers fail open when the server's SFTP longname is not OpenSSH-style ls -l | `internal/cli/transfer.go:1449` | remotePathInfo decides existence by string-parsing `ls -la` long listings: parseSFTPLongEntry requires the kind char in {d,-,l} and >= 9 whitespace fields, joining fields[8:] as the name, then… |
| No ConnectTimeout on any sftp invocation — pickers, existence probes, and transfers hang for the OS TCP timeout on unreachable hosts | `internal/sshcmd/sshcmd.go:219` | Supervised ssh commands get `-o ConnectTimeout=10` precisely so 'an offline host cannot leave the supervised UI waiting on the operating system's much longer TCP timeout' (sshcmd.go:24-27), but… |
| Receive writes directly to the final local path: an interrupted get destroys the confirmed-overwrite original and leaves a truncated file | `internal/sshcmd/sshcmd.go:271` | BuildSFTPBatch emits `get <remote> <localFinalPath>` with no temp-file indirection, and runSFTPCommand (transfer.go:369-395) does no cleanup on a non-zero exit. If the user confirms overwriting an… |
| In-band receiver/probe one-liners are POSIX-shell-only and are typed into whatever login shell the user runs (fish, nushell break) | `internal/inband/inband.go:141` | The receiver and commit commands rely on POSIX syntax — `read_rc=$?`, `[ "$read_rc" -eq 0 ]`, `{ ...; }` grouping, `$(...)` with `if/then/fi` — but they are written to the user's interactive remote… |
| Zero test coverage for the in-band PTY state machine and the CLI transfer safety pipeline | `internal/session/session.go:907` | newInbandSendFunc and waitForInbandOutput (the probe/READY/DONE state machine, the timeout/0x03 abort paths, the buffer trimming at 64KiB) have no tests at all — grep for Inband in… |

**`authkeys`**

| Finding | Location | Summary |
|---|---|---|
| Deleting one fingerprint removes EVERY authorized_keys entry that shares the same key blob (distinct grants collapse → data loss) | `internal/authkeys/authkeys.go:625` | Fingerprint is computed from the key blob only (SHA256Fingerprint decodes Blob, authkeys.go:54-61) and Identity() is Type+" "+Blob (authkeys.go:34-36) — neither includes Options. PlanDelete… |
| No test asserts control characters are rejected in the OPTIONS field (the exact bypass left open by the comment fix) | `internal/authkeys/authkeys_test.go:67` | The comment-field hardening has thorough coverage: TestParsePublicKeyLineRejectsControlCharsInComment (authkeys_test.go:67-89) checks newline/CR/NUL/ESC/DEL,… |

**`transcript` — recording & bundles**

| Finding | Location | Summary |
|---|---|---|
| session prune orphans transcript .cast files — documented cleanup never reclaims transcript storage | `internal/state/state.go:380` | PruneRecords (the documented storage-cleanup mechanism, default 168h) only deletes the session record JSON. The transcript body — the large file at <stateDir>/sessions/<id>.cast, capped at 50MiB each… |
| Replay sleeps for an attacker-controlled offset — uninterruptible ~292-year hang on imported transcripts | `internal/transcript/transcript.go:667` | transcript.Replay computes delay = offset - previous and time.Sleep(delay/speed * 1s) with no upper bound and no overflow guard. Offsets in an imported transcript are attacker-controlled. A single… |
| Writer permanently stops recording after any single transient write error | `internal/transcript/transcript.go:181` | writeFrame sets w.truncated=true and silently drops ALL subsequent frames on any file.Write error (e.g. a transient ENOSPC or EINTR), in addition to the maxBytes cap. There is no retry and no… |
| session log --follow aborts on a transient partial-frame read of a live transcript | `internal/cli/session.go:1794` | followTranscript re-reads the whole .cast every 500ms via transcript.Read, which uses bufio.Scanner. If the poll lands while the writer has flushed only part of a frame line (large output frames are… |
| Transcript freeze-critical paths are untested: prune .cast cleanup, replay timing/overflow, marker escaping, malicious/oversized bundle import | `internal/state/state_test.go:191` | Entering a maintenance freeze, several risky transcript paths have no tests pinning behavior. The prune test never creates a .cast, so the orphaned-transcript bug is invisible to CI. There are no… |

**`incoming` — daemon, presets, incoming sessions**

| Finding | Location | Summary |
|---|---|---|
| `proxy saved rename NAME NAME --yes` deletes the preset (data loss; no same-name guard) | `internal/cli/proxy_saved.go:207` | runProxySavedRename has no guard rejecting OLD==NEW (forward_saved.go:261-264 has `if flags.Name == flags.NewName { ... return 1 }`). With OLD==NEW, the `ReadProxy(NewName)` existence check passes… |
| Detach reports unconditional success even when the child never started or wrote a record | `internal/cli/daemon.go:113` | daemonizeRoute discards waitForDetachedRecord's boolean return and always prints 'forward/proxy detached', the session id, daemon pid, log path, and a stop hint, then returns 0. If the child dies… |
| PID-reuse: stale/inherited records report 'active' and stop/stop-all signal unrelated processes | `internal/state/state.go:167` | ProcessAlive and the stop paths key purely on LocalPID with kill(pid,0)/kill(pid,SIGHUP) and no identity check (no start-time, no /proc cmdline). After a reboot or daemon crash, a record's LocalPID… |
| Orphan detached records are a permanent dead end (cannot be stopped, listed-away, or pruned) | `internal/cli/forward_management.go:260` | When a detached daemon dies without writing EndedAt (crash, kill -9, the backoff-SIGHUP path above, or reboot), the record has EndedAt==nil and a dead/zeroed LocalPID. `forward stop`/`proxy stop`… |
| `incoming list` reports local tmux panes (and other parenthesized-host who rows) as incoming SSH sessions with garbage client IPs | `internal/incoming/incoming.go:283` | isInteractiveSSHWhoEntry keeps any pts/ or ttys row whose `who` line has a '(host)' suffix, treating the parenthesized field as a remote client. On Linux, tmux registers utmp entries that `who`… |

**`ui` — TUI widgets**

| Finding | Location | Summary |
|---|---|---|
| Byte-wise backspace corrupts multibyte filter queries in picker, host chooser, and transfer browser | `internal/ui/picker.go:381` | The three list screens slice ONE BYTE off the query on backspace: picker.go:379-383, host_chooser.go:258-262, transfer_browser.go:136-140. msg.Text is arbitrary UTF-8, so typing any non-ASCII… |
| Pasting is silently dropped in every screen except the add-alias form and text prompt | `internal/ui/forward_builder.go:461` | bubbletea v2 enables bracketed paste by default (vendored cursed_renderer.go:115-116 writes SetModeBracketedPaste unless View.DisableBracketedPasteMode, which no ssherpa view sets), and pasted text… |
| Destructive/dangerous confirms missed by commit 2daf46d still default to Yes (authorized_keys replace, raw transcript replay) | `internal/cli/authkeys.go:236` | ui.Confirm defaults the selection to Yes unless Danger is set (confirm.go:87 'selectedYes: !opts.Danger'). Commit 2daf46d moved deletes onto ConfirmDelete (Danger=true, default No), but two dangerous… |
| wrapConfirmText collapses explicit newlines, mashing the transcript-import verification fields into one paragraph | `internal/ui/confirm.go:178` | wrapConfirmText word-wraps with strings.Fields, which treats \n as ordinary whitespace. The transcript-import confirmation (cli/session.go:1023-1043) deliberately formats its operator-verification… |
| Forward builder accepts IPv6 specs that produce an ssh argv OpenSSH rejects (unbracketed -L) | `internal/sshcmd/sshcmd.go:193` | ParseForwardLocal explicitly accepts bracketed IPv6 ('[::1]:5432') and returns the unbracketed bind '::1', so the wizard's per-keystroke validation passes. But both the wizard preview… |
| No wide-character awareness: CJK/emoji content shears the frame; the test oracle shares the flaw | `internal/termstyle/termstyle.go:17` | termstyle.VisibleWidth counts every rune as width 1 and Truncate slices runes, so all frame math in internal/ui (workflowLine padding, PadRight, wrapPlain picker.go:1076, wrapConfirmText… |
| No graceful degradation below minimum sizes: views render wider and taller than tiny terminals | `internal/ui/picker.go:404` | Every View clamps the render width UP to a floor instead of down to the terminal: picker.go:404 'width := max(56, m.width)', transfer_browser.go:170 'max(64, m.width)', forward_builder.go:868… |
| proxy_builder.go (468 lines) has zero tests; no unicode-input or PasteMsg coverage anywhere in the package | `internal/ui/proxy_builder.go:234` | There is no proxy_builder_test.go: the SOCKS wizard's listener validation chain (updateListener at proxy_builder.go:234-256 wiring ParseForwardLocal + ValidateProxy), summary actions, save-name flow… |

**`termstyle` — theming & width math**

| Finding | Location | Summary |
|---|---|---|
| Strip passes non-CSI escape sequences (ESC=, ESC>, ESC(B, SS3, DCS) through verbatim, leaving raw ESC bytes in 'clean' transcript exports | `internal/termstyle/termstyle.go:38` | Strip only recognizes the two-byte introducer '\x1b[' (and transcript.Clean separately strips OSC via stripOSC, transcript.go:723). Every other escape form is copied to the output as literal bytes:… |
| VisibleWidth counts runes, not display cells: CJK/emoji count 1 (render 2) and combining marks count extra, breaking PadRight/Truncate column layout at 18+ UI call sites | `internal/termstyle/termstyle.go:17` | VisibleWidth does width++ per decoded rune with no East-Asian-width or zero-width handling. Verified: VisibleWidth("日本語") = 3 (renders 6 cells), VisibleWidth("👍") = 1 (renders 2), VisibleWidth("é")… |
| SSHERPA_THEME_FILE pointing at a not-yet-created path hard-fails every theme resolution (TUI, transcript viewer) while 'ssherpa theme' treats the same path as creatable; docs call it a 'default path' | `internal/termstyle/theme.go:305` | resolveThemeFile returns explicit=true for the env var (theme.go:309-311), and ResolveTheme then fails hard on fs.ErrNotExist for explicit paths (theme.go:137-140). Verified: ResolveTheme with… |
| Test gaps: zero coverage for Truncate, for Strip/VisibleWidth on non-SGR escape input, for NO_COLOR/SSHERPA_NO_COLOR env handling, and for rawSGR bounds | `internal/termstyle/termstyle_test.go:1` | termstyle_test.go contains only five tests (TestVisibleWidthIgnoresANSIEscapes, TestPadRightUsesVisibleWidth, TestDefaultThemeUsesPaletteCodes, TestResolveThemeParsesConfigOverrides,… |

**`cli` — dispatch, flags, exit codes**

| Finding | Location | Summary |
|---|---|---|
| Saved forward/proxy partial overrides contradict code comment and produce misleading errors | `internal/cli/route.go:330` | The comment above the forward catalog lookup claims 'CLI args always win — explicit local / remote / through values override the catalog defaults' (route.go:331-335), but the gate is 'flags.Select !=… |
| Aliases starting with '-' are listed but unusable: check rejects them (no -- escape) and --select builds an ssh argv that OpenSSH parses as options | `internal/cli/check.go:250` | An ssh config 'Host -web' is valid and appears in 'ssherpa list', but: (1) parseCheckFlags has no 'arg == "--"' case (unlike every other parser), so both 'ssherpa check -web' and 'ssherpa check --… |
| check with a non-matching --filter/--user reports ok:true and exit 0 — false-healthy signal for monitoring | `internal/cli/check.go:138` | runCheck only requires that some scope flag is present (check.go:84-87); if --filter or --user matches zero aliases, runCheckWithFlags iterates an empty inventory and computes out.OK by scanning zero… |
| Home-page saved presets are hidden by title collision with active ad-hoc tunnels | `internal/cli/cli.go:1139` | pickerSavedForwards/pickerSavedProxies hide a saved preset when its name appears in activeSavedNames (cli.go:1139-1141, 1163-1165), which is keyed on the active row's Title (cli.go:1118-1126). But… |
| Five duplicated ~150-case flag parsers plus three argv serializers must be kept in sync by hand; drift already exists | `internal/cli/route.go:425` | parseConnectFlags (cli.go:436-616), parseJumpFlags (route.go:425-625), parseProxyFlags (route.go:627-842), parseForwardFlags (route.go:844-1130) and parseCheckFlags (check.go:149-262) are… |
| Test gaps: connect bare-positional passthrough, check '--'/dash-alias handling, proxy catalog bind override, and serializer round-trips are all untested | `internal/cli/cli_test.go:223` | Despite ~3000 lines of cli tests, the risky dispatch/parse paths found in this audit have zero coverage: (1) no test exercises runConnect with bare positionals before '--'… |

**security — filesystem**

| Finding | Location | Summary |
|---|---|---|
| Control-master sockets use predictable shared /tmp path with no ownership check and ignore XDG_RUNTIME_DIR | `internal/session/session.go:625` | prepareControlMaster always builds the socket dir as os.TempDir()/ssherpa-<uid>/cm and calls MkdirAll(...,0700). On Linux os.TempDir() is the world-writable /tmp; MkdirAll neither fails nor repairs… |
| Symlinked ~/.ssh/config is silently replaced with a regular file (de-referenced) | `internal/fsutil/write.go:83` | readExisting uses os.Stat (follows symlinks) to read the current content, then writeTempRename renames a fresh temp file over the path, which replaces the symlink itself with a regular file. This… |

**security — terminal boundary**

| Finding | Location | Summary |
|---|---|---|
| Clean leaks RIS charset DCS bare ESC | `internal/transcript/transcript.go:591` | Clean only strips CSI and OSC so RIS charset DCS bare ESC reach terminal via log grep export |
| Remote forged SessionRecord via telemetry frames | `internal/session/osc_tracker.go:311` | telemetry base64 JSON decoded into SessionRecord validating only ID then persisted |

**security — supply chain**

| Finding | Location | Summary |
|---|---|---|
| All GitHub Actions pinned by mutable tags; release job hands two secrets to an unpinned third-party action | `.github/workflows/release.yml:29` | Every action is pinned by major-version tag, not commit SHA: actions/checkout@v6 (ci.yml:26,124; release.yml:18), actions/setup-go@v6 (ci.yml:29,128; release.yml:23), goreleaser/goreleaser-action@v7… |
| No release signing, SBOM, or provenance — only an unsigned checksums.txt | `.goreleaser.yaml:83` | .goreleaser.yaml has `checksum: name_template: checksums.txt` but no `signs:`, no `sbom:`, and release.yml has no `id-token: write` / `attestations: write` and no actions/attest-build-provenance… |
| Released binaries are not reproducible: no -trimpath, wall-clock date, no mod_timestamp (diverges from CI builds) | `.goreleaser.yaml:22` | The goreleaser build stanza has no `flags:` entry, so release binaries are built WITHOUT -trimpath, while both CI build steps (ci.yml:105 `go build -trimpath -o ssherpa`, ci.yml:140) and the README… |
| Release workflow is not gated on tests — a tag on a red commit ships immediately | `.github/workflows/release.yml:3` | release.yml triggers on `push: tags: v*` and runs goreleaser directly with no `go test`/`go vet` step and no dependency on the CI workflow; .goreleaser.yaml sets `release: draft: false`, so artifacts… |
| Homebrew tap is an unprotected single point of trust and can drift on partial release failure | `.goreleaser.yaml:74` | The cask is pushed straight to 0xbenc/homebrew-tap@main using TAP_GITHUB_TOKEN (.goreleaser.yaml:74-78). `brew install --cask 0xbenc/tap/ssherpa` (README.md:43) trusts whatever is on that branch:… |
| No Dependabot/Renovate and no govulncheck — dependency and action CVEs invisible during the maintenance freeze | `.github` | `find .github -iname '*dependabot*' -o -iname '*renovate*'` returns nothing, and grep for govulncheck/staticcheck/gosec across .github finds nothing. For a project entering a long stability phase,… |

**contract / compat**

| Finding | Location | Summary |
|---|---|---|
| state_version is written to every state file but never validated by any reader | `internal/state/state.go:222` | SessionRecord, StoredForward, StoredProxy and incoming markers all stamp state_version=1 on write (state.go:222, forward_catalog.go:53, proxy_catalog.go:34, incoming.go:171), but… |
| session list/show --json expose the raw internal SessionRecord struct as the public API | `internal/cli/session.go:1136` | `ssherpa session list --json` emits `writeJSON(stdout, records)` — the unfiltered []state.SessionRecord including ssh_argv, control_path, remote_cwd, remote_prompt, events, transcript spec,… |
| SendEnv=SSHERPA_* wildcard couples the entire env-var config namespace to the wire protocol | `internal/sshcmd/sshcmd.go:22` | Supervised sessions inject `-o SendEnv=SSHERPA_*` (SessionEnvPattern, applied in WithSessionEnvForwarding). The same SSHERPA_ prefix is used for local configuration (SSHERPA_STATE_DIR,… |
| Exit-code scheme has collisions and one-off special cases | `internal/cli/check.go:110` | check returns 3 for inventory-load failure while every other command returns 2 for the same condition (cli.go runConnect/runList return 2); check also returns 2 for 'checks ran but failed',… |
| Theme config parser hard-fails on unknown role keys — forward-incompatible user config file | `internal/termstyle/theme.go:190` | ParseThemeConfig returns an error for any key that is not a known role ('unknown theme role %q'). The default theme.conf path is loaded by ResolveTheme on essentially every themed command. If a… |
| Hidden --__supervisor/--__detached-* flags are an unversioned same-binary re-exec protocol on the public argv namespace | `internal/cli/daemon.go:18` | `ssherpa --__supervisor --__detached-id ID --__detached-state-dir DIR --__detached-log-path P forward ...` is dispatched before all other parsing (cli.go:171-173) and is invokable by any user or… |

**test-suite health**

| Finding | Location | Summary |
|---|---|---|
| In-band transfer PTY driver has effectively zero test coverage since flaky e2e was deleted | `internal/session/session.go:907` | Commit 4280600 ('Remove flaky inband transfer e2e test') deleted TestRunSupervisedOverlayInbandSendWritesRemoteFile, the only test exercising the in-band send driver end-to-end. What remains:… |
| CI never runs the race detector despite goroutine/PTY/signal-heavy session and daemon code | `.github/workflows/ci.yml:47` | ci.yml's test step is plain 'go test ./...'. grep for 'race' across both workflows returns nothing. The session package alone has signal-forwarding goroutines, an output tap, PTY readers, and… |
| cli package at 41.8%: entire interactive layer (142 zero-coverage functions) never runs in tests or CI | `internal/cli/session.go:938` | 142 functions in internal/cli have 0% coverage. The concentrations: session.go 34/64 funcs at 0% (replayRawWithControls, prepareReplayTerminal, drawReplayOverlay, runTranscriptBundleExportTUI,… |
| ui/proxy_builder.go is completely untested (0/21 functions, no test file) | `internal/ui/proxy_builder.go:23` | There is no proxy_builder_test.go. All 21 functions are at 0%: ListenerSpec, BuildProxy, updateDestination, updateListener (which validates user listener input via sshcmd.ParseForwardLocal),… |
| Bundle import — an untrusted-input zip parser — is tested only on the happy path | `internal/transcript/transcript.go:347` | ImportBundle parses zip bundles that explicitly come from other machines (origin classes imported_other/imported_unknown), i.e., untrusted input. Coverage: ExportBundle 63.2%, ImportBundle 69.6%,… |

**performance**

| Finding | Location | Summary |
|---|---|---|
| Home picker render performs four independent ListRecords directory scans, and session records are never auto-pruned | `internal/cli/cli.go:718` | selectConnectItem assembles home-picker data via pickerSessionCounts (cli.go:922), pickerStoppableSessionCount (cli.go:948), pickerActiveTunnels (cli.go:972), and pickerActiveProxies (cli.go:1002) —… |
| stdin-to-PTY copy loop reads and writes 1 byte per syscall — ~0.8 MB/s paste throughput floor | `internal/session/session.go:747` | copyInput uses a 1-byte buffer so it can intercept the overlay/composer hotkeys, paying two syscalls per input byte. Measured floor for this exact read-1/write-1 pattern over OS pipes: 0.78-0.81 MB/s… |
| ssherpa check probes hosts strictly sequentially with 5s+2s default timeouts — N×7s worst case on fleets | `internal/cli/check.go:126` | runCheckWithFlags loops over aliases serially; each checkAlias runs an SSH probe (default 5s timeout, check.go:150) and an ICMP probe (2s). No goroutines/worker pool anywhere in check.go. Checking a… |

**UX / support load**

| Finding | Location | Summary |
|---|---|---|
| Interactive picker hangs forever when stdin is not a TTY (scripts, cron, CI) | `internal/ui/picker.go:249` | ui.Pick and all chooser/confirm flows start bubbletea with no TTY check. Running `ssherpa` (or `ssherpa edit`, `receive --print` without --select, etc.) with stdin at EOF writes alt-screen ANSI to… |
| Typo'd subcommand silently becomes a remote ssh command | `internal/cli/cli.go:222` | Run's `default:` branch routes any unrecognized first argument into runConnect, where non-flag args become SSHArgs appended after the alias. `ssherpa lst` therefore opens the picker (or hangs… |
| Per-subcommand --help is the same 124-line global dump; `ssherpa help <topic>` is an error | `internal/cli/cli.go:215` | Measured: 11 of 14 subcommands (`add edit jump proxy forward send receive check authkeys list show`) print the identical 124-line global usage for --help; only incoming/theme/session have dedicated… |
| TUI 'Completions and manpage' prints cwd-relative paths that don't exist for installed binaries | `internal/cli/tui_features.go:283` | printArtifactInfo resolves 'completions/ssherpa.bash' etc. via filepath.Abs, which joins against the user's current working directory — not the install prefix. For every brew/deb/rpm/tarball user… |
| check failures show empty MESSAGE column; ssh stderr is discarded entirely | `internal/cli/check.go:415` | defaultRunSSHCheckProbe runs ssh via proc.Run() without capturing stderr, so the actual failure reason (Could not resolve hostname / Permission denied / timeout) is lost. checkAlias sets… |
| Plain `list` silently exits 0 with no output on missing, unreadable, or broken config | `internal/cli/cli.go:1248` | runList's human output loop prints only aliases and drops inventory.Diagnostics. Measured: missing config, chmod-000 config (severity 'error': 'could not read config file: ... permission denied'),… |
| Destructive confirm defaults inconsistent: authkeys replace and imported-raw-replay default to Yes | `internal/cli/authkeys.go:236` | Commit 2daf46d made Danger confirms default No (confirm.go:87 `selectedYes: !opts.Danger`). Delete alias/key/saved-forward and transfer overwrite (Danger:true, transfer.go:449) comply, and delete-all… |

**docs drift**

| Finding | Location | Summary |
|---|---|---|
| All shell completions missing 7 of 12 session subcommands (log/replay/grep/export/bundle/identity/browse) | `completions/ssherpa.bash:105` | internal/cli/session.go dispatches list, map, show, log, replay, grep, export, bundle, identity, browse, stop-all, prune (session.go:83-105), and README advertises 'ssherpa session log, grep, replay,… |
| SSHERPA_SFTP_BINARY env var undocumented despite being referenced in ssherpa's own error hint | `docs/non-interactive.md:43` | The environment-variable table lists SSHERPA_SSH_BINARY but not SSHERPA_SFTP_BINARY, which sets the default sftp binary (cli/binary.go:38, transfer.go:1622) and which the missing-binary error message… |
| Theme file default path wrong on macOS: docs say ~/.config/ssherpa/theme.conf, code uses os.UserConfigDir (~/Library/Application Support) | `docs/non-interactive.md:30` | The Defaults table states unconditionally 'Theme file \| ~/.config/ssherpa/theme.conf'. resolveThemeFile uses os.UserConfigDir() (termstyle/theme.go:315-319), which on macOS resolves to… |
| SSHERPA_IGNORE_USER_GIT undocumented; ssherpa silently hides User=git hosts (e.g. github.com) from list and picker by default | `internal/cli/cli.go:1419` | loadInventory defaults IgnoreGitUser to true (cli.go:1419-1421: enabled unless SSHERPA_IGNORE_USER_GIT is set to a false value), and hostlist drops any alias whose parsed User is 'git'… |
| --all-sources documented as 'delete all matching source stanzas instead of the first target' but actually gates multi-FILE deletion; all same-file stanzas are always deleted | `docs/non-interactive.md:272` | Both non-interactive.md ('Delete all matching source stanzas instead of the first target') and the man page imply stanza-level granularity. In code, chooseExistingTargets only errors when the alias… |
| Built-in `ssherpa help` lists only 5 of 12 session subcommands and ships an internal 'Phase 10:' development paragraph | `internal/cli/cli.go:130` | The top-level usage block's 'Session Commands' section (cli.go:130-135) omits log, replay, grep, export, bundle, identity, and browse — the recording features README markets. The help text also ends… |
| In-band base64 PTY transfer fallback is undocumented; README/docs claim transfers are 'over OpenSSH SFTP' and label the transport env var 'Internal/testing' | `internal/cli/transfer.go:819` | Overlay send automatically falls back to an in-band base64 transfer through the live PTY when direct SFTP fails or errors (transfer.go:769-835, 'ssherpa: using in-band PTY transfer to %s'), and… |

**release & distribution**

| Finding | Location | Summary |
|---|---|---|
| Release binaries built without -trimpath and not reproducible | `.goreleaser.yaml:22` | goreleaser builds set only ldflags ('-s -w' + version stamps); no 'flags: [-trimpath]' and no 'mod_timestamp'. The published v1.7.1 linux_amd64 binary confirms: no trimpath build setting, embedded… |
| No signing, SBOM, or provenance attestation on release artifacts | `.goreleaser.yaml:83` | Only 'checksum: name_template: checksums.txt' exists — no 'signs:' or 'sboms:' sections, and release.yml has no attest-build-provenance step (permissions are only 'contents: write'; no 'id-token:… |
| Shipped completions and man page already drifted from CLI: session transcript subcommands missing | `completions/ssherpa.bash:105` | internal/cli/session.go:83-106 dispatches list, map, show, log, replay, grep, export, bundle, identity, browse/transcripts, stop-all, prune — but all three completion files (bash:105, zsh:130,… |
| Archives and packages omit LICENSE despite MIT requiring its inclusion | `.goreleaser.yaml:33` | The archives 'files:' override (completions/* and man/* only) drops goreleaser's default behavior of bundling LICENSE/README. Verified: 'tar tzf ssherpa_1.7.1_linux_amd64.tar.gz' lists only… |
| Homebrew cask ships unsigned, un-notarized macOS binaries with no quarantine handling; cask path unusable on Linux | `.goreleaser.yaml:60` | The homebrew_casks config (correctly automated, and correctly installing binary + manpages + all completions — verified in the generated Casks/ssherpa.rb at 0xbenc/homebrew-tap) has no… |
| Release workflow publishes without running any tests and has no config-drift guard in CI | `.github/workflows/release.yml:12` | release.yml triggers on tag push and runs goreleaser directly — no 'go test', no gate on the CI workflow passing for that commit. A tag pushed on a broken or never-CI'd commit ships immediately to… |
| Half the shipped platform matrix is compile-only: linux/arm64 and darwin/amd64 never run tests | `.github/workflows/ci.yml:107` | goreleaser ships linux/{amd64,arm64} and darwin/{amd64,arm64}, but the CI test matrix is only ubuntu-latest (amd64) and macos-latest (arm64); the cross-build job for linux/arm64 and darwin/amd64 only… |
| No CHANGELOG.md; release notes are raw commit lists and semver practice is inconsistent | `.goreleaser.yaml:89` | Across 23+ tags there is no CHANGELOG.md (verified: find finds none; docs/ has only IMPROVEMENTS.md, non-interactive.md). Release bodies are goreleaser's default commit-hash lists (v1.9.1 body: '##… |
| deb/rpm users have no update channel and packages omit the openssh-client dependency | `.goreleaser.yaml:37` | The nfpms section declares no 'dependencies:', yet ssherpa hard-requires the ssh binary at runtime (internal/sshcmd/binary.go:13: 'install an OpenSSH client, or pass --ssh-binary PATH'). More… |

**dependencies**

| Finding | Location | Summary |
|---|---|---|
| No automated dependency-update or vulnerability signal for the low-touch period | `.github/workflows/ci.yml:43` | There is no .github/dependabot.yml (ls .github/workflows shows only ci.yml and release.yml) and ci.yml runs only gofmt/vet/test/smoke/build — no govulncheck step. For a 1–2 year low-touch maintenance… |
| bubbletea v2 stable depends on charmbracelet/ultraviolet at untagged pseudo-versions (no release has ever been cut) | `go.mod:13` | The ultraviolet pseudo-version is not ssherpa's choice — it is bubbletea's own requirement: v2.0.6 requires v0.0.0-20260416155717, and even v2.0.7 still requires a pseudo-version… |
| CI and release builds pin Go 1.26.3 via go-version-file; Go 1.26 exits upstream support (~Feb 2027) inside the freeze window | `.github/workflows/release.yml:25` | Both ci.yml and release.yml use `go-version-file: go.mod`, and go.mod says `go 1.26.3` — so release binaries are built with exactly 1.26.3 even though 1.26.4 is already current (local toolchain is… |

**governance**

| Finding | Location | Summary |
|---|---|---|
| No written versioning/support/deprecation policy; SECURITY.md support window is days long at observed cadence | `SECURITY.md:5` | SECURITY.md supports 'Only the latest minor release line', but git tags show 22 releases in 13 days (v1.3.0 2026-05-29 ... v1.7.1 2026-06-05), five minor bumps in one week — so the stated support… |
| Release process undocumented; Homebrew publish hinges on an undocumented personal token (bus factor 1) | `.github/workflows/release.yml:36` | All 108 commits are by one author (git shortlog -sne: '104 Ben Chapman <0xbenc@gmail.com>' + '4 Ben Chapman <94716829+...>'). Releases are tag-push automated, but the cask publish requires secret… |
| No issue templates or PR template — zero pre-triage for incoming adoption | `.github` | .github/ contains only workflows/ (ls shows ci.yml, release.yml). There is no ISSUE_TEMPLATE/ directory, no PULL_REQUEST_TEMPLATE.md, no SUPPORT.md, and no config.yml routing questions elsewhere. The… |
| No dependabot.yml — dependencies and pinned actions never get update PRs | `.github` | No dependabot.yml (or renovate config) exists anywhere in the repo. The Go module tree (bubbletea v2, creack/pty, charmbracelet/x/*) and the GitHub Actions (checkout@v6, setup-go@v6,… |
| CI lacks -race, govulncheck, and goreleaser config validation | `.github/workflows/ci.yml:47` | ci.yml runs gofmt/vet/'go test ./...'/smoke tests/cross-builds — solid — but: (1) no 'go test -race' despite heavy PTY/goroutine code in internal/session and internal/state; (2) no govulncheck, so… |
| CONTRIBUTING.md does not state project status, scope of accepted PRs, or freeze policy | `CONTRIBUTING.md:3` | CONTRIBUTING.md is 32 lines: local check commands and five safety rules. It says nothing about the imminent feature freeze, what kinds of PRs are welcome (bugfix vs feature), required Go version, how… |


### 12.3 Low findings (confirmed)

- **SFTP batch quoting does not neutralize leading dash** (`internal/sshcmd/sshcmd.go:537`) — isSafeSFTPPath allows dash so a leading-dash path is emitted as a get/put operand and parsed as flags.
- **Backups of authorized_keys/ssh config retain original world-readable mode; never hardened to 0600** (`internal/fsutil/write.go:63`) — AtomicWriteFile captures the backup mode from the existing file's permissions (stat.Mode().Perm()) and passes it to createBackup BEFORE applying the opts.Mode override…
- **Raw replay emits attacker bytes unwarned** (`internal/cli/session.go:161`) — session replay writes raw frame data no confirm; TUI warns only for imports
- **`before: hooks: go mod tidy` lets the release build mutate the dependency graph at release time** (`.goreleaser.yaml:8`) — Go's default -mod=readonly protects CI (`go test`, `go build` in ci.yml fail rather than rewrite go.mod), but the goreleaser before-hook explicitly runs `go mod tidy`,…
- **Release build restores main-branch Actions caches (setup-go cache: true) — cache-poisoning path into release artifacts** (`.github/workflows/release.yml:26`) — release.yml uses actions/setup-go@v6 with `cache: true`. Tag-ref workflow runs fall back to the default-branch cache scope, so the release build's GOMODCACHE and…
- **SECURITY.md asks for private reports but provides no reporting channel** (`SECURITY.md:19`) — SECURITY.md instructs reporters to 'report it privately and do not disclose it publicly' and forbids public GitHub issues (line 32), but contains no email address, no…
- **Supervised-session key table omits overlay 'S' (send file) and 'V' (receive file) keys** (`docs/non-interactive.md:181`) — The overlay handles 's'/'S' (file send) and 'v'/'V' (file receive) plus 'r'/'R' refresh (session.go:855-893), and README lists 'File sending' as a standout, but the only…
- **Pinned golang.org/x/sys v0.43.0 carries known vuln GO-2026-5024; fix is one bump away** (`go.mod:25`) — govulncheck reports GO-2026-5024 (integer overflow in NewNTUnicodeString, golang.org/x/sys/windows, fixed in v0.44.0) against the pinned x/sys v0.43.0. Impact today is…
- **bubbletea v2 is only ~3.5 months stable with 7 patch releases in 14 weeks — a hard 2-year pin at v2.0.6 forgoes a fast-moving fix stream** (`go.mod:6`) — Proxy metadata shows v2.0.0 on 2026-02-24 and v2.0.7 on 2026-06-01 — seven patches in 14 weeks, three on a single day (Apr 13). That cadence signals an early-life major…
- **PTY master fd leaked on every reconnect attempt (regression from commit a112f3b)** (`internal/session/session.go:1984`) — attemptOnce drains output after proc.Wait() with a select. On the common fast path the PTY reader hits EIO right after child exit, so outputDone closes within the 100ms…
- **nextArg accepts flag-looking values, so a missing flag value silently consumes the next flag** (`internal/cli/cli.go:1363`) — nextArg (cli.go:1363-1370) returns args[i+1] unconditionally, so 'ssherpa list --filter --json' sets Filter="--json" and exits 0 with empty output instead of either…
- **No terminal capability degradation: raw truecolor/256-color SGR from theme.conf is emitted verbatim on 16-color and TERM=dumb terminals; TERM/COLORTERM never consulted** (`internal/termstyle/theme.go:158`) — ResolveTheme's only output-suppression inputs are the --no-color flag, SSHERPA_NO_COLOR, and NO_COLOR (theme.go:158). Nothing in the repo inspects TERM or COLORTERM for…
- **theme.conf 'theme ='/'base =' key is parsed and validated but completely ignored by ResolveTheme; VividTheme/BuiltinTheme are dead code; the theme editor silently deletes the line on save** (`internal/termstyle/theme.go:150`) — ParseThemeConfig accepts 'theme = X' / 'base = X' and even rejects an empty value with an error (theme.go:183-186), strongly implying the key is meaningful. But…
- **Truncate is not ANSI-aware (unlike siblings VisibleWidth/PadRight): truncating styled text slices mid-escape and drops the trailing reset, leaking style; safety rests on caller convention only** (`internal/termstyle/termstyle.go:73`) — Truncate slices []rune with no escape awareness, while its sibling helpers in the same file are ANSI-aware. Verified: Truncate(Apply(false,"1;36","hello world"), 8) =…
- **Remaining timing-dependent tests: inventory of flake-risk sites for CI under load** (`internal/session/session_test.go:1303`) — One flaky e2e was already deleted (4280600); these are the remaining timing sensitivities. (1) session_test.go:1303 latency-warning tests keep the child alive with a…
- **CLI replay/log --raw of imported transcripts has no untrusted-output guard the TUI enforces** (`internal/cli/session.go:161`) — The TUI replay-raw action confirms before raw-replaying an imported recording ('Raw replay can emit terminal escape sequences from another machine'), and the TUI viewer…

### 12.4 Unverified observations (single-pass; leads, not conclusions)

**`sshconfig` + `hostlist` — config parsing & mutation:**
- *[low]* filepath.Match used for Host pattern semantics: [..] character classes and backslash escapes that OpenSSH treats literally are honored, mis-attributing effective values (`internal/hostlist/hostlist.go:192`)
- *[low]* Mixed line-ending files are fully rewritten (all endings normalized) even when the operation reports 'unchanged' (`internal/sshconfig/mutate.go:313`)
- *[low]* A pre-existing unparseable line anywhere in the file blocks every mutation with a misleading, location-free error (`internal/sshconfig/mutate.go:637`)
- *[low]* No file locking around the plan/apply read-modify-write; concurrent ssherpa invocations can silently drop each other's edits (`internal/cli/mutate.go:1686`)

**`session` — PTY supervision:**
- *[low]* forwardSignals forwards SIGINT to a single process, not the child's process group (`internal/session/session.go:1591`)

**transfer — SFTP & in-band:**
- *[low]* outputTap.observe silently drops suppressed output when the tap channel is full; in-band streaming phase never drains it (`internal/session/session.go:730`)
- *[low]* In-band max-bytes knob is unreachable and oversize errors misreport the file size (`internal/cli/transfer.go:827`)
- *[low]* Duplicate SFTP quoting implementations in two packages risk divergence during the freeze (`internal/cli/transfer.go:1594`)

**`authkeys`:**
- *[low]* Whitespace inside options is not byte-exact on round-trip (tabs/extra spaces collapse on Render) (`internal/authkeys/authkeys.go:262`)
- *[low]* CR-only (classic-Mac) line endings make the whole file parse as one unreadable line (`internal/authkeys/authkeys.go:752`)

**`transcript` — recording & bundles:**
- *[low]* Recorded asciicast header hardcodes 120x40 — exported casts render at the wrong terminal size (`internal/session/session.go:1890`)
- *[low]* Pause/resume gaps are replayed verbatim — replay sleeps through the entire paused wall-clock interval (`internal/session/session.go:1816`)

**`incoming` — daemon, presets, incoming sessions:**
- *[low]* proxy saved rename/edit/delete have no test coverage (untested data-mutating paths) (`internal/cli/proxy_saved.go:207`)

**`ui` — TUI widgets:**
- *[low]* Dead code in picker.go: renderHeader, previewKV, sessionDescription are uncalled and contain stale layout math (`internal/ui/picker.go:484`)
- *[low]* Stale PickOptions doc: claims sub-pickers quit on lowercase 'q', but only capital 'Q' cancels everywhere (`internal/ui/picker.go:110`)
- *[low]* validateSaveName duplicates state.validateCatalogName with no drift guard (`internal/ui/forward_builder.go:850`)
- *[low]* Text viewer ignores ctrl+c (bubbletea v2 does not auto-quit on the ctrl+c key) (`internal/ui/text_viewer.go:105`)

**`cli` — dispatch, flags, exit codes:**
- *[low]* runConnect ItemCheck branch swallows non-zero check exit codes (missing code==0 gate) (`internal/cli/cli.go:325`)
- *[low]* --theme NAME is dead plumbing: parsed by five parsers, validated, threaded through every UI struct, but ignored by termstyle and dropped inconsistently by serializers (`internal/cli/route.go:1814`)
- *[low]* Picker Add action drops --no-color/--theme-file: Add form renders unthemed/colored regardless of connect flags (`internal/cli/cli.go:290`)
- *[low]* Forward/proxy builder 'Run in background' fails after the wizard when home mode is --print (`internal/cli/route.go:1589`)

**security — filesystem:**
- *[low]* ssh config writes inherit existing (possibly world-readable) mode; never normalized (`internal/cli/mutate.go:1525`)
- *[low]* Imported transcript written non-atomically via os.WriteFile (follows symlink, no atomic rename) (`internal/transcript/transcript.go:397`)

**security — supply chain:**
- *[low]* README install instructions never mention checksums.txt verification (`README.md:55`)
- *[low]* ultraviolet pinned to an untagged pseudo-version commit — upstream stability risk, not tampering risk (`go.mod:13`)

**contract / compat:**
- *[low]* Undocumented environment variables: SSHERPA_SFTP_BINARY, SSHERPA_IGNORE_USER_GIT, SSHERPA_HOST_LABEL (`internal/cli/binary.go:38`)
- *[low]* session grep --json emits null instead of [] when there are no matches (`internal/transcript/transcript.go:636`)
- *[low]* forward stop / proxy stop accept --yes but never use it (`internal/cli/forward_management.go:238`)
- *[low]* --print --json is the only compact (non-pretty) JSON output; docs claim pretty-printing (`internal/sshcmd/sshcmd.go:461`)
- *[low]* ssherpa help output contains dev-phase leftovers and omits ~half the session surface (`internal/cli/cli.go:137`)
- *[low]* forward/proxy subcommand keywords permanently shadow positional alias names (`internal/cli/route.go:287`)
- *[low]* stdout/stderr conventions are inconsistent for cancellation and stop messages (`internal/cli/authkeys.go:165`)
- *[low]* Undocumented command/flag aliases: `session transcripts`, `export --format cast` (`internal/cli/session.go:101`)

**test-suite health:**
- *[low]* Platform/CI execution gaps: code that never runs anywhere in CI (`internal/cli/check.go:440`)
- *[low]* staticcheck (not in CI) flags 14 dead functions plus two real logic smells (`internal/ui/host_chooser.go:584`)
- *[low]* Transcript writer/export surface gaps: WriteInput, Snapshot, ExportAsciicast at 0% (`internal/transcript/transcript.go:599`)

**performance:**
- *[low]* state.WriteRecord costs 6ms (two fsyncs + discarded unified diff) and runs synchronously inside the PTY output loop (`internal/fsutil/write.go:175`)
- *[low]* Supervised PTY output path tops out at ~83 MB/s (2.6x below raw PTY) due to per-byte OSC state machine and per-chunk allocation (`internal/session/session.go:1997`)
- *[low]* transcript.Read loads entire recordings (all frames) into memory for text/grep/replay/export (`internal/transcript/transcript.go:504`)

**UX / support load:**
- *[low]* --theme flag and `theme =` config key are parsed but completely nonfunctional (`internal/termstyle/theme.go:130`)
- *[low]* Global usage contains internal 'Phase 10' jargon and is stale/incomplete (`internal/cli/cli.go:137`)
- *[low]* Ctrl-] overlay and escape rope are undiscoverable in-product and absent from the man page (`man/ssherpa.1:1`)
- *[low]* TERM=dumb still renders the full TUI with kitty-protocol queries and box-drawing (`internal/termstyle/theme.go:158`)
- *[low]* Minor copy issues: '[would-added]', 'delete 1 aliases', failed confirmation exits 0 (`internal/cli/mutate.go:388`)

**docs drift:**
- *[low]* Completions missing forward reconnect flags (--reconnect-backoff, --reconnect-max-backoff; zsh/fish also --no-reconnect/--reconnect-max) (`completions/ssherpa.zsh:48`)
- *[low]* zsh completion has no handling for authkeys, edit, add, jump, or theme; fish has no authkeys flags (`completions/ssherpa.zsh:137`)
- *[low]* fish completion for check missing --filter/--user/--all and 'recv' alias never completed (`completions/ssherpa.fish:20`)
- *[low]* No shell completes top-level connect flags (--select, --print, --direct, --latency-*, --composer-key, --no-record, --record-max-bytes, ...) (`completions/ssherpa.bash:9`)
- *[low]* man page omits 'list' and 'show' commands entirely (`man/ssherpa.1:32`)
- *[low]* man page session coverage omits recording/replay/export/bundle/identity/browse/prune (`man/ssherpa.1:76`)
- *[low]* man page documents no supervised-session keybindings (Ctrl-], escape rope, panic taps, composer) (`man/ssherpa.1:25`)
- *[low]* Documented `version` output format omits colons; scripts parsing per the docs will fail (`docs/non-interactive.md:85`)
- *[low]* --record-max-bytes default documented as '50MB' but actual default is 50 MiB while the parser treats MB as 10^6 (`docs/non-interactive.md:160`)
- *[low]* Documented exit code 3 ('config/inventory load failure for check') is effectively unreachable — unreadable/missing config degrades to empty inventory and exit 2 (`docs/non-interactive.md:67`)
- *[low]* Undocumented command aliases: 'session transcripts' (= browse) and export '--format cast' (= asciicast) (`internal/cli/session.go:101`)
- *[low]* README lacks any quick-start/usage section between install and the CLI reference link (`README.md:91`)

**release & distribution:**
- *[low]* 'go mod tidy' as release before-hook can mutate the tagged module state (`.goreleaser.yaml:6`)
- *[low]* TAP_GITHUB_TOKEN is an undocumented long-lived PAT single point of failure (`.github/workflows/release.yml:36`)
- *[low]* go install users get version 'dev' — no debug.ReadBuildInfo fallback (`cmd/ssherpa/main.go:10`)
- *[low]* Man page metadata is hand-stamped and will go stale at freeze (`man/ssherpa.1:1`)

**dependencies:**
- *[low]* go.mod pins patch-level `go 1.26.3`, forcing toolchain auto-download and breaking GOTOOLCHAIN=local builders on older patches (`go.mod:3`)
- *[low]* creack/pty: repo active but last tag v1.1.24 is 19 months old; unreleased fixes accumulate on master (`internal/session/session.go:1930`)
- *[low]* No SBOM or build attestation in goreleaser despite tiny auditable dependency tree (`.goreleaser.yaml:1`)

**governance:**
- *[low]* docs/IMPROVEMENTS.md is stale and self-contradictory ('Not committed' yet committed; completed items still listed) (`docs/IMPROVEMENTS.md:109`)
- *[low]* Release artifacts are unsigned and have no SBOM/attestation (`.goreleaser.yaml:83`)
- *[low]* Hand-maintained man page and docs carry hardcoded dates/versions that will drift through the freeze (`man/ssherpa.1:1`)

**gap sweep (§15):**
- *[medium]* Injected -o ControlMaster/ControlPath/ControlPersist silently override the user's own multiplexing configuration in ~/.ssh/config (`internal/sshcmd/sshcmd.go:132`)
- *[medium]* ControlPath guard never recognizes the -S flag, and '-- ssh-args' are appended after the destination so -S cannot even reach ssh as an option (`internal/sshcmd/sshcmd.go:237`)
- *[medium]* No test covers ControlMaster lifecycle/teardown; socket-removal errors are silently discarded (`internal/session/session_test.go:87`)
- *[medium]* No path to send a literal 0x1D to the remote — double-press does not send-literal (screen C-a a convention absent) (`internal/session/session.go:754`)
- *[medium]* Composer is enabled by default and swallows Ctrl-G (0x07) — emacs keyboard-quit never reaches the remote unless --no-composer (`internal/session/session.go:165`)
- *[medium]* Man page and README omit the panic-tap and all hotkey-interception caveats; no vim/telnet/emacs conflict warning anywhere (`man/ssherpa.1:1`)
- *[medium]* Only stall detector (latency watchdog) is opt-in, warn-only by default, and its force-kill path suppresses the reconnect it should trigger (`internal/session/session.go:2228`)
- *[medium]* Respawn reuses identical argv with no mux hygiene between attempts; retry can attach to a half-dead user-config ControlMaster and stall unboundedly (`internal/session/session.go:1924`)
- *[medium]* Interactive teardown unlinks the control socket without terminating the ControlPersist=10m master, orphaning an authenticated ssh for up to 10 minutes (`internal/session/session.go:288`)
- *[medium]* Recording stops silently on size cap or write error — no marker, no live notice, Truncated flag lost on crash (`internal/transcript/transcript.go:176`)
- *[medium]* session log --follow hard-exits on any transient read error while racing the live writer (`internal/cli/session.go:1794`)
- *[medium]* No test covers a truncated or partially-written .cast — reader robustness class systematically untested (`internal/transcript/transcript_test.go:13`)
- *[medium]* Telemetry fallback flattens chains deeper than 2: grandchild mirrors get the wrong parent, wrong depth, and a route missing intermediate hops (`internal/session/session.go:2127`)
- *[medium]* README promises nested session map / lineage with no AcceptEnv caveat; the only AcceptEnv docs are buried in non-interactive.md and the man page's incoming section (`README.md:23`)
- *[medium]* --config is inventory-only for connect/jump/proxy/forward: launched ssh omits -F, so aliases from an alternate config fail to resolve (`internal/sshcmd/sshcmd.go:111`)
- *[medium]* RS-framed telemetry prints ~1.4KB of visible base64 (full session record incl. argv) to the terminal when ssherpa runs inside a plain SSH login (`internal/session/session.go:2051`)
- *[medium]* No integration test exercises the sshd AcceptEnv seam; all nested-metadata tests inject env directly into the process (`internal/state/state_test.go:300`)
- *[low]* SOCKS proxy sessions can never reconnect even though the retry policy explicitly whitelists KindProxy (`internal/cli/route.go:276`)


---

## 13. What adversarial verification changed

Every non-low finding was sent to an adversarial panel (1–3 independent verifiers per finding, default stance *refuted*, distinct lenses: correctness / reproduction / impact). The outcome is unusual and worth interpreting:

- **0 of 146 panel-reviewed findings were refuted.** The hunters were grounded — most findings came with quoted code and many with live reproductions, and verifiers independently re-derived them (several built fixtures: a 20-attempt fd-leak reproduction, a live OpenSSH 10.2 "invalid quotes" fatal, a same-name `proxy saved rename` data-loss reproduction, torn-cast fixtures driven through every reader).
- **46 findings had severity recalibrated** — the panels' real value. Notable downgrades:
  - *PTY master fd leaked on every reconnect attempt* — confirmed real (regression from `a112f3b` itself: the fast-path `case <-outputDone: return waitErr` skips `ptmx.Close()`), but recalibrated **critical → low**: one fd per reconnect attempt, bounded by attempt counts in practice.
  - *Deleting one fingerprint removes every entry sharing the key blob* — confirmed, **high → medium**: requires the same public key granted twice with different options, a real but uncommon `authorized_keys` shape.
  - *No `recover()` → panic leaves terminal in raw mode* and *SIGTERM during backoff skips finalization* — confirmed, **high → medium**: real, but require a panic/signal in a narrow window.
  - *`nextArg` accepts flag-looking values* (medium → low), *theme `base =` key dead code* (medium → low), *`Truncate` not ANSI-aware* (medium → low), and similar — real defects whose blast radius the impact lens judged small.
  - Upgrade of note: *authorized_keys OPTIONS field control-character bypass* was held at **high** with a sharpened mechanism: `scanFields` is quote-aware, so a newline inside a quoted `command="..."` option survives into the options token and `ValidateStructural` never checks it — the exact bypass left open by the comment-only fix in `5448f8d`.
- **One verification vote was lost to an API error** (a verifier on the *paste silently dropped* finding); its panel concluded from the remaining votes.
- The 16 confirmed lows and the 84 "unverified observations" in §12 were never panel-reviewed (by design — verification effort was spent on what would gate the lock). Treat those as good leads with single-auditor confidence.

---

## 14. The pre-lock plan

The triage agent unified all 155 confirmed findings with the 99-item `docs/IMPROVEMENTS.md` backlog and recent git history. The philosophy of the split: **"must" = shipping a release labeled *stable* with this issue would be a mistake** (data loss, corruption of user files, security, or a contract that cannot be frozen as-is); **"should" = makes the low-touch maintenance phase actually low-touch**; everything else is post-lock, already done, or explicitly declined. Effort: S = hours, M = a day or two, L = several days.

### 14.1 Must do before the lock — eleven work packages

**WP1 · Argument-injection guard (M)** — Insert a positional `--` before the destination in all five argv builders (`BuildDirect`, `BuildProxy`, `BuildForward`, `BuildSFTP`, `BuildProbe` — `sshcmd.go`) and/or reject leading-dash alias names at inventory build. Closes the confirmed-high ProxyCommand RCE from hostile/shared configs, the SFTP leading-dash operand, and makes dash-aliases consistently handled in `check`. *(IMPROVEMENTS #38; findings: ssh argument injection; SFTP dash; dash-aliases unusable.)*

**WP2 · Terminal-escape sanitization at every sink (M)** — One sanitizer, applied at every point remote-influenced or imported text reaches the user's terminal: OSC 7 percent-decoded cwd/host (`parseOSC7` → overlay), imported session metadata (`session show`/`browse`: TargetAlias, Route, Argv, DisconnectReason, Events), marker frames in "cleaned" transcript text, and `Clean`'s gaps (RIS, charset designators, DCS/SOS/PM/APC, bare ESC). Fix `skipANSI`/`Strip` to terminate CSI on the full final-byte range (`@`–`~`), not just ASCII letters — currently they both over-consume and under-strip. Gate raw replay of imported transcripts behind the same default-No confirm the TUI uses. *(Findings: OSC7 injection; metadata verbatim render; marker raw emit; Clean leaks; skipANSI; CLI raw-replay guard.)*

**WP3 · Telemetry & bundle hardening (M)** — Clamp what a remote can forge: validate mirrored `SessionRecord`s (size, field allowlist, parent attribution) instead of accepting any JSON with a non-empty ID; cap zip-entry decompression on bundle import/preview (zip-bomb); clamp replay sleep offsets (an imported cast can currently sleep ~292 years); make `ImportBundle`'s transcript write atomic. *(Findings: forged telemetry records; zip no size limit; replay sleep; non-atomic import write.)*

**WP4 · authkeys correctness (S–M)** — Validate the *options* field for control characters (the quoted-`command=` newline bypass left open by `5448f8d`); make delete operate on exact entries rather than collapsing all lines sharing a key blob (or at minimum show every line that will be removed in the confirm); flip `authkeys replace` (lockout-capable) and imported-raw-replay confirms to default-No for consistency with `2daf46d`. *(Findings: options validation; blob-collapse delete; confirm defaults.)*

**WP5 · sshconfig mutation round-trip integrity (L)** — The highest-stakes cluster: ssherpa edits the user's real `~/.ssh/config`. Fix `renderValue` single-quote handling (OpenSSH ≥ 8.7 fatals on the written file); make alias matching case-insensitive end-to-end so editing `Host Prod` updates instead of appending a duplicate; tighten block-bound detection so deletes stop taking trailing comments and `Include` directives (which silently wipes every host the include defines); preserve all unmanaged options when splitting an alias out of a multi-pattern stanza; preserve multiple `IdentityFile` lines on edit. Land it with the round-trip/fuzz tests the test-gap findings specify — these exact shapes (trailing comments, mixed case, multi-IdentityFile, multi-pattern, CRLF) are currently untested. *(IMPROVEMENTS #60, #66; findings: renderValue; mixed-case; delete-trailing-content; multi-pattern option drop; IdentityFile collapse; mutation test gaps.)*

**WP6 · State-layer integrity (M–L)** — `PruneRecords` deletes by JSON-internal `id` rather than filename — a tampered or imported record can point the delete outside `sessions/`; fix to delete the file it read. Make `ListRecords`/`ListForwards`/`ListProxies` skip-and-warn on one unparseable file instead of failing the whole listing (one corrupt record currently takes down `list`, prune, and cleanup). Add read-side `state_version` gating (see §4). Decide the cross-process story: either per-record file locking or documented last-writer-wins with closed-record protection (today a slow writer can resurrect a closed session and drop events). Reap dead local sessions (PID-probe + TTL) so `session list` reflects reality and PID reuse stops reporting ghosts as active. *(IMPROVEMENTS #28, #29; findings: prune path-escape; hard-fail listing; lockless RMW; stale local sessions; PID reuse.)*

**WP7 · Supervisor reliability (M)** — Install a top-level `recover()` + guaranteed raw-mode restore so a panic can never wedge the user's terminal; extend signal handling across the reconnect-backoff window (currently SIGTERM/SIGHUP there kills the daemon with no finalization — `forward stop` during backoff reports success and leaves a permanent orphan); close the leaked PTY master on the fast path (`a112f3b` regression); synchronize the `restoreTerminal` closure; and adopt the ControlMaster lifecycle fixes from the gap sweep (§15.1–15.3): `ssh -O exit` on teardown instead of unlinking a live socket, and a per-attempt or health-checked ControlPath so reconnects can't attach to a half-dead master. *(IMPROVEMENTS #30, #31, #46; findings: no-recover; backoff signals; fd leak; restoreTerminal race; ControlMaster orphan cluster.)*

**WP8 · Transfer safety (M)** — The in-band PTY transport's sentinel matching fires on the PTY echo of its own command (the probe is currently a no-op and payload can stream before remote raw mode); failure sentinels are unparseable so every remote error surfaces as a generic 30s timeout; the overwrite gate mishandles directory destinations; `receive` writes directly to the final path so an interrupted get destroys the original it just asked permission to overwrite; remote temp files leak on every failure path; any path whose basename is `download` is unusable; CR/LF in filenames mangles SFTP batches; no `ConnectTimeout` on any sftp invocation (unreachable hosts hang for the OS TCP timeout). Re-establish deterministic coverage of this driver — it has had effectively zero tests since the flaky e2e was deleted (`4280600`). *(IMPROVEMENTS #32, #67; findings: 8 transfer findings + test gap.)*

**WP9 · Transcript durability (M)** — Fix the torn-tail cluster (§15.4): salvage-tolerant `read()` (the only **critical** finding — one torn line currently makes a whole recording unreadable), `writeFrame` truncate-back + fsync policy, export-side parse check so bundles stop certifying corrupt artifacts, and make `session prune` delete orphaned `.cast` files (recordings currently accumulate forever). *(Findings: torn-read critical; writeFrame; bundle poison; prune orphans .cast.)*

**WP10 · Contract stabilization (L)** — The five decisions in §4: public JSON projections + version envelope (stop emitting raw `SessionRecord`); read-side schema gating; exit-code collision fixes; replace the `SendEnv=SSHERPA_*` wildcard with enumerated lineage vars **and** add AcceptEnv-degradation detection + documentation (§15.5); version or document the `--__supervisor` IPC. Plus: document or remove `SSHERPA_SFTP_BINARY`, `SSHERPA_IGNORE_USER_GIT`, `SSHERPA_HOST_LABEL`, `recv`, `session transcripts`; make the theme parser tolerate unknown keys (today a theme.conf written by a future version hard-fails every older binary — forward-incompatible in exactly the way a frozen tool can't be); ship proper per-subcommand `--help` and delete the internal "Phase 10" paragraph from the global help. *(IMPROVEMENTS #22, #28, #88-adjacent; findings: contract cluster, ~12 items.)*

**WP11 · Release-integrity table stakes (M)** — Regenerate completions + man page against the frozen CLI and add a drift test (currently 7 of 12 `session` subcommands are missing from every completion file ssherpa ships); include LICENSE in archives/packages (MIT requires it); add `-trimpath` + `mod_timestamp` (reproducible builds — also: release binaries currently differ from what CI tested); gate `release.yml` on the test matrix; SHA-pin actions and scope the tap PAT; replace the `go mod tidy` release hook with `go mod verify`; disable the release-job cache restore; add artifact attestations (or cosign) + SBOM; bump bubbletea v2.0.6→v2.0.7 and `x/sys`→current (clears GO-2026-5024) *before* tagging the freeze release; harden backup file modes to 0600 (world-readable `authorized_keys.ssherpa-backup` copies today); add a security contact to SECURITY.md; declare deb/rpm `openssh-client` dependency. *(IMPROVEMENTS #40, #41, #93; findings: 15 release findings.)*

### 14.2 Should do before the lock (high leverage for the maintenance phase)

- **CI gates (M):** `go test -race` (measured ~45s), `govulncheck` + staticcheck jobs, a weekly `schedule:` trigger so CVEs surface without commits, `goreleaser check` + snapshot build on PRs, dependabot (gomod weekly grouped + github-actions), linux/arm64 test runner (free for public repos; half the shipped matrix is currently compile-only), and the timing-budget widenings the flake inventory identified. *(IMPROVEMENTS #42, #43, #49, #70, #71.)*
- **Governance pack (M):** issue templates requiring `ssherpa version` output/OS/terminal, PR template, `RELEASING.md` documenting the end-to-end release (including `TAP_GITHUB_TOKEN` scope/owner/rotation — bus factor 1 today), a written support/versioning/deprecation policy that makes SECURITY.md's "latest minor" pledge coherent with an actual cadence, a "Project status: maintenance" section in README/CONTRIBUTING, and a CHANGELOG going forward. *(IMPROVEMENTS #84 et al.)*
- **Editorial recommendation (this report's, not the triage agent's):** pull the `hostlist.Build` O(n²) fix forward from post-lock. It's a measured 1.1–4.5s stall at 5–10k hosts paid on *every* home-loop pass, the fix is a contained pattern-index, and "works with your real ssh config" is the headline promise. *(IMPROVEMENTS #2-adjacent; finding: hostlist O(n²), confirmed high.)*

### 14.3 Post-lock backlog

Everything else confirmed here plus the remaining `docs/IMPROVEMENTS.md` items: UX polish (typo'd-subcommand guard, per-screen paste, wide-char/CJK width, tiny terminals, picker MRU, `check` parallelism + stderr capture, 4× `ListRecords` render scans, 1-byte stdin copy loop, capability degradation), `doctor` command, asciinema demos, AUR/Nix packaging, and the unverified observations in §12. The IMPROVEMENTS.md file should be pruned (four items are already shipped — see 14.4) and re-labeled as the official post-lock backlog.

### 14.4 Already done (verified, prune from IMPROVEMENTS.md)

- #37 authkeys comment control-character rejection — `5448f8d` (but see WP4: options field still open).
- #18 destructive confirms default-No — `2daf46d` (but see WP4: two stragglers).
- #35 SECURITY.md — `f92c7e7` (but see WP11: no reporting channel).
- #29 stale session reaping — `83ee50d` (remote mirrors only; see WP6 for local records).

### 14.5 Explicitly rejected (declining is part of locking)

- **#90 Windows/ConPTY** — new platform, new PTY abstraction; incompatible with a freeze.
- **#82 Plugin/hook interface** — a new public contract is the opposite of freezing the contract.
- **#97 alternative transports** (wormhole et al.) — new transport, new attack surface.

Write these down as "considered and declined for 1.x" in CONTRIBUTING so they don't get re-litigated in issues during the maintenance phase.

---

## 15. Gap sweep — what the first 22 audit tracks missed

After the main audit, a completeness critic was asked what 22 tracks of auditors had *missed*. It named five areas; five gap-sweep agents investigated, several with live experiments (real `sshd` instances, torn-file fixtures, fd counting). This wave produced the audit's only critical finding and one of its most important compatibility discoveries — a good argument for keeping a "what did we miss" pass in any future audit.

### 15.1 ControlMaster lifecycle (2 confirmed high + 3 observations)

ssherpa injects `ControlMaster=auto` + `ControlPersist=10m`, but on session end it **unlinks the live socket instead of issuing `ssh -O exit`** — every supervised session orphans an authenticated ssh master process for up to 10 minutes. Confirmed consequence: the orphan holds the user's `LocalForward`/`RemoteForward` ports, so relaunching the same host within 10 minutes silently loses forwards. Observations (unverified): the injected `-o` flags silently override a user's own multiplexing config; `-S` via passthrough args can't reach ssh ahead of the destination; zero tests cover mux teardown.

### 15.2 The Ctrl-] hotkey collides with real software (2 confirmed high)

`0x1D` is hardcoded, unremappable, and not disableable short of `--direct` (which gives up supervision entirely) — it is also vim's jump-to-tag, telnet's escape, and emacs `C-]`. Worse, **three fast presses fire the panic rope and tear down the entire nested session chain with no confirmation** — a vim user hammering jump-to-tag can kill every session below them. The composer's default Ctrl-G interception (emacs keyboard-quit) is the same class. Recommended: `--overlay-key`/`--no-overlay` mirroring the existing `--composer-key`/`--no-composer` design, a confirm or hold on the panic-tap, and a documented conflicts table.

### 15.3 Reconnect supervision is inert for silent link death (1 confirmed high)

The reconnect feature only reacts to *child exit*. ssherpa injects no `ServerAliveInterval`/`ServerAliveCountMax`, so a silently dead link (NAT timeout, sleep/resume, network change) never triggers it; the opt-in latency watchdog is warn-only by default, and its force-kill path suppresses the reconnect it should cause. Respawn also reuses the same ControlPath, so a retry can attach to a half-dead persisted master (ties into 15.1). For a feature whose whole point is tunnels that stay up, this needs keepalive injection (or documented user-config guidance) before the freeze certifies it.

### 15.4 Transcript torn-tail cluster (1 confirmed **critical**, 2 confirmed high)

Empirically driven with fixtures: a `.cast` missing its last 5 bytes (crash, ENOSPC, power loss) makes **every** consumer hard-fail — `replay`, `log`, `grep`, `export`, bundle, the TUI browser — losing the entire recording rather than the last frame (`transcript.read()` aborts on any unparseable line; `bufio.Scanner` always surfaces the torn tail). The writer itself creates torn tails (single unsynced `Write` per frame, no truncate-back on error, no fsync on close). And bundle export does a raw `ReadFile` + SHA-256 of the corrupt bytes, so the "verified" portable bundle ships the poison to the recipient, where import "succeeds" and every viewer fails. Fixes are localized: salvage-tolerant read with a torn-tail warning, truncate-back + fsync policy in the writer, parse-before-export. Plus: `session prune` deletes only `<id>.json`, never `<id>.cast` — documented cleanup never reclaims transcript storage.

### 15.5 Nested lineage vs. stock sshd (1 confirmed high — verified against real sshd)

The deepest compatibility finding in the audit. All nested-session metadata (`SSHERPA_SESSION_ID/DEPTH/ROUTE/ORIGIN_HOST`) rides `-o SendEnv=SSHERPA_*`, and **no stock sshd accepts it** (`AcceptEnv` defaults: upstream none, Debian/Ubuntu only `LANG/LC_*`). Verified experimentally against OpenSSH 10.2 sshd in both configurations: on a default server the remote ssherpa believes it is a root session (no parent, depth 0), `incoming` markers lose all lineage fields, and remote-exported transcript bundles embed wrong route/depth. The OSC telemetry fallback repairs only the *local* map — masking the breakage from the operator — and mis-attributes chains deeper than two hops (grandchild recorded as child, intermediate hop vanishes; observed). The escape rope is unaffected (signal-based, not env-based). Recommended: detect the degradation (the telemetry-backfill branch firing *is* the detection signal) and surface it once per session; document a one-line server opt-in (`AcceptEnv SSHERPA_*`) with an honest table of what works without it; longer-term, move lineage in-band. Related observations from the same experiment: `--config` is inventory-only (launched ssh omits `-F`, so alternate-config aliases fail to resolve — the SFTP path *does* pass `-F`), and the RS-framed telemetry duplicate prints ~1.4 KB of visible base64 (containing the full session record) into any plain-ssh parent terminal.

---

*Generated by a multi-agent audit workflow (map → hunt → adversarial verify → triage → gap sweep), 422 agent runs, 2026-06-11. Findings cite `a112f3b`. Companion backlog: `docs/IMPROVEMENTS.md`.*
