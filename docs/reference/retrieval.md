# Retrieval

Reference for the retrieval subsystem: how content is chunked, indexed,
scoped and fused. This page grows as the subsystem lands; the section below
covers the corpus and scope model, which every other part of retrieval is
filtered through.

## Corpus and scopes

Package home: [`internal/retrieval/corpus`](../../internal/retrieval/corpus).

A **corpus** is one indexed body of content, owned by exactly one session
scope. A **record** is one indexed unit inside a corpus, carrying its own
scope reference. Both carry three classification axes, and a record can
never be wider than its corpus on any of them.

| Axis | Type | Values | Answers |
| --- | --- | --- | --- |
| Privacy | `PrivacyClass` | `personal`, `global`, `project` | Whose material is this, and is the asking session entitled to material of that kind |
| Visibility | `VisibilityClass` | `private`, `scope-local`, `shared`, `team` | How far beyond the owning scope may this travel |
| Trust | `TrustLevel` | `trusted`, `untrusted-source` | May instructions found in this content be acted on |

### Scope membership

Every corpus and every record carries a `scope_ref`: the id of the session
scope that owns it. The scope id is opaque to this package; the session
scope resolver is the one place that decides how one is composed.

A session presents a `Membership`: its own resolved scope, the chain that
scope sits in, and the relationship edges declared for it. Edge kinds are
`depends_on`, `member_of` and `shares_context_with`, and they are explicit
data written at init or by the user. Nothing is inferred.

Membership is **deny-by-default**. The candidate set is the session's own
scope chain plus its declared edge targets, and nothing else. The decision
is made per record before anything else happens to that record, never as a
filter applied after ranking. An empty membership admits nothing; a
malformed one is refused outright rather than silently matching nothing,
so a caller with a broken membership is told rather than concluding the
index is empty.

### Visibility reach

| Class | Reaches |
| --- | --- |
| `private` | The owning scope only |
| `scope-local` | The owning scope's chain |
| `shared` | The chain plus declared edge targets |
| `team` | The chain plus declared edge targets; the class the team capability and grant model reads |

`team` is a wider classification, not a wider membership rule. Core carries
the classification and never performs the sharing; the capability registry
and grant model consume the class and decide what team sharing means.

### The trust dimension

Trust is defined once, here, and enforced nowhere here. Every record
surfaced through the scope-filtered query API carries its trust tag intact,
so the consumers that must act on it can:

- Context assembly, which sees the tag on every retrieval result it
  assembles into a budget.
- The auto-advance ceiling, which never advances on instructions carried by
  untrusted-source-tagged content.

A record's effective trust is the less trusted of its own tag and its
corpus source's tag: a record cannot claim to be trusted when the source it
came from is not.

This dimension is distinct from a node's trust tier, which classifies
machines rather than content and lives with the nodes subsystem.

### Fail-closed on read, strict on write

The two directions are deliberate and different:

- **Write path.** `Corpus.Validate` and `Record.Validate` refuse anything
  not fully classified. There is no default on any axis and no coercion: an
  omitted or unrecognized enum, or a malformed scope reference, is an
  invalid-input error. A caller writing a bad value has a bug and needs to
  hear about it.
- **Read path.** A record already in the store whose classification does
  not resolve is treated at the least-privileged value on every axis:
  visibility collapses to `private`, privacy to `personal`, trust to
  `untrusted-source`, and a record with no resolvable scope or corpus is
  withheld from everyone. A value that cannot be read must not become a
  permission.

Records come back from a query carrying the values that were actually
decided, so a consumer reads the resolved classification rather than
re-deriving it and possibly disagreeing.

### What lives elsewhere

The model carries the flags; it does not implement the systems that act on
them. Query-time narrowing of the index legs, the recall command surface
and its corpus and scope flags, the retrieval configuration surface,
egress-time substitution, the team capability and grant wiring, context
assembly and auto-advance enforcement are each their own subsystem, and
none of them are reachable from this package.

## Embed pipeline

Package home: [`internal/retrieval/embed`](../../internal/retrieval/embed).

The embed pipeline is the write half of retrieval. It takes the chunks the
ingest stage produced for one corpus, embeds them, and writes the vectors
into that corpus's vector namespace, which is where the query-time
embedding leg reads them from.

### Batching

Chunks are grouped and embedded a batch at a time through the `Embedder`
seam, never one call per chunk. A per-item call dominates wall time on
every real embedding backend, so the batch is the unit of work: the
pipeline groups, the provider embeds, and the results come back in input
order.

### Content-hash dedupe

