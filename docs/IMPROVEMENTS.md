# ssherpa — 99 Potential Improvements

A curated, codebase-specific backlog. Each item is concrete (names real files/packages where relevant), justified, and scoped to be genuinely valuable rather than busywork. Categories: **UX**, **Reliability**, **Security**, **Performance**, **Testing**, **Architecture**, **Docs**, **Distribution**, **Observability**, **Features**.

> **Status (2026-06-12, pre-1.x-freeze hardening on branch `pre-lock`; see
> `docs/STABILITY_AUDIT.md` for the audit and `docs/PRE_LOCK_PROGRESS.md` for
> the work log):**
>
> - **Done by the pre-lock hardening:** #17 (list empty-state hint), #18, #22
>   (partial: exit-code collisions fixed, grep(1) convention adopted; full
>   error taxonomy still open), #28 (read-side version gates), #29 (plus
>   crashed-local reaping), #30, #31, #32, #35, #36 (partial: backup modes
>   hardened to the effective mode), #37 (plus the options field), #40
>   (artifact attestations), #41 (SBOM), #42, #43 (partial: govulncheck
>   weekly; gosec not adopted), #46 (control-socket dir hardening), #49
>   (dependabot), #60 (mutation fuzz target), #66 (round-trip property
>   tests), #70 (partial: race detector in CI; coverage gate still open),
>   #71, #88 (partial: completions drift test; generation remains manual).
> - **Explicitly declined for 1.x** (re-litigate only at a 2.0 boundary):
>   #82 (plugin interface), #90 (Windows/ConPTY), and new transports.
> - Everything else is the post-lock maintenance backlog. Bugfixes and
>   user-requested improvements stay welcome; large new surface is frozen.

