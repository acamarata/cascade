# Acceptance fixture provenance

Art.2.2 requires every fixture in this tree to record where it came from.
Most of what the drills in this package assert against is not a fixture at
all — they run the shipped binary against a real counterpart and read what
it says. The few files here are recorded below.

| File | Provenance |
|---|---|
| `j_s21_routing_golden.json` | Authored by hand from a real run of `cascade run --task classify --json` against the drill's local rehearsal endpoint, on 2026-09-17 (`v2.0.0-alpha.4-SNAPSHOT`). It pins the ENVELOPE SHAPE — which keys a dispatch answers with — not any provider's wire dialect. |
| `widget_parity_journal.template.md` | Authored by hand from the contract of the widget parity journal (23 locators) and the owner's widget v1 parity intake §1. It is the blank the owner-present session copies; the unfilled copy is red by test. |
| `widget_parity/seed/**` | Synthetic. One complete, passing journal with its evidence, written by hand to the SHAPE of real `codesign -dv --verbose=4`, `spctl -a -vv`, `notarytool` and `cascade widget install --json` output (2026-10-12). The artifact is a text stand-in, not a disk image, and the CDHash and spctl probes are recorded `codesign` and `spctl` outputs for that stand-in; the signer, team and submission id are made up. These files prove the VERIFIER's logic. They prove nothing about a real build: that claim belongs to the live lane (`TestWidgetParityJournal_Live`, tag `widgetparity`), which recomputes the hash and CDHash from the owner's real artifact. |

## What is deliberately not here

**No captured provider responses.** The rehearsal endpoint in
`j_s21_rehearsal_test.go` serves a dialect this repository wrote, and that
is stated in its own header: it proves the drill's script and the product's
wiring, never that a real provider answered. Art.2.4 names self-authored
dialect evidence as the trap; recording it here as a "fixture" would dress
the same evidence up as something stronger.

The real-counterpart claim belongs to the credential-gated lane
(`TestJ_S21_Acceptance_CompatSubEndToEnd`), which speaks to a live
subscription and captures nothing.
