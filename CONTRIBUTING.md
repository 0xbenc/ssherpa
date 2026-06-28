# Contributing

Keep changes small, tested, and aligned with the existing command behavior.

Make a pull request into main when you are done creating and testing.

## Local Checks

Run these before committing:

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
```

For release config smoke tests, install GoReleaser and run:

```sh
goreleaser check
goreleaser release --snapshot --clean
```

## TUI alignment & drift guards

ssherpa shares its terminal-UI primitives with [passage](https://github.com/0xbenc/passage)
through three semver-pinned modules — `termtheme` (roles + `.theme`), `termnav`
(navigation / list windowing), and `termchrome` (box/footer/kvrow + glyphs/countdown;
consumed via the `internal/chrome` shim). Keep them aligned:

- **Footers** go through `chrome.Footer([]chrome.KeyHint{...})` — never hand-build a
  multi-space separator. CI greps for `  /  ` and triple-space footer literals.
- **Spinners / progress** use `termchrome.ResolveGlyphs` — never inline frame runes.
- **No golden-update flag.** "goldens" are inline string literals; a chrome/pixel change
  means hand-editing the expectations and keeping `assertBorderIntegrity` +
  `TestTruncateStyledSanitizesOnOverflow` green. The raw-transcript path keeps its own
  `Strip` overflow policy (`sessionview.truncateVisible`) — do **not** rewrite it to `Sanitize`.
- **Interaction contract.** `docs/flow-contract.md` is a pointer to the canonical contract
  in the passage repo (the interaction source of truth for both apps). Any PR changing an
  interactive surface (incl. the PTY overlay or a wizard) must keep ssherpa conformant and,
  if it changes shared grammar, update the canonical passage copy in a companion PR. This is
  a release-blocking checklist item; behavioral conformance here is gated by this repo's
  golden/invariant tests.
- **Cross-repo pin lockstep.** ssherpa and passage pin **identical** termtheme/termnav/
  termchrome versions, with **no `replace`** in the released `go.mod`. Bump both apps
  together. *Hotfix exception:* an urgent single-app fix may bump one app ahead; restore
  lockstep next release.

## Safety Rules

- Keep OpenSSH as the source of truth.
- Do not mutate user SSH config or `authorized_keys` without tests,
  backups, and dry-run behavior.
- Use temp HOME directories for destructive tests.
- Prefer parser-backed edits over string replacement.
- Keep the direct `ssh alias` runner as the default until supervised PTY
  behavior is proven.
