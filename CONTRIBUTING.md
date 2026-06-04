# Contributing

Keep changes small, tested, and aligned with the existing command behavior.

Make a pull request into main when you are done creating and testing.

## Local Checks

Run these before committing:

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
```

For release config smoke tests, install GoReleaser and run:

```sh
goreleaser check
goreleaser release --snapshot --clean
```

## Safety Rules

- Keep OpenSSH as the source of truth.
- Do not mutate user SSH config or `authorized_keys` without tests,
  backups, and dry-run behavior.
- Use temp HOME directories for destructive tests.
- Prefer parser-backed edits over string replacement.
- Keep the direct `ssh alias` runner as the default until supervised PTY
  behavior is proven.
