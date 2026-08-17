# Cascade

[![CI](https://img.shields.io/github/actions/workflow/status/acamarata/cascade/ci.yml?branch=main&label=CI)](https://github.com/acamarata/cascade/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![crates.io](https://img.shields.io/crates/v/cascade-cli.svg)](https://crates.io/crates/cascade-cli)
[![GitHub release](https://img.shields.io/github/v/release/acamarata/cascade)](https://github.com/acamarata/cascade/releases/latest)

A local-first AI context manager. Write your rules once, at the right scope, and every AI coding tool picks them up automatically.

---

## What is Cascade?

Most AI coding tools read instruction files: `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and others. When you work across multiple repos and tools, those files multiply and drift. You end up with the same rules in ten places, tools seeing different subsets, and no clear answer for what wins when files conflict.

Cascade fixes that with a six-tier hierarchy of `CASCADE.md` files organized by scope. Each tier inherits from the one above. When Cascade resolves instructions for a given working directory, it walks the hierarchy from global down to the narrowest scope, merges the result, and writes the tool-specific files each agent expects. One source of truth, always consistent.

The six tiers:

| Tier | Abbreviation | Typical location |
|---|---|---|
| Global Cascade Instructions | GCI | `~/.cascade/CASCADE.md` |
| Personal Cascade Instructions | PCI | `~/Downloads/.cascade/CASCADE.md` |
| All-Projects Cascade | APC | `~/Sites/.cascade/CASCADE.md` |
| Per-Project Cascade | PPC | `~/Sites/myproject/.cascade/CASCADE.md` |
| Per-Repo Cascade | PRC | `~/Sites/myproject/myrepo/.cascade/CASCADE.md` |
| Per-App Cascade | PAC | `~/Sites/myproject/myrepo/apps/myapp/.cascade/CASCADE.md` |

---

## What is shipped

- Six-tier instruction hierarchy (GCI → PCI → APC → PPC → PRC → PAC) resolved for any working directory
- Harness-native file generation: writes `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and more from a single source
- Local RAG index over your instruction corpus: FTS5 + Multilingual E5 Large
  dense embeddings + RRF fusion, no cloud required (`bge-m3` remains the
  backward-compatible configuration key)
- MCP server with multiple transports, exposing your cascade as live context to any MCP-compatible tool
- WASM plugin system (wasmtime, capability-gated) with a PDK and `cargo-generate` template
- Background daemon (`cascaded`) with file watcher that re-indexes and re-generates derived files on change
- CLI (`cascade`) for all operations: init, resolve, search, sync, link, doctor, and more
- Tauri 2 desktop app (macOS) with onboarding wizard, knowledge vault, graph view, and settings

---

## What is roadmap

See [Roadmap](../../wiki/Roadmap) for the full list. Short version:

- Linux and Windows GUI app (P5)
- OpenAI, Anthropic, and local model (Ollama) provider adapters
- Plugin registry at `plugins.cascade.dev` with signed distribution
- `cascade watch` for live context sync
- Team tier: shared instructions from a git URL merged above PRC
- VS Code extension

---

## Install

### One-liner (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

Downloads the latest release, verifies the SHA-256 checksum, installs to `~/.local/bin`, registers the daemon, and runs the init wizard. No `sudo` required.

```sh
# Pin a specific version
CASCADE_VERSION=v1.0.0 curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh

# Skip daemon registration and init (useful in CI)
CASCADE_NO_DAEMON=1 CASCADE_NO_INIT=1 curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

### One-liner (Windows PowerShell)

```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\Cascade\bin`. No Administrator prompt required.

### Package managers

| Platform | Command |
|---|---|
| macOS (Homebrew) | `brew install --cask acamarata/cascade/cascade` |
| Linux (AUR) | `yay -S cascade-bin` |
| Linux (Snap) | `snap install cascade` |
| Linux (Flatpak) | `flatpak install flathub dev.camarata.Cascade` |
| Windows (Winget) | `winget install acamarata.cascade` |
| Windows (Chocolatey) | `choco install cascade` |
| Windows (Scoop) | `scoop install cascade` |
| Any (cargo) | `cargo install cascade-cli` |

Full installation docs: [Installation wiki](../../wiki/Installation)

---

## Quick start

```bash
# 1. Initialize a cascade in your current directory
cascade init

# 2. Edit your global rules
cascade edit --tier gci

# 3. Generate harness files (CLAUDE.md, AGENTS.md, etc.)
cascade generate-instructions

# 4. Search your indexed rule base
cascade search "authentication"

# 5. Check that everything is wired correctly
cascade verify
```

Full walkthrough: [Quickstart](../../wiki/Quickstart)

---

## Interactive-session failover proxy (opt-in)

The daemon automatically runs a request-level Anthropic failover proxy on `127.0.0.1:3763`. Point an interactive `claude` session at it and every request is routed through Cascade's fleet account-priority logic (the same `select_account` spill order the conductor uses): if the chosen account is rate-limited or unauthenticated, the request transparently spills to the next account and the caller sees one continuous, correctly-framed SSE stream.

Activate it for one terminal session (explicit, never automatic — nothing mutates your shell config):

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:3763
claude
```

Deactivate by starting `claude` without that variable (or `unset ANTHROPIC_BASE_URL`).

**Billing note:** once activated, your interactive session's usage is billed against whichever fleet account the proxy selects (zai/GLM lane first, then Claude accounts) — not necessarily the account you originally logged in with. Sensitive prompts still respect the sensitivity firewall and never land on untrusted lanes.

Current fidelity limits (text-chat traffic): tool calls are rendered as text markers, and image/document blocks are not forwarded. `cascade doctor` reports whether the proxy is reachable and repeats the activation one-liner.

---

## Architecture

```
┌──────────────────────────────────────────┐
│           Cascade.app (Tauri 2)           │
│  Onboarding wizard · Knowledge vault     │
│  Template browser · Persona library      │
│  Project maps · Settings                 │
└─────────────────┬────────────────────────┘
                  │ JSON-RPC / HTTP
┌─────────────────▼────────────────────────┐
│              cascaded (daemon)            │
│  Cascade resolver · RAG indexer          │
│  MCP server · WASM plugin host           │
│  Context optimizer · Policy guardrails   │
│  Gemini key-pool proxy                   │
│  Anthropic failover proxy (:3763)        │
│       127.0.0.1:9761 dashboard           │
└──────┬──────────┬───────────┬────────────┘
       │          │           │
  ┌────▼───┐ ┌───▼────┐ ┌────▼──────────┐
  │cascade │ │OS tray │ │  AI tools     │
  │  CLI   │ │widget  │ │  CC · OC      │
  │        │ │        │ │  Cursor·Aider │
  └────────┘ └────────┘ └───────────────┘
```

The daemon is the center of gravity. It resolves the cascade hierarchy, writes derived files to each tool's expected location, watches the filesystem for changes, serves the MCP server, and runs the RAG pipeline. The CLI and GUI talk to the daemon via a local socket.

---

## Documentation

- [Quickstart](../../wiki/Quickstart) — first cascade in 5 minutes
- [Installation](../../wiki/Installation) — all install methods
- [Cascade Concepts](../../wiki/Cascade-Concepts) — why cascading instructions
- [Six-Tier Taxonomy](../../wiki/Six-Tier-Taxonomy) — GCI / PCI / APC / PPC / PRC / PAC
- [CLI Reference](../../wiki/CLI-Reference) — every command and flag
- [Configuration](../../wiki/Configuration) — config file reference
- [MCP Server](../../wiki/MCP-Server) — live context injection
- [Plugin Development](../../wiki/Plugin-Development) — WASM plugin guide
- [Architecture](../../wiki/Architecture) — system deep-dive
- [Roadmap](../../wiki/Roadmap) — what is coming

---

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md) and the [Building From Source](../../wiki/Building-From-Source) wiki page.

Bug reports and feature requests go to [GitHub Issues](https://github.com/acamarata/cascade/issues). Security issues go to the process in [SECURITY.md](SECURITY.md) — not as public issues.

---

## License

MIT. See [LICENSE](LICENSE).

Author: [Aric Camarata](https://github.com/acamarata)
