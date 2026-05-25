# Phase 3: Safe Config Mutation

Phase 3 adds write-capable SSH config operations. The Bash Zoo behavior is
still the compatibility reference, but the Go port intentionally improves
the dangerous parts: updates are planned in memory, dry-run diffs are
available, existing files are backed up before writes, writes use
same-directory temp files and atomic rename, and multi-alias stanzas are
edited without deleting unrelated aliases.

## Commands

```sh
ssherpa add --alias NAME --host HOST [--user USER] [--port PORT]
ssherpa add --alias NAME --host HOST --identity PATH --identities-only
ssherpa add --alias NAME --host HOST --dry-run
ssherpa add --alias NAME --host HOST --yes

ssherpa edit set ALIAS [--host HOST] [--user USER] [--port PORT]
ssherpa edit set ALIAS [--identity PATH] [--identities-only]
ssherpa edit set ALIAS --clear-user --clear-port --clear-identity

ssherpa edit delete ALIAS [--all-sources]
ssherpa edit delete ALIAS --dry-run

ssherpa edit delete-all --dry-run
ssherpa edit delete-all --confirm "delete N aliases"
```

All mutation commands accept `--config PATH`. Without `--config`, `add`
updates the included file where an existing alias was found. New aliases
are appended to the default root config, `~/.ssh/config`.

## Add And Update

`ssherpa add` appends a new `Host` stanza when the alias does not exist.
When the alias already exists in one source file, it updates that source.

Single-alias stanzas are updated in place while preserving unrelated
directives and comments where possible. Multi-alias stanzas such as
`Host prod web` are split safely:

```sshconfig
Host web
  HostName shared.example.com

Host prod
  HostName prod.example.com
```

The writer refuses to split a stanza containing wildcard or negated
patterns unless that case is handled manually.

## Delete

`ssherpa edit delete ALIAS` removes the alias from its source file.

- `Host prod` removes the whole stanza.
- `Host prod web` becomes `Host web`.
- Duplicate aliases in the same file are all removed.
- Duplicate aliases across files require `--all-sources` or an explicit
  `--config PATH`.
- Stanzas containing wildcard or negated patterns require
  `--delete-patterns`.

## Delete All

`ssherpa edit delete-all` operates on the currently listed aliases, so it
honors `--filter`, `--user`, `--all`, and git-user hiding.

Dry-run mode prints diffs without writing:

```sh
ssherpa edit delete-all --filter scratch --dry-run
```

Destructive mode requires an exact confirmation phrase:

```sh
ssherpa edit delete-all --filter scratch --confirm "delete 3 aliases"
```

Pattern aliases are never deleted by delete-all unless both `--all` and
`--delete-patterns` are supplied, followed by the exact confirmation.

## Write Safety

Every applied mutation:

- renders the changed file in memory;
- re-parses the rendered config before writing;
- refuses to write if the file changed after planning;
- creates a timestamped backup for changed existing files;
- writes a temp file in the same directory;
- preserves the existing file mode where possible;
- fsyncs the temp file;
- renames atomically;
- fsyncs the parent directory where supported;
- reports changed files and backup paths.

Backup names look like:

```text
~/.ssh/config.ssherpa-backup.20260524T194500Z
```

## Verification

```sh
go test ./...
go vet ./...
go run ./cmd/ssherpa add --alias prod --host prod.example.com \
  --config internal/sshconfig/testdata/matrix/config --dry-run
```

Use disposable temp configs for real write tests:

```sh
tmp="$(mktemp -d)"
cfg="$tmp/config"
printf 'Host old\n  HostName old.example.com\n' > "$cfg"

go run ./cmd/ssherpa add --alias prod --host prod.example.com --config "$cfg" --yes
go run ./cmd/ssherpa edit set prod --user alice --config "$cfg" --yes
go run ./cmd/ssherpa edit delete prod --config "$cfg" --dry-run
go run ./cmd/ssherpa edit delete-all --config "$cfg" --dry-run
```

## Known Limits

- The interactive add/edit prompts are intentionally plain; the tested
  core is the non-interactive mutation path.
- The writer preserves comments and unrelated directives where practical,
  but replacing a `Host` line can normalize that line's spacing.
- Effective OpenSSH values are still parsed locally rather than resolved
  with `ssh -G`.
- `authorized_keys`, jump, and proxy workflows remain later phases.
