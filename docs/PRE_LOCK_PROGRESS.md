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
| Batch A — WP1 argv `--` guard (sshcmd) | pending |
| Batch A — WP4 authkeys (options validation, exact delete, confirm defaults) | pending |
| Batch A — WP5 sshconfig mutation round-trip integrity | pending |
| Batch A — WP11 release integrity (goreleaser/CI/completions/deps) | pending |
| Batch B — WP9 transcript durability (torn tail, fsync, export, prune .cast) | pending |
| Batch B — WP3 clamps (zip-bomb, replay sleep, atomic import) | pending |
| Batch B — WP6 state integrity (prune path, skip-bad-file, version gates, reaping) | pending |
| Batch B — WP2 termstyle (skipANSI/Strip CSI final bytes) | pending |
| Batch C — WP7 supervisor (recover, backoff signals, fd leak, ControlMaster exit) | pending |
| Batch C — WP8 transfer safety (in-band echo, overwrite gates, timeouts) | pending |
| Batch C — WP2 render sinks (OSC7, imported metadata, raw-replay confirm) | pending |
| Batch C — WP10 contract stabilization (JSON envelopes, exit codes, SendEnv) | pending |

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
