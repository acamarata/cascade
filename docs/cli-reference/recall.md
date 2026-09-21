# `cascade recall`

Reference for the `recall` noun (07-CLI-COMMAND-TREE.md §recall; rpc:
`recall.*`). One verb, `cascade recall <query>`, implemented in
`cmd/cascade/recall.go` over the query service in
`internal/retrieval/recall` (P1-E06-W2-S11-T3).

**Mounting status:** mounted. `cmd/cascade/root.go`'s `mountSubcommands`
calls `mountRecallCmd(root)`, and the daemon side is registered by
`registerRecallHandler` inside `buildRPCServer`
(`cmd/cascade/daemon_unix_run.go`). Both directions are proven by tests
that go red when the line is removed:
`TestRecallResolvesOnTheRealRootCommand` (cmd/cascade/recall_test.go) and
`TestRecallIsReachableOnTheDaemonTheCompositionRootBuilds`
(cmd/cascade/recall_integration_test.go).

## Help output

This block is the command's real `--help`, captured from a built binary.

```
$ cascade recall --help
Run <query> against the retrieval index and print the fused, ranked
results with the corpus and trust tag behind each one.

Results are ranked by reciprocal rank fusion over every retrieval
leg this build has available; --k caps how many are returned and
--corpus narrows the search to named corpora. Nothing outside the
scope named by --scope is searched, ranked or cited.

A query that matches nothing prints that and exits 0. A malformed
query, an unknown corpus and an unreadable index each fail with a
typed error and its own exit code, so an empty answer is never
confused with a broken index.

Usage:
  cascade recall <query> [flags]

Examples:
  cascade recall "reciprocal rank fusion"
  cascade recall "retry policy" --corpus handbook --k 5 --cite
  cascade recall "retry policy" --json

Flags:
      --cite             print the Markdown citation block under the results
      --corpus strings   restrict the search to named corpora (repeatable)
  -h, --help             help for recall
      --k int            maximum number of results to return (default 10)
      --scope string     the session scope to search within

Global Flags:
      --config string    override the config file path
      --json             emit output as a versioned JSON envelope
      --no-color         disable colored output (also respects NO_COLOR; see docs/cli-output-contract.md)
      --profile string   select a named config profile
  -q, --quiet            suppress progress output
  -v, --verbose          increase log verbosity
```

## What it does

`recall` runs one fused query over the retrieval index and prints the
ranked results with the corpus and TRUST tag behind each. Ranking is
reciprocal rank fusion (`internal/retrieval/rrf`) over every retrieval
leg the build has available; the citation set
(`internal/retrieval/citations`) rides every response, and `--cite` adds
its rendered Markdown footnote block to the human output.

The command is one-shot and non-interactive: it takes its whole input
from the argument and the flags, prompts for nothing, and `CASCADE_NO_INPUT`
therefore has no effect on it (06-FORGE-SPEC §5 rule 8).

## Scope

`--scope` names the session scope the query is asked from. Nothing
outside it is searched, ranked or cited: the scope filter
(`internal/retrieval/fusion.ScopeFilter`) narrows the candidate set
BEFORE either leg runs, and every ranked row is re-resolved against that
same filter before it is described. A row that does not resolve is
counted in `withheld` and nothing else about it — not its path, not its
corpus, not its id — appears in the output, in the citations, or in any
diagnostic.

Personal-tier content is served only to a query that states personal
entitlement. The CLI does not expose an entitlement flag, so
`cascade recall` runs at the project tier: the fail-closed default.

## Empty answers versus broken indexes

A query that matched nothing prints `no results` and exits **0**. That is
not an error, and it is deliberately the only case that returns an empty
list, because a user cannot tell an empty list from a broken index. Every
other empty outcome is a typed refusal with its own exit code:

| Situation | Kind | Exit |
|---|---|---|
| Empty or whitespace-only query; bad `--k`; malformed `--scope` | `invalid_input` | 2 |
| `--corpus` names nothing this scope can read | `not_found` | 3 |
| No retrieval index has been built yet | `not_found` | 3 |
| The index catalog exists but cannot be read | `unavailable` | 5 |
| No retrieval leg is available in this build | `unavailable` | 5 |
| The index catalog is damaged | `integrity` | 13 |
| The index catalog is from a newer build | `unsupported` | 12 |

