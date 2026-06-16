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

> **Where did github.com go?** Hosts whose `User` is `git` (github.com,
> gitlab.com, and other git remotes) are hidden from the host list by
> default — they are deploy endpoints, not shells. Set
> `SSHERPA_IGNORE_USER_GIT=0` to show them.

It exists because SSH workflows get messy: bastions, manual hops, local
forwards, SOCKS proxies, file copies, and half-remembered aliases.

`ssherpa` **keeps OpenSSH as the source of truth**, but makes daily SSH wizardy livable.

## Standouts

- **Session map:** press `Ctrl-^` in a supervised session to see the active
  route and nested lineage (rebindable with `--overlay-key`). Lineage deeper
  than one nesting level needs a one-line server opt-in — see
  [Server-side setup](#server-side-setup-optional).
- **File sending:** send or receive individual files over OpenSSH SFTP, with
  overwrite protection and picker-driven paths.
- **Remote key seeding:** install or remove validated public keys on many saved
  SSH aliases, including hosts reached through ProxyJump routes, without sudo.
- **Escape rope:** `Ctrl-^`, `X`, `X` disconnects every supervised layer below
  you and returns to the outer shell. Mash `Ctrl-^` three times for the
  no-questions-asked panic rope. A layer running inside `tmux` would normally
  survive the rope (the multiplexer keeps it alive across the disconnect), so a
  nested ssherpa inside tmux watches its own upstream link and tears *itself*
  down — without touching tmux — when the link is lost while you were attached;
  a deliberate `tmux` detach is spared so detach-and-reattach still works.
  Requires the one-line server opt-in (see [Server-side setup](#server-side-setup-optional))
  or `SSHERPA_MUXER_GUARD=force`; disable with `--no-muxer-guard`. tmux only for
  now (screen is detected and labeled but not auto-torn-down).
- **Session recording:** start, pause, and resume replayable output transcripts
  from the `Ctrl-^` session map overlay. Browse recordings from the TUI or use
  `ssherpa session log`, `grep`, `replay`, and `export`. Portable transcript
  bundles can be imported on another machine and are labeled as local, imported
  from this machine, imported from another machine, or unknown origin.
- **Presets:** save reusable local port-forward and SOCKS proxy entries, launch
  them later, and stop tracked background sessions by name. Tunnels reconnect
  with backoff when ssh exits; pair with `ServerAliveInterval` in your ssh
  config so dead links exit instead of hanging (see the CLI reference).
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

## Server-side setup (optional)

ssherpa needs nothing installed on the servers you connect to. Nested-session
lineage rides SSH environment variables (`SSHERPA_SESSION_ID`,
`SSHERPA_DEPTH`, `SSHERPA_ROUTE`, ...), and a stock sshd rejects them — its
`AcceptEnv` default allows none. ssherpa degrades gracefully; here is exactly
what works without any server change:

| Feature | Stock server | With `AcceptEnv SSHERPA_*` |
| --- | --- | --- |
| Connect, jump, forward, proxy, transfer, check | works | works |
| Escape rope and panic rope | works | works |
| Local session map, one nesting level deep | works | works |
| Incoming session presence (`ssherpa incoming list`) | works | works |
| Remote lineage in maps deeper than one level | remote ssherpa sees itself as a root session | full route and depth |
| tmux escape-rope teardown of a nested session | detect-only (no auto-teardown; set `SSHERPA_MUXER_GUARD=force` to override) | tears down on upstream loss |
| Incoming session labels (origin host, route) | missing | shown |
| Route/depth metadata in transcripts exported on the remote | incomplete | complete |

To opt in, add one line to the server's `/etc/ssh/sshd_config` and reload
sshd:

```
AcceptEnv SSHERPA_*
```

Labels are advisory either way: clients can spoof accepted environment
variables.

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
