# Contributing

`ssherpa` is currently in Phase 1 of the Go port. Keep changes small,
tested, and aligned with the compatibility contract in `PORT_PLAN.md`.

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
