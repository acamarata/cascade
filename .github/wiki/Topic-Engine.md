# Topic Engine

The topic engine (`internal/conversation/topics`) tracks topic boundaries
and per-turn topic labels across a conversation. This page covers the
fixture corpus, the evaluation harness, and the segmenter that produces
real predictions from them.

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

**Test-only.** `LoadCorpusRecord`, `LoadCorpus`, `Corpus`, and
`CorpusRecord` are defined in `eval_harness_test.go`, not a package-level
`.go` file: this package's harness has no production caller and none is
planned (R-14.283, 2026-09-20), so importers of `internal/conversation/topics`
cannot see these symbols — they exist only for this package's own test
binary.

A record may also carry two OPTIONAL arrays that only the segmenter's corpus
harness reads: `embeddings` (one vector per turn) and
`classifier_predictions` (one recorded cheap-lane label per turn).
`LoadCorpus` ignores both. Format and rules:
`internal/conversation/topics/testdata/README.md`.

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
and by how much. Like `LoadCorpus` above, `Evaluate`, `AssertFloors`,
`EvalResult`, and `EvalPredictions` are test-only (`eval_harness_test.go`):
this table's constants (`BoundaryF1Floor`, `AssignmentAccuracyFloor`) are
the only symbols from the harness a production caller could ever see, and
neither is used outside this package's own tests today.

## Segmenter

`topics.NewSegmenter(executor provider.ModelExecutor, embedder provider.Embedder,
cfg HysteresisConfig) (Segmenter, error)` builds the engine that produces real
`[]Boundary` predictions from a `[]Turn` window. It combines four
components:

1. **Cheap-lane classify** - one `provider.ModelExecutor.Execute` call per
   turn with `task_class = "classify"` (06-FORGE-SPEC.md §5.16: cheapest/
   free lane affinity, `Reasoning "low"`, 8K context). Sensitivity is
   inherited, not declared: the request sets no `Policy` and leaves
   `Sensitivity` at its zero value (`provider.SensitivityRestricted`), the
   fail-closed default when the calling thread's own tier cannot be
   resolved locally.
2. **Embedding-distance boundary detection** - every turn's text is
   embedded in one batched `provider.Embedder.Embed` call; consecutive
   turns are compared with normalized cosine distance
   (`1 - cosineSimilarity`).
