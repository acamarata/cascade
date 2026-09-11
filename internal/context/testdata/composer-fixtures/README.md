# composer-fixtures provenance (Art.2.2)

Every fixture in this directory is self-authored for
`P1-E21-W5-S46-T1` (`internal/context/composer_invariant_test.go`). None of
it is harvested, copied, or derived from cascade v1 output or from any real
user conversation. Every `content` value the test constructs from these
fixtures is a synthetic placeholder string built from the fixture's own
`label` and `tokens` fields — never real prose, and never anything sourced
from a live run.

## valid-fixtures.yaml

A table of composer scenarios: a token `budget` and an ordered list of
`slots`, each declaring a `kind` (`tier`, `memory`, `retrieval`, `history`),
a `label`, and a declared `tokens` size. The test's `fixtureCounter` reads
the size directly off each slot's synthesized content (via an embedded
`|tokens=N` marker) rather than estimating it, so the fixture's declared
size is exactly what `Compose` measures — no drift between what the
fixture claims and what the test exercises.

The set spans: everything fits; a low-priority slot alone exceeds the
budget (must drop, not error); the highest-priority slot alone exceeds the
budget (must error, never silently proceed); a budget too small for
anything (drops everything but the first, or errors, depending on which
slot is first); and multiple slots per kind at varying sizes, to cover the
"tier-output size × memory-candidate count × retrieval-hit count" shape
this ticket's contract names.

## violation-fixture.yaml

One deliberately invalid `(budget, claimed_tokens_used)` pair. It is never
fed through `Compose` — `Compose` is designed to never produce this shape.
It exists solely to prove that the invariant-checking assertion the test
suite uses is itself capable of detecting a violation: the test builds a
`ComposedResult` directly from this fixture's numbers (bypassing `Compose`
entirely) and requires the checking helper to report it as invalid. If that
assertion ever passed this fixture, the checker would not be enforcing
anything, and the "passing" fixture-driven test above would be worthless.
