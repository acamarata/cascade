# internal/events/scheduler/testdata — state provenance

This directory documents the provenance of test state used by this
package's tests (task 9's "state provenance in
internal/events/scheduler/testdata/README.md"), per
06-FORGE-SPEC.md §5.7/§5.12's provenance discipline (Article 2: every
fixture and seed traces to a named, reproducible source, never an
unexplained blob).

## Fuzz corpus

`FuzzCronParse`'s seed corpus itself lives at
`internal/testdata/fuzz/scheduler/cron_seeds.txt` (repo-wide convention:
fuzz corpora never live beside their owning package — see that
directory's own README.md for the seed list's contents and rationale).
This file is the pointer task 9 asks for; it is not a duplicate copy of
the corpus.

## In-test fixtures

Every other test in this package (`scheduler_test.go` and its R-14.117
siblings) constructs its own state programmatically at test time —
`storetest.NewMemStore()` for a real, empty `provider.Store`,
`testkit.NewFrozenClock` for a deterministic starting instant, and
literal `CronJob`/spec values chosen to exercise a specific behavior
(skip-missed, lock exclusion, orphaned-owner, overrun). None of this
package's tests read a golden/fixture FILE from disk — there is nothing
else in this directory to document provenance for. This README exists so
a future reader who expects fixture files here (as several sibling
packages have) finds an explicit "there are none, and here is why"
rather than an empty, unexplained directory.
