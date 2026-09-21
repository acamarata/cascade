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

`topics.NewSegmenterWith(classifier topics.Classifier, embedder provider.Embedder,
cfg HysteresisConfig) (Segmenter, error)` builds the engine (the classifier comes from
`topics.NewClassifier(executor)`, built once and shared with the AutoThreader so the
window's opening turn is classified exactly once) that produces real
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
classifier := topics.NewClassifier(executor)
seg, err := topics.NewSegmenterWith(classifier, embedder, topics.HysteresisConfig{
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

## Auto-thread routing, taxonomy, and exemplars (P1-E21-W5-S45-T3)

Four sub-systems sit between the segmenter above and the conversation
domain: `AutoThreader` routes segmented turns into threads, `TaxonomyConfig`
resolves a raw classifier label to a canonical topic, `ExemplarStore`
persists per-topic examples for future few-shot classification, and
`Reassign` corrects a misfiled turn.

### ThreadStore (thread_store.go)

`ThreadStore` is the seam `AutoThreader.Route` and `Reassign` depend on. It
stays an interface so both can be exercised against a double with no
database (R-14.64), but the interface is not the only thing that ships:
`NewConversationThreadStore(store, clock)` is the real implementation, and
`thread_store_conversation_test.go` drives it against a real
modernc-sqlite `conversation.Store`. The composition root wires it at
startup; `internal/build/testonly-allow.json` names the ticket expected to
make that call.

The four methods:

- `CreateOrSelect(ctx, topicType) (ThreadID, error)` - the same `topicType`
  always returns the same `ThreadID`.
- `LookupThread(ctx, topicType) (ThreadID, bool, error)` - the read-only half
  of `CreateOrSelect`, added by S-45.T4: it reports the id of the thread that
  already owns `topicType` (and `false` when none does) without creating one,
  for a caller that must say what routing *would* do without doing it. An
  implementation must return the same id `CreateOrSelect` would; a store that
  cannot answer returns the same typed error, never a false "none".
- `AppendTurn(ctx, threadID, turn) error` - `turn` is a `ThreadTurn`: the
  segmenter-side `Turn` plus the content-addressed `TurnID` its caller
  computed with `NewTopicTurnID`. Identity is the caller's, exactly as
  `internal/conversation.Turn`'s own `ID` field is (set via `NewTurnID`,
  never handed back by the store), which is why this returns a bare error.
  **Appending a turn whose id the thread already holds is a no-op**, not an
  error - that is what makes a re-delivered window idempotent.
- `MoveTurn(ctx, threadID, turnID, newType) error` - reassigns an existing
  turn, or refuses with a typed error naming what the backing domain
  cannot do (see § Reassign).

`NewTopicTurnID(threadID, index, turn)` is the turn's content address:
identity is `(thread, index within the routed window, speaker, text)`.
Re-delivering the same window is therefore a no-op, while a
differently-aligned window that repeats a turn at a different index is a
different turn - this package has no cross-window turn identity to consult,
and collapsing genuinely repeated turns (the same short reply twice) would
lose real data. The text is hashed into an opaque digest and never appears
in a log line, an error message, or a metric label.

**The real implementation's two documented limits.** `CreateOrSelect`
resolves a deterministic `"topic:<topic_type>"` thread id and probes
`GetThread` so an unreachable store is refused at selection time; the
thread ROW is written by the first `AppendTurn` (the conversation domain
exposes no create-thread call - its own `store.go` records that gap, and
its `AppendTurn` ensures the thread on first use). `MoveTurn` refuses with
`cascade.KindUnsupported` naming the missing primitive: conversation turn
rows are append-only with content-addressed ids and the domain offers no
move, so the only alternatives would be raw SQL against another domain's
tables or reporting a move that did not happen. The gap is filed for the
owner rather than papered over.

### AutoThreader.Route (auto_thread.go)

`Route(ctx, turns)` calls its `Segmenter`, then partitions `turns` at each
`Boundary.TurnIndex` (segmenter_core.go). Each partition's classifier label
is resolved through `TaxonomyConfig.Resolve` to a `TopicType`, which
selects (or creates) a thread via `ThreadStore.CreateOrSelect`, and every
turn in the partition is appended to that thread. **The window's first
partition (turns before the first reported `Boundary`) has no classifier
label at all** - `Segment`'s own contract never reports a boundary at
index 0 - so `Route` resolves it with the empty string, which
`TaxonomyConfig.Resolve` already treats like any other unmapped label (its
configured fallback), rather than inventing a separate "unclassified" case.

Three rules the implementation holds to:

- **An empty window short-circuits before the `Segmenter` is called.**
  There is nothing to segment, so a classify/embed round trip to be told so
  would be waste.
- **A boundary list that cannot describe a partition is refused.** A
  `TurnIndex` that is negative, zero, repeated, out of order, or at/past the
  end of the window returns a typed `cascade.KindInvalidInput` error naming
  the offending index - never a panic on the slice expression, and never an
  empty segment (which would create a thread no turn was filed under).
- **Routing the same window twice files N turns, not 2N.** Each turn's id
  is its content address at its window index, and `AppendTurn` is a no-op
  for an id the thread already holds.

A resolved `TopicType` also crosses the key-space boundary before anything
is filed: non-empty, at most 64 bytes, letters/digits/`.`/`_`/`-`/`:` only,
refused with `KindInvalidInput` rather than silently rewritten.

`NewAutoThreader(segmenter, store, taxonomy, exemplars, publisher, clock)`
takes an already-built `Segmenter` for testability (R-14.64). The last
three dependencies are `Reassign`'s: `Reassign` is a method on the same
`AutoThreader`, not a separate service, because it operates the same
`ThreadStore` against the same taxonomy that `Route` does.
`NewDefaultAutoThreader(executor, embedder, cfg, store, taxonomy,
exemplars, publisher, clock)` is the production constructor: it builds one
`Classifier`, calls `NewSegmenterWith` (segmenter_core.go) with it and hands the
same classifier to the AutoThreader, so a real caller never builds a `Segmenter`
by hand and the opening turn is never classified twice.

### TaxonomyConfig (taxonomy.go)

Mechanism only, per R-14.63: `NewTaxonomyConfig(labels, fallback)` takes a
caller-supplied `map[string]TopicType` and a fallback `TopicType`.
`Resolve(label)` returns the mapped type or the fallback for anything
unmapped, including the empty string - never a panic, never an error. Core
ships **no built-in label set** and no `[topics.taxonomy]` config section;
cascade-pa injects its own defaults (`general`, `code`, `memory`, `task`)
through its own plugin config (`[plugins.<name>]`, 08-INIT-CONFIG-SPEC §3).
`TopicType` is an open string type, not a closed enum - the set of topics
this package will ever see is whatever a caller's labels and fallback name.

### ExemplarStore (exemplar_store.go)

A bounded per-topic FIFO of `Turn` exemplars, for few-shot injection into a
cheap-lane classify call. `Add(ctx, topicType, turn)` appends the newest
exemplar and evicts the oldest once the configured depth (default 20,
`NewExemplarStore`'s `maxDepth <= 0` falls back to this) would otherwise be
exceeded. `Exemplars(ctx, topicType)` returns the current slice, oldest
first, or `nil` for a topic with none yet. `topicType` crosses the same
key-space boundary `Route` applies.

**Exemplar ids carry no clock.** `exemplar_id` is a content address over
the turn's own hash and its topic, so a retried or repeated `Add` of the
same turn converges on one record - `Add` of an id the topic already holds
is a no-op, enforced in code, matching the `PRIMARY KEY ("exemplar_id")` in
this table's reference migration. `created_at` still records when the
exemplar was first observed; that is data about the record, not part of its
identity.

**No consumer is scheduled for `Exemplars` yet.** The cheap-lane classifier
that would take few-shot exemplars shipped in S-45.T2, which does not read
them. The read side's owner is filed for the owner rather than assumed.

Persisted through the B/S-02 `pkg/provider.Store` key-value abstraction -
never direct SQL - as one JSON-encoded bounded slice per topic, under key
`"topic_exemplars/<topic_type>"` in the `retrieval` domain namespace
(`internal/storage.DomainRetrieval`). `Store.Put`'s own create-or-overwrite
semantics are what makes the first write idempotent; there is no separate
schema-init call. `internal/retrieval/migrations/0020_topic_exemplars.sql`
is a reference-only rendering of the conceptual row shape (never executed
by any code path), extended with `speaker`/`text` columns beyond the
contract's four named ones, since `Exemplars` must return real `Turn`
content for few-shot use. `embedding_ref` is always empty in this ticket -
no `Embedder` is wired into `ExemplarStore`'s constructor - an honestly
empty column, never a fabricated reference.

### Reassign (reassign.go)

`AutoThreader.Reassign(ctx, threadID, turnID, turn, currentTopicType,
newTopicType)` corrects a misfiled turn: it moves the turn
(`ThreadStore.MoveTurn`), records `turn` as a fresh exemplar for
`newTopicType` (`ExemplarStore.Add`), and publishes a `MisfileEvent`. A
no-op reassignment (`newTopicType == currentTopicType`) returns a typed
`cascade.KindConflict` error before touching the store, the exemplar set,
or the bus. Any dependency failure stops the sequence immediately: a
failed move never adds an exemplar for a move that did not happen, and a
failed exemplar write never publishes an event overstating what was
recorded. An unusable target `TopicType` is refused before the move.

`Reassign` is a method on `AutoThreader`, not a `Reassigner` service with
its own constructor: it drives the same `ThreadStore` against the same
taxonomy `Route` does, and a second type over identical dependencies would
have been a parallel object graph for nothing. The `turn` and
`currentTopicType` parameters are two additions to the ticket's own literal
signature, both recorded for the owner: nothing else can supply
`ExemplarStore.Add`'s required `Turn`, and `ThreadStore` has no per-turn
lookup to ask a turn's current topic from.

**Against the real store, `MoveTurn` refuses** (see § ThreadStore): the
conversation domain has no turn-move primitive, so the correction path is
proven end to end against the `ThreadStore` double and the missing
primitive is filed for the owner rather than faked.

**Audit event mechanism.** "Emits a typed misfile audit event to the audit
domain" is not `internal/audit.Writer.Append`: `audit.Kind` is a CLOSED
14-member enum (T0 ruling R-21.235), and none of the fourteen
(`policy.*`, `approval.*`, `config.reload`, `elevation.*`, `secrets.*`,
`vault.access`) names a topic-classification correction. The real seam is
`internal/events.EventKind`, documented there as deliberately open so
independently-owned producers can mint their own values - the same shape
`internal/audit`'s own `EventKindRecorded` already uses. `Reassign`
publishes `EventKindMisfileReassigned` (`"topics.misfile_reassigned"`)
into the audit domain's namespace via the injected `MisfileEventPublisher`
seam (shaped exactly like `internal/events.Bus.Publish`, so a real `*Bus`
satisfies it with no adapter code). The JSON payload is `MisfileEvent`:

```json
{
  "thread_id": "...",
  "turn_id": "...",
  "from_topic_type": "general",
  "to_topic_type": "code",
  "reassigned_at": 1758000000
}
```

## Observe-log mode (P1-E21-W5-S45-T4)

`ObserveLogger` (`observe_log.go`, with the first-use window itself in
`observe_state.go`) wraps an `AutoThreader` with a 7-day gate: for the first
week after the topic engine's first real use, calling `Observe` instead of
`Route` records what the pipeline *would* have done without touching a
single conversation thread. New users are not guaranteed onto a
well-calibrated segmenter on day one - `Observe` buys the engine a week of
real transcripts before its boundary decisions are allowed to reorganize
anyone's threads.

**`Observe(ctx, turns) (ObserveResult, error)`** branches on elapsed time
since `first_use_at`:

- **Apply mode** (elapsed >= 7\*24h): delegates transparently to
  `AutoThreader.Route` - same thread ids, same errors, no audit event. The
  result carries `Observed: false` and `ThreadIDs`.
- **Observe mode** (elapsed < 7\*24h): never calls `Route` and never writes
  through the `ThreadStore`. The result carries `Observed: true` and one
  `Proposal` per planned segment.
- An empty `turns` window is a no-op before either path runs, and before any
  store read: the result is the zero `ObserveResult`. The `Observed` flag is
  what tells a caller "observed, and there was nothing to propose" apart from
  "the window is closed" and "there was nothing to route".

**One routing decision, not two.** Observe mode does not re-derive routing.
`AutoThreader.plan` (`auto_thread.go`) is the pipeline's single planning
step - segment, partition, resolve each partition's topic through the
`TaxonomyConfig`, validate it - and `Route` and `Observe` both call that same
method. Whatever `plan` does, both modes do.

**Where a proposed thread id comes from.** A `ThreadID` is opaque: only the
`ThreadStore` implementation knows what shape it mints. So observe mode asks
the store rather than guessing, through `LookupThread(ctx, topicType)`, the
read-only half of `CreateOrSelect`:

- a topic that already has a thread yields that thread's **real id**, and
  `Proposal.Existing` is true - it is the id `Route` returns for that topic
  against that store, which `observe_pin_test.go` asserts by running both
  over the same window and comparing;
- a topic with no thread yet yields the marker `would_create:<topic_type>`
  with `Existing` false, never a fabricated id;
- a store that cannot answer fails the `Observe` call, exactly as
  `CreateOrSelect`'s failure fails apply mode. Observe mode never reports a
  routing success the pipeline would not have had.

**`first_use_at` persistence.** One record for the whole engine, not one per
topic: the schema's `{topic_type, first_use_at}` shape stores a single row
(`topic_type` fixed to the constant `"engine"`) under a B/S-02
`provider.Store` key - the `retrieval` domain, key `topic_observe_state`
(R-16.63 addendum). `Observe`'s first call reads the row; present, it caches
the stored value; absent, it claims the row for the current `Clock` time with
a **conditional create** inside the store's own transaction
(`Tx.CompareAndSwap` with a nil `old`). Two instances racing on a fresh store
therefore cannot both write: the loser sees `KindConflict`, re-reads, and
adopts the winner's value, so `first_use_at` is set once and never moved
forward. The in-process cache likewise never overwrites itself, so a second
`Observe` on the same instance touches no store at all.

**`IsObserving() (bool, error)`** reports the current gate state for
doctor/status surfaces: true iff `first_use_at` is set and less than 7\*24h
has elapsed. It takes no `context.Context` (its literal call shape), so its
fallback read - when nothing has called `Observe` on this instance yet - uses
`context.Background()`. An **absent** row is not a failure: it is the
contract's "no `Observe` call has occurred", and reports `(false, nil)`. A
store that cannot answer (`KindUnavailable`) and a row that will not decode
(`KindIntegrity`) report `(false, err)`: the gate state is then *unknown*,
and a caller must not read that `false` as "apply mode is fine".

**Audit event schema**, published as `EventKindTopicObserve`
(`"topics.observe_log"`) into the audit domain's namespace via the injected
`AuditPublisher` seam (shaped like `internal/events.Bus.Publish`, matching
`Reassign`'s own `MisfileEventPublisher` precedent - see below). A failed
publish fails the `Observe` call: the event is observe mode's only product.

```json
{
  "event": "topic_observe",
  "proposed_boundaries": [{"turn_index": 1, "topic_type": "code-topic"}],
  "proposed_thread_ids": ["would_create:fallback", "topic:code-topic"],
  "observed_at": "2026-09-21T00:00:00Z",
  "elapsed_days": 3
}
```

`proposed_thread_ids` mixes real ids and `would_create:` markers exactly as
the store answered. `observed_at` is the observation time from the injected
clock (RFC3339), not `first_use_at`; `elapsed_days` is fractional days, not
hours.

Carries no turn content: `topic_type` values are caller-configured labels,
and every `proposed_thread_ids` entry is either a store-issued id or a marker
derived from one such label, so the event never widens the exposure of
conversation text (the same PRIVACY posture `NewTopicTurnID` and
`MisfileEvent` already document).

**Transition to apply mode is automatic** - there is no toggle, no config
flag, and no operator action: the next `Observe` call after the 7-day window
elapses simply routes for real.

**Not wired yet.** Nothing in this tree composes a running `AutoThreader`, so
nothing constructs a running `ObserveLogger` either; the per-turn composition
root for the topic engine is unowned. S-46.T4 adds read-only `--topics` /
`--threads` CLI handlers over the conversation-service RPC and constructs
neither type, so it will not by itself retire the `NewObserveLogger` entry in
`internal/build/testonly-allow.json`. That entry is parked, with its reason
stating exactly that, pending the planner's disposition of the S-45.T3 and
S-45.T4 PCIs. CLI surfaces for observe-log status are out of this ticket's
scope; doctor and status surfaces call `IsObserving()` directly once a
composition root exists.


## Platform support

The package is pure Go with no CGO and no OS-specific code path.
`TestSegmenterPlatformParity` (`segmenter_core_test.go`),
`TestTopicsPlatformParity` (`reassign_test.go`, covering AutoThreader,
TaxonomyConfig, ExemplarStore, and Reassign together), and
`TestObservePlatformParity` (`observe_mode_test.go`, covering ObserveLogger
through both modes) are the explicit, CI-asserted per-platform results
Art.5 requires: all three run to a pass on the macOS, Linux, and Windows
CI matrix.
