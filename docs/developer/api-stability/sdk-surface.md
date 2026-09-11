# The pkg/ SDK surface

What `pkg/` is, and what it is not.

## What pkg/ IS

`pkg/` is the public Go API: the surface a plugin author, a provider
implementer, or any downstream Go consumer imports. It has three
directories today:

- **pkg/provider** - the contract types every provider implementation
  satisfies: `AgentProvider`, `ModelProvider`, `Embedder`, `Reranker`,
  `ReviewProvider`, `Store`, `VectorStore`, `Blob`, `Cache`, `Queue`,
  `Connector`. A downstream author implements one of these interfaces to
  add a new provider; they do not need to read anything under `internal/`
  to do so.
- **pkg/plugin** - the plugin SDK: the `cascade.plugin/v2` manifest
  schema, the plugin SDK itself, intents, and the registry client a
  plugin uses to interact with the host.
- **pkg/cascade** - the shared error taxonomy: the frozen 14-kind
  enumeration, the CLI exit-code table, and the JSON-RPC error-code
  table. Any package returning an error a caller across a process or RPC
  boundary needs to interpret returns one of these kinds; the tables are
  the single source both the CLI and the JSON-RPC layer key off.

## What pkg/ is NOT

`internal/` is the core engine: bootstrap, daemon, storage, retrieval,
policy, and everything else this project runs internally. It is not
importable by third parties. A plugin or provider author never imports
`internal/` directly, and Go's own `internal/` visibility rule enforces
this at compile time for anyone outside this module; the import-boundary
linter enforces the same rule as a stated law, not only as a Go language
mechanic, so a violation is caught even where a clever workaround might
otherwise slip past the language-level check.

## The import boundary, stated as SDK law

Plugins and providers import `pkg` only. `pkg` never imports `internal`.
This is enforced by a depguard rule in CI, and it is restated here as
policy so the enforcement mechanism and the promise it protects are both
on record: a downstream author who only ever imports `pkg/provider` or
`pkg/plugin` can rely on never pulling in an `internal/` implementation
detail transitively, because `pkg` itself cannot import one.
