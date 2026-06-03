# TUI Old-Generation Backlog

This tracks screens that still feel visually older than the newer framed
workflow surfaces. The current new-generation language is:

- rounded frame with the title embedded in the border
- compact status/progress/context row when useful
- section headers inside the frame
- dense but readable key/value rows
- strong selected/status treatment
- footer actions contained inside the frame
- width-safe truncation and wrapping

The plan is to modernize everything except the homepage first. The homepage is
listed for completeness, but it is explicitly deferred until the non-home
surfaces are aligned.

## Deferred

### Home Page Picker

- Surface: main `ssherpa` screen.
- Code: `internal/cli/cli.go`, `internal/ui/picker.go`.
- Current issue: flat full-width picker with ASCII rules, filter band, list
  and preview styling from the older visual language.
- Status: defer until every non-home old-generation surface is done.
- Notes: this should remain behaviorally rich: grouped scrollable left list,
  right-side detail pane, filter, refresh, active session/tunnel counts,
  incoming SSH rows, saved forwards/proxies, and shift-arrow section jumps.

## Non-Homepage Worklist

### 1. Check Results Screen

- Surface: reachability results after `CHECK`.
- Code: `internal/ui/check_results.go`.
- Current issue: flat title/rule/table layout, old footer placement, no framed
  shell.
- Target: framed result screen with status summary, compact result rows,
  failure emphasis, and contained footer.

### 2. Theme Builder

- Surface: `THEME` editor.
- Code: `internal/ui/theme_editor.go`.
- Current issue: custom flat title/rules/split panels. It is functional, but
  visually older than the workflow shell.
- Target: framed editor shell with schema, preview, palette, message/warning,
  and footer integrated into one layout.

### 3. Transfer Direction Picker

- Surface: initial `Send file` / `Receive file` choice before the File Transfer
  Browser.
- Code: `internal/cli/transfer.go`.
- Current issue: still uses generic `ui.Pick`, so it looks older than the FTB
  and TCS it leads into.
- Target: small framed transfer start screen or a dedicated lightweight chooser
  using the same transfer workflow language.

### 4. Generic Alias Pickers

- Surfaces:
  - jump destination picker
  - proxy host picker
  - forward host picker
  - check host picker
  - transfer host picker
- Code: `internal/cli/route.go`, `internal/cli/tui_features.go`,
  `internal/cli/transfer.go`.
- Current issue: all call `ui.Pick` through `pickAlias` or similar helpers.
- Target: framed host chooser with filter, grouped host rows, preview/details,
  and the same footer grammar as the new screens.

### 5. Jump Hop Picker

- Surface: `Jump: add another hop or finish`.
- Code: `internal/cli/route.go`.
- Current issue: generic picker for a route-building workflow, visually older
  than the add/forward/proxy builders.
- Target: framed route-step chooser that shows destination, selected hops, and
  the remaining host choices without duplicating the homepage style.

### 6. Edit Menus

- Surfaces:
  - pick alias or saved preset to edit
  - alias edit/delete/back action picker
  - saved forward action picker
  - saved proxy action picker
- Code: `internal/cli/mutate.go`.
- Current issue: generic flat picker menus around otherwise newer form/confirm
  flows.
- Target: framed management chooser with clear object type, selected target
  context, and danger styling for destructive actions.

### 7. Check Reachability Menus

- Surfaces:
  - check mode picker
  - saved forward picker for one-forward checks
- Code: `internal/cli/tui_features.go`.
- Current issue: generic flat picker before a results screen.
- Target: framed check launcher with host/saved-forward context and concise
  action rows.

### 8. Docs Picker

- Surface: completions/manpage chooser.
- Code: `internal/cli/tui_features.go`.
- Current issue: generic flat picker for a small menu.
- Target: framed artifact chooser with path preview and contained footer.

### 9. authorized_keys Manager

- Surfaces:
  - main authorized_keys action menu
  - delete-key picker
- Code: `internal/cli/authkeys.go`.
- Current issue: generic picker surface and older visual hierarchy.
- Target: framed keys manager menu with clear action grouping, path context,
  and stronger key/fingerprint previews.

### 10. Generic `ui.Pick` Component For Non-Home Use

- Surface: shared picker used by most old-generation non-home screens.
- Code: `internal/ui/picker.go`.
- Current issue: it is the root of the old visual language.
- Target: add a new framed/non-home picker mode or a replacement chooser, then
  migrate non-home call sites first. Do not switch the homepage to the new
  picker until the deferred homepage pass.

## Transitional, Not First-Pass Old Gen

### Session Map

- Surface: `MAP` / session route map.
- Code: `internal/sessionview/sessionview.go`.
- Current state: already has a rounded frame and newer graphical route language.
- Reason not first pass: it is not fully shared-shell aligned, but it does not
  read as old-gen the way the flat picker and table screens do.
- Later target: unify frame helpers and footer/status handling with the shared
  shell system if it can be done without reducing the map's route readability.

## Already New Generation

- Add alias workflow: `internal/ui/add_form.go`
- Forward builder: `internal/ui/forward_builder.go`
- Proxy builder: `internal/ui/proxy_builder.go`
- Confirm screens: `internal/ui/confirm.go`
- Text prompts: `internal/ui/text_modal.go`
- File Transfer Browser: `internal/ui/transfer_browser.go`
- Transfer Complete Screen: `internal/ui/transfer_complete.go`

## Suggested Execution Order

1. Check results screen.
2. Transfer direction picker.
3. Generic alias chooser and jump hop chooser.
4. Edit menus.
5. Check menus.
6. Docs picker.
7. authorized_keys manager.
8. Theme builder.
9. Shared non-home `ui.Pick` cleanup after the call sites prove the pattern.
10. Homepage picker, deferred until last.
