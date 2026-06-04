# Contributing

Keep changes small, tested, and aligned with the existing command behavior.

## Compatibility Reference

The Bash Zoo implementation remains the behavior reference until the Go
port reaches parity:

- `bash-zoo/scripts/ssherpa.sh`
- `bash-zoo/docs/ssherpa.md`
- https://github.com/0xbenc/bash-zoo/blob/main/scripts/ssherpa.sh

Do not change command behavior casually. When Go behavior intentionally
differs from Bash behavior, document the reason in the relevant PR or
design note.

## Local Checks

Run these before committing:

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go run ./cmd/ssherpa version
go run ./cmd/ssherpa list --json --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa --print --select prod --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa add --alias smoke --host smoke.example.com --config internal/sshconfig/testdata/matrix/config --dry-run
go run ./cmd/ssherpa jump --dest prod --hop quoted --print --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa proxy --select prod --port 1080 --print --config internal/sshconfig/testdata/matrix/config
tmp="$(mktemp -d)"
auth="$tmp/authorized_keys"
go run ./cmd/ssherpa authkeys add --key "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example" --path "$auth" --yes
go run ./cmd/ssherpa authkeys list --json --path "$auth"
```

For release config smoke tests, install GoReleaser and run:

```sh
goreleaser check
goreleaser release --snapshot --clean
```

The current GoReleaser config has publishing disabled.

## Safety Rules

- Keep OpenSSH as the source of truth.
- Do not mutate user SSH config or `authorized_keys` without tests,
  backups, and dry-run behavior.
- Use temp HOME directories for destructive tests.
- Prefer parser-backed edits over string replacement.
- Keep the direct `ssh alias` runner as the default until supervised PTY
  behavior is proven.
