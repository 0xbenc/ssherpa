# Unified Theme Engine: Feasibility Verdict & Design Doc

**Status:** Approved — feasible (yes)
**Recommended architecture:** Hybrid — tiny shared `termtheme` module (data layer + portable format) + per-app palettes/editors/resolution
**Author:** Lead architect
**Date:** 2026-06-22

---

## 1. Executive feasibility verdict

**The work is already ~90% shared, and that is verified against source, not assumed.**

`passage/internal/termstyle/theme.go` and `ssherpa/internal/termstyle/theme.go` are **logic-identical** for every cross-compat-critical function: `ParseThemeConfig`, `ParseStyleSpec`, `Apply`, `Normalized`, `Style`, `rawSGR`/`normalizeSGR`, `roleForKey`, the `styleTokenCodes`/`colorTokenCodes` maps, env/path resolution, and the tolerate-and-warn-on-unknown-keys path. The divergence is small, enumerable, and **all data, not algorithm**:

| Divergence | passage | ssherpa | Verified at |
|---|---|---|---|
| Role count | 16 (incl. `selected_bar`) | 15 | theme.go role consts |
| `selected_bar` aliases | `selected_bar`/`selection_bar`/`bar` | **none** | `roleAliases` map |
| `TerminalTheme` title | `32` | `1;36` | `TerminalTheme()` |
| `TerminalTheme` muted | `2` | `90` | `TerminalTheme()` |
| `TerminalTheme` subtle | `39` | `2` | `TerminalTheme()` |
| `TerminalTheme` border | `1;32` | `90` | `TerminalTheme()` |
| `selected_bar` builtin | terminal `100` / vivid `48;2;45;55;72` | absent | `TerminalTheme`/`VividTheme` |
| Writer emits `theme = base` | yes (cli.go:505) | **no** (theme.go:147) | `formatThemeConfig` |
| Env prefix / config dir | `PASSAGE_` / `passage/` | `SSHERPA_` / `ssherpa/` | `ResolveTheme`/`resolveThemeFile` |

There is exactly **one genuine algorithmic fork**: `Truncate`/`TruncateWith`/`VisibleWidth`. passage uses `utf8.DecodeRuneInString` + a private `cellWidth()`; ssherpa uses `ansi.FirstGraphemeCluster(value[i:], ansi.GraphemeWidth)`. These produce **different bytes** for emoji-with-variation-selector and multi-rune clusters. This single fork **falsifies the vendored-contract proposal's keystone claim** that the hand-written parser is byte-identical (its sha256-region drift guard would fail on day one).

**Verdict:** Lean into the shared reality. Extract the proven-identical data layer into one tiny module, keep the genuinely-divergent palettes/editors/resolution per-app, and make a portable `.theme` file the interchange unit. This is **Hybrid (Architecture 4)** with the best ideas grafted from the format-first and vendored-contract angles.

### Why not the other three angles

- **Pure shared-module:** overreaches. It plans to share the editor (verified 401-line diff, app-specific previews) and never accounts for passage-only `countdown.go` (references `Role`/`Theme`) and `glyphs.go`. Those would pollute a "single source of truth" module or need unscoped carve-out work.
- **Format-only (no shared code):** its own example prints bare `[palette]`/`[roles]` section headers. The **real parser hard-fails** on those — `cutThemeAssignment` only accepts `=`/`:` lines and `ParseThemeConfig` returns `"line N: expected key=value"` otherwise. And copy-paste has **already silently diverged** (Truncate), proving the synced-contract model rots.
- **Vendored-contract:** rests on the false byte-identical-parser premise above, and **mislocates `formatThemeConfig` in `termstyle`** — it actually lives in `internal/cli` (passage cli.go:499, ssherpa theme.go:147), so the proposed conformance test asserting it from inside `termstyle` cannot compile.

---

## 2. Unified canonical role registry

**Canonical superset = 16 roles = exactly passage's current set.** ssherpa's 15 is a strict subset (its only missing role is `selected_bar`), so no third role-owner is needed and no role exists in ssherpa but not passage.

Owned in `termtheme` as `Role` consts + `UniversalRoles()`, in display order:

