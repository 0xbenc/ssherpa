# Development Notes

## Go Toolchain

Use the official Go tarball instead of the Ubuntu package. Ubuntu
packages can lag the upstream release, and this project will build and
test release artifacts across platforms.

This workspace was bootstrapped on 2026-05-24 with:

- OS: Ubuntu 24.04, Linux x86_64
- Go: `go1.26.3`
- Archive: `go1.26.3.linux-amd64.tar.gz`
- SHA256:
  `2b2cfc7148493da5e73981bffbf3353af381d5f93e789c82c79aff64962eb556`
- Install root: `~/.local/share/go/1.26.3`
- PATH symlinks: `~/.local/bin/go`, `~/.local/bin/gofmt`

Official references:

- Latest version endpoint: https://go.dev/VERSION?m=text
- Release metadata: https://go.dev/dl/?mode=json
- Install guide: https://go.dev/doc/install

### Install Or Upgrade

Check the current official version:

```sh
curl -fsSL 'https://go.dev/VERSION?m=text'
```

Fetch release metadata and choose the archive for the host OS and
architecture:

```sh
curl -fsSL 'https://go.dev/dl/?mode=json'
```

Download the archive:

```sh
curl -fL -o /tmp/go1.26.3.linux-amd64.tar.gz \
  https://go.dev/dl/go1.26.3.linux-amd64.tar.gz
```

Verify the checksum:

```sh
sha256sum /tmp/go1.26.3.linux-amd64.tar.gz
```

Extract to a versioned user-local directory:

```sh
mkdir -p ~/.local/share/go/1.26.3
tar -C ~/.local/share/go/1.26.3 --strip-components=1 \
  -xzf /tmp/go1.26.3.linux-amd64.tar.gz
```

Expose the toolchain on PATH:

```sh
ln -s ~/.local/share/go/1.26.3/bin/go ~/.local/bin/go
ln -s ~/.local/share/go/1.26.3/bin/gofmt ~/.local/bin/gofmt
```

If replacing an existing symlink during an upgrade, remove or update the
old symlink first. Do not overwrite an unrelated binary in
`~/.local/bin`.

Verify:

```sh
go version
printf 'package main\n' | gofmt
go env GOROOT GOPATH GOMODCACHE
```

Expected workspace result:

```text
go version go1.26.3 linux/amd64
GOROOT=/home/xbenc/.local/share/go/1.26.3
GOPATH=/home/xbenc/go
GOMODCACHE=/home/xbenc/go/pkg/mod
```

If a shell already had a stale command cache, run `hash -r` for bash or
`rehash` for zsh.

## Local Checks

Run the same baseline checks as CI:

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
go run ./cmd/ssherpa authkeys add \
  --key "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example" \
  --path "$auth" --yes
go run ./cmd/ssherpa authkeys list --json --path "$auth"
go build -trimpath -o ssherpa ./cmd/ssherpa
```

The native build output `ssherpa` is ignored by git.

The CI workflow also cross-builds these release targets with
`CGO_ENABLED=0`:

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`

## Release Draft

`.goreleaser.yaml` is present as a draft release configuration. It
builds Linux and macOS archives and injects version metadata with linker
flags. Publishing is disabled with `release.disable: true`.

When GoReleaser is installed, validate the config with:

```sh
goreleaser check
goreleaser release --snapshot --clean
```

The config uses GoReleaser v2 syntax and the public schema at
https://goreleaser.com/static/schema.json.

## Compatibility Reference

The Bash Zoo implementation is the behavior reference for the port:

- Local sibling checkout: `../bash-zoo/scripts/ssherpa.sh`
- Source: https://github.com/0xbenc/bash-zoo/blob/main/scripts/ssherpa.sh
- Docs: https://github.com/0xbenc/bash-zoo/blob/main/docs/ssherpa.md
