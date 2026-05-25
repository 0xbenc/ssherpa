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
cat > "$tmp/fake-ssh" <<'EOF'
#!/bin/sh
printf 'fake supervised ssh: %s\n' "$*"
exit 0
EOF
chmod +x "$tmp/fake-ssh"
go run ./cmd/ssherpa --state-dir "$tmp/state" --ssh-binary "$tmp/fake-ssh" --select prod --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa session list --json --state-dir "$tmp/state"
go run ./cmd/ssherpa session map --state-dir "$tmp/state"
go run ./cmd/ssherpa session map --all --state-dir "$tmp/state"
go run ./cmd/ssherpa --state-dir "$tmp/state" --ssh-binary "$tmp/fake-ssh" --select prod --composer-key ctrl-r --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa --state-dir "$tmp/state" --ssh-binary "$tmp/fake-ssh" --select prod --latency-warn 1ms --config internal/sshconfig/testdata/matrix/config
go run ./cmd/ssherpa session list --json --state-dir "$tmp/state"
go build -trimpath -o ssherpa ./cmd/ssherpa
```

For manual supervised-session UX testing, connect normally and press
`Ctrl-]` during the session. The local active-session map overlay should
open; press `Ctrl-]`, `q`, or `Esc` to return to the remote PTY. Use
`Ctrl-G` during the session to open the local queued-input composer.
Enter sends the composed buffer plus a newline, `Ctrl-G` sends without a
newline, and `Esc` cancels. Use `--no-composer` to disable this path or
`--composer-key ctrl-r` to choose another control-key hotkey. Use
`--latency-warn 2s` to enable the local sidecar probe. The warning is
printed locally, recorded in session state, and never sent to the remote
PTY. Add `--latency-disconnect 30s` only when testing explicit
auto-disconnect behavior; it requires `--latency-warn`. Use `--direct`
only when testing the unsupervised runner.

The native build output `ssherpa` is ignored by git.

The CI workflow also cross-builds these release targets with
`CGO_ENABLED=0`:

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`

## Releases

Releases are cut by pushing a semver tag. The `Release` GitHub Actions
workflow (`.github/workflows/release.yml`) runs GoReleaser on any `v*`
tag and publishes a GitHub Release with:

- `tar.gz` archives for `linux/amd64`, `linux/arm64`, `darwin/amd64`,
  and `darwin/arm64`,
- `.deb` and `.rpm` packages for Linux (amd64 and arm64),
- a `checksums.txt`,
- version metadata injected via linker flags (`main.version`,
  `main.commit`, `main.date`).

To cut a release:

```sh
git tag v1.2.3
git push origin v1.2.3
```

The tag must point at a commit that already contains the workflow, so
push the branch first. The workflow uses the repo's `GITHUB_TOKEN`; no
extra secrets are required.

Validate the config and dry-run the whole pipeline locally before
tagging:

```sh
goreleaser check
goreleaser release --snapshot --clean   # builds everything into ./dist
```

The config uses GoReleaser v2 syntax and the public schema at
https://goreleaser.com/static/schema.json. SBOMs and a Homebrew tap are
possible future additions (see `PORT_PLAN.md`).

## Compatibility Reference

The Bash Zoo implementation is the behavior reference for the port:

- Local sibling checkout: `../bash-zoo/scripts/ssherpa.sh`
- Source: https://github.com/0xbenc/bash-zoo/blob/main/scripts/ssherpa.sh
- Docs: https://github.com/0xbenc/bash-zoo/blob/main/docs/ssherpa.md
