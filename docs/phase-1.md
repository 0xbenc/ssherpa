# Phase 1 Inventory

Phase 1 adds read-only SSH config inventory and JSON output. It does not
write config files, execute SSH, or launch an interactive picker.

## Commands

```sh
ssherpa list [--json] [--all] [--filter SUBSTR] [--user USER] [--config PATH]
ssherpa show ALIAS [--json] [--config PATH]
```

Default config root:

```text
~/.ssh/config
```

## Parser Coverage

The parser supports:

- Empty lines and comments.
- Inline comments outside double quotes.
- Case-insensitive keywords.
- `Keyword value` and `Keyword=value`.
- Double-quoted values.
- `Host` lines with multiple patterns.
- Negated patterns beginning with `!`.
- `Include` with absolute paths, `~`, relative paths, and globs.
- Missing include globs without hard failure.
- Duplicate include detection.
- Include cycle diagnostics.
- `Match` scopes as conditional contexts.
- Multiple `IdentityFile` values.
- CRLF input.

## Inventory Rules

- Concrete aliases are shown by default.
- Wildcard and negated patterns are hidden unless `--all` is set.
- Entries with parsed `User git` are hidden by default.
- Set `SSHERPA_IGNORE_USER_GIT=0` to show git-user entries.
- `--user USER` filters aliases with a parsed user that differs from
  `USER`; aliases with no parsed user remain visible.
- `--filter SUBSTR` matches against alias name, host, user, port, and
  identity files.
- Duplicate aliases keep the first parsed occurrence and add a warning to
  that alias.
- Aliases from conditional includes are included and marked
  `is_conditional`.

## JSON Shape

`list --json` emits:

```json
{
  "config": {
    "root_path": "/home/me/.ssh/config",
    "files": [],
    "includes": []
  },
  "aliases": [],
  "diagnostics": []
}
```

`show ALIAS --json` emits the same `config` and `diagnostics` fields
with one `alias` object or `null`.

## Acceptance

```sh
go test ./...
go run ./cmd/ssherpa list --json --config internal/sshconfig/testdata/matrix/config
```

Temp HOME smoke test:

```sh
tmp_home="$(mktemp -d)"
mkdir -p "$tmp_home/.ssh"
cp internal/sshconfig/testdata/matrix/config "$tmp_home/.ssh/config"
cp -R internal/sshconfig/testdata/matrix/config.d "$tmp_home/.ssh/config.d"
cp internal/sshconfig/testdata/matrix/conditional.conf "$tmp_home/.ssh/conditional.conf"
HOME="$tmp_home" go run ./cmd/ssherpa list --json
```

## Known Limits

- `ssh -G` resolution is not implemented yet.
- `Match` conditions are not evaluated; they only mark included aliases
  as conditional.
- OpenSSH token expansion is not implemented.
- User-qualified include paths such as `~otheruser/file` are rejected
  with a diagnostic.
- Pattern aliases shown with `--all` are inventory records, not validated
  connection targets.
