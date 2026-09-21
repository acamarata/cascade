# internal/review testdata provenance

Per 12-QUALITY-CONSTITUTION.md Art.2.2, every fixture the no-network unit
lane replays must carry a stated provenance: what produced it, and when.

## fixtures/cr-review-session.json

- **Tool**: hand-authored, not captured from a live daemon session. This
  build environment had no running `cascade daemon` and no live model
  credentials available to this ticket's session (P1-E25-W5-S52-T4), so a
  genuine `cascade run`/`conductor.execute` round trip could not be
  recorded as part of this ticket, matching the disclosed-gap precedent
  `providers/anthropic/testdata/README.md` already records for the same
  reason.
- **Version**: built against this repository's `pkg/provider.ModelRequest`/
  `ModelResponse` wire shapes and `pkg/provider.ReviewRequest`/
  `ReviewResponse` types as of commit `3692cf274880948b623bd68da3b720c1d5cb2f21`
  (the execution_guidance source snapshot this ticket's contract records).
- **Date**: 2026-09-20; amended 2026-09-21 (CR fix D8).

**Amendment, 2026-09-21 (CR fix D8).** The fixture's recorded
`conductor_exchange.sensitivity` changed from `internal` to `restricted`: the
reviewer now dispatches at the `review` task class's own `SensitivityDefault`
from the real §5.16 table and never below it, so `internal` was the wrong wire
value. More importantly, the fixture is no longer decoded through a struct the
test itself declares. `codec_test.go`'s
`TestReviewProviderRealCounterpart_WireCodec` dials a real
`pkg/provider.Client` so the request is encoded by the REAL wire codec
(`ModelRequest.toWireParams`, the same translation
`internal/plugins/review_wiring.go`'s production executor goes through), decodes
the recorded `response` object through `provider.ModelResponse` itself, and
asserts `task_class`, `requirements` and `sensitivity` BOTH against
`conductor.TaskClasses()` and against the values recorded below. A fixture that
drifts off protocol now fails the test instead of being believed.

The fixture records one representative CR-B exchange: the caller-facing
`ReviewRequest` (Level/Diff/Context) internal/review's real-counterpart
test (`provider_test.go`'s Art.2 test, invoking `Provider.Review` through
the exact `pkg/provider.ReviewProvider` interface path an external caller
such as cascade-pbd would use) feeds in, and the structured JSON output a
conductor dispatch would return, which the real-counterpart test's fake
`provider.ModelExecutor` replays verbatim rather than a live daemon
round trip.

This is recorded honestly as a known gap against Art.2's "real
counterpart" standard: the shapes below reflect this repository's own
documented wire contracts, not a byte-for-byte capture of an actual
`conductor.execute` daemon exchange. When this reviewer is exercised
against a live daemon (outside this ticket's no-network unit-test scope),
the `integration`-tagged lane should capture the real bytes here in its
place.
