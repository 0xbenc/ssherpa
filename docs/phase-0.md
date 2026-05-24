# Phase 0 Verification

Phase 0 establishes the standalone Go project foundation. It does not
port SSH alias behavior yet.

## Deliverables

- `go.mod`
- `cmd/ssherpa/main.go`
- Internal CLI package
- `ssherpa version`
- Unit tests for current CLI behavior
- GitHub Actions CI for formatting, vet, test, smoke run, native build,
  and Linux/macOS cross-builds
- `README.md`
- `CONTRIBUTING.md`
- Draft `.goreleaser.yaml` with publishing disabled

## Acceptance Commands

```sh
go test ./...
go run ./cmd/ssherpa version
```

Additional local checks:

```sh
gofmt -l .
go vet ./...
go build -trimpath -o ssherpa ./cmd/ssherpa
```

Cross-build smoke commands:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/ssherpa_linux_amd64 ./cmd/ssherpa
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/ssherpa_linux_arm64 ./cmd/ssherpa
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o /tmp/ssherpa_darwin_amd64 ./cmd/ssherpa
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o /tmp/ssherpa_darwin_arm64 ./cmd/ssherpa
```

## Known Non-Goals

- No SSH config parsing yet.
- No Bubble Tea picker yet.
- No SSH process runner yet.
- No config or `authorized_keys` mutation yet.
- No packaging publication yet.

The Bash Zoo implementation remains the compatibility reference for the
next phases.
