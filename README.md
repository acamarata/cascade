# Cascade

[![CI](https://img.shields.io/github/actions/workflow/status/acamarata/cascade/ci.yml?branch=main&label=CI)](https://github.com/acamarata/cascade/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![crates.io](https://img.shields.io/crates/v/cascade-cli.svg)](https://crates.io/crates/cascade-cli)
[![GitHub release](https://img.shields.io/github/v/release/acamarata/cascade)](https://github.com/acamarata/cascade/releases/latest)

A local-first AI context manager for coding agents. Write your rules once, at the right scope, and every tool picks them up automatically.

---

## What is a cascade?

Most AI coding agents read instruction files: `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and others. As soon as you work across multiple repos and multiple tools, those files multiply and drift. You end up with the same rules in ten places, tools seeing different subsets, and no clear answer for what wins when files conflict.

A cascade is a six-tier hierarchy of `CASCADE.md` files organized by scope. Each tier inherits from the one above. When Cascade resolves instructions for a given working directory, it walks the hierarchy from global down to the narrowest scope, merges the result, and writes the tool-specific files each agent expects. One source of truth, always consistent.

## Features

- Resolves a 6-tier instruction hierarchy (GCI → PCI → APC → PPC → PRC → PAC) for any working directory
- Generates harness-native files (`CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and more) from a single source
- Local RAG index over your instruction corpus: FTS5 + dense embeddings + RRF reranking, no cloud required
- MCP server with 5 transports, exposing your cascade as context to any MCP-compatible tool
- WASM plugin system (wasmtime, capability-gated) with a PDK and `cargo-generate` template

## Install

### One-liner (macOS / Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

Downloads the latest release, verifies the SHA256 checksum, installs to `~/.local/bin`, and runs the daemon + init setup. No `sudo` required.

```sh
# Pin a version
CASCADE_VERSION=v1.0.0 curl -fsSL .../install.sh | sh

# Skip daemon + init (CI / unattended agents)
CASCADE_NO_DAEMON=1 CASCADE_NO_INIT=1 curl -fsSL .../install.sh | sh
```

### One-liner (Windows PowerShell)

```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\Cascade\bin` and adds it to your user PATH. No Administrator prompt required.

```powershell
# Pin a version
$env:CASCADE_VERSION = "v1.0.0"
irm .../install.ps1 | iex

# Skip daemon + init
$env:CASCADE_NO_DAEMON = "1"; $env:CASCADE_NO_INIT = "1"
irm .../install.ps1 | iex
```

### Package managers

| Platform | Install command |
|---|---|
| macOS | `brew install acamarata/tap/cascade` |
| Linux (AUR) | `yay -S cascade-bin` |
| Linux (Snap) | `snap install cascade` |
| Linux (Flatpak) | `flatpak install flathub dev.camarata.Cascade` |
| Windows (Winget) | `winget install acamarata.cascade` |
| Windows (Chocolatey) | `choco install cascade` |
| Windows (Scoop) | `scoop install cascade` |
| Nix | `nix run github:acamarata/cascade` |
| Any (cargo) | `cargo install cascade-cli` |

Full documentation for all install paths: [Installation wiki](../../wiki/Installation)

## Quick start

```bash
# 1. Install (macOS example)
brew install acamarata/tap/cascade

# 2. Run the setup wizard: detects your tools, creates your first hierarchy
cascade init

# 3. Edit your global rules
cascade edit --tier gci

# 4. Sync derived files to all connected tools
cascade sync

# 5. Search your indexed rule base
cascade search "authentication"
```

Full documentation: [the wiki](../../wiki) | [CLI reference](../../wiki/CLI-Reference) | [quickstart guide](../../wiki/Quickstart)

## Architecture

```
┌──────────────────────────────────────────┐
│              Cascade.app (Tauri 2)        │
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
│       127.0.0.1:9761 dashboard           │
└──────┬──────────┬───────────┬────────────┘
       │          │           │
  ┌────▼───┐ ┌───▼────┐ ┌────▼──────────┐
  │cascade │ │OS tray │ │  AI tools     │
  │  CLI   │ │widget  │ │  CC · OC      │
  │        │ │        │ │  Cursor·Aider │
  └────────┘ └────────┘ └───────────────┘
```

## Distribution channels

| Channel | Package | Notes |
|---|---|---|
| Homebrew Cask | `acamarata/tap/cascade` | macOS GUI + CLI |
| AUR | `cascade-bin` | Linux, pre-built binary |
| Winget | `acamarata.cascade` | Windows |
| Chocolatey | `cascade` | Windows |
| Scoop | `cascade` | Windows |
| Snap | `cascade` | Linux |
| Flatpak | `dev.camarata.Cascade` | Linux |
| Nix | `github:acamarata/cascade` | NixOS + flakes |
| crates.io | `cascade-cli` | `cargo install cascade-cli` |

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md) and the [Contributing wiki page](../../wiki/Contributing).

## License

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

MIT. See [LICENSE](LICENSE).
