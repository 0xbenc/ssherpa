# ssherpa

`ssherpa` is a Go port of the Bash Zoo SSH helper. The port keeps
OpenSSH config as the source of truth while growing toward a safer,
testable SSH workflow tool.

Current status: Phase 0 foundation. The repository has a Go module, a
tested CLI skeleton, `ssherpa version`, CI, contributor notes, and a
draft GoReleaser config with publishing disabled.

The implementation plan lives in [`PORT_PLAN.md`](PORT_PLAN.md).

## What Works Now

```sh
go run ./cmd/ssherpa version
go test ./...
go vet ./...
```

`ssherpa` without a command currently prints Phase 0 help. SSH alias
inventory, the picker, connection execution, config mutation, jump/proxy
flows, and `authorized_keys` management have not been ported yet.

## Compatibility Reference

Until the Go port reaches parity, the Bash Zoo implementation remains
the behavior reference:

- Local sibling checkout: `../bash-zoo/scripts/ssherpa.sh`
- Upstream source:
  https://github.com/0xbenc/bash-zoo/blob/main/scripts/ssherpa.sh
- Bash Zoo user docs:
  https://github.com/0xbenc/bash-zoo/blob/main/docs/ssherpa.md

## Development

This workspace uses an official user-local Go install rather than the
Ubuntu package:

- Go version: `go1.26.3`
- Install root: `~/.local/share/go/1.26.3`
- PATH symlinks: `~/.local/bin/go`, `~/.local/bin/gofmt`

See [`docs/development.md`](docs/development.md) for the bootstrap and
upgrade process, and [`CONTRIBUTING.md`](CONTRIBUTING.md) for the local
checks expected before commit.
