# ssherpa

`ssherpa` is a Go port of the Bash Zoo SSH helper. The port keeps
OpenSSH config as the source of truth while growing toward a safer,
testable SSH workflow tool.

Current status: Phase 3 config mutation. The repository has a Go module,
tested SSH config inventory, `ssherpa list`, `ssherpa show`, a Bubble Tea
alias picker, print mode, direct SSH execution, safe add/edit/delete
config mutations, CI, contributor notes, and a draft GoReleaser config
with publishing disabled.

The implementation plan lives in [`PORT_PLAN.md`](PORT_PLAN.md).

## What Works Now

```sh
go run ./cmd/ssherpa version
go run ./cmd/ssherpa list --json
go run ./cmd/ssherpa show ALIAS --json
go run ./cmd/ssherpa --print --select ALIAS
go run ./cmd/ssherpa add --alias ALIAS --host HOST --dry-run
go run ./cmd/ssherpa edit set ALIAS --host HOST --dry-run
go run ./cmd/ssherpa edit delete ALIAS --dry-run
go test ./...
go vet ./...
```

`ssherpa` without a command opens the Bubble Tea alias picker and runs
the selected alias with local OpenSSH. SSH config inventory supports
`Include`, source positions, duplicate warnings, wildcard hiding,
git-user hiding, and basic parsed effective values.

Config mutation uses dry-run diffs, backups for writes to existing
files, temp-file atomic renames, permission preservation, and safeguards
for multi-alias or wildcard `Host` stanzas. Jump/proxy flows and
`authorized_keys` management have not been ported yet.

## Examples

```sh
ssherpa list --json
ssherpa list --json --config ~/.ssh/config.work
ssherpa list --all --filter prod --user alice
ssherpa show prod --json
ssherpa --print --select prod -- -L 8080:localhost:8080
ssherpa --select prod --ssh-binary /tmp/fake-ssh
ssherpa add --alias prod --host prod.example.com --user alice --dry-run
ssherpa add --alias prod --host prod.example.com --user alice --yes
ssherpa edit set prod --port 2222 --identity ~/.ssh/prod --yes
ssherpa edit delete prod --all-sources --dry-run
ssherpa edit delete-all --filter scratch --dry-run
```

Default inventory reads `~/.ssh/config`. Use `--config PATH` for a
different root.

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
