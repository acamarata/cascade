# Topic Engine

The topic engine (`internal/conversation/topics`) tracks topic boundaries
and per-turn topic labels across a conversation. This page covers the
fixture corpus and evaluation harness; the segmenter that produces real
predictions is a separate, later component.

## Corpus format

A corpus is a directory of `*.json` files, one record per file:

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
- `boundaries`: 0-based turn indices, within `[0, len(turns))`, where a
  human labeler marked a topic boundary.
- `topic_labels`: turn index (JSON string key, within `[0, len(turns))`)
  to topic label.

`topics.LoadCorpusRecord` parses and validates one record; `topics.LoadCorpus`
loads every `*.json` file directly under a directory, sorted by filename.
Both return a `*cascade.Error` (`KindInvalidInput` for a malformed record,
`KindNotFound` for a missing directory) rather than a bare error.

## Provenance requirement

The canonical corpus location is `internal/conversation/topics/testdata/corpus/`.
It is owner-supplied (06-FORGE-SPEC.md §7 owner prerequisite) and, per
Art.2, must ship with a `README.md` stating: source tool, tool version,
capture date, sanitization method, and label schema. See
`testdata/corpus/README.md` for the current delivery status.

## Accuracy targets

Per 06-FORGE-SPEC.md §5 rule 12:

| Metric | Floor | Constant |
|---|---|---|
| Boundary-detection F1 | >= 0.80 | `topics.BoundaryF1Floor` |
| Topic-assignment accuracy | >= 0.85 | `topics.AssignmentAccuracyFloor` |

`topics.Evaluate(corpus, predictions)` returns a `topics.EvalResult` with
micro-averaged boundary precision/recall/F1 and per-turn assignment
accuracy. `topics.AssertFloors(result)` returns `nil` when both floors are
met, or a `*cascade.Error` (`KindIntegrity`) naming which floor(s) failed
and by how much.

## Running the corpus accuracy test

`eval_corpus_test.go` carries the `topics_corpus` build tag and defines
`TestCorpusAccuracyFloors`. It loads the committed corpus, evaluates
perfect-oracle predictions (the corpus's own labels) against it, and
asserts the result clears both floors — exercising the full
load-score-assert pipeline independent of any specific segmenter
implementation.

```sh
# Skips cleanly when testdata/corpus/ has no *.json records.
go test -tags topics_corpus ./internal/conversation/topics/... -run '^TestCorpusAccuracyFloors$'
```

CI runs with `-tags topics_corpus` and requires the corpus to be present.
Locally, without the corpus, the test reports a skip rather than a
failure.

A later ticket's segmenter test (`TestSegmenterCorpusAccuracy`) reuses
this same pattern — load corpus, run the segmenter to get real
predictions, `Evaluate`, `AssertFloors` — in place of the perfect oracle
used here.