3. **Hysteresis filter** - a *candidate* boundary is a turn whose distance
   from its predecessor is at or above `HysteresisConfig.Threshold` AND
   whose classifier label differs from the last *committed* topic (not from
   the immediately preceding turn's raw label). The candidate commits when
   the next `Window - 1` transitions do not spike: a real topic change is
   one spike followed by low distances inside the new topic, so the
   evidence lies after the candidate, not before it. A spike inside that
   confirmation window is flicker (a one-turn excursion answered by a
   counter-spike) and the candidate is rejected. `Window = 1` has no
   confirmation window and commits on the spike alone.
4. **Bounded topic stack** - an internal FIFO of recently-committed topics
   (max depth `defaultTopicStackDepth`), consulted for the "last committed
   topic" comparison above and pushed to on every commit; pushing past the
   max depth evicts the oldest entry rather than growing unbounded.

```go
seg, err := topics.NewSegmenter(executor, embedder, topics.HysteresisConfig{
    Threshold: 0.5, // normalized cosine distance, must be > 0
    Window:    2,   // confirmation window length, in transitions
})
boundaries, err := seg.Segment(ctx, turns)
```

### What a `Boundary` index means

`Boundary.TurnIndex` is the **first turn of the new segment** - the turn
whose distance from its predecessor cleared the threshold - never the later
turn at which the confirmation window finished being observed. The index
therefore does not move when `Window` changes: `Window` controls how much
evidence a candidate needs, not which turn is reported. That is what makes a
`[]Boundary` directly comparable to a corpus record's `boundaries`.

One consequence is documented on the type: a boundary committed within
`Window - 1` turns of the end of the window has a confirmation window that
runs past the last turn, so it is committed **provisionally** on the
evidence available. Withholding it would make a genuine final-turn topic
change systematically unreportable. Re-segmenting a longer window may
therefore drop such a boundary; it is the only case where extending the
input changes an earlier boundary.

### Errors

`Segment` returns `(nil, nil)` for a nil or empty `turns`. It returns a
typed `*cascade.Error` for:

| Situation | Kind |
|---|---|
| nil `ctx` | `KindInvalidInput` |
| canceled context | `KindCanceled` |
| expired deadline | `KindTimeout` |
| embedding batch failing `EmbedModel.ValidBatch` (wrong count, foreign model, wrong width) | `KindInvalidInput` |
| a zero-norm embedding (no direction to compare - never reported as distance 0) | `KindInvalidInput` |
| classifier answered with only whitespace | `KindIntegrity` |
| classifier or embedder dependency failure | the underlying error's own Kind, preserved |

`HysteresisConfig.Validate()` rejects a `Window` below 1 and a `Threshold`
that is zero, negative, NaN, or infinite. Zero is refused rather than read
as "maximally sensitive": every pair of non-identical vectors is at distance
above 0, so a zero floor makes every transition a spike and turns the
distance signal off.

`AutoThreader` (a later ticket) is the production composition point that
builds a `Segmenter` and calls `Segment`; see that ticket's own docs for
how boundaries route into conversation threads.

## Running the corpus accuracy test

### Against the owner corpus (build tag `topics_corpus`, currently skipped)

Two build-tagged tests score against `testdata/corpus/`, and both **skip**
while it holds no `*.json` records:

- `eval_corpus_test.go`'s `TestCorpusAccuracyFloors` scores perfect-oracle
  predictions (the corpus's own labels) against the corpus, exercising the
  load/score/assert pipeline independent of any segmenter.
- `segmenter_corpus_test.go`'s `TestSegmenterCorpusAccuracy` runs a real
  `Segmenter` over every corpus transcript and scores its actual
  `[]Boundary` output, plus the per-turn labels derived from it. It replays
  a record's precomputed per-turn `embeddings` and, when present, its
  `classifier_predictions`, so the run is deterministic and network-free
  (format: `internal/conversation/topics/testdata/README.md`). A record
  WITHOUT `embeddings` makes it FAIL rather than skip: scoring a segmenter
  against vectors the test invented would measure the fixture, not the
  segmenter.

```sh
# Both skip while testdata/corpus/ has no *.json records.
go test -tags topics_corpus ./internal/conversation/topics/... -run '^TestCorpusAccuracyFloors$'
go test -tags topics_corpus ./internal/conversation/topics/... -run '^TestSegmenterCorpusAccuracy$'
```

**Neither has ever run against real data, and no CI job runs this tag.**
The corpus is an outstanding owner prerequisite (06-FORGE-SPEC.md §7,
shared by L/S-25.T1, U/S-45.T1 and F/S-12.T5), so there is nothing for a
gate to measure yet; a workflow that ran `-tags topics_corpus` today would
report two skips and prove nothing, and a gate that failed on the skip
would block every unrelated commit on an owner deliverable. The CI job
lands with the corpus, in the ticket that delivers it.

### Without the corpus (runs on every `go test`)

`segmenter_harness_test.go` carries no build tag. It builds a small
synthetic corpus in memory - three records over a closed topic vocabulary,
with test doubles that classify and embed from the turn TEXT rather than
from the records' labels - and drives the real
`Segment` -> `Evaluate` -> `AssertFloors` path over it:

- `TestSegmenterClearsAccuracyFloorsOnSyntheticCorpus` requires both floors
  (measured: boundary F1 1.0, assignment accuracy 0.90).
- `TestSegmenterFloorsFailTheWrongSegmenter` scores the same corpus from a segmenter
  that never emits a boundary and requires both floors to be MISSED, so the
  harness is proven able to fail.

The assignment half is capped below 1.0 by design: per-turn labels are
derived from the segmenter's own boundaries, and `Segment` surfaces no
label for the implicit opening segment, so turns before the first boundary
score as misses. The per-turn assignment surface itself belongs to
P1-E21-W5-S45-T3. See `testdata/README.md` for what makes the fixture
synthetic and why nothing in it reads the ground-truth labels.

## Platform support

The package is pure Go with no CGO and no OS-specific code path.
`TestSegmenterPlatformParity` (`segmenter_core_test.go`) is the explicit,
CI-asserted per-platform result Art.5 requires: the same named test runs
to a pass on the macOS, Linux, and Windows CI matrix.
