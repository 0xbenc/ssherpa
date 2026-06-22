# Making ssherpa a Claude Code grade TUI

A decision-ready roadmap for the maintainer. ssherpa is ~14,281 lines of hand-rolled
UI across ~14 separate Bubble Tea v2 programs plus a live-PTY session-map overlay.
This document picks the architecture, sequences the verified work, and draws the line
on what we will deliberately not do.

---

## 1. North Star

**"Claude Code grade" for ssherpa means: one instrument with fourteen faces, where the
signature face — the Ctrl-^ session map over a live supervised PTY — is the *most*
polished surface, not the least.** The wow on that screen is correctness-and-navigability
led (calm, incorruptible, finally-reachable depth), because motion over a live PTY is
unsafe by construction; visceral motion is concentrated in the standalone alt-screen
programs that own their terminal.

### Measurable checklist

| # | Property | Today | Target |
|---|----------|-------|--------|
| N1 | **Box integrity** under wide runes (CJK/emoji/combining) | ✗ tears every box — `VisibleWidth` counts runes (`termstyle.go:29`) | Cell-accurate; a `日本-east` host never drifts the `│` edge |
| N2 | **Sanitization at every render sink** (no raw ESC/C1 into trusted chrome) | Partial — sinks `Strip` only past the width check | Unconditional `Sanitize` at every chrome sink; raw transcript view preserved |
| N3 | **Coherent design system** — one cursor, one footer grammar, one box math | ✗ `>>` vs `>`, `Q quit` vs `q back`, gutters 7/8/9/13/14, 2 forked box impls | One shared `internal/chrome`; drift is a failing test |
| N4 | **Ranked fuzzy search + match highlight** | ✗ boolean subsequence, no score, no positions (`picker.go:1167`) | fzf-style scored matcher, `RoleMatch` highlight, within-group ranking |
| N5 | **Motion that informs**, gated idle-cold | ✗ zero motion except one offline transcript tick | Spinner/elapsed + SFTP progress in standalone programs only; supervised path stays cold |
| N6 | **Session map is navigable** — deep lineage reachable | ✗ flat 6-line stub, `... N more`, no scroll (`sessionview.go:173`) | Scrollable, cursor-navigable, breadcrumb, per-node liveness |
| N7 | **Capability-aware degradation** — color tier + glyph tier | ✗ binary full-SGR-or-NO_COLOR; raw 24-bit only | truecolor→256→16→mono downsample; rounded→ASCII glyph fallback |
| N8 | **Supervised stream never corrupts** — provable, CI-tested | Convention only, untested | Idle-cold invariant test; byte-identical paints; SIGWINCH-safe restore |
| N9 | **Discoverable affordances** — `?` help, non-cramped footers | ✗ no `?` overlay; footers silently truncate with `~` | `?` help overlay; progressive-disclosure footer with `+N` |
| N10 | **Delightful empty/error/loading states** | ✗ bare `No matches`; multi-GB transfer is a frozen black screen | First-run welcome ≠ zero-match; live transfer/check progress |

**The gap today:** ssherpa is a v1 string-renderer wearing a v2 framework. Of Bubble Tea
v2's declarative `View` capabilities it uses exactly one (`AltScreen`). The substrate
counts runes not cells, so N1 fails app-wide. The design system (N3) exists implicitly in
`renderWorkflowShell` but has already drifted across copies. The signature screen (N6)
hides the exact lineage you opened it to read. Only N8's *spirit* is honored today — the
overlay is correctly event-driven — but it is unguarded by tests.

---

## 2. Current State Assessment

### Strengths (do not throw these away)
- **A real shared shell already exists.** 13 of 14 screens render through
  `renderWorkflowShell` (`workflow_shell.go:18`) with consistent border glyphs, a
  step-progress strip, title edge, and divider. The design system is implicit but present;
  the work is naming it and killing drift, not inventing it.
- **The picker has genuine responsive master-detail.** `renderBody` (`picker.go:544`)
  splits list+preview at `width>=100`, height-budgets scrolling with "N more above/below",
  group headers, and shift-arrow section jumps. This is already Claude-Code-grade for *one*
  screen.
- **The supervised overlay uses the correct technique.** `drawSessionOverlay`
  (`session.go:1536`) paints a bottom-pinned strip with DECSC/DECRC (`\x1b7`/`\x1b8`) and
  absolute-row erase — the tmux status-line approach. It never switches screen buffers, so
  it cannot desync the child's own alt-screen state. It freezes the stream under
  `output.mu` + a suppressing output tap (`session.go:1165`), and is event-driven (blocking
  `stdin.Read`) — **idle stays cold during supervision.**
- **Sanitization primitives exist.** `termstyle.Sanitize` (`termstyle.go:59`) neutralizes
  C0/C1/DEL; `cleanField`/`sanitizeRemoteString` guard the OSC boundary.

### Limitations holding it back
- **Rune-counting width (`termstyle.go:17-33`) is the keystone bug.** `PadRight`,
  `Truncate`, every box helper in `workflow_shell.go`, and the entire `sessionview` box
  family build on it. One wide rune tears the border. Passage already fixed the identical
  helper via `ansi.StringWidth`.
- **Fuzzy is boolean (`fuzzyMatch`, `picker.go:1167`).** No score, no ranking, no match
  positions → no highlight possible. Six filter sites have independently drifted.
- **No capability tiering.** `VividTheme` emits raw 24-bit SGR (`theme.go:81-101`); the only
  fallback is NO_COLOR. Glyphs (`╭ ├ ● ✓`) emit unconditionally → tofu on legacy TERM.
- **The signature screen is the weakest-rendered.** `MapView` truncates lineage to a fixed
  budget (`sessionview.go:173`); the overlay loop handles only `q/X/r/t/s/v` — no
  up/down/PgUp. The deep ProxyJump chain you opened the map to read is **unreachable**.

