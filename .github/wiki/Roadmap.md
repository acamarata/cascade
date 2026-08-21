# Roadmap

This page describes planned work for Cascade. Items are organized by phase. No delivery dates are committed.

---

## Current phase: P7 - True-100 Completion

P7 is in active development. It is a completion phase rather than a feature
phase: its purpose is to close the gap between what the codebase claims and
what it actually does, ahead of a stable public release.

The recurring theme, and the reason the phase exists, is capability that was
built but never wired to a consumer — a signature verifier the load path never
called, a cross-session dedup pool only tests constructed, a retriever field
nothing injected. P7 audits for that class of gap and closes it.

Work items include:

- Security hardening of the plugin surface: signature verification enforced at
  load, WASM FFI boundary bounds-checking, capability resolution with symlink
  containment
- Daemon resilience: a process panic hook, supervised subsystem tasks with
  restart policies, and poison-recovering locks on hot paths
- Retrieval quality: tree-sitter code chunking, multi-vector late interaction,
  and the search configuration actually reaching the search
- Supply chain: keeping `cargo-audit`, `cargo-deny` and `pnpm audit` at zero
  open advisories rather than accumulating documented exceptions
- Documentation accuracy, including this page

## Completed phases

| Phase | Theme |
|---|---|
| P1 | Core resolver, tier model, CLI foundations |
| P2 | Daemon, IPC, desktop app and macOS widget |
| P3 | Providers, key-pool proxy, quota and fleet |
| P4 | RAG pipeline, MCP server, WASM plugin system |
| P5 | Scaling, packaging and distribution channels |
| P6 | Agents, orchestration and the conductor |

Released versions are listed in the [Changelog](CHANGELOG.md).

---

## Coming next: P8 - Go-core migration

P8 scope is planned but not started, and may change:

**Language split**

- Migrate the daemon orchestration, CLI surface and dispatch state machines to
  Go, keeping Rust for the components where it is specifically better:
  embeddings, vector search and reranking, plus the Tauri desktop shell
- Publish the assistant plugin as its own repository

**Ecosystem**

- Plugin registry and signed plugin distribution
- Plugin update checks
- Broader first-party provider presets

**Multi-machine and team features**

- Encrypted sync of cascade tiers via a user-controlled backend (no Cascade cloud)
- Team tier: a shared URL pointing to a git repo, merged above PRC in the hierarchy
- Conflict resolution UI in the desktop app

---

## Known limitations in the current release

These are limitations verified against the code, not aspirations:

- The WASM plugin sandbox does not expose network access to guests. A plugin's
  `net` permission list is recorded in its manifest but WASI sockets are not
  added to the linker; plugins that need HTTP use a host function instead.
- `cascade.search_codebase` accepts a `lang` argument but uses it only to
  annotate results. `RetrieveOpts` has no language field, so retrieval is not
  filtered by language.
- The multi-vector (ColBERT) retrieval channel only contributes for chunks that
  have stored token embeddings. Chunks indexed before multi-vector storage was
  enabled keep their three-channel score until reindexed.
- The MCP server does not implement the MCP sampling primitive.
- The bundled WASM policy (`bundled-policy-wasm`) requires an OPA-built artifact
  that is not checked in, so that feature does not build from a clean checkout.
  Policy evaluation falls back to the simple evaluator by default.

---

## How to influence the roadmap

Open a [GitHub issue](https://github.com/acamarata/cascade/issues) with your use case. Feature requests are tracked there. The most-requested items with clear use cases move up.

See also: [Contributing](Contributing.md) · [Changelog](CHANGELOG.md)
