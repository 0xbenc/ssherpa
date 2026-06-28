# TUI Interaction Contract — pointer

The canonical interaction contract for ssherpa **and** passage lives in the passage repo:

> https://github.com/0xbenc/passage/blob/main/docs/flow-contract.md

It is the single source of truth for the shared interaction grammar (key bindings,
selection cue, filter semantics, footer grammar, quit convention). ssherpa-only surfaces
— the live-PTY session overlay (`internal/sessionview`) and the multi-step wizards
(`add_form`/`forward_builder`/`proxy_builder`) — are governed by it; see its **App-local**
section for the parts that stay ssherpa-specific (the overlay's `Strip`-on-overflow
raw-transcript policy and the `✓ ● ○` step-rail composition).

**Drift rule:** any PR that changes an interactive surface in this repo must keep ssherpa
conformant with the contract and, if it changes shared grammar, update the canonical
passage copy in a companion PR. This is a release-blocking checklist item — ssherpa CI
cannot gate a file in another repo; behavioral conformance here is gated by this repo's
golden/invariant tests (border integrity, Sanitize-on-overflow, footer grammar).
