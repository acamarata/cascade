# Topic-engine fixture corpus

## Status: not yet delivered

This directory is the canonical landing path for the owner-supplied
sanitized labeled transcript corpus (06-FORGE-SPEC.md §7 owner
prerequisite, shared by L/S-25.T1, U/S-45.T1 and F/S-12.T5). As of this
ticket (P1-E21-W5-S45-T1) it contains no corpus records.

`internal/conversation/topics/eval_corpus_test.go`'s `TestCorpusAccuracyFloors`
(build tag `topics_corpus`) skips cleanly while this directory has no
`*.json` records, per 06-FORGE-SPEC §7: the prerequisite gates this
ticket's *acceptance* of a real-corpus measurement, never its build. The
load-and-validate function (`LoadCorpusRecord`/`LoadCorpus` in `eval.go`)
and the scorers (`Evaluate`, `AssertFloors` in `eval.go`) are implemented
and unit-tested against hand-crafted in-memory fixtures in `eval_test.go`
regardless of whether this directory is populated.

No corpus data was fabricated to fill this gap. A synthetic substitute
here would produce accuracy numbers with no evidential value once a real
segmenter (T2) is scored against it, and would misrepresent the risk this
corpus exists to de-risk (12-QUALITY-CONSTITUTION.md Art.12; see the
F/S-12.T5 spike journal, which reached the same conclusion for the same
missing artifact).

## Required provenance when the owner delivers the corpus

Per Art.2 (fixture-provenance requirement), this README must be updated,
alongside the committed `*.json` records, with:

- **Source tool**: the name of the tool/harness the transcripts were
  captured from.
- **Tool version**: the version string of that tool at capture time.
- **Capture date**: the date (or date range) the transcripts were
  recorded.
- **Sanitization method**: how personal names, account/machine
  identifiers, file paths, and any credential-shaped strings were removed
  or replaced before the transcripts left their source, and who performed
  it.
- **Label schema**: the topic label vocabulary used, and who assigned the
  boundary/topic labels (human labeler, review process).

## Record format

Each record is one `*.json` file directly under this directory (no
subdirectories; `LoadCorpus` reads only `*.json` at this level, sorted by
filename for determinism):

```json
{
  "id": "unique-record-id",
  "turns": [
    {"speaker": "a", "text": "turn text"},
    {"speaker": "b", "text": "turn text"}
  ],
  "boundaries": [1],
  "topic_labels": {"0": "topic-a", "1": "topic-b"}
}
```

- `id`: non-empty, unique within the corpus.
- `turns`: non-empty ordered list of speaker turns.
- `boundaries`: turn indices (0-based, within `[0, len(turns))`) where a
  human labeler marked a topic boundary.
- `topic_labels`: a map from turn index (as a JSON string key, within
  `[0, len(turns))`) to the topic label assigned to that turn.

`LoadCorpusRecord` (`eval.go`) rejects any record violating these
constraints with a `*cascade.Error` carrying `KindInvalidInput`.

## Accuracy floors

Per 06-FORGE-SPEC.md §5 rule 12: boundary-detection F1 >= 0.80
(`topics.BoundaryF1Floor`) and topic-assignment accuracy >= 0.85
(`topics.AssignmentAccuracyFloor`). `topics.AssertFloors` checks a
`topics.EvalResult` against both.