```
title, primary, secondary, accent, muted, subtle, foreground,
selected, selected_bar, border, success, warning, danger, info, search, pill
```

The shared `roleAliases` map is the **union** of both apps' alias maps (identical today except passage's three `selected_bar` aliases). **Required change:** ssherpa adopts `selected_bar`/`selection_bar`/`bar` -> `selected_bar`. Today ssherpa has zero such aliases, so it currently **drops** a passage `selected_bar` line at parse time via the unknown-key `Warnings` path. After adopting the shared map, ssherpa *recognizes* the role.

### Two-tier model (the key abstraction)

- **Tier 1 — universal superset** (shared, `termtheme`): what a file *can carry*. `ParseThemeConfig` populates **every** recognized universal role into `Codes`/`Specs`, regardless of host app.
- **Tier 2 — `AppRoles []Role`** (per-app): what the app actually *renders/edits*. passage = all 16; ssherpa = 15 (no `selected_bar`). `Style()` and the editor only ever request roles in `AppRoles`.

A foreign role (`selected_bar` arriving at ssherpa) therefore sits in the in-memory model **unused but preserved**, never painted, and is re-emitted on the next export (passthrough).

**Per-app builtin palettes (`TerminalTheme`/`VividTheme`) stay per-app** — they genuinely diverge and unifying them would silently restyle one app.

**Extension:** a future app adds a new role to `UniversalRoles()` (single source of truth) + its own `AppRoles`. Older binaries hit tolerate-and-warn and never hard-fail (the existing `TestParseThemeConfigToleratesUnknownRoleKeys` guarantee, present in both repos).

---

## 3. Portable theme file format

The portable `.theme` file **is the existing `theme.conf` grammar** (`key = value` / `key : value`, `#` comments, raw-SGR or token specs, `-`/`_` key normalization) with three additions:

1. A versioned header comment block: `# termtheme v1` and `# source = <app> <version>`.
2. A **mandatory full-role dump** — every role the producer defines is emitted with an inline spec, so the receiver never has to reconstruct the producer's base palette.
3. A `format = N` integer key (N starts at 1).

**No `[section]` headers** — the real parser hard-fails on them. Palette/role layering is done **without** sections: an optional swatch is a `@`-prefixed key (`@brand = 1;38;2;96;221;255`) and a role may reference it (`primary = @brand`). v1 exporters **always inline** (never emit `@refs`); importers resolve `@refs` eagerly before the formatter sees them. `@`-keys are tolerated-and-warned by any binary predating `@` support.

### Worked example (passage export, vivid base)

```ini
# termtheme v1
# source = passage 0.6.0
format = 1
theme = vivid
title = 1;38;2;96;221;255
primary = 1;38;2;96;221;255
secondary = 1;38;2;120;183;255
accent = 1;38;2;255;209;102
muted = 38;2;132;145;160
subtle = 38;2;58;69;87
foreground = 38;2;235;239;245
selected = 1;38;2;255;255;255
selected_bar = 48;2;45;55;72
border = 38;2;58;69;87
success = 1;38;2;134;239;172
warning = 1;38;2;255;209;102
danger = 1;38;2;255;151;112
info = 1;38;2;214;160;255
search = 1;38;2;255;255;255
pill = 1;38;2;25;30;38;48;2;96;221;255
```

An ssherpa export is identical **minus the `selected_bar` line** (and `# source = ssherpa …`). `# termtheme v1` / `# source =` are stripped by the existing `#` handler; `format` is tolerated-and-warned by today's binaries; `theme` is already honored by both. So an **old binary** reading a v1 file loads every role it knows and ignores the rest — graceful even via `--theme-file`.

### Load rules / fallback chains

| Situation | Behavior |
|---|---|
| **Missing role** (producer omitted, or producer app lacks it, e.g. ssherpa omits `selected_bar`) | Importer fills from its **own** builtin base via existing `Normalized()` merge. Fail-open, never errors. NOTE this uses the importer's *divergent* default — which is exactly why exporters MUST dump every role they have. |
| **Foreign role present, importer doesn't render it** (`selected_bar` -> ssherpa) | Recognized via shared alias map, stored in `Codes`/`Specs`, **not painted**, **re-emitted verbatim** on next export (passthrough). `passage→ssherpa→passage` is lossless. |
| **Unknown key not in superset** (future role, old binary) | Tolerate-and-warn, skipped. |
| **`format = N`, N > supported** | Best-effort parse all role lines, warn `"pack format N newer than supported M"`, never hard-fail. (ssherpa already ships an `ErrFutureBundleVersion` posture in `internal/portable`; soften to warn-not-refuse for forward compat.) |

