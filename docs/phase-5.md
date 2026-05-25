# Phase 5: Authorized Keys

Phase 5 ports the Bash Zoo `authorized_keys` manager and tightens the
unsafe parts of the Bash implementation.

The Go version treats `authorized_keys` as structured data. It preserves
existing comments and unrelated lines, preserves leading key options
exactly, detects duplicates by key type plus blob, validates keys with
`ssh-keygen -lf -` when available, and falls back to structural base64
validation when `ssh-keygen` is not installed.

## Commands

Interactive:

```sh
ssherpa authkeys
```

Non-interactive:

```sh
ssherpa authkeys list [--json]
ssherpa authkeys add --key "ssh-ed25519 ..."
ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub
ssherpa authkeys merge --from-dir ./keys
ssherpa authkeys replace --from-dir ./keys
ssherpa authkeys delete --fingerprint SHA256:...
```

Mutation commands also accept:

```text
--path PATH
--dry-run
--yes
--ssh-keygen PATH
```

`--path` is useful for tests and disposable files. Without it, ssherpa
uses `SSHERPA_AUTHORIZED_KEYS_PATH` when set, then falls back to
`~/.ssh/authorized_keys`.

## Directory Imports

`merge` and `replace` read keys from a directory:

- if `DIR/authorized_keys` is a directory, regular files under it are
  read in sorted order;
- otherwise `DIR/*.pub` files are read in sorted order;
- blank lines and comments are ignored;
- invalid keys, duplicate keys, and duplicates with different options are
  reported as diagnostics;
- source path and line number are preserved in diagnostics and JSON
  output.

`merge` appends only keys that are not already present. If the same key
already exists with different options, ssherpa warns and leaves the
existing policy unchanged.

`replace` refuses to replace the file when no valid keys are found.

## Write Safety

Every applied mutation:

- plans the full new file in memory before writing;
- refuses to write if the file changed after planning;
- supports dry-run unified diffs;
- creates timestamped backups for changed existing files;
- writes through same-directory temp files and atomic rename;
- writes `authorized_keys` with mode `0600`;
- creates missing parent directories with mode `0700`.

Backup names look like:

```text
~/.ssh/authorized_keys.ssherpa-backup.20260524T194500Z
```

## Examples

Use a disposable file:

```sh
tmp="$(mktemp -d)"
auth="$tmp/authorized_keys"

ssherpa authkeys add \
  --key "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example" \
  --path "$auth" --yes

ssherpa authkeys list --json --path "$auth"
```

Preview a merge:

```sh
ssherpa authkeys merge --from-dir ./keys --dry-run
```

Replace with confirmation:

```sh
ssherpa authkeys replace --from-dir ./keys
```

Delete by fingerprint:

```sh
ssherpa authkeys delete --fingerprint SHA256:HIw5mTiqXNXNO2h1Vh9R81VrAaKPj4DqNvb3oWElxwk
```

## Verification

```sh
go test ./...
go vet ./...
go run ./cmd/ssherpa authkeys list --json --path /tmp/ssherpa-authorized-keys
```

Use disposable files for write tests:

```sh
tmp="$(mktemp -d)"
auth="$tmp/authorized_keys"
keys="$tmp/keys"
mkdir -p "$keys"
printf '%s\n' "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example" > "$keys/alice.pub"

go run ./cmd/ssherpa authkeys merge --from-dir "$keys" --path "$auth" --dry-run
go run ./cmd/ssherpa authkeys merge --from-dir "$keys" --path "$auth" --yes
go run ./cmd/ssherpa authkeys list --path "$auth"
```

## Known Limits

- Interactive delete removes one selected key at a time. The
  non-interactive command accepts repeated `--fingerprint` values for
  batch deletion.
- Validation uses `ssh-keygen` when available, but structural fallback
  cannot prove that the decoded blob internally matches the displayed key
  type.
- Remote host key management and `known_hosts` browsing remain out of
  scope for the compatibility release.