A chunk's id is the hash of its content, so "have we embedded this
already" is answerable without re-embedding anything. The pipeline keeps a
ledger of which content it has embedded, into which namespace, under which
model, and skips anything already there.

The consequences are worth stating plainly, because embedding is the
expensive step:

- Re-running over an unchanged corpus makes no embedding calls at all and
  writes no vectors. It is a no-op.
- Editing a file changes only the chunks that actually changed; the rest
  keep their ids and are skipped.
- Identical content appearing twice, in one run or at two paths, is
  embedded once.
- The same content in two corpora is embedded once per corpus, because
  each corpus's index holds its own copy.

Vectors are written as insert-or-replace keyed by the chunk id, so even a
redundant write cannot produce a duplicate row.

### One model per namespace

A vector namespace holds exactly one embedding space. The first write into
a namespace binds it to the model that produced the vectors, and every
later write is checked against that binding: a different model, or the
same model at a different vector width, is refused and nothing is written.

This is a refusal rather than a warning because the failure is otherwise
undetectable. Similarity between vectors from two unrelated embedding
spaces returns a perfectly plausible number; nothing downstream can tell
that the number is meaningless. Switching models therefore means indexing
into a new namespace, or re-indexing the existing one, and never mixing.

### What a failed run leaves behind

Batches commit one at a time. A batch that fails, at the embedder or at
the vector store, writes no vectors and records nothing in the ledger, so
no half-written batch lands. Batches that already committed stay
committed, and the ledger names exactly those, so re-running resumes at
the failed batch and re-embeds nothing that already succeeded.

The write order within a batch is vectors first, ledger second. An
interruption between the two leaves vectors that the ledger does not know
about, so the next run embeds them again and replaces the same rows:
wasted work, never a gap. The other order would leave a ledger entry with
no vector behind it, and every later run would skip content the index does
not actually hold.

### Error paths

| Situation | Result |
| --- | --- |
| No chunks | A no-op. No provider call, no write, no error. An ingest that produced nothing is an ordinary outcome. |
| Corpus not fully classified | Refused as invalid input before any work. There is no default classification. |
| Chunk id is not its content's hash | Refused as an integrity failure. Dedupe and the vector key both rest on that being true, so it is verified rather than trusted. |
| Embedder fails | Reported as unavailable, with the count of what committed before it. |
| Embedder returns the wrong count, model, or vector width | Refused as an integrity failure; that batch writes nothing. |
| Vector store fails | Reported as unavailable; that batch writes nothing. |

Nothing in this pipeline reaches the network. An embedding backend that
does is an `Embedder` implementation, and the network call belongs there.

## RRF fusion

Package homes: [`internal/retrieval/rrf`](../../internal/retrieval/rrf) for the
ranking core, [`internal/retrieval/fusion`](../../internal/retrieval/fusion)
for the scope filter and the vector leg.

A query runs two legs: a full-text leg and an embedding-similarity leg.
Each returns its own ranked list, and the two are merged by Reciprocal
Rank Fusion, which ranks on position rather than on score. That matters
because the two legs score on incomparable scales; their agreement about
ordering is comparable, and their raw numbers are not.

### The formula and its constants

For a chunk `d`, the fused score is the sum over the legs that returned it
of `weight / (k + rank)`, where `rank` is 1-based. A leg that did not
return `d` contributes nothing for `d`.

| Constant | Value | Meaning |
| --- | --- | --- |
| `k` | 60 | Smoothing constant. Larger values flatten the advantage of a top-1 hit relative to the rest of its list. Configurable: the fusion call takes it as a parameter. |
| neutral leg weight | 1.0 | The multiplier that leaves standard RRF unchanged. A heavier leg contributes proportionally more; a leg weighted 0 contributes nothing while still being recorded as having run. |

### Determinism

The fused order is a function of the input values alone. Two properties
produce that:

- Each chunk's contributions are summed in a fixed order, by leg name and
  then rank. Floating-point addition is not associative, so summing in
  arrival order would let the same query score differently when the legs
  were collected in a different order, and a difference in the last bits is
  enough to flip an exact tie.
- Ties in the fused score break on the chunk id, ascending. Exact ties are
  routine rather than rare: any two chunks holding mirrored ranks across
  two equally weighted legs tie exactly. Without a stated rule their order
  would come from map iteration and differ between runs of one query.

### Dedupe

Identity is the chunk id, the stable content-addressed identifier the
chunker assigns. A chunk both legs returned produces exactly one result
whose score is the sum of both contributions, so agreement between the legs
raises a result rather than merely recording it twice.