### The 14-screen drift, quantified
The two box implementations are **near-verbatim forks across a package boundary**:
`workflowEdge/workflowLine/truncateStyled` (`workflow_shell.go:102-146`) vs
`boxEdge/boxLine/truncateVisible` (`sessionview.go:1956-1999`). They differ in exactly one
load-bearing way: `workflowEdge` border-styles the edge fill dashes (`RoleBorder`);
`boxEdge` leaves them unstyled — a silent SGR-color divergence between the picker and the
live overlay. Catalogued drift:

| Axis | Variants observed |
|------|-------------------|
| Selection cursor | `>>` (picker) vs `>` (others) |
| Footer key verb | `Q quit` / `Q back` / `q back`; `enter select` / `launch` / `connect` / `open/select` |
| Footer separator | `' / '` (picker) vs `'  /  '` (host_chooser, add_form, proxy) |
| Title casing | `SSHERPA SESSION MAP` (default) vs lowercase `ssherpa session map` (live callers) vs `SESSIONS` (list) |
| KV-row gutter | 8 / 8 / 8(+lead) / 14 / 9 — labels literally don't line up between adjacent screens |
| Box impl | 2 forked copies, differing in edge-dash SGR |
| Scroll engine | 3 copies (`picker`/`host_chooser`/`transfer_browser`) + a weaker `windowRange` in wizards |
| Bracketed paste | `PasteMsg` handled in 2 screens, ignored in 5 |

Coherence is currently a property of copy-paste discipline — **and that discipline has
already failed.**

---

## 3. The Architecture Decision

Three candidate stances were scored. **Adopt the elevated-handroll path: keep the three
direct deps, keep the 14-program launch model, and promote the existing forked chrome into
one shared in-house `internal/chrome` package — do NOT import lipgloss/bubbles.**

| Stance | Avg | Verdict |
|--------|-----|---------|
| session-map-signature | 85 | The *outcome* — fold into the roadmap, not a separate architecture |
| **elevated-handroll** | **84** | **Recommended** |
| charm-native-rewrite | 77 | Rejected (see below) |
| design-system-unification | 75 | The *mechanism* — fold in |

### Why elevate-handroll over charm-native (lipgloss/bubbles)

The charm-native bet is seductive — lipgloss's compositor gives width-correct borders for
free and deletes ~30-40% of UI code. **We reject it for three supervision-specific reasons:**

1. **The signature screen cannot be a lipgloss program.** The Ctrl-^ overlay is *not* a
   `tea.Model` — it is a synchronous, blocking-read, bottom-pinned DECSC/DECRC painter over
   a live PTY (`session.go:1536`), deliberately alt-screen-free so it never collides with a
   remote vim/tmux. lipgloss buys nothing here and bubbles cannot run here. The screen that
   most needs polish is exactly the one the charm rewrite can't reach — so we'd end up
   hand-rolling it anyway, now as the *only* non-conformant surface.
2. **Re-introducing styling drift is the disease the SGR-role system cures.** Importing
   lipgloss means two styling vocabularies (SGR roles for the overlay, lipgloss styles for
   the rest) — the exact cross-screen drift `internal/termstyle` exists to prevent.
3. **Dependency-light + fast-startup is a product value.** Three direct deps is a feature.

The width-correctness win lipgloss promises is available without it: `ansi.StringWidth` is
**already an indirect dependency** (`go.mod`, via bubbletea v2). We get cell-accurate width
by promoting one transitive dep to direct — zero new dependency, zero startup cost.

### Why elevate over pure design-system-unification

Unification (extract `internal/chrome`, delete forks) is *necessary but not sufficient*. It
fixes coherence (N3) but not the substrate (N1), search (N4), motion (N5), or the signature
screen (N6). The elevated path **contains** unification as Phase 2 and pushes craft to the
max on top of it.

### Follow passage's lead — and where to diverge

- **Follow passage:** the cell-accurate-width migration (`ansi.StringWidth`), the
  GlyphSet capability tier, and the centralized `humanizeRelative` helper. These are solved
  problems in the sibling repo; port the pattern, don't reinvent.
- **Diverge from passage:** ssherpa supervises a **live PTY**. passage has no equivalent of
  the Ctrl-^ overlay, the escape rope, the output tap, the muxer guard, or SIGWINCH-driven
  repaint over a running stream. Every motion/redraw decision here must clear a bar passage
  never faces: *never schedule a timer on the supervised path; restore the underlying screen
  byte-exactly.* The shared `internal/chrome` package must own **string builders only** —
  never the DECSC/DECRC paint mechanics, which stay in `internal/session`.

**The thesis:** same three direct deps, same launch model, same OpenSSH-as-truth — but
every screen now measures right, sanitizes right, ranks right, degrades right, and looks
like a sibling of the one beside it, while the marquee overlay becomes the reference
implementation instead of the forked outlier.

---

## 4. The Roadmap

Ordered by dependency and impact-per-effort. Each item carries its feasibility verdict;
items that were refuted as written are **reframed or dropped** with reasons.

### Phase 0 — Foundation (substrate correctness; everything tears more elaborately without it)

| id | What | Why | Files | Effort | Risk | Feasibility |
|----|------|-----|-------|--------|------|-------------|
| **S1** | Cell-accurate width via `ansi.StringWidth`; rewrite `Truncate`'s inner accumulator to grapheme cell-width; atomic test re-baseline | One change stops box tearing across all 14 screens + the overlay | `termstyle.go` (`VisibleWidth:17`, `Truncate:194`), `termstyle_test.go` | **M** (up from S) | medium | ✅ with expanded boundary |
| **S2-a** | Unconditional `Sanitize` (not `Strip`) at the **chrome** sinks `truncateStyled` (`workflow_shell.go:138`) + `truncateOverlayLine` (`session.go:1958`) | Defense-in-depth at trusted chrome an operator reads mid-incident | as listed | S | low | ✅ |
| **S3** | **Idle-cold invariant test** for the supervised overlay | Converts the single most important safety property from convention to CI contract | new test in `internal/session/`, `sessionview_test.go` | M | low | ✅ |