Exit codes are the A-T7 taxonomy's (`pkg/cascade/codes.go`); the same
Kinds cross the wire as the taxonomy's JSON-RPC codes.

## `--json`

`--json` emits the shared versioned envelope (docs/cli-output-contract.md).
Its `data` member is the `recall.query` RPC result verbatim — the same
struct, so the human table and the JSON payload cannot describe different
answers.

## RPC and the v1-parity alias

The CLI routes through the Go IPC client SDK (`internal/client`) to
`recall.query` on the daemon. The v1 name `cascade_search` is registered
as an alias bound to the same handler value, so callers written against
v1 keep working and the two names cannot answer differently.

## `cascade recall index`

The index lifecycle noun. Four subcommands, all mounted on the real root
and all reachable from the built binary; each is also a daemon RPC method
(`recall.index.rebuild`, `.verify`, `.migrate`, `.update`), registered by
`internal/daemon.RegisterRecallIndexHandler` from `buildRPCServer`.

| Subcommand | What it does |
| --- | --- |
| `rebuild` | Re-runs the whole ingest, chunk, index and embed pipeline over every registered source from a clean slate. |
| `verify` | Reports missing, orphaned and vector-incomplete chunks against the registered sources. Read-only. |
| `migrate` | Converges the retrieval index domain's on-disk schema to the one this binary ships. |
| `update` | Re-ingests only the files a git diff reports changed since the last run. |

`verify` is the one to reach for first: it is read-only and tells you
whether the index is behind, inconsistent, or fine. `rebuild` is the
documented repair path and is deliberately something a human or a script
invokes, never something `cascade doctor --fix` triggers on its own
(R-21.189).

### The doctor check

`cascade doctor` runs a `retrieval_index` check that reports whether the
index is present, current and consistent. It is registered in
`productionCheckRegistry`, so it runs in a shipped binary rather than only
under test.

A fresh install with no index yet reports OK, not a warning. An absent
index on a machine that has never indexed anything is the expected state,
and a check that cries about it teaches people to ignore `doctor`.

### The generation marker is computed once

The index records the tree it was built from as a **generation marker**:
the current commit, plus a digest of whatever is uncommitted in the working
tree. The second half is what lets drift be noticed before a commit rather
than only after one.

Both surfaces that report on the marker — `cascade recall index verify` and
`cascade doctor`'s `retrieval_index` check — compute it with the **same
function**, `internal/daemon.GitTreeHash`. There is deliberately only one
implementation. There used to be two, the doctor carrying its own copy
under a comment promising the algorithm was identical; the copy hashed
`git status --porcelain` without trimming it, so on any working tree with
an uncommitted change the two disagreed: `verify` reported the marker
current while `doctor` reported it drifted and exited 5. A clean checkout —
every test fixture, every CI run — hashed identically either way, so
nothing caught it until the wave gate ran the real binary on a real
machine (R-14.278).

A machine with no git repository has no marker to compute. That is a
supported configuration: the marker then reads as drifted, which is the
fail-closed direction.

### Schema versions

The retrieval index shares one globally keyed migration ledger with every
other domain in the same database file: `applied_migrations` has no
per-MigrationSet identity column. A domain schema therefore claims the
next unused version number for the whole file, and the binary must declare
that it understands that version through `runtimeReaderCeiling()` in
`cmd/cascade/daemon_unix_store.go`. Skipping the second half leaves a
daemon that refuses to reopen the database it just wrote.

`migrate` reports what it applied. An empty migration set writes no ledger
row at all, and the result says so rather than claiming an application
that never happened.

## `cascade recall what`

