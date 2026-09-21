# Context Reference

## Session scope

`cascade context scope show [--json]` resolves the current session's
scope: a deny-by-default view of which project, product, and workspace
the current working directory belongs to, and which other scopes it may
see.

The resolved record carries exactly these fields:

| Field | Meaning |
|---|---|
| `kind` | `general` for an unresolved directory, or `session` once a repository is resolved. |
| `user` | Caller-supplied user identifier. |
| `machine` | Caller-supplied machine identifier. |
| `cwd` | The working directory scope was resolved from. |
| `workspace` | The resolved workspace id, if the project declares one. |
| `product` | The resolved product id, if the project declares one. |
| `project` | The resolved project id (the repository's own id). |
| `repository` | The repository record (remote URL + path hash), or absent. |
| `package_path` | The path from the repository root to `cwd`. |
| `branch` | Caller-supplied branch. |
| `task` | Caller-supplied active task id. |
| `session` | Caller-supplied session id. |
| `explicit_overrides` | Caller-supplied override value, preserved as-is. |

### Resolution order

1. Resolve the git root anchor for `cwd`.
2. Look up the repository record bound to that root.
3. If no repository is bound, return a **general** scope: user tiers plus
   the cascade-pa context only, with no project, product, workspace,
   branch, or task state. This is a successful, restricted result — never
   a fallback to an unscoped or global query.
4. Otherwise, walk the persisted scope graph outward from the project to
   its declared workspace/product membership.
5. Attach the caller-supplied branch, active task, and session.

### Explicit edges

Relationships between scopes are explicit, persisted data — never
inferred from name, path, or vocabulary overlap. Exactly three edge
values exist:

- `depends_on`
- `member_of`
- `shares_context_with`

An unknown edge value is refused with a typed invalid-input error, never
silently accepted or ignored.

### Deny-by-default candidate set

A session's candidate scope set is exactly:

- its own resolved scope chain (session → task → project →
  workspace/product), and
- the direct targets of `depends_on`/`shares_context_with` edges declared
  FROM a scope in that chain.

No other scope is visible. Two projects that happen to share vocabulary
never see each other's records unless an edge between them is explicitly
declared and persisted first.

## Traversal table

Visibility across the scope graph is governed by a single closed table,
`Traversal(kind, edgeClass)`, keyed on the scope kind (`session`, `task`,
`project`, `workspace`, `product`, `global`) and the edge class the
relationship maps onto:

- `member_of` → the `member` edge class
- `depends_on` and `shares_context_with` → the `route` edge class
- a session's own resolved chain → the `parent` edge class (never a
  persisted row)

A `(kind, edgeClass)` pair absent from the table is **not traversable** —
there is no permissive default. Widening the table requires updating the
pinned golden test alongside it, so an accidental widening fails CI
rather than silently shipping.

Cycles are rejected when a `member_of` edge is stored, not discovered
later when something tries to traverse it: an edge that would close a
containment cycle is refused with a typed invalid-input error at write
time.

### Addressing boundary

This scope graph and its traversal table are the routing authority a
notification or message address resolves against — they are not
themselves an authorization or capability system. Sending across a scope
boundary requires a separate capability check, carried by the components
that own delivery; the traversal table only decides what is visible, and
an address alone never widens that.

## Daemonless behavior

`cascade context scope show` works with or without a running daemon. When
a daemon is confirmed reachable, the command dials it over the daemon's
RPC surface (`context.scope.show`). Otherwise it resolves the scope
in-process against the local `cascade.db` file. Both paths return
identical data. The command never prompts, is unaffected by
`CASCADE_NO_INPUT`, and runs on Windows exactly like every other
one-shot, daemonless command.

## Rolling summaries

When a block of content does not fit the remaining token budget, the context
composer asks the rolling summarizer for a shorter stand-in. Summaries are
kept at three granularities:

| Level | Scope | Source window | Summary bound |
|---|---|---|---|
| `turn-window` | the recent turns of one thread | 4000 tokens | 500 tokens |
| `thread` | one whole topic thread | 16000 tokens | 250 tokens |
| `epoch` | a period spanning several threads | 64000 tokens | 125 tokens |

**What selects a level.** The remaining budget does. A level is affordable
when its whole summary still fits what is left, so a large remaining budget
gets the least compressed level and a tight one gets the most compressed. The
boundaries are the two higher-detail levels' own bounds: 500 tokens or more
selects `turn-window`, 250 to 499 selects `thread`, and anything below 250
selects `epoch`. All three windows and bounds are code defaults. There is no
configuration section for them.

