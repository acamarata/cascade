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

## Not here

- `recall what` (the fused turns/threads/memories/files surface) and the
  `cascade what` hidden alias belong to V/S-47 (§D-21 strike).
- `recall index rebuild|verify|migrate` belongs to F/S-11.T4.
- The `[retrieval]` config surface belongs to F/S-12.T4.
- The mirrored MCP tool `cascade_recall_query` is not exposed yet: the
  MCP tool table is sourced from plugin manifests
  (`internal/mcp.NewToolRegistry(plugin.Builtins)`), and plugins may not
  import `internal/**` (Art.10.2, enforced by
  `internal/build/arch_test.go`'s plugins-providers-boundary rule), so no
  mechanism exists yet for mirroring a core RPC namespace into that one
  table. It is genuinely absent rather than faked (Art.1).
