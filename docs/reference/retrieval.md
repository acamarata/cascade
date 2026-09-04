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
