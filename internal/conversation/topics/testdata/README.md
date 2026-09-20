# Topic-engine test data

Two different things live under `testdata/`, and they are not
interchangeable:

| Path | What it is | Who uses it |
|---|---|---|
| `corpus/` | The owner-supplied labeled transcript corpus. **Not yet delivered** (06-FORGE-SPEC.md §7 owner prerequisite) — see `corpus/README.md`. | `TestCorpusAccuracyFloors` and `TestSegmenterCorpusAccuracy`, both behind `-tags topics_corpus`; both skip while it is empty. |
| `fuzz/` | Go fuzzing corpus seeds for `FuzzCorpusRecord`. | `go test -fuzz` |

There is also an **in-test synthetic corpus**, which is not on disk at all:
it is built in memory by `syntheticCorpus()` in
`segmenter_harness_test.go`. It is documented here because it is fixture
data in everything but storage.

## The in-test synthetic corpus (`segmenter_harness_test.go`)

Three records, built from a closed four-topic vocabulary (`alpha`, `beta`,
`gamma`, `delta`). Each record is a run of same-topic turns, then another,
then another; its ground-truth `boundaries` are the turn indices where a run
starts, and every turn carries the ground-truth label of its run.

It is **synthetic and openly so**. Its purpose is not to measure real
accuracy — only the owner corpus can do that — but to execute the real
`Segment` → `Evaluate` → `AssertFloors` path on every ordinary `go test`
run, and to prove that path can FAIL: `TestSegmenterFloorsFailTheWrongSegmenter`
scores the same corpus from a segmenter that never emits a boundary and
requires both floors to be missed.

What makes it synthetic, stated plainly:

- Every turn's text literally names its topic, and both test doubles read
  that text: the classifier answers with the topic word it finds, and the
  embedder returns the one-hot vector of that topic's index in the
  vocabulary. So same-topic turns are at cosine distance 0 and
  different-topic turns at distance 1 — a cleaner signal than any real
  embedding model produces.
- Neither double reads the records' ground-truth labels. That is the point:
  a double that echoed `TopicLabels` would make assignment accuracy 1.0 by
  construction and would measure nothing.
- Assignment accuracy is capped below 1.0 by design. The per-turn label
  predictions are derived from the segmenter's OWN boundaries
  (`predictedLabels`), and `Segment` surfaces no label for the implicit
  opening segment, so every turn before the first boundary scores as a
  miss. The measured value is 0.90 on this fixture (boundary F1 1.0); the
  per-turn assignment surface itself is P1-E21-W5-S45-T3's, not this
  ticket's.

## Optional fixture fields on a corpus record

`eval.go`'s `CorpusRecord` defines the ground-truth fields (`id`, `turns`,
`boundaries`, `topic_labels`; see `corpus/README.md`). A record may
additionally carry two OPTIONAL arrays that only the segmenter harness
reads, parsed by `loadCorpusFixtures` in
`segmenter_corpus_fixture_test.go`:

```json
{
  "id": "unique-record-id",
  "turns": [{"speaker": "a", "text": "..."}, {"speaker": "b", "text": "..."}],
  "boundaries": [1],
  "topic_labels": {"0": "topic-a", "1": "topic-b"},

  "embeddings": [[0.12, -0.03, 0.44], [0.51, 0.02, -0.18]],
  "classifier_predictions": ["topic-a", "topic-b"]
}
```

- `embeddings` — one vector per turn, in turn order, all the same width.
  The width becomes the fixture embedding space's `Dimensions`.
  **`TestSegmenterCorpusAccuracy` FAILS, rather than skipping, on a record
  that has none**: the segmenter's boundaries come from turn-to-turn
  embedding distance, so scoring it against vectors the test invented would
  measure the fixture instead of the segmenter. `LoadCorpus` ignores this
  field, so adding it does not affect `eval_corpus_test.go`.
- `classifier_predictions` — one recorded cheap-lane label per turn, in
  turn order. Present, these are replayed as the classifier's answers, and
  the classify step is then a real recorded prediction. Absent, the
  record's own `topic_labels` are replayed instead, which makes the
  classify step an oracle; the test says so in its output rather than
  leaving a reader to discover it.

Both fields exist so the corpus run stays deterministic and network-free
(12-QUALITY-CONSTITUTION.md Art.7): no live embedding or model call is ever
made from a test. When the owner corpus is delivered with either field, the
Art.2 provenance requirements in `corpus/README.md` cover it too — a
recorded vector or prediction needs the same "which tool, which version,
when" record as the transcript it belongs to.