**S1 feasibility note (load-bearing — read this):** `VisibleWidth` is a clean swap, but the
proposal's "single function" framing is wrong. Two additional things break and must land in
the same commit: (a) `termstyle_test.go:80` "esc before control byte" (`\x1b\x01x`) regresses
2→1 because `ansi.StringWidth` treats C0 `\x01` as zero-width — **not in the disclosed
re-baseline**; (b) `Truncate` counts runes internally (`termstyle.go:207-229`) but its width
contract is now cells — `TestTruncate` "multibyte cut" violates its own `VisibleWidth(got) <=
width` invariant. **Co-fix:** rewrite `Truncate`'s inner loop to accumulate grapheme cell
width, and audit the two out-of-package rune-slicing truncators the new contract silently
invalidates — `replayTruncateLine` (`cli/session.go:1144`) and `session.go:1962` — which
render over the live terminal / privacy-sensitive transcript. ASCII output stays byte-identical
(`ansi.StringWidth == rune count` for ASCII), so the supervised stream is provably untouched.
Reclassified **S → M**.

**S2 feasibility note — SPLIT, do not apply blindly:** The original S2 wanted `Sanitize` as
the unconditional first statement of **all three** sinks. **This is refuted for the third
sink.** `truncateVisible` (`sessionview.go:1991`) is the sole render path for **raw transcript
body lines** (`View → visibleLines:1068 → boxLine:1013`); raw mode deliberately preserves
escapes (`transcript.go:801`) behind the `r` two-step trust gate, and `transcript_test.go:411`
pins that `\x1b]0;evil` survives. Unconditional `Sanitize` there silently defeats the
operator's confirmed choice to view raw bytes. **Therefore S2 applies only to the two chrome
sinks (`truncateStyled`, `truncateOverlayLine`).** For the transcript body, leave the existing
`m.raw`-gated path; if a sink guard is wanted, gate it on `!m.raw`.