**Rolling, not rebuilt.** A regeneration is handed the summary already on
record together with the new content window, and the model is asked to update
the old summary so it also covers the new material. A summary therefore keeps
carrying facts from content that has since fallen out of the window, instead
of being rebuilt from whatever slice of history happens to fit.

**Staleness and failure are never served silently.** A stored summary records
the version of the source it was generated from. When the current source
differs, the summary is stale and a regeneration runs behind a guard keyed on
the entity, the level and the source version, so concurrent callers of the
same version share one regeneration while a caller holding newer content
starts its own. If that regeneration fails, the engine reports the failure and
the staleness as events naming which stage failed, and the composer is given
an error rather than the old text, and it drops the block from the assembly
and records why. A summary that describes a previous version of the source
never reaches an assembly that claims to describe the current one.

**Size bound.** A model response longer than its level's bound is a failed
summarization, not a large one. It is not stored and not served, and the
failure names the size that broke the bound.

**Cheap-lane routing.** Every summarization is dispatched through the
Conductor with task class `summarize`, whose taxonomy row carries a cheap lane
affinity and low reasoning requirement. The summarizer names the task class
and nothing else about the lane: which lane serves it is the router's
decision.

**Sensitivity is inherited.** A summarization request declares no policy and
leaves its sensitivity tier unset, which resolves to `restricted`. The thread's
own privacy mode is applied by the router's first filter, from the request
context, on every selection. The summarizer cannot widen it.

## Pre-/post-assembly pipeline stages

The composer optionally runs two more passes around its own assembly pass,
distinct from rolling-summary substitution above: a pre-assembly stage, run
once over the caller's slots before assembly begins, and a post-assembly
stage, run once over the assembled context before the result is returned.
Neither is attached by default; a caller opts in per stage.

| Stage | When it runs | What it does | Task class | §5.16 lane affinity |
|---|---|---|---|---|
| Pre-assembly, classify | before assembly | labels every slot | `classify` | cheapest/free |
| Pre-assembly, segment | before assembly | splits over-long slots | `segment` | cheapest/free |
| Post-assembly, summarize | after assembly | condenses the assembly | `summarize` | cheap |

A caller attaches at most one pre-assembly stage, in one of its two kinds,
and at most one post-assembly stage. Each declares a fixed task class that
never varies with input.

**Each stage transforms the composition.** The classify stage dispatches one
request over the slots and writes a category label onto each of them, which
the composer carries onto every slot in its result. The segment stage
dispatches one request and replaces each slot longer than its equal share of
the input with the parts the model's offsets cut it into, so the assembly
loop composes the split slots rather than the original ones. The summarize
stage dispatches one request over the assembled context and, when the answer
comes back genuinely shorter, the composer replaces the assembled content
with it and re-measures the token accounting, so the bounded-context
invariant still describes what it returns. This post-assembly pass is
unrelated to the rolling summarizer above, which substitutes for one
oversized slot during assembly instead.

**A response that does not honor its protocol is declined, not guessed at.**
Each stage states its response format in its own prompt and parses exactly
that: one label per slot, one offset line per slot, or a condensation
shorter than the input. A response that arrives in some other shape, or one
whose condensation is not smaller, leaves the working set exactly as it
arrived. Nothing is partially applied.

**A stage that cannot run never fails the composition.** A dispatch failure,
a router refusal, no eligible lane, a declined response, or a condensation
the composer cannot measure all degrade to plain assembly: the caller gets
the result it would have got with no stage attached, and one degrade event
records the stage, its task class, a fixed reason and the failure's kind. A
caller that asked for context is never handed an error because a free lane
was busy. There is also no retry on a stronger lane: spending expensive-lane
capacity is exactly what pinning these stages to cheap lanes prevents.

**Determinism.** The assembly loop itself is deterministic, and attaching a
stage bounds that determinism by the stage's model, exactly as attaching a
rolling summarizer already bounds it on the overflow path. A caller that
needs a byte-identical assembly across runs attaches neither, which is the
default.

**Cheap-lane routing and inherited sensitivity** work exactly as described
for rolling summaries above: each stage names its task class and nothing
else about the lane, and every request it builds declares no policy and
leaves sensitivity unset so the thread's own privacy mode, applied by the
router, is never widened.

**What the reply pipeline may attach.** Nothing in the shipping binary
attaches a stage yet. The first production composition is the chat reply
pipeline; whether it attaches a pre-assembly stage, which kind, and whether
it condenses afterwards are that surface's own decisions, and the composer
behaves identically with none, one or both attached.