---

## 4. Backwards-compatibility plan

Fully preserved and verified.

- **Grammar unchanged.** `termtheme.ParseThemeConfig` is the moved-verbatim parser, so every legacy `~/.config/{passage,ssherpa}/theme.conf` parses byte-identically.
- **Resolve precedence unchanged.** `opts.Name` stays ignored, `cfg.BaseName` stays load-bearing, `NO_COLOR` / `PASSAGE_NO_COLOR` / `SSHERPA_NO_COLOR` stay per-app in each `ResolveTheme`.
- **Per-app builtin palettes kept verbatim** — a user who never customized a role sees zero visual change.
- **New headers are comments** the existing `#` handler strips; legacy files (no header) stay valid. No-`theme=` line still defaults to terminal.
- **`format=` / `@`-keys** take the existing tolerate-and-warn path in any older binary.
- **Atomic save** (`fsutil.AtomicWriteFile` + backup) and config-dir/`~` expansion untouched.
- **No user-data migration** — live `theme.conf` is unchanged; `.theme` is reached only via new export/import verbs.

**Two intentional, strictly-additive on-save changes (both ssherpa):**
1. Emit `theme = <base>` when `base != terminal` (mirrors passage cli.go:505-511 — **also fixes a confirmed standalone ssherpa base-loss bug**).
2. Preserve passthrough roles (`selected_bar`) instead of dropping them.

An old ssherpa binary reading the new output already honors `theme = vivid` on load, so **no downgrade regression**. The common case (terminal base, no passthrough roles) keeps ssherpa output byte-identical.

---

## 5. Export / import UX

**CLI (both apps), mirroring ssherpa's existing `export`/`import` porting UX in `internal/portable` + `cli/porting.go`:**

```
passage theme export PATH      ssherpa theme export PATH
passage theme import PATH      ssherpa theme import PATH
```

- **Export** = load current `ThemeConfig` → `termtheme.Marshal(cfg, {App, Version, Passthrough})` → `AtomicWriteFile(PATH.theme)`.
- **Import** = `ReadFile` → `termtheme.Unmarshal` → `formatThemeConfig` → `AtomicWriteFile(live theme.conf, backup)`.

**Editor:** unchanged and per-app (verified 401-line diff, app-specific previews: passage password-store mockup, ssherpa SSH host/session mockup + supervised-mode pill; differing `ThemeEditorOptions`/`NoAltScreen` handling). The editor consumes shared `ThemeConfig`/`Role`/`ParseStyleSpec`/`Style` and iterates its own `AppRoles`. Optionally add a footer hint "exported with `… theme export`" but no shared editor code.

---

## 6. Phased roadmap (nothing ships half-migrated)

| Phase | Goal | Effort | Risk |
|---|---|---|---|
| **0** | **Unify Truncate first.** Replace passage's rune-based width helpers with ssherpa's grapheme-cluster version in BOTH apps; re-baseline passage width/golden tests. Strict correctness win, lands per-repo, no module yet. | S | medium |
| **1** | **Fix ssherpa writer + adopt `selected_bar` alias** (ssherpa only). Add the 3 aliases; add `Passthrough` to `ThemeConfig`; patch `formatThemeConfig` to emit `theme = <base>` and re-emit passthrough roles. Fixes base-loss bug + makes round-trip lossless, all before extraction. | M | medium |
| **2** | **Cut `termtheme` module (data layer only).** Move Role/superset, alias+token maps, parser, `ParseStyleSpec`, `Apply`, `Normalized`, `Style`, SGR + unified width helpers, and new `Marshal`/`Unmarshal`. Golden test asserts byte-identical parse/serialize vs pre-extraction. Do NOT move palettes, resolution, editors, `countdown.go`, `glyphs.go`. | L | medium |
| **3** | **Adopt in passage behind a type-alias shim** (`type Role = termtheme.Role`). ~11 call sites untouched; palettes/`AppRoles`/`ResolveTheme`/countdown/glyphs stay local. Superset app = lower-risk first adopter. | M | low |
| **4** | **Adopt in ssherpa behind the same shim** (~50 call sites, mechanical). Wire Phase-1 `Passthrough` + writer through shared `Marshal`. Re-run ssherpa portable-bundle tests. | M | low |
| **5** | **Add `theme export`/`import` verbs** to both. Ship cross-app golden `.theme` fixtures + import-golden tests in each repo. | M | low |