`recall what <query>` (P1-E22-W5-S47-T1, R-14.65/66; rpc `recall.what`,
`internal/retrieval/recallwhat*.go`) fuses the domains this build can scope —
files and memory; the conversation leg is excluded until threads carry a
scope reference and is reported as an unavailable domain — into one ranked,
cited answer. Unlike
the bare `recall` command it takes **only** the positional query: there
is no `--scope`, `--corpus`, `--k` or `--cite` flag (07-CLI-COMMAND-TREE
§recall ratifies none). The session scope is resolved by the daemon from
this process's working directory (`context/scope.ResolveSessionScope`,
E/S-08.T4) — the CLI asserts nothing about its own scope.

```
$ cascade recall what "why did the retry policy change"
```

**Domains and their trust/privacy handling:**

- **files** — reuses `internal/retrieval/recall.Service` whole: the same
  scope filter, RRF fusion and citation set as the bare command.
- **memory** — the memory projection's indexed read model
  (`internal/memory.ProjectionJob.SearchIncludingExpired`), narrowed to
  the resolved scope by `ScopeRef` equality. This leg calls the
  `...IncludingExpired` variant rather than plain `Search` deliberately: a
  row past its TTL is still a candidate here (see R-16.7 demotion below),
  where the bare `recall` command's own leg would exclude it outright. A
  superseded or expired `MemoryEntry` never ranks above the entry that
  supersedes it, or above a non-expired peer — demoted, not excluded,
  reordering only, no rescoring (`recallwhat_filter.go`'s
  `demoteSupersededAndExpired`, pinned against a golden fixture at
  `internal/retrieval/testdata/v1-goldens/recallwhat_ranking.json`).
- **conversation** — **unconditionally excluded.** `conversation.Thread`
  carries no `ScopeRef` field and `conversation.SearchFilter` carries only
  `ThreadID`/`Limit`, so this leg has no server-side column to narrow a
  search by. Running it unscoped would leak a thread across project
  boundaries — reproduced directly: a public thread created under one
  project, returned to a `recall.what` caller whose cwd resolves to a
  different project — so instead the leg never runs at all:
  `SearchTurns`/`ThreadPrivacy`/`ListSegments` are never called, and every
  response carries `Errors["conversation"]` (`KindUnavailable`,
  "the conversation domain cannot be narrowed to a session scope")
  regardless of any given thread's role, tier, or trust. A widening ticket
  to add `ScopeRef` to `Thread` and `SearchFilter` is tracked separately;
  the leg is restored there, not before.

**Two unconditional exclusion rules**, both enforced by the real egress
boundary (`internal/hooks/egress.Engine`, class `recall-what`,
`EgressClassRecallWhat`) rather than by anything the caller asserts about
itself: (1) content tagged `TrustUntrustedSource` (a tool-authored
conversation turn, or a file the corpus model tags untrusted) never
appears in a `recall.what` answer. (2) a conversation thread whose own
privacy tier the egress class does not admit (it admits only
`internal`/`public`) is excluded — currently unreachable in practice since
the conversation leg above never runs at all, but tested and kept ready
for the day that leg is restored. Every outbound field — snippet, path,
memory key, the rendered citation block, and each per-domain error
string — transits the same boundary before it reaches the wire, so a
vaulted or credential-shaped value cannot leak through any of them
(H/S-16.T1).

`Withheld` counts rows an authorization or privacy decision dropped.
`Truncated` is a **separate** count for rows dropped only because there
were more than the result cap — the CLI reports the two with different
wording ("excluded by scope or privacy policy" vs. "not shown (result
cap)"), never conflating a privacy exclusion with a plain overflow.

Domain-unavailable errors (an index or store this build could not reach)
degrade to a partial answer: the human table reports a count
("N domain(s) unavailable"), `--json` carries the per-domain detail in
`errors`.

## Not here

- The `cascade what` hidden alias is deferred to V/S-47.T5.
- The `[retrieval]` config surface belongs to F/S-12.T4.
- The mirrored MCP tool `cascade_recall_query` is not exposed yet: the
  MCP tool table is sourced from plugin manifests
  (`internal/mcp.NewToolRegistry(plugin.Builtins)`), and plugins may not
  import `internal/**` (Art.10.2, enforced by
  `internal/build/arch_test.go`'s plugins-providers-boundary rule), so no
  mechanism exists yet for mirroring a core RPC namespace into that one
  table. It is genuinely absent rather than faked (Art.1).
