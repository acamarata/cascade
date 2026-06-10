# Cascade

Cascade is a local-first, vendor-neutral AI context manager. It maintains a structured hierarchy of instruction files that any AI coding tool can read, keeps them consistent across your machine, and makes your project context searchable via built-in RAG.

No cloud sync. No lock-in. Your instructions live on disk in plain Markdown.

## What it does

You write rules and context once, in `CASCADE.md` files organized by scope. Cascade resolves the right set of files for each project, writes derived files that each tool expects (like `CLAUDE.md` or `.cursorrules`), and indexes everything for semantic search.

A six-tier hierarchy governs scope: global defaults flow down to project-level, then repo-level, then app-level. Lower tiers add specificity; higher tiers set defaults. Conflicts resolve predictably.

## Quick navigation

**Getting started**
- [Install](Install.md) — macOS, Linux, Windows
- [Quickstart](Quickstart.md) — your first cascade in 30 minutes
- [Onboarding Wizard Walkthrough](Onboarding-Wizard-Walkthrough.md)

**Concepts**
- [The Cascade Concept](Cascade-Concept.md) — why cascading instructions
- [Six-Tier Taxonomy](Six-Tier-Taxonomy.md) — GCI / PCI / APC / PPC / PRC / PAC
- [Architecture](Architecture.md) — system components and data flow
- [Glossary](Glossary.md)

**Reference**
- [CLI Reference](CLI-Reference.md)
- [Templates](templates.md)
- [Configuration Reference](Configuration-Reference.md)
- [RAG Setup](RAG-Setup.md)
- [MCP Server](MCP-Server.md)

**Tool integrations**
- [Claude Code](Integration-Claude-Code.md) · [OpenCode](Integration-OpenCode.md) · [Codex](Integration-Codex.md)
- [Cursor](Integration-Cursor.md) · [Aider](Integration-Aider.md) · [Windsurf](Integration-Windsurf.md) · [Antigravity](Integration-Antigravity.md)

**Help**
- [Troubleshooting](Troubleshooting.md) · [FAQ](FAQ.md)

**Community**
- [Contributing](Contributing.md) · [Governance](Governance.md) · [Roadmap](Roadmap.md)
- [Plugin Development](Plugin-Development.md) · [Plugin Development Guide](plugin-development-guide.md) · [Building From Source](Building-From-Source.md)

**Project info**
- [Security](Security.md) · [Privacy](Privacy.md) · [Telemetry Consent](Telemetry-Consent.md)
- [Changelog](Changelog.md)

## 30-second example

```bash
# Install
brew install acamarata/tap/cascade

# Run the setup wizard
cascade init

# Search your context
cascade search "how do I handle auth in this project"
```

The wizard detects which AI tools you use, walks you through creating your first cascade hierarchy, and starts the background indexer. After that, your tools always see current, consistent context.