**S3 feasibility note:** The overlay path (`sessionOverlayLines`/`MapView`) is non-`tea`
synchronous code, so "no tick scheduled" is provable *structurally* — assert no
`tea.Tick`/`time.After`/`go`-func + a `runtime.NumGoroutine()` delta around a paint. Do **not**
claim a clock can be injected into package-level `tea.Tick`. Split frame-equality by layer:
byte-identity + ASCII-stability on `sessionOverlayLines` (pure, fileless); cover the real
`\x1b7`/`\x1b8` emission in a separate pty-backed test (reuse `session_test.go`'s harness) —
note `drawSessionOverlay(nil,...)` hits the non-terminal branch and skips the escapes. The
cold-path guard (sub-claims a,b) is width-independent and should land **first**, before S1 and
before any motion.

### Phase 1 — Design System (coherence becomes a property of shared code)

| id | What | Why | Files | Effort | Risk | Feasibility |
|----|------|-----|-------|--------|------|-------------|
| **S4** | Promote box builders into `internal/chrome` (`BoxTop/Divider/Bottom/Line/Edge/Truncate` taking `termstyle.Theme`); both `renderWorkflowShell` and `MapView` render through it; delete the `sessionview` fork | Width+sanitize fixes land once, reach both surfaces; map renderers can never disagree | new `internal/chrome/`, `workflow_shell.go`, `sessionview.go:1944-1999` | M | medium | ✅ — reframe |
| **S5** | Full-frame golden-render harness across standalone programs | Makes S6/S8/S9/S10 a re-baseline, not a brittle-edit slog; 14-program drift becomes a failing test | golden tests **in package `ui`**, plus separate `sessionview.MapView` case | **L** | low | ✅ — relocate |
| **S6** | Shared `chrome.Footer([]KeyHint)` (one grammar, `+N` overflow, `?` help) + `chrome.KVRow(label,value,gutter)` on one gutter | Cheapest highest-coherence win; kills 15-footer + 6-KV-gutter drift | `internal/chrome/footer.go`+`kvrow.go`, ~14 ui call-sites | M | low | ✅ — scope to ui |

**S4 feasibility note — reframe from "delete verbatim fork" to "unify near-duplicates":** The
two impls are **NOT verbatim**. `workflowEdge` border-styles the edge fill dashes; `boxEdge`
leaves them default-colored (confirmed at `workflow_shell.go:109` vs `sessionview.go:1961`).
A naive merge silently recolors one surface — and one of them is the live overlay in color
mode. **Decide and pin the canonical edge styling (recommend border-colored fill) with a
golden-string test (color + NO_COLOR) before deleting anything.** Reconcile signatures:
`workflow*` take `pickerTheme`, `box*` take `termstyle.Theme` — standardize on
`termstyle.Theme`, unwrap `pickerTheme.theme` at call sites, keep `pickerTheme`-bound
`workflowProgress`/`fitWorkflowProgress` in `ui`. `chrome` owns **strings only**; the
DECSC/DECRC paint stays in `session`.

**S5 feasibility note — relocate:** `internal/chrome` cannot reach unexported
`pickerModel`/`hostChooserModel`/their `View()`. **The harness must live in package `ui`**
(mirroring `host_chooser_test.go`), with the one exported `sessionview.MapView` case
separate. Scope the matrix to what exists: NO_COLOR (`WithNoColor(true)`) and narrow widths
(48-col clamp) are real and high-value. Treat wide-rune CJK and ASCII-glyph cases as
**regression pins** of current behavior that flip green only after S1 / S10 land. Reframe
`assertSameChrome` from an equality assertion to a **characterize-drift** snapshot diff (it
can't pass until S4/S6 land).

**S6 feasibility note — exclude sessionview:** Land **after** S4. Limit to the ~14
`workflow_shell` screens; **explicitly exclude `internal/sessionview`** (its `boxLine`
substrate sits over the live PTY). Canonical grammar: single verb, lowercase keys,
single-space `/` (matching sessionview's existing grammar). Update the pinned assertions in
the same commit: `picker_test.go:175/178`, `authkeys_test.go:67`, `host_chooser_test.go:226`,
`management_chooser_test.go:67`. `chrome.Footer` computes the `+N` marker from structured
hints **before** truncation.

### Phase 2 — Search & Layout

| id | What | Why | Files | Effort | Risk | Feasibility |
|----|------|-----|-------|--------|------|-------------|
| **S7** | `internal/fuzzy.Match(cand,query) → (matched, score, positions)` (fzf weights); per-field scoring; rank **within Hosts group only** | Boolean subsequence → ranked relevance + match positions feeding S8 | new `internal/fuzzy/`, `picker.go:1167`, +5 filter sites | M | medium | ✅ + amendments |
| **S8** | `RoleMatch` highlight role + match-char highlighting in list rows | Users can't see *why* a row matched | `theme.go` (both builtins + `Roles()` + `theme_editor.go:563`), row renderers | M | medium | ✅ — split off the fill bar |
| **S9** | One `chrome.ListView` collapsing the 3+1 forked scroll engines (index + callback based) | PgUp/Home/End/shift-arrow + "more above/below" differ per screen; ~750 lines → one feel | new `internal/chrome/listview.go`, `picker/host_chooser/transfer_browser` | **L** | high | ✅ — index-based, per-screen migration |

**S7 amendments:** (a) Add `SourceLine int` to picker `Item` and populate at
`BuildItems:237` — the "SourceLine ASC" tiebreak is dead text without it. (b) Re-sort **only
the contiguous `m.filtered` sub-range where `Group=="Hosts"`** (the final block, after
Actions), located by group identity not index; pinned groups keep config order so
`jumpSection`/`filteredGroup` stay valid (guarded by `TestPickerShiftArrowsJumpSections`).
(c) Pin host_chooser's Done/action row out of the ranked region. Keep `sessionview` out of
scope (mode-filtered, not fuzzy).

**S8 note — do NOT ship as one wrapper.** Hard prerequisites S1 and S7 must land first.
**Split the filled selection bar off from the highlight:** today rows are self-resetting
styled segments joined with plain `PadRight` padding, so a background bar won't span
gaps/padding without restructuring each renderer's composition (open-bg → fg-only segments
→ close-bg-after-padding). Ship cheap `RoleMatch` *foreground* highlighting decoupled from the
bar. Add `RoleMatch` in **all** of `TerminalTheme`/`VividTheme`/`Roles()`/`roleAliases` **and**
the hardcoded `themeRoles` slice (`theme_editor.go:563`), with a 16-color-safe bar code (not
truecolor-only). **Security-critical:** the remote SFTP file picker is the one net-new path
where attacker-influenced bytes flow into per-cell SGR — keep it strictly **Sanitize-then-
highlight** (gate on S2).

**S9 note — index-based, strict migration.** Build `ListView` on indices + injected callbacks
(`groupOf(i)`, injected `height`, `rowFunc(i, selected)`) — **not** a generic `ListView[T]` —
to bridge `[]Item` vs `[]hostChooserItem` without a type-level seam. Preserve the variable-
height group-header cost model exactly (+1 new group, +1 blank separator when rendered>0).
Port existing window tests as ListView tests **before** migrating. Per the hard ordering
constraint, land S7 ranking + ListView in the **same commit per screen** so two scroll
engines never coexist. Standardize the single `>` cursor here.

### Phase 3 — Motion & Feedback (only inside standalone alt-screen programs that own their terminal)

| id | What | Why | Files | Effort | Risk | Feasibility |
|----|------|-----|-------|--------|------|-------------|
| **S10-a** | `runProgram[T]` launch helper + `tea.WithColorProfile(Detect(...))` on every program + wire the dead `VividTheme`/`BaseName` path | Truecolor SGR downsamples to 256/16/mono automatically; one place owns launch wiring + 7 fallback widths | `internal/cli/`, `theme.go:155` | M | low | ✅ — land now |
| **S10-b** | GlyphSet (rounded vs ASCII) charset detection + threading; auto-Vivid on `COLORTERM=truecolor` | Legacy/non-UTF8 TERM shows tofu today | `theme.go`, 9 chrome files | M | medium | ⚠️ defer — gate behind S1/S4/contrast measurement |
| **S11** | Gated spinner+elapsed for SFTP transfer + dial wait (**indeterminate only**) | Multi-GB upload is a frozen black screen → frozen complete with zero bytes | new `chrome/spinner.go`, `transfer_*.go`, `cli/transfer.go` | L | high | ⚠️ — determinate bar **dropped** |
| **S12** | Live `ssherpa check` probe board: per-host status + N/M count, bounded concurrency, gated tick | Synchronous 5s SSH + 2s ICMP per host with no feedback is indistinguishable from a hang | `cli/tui_features.go`, `check_results.go` | M+ | medium | ✅ — after S3/S6/S9/S10/S11 |

**S10 note — split.** The WithColorProfile half is the high-value, no-new-dep, no-supervision-
danger three-quarters: `colorprofile` is already an indirect dep; bubbletea v2 re-parses
`View()` content and downsamples via the renderer (`cursed_renderer.go:268,596`), covering all
14 programs **and** the overlay (which renders via `tea.NewView`). The raw-stdout non-tea paths
(theme preview, dumps) wrap in `colorprofile.NewWriter`. **Land S10-a now.** The GlyphSet half
is genuinely new hand-rolled charset detection with a 9-file blast radius, and auto-Vivid is an
appearance change for every truecolor user that amplifies the rune-width bug via Vivid's filled
`48;2` pill — **gate S10-b behind S1/S4 and behind measured 3:1 contrast** (note `RoleSubtle`
and `RoleBorder` are currently identical `38;2;58;69;87`).

**S11 note — DROP the determinate bar.** ssherpa runs `sftp -b -` (`sshcmd.go:258`), which
suppresses the progress meter; `proc.Run()` (`transfer.go:454`) exposes **no byte counter**;
and switching to `pkg/sftp` is correctly forbidden (loses ProxyJump-route correctness). `os.Stat`
gives the denominator but never the numerator. **Ship indeterminate spinner+elapsed only**
(plus an optional static "uploading X of 1.4 GB…" label). Required guardrails: run sftp/ssh as
an **async `tea.Cmd`** so the program can tick while it blocks (preserving `runSFTPReceive`'s
temp-file+rename overwrite safety, `transfer.go:249-283`); gate the tick behind a busy flag **and
return `nil` (not the next tick) on completion** so idle goes cold before the supervised session
resumes; gate the whole spinner program behind **TTY detection** so `--print`/piped paths stay
plain; keep every target its own `tea.NewProgram` inside the existing `suspendTerminal` window.
*If* true byte progress is ever wanted, the only honest path is the in-band overlay sender
(`session.go:1304`), which chunks the payload itself.

### Phase 4 — Session-Map Signature (the marquee surface becomes the reference implementation)

| id | What | Why | Files | Effort | Risk | Feasibility |
|----|------|-----|-------|--------|------|-------------|
| **S13-std** | Shared `mapBody(records,currentID,scroll,w,h)` + breadcrumb + scroll/cursor in the **standalone** map; fix lowercase title | The deep ProxyJump chain you open the map to read is unreachable | `sessionview.go` (`MapView:148`), `session.go:1708/1716` | L | medium | ✅ — standalone half only |
| **S13-live** | Wire up/down/PgUp into the **live overlay** loop | Same, over the supervised stream | `session.go` overlay loop | L | high | ⚠️ — **blocked**, see note |
| **S14** | Per-node liveness badges (reconnect try, REC, tmux-held, latency) read synchronously at paint time | Map shows only static active/exited glyph | `sessionview.go` (`statusMarker`, `HealthSummary:2005`, `MuxerSummary`) | M | low | ✅ |
| **S15** | SIGWINCH-aware overlay repaint (eliminate stale-coordinate corruption) | Resizing while the map is open lands DECRC on the wrong cell over the live stream | `session.go` (`forwardSignals:1991`, overlay loop) | **L** (up from M) | high | ✅ — reframe concurrency |
| **S16-a** | Escape-rope confirm enumerates blast radius (named targets, count, depth) | Tears down the whole tree but the confirm is a flat 3-line strip, identical weight to any notice | `session.go` (`drawEscapeConfirm:1496`) | M | medium | ✅ — panel only |
| **S16-b** | Hold-to-confirm arm meter | Match confirm weight to blast radius | as above | — | — | ❌ **dropped** |

**S13-live is BLOCKED as written — this is the single most important feasibility finding in
Phase 4.** The live overlay reads **one byte at a time** and already treats `0x1b` (ESC) as
*close overlay* (`session.go:1237`). Every arrow/PgUp key is a multi-byte escape sequence
*beginning with* `0x1b`, so the first byte of up-arrow closes the overlay and the trailing
bytes **leak into the supervised PTY** (`session.go:1150`). There is no input escape-sequence
parser in the session package (`osc_tracker` parses *output*, not keystrokes), and the same
single-byte-ESC-cancel idiom is shared by the composer. **Defer S13-live until a minimal input
ANSI decoder exists with an explicit rule distinguishing a lone ESC (close) from an
arrow/PgUp prefix, AND S3's golden harness actually covers `drawSessionOverlay`.** Ship
S13-std now: extract `mapBody`, wire cursor/scroll into the standalone `mapModel` via
`KeyPressMsg.String()`, fix the lowercase title at `session.go:1708/1716`. Add cell-accurate
truncation (S1) for lineage labels before they scroll into view.

**S14 note:** Pure additive string appends into lines already passing through `cleanField` +
`truncateOverlayLine`; no tick, no goroutine, paint-on-event only. Two accuracy adjustments:
(a) the live recorder's pause/resume flag lives only in the in-memory `sessionRecorder`, so a
disk-derived REC badge shows *recording-exists*, not *live active* — label it conservatively
or source from the in-process recorder; (b) surface recording **on/off only, never a transcript
byte**. Enumerate each new field through `cleanField`/`Sanitize` (S2).

**S15 note — the sketch's concurrency model is wrong; the bug is real.** `drawSessionOverlay`
recomputes `startRow` from current height (`session.go:1561`) but `clearSessionOverlay` restores
from a frame captured *before* the resize → stale rows uncleared, DECRC on the wrong cell. The
proposed `select{ stdin-read ; resizeCh }` is **impossible** — Go's `select` can't wait on a
blocking `os.File.Read`. And the "clear-old-redraw under `lockedWriter`" mitigation
**deadlocks**: `showSessionOverlay` holds `output.mu` for the entire overlay lifetime across
every blocking read, so the SIGWINCH goroutine can never acquire it. **Correct design:** drive
the repaint from *inside* the overlay loop — pump stdin into a channel via a tiny goroutine, then
`select` on `byteCh`/`resizeCh`; recompute clear coordinates from current `term.GetSize` and
clear `max(old,new)` rows rather than trusting stale DECRC. A cheaper lower-blast-radius variant:
in `clearSessionOverlay`, clamp against current size and clear the full bottom band on detected
change. **Re-estimate M → L.** Add a stress test interleaving rapid resize + paint.

**SHIPPED (cheaper variant).** Chose the lower-blast-radius fix, not the stdin-pump rewrite: the
full `select{byteCh/resizeCh}` repaint touches the supervised hot path (idle-cold invariant +
`output.mu` lifetime) for marginal gain over fixing the actual corruption. `clearSessionOverlay`
now takes `stdin`, re-measures via `overlaySize`, and — when the height changed since paint —
also clears the current bottom band (same height) before DECRC, so a SIGWINCH between paint and
clear can't leave residue over the live stream. The overlay still sits at its draw-time position
until the next keypress (no live repaint), but closing it is now resize-safe. Covered by
`overlay_resize_test.go` (PTY `Setsize` between frame-capture and clear; asserts both the
draw-time band and the post-resize bottom band are cleared, and the no-resize path stays
byte-minimal). The stdin-pump live-repaint remains a documented follow-on if live resize-repaint
(vs. resize-safe-close) is ever wanted.

**S16-b is dropped.** The escape confirm is a raw single-byte stdin loop, **not a `tea`
program**, so Bubble Tea v2 key-release events are unreachable; raw mode has no key-release at
all (option 1 fails); and auto-repeat is byte-identical to deliberate taps (option 2 without a
wall clock fails). A meter cannot both arm deliberately and stay idle-cold. **Ship S16-a only:
the enumerated `RoleDanger` panel** ("Tear down 4 sessions across 3 layers: bastion → db1 →
db1-shadow?"), counting **active descendants only** (`state.ListRecords` includes exited +
synthesized nodes — counting raw would make the confirm lie), keep the existing 3-tap panic
bypass unchanged, write an audit line on fire.

### Phase 5 — Delight (low-risk signposting; pure-render, no supervision surface)

| id | What | Files | Effort | Risk | Feasibility |
|----|------|-------|--------|------|-------------|
| **G1** | One-shot idle-cold connect banner teaching Ctrl-^ before raw-mode handoff (persisted seen-count, env override) | `session.go`, `internal/state` | S | low | ✅ — gate precisely |
| **G2** | First-run empty-inventory welcome distinct from zero-match | `picker.go` | S | low | ✅ — branch in `View`, not the list path |
| **G3** | Home `?` help overlay + both-map-door cross-reference + Docs explainer | `picker.go`, `tui_features.go` | M | low | ✅ |
| **G7** | Shared session-map keymap/footer/title as data; fix `v`/`x` collision + casing | `sessionview`, `session.go`, `chrome` | M | medium | ✅ — 3 surfaces |
| **G9** | Escape-rope confirm copy enumerates live blast radius (folds into S16-a) | `session.go` | M | medium | ✅ — active-only |

**G1 note:** Gate precisely — print only when `terminalInput && !opts.Detached && record.Depth==0
&& seenCount<N && SSHERPA_NO_CONNECT_HINT==""` (read via `opts.Env`, not `os.Getenv`). Place
between `session.go:400` (terminalInput computed) and `:401` (makeRaw). The write happens before
`pty.Start` and raw mode → cannot desync. Persist seen-count as a tiny atomic JSON file modeled on
`identity.json`; any read/write error degrades to showing the hint, never blocks the connect.

**G2 note:** Zero hosts does **not** produce an empty list — Action rows always render
(`picker.go:221-234`). The welcome must be a new `(query=="" && no ItemAlias)` branch in
`View`/`renderBody`, **not** a swap of the `No matches` path. Point users to the `Add` action
(and Sessions/route-map), **not** Ctrl-^ — the overlay is only reachable inside a live session, and
a first-run machine has none.

**G7 note — three surfaces, not two:** the live overlay (`drawSessionOverlay`), the interactive map
footer, and the divergent standalone `listModel` (the real outlier, hand-built uppercase `SESSIONS`
+ its own footer). `v`/`x` mean different things across faces (receive/escape-rope vs active/exited
filter) — remap the standalone filters off `v`/`x`, reserve `v=receive`/`X=escape-rope` globally.
Note `MapAction` is already a result enum (`sessionview.go:60`) — name the table `sessionActions`.
Do **not** touch the overlay draw mechanics.

### Dropped / out-of-scope
- **G4/G5 (overlay alt-screen scrollback via DECSET 1049):** ✗ Switching the overlay to DECSET
  1049 nests/collides with a supervised remote already in alt-screen (vim/tmux/less), which
  `osc_tracker` never tracks (it watches OSC 7/133/777, not DEC private mode 1049/47), and the
  session keeps no local cell model to repaint from. **Keep the DECSC/DECRC bottom-strip approach.**
  The cell-accurate-truncate piece of G5 folds into S1.
- **S16-b arm meter:** dropped (above).
- **G10 in-band-send determinate byte bar:** deferred — `InbandSend` is synchronous with no
  progress callback; requires backend plumbing first, and any progress render must be the *sole*
  writer to the PTY during the handshake.

---

## 5. Before / After

### Host picker

**Before (today):** rune-counted width tears on `日本-east`; boolean filter, no highlight,
config-order results; `>>` cursor; cramped slash footer truncated with `~`; bare `No matches`.

```
  ╭ SSHERPA ─────────────────────────────────────────────────╮
  │ filter: prod                                    12 hosts  │
  ├───────────────────────────────────────────────────────────┤
  │ >> prod-db1                                               │
  │    prod-db-shadow                                         │
  │    日本-east                                             │ ← border drifts (wide rune)
  │    No matches                                            │ ← (when filter excludes all)
  ├───────────────────────────────────────────────────────────┤
  │ enter select / type filter / arrows move / R refresh / Q~│ ← hints cut off
  ╰───────────────────────────────────────────────────────────╯
```

**After (elevated):** cell-accurate border holds; ranked fuzzy with `RoleMatch` highlight;
one `>` cursor; canonical footer with `?`; responsive master-detail; post-select dial spinner.

```
  ╭ SSHERPA  v1.4.0  CONNECT ──────────────────────────────────────────╮
  │ STATUS   2 active sessions · 1 saved forward                       │
  │ FILTER   prod▏                                            3/41      │
  │                                                                    │
  │   ▌ HOSTS                              │  prod-db1                 │
  │ >  pro̲d̲-db1      ──▶ db1.internal      │  ─────────────────────    │
  │    staging-pro̲d̲  web.stg.internal      │  HostName  10.0.2.7       │
  │    日本-east     tokyo.internal        │  ProxyJump bastion        │
  │                                        │  Port      22             │
  ├────────────────────────────────────────────────────────────────────┤
  │ enter connect · / filter · ^ map · ⇧↑↓ section · R refresh · ? help │
  ╰────────────────────────────────────────────────────────────────────╯
   ⠹ dialing prod-db1 via bastion…  3.2s      (spinner: alt-screen only, idle-cold)
```

NO_COLOR + ASCII GlyphSet (same widths, same grammar):

```
  + SSHERPA v1.4.0 CONNECT -------------------------------------------+
  | FILTER  prod_                                            3/41      |
  | >  prod-db1      --> db1.internal                                 |
  |    staging-prod  web.stg.internal                                 |
  + enter connect / filter / map / ? help ---------------------------+
```

### Session-map overlay (Ctrl-^, over a LIVE ssh PTY)

**Before (today):** flat strip, lineage truncated to `... N more`, no scroll, no liveness,
rune-counted width tears over live scrollback, lowercase title.

```
  …prod-db1:~$ tail -f /var/log/app.log    ← live scrollback (frozen for overlay)
  ╭ ssherpa session map ────────────────────────────────────╮
  │ route: you → bastion → prod-db1                          │
  │ ● prod-db1   active                                      │
  │ ... 3 more line(s)                                       │ ← the chain you opened to read
  │ q close · X escape-rope · r refresh                      │
  ╰──────────────────────────────────────────────────────────╯
```

**After (elevated):** rendered through the SAME `chrome` primitives as the picker;
scrollable lineage tree (standalone now, live after the input-decoder lands); per-node
liveness read synchronously at paint; "you are here" breadcrumb; every line cell-measured
and unconditionally `Sanitize`d; idle-cold, event-driven, no tick; DECRC restores byte-exact.

```
  …prod-db1:~$ tail -f /var/log/app.log    ← live scrollback, restored byte-exact on close
  ╭ SSHERPA SESSION MAP ──────────────────────  active 4 · exited 1 ──╮
  │ ROUTE    you ─▶ bastion.edge ─▶ prod-db1      here ▸ db1-shadow    │
  │ ├─ ● ssh-bastion     active   12ms  REC ·  tmux-held              │
  │ │  └─ ● prod-db1     active   31ms                                │
  │ │     ├─▶ ● db1-shadow  reconnecting (try 3)   ◀ you              │
  │ │     └─ ● db1-replica  active   28ms                             │
  │ └─ ○ staging-web     exited(0)   3m ago                           │
  │ ▼ 2 more — ↓ to scroll                                            │
  ├──────────────────────────────────────────────────────────────────┤
  │ ↑↓ move · r refresh · t REC · s send · v recv · X rope · ? help   │
  ╰──────────────────────────────────────────────────────────────────╯
```

Escape-rope confirm (S16-a — enumerated, no meter):

```
  ╭ ESCAPE ROPE ──────────────────────────────────────────────────────╮
  │ ⚠  Tear down 4 supervised sessions across 3 layers:               │
  │      bastion → prod-db1 → db1-shadow, db1-replica                  │
  │    X confirm · any other key cancels                              │
  ╰────────────────────────────────────────────────────────────────────╯
```

---

## 6. Degradation & Supervision-Safety Guarantees

**Color/degradation (N7):**
- One `colorprofile.Detect` (already an indirect dep) → `tea.WithColorProfile` on every
  program; bubbletea v2 re-parses `View()` content and downsamples truecolor → 256/16/mono
  in the renderer (`cursed_renderer.go:268`), covering all 14 programs **and** the overlay.
  Raw-stdout non-tea paths wrap in `colorprofile.NewWriter`. NO_COLOR honored centrally in
  `termstyle.Apply`.
- GlyphSet (S10-b) degrades toward the safer ASCII tier on charset uncertainty; the ASCII
  tier actually *mitigates* the box-drawing tear on non-UTF8 terminals.
- Narrow widths: golden harness (S5) pins border integrity at the 48-col clamp; footers use
  progressive disclosure (`+N`) instead of silent `~` truncation.
- `--no-alt-screen`: every program sets `view.AltScreen = !m.noAltScreen` already; the
  `runProgram` helper (S10-a) centralizes this so it can't drift.

**Supervision safety (N8) — the non-negotiable invariants:**
- **Idle stays cold.** S3 makes this a CI contract: the overlay path schedules zero timers,
  starts no goroutine, repeats byte-identically across identical paints. Every motion item
  (S11/S12) lives only in standalone alt-screen programs, every tick gated behind a busy flag
  that **returns `nil` at rest** (the existing unconditional `transcriptTick` is the wrong
  model — S11 improves on it).
- **ASCII byte-identity across the width migration.** S1 changes nothing for ASCII
  (`ansi.StringWidth == rune count`), proving the supervised stream is untouched — pinned by
  S3's ASCII-stability assertion.
- **Bottom-strip paint only, never DECSET 1049.** The overlay keeps DECSC/DECRC; we
  deliberately reject alt-screen scrollback save (would collide with a remote's own
  alt-screen state that `osc_tracker` doesn't track). S15 fixes stale-coordinate restore by
  recomputing against current `term.GetSize` and clearing `max(old,new)` rows.
- **Sanitize at every chrome sink (N2).** S1's cell-accurate truncate + S2's unconditional
  `Sanitize` close the short-OSC7-ESC vector at the trusted-chrome boundary; S8's remote SFTP
  highlight path is strictly Sanitize-then-highlight.
- **Live-input ANSI decoding is a hard prerequisite for S13-live**, precisely because the
  current single-byte loop would leak arrow-key bytes into the PTY.

**Transcript privacy (N2):** Recording state is surfaced **on/off only, never a byte** (S14).
The raw transcript body path keeps its `r` two-step trust gate — **S2 is explicitly NOT
applied to `truncateVisible`** so the operator's confirmed raw view survives
(`transcript_test.go:411`).

---

## 7. Risks, Tradeoffs & What We Will Not Do

**Tradeoffs we accept:**
- **The signature screen's wow is correctness-led, not animation-led.** By keeping the live
  overlay event-driven and lipgloss-free, we trade flashy motion for *calm, incorruptible,
  finally-reachable depth*. This is the right trade over a live PTY — and it's honest.
- **S5 is a real L, not free.** A golden harness is the tentpole that turns S6/S8/S9/S10 from
  brittle-edit slogs into re-baselines; budget it explicitly.
- **S9/S15 are genuinely high-risk.** ListView touches the most-used navigation; S15 adds
  concurrency to the most corruption-sensitive path. Both land behind golden frames + stress
  tests, per-screen, never half-migrated.

**What we will deliberately NOT do:**
1. **Not merge the 14 `tea.NewProgram` launch points.** That machinery keeps non-interactive
   CLI paths trivial and keeps the supervised overlay a separate, alt-screen-free paint target.
2. **Not import lipgloss or bubbles.** Re-introduces the cross-screen styling drift the
   SGR-role system exists to prevent, and can't reach the signature screen anyway.
3. **Not switch SFTP to `pkg/sftp`.** Loses ProxyJump-route correctness that piping through
   real `ssh` provides. Consequence: **no determinate transfer bar** (S11 ships indeterminate).
4. **Not switch the overlay to DECSET 1049 alt-screen.** Collides with a supervised remote's
   own alt-screen; `osc_tracker` doesn't track mode 1049/47.
5. **Not build a hold-to-confirm arm meter on the escape rope** (S16-b). Raw mode has no
   key-release; auto-repeat is byte-identical to taps; a meter can't be both deliberate and
   idle-cold. Ship the enumerated danger panel instead.
6. **Not schedule any timer on the supervised path, ever.** CI-enforced by S3.

---

## 8. Recommended First PR

**Scope: Phase 0 substrate + the idle-cold contract. One PR, ~2-3 focused commits, no new
dependency.** This is the non-negotiable first move — every later screen tears, leaks, and
desyncs more elaborately without it — and it ships a permanent safety net before any motion
work can regress it.

**Commit 1 — Idle-cold invariant test (S3 cold-path, width-independent, lands first):**
- Structural assertion that `sessionOverlayLines`/`MapView` reference no
  `tea.Tick`/`tea.Every`/`time.After`/`go`-func; `runtime.NumGoroutine()` delta around a paint.
- Byte-identity: two paints of `sessionOverlayLines` for identical records produce identical
  frames.
- Files: new `internal/session/overlay_cold_test.go`, `internal/sessionview/sessionview_test.go`.

**Commit 2 — Cell-accurate width substrate (S1, expanded boundary):**
- Rewrite `VisibleWidth` (`termstyle.go:17`) via `ansi.StringWidth` (promote the already-indirect
  `charm.land/x/ansi` to a direct dep).
- Rewrite `Truncate`'s inner accumulator (`termstyle.go:194`) from rune-count to grapheme
  cell-width so `VisibleWidth(got) <= width` holds for wide chars.
- Re-baseline **four** test concerns in the same commit: the ASCII `width==4` case stays, the two
  `len([]rune)` loops, the `日本語x` 4→7 case, **and** the omitted `esc before control byte`
  (`\x1b\x01x`) 2→1.
- Audit + fix the two out-of-package rune-slicing truncators the new contract invalidates:
  `replayTruncateLine` (`cli/session.go:1144`) and `session.go:1962`.
- Add an ASCII-byte-identity assertion (extend Commit 1's golden) proving the supervised stream
  is untouched.

**Commit 3 — Unconditional Sanitize at the chrome sinks (S2, scoped):**
- `Sanitize` as the first statement of `truncateStyled` (`workflow_shell.go:138`) and
  `truncateOverlayLine` (`session.go:1958`); switch the overflow branch from `Strip` to
  `Sanitize`.
- **Do NOT touch `truncateVisible`** (`sessionview.go:1991`) — it renders raw transcript bodies
  behind the `r` trust gate.
- Add a test feeding a sub-width raw ESC/C1 byte to each chrome sink, asserting absence in output;
  add a regression test that an emoji/CJK transcript-replay line never exceeds `width` cells.

**Why this first:** it fixes N1 (box integrity) app-wide and the N2 chrome vector in one stroke,
establishes the N8 CI contract that gates all future motion, adds zero dependencies and zero
startup cost, and is provably safe for the supervised stream (ASCII byte-identical). It unblocks
S4 (the `chrome` package the whole design system rests on) and every screen downstream. Smallest
diff, highest and most permanent leverage.
