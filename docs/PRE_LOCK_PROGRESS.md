# Pre-lock hardening — progress tracker

Working branch: `pre-lock`. Companion documents: `docs/STABILITY_AUDIT.md`
(the full audit; work packages cited below are its §14.1) and
`docs/IMPROVEMENTS.md` (post-lock backlog).

This file exists so progress survives interrupted sessions. Update it with
every commit on this branch.

## Decisions (maintainer-confirmed)

- **Overlay hotkey:** triple-mash panic rope is beloved and stays exactly
  as-is. Default key moved `Ctrl-]` → `Ctrl-^` (0x1E; mosh-precedent freest
  key) with a new `--overlay-key` flag to rebind. (Done — see below.)
- **hostlist O(n²):** no preference on sequencing; fix when convenient.
- **Reconnect / silent link death:** document the limitation (point users at
  `ServerAliveInterval`); do not inject keepalives.
- **Versioning/cadence policy:** deferred until release cadence slows.
- Explicitly rejected for 1.x: Windows/ConPTY, plugin interface, new
  transports.

## Status

| Item | Status |
| --- | --- |
| Overlay hotkey → Ctrl-^ + `--overlay-key` | **done** |
| Reconnect limitation docs | **done** |
| Batch A — WP1 argv guard | **done** (9221070) — dash aliases filtered from inventory with warning; ValidateDestination at route layer incl. tampered catalogs; `--` kept only for sftp/probe; passthrough preserved and pinned by tests |
| Batch A — WP4 authkeys | **done** (a5ba07f) — incl. review fixes: C1 range, dry-run preview gate, --all-matching docs |
| Batch A — WP5 sshconfig | **done** (281b0e6) — incl. review fixes: multi-casing delete refusal, resolved-casing plan.Aliases, plan.Warnings printed |
| Batch A — WP11 release | **done** (1525cc7) — incl. review fixes: SBOM+syft, macos release gate, dependabot direct-only. Completions/man regen + drift test DEFERRED to final pass after Batch C (CLI surface still moving) |
| Batch B — WP9/WP3 transcript | **done** (d4260d4) — salvage read + ErrTornTail, writer truncate-back+fsync+stop marker, bundle parse-before-export + zip caps + atomic import, replay clamp, full ECMA-48 Clean |
| Batch B — WP6 state | **done** (1d919df) — prune-by-filename + .cast/.log + orphan sweep, tolerant listings + Detailed variants, ErrFutureStateVersion gates, crashed-local reaping, finalization-preserving WriteRecord, cleanup writes back to read file |
| Batch B — WP2 termstyle | **done** (35ea824) — ECMA-48 CSI finals + embedded C0, non-CSI grammar, SS3 whole-rune, ANSI-aware Truncate |
| Batch C handoffs from B (do in Batch C) | torn-tail consumer adoption (cli/session.go:1771, followTranscript, sessionview:1029), cli raw-replay delay clamp, bundle Warning printing, prune Artifacts + cleanup LocalSessions + Skipped printing, Detailed-listing warnings, TranscriptSpec stop_reason field + recorder wiring |
| Batch C — WP7 supervisor | **done** (dec8ddc) — panic-safe terminal restore, lifetime signal handling incl. backoff, fd leak + regression test, ControlMaster `-O exit` + XDG socket dir, OSC7/telemetry sanitize + clamps, AcceptEnv degradation event, transcript stop_reason |
| Batch C — WP8 transfer | **done** (23541ea + 7a233e5) — echo-proof sentinels with deterministic driver tests, parseable failures, temp+rename receive with mode preservation, overwrite gates, ConnectTimeout, SendEnv enumeration, ErrNoCompletion |
| Batch C — WP2 render sinks | **done** (5b9a761 + 224b8e7) — termstyle.Sanitize (ESC + C0/C1) at every sink, torn-tail adoption (CRITICAL-01 closed end-to-end), raw-replay default-No guard + clamp, wrapConfirmText newlines, Batch B surfacing |
| Batch C — WP10 contract | **done** (f413575 + 224b8e7) — per-command help, exit-code unification (3 gone, grep(1) convention), schema_version envelopes on list/show/check/session JSON with public projections, check stderr capture, theme forward-compat, full docs/man refresh, completions regen + drift test (ce48eea) |

## Notes for future sessions

- Audit working data was staged in `/tmp/ssherpa-audit/` (ephemeral). The
  canonical findings live in `docs/STABILITY_AUDIT.md` §12; per-finding
  evidence and suggested fixes are inline there.
- An fd-leak regression test from the audit is stashed at
  `/tmp/ssherpa-audit/fdleak_audit_test.go.stash` — reintroduce (cleaned)
  with the WP7 fix; if the stash is gone, the reproduction recipe is in
  audit finding HIGH (session fd leak): 20 reconnect attempts → 20 leaked
  PTY master fds via the fast-path return in `attemptOnce`.
- Batches are file-disjoint to allow parallel subagents: A touches
  sshcmd/authkeys/sshconfig/release-config; B touches
  transcript/state/termstyle; C touches session/inband/cli render+contract.

## Maintainer manual actions (cannot be done in-repo)

- Enable GitHub private vulnerability reporting (Settings → Security) so the
  new SECURITY.md channel works.
- Replace `TAP_GITHUB_TOKEN` with a fine-grained PAT scoped to contents:write
  on 0xbenc/homebrew-tap only; document owner/expiry/rotation in RELEASING.md.
- Apple Developer signing/notarization for the Homebrew cask (Gatekeeper).
- goreleaser version pin (v2.16.0 in both workflows) is NOT bumped by
  dependabot — bump manually on goreleaser releases.

## Deferred minors (post-lock or later batch)

- ui.wrapConfirmText collapses explicit newlines (multi-entry authkeys delete
  confirm renders as one paragraph) — fix with Batch C ui work.
- add-flow records picker result under typed casing, not plan-resolved casing
  (cosmetic).
- Include-at-end-of-stanza scope-change warning unevenness (sshconfig).
- splitFields backslash-in-quotes parity with OpenSSH argv_split (tokenizer
  dialect finding, §12.2).

## Final state (2026-06-12)

All five tracked items plus both review-fix rounds are complete; the full
suite passes `go test ./... -race -count=1`. Every audit must-do work
package (WP1–WP11) landed.

Explicit descopes / known residuals (decided, not forgotten):
- Reconnect reuses the per-record ControlPath; a retry after silent link
  death could attach to a half-dead user-config master. Only reachable
  with user-config ControlMaster on tunnels (ssherpa's own master applies
  to interactive sessions only); revisit post-lock.
- SIGINT during the reconnect backoff window still exits without
  finalizing (intercepting it would swallow Ctrl-C meant for the remote);
  SIGTERM/SIGHUP/SIGQUIT are handled.
- `--__supervisor` IPC carries no version token; documented as internal
  same-version-only protocol instead.
- PID-reuse identity checks are not portably solvable stdlib-only; TTL
  reaping bounds the risk (documented in state.go).
- fish completion syntax was not machine-checked locally (fish not
  installed); run `fish --no-execute completions/ssherpa.fish` once.
