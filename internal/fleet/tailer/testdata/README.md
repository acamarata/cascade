# fleet/tailer test fixtures

Per R-40.X16: this ticket captures nothing. There is no
`internal/fleet/tailer/testdata/transcripts/` directory, and there must
never be one — a second copy of a redacted transcript in a public repo is
exactly the leak surface R-21.152 closes.

## Real fixture pointer (Art.2 provenance)

The tailer's real-fixture tests read E/S-09.T6's committed transcripts by
path, in place, from their permanent home:

- `internal/context/testdata/transcripts/cc-sample-redacted.jsonl`
- `internal/context/testdata/transcripts/codex-sample-redacted.jsonl`
- `internal/context/testdata/transcripts/opencode-sample-redacted.jsonl`
  (not used by this package's tailer — opencode has no transcript file;
  see docs/adrs/ADR-E09T6-harness-transcript-stability.md)

Full provenance (tool, version, capture date, what was redacted and why)
is documented in `internal/context/testdata/transcripts/README.md`, per
ticket P1-E05-W2-S09-T6. This package's tests copy the two file-writing
harnesses' fixtures into `t.TempDir()` at test time and plant a
credential canary in that copy before running the tailer over it, per the
R-21.152 credential-canary requirement (R-40.X16) — the committed fixture
bytes on disk are never modified.

## FuzzTranscriptParse seed corpus

`fuzz/FuzzTranscriptParse/seed_transcript.jsonl` is a Go-native fuzz
corpus entry (`go test fuzz v1` header format), not a plain JSONL sample.
Per R-21.266 the corpus is package-local and Go auto-loads it with no
`f.Add` ceremony; per R-40.X16 it is derived at test-writing time and
carries no harness-captured bytes of its own.