**Sequencing rationale:** Phases 0 and 1 deliver value standalone (correctness fix + bug fix) and de-risk the extraction by removing the only algorithmic fork and the only lossy round-trip *before* any code is shared. The module never ships until both apps can adopt it.

---

## 7. Distribution & coupling decisions

- **Shared module, not synced contract.** Copy-paste has *already* diverged (Truncate); a contract would rot the same way and the vendored-contract proposal's enforcement (sha256 parser hash) is already broken. The module is justified **only because cross-app interchange is the goal** — if that requirement is dropped, do **not** extract; keep copy-paste.
- **Multirepo, not monorepo.** Three repos under `0xbenc`; `go.work` for local cross-edit, pinned tags for release. Keeps passage/ssherpa release cadences and ssherpa's idle-cold path independent. A monorepo would couple their releases.
- **`termtheme` deps:** Go 1.26 + only `github.com/charmbracelet/x/ansi v0.11.7` (already shared). Stays raw-SGR; downsampling stays in each app's bubbletea v2 renderer (verified: `colorprofile` is indirect-only, neither app imports it directly).

---

## 8. Risks

1. **Passthrough is a latent drift trap, not compiler-enforced.** Writers iterate `Roles()` over `cfg.Specs` and the editor only fills `Specs` for shown roles; any future save refactor that rebuilds `Specs` from `AppRoles` re-introduces the exact `selected_bar`/`theme=` loss. **Mitigate** with an explicit `passage→ssherpa→passage` round-trip golden test.
2. **Three-way release coordination** — a shared-parser fix needs a module tag + two `go.mod` bumps + two releases; a module regression breaks both binaries.
3. **Per-app palette divergence is real data** and must stay per-app; hoisting `TerminalTheme`/`VividTheme` silently restyles one app.
4. **Fail-open ≠ identical color** — base defaults diverge, so a *missing* role renders with the importer's default. The full-role-dump rule is load-bearing; enforce by test.
5. **Raw-SGR / terminal-palette brittleness** — an exported truecolor theme depends on the receiver's renderer to downsample; on 16-color terminals it may look different. Document, don't solve in-format; assert the module never downsamples.
6. **Truncate unification shifts passage width/golden tests** — must re-baseline in Phase 0.
7. **Test duplication** — large `termstyle_test.go` in both repos must be reconciled into the module suite (budget into Phase 2).
8. **Pinned pseudo-versions** — `bubbletea v2` / `x/ansi` must stay lockstep across module + both apps.

---

## 9. Open questions

1. **Module vs synced contract** — recommend module; fallback is the vendored-contract *data model* (app-keyed `registry.json` superset + golden fixtures) **without** the broken sha256 hash and **without** codegen for the writer (which lives in `internal/cli`). Decide before Phase 2.
2. **Monorepo vs multirepo** — recommend multirepo; confirm no other monorepo plans.
3. **Should ssherpa surface `selected_bar` in its editor as pass-through-only** (visible, non-rendered, preservable) or keep it invisible? Invisible is simpler but freezes the imported bar color during cross-app edits.
4. **Export source** — default to live `theme.conf` or require explicit name/file? Align with ssherpa's existing porting UX.
5. **`@`-palette refs in v1** — reserve syntax (tolerated) but defer the resolver? Costs nothing now; confirm no near-term need.
6. **Membership-check placement** — should `termtheme` expose `IsRenderable(app, role)` (parse-time foreign-role warning) or leave it to each app's `Style`/editor (render-time)?