Identical content at two paths shares one chunk id by design, so the merged
result can see more than one source path. It keeps the lexicographically
first, which does not depend on which leg was read first. If the legs
disagree about the trust tag, the merged result takes the untrusted side.

This is result-level dedupe. Collapsing overlapping line ranges within one
file is a separate, later step belonging to citations.

### Normalization

Fused scores are min-max normalized into `[0,1]` across the fused list,
after fusion rather than per leg, so the best result in the answer is 1 and
the worst is 0. Normalization is monotone, so it never reorders anything.

A single result, and a set whose scores are all equal, both normalize to 1
rather than 0: the formula is undefined with no range, and 0 would report
the best available evidence as no evidence.

### Scope

Scope is enforced in exactly one place, before either leg runs. The filter
resolves the asking session's authorized records through the corpus model,
and hands the legs the narrowed set: a list of vector namespaces to open,
and a predicate the full-text leg binds into its own query. There is no
scope filtering after ranking anywhere in the retrieval path, and the
ranking core has no access to the model that would let it make a scope
decision at all.

A vector namespace holds a whole corpus, while classification is per
record, so the driver can return an id the model withheld. Such an id has
no resolved classification, and content with no resolved classification is
withheld, on the driver's raw response and before anything is ranked.

### The vector leg

The leg embeds the query text once and asks each scope-bound namespace for
its nearest neighbours, merging the answers and cutting to the requested
count.

When no embedder is configured the leg is skipped. Fusion continues on the
full-text leg alone and a `retrieval.vector_leg.unavailable` event is
recorded, so a half-strength query is visible to the doctor rather than
looking like a thin index. A missing embedder is a supported local
configuration, not a fault, and the leg never substitutes invented vectors
for the ones it cannot compute.

### What a result carries

| Field | Meaning |
| --- | --- |
| Chunk id | The identity the result was deduped on. |
| Path | The source path, chosen deterministically when the legs saw more than one. |
| Corpus id | The corpus the chunk was retrieved from. |
| Trust | The trust tag, carried through unchanged. Fusion never enforces it; context assembly and the auto-advance ceiling do. |
| Raw score | The fused score before normalization, the value the ranking is decided by. |
| Score | The normalized score, in `[0,1]`. |
| Strategies | The legs that contributed, sorted by name. |

The citations model, the recall command surface and context assembly all
read this shape.

## Citations and provenance

A citation is a claim about where an answer came from. A wrong citation is
worse than no citation: it manufactures confidence in the one place a
reader has no way to check. Every rule below exists so a citation cannot
claim more than the retrieval path actually established.

Citations attach to the fused results, in the order fusion ranked them. A
citation's rank is its position in that list.

### What a citation carries

| Field | Meaning |
| --- | --- |
| Chunk id | The stable chunk id whose rank and score the citation reports. |
| Merged chunk ids | The other chunks folded in by same-file dedupe, sorted. Absent when nothing merged. |
| Path | The source path, exactly as fusion resolved it for that chunk id. Absent when the source has no path. |
| Lines | The 1-based, inclusive line span. Absent when the source has no lines. |
| Corpus id | The corpus of the record the scope resolver authorized. |
| Trust | The effective trust tag: the least trusted of everything that went into the citation. |
| Rank | 1-based position in the fused ranking; the strongest rank after a merge. |
| Score, raw score | The normalized and pre-normalization scores behind that rank. |
| Strategies | The contributing legs, sorted by name. |

Every location field is optional at the record level. A field the source
did not supply is absent, never defaulted: a fabricated line range is
exactly the kind of error nothing downstream can catch.

### Never cite what the caller may not see

Assembling a citation set requires a source resolver — the query path's
scope filter — and there is no way to assemble one without it. A result the
resolver does not authorize produces no citation at all: not its path, not
its corpus, not its line numbers, and nothing in the rendered output. Those
results are counted in `Withheld` and are otherwise invisible.

A result whose leg claims a corpus the authorized record does not agree
with is withheld on the same rule. The disagreement means the chunk was
reached through a corpus the session was not cleared for, and the
fail-closed reading of a disagreement about authorization is to withhold.

### Trust is never laundered

A citation's trust is the least trusted of the fused result's tag and the
authorized record's tag. Fusion already resolves a chunk two differently
classified paths reported to the untrusted side; re-deriving trust from the
record alone would make the trusted half reappear in the citation. A merge
combines the same way, so a merged citation is never reported as safer than
its least safe half.

### Citation-level dedupe

Two citations naming the same path with overlapping known line spans merge
into one. This is citation-level dedupe and is distinct from fusion's
result-level dedupe, which collapses one chunk id returned by several legs.

