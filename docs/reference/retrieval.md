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
