# Phase 6: Supervised PTY Sessions

Phase 6 added the session supervisor for SSH commands. Phase 7 promotes
the supervised PTY runner to the default for connect, jump, and proxy
flows. Use `--direct` for the old unsupervised runner.

## Commands

Run a direct alias through the supervised PTY runner:

```sh
ssherpa --select prod
```

Run a jump route or SOCKS proxy through the same runner:

```sh
ssherpa jump --dest prod --hop bastion
ssherpa proxy --select prod --port 1080
```

Inspect session state:

```sh
ssherpa session list
ssherpa session list --json
ssherpa session show SESSION_ID
ssherpa session show SESSION_ID --json
ssherpa session prune --older-than 168h --dry-run
```

## State Directory

Session records are local JSON files:

```text
Linux:  ~/.local/state/ssherpa/sessions/*.json
macOS:  ~/Library/Application Support/ssherpa/sessions/*.json
```

Override the directory with:

```sh
SSHERPA_STATE_DIR=/tmp/ssherpa-state ssherpa --select prod
ssherpa --state-dir /tmp/ssherpa-state --select prod
ssherpa session list --state-dir /tmp/ssherpa-state
```

Records are written with mode `0600`. Parent directories are created
with mode `0700`.

## Recorded Fields

Each record captures:

- session ID;
- parent session ID when the local process is launched from an existing
  supervised session;
- depth;
- route and hops;
- target alias;
- exact SSH argv;
- start/end timestamps;
- local process PID and SSH process PID;
- exit code;
- runner mode;
- state schema version.

The child process receives:

```text
SSHERPA_SESSION_ID
SSHERPA_PARENT_SESSION_ID
SSHERPA_DEPTH
SSHERPA_ROUTE
```

OpenSSH server-side `AcceptEnv` policy decides whether that metadata
survives to a remote login shell. Local nested invocations record parent
and depth reliably.

## Terminal Behavior

Supervised mode starts SSH under a PTY, puts the local terminal in raw
mode only when stdin is a terminal, forwards resize and common interrupt
signals, and restores the terminal on exit. Non-TTY stdin, such as CI or
test input, skips raw mode.

As of Phase 7, sessions also intercept `Ctrl-]` locally to open the
session map overlay. That byte is not sent to the remote PTY when the
overlay opens.

## Verification

Run the unit and integration suite:

```sh
go test ./...
go vet ./...
go build -trimpath -o ssherpa ./cmd/ssherpa
```

Use a disposable fake SSH binary for local smoke testing:

```sh
tmp="$(mktemp -d)"
cat > "$tmp/fake-ssh" <<'EOF'
#!/bin/sh
printf 'fake supervised ssh: %s\n' "$*"
exit 0
EOF
chmod +x "$tmp/fake-ssh"

go run ./cmd/ssherpa \
  --state-dir "$tmp/state" \
  --ssh-binary "$tmp/fake-ssh" \
  --select prod \
  --config internal/sshconfig/testdata/matrix/config

go run ./cmd/ssherpa session list --state-dir "$tmp/state"
go run ./cmd/ssherpa session list --json --state-dir "$tmp/state"
```

## Known Limits

- Latency probing, auto-disconnect, and queued input composer behavior
  are intentionally deferred to later phases.
- The session list is a local record view, not a remote process manager.
- `session prune` removes ended records only; active records are never
  pruned by age.
