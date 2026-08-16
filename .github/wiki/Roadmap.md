# Roadmap

This page describes planned work for Cascade. Items are organized by phase. No delivery dates are committed.

---

## Current phase: P4 - RAG, MCP & Plugin Ecosystem

P4 is in active development. Work items include:

- RAG pipeline with FTS5 + Multilingual E5 Large + RRF
- Context fingerprinting and cross-session dedup
- Cache management CLI (`cascade cache stats/clear`)
- Rollback and update commands
- MCP transport hardening and auth
- WASM plugin system with PDK
- Policy guardrail engine (`cascade policy`)
- Full wiki and documentation (this page is part of that effort)

---

## Coming next: P5 - Scaling & Ecosystem

P5 scope is preliminary and may change. Planned areas:

**Performance and scale**

- Incremental indexing for large codebases (100k+ files)
- Parallel ingestion pipeline
- Vector quantization for reduced index size
- Streaming search results for large result sets

**Provider integrations**

- OpenAI provider in the key-pool proxy
- Anthropic Claude API provider
- Local model provider via Ollama
- Per-project provider routing (different keys for different repos)

**Plugin ecosystem**

- Plugin registry at `plugins.cascade.dev`
- Signed plugin distribution
- Plugin update checks and auto-update
- Community plugin templates: Notion, Confluence, Linear, Jira, GitHub Issues, GitLab

**CLI and UX**

- `cascade watch` command for live context sync
- `cascade diff` to preview what a tier change would affect
- Shell integration hooks (prompt injection for context-aware completions)
- VS Code extension (palette commands, status bar)

**Multi-machine and team features**

- Encrypted sync of cascade tiers via a user-controlled backend (no Cascade cloud)
- Team tier: a shared URL pointing to a git repo, merged above PRC in the hierarchy
- Conflict resolution UI in the desktop app

---

## Known limitations in the current release

- The GUI app is macOS-only in the P4 release. Linux and Windows GUI are P5.
- The WASM plugin sandbox does not yet support network access capabilities.
- The MCP server does not yet support the MCP sampling primitive.
- Windows widget requires Windows 11; not available on Windows 10.
- The Multilingual E5 Large model requires substantial resident memory. A lighter model option is being evaluated.

---

## How to influence the roadmap

Open a [GitHub issue](https://github.com/acamarata/cascade/issues) with your use case. Feature requests are tracked there. The most-requested items with clear use cases move up.

See also: [Contributing](Contributing.md) · [Changelog](Changelog.md)