The merged citation takes its identity, rank and scores from the stronger
half, so its numbers still describe a real result. The span is the union,
the strategies are the union, the trust is the lesser, and the folded chunk
ids are listed. Citations with no line information never merge: two chunks
from one file have not been shown to be the same region.

Two citations claiming one span from two different corpora do not merge —
that would attribute one corpus's content to another. It is a conflict
error.

### Markdown footnote rendering

The `--cite` output and MCP search responses render the set as Markdown
footnotes: a reference `[^n]` inline, and one definition line per citation.

```
[^1]: docs/retrieval.md lines 10-24 (score: 1.000)
[^2]: corpus memories (score: 0.427) [untrusted]
```

Footnote numbers are 1-based positions in the set, not retrieval ranks,
which have gaps wherever a merge or a withheld result removed a row. A
source with no path is identified by its corpus, or by its chunk id when it
has neither. Scores render at fixed width so a very small score is never
shown in exponential form. A citation whose trust is anything other than
trusted is marked `[untrusted]`, because the rendered form is what a reader
actually sees.

Rendering is deterministic: the same set produces the same bytes, on every
run and every platform.

### Error paths

| Case | Result |
| --- | --- |
| Nil result list | Invalid-input error. |
| Empty result list | An empty set and no error — an answer with no sources is correctly cited by citing nothing. |
| No source resolver | Invalid-input error. Citations are never assembled without knowing what the session may see. |
| Result with no chunk id | Invalid-input error. |
| Overlapping span claimed by two corpora | Conflict error. |
| Result the resolver withholds | No citation, counted in `Withheld`. |

## Configuration

The `[retrieval]` section of `config.toml` is the whole externally tunable
surface of the pipeline above. Its reload class is hot: a valid change is
picked up by a running daemon and takes effect on the next query, without
a restart.

| Key | Type | Default | Valid range | Hot reload | Description |
| --- | --- | --- | --- | --- | --- |
| `retrieval.sources` | array of strings | none | each entry non-empty and unique | yes | The corpus sources the ingest and index-lifecycle surfaces read. |
| `retrieval.fusion.k` | integer | 60 | greater than zero | yes | The RRF smoothing constant. This IS the `k` of the fusion formula; there is no separate top-K key. |
| `retrieval.fusion.weights` | table of numbers | none | each weight finite and not negative | yes | Per-leg fusion weights, keyed by leg name (`fts5`, `vector`). An unset table leaves every leg neutral. |
| `retrieval.reranker.enabled` | boolean | `false` | — | yes | Gates the optional post-fusion reranker stage. |

`retrieval.fusion.enabled` is documented with the fusion gate it belongs
to and is not validated by this surface.

```toml
[retrieval]
sources = ["handbook", "notes"]

[retrieval.fusion]
k = 60
weights = { fts5 = 1.0, vector = 1.0 }

[retrieval.reranker]
enabled = false
```

Every key is readable with `cascade config get retrieval.<key>` and
writable with `cascade config set retrieval.<key> <value>`. Setting a key
to the value it already holds converges: the file is unchanged and the
reload is a no-op.

### Validation fails closed

The loader refuses the whole config rather than falling back to a default,
for every case below. An operator who configured retrieval and silently
received the defaults would have been misled about what their system is
doing, which is a worse outcome than a startup that stops and says why.

| Case | Result |
| --- | --- |
| Unknown key under `[retrieval]` or `[retrieval.fusion]` or `[retrieval.reranker]` | Config error naming the key. |
| `[retrieval]`, `[retrieval.fusion]` or `[retrieval.reranker]` is not a table | Config error naming the section. |
| `sources` is not an array, or an entry is not a string | Config error naming the entry by index. |
| An empty or duplicated `sources` entry | Config error naming the entry by index. |
| `fusion.k` is not an integer, or is zero or negative | Config error. A non-positive `k` is refused, never defaulted. |
| `fusion.weights` is not a table, or a weight is not a finite, non-negative number | Config error naming the weight. |
| `reranker.enabled` is not a boolean | Config error. |
| A fusion weight names a leg that did not run in the query | Invalid-input error from the fusion. A misspelled leg name would otherwise look exactly like an applied weight. |
| `reranker.enabled = true` with no reranker implementation registered | Invalid-input error from the reranker stage. |

The same rules apply to a value supplied through the environment
(`CASCADE_RETRIEVAL__FUSION__K` and friends): an override is held to the
file's standard, so an exported bad value is reported rather than ignored.
