# Provider SDK

Reference for the public provider contracts in
[`pkg/provider`](../../pkg/provider). Third-party code implements these
interfaces without importing anything under `internal/`: the package depends
on the standard library alone.

Every implementation returns [taxonomy errors](error-taxonomy.md) at the
interface boundary, never a bare `errors.New` or `fmt.Errorf` value.

This page grows as the surface lands. The sections below cover the two
retrieval-model seams.

## Embedder

Turns text into dense vectors.

```go
type Embedder interface {
	Model() EmbedModel
	Embed(ctx context.Context, inputs []EmbedInput) ([]EmbedOutput, error)
}
```

`EmbedInput` carries the `Text` to embed. `EmbedOutput` carries the `Vector`
and the `EmbedModel` that produced it. `EmbedModel` is the pair (`ID`,
`Dimensions`) that identifies an embedding space.

**Batching.** `Embed` takes a batch because per-item calls dominate wall time
on every real backend. The pipeline groups chunks and issues one call per
group. A one-element batch is an ordinary call, not a special case: handle
`len(inputs) == 1` by the same path as any other size.

**Contract for implementors.**

| Clause | Requirement |
| --- | --- |
| Positional correspondence | `outputs[i]` is the embedding of `inputs[i]`, and `len(outputs) == len(inputs)`. Reordering, deduplicating, dropping or padding the batch is a violation even when the same set of vectors comes back. |
| All-or-nothing | Either a complete batch with a nil error, or a nil slice with a non-nil error. There is no partial success and no per-item error channel. |
| Empty input | An empty or nil batch returns an empty slice and a nil error, so callers need not special-case an empty chunk group. |
| Model agreement | Every output carries `Model` equal to the embedder's `Model()`, and a `Vector` of exactly `Model().Dimensions` elements. |
| Cancellation | A canceled or expired context aborts the call and returns a `canceled` or `timeout` error, never a partial batch. |

**Model identity.** Similarity between two unrelated embedding spaces returns
a plausible number rather than an error, so a mixed index is undetectable
after the fact. Callers that persist vectors record the `EmbedModel`
alongside them and compare with `EmbedModel.Equal` before querying an
existing index. Two spaces that are not interchangeable must not share an
`ID`.

**Checking a response.** `EmbedModel.ValidBatch(inputs, outputs)` checks the
structural half of the contract: one output per input, every output under the
expected model, every vector at the expected width. It cannot check ordering,
since a reordered batch is structurally identical to a correct one, which is
why positional correspondence is stated as a binding contract instead.

**Error kinds.** `invalid_input` for a batch the backend rejects,
`unavailable` or `timeout` for an unreachable backend, `quota_exhausted` when
a provider quota is spent, `canceled` for a canceled context.

## Reranker

Reorders candidate passages by relevance to a query. This is the optional
second stage of retrieval: a cheap first pass proposes candidates, and a
reranker scores each one against the query.

```go
type Reranker interface {
	Rerank(ctx context.Context, query string, passages []string) ([]RankedPassage, error)
}
```

`RankedPassage` carries the `Text`, verbatim as it was passed in, and a
`Score`.

A reranker is not a retriever. It never introduces a passage the caller did
not supply and never removes one: truncating to a top-N is the caller's
decision, taken after seeing the scores.

**Contract for implementors.**

| Clause | Requirement |
| --- | --- |
| Completeness | The result contains every input passage exactly once, text unedited. Duplicate inputs produce that many entries. |
| Ordering | Sorted by `Score` descending. Ties may break in any order, so a caller needing stability imposes it. |
| All-or-nothing | Either a complete ranking with a nil error, or a nil slice with a non-nil error. |
| Empty input | Nil or empty passages return an empty slice and a nil error, whatever the query: a first pass that matched nothing is not an error. |
| Cancellation | A canceled or expired context returns a `canceled` or `timeout` error, never a partial ranking. |

**Score scale.** Scores are model-defined and are not comparable across
rerankers, across queries, or against a vector-search similarity. They are
meaningful only as an ordering within one result.

**Checking a response.** `ValidRanking(passages, ranked)` checks the
structural half: the same passages come back, each as many times as it went
in, in non-increasing score order. It does not judge the scores themselves.
A reranker returning a constant score produces a valid ranking and a useless
one, which is a quality question rather than a contract violation.