| # | Category | Improvement | Why it matters / Concrete approach |
|---|----------|-------------|-------------------------------------|
| 1 | UX | `ssherpa doctor` health command | Single command that audits `~/.ssh/config` perms, missing `IdentityFile` paths, unreachable `HostName`s, dangling `Include`s, and a too-permissive `authorized_keys`. Reuse `sshconfig.Load` diagnostics + `check.ProbeSSH`. |
| 2 | UX | Most-recently-used aliases float to top of picker | Track last-connect timestamps in state; sort `hostlist.List` output by recency as a secondary key so the picker surfaces what you actually use. |
| 3 | UX | Show `ssh -G <alias>` resolved config preview before connecting | A confirm pane that displays the fully-resolved effective config so users catch surprises (wrong `User`, unexpected `ProxyJump`) before the session opens. |
| 4 | UX | Connection history line in picker detail | "Last connected 5m ago · 142 sessions" rendered from `state` records gives instant context without leaving the TUI. |
| 5 | UX | Built-in theme presets (dark/light/solarized/high-contrast) | Ship 3–4 curated palettes in `termstyle/theme.go` selectable with one keypress in the theme editor, instead of only freeform color editing. |
| 6 | UX | Session breadcrumb in overlay title | Render the lineage path `prod → bastion → db` from `sessionview` lineage data in the overlay header, not just a flat active-session list. |
| 7 | UX | Escape rope hold-to-confirm with visual countdown | Require a brief hold (or second chord) with an on-screen countdown before the SIGHUP cascade fires, preventing accidental teardown of nested sessions. |
| 8 | UX | Duplicate/shadow-alias warning on add | When `add_form.go` writes an alias whose pattern is shadowed by an earlier `Host *` block, warn that it may never match (first-match-wins). |
| 9 | UX | `IdentityFile` existence check in add/edit forms | Validate the key path exists (and warn on wrong perms) before writing the config block, surfacing the error inline in `add_form.go`. |
| 10 | UX | Search aliases by hostname/IP | `ssherpa search 10.0.0.5` finds every alias resolving to that host — invaluable in large configs with hundreds of entries. |
| 11 | UX | Forward/proxy preset library with named ports | Pre-seed common service ports (5432 postgres, 3306 mysql, 6379 redis, 8080 http) in the forward builder so users pick a service, not a raw port. |
| 12 | UX | Per-hop reachability test in jump builder | In `route.go`'s jump builder, probe each hop (`BatchMode`) and mark broken hops red before the user commits the chain. |
| 13 | UX | In-transfer progress (bytes/percent) for in-band | Stream progress during base64 transfer in `inband` — currently large transfers look frozen. Emit byte counters as the payload chunks flow. |
| 14 | UX | Dry-run diff coloring for config mutations | Render `sshconfig/mutate.go` dry-run output as a proper red/green unified diff in `termstyle`, not plain text. |
| 15 | UX | `NO_COLOR` / dumb-terminal graceful degradation | Honor `NO_COLOR` and `TERM=dumb` across `termstyle` and all TUI components so output stays readable in logs and CI. |
| 16 | UX | Keystroke shorthands for common flags | Add `-p` (print), `-s` (select), `-j` (jump) short aliases alongside the long flags in `cli.go` for faster invocation. |
| 17 | UX | Empty-state guidance | When `~/.ssh/config` has zero hosts, the picker shows an actionable "Press a to add your first host" instead of an empty list. |
| 18 | UX | Confirm-dialog default-to-safe | Ensure destructive confirms in `confirm.go` default the cursor to "No" so a stray Enter never deletes an alias or key. |
| 19 | UX | Inline fuzzy-match highlighting | Highlight matched substrings in picker results so users see *why* an item matched their query. |
| 20 | UX | Session resume offer on exit | After a supervised session ends, offer to reconnect using the last-known cwd (`osc_tracker` data) for a tmux-like resume feel. |
| 21 | Reliability | Custom error taxonomy | Define `ErrAliasNotFound`, `ErrConfigParse`, `ErrTransferFailed`, etc. so callers branch on type (`errors.Is`) and exit codes become meaningful. |
| 22 | Reliability | Stable, documented exit codes | Map each error class to a distinct exit code (2=usage, 3=config, 4=transfer, …) and document them; scripts wrapping ssherpa need this. |
| 23 | Reliability | Circular `Include` detection test + clear error | Add an explicit test and a user-facing "include cycle: a→b→a" message in `sshconfig.Load` rather than infinite recursion or stack overflow. |
| 24 | Reliability | ProxyJump cycle rejection | Reject `A → B → A` jump chains in `route.go` with a clear message before spawning ssh. |
| 25 | Reliability | Atomic-write disk-full / ENOSPC handling | In `fsutil/write.go`, ensure a failed temp write never deletes the original and surfaces "disk full" clearly; add an error-injection test. |
| 26 | Reliability | Backup retention cap | Limit timestamped backups of config/`authorized_keys` to the N most recent (configurable) to avoid unbounded growth in `~/.ssh`. |
| 27 | Reliability | External-change detection before mutate | Store a hash of the last-written config; if the file changed out-of-band since, warn before overwriting so concurrent edits aren't clobbered. |
| 28 | Reliability | Versioned state schema + migrations | Add `SchemaVersion` to `state` records and a migration path so future format changes don't orphan old sessions. |
| 29 | Reliability | Stale-session reaping | Auto-prune session records whose PIDs are dead and older than a TTL on startup, so `session list` reflects reality. |
| 30 | Reliability | Robust terminal restore on panic | Guarantee raw-mode restoration in `session.go` via a top-level recover + `defer`, so a panic never leaves the user's terminal wedged. |
| 31 | Reliability | Signal-during-PTY-setup race fix | Handle SIGTERM/SIGINT that arrives *between* PTY spawn and signal-handler install, preventing a zombie ssh child. |
| 32 | Reliability | In-band transfer integrity on partial failure | Ensure a SHA256 mismatch in `inband` never commits a partial file to the destination path (write to temp, verify, then rename remotely). |
| 33 | Reliability | Window-resize (SIGWINCH) propagation | Confirm terminal resizes propagate to the PTY so full-screen remote apps (vim/htop) reflow correctly; add a regression test. |
| 34 | Reliability | Idempotent authkeys operations | Make `authkeys` add/merge no-ops when the exact key already exists (match by fingerprint), reporting "already present" rather than duplicating. |
| 35 | Security | `SECURITY.md` with disclosure policy | Add a vulnerability-reporting policy and supported-versions table — table stakes for a security-adjacent tool. |
| 36 | Security | Config/key permission warnings | Warn when `~/.ssh/config` or `authorized_keys` is group/world-writable or readable, mirroring OpenSSH's own strictness. |
| 37 | Security | Reject newlines/control chars in authkeys comments | Validate key comments in `authkeys` so a crafted comment can't inject a second line into `authorized_keys`. |
| 38 | Security | Directory-traversal guard in SFTP batch paths | Reject `..` and absolute-escape paths in `sshcmd` SFTP batch generation to prevent writing outside the intended target. |
| 39 | Security | Enforce `--max-bytes` early in in-band transfer | Check payload size before encoding in `inband`, rejecting oversized transfers up front rather than after streaming. |
| 40 | Security | GPG/cosign-sign release artifacts | Sign archives + `checksums.txt` in `release.yml` for verifiable provenance. |
| 41 | Security | SBOM in releases | Generate a CycloneDX SBOM during GoReleaser so downstream consumers can audit the (small) dependency tree. |
| 42 | Security | govulncheck in CI | Add `govulncheck ./...` to `ci.yml` to catch known vulnerabilities in deps and the toolchain on every PR. |
| 43 | Security | gosec / static security linting | Run `gosec` in CI to catch insecure temp-file, perms, and exec patterns before merge. |
| 44 | Security | Remote sshd version probe + warning | During `check`, surface the remote OpenSSH version and warn on known-EOL releases. |
| 45 | Security | Escape rope audit entry | Append an immutable audit line (host, lineage, time) to state when the rope is pulled, for after-incident review. |
| 46 | Security | Secure temp-file creation review | Audit every temp path (`fsutil`, `inband`, SFTP batch) to ensure `O_EXCL`/`0600` creation and no predictable names — add tests. |
| 47 | Security | Scrub session IDs from user-facing errors | Avoid leaking internal session identifiers into stderr/logs where they could aid correlation attacks. |
| 48 | Security | Optional fingerprint confirmation on first connect | When connecting to an alias whose host key isn't yet known, surface the fingerprint clearly in the TUI before proceeding. |
| 49 | Security | Dependabot / renovate for Go modules | Automate dependency-update PRs so security fixes in bubbletea/creack-pty land promptly. |
| 50 | Performance | Parsed-config cache with mtime invalidation | Cache the parsed `sshconfig.Graph` under state with an mtime check, skipping re-parse of large multi-include configs on every invocation. |
| 51 | Performance | Concurrent host probes in `check` | Fan out reachability/RTT probes across hosts with a bounded worker pool instead of sequentially — large fleets become usable. |
| 52 | Performance | Append-only session event log | Avoid rewriting the whole `state` record on every event; append events and compact periodically. |
| 53 | Performance | Compress payloads before base64 (in-band) | gzip the payload before base64 in `inband` for slow links — base64 inflates 33%, gzip often more than offsets it for text. |
| 54 | Performance | Reuse `ControlMaster` sockets | Detect/offer `ControlMaster`+`ControlPersist` so repeated ssherpa invocations to the same host skip re-auth latency. |
| 55 | Performance | Lazy `Include` parsing | Parse included files on first access rather than eagerly, so configs with many conditional includes load faster. |
| 56 | Performance | Lazy forward/proxy catalog loading | Defer loading saved-forward/proxy catalogs until the relevant menu opens, trimming cold-start cost. |
| 57 | Performance | PTY read-buffer sizing | Tune the PTY copy buffer in `session.go` to terminal dimensions to reduce syscalls under heavy output (e.g. `cat` of a big file). |
| 58 | Performance | Picker render memoization | Cache the rendered list between keystrokes when the underlying inventory is unchanged, re-filtering only the visible window. |
| 59 | Performance | Watchdog result coalescing | Batch latency-watchdog probe results into a single state write per interval rather than one per probe. |
| 60 | Testing | Fuzz `sshconfig.Load` | Add a Go fuzz target feeding random/malformed config text to harden the parser against crashes and pathological includes. |
| 61 | Testing | Dockerized end-to-end suite | A CI-only container running real `sshd` so connect/transfer/forward/jump flows are exercised against an actual server without requiring local developer Docker usage. |
| 62 | Testing | PTY replay fixtures | Record real PTY byte streams and replay them to test `osc_tracker` (OSC 7/133) and overlay behavior deterministically. |
| 63 | Testing | Error-injection framework for I/O | Inject ENOSPC, EACCES, and partial writes into `fsutil`/`state` to verify atomic-write and backup guarantees hold. |
| 64 | Testing | Snapshot tests for TUI render output | Golden-file the rendered output of `picker`, `sessionview`, and `confirm` to catch visual regressions; raises `ui` coverage above ~45%. |
| 65 | Testing | Signal-injection lifecycle tests | Deliver SIGTERM/SIGHUP/SIGWINCH mid-session and assert clean teardown and terminal restore in `session`. |
| 66 | Testing | Property-based tests for alias round-trips | Generate random valid `Host` blocks, write then re-parse, and assert structural equality to find mutation edge cases. |
| 67 | Testing | Transfer failure-mode tests | Simulate SFTP timeout, corrupted base64, and mid-stream drop; assert no partial/corrupt destination file results. |
| 68 | Testing | Concurrent-process state race tests | Run two ssherpa processes mutating state simultaneously to surface file-locking gaps in `state`. |
| 69 | Testing | Terminal-capability matrix tests | Mock `TERM`, `NO_COLOR`, and width to verify graceful degradation across dumb/256/truecolor terminals. |
| 70 | Testing | Coverage gate in CI | Fail CI if total coverage drops below a threshold, with per-package floors; target lifting `cli` (~43%) and `ui` (~45%). |
| 71 | Testing | Race detector in CI | Run `go test -race ./...` (esp. `session`, `daemon`, `state`) to catch the concurrency bugs PTY/daemon code is prone to. |
| 72 | Testing | Escape rope edge cases | Test rope behavior under nested zsh/fish and `nohup` layers where signal delivery differs. (`tmux` addressed by the muxer guard — `internal/session/muxer_guard.go`; `screen` is detect-only.) |
| 73 | Testing | Performance regression guard | Benchmark large-config parse time and alert if it regresses >2× between commits. |
| 74 | Architecture | Split monolithic `cli.go` (1410 LOC) | Separate command dispatch from interactive flows; move each command group into its own file/handler to improve testability of `cli` (currently ~43%). |
| 75 | Architecture | Decompose `session.go` (1958 LOC) | PTY plumbing, overlay, input composer, watchdog, and escape rope are five concerns in one file; extract into focused units. |
| 76 | Architecture | Extract a flag-parsing package | The repeated `inventoryFlags`/`transferFlags` patterns + `nextArg`/`hasHelpFlag` helpers belong in one reusable parser to kill duplication. |
| 77 | Architecture | Thread `context.Context` everywhere | Replace ad-hoc timeouts with `context.Context` through `check`, `transfer`, and `connect` for unified cancellation/timeout. |
| 78 | Architecture | Dependency injection for globals | Pass logger, config resolver, and state manager as interfaces instead of package globals (e.g. `daemonStartProcess`) for cleaner tests. |
| 79 | Architecture | Mode enum instead of boolean flags | Replace `--supervise`/`--direct`/`--print` booleans with a single mode enum to make illegal combinations unrepresentable. |
| 80 | Architecture | Unify session metadata structs | Consolidate `SessionRecord`/`ForwardSpec`/`ProxySpec` shared fields to reduce the metadata-passing sprawl across packages. |
| 81 | Architecture | Structured logging facade | Introduce a leveled, structured logger (slog) with a `--verbose`/`--debug` flag, replacing scattered direct stderr prints. |
| 82 | Architecture | Plugin/hook interface | Define a documented contract for user scripts on session start/end (logging, Slack, audit) — a small, safe extension point. |
| 83 | Architecture | Centralize XDG path resolution | One module resolves state/config/cache dirs across Linux + macOS, removing per-package platform branches. |
| 84 | Docs | Architecture Decision Records | Capture key decisions (OpenSSH as source of truth, local-only supervision, three transports) as ADRs for future contributors. |
| 85 | Docs | Troubleshooting guide | Cover permission-denied, rope-not-firing, SFTP-unreachable, and terminal-wedged scenarios with fixes. |
| 86 | Docs | Migration guide from the bash version | Map old flags/behaviors to the Go rewrite so existing users transition without surprises. |
| 87 | Docs | State + config-graph schema reference | Document the JSON shapes in `state`/`sshconfig` as a semi-stable internal API others can build on. |
| 88 | Docs | Auto-generate man page + completions from CLI spec | Derive `man/ssherpa.1` and the bash/fish/zsh completions from a single command spec so they never drift from `cli.go`. |
| 89 | Docs | Asciinema demos in README | Embed short recorded casts of picker → connect → escape rope and an in-band transfer; far more persuasive than prose. |
| 90 | Distribution | Windows support (ConPTY) | Abstract the PTY layer to support Windows ConPTY (or document WSL-only), then ship a Windows binary — meaningfully widens the audience. |
| 91 | Distribution | Nix flake | Add `flake.nix` so Nix users get a reproducible install and dev shell. |
| 92 | Distribution | AUR package | Publish to the Arch User Repository (`ssherpa-bin` + source) to reach Arch users. |
| 93 | Distribution | Reproducible builds | Pin toolchain, set `-trimpath`/`-buildvcs`, and document how to reproduce release binaries bit-for-bit. |
| 94 | Distribution | `ssherpa upgrade` self-update (opt-in) | Check the GitHub release feed and self-update for users who didn't install via a package manager — with signature verification. |
| 95 | Observability | `--json` output for all read commands | Ensure `list`, `session`, `check`, and catalog commands emit stable JSON for scripting and dashboards (extend existing `list --json`). |
| 96 | Observability | Latency SLA tracking per alias | Persist p50/p95/p99 RTT over time from the watchdog and expose a `session stats` view to spot degrading links. |
| 97 | Features | First-class wormhole transport | Implement a magic-wormhole off-band transport for secure transfers when neither SFTP nor in-band fits. |
| 98 | Features | Multi-file / multi-port batch operations | `send file1 file2 …` and forward builders that open several local ports in one command, reducing repetitive invocations. |
| 99 | Features | Saved multi-hop route aliases | Let users name and re-run a full jump chain (`prod-db = laptop→bastion→db`) instead of rebuilding hops each time. |

---

_Generated as a planning artifact; now the official post-lock backlog. Items are independent and individually shippable; prioritize by impact (security + reliability first, then UX, then breadth)._
