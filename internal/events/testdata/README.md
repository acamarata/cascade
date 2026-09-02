# internal/events/testdata — corpus provenance pointer

`internal/events` has no package-local test fixtures of its own; this file
exists to satisfy the ticket's `docs_updates` requirement to "state corpus
provenance in internal/events/testdata/README.md" (P1-E03-W1-S04-T3, task
7) and to point at the corpus's actual location.

`FuzzEventDecode`'s seed corpus (`internal/events/fuzz_test.go`) lives at
**`internal/testdata/fuzz/events/`**, not here — per 06-FORGE-SPEC.md §5.7,
every fuzz corpus in this module lives under the single shared
`internal/testdata/fuzz/` home, never beside its owning package. See
`internal/testdata/fuzz/events/README.md` for the full seed-by-seed
provenance record (Article 2: tool, version, date for each file).
