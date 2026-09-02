# internal/testdata/fuzz/scheduler — FuzzCronParse seed corpus

Seed corpus for `internal/events/scheduler.FuzzCronParse`
(`internal/events/scheduler/fuzz_test.go`), per 06-FORGE-SPEC.md §5.7
(fuzz corpora live under `internal/testdata/fuzz/`, never beside the
owning package — mirrors the `events/`, `config_literal/`, and
`manifest/` siblings already in this directory) and this ticket's
(P1-E03-W1-S04-T4) task 9.

`cron_seeds.txt` — one candidate `CronJob.Spec` string per line, read by
`FuzzCronParse`. Covers both accepted forms
(`internal/events/scheduler/cron.go`'s `ParseSpec` doc comment): `@every
<duration>` valid and invalid durations, and standard 5-field numeric
cron expressions covering wildcards, lists, ranges, steps, and every
field's boundary values (both in-range and one-past-range, per field) —
plus a blank line and several deliberately malformed specs (wrong field
count, non-numeric tokens, a malformed range, a zero step), so the
fuzzer starts from a corpus that already exercises both the accept and
the reject path. See `internal/events/scheduler/testdata/README.md` for
full provenance and rationale.

Provenance (Article 2): hand-authored directly from `cron.go`'s own
`ParseSpec` grammar (this ticket's own contract) — not harvested from any
external corpus or tool. Tool: none (manually written). Date: 2026-09-02.
