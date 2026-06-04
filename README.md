# ssherpa

[![CI](https://github.com/0xbenc/ssherpa/actions/workflows/ci.yml/badge.svg)](https://github.com/0xbenc/ssherpa/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/0xbenc/ssherpa?sort=semver)](https://github.com/0xbenc/ssherpa/releases/latest)
[![Go](https://img.shields.io/badge/go-1.26.3-00ADD8.svg)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

*The SSH config you already have, with a map and an escape rope.*

![ssherpa picker running in a supervised session](docs/ssherpa.jpg)

`ssherpa` is a TUI for OpenSSH. It reads your
real `~/.ssh/config`, and lets you choose a host or preset.
Then it runs your real `ssh`, and adds a map when sessions get nested.

It exists because SSH workflows get messy: bastions, manual hops, local
forwards, SOCKS proxies, file copies, and half-remembered aliases.

`ssherpa` **keeps OpenSSH as the source of truth**, but makes daily SSH wizardy livable.

## Standouts

- **Session map:** press `Ctrl-]` in a supervised session to see the active
  route and nested lineage.
- **File sending:** send or receive individual files over OpenSSH SFTP, with
  overwrite protection and picker-driven paths.
- **Escape rope:** `Ctrl-]`, `X`, `X` disconnects every supervised layer below
  you and returns to the outer shell.
- **Session recording:** view a log your commands and outputs.
- **Presets:** save reusable local port-forward and SOCKS proxy entries, launch
  them later, and stop tracked background sessions by name.
- **Full Theming:** adjust the colors to your liking and save presets.

## Install

### Homebrew cask

```sh
brew install --cask 0xbenc/tap/ssherpa
```

Or:

```sh
brew tap 0xbenc/tap
brew install --cask ssherpa
```

### Release artifacts

Download the latest macOS or Linux artifact from
[GitHub Releases](https://github.com/0xbenc/ssherpa/releases/latest).

Archives are published for:

- `darwin_amd64`
- `darwin_arm64`
- `linux_amd64`
- `linux_arm64`

```sh
tar -xzf ssherpa_VERSION_OS_ARCH.tar.gz
sudo install -m 0755 ssherpa /usr/local/bin/ssherpa
```

Linux packages are also published as `.deb` and `.rpm`:

```sh
sudo dpkg -i ssherpa_VERSION_linux_amd64.deb
sudo rpm -i ssherpa_VERSION_linux_amd64.rpm
```

### From source

Requires Go 1.26.3 or newer.

```sh
git clone https://github.com/0xbenc/ssherpa.git
cd ssherpa
go build -trimpath -o ssherpa ./cmd/ssherpa
sudo install -m 0755 ssherpa /usr/local/bin/ssherpa
```

This works on macOS and Linux. The release build supports `amd64` and `arm64`
for both platforms.

## Docs

- [Non-interactive CLI reference](docs/non-interactive.md)

## Contributors

<p>
  <a href="https://github.com/0xbenc"><img src="https://github.com/0xbenc.png?size=96" width="48" height="48" alt="@0xbenc"></a>
  <a href="https://github.com/basedvik"><img src="https://github.com/basedvik.png?size=96" width="48" height="48" alt="@basedvik"></a>
</p>

## License

[MIT](LICENSE) (c) Ben Chapman

<sub>Screenshot background art by [Robh](https://broadviewgraphics.blogspot.com/2012/11/tron-uprising-update.html).</sub>
