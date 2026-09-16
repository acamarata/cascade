# P9 dogfood fixtures — provenance

Five real planning tickets, harvested from the archived P9 planning corpus,
used as the inputs to `TestP9DogfoodGoldenParity` in
`plugins/pbd/dispatch_golden_test.go`.

## Where they came from

| fixture | model_class | source ticket | epic |
|---|---|---|---|
| `mech.json` | mech | `P9-E1-W1-S1-T1` | E-01 storage schema |
| `build.json` | build | `P9-E2-W1-S6-T1` | E-02 phase lifecycle |
| `heavy.json` | heavy | `P9-E3-W2-S1-T1` | E-03 one-app convergence |
| `review.json` | review | `P9-E4-W2-S7-T1` | E-04 cockpit UI |
| `arbiter.json` | arbiter | `P9-E5-W3-S7-T1` | E-05 model catalog |

Each fixture's `source_file` field names the ticket's path inside that
corpus. The `title`, `tasks` and `spec_refs` are the ticket's own text,
copied without editing. Captured 2026-09-16 from the P9 planning tree as it
stands in the v1 archive (`archive/p9-integration`), which is reference-only
per the repo's v1 rule — no v1 source enters this tree, only fixture data.

## The `model_class` values are the test axis, not corpus data

**Read this before changing a fixture.** The P9 corpus predates the
`model_class` field entirely. Its 207 ticket files carry a `model:` mapping
of harness-to-model names (`{cc: sonnet, oc: dsv4-pro}` and
`{cc: haiku, oc: dsv4-flash}` — two distinct values across the whole
corpus), and only two files mention the string `model_class` at all.

So the contract's "five P9 corpus tickets, one per model_class" cannot be
satisfied literally: those five tickets do not exist and never did. What is
recorded here instead:

- the ticket **content** is real, unedited corpus text — that is what makes
  the golden a meaningful lock on request assembly;
- the **`model_class`** on each is assigned by this test, one per §5.18
  class, so all five mapping rows are exercised.

Nothing here claims the P9 planners classified these tickets. Recorded as a
ruling in `15-T0-RULINGS-R14.md`.

## The `request` field

`request` is the `conductor.execute` params body the dispatch seam assembled
for that ticket, captured once by hand. There is deliberately no `-update`
flag: a golden that regenerates itself asserts nothing. To change one,
edit it by hand and say why in the ticket journal.

## Identifier sweep

These files carry harvested planning prose in a PUBLIC repo.
`TestGoldenTicketsCarryNoIdentifiers` re-runs the sweep on every test run so
a later refresh cannot reintroduce a path or account name silently.
