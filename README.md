# Cascade

[![Build](https://img.shields.io/github/actions/workflow/status/acamarata/cascade/ci.yml?branch=main&label=build)](https://github.com/acamarata/cascade/actions)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Crates.io](https://img.shields.io/crates/v/cascade-core.svg)](https://crates.io/crates/cascade-core)
[![npm](https://img.shields.io/npm/v/@acamarata/cascade.svg)](https://www.npmjs.com/package/@acamarata/cascade)
[![docs.rs](https://img.shields.io/docsrs/cascade-core)](https://docs.rs/cascade-core)
[![MSRV](https://img.shields.io/badge/rust-1.78+-orange.svg)](https://www.rust-lang.org)
[![Discussions](https://img.shields.io/github/discussions/acamarata/cascade)](https://github.com/acamarata/cascade/discussions)

A six-tier instruction cascade manager for AI coding agents. Write your rules once, at the right scope, and every tool picks them up automatically.

---

## The problem

AI coding agents read instruction files (`CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and so on). Most teams end up with a tangle: rules duplicated across repos, tools that see different subsets, no clear override order. When you work across many repos and tool contexts, this becomes expensive to maintain and easy to get wrong.

## What Cascade does

Cascade manages a six-tier hierarchy of instruction files:

```
GCI  — global, applies everywhere
 └─ PCI  — per-project cluster
     └─ APC  — per-application
         └─ PPC  — per-product
             └─ PRC  — per-repo
                 └─ PAC  — per-app context
```

Each tier inherits from the one above. A rule at the GCI level applies everywhere unless a lower tier overrides it. Cascade resolves the full effective instruction set for any given working directory and writes the tool-specific files each agent expects.

## Install

**macOS (Homebrew)**
```bash
brew install acamarata/tap/cascade
```

**Linux (AUR)**
```bash
yay -S cascade-bin
```

**Windows (Winget)**
```powershell
winget install acamarata.cascade
```

**From source**
```bash
cargo install cascade-cli
```

Verify: `cascade --version`

## Quickstart

```bash
# First-time setup: interactive wizard
cascade init

# Edit your global rules
cascade edit --tier gci

# Sync derived files to all connected tools
cascade sync

# Search your indexed rule base
cascade search "authentication"

# Check status
cascade status
```

Full documentation: [the handbook](docs/handbook/) and [the wiki](../../wiki).

## Screenshots

| Onboarding wizard | Cascade editor | RAG search |
|---|---|---|
| *(screenshot — E5 beta)* | *(screenshot — E4 beta)* | *(screenshot — E7 beta)* |

## Features

- Automatic discovery and resolution of CASCADE.md files in the working tree
- Derived-file writer: generates CLAUDE.md, AGENTS.md, .cursorrules, and others from a single source
- MCP server exposing the resolved cascade as context to any MCP-compatible tool
- Local RAG index over your instruction corpus: semantic search, citations, no cloud required
- Onboarding wizard for first-run setup across 7 supported tools
- All data stays local; no telemetry by default

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md) and [the wiki Contributing page](../../wiki/Contributing).

## License

MIT. See [LICENSE](LICENSE).
