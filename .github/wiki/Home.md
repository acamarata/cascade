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
- [Distribution Channels](Distribution-Channels.md) — all 9 install methods
- [Onboarding Wizard](onboarding-wizard.md) — guided setup walkthrough

**Concepts**
- [The Cascade Concept](Cascade-Concept.md) — why cascading instructions
- [Six-Tier Taxonomy](Six-Tier-Taxonomy.md) — GCI / PCI / APC / PPC / PRC / PAC
- [Architecture](Architecture.md) — system components and data flow
- [Cascade Resolution](Cascade-Resolution.md) — how tiers merge
- [Glossary](Glossary.md)

**Reference**
- [CLI Reference](CLI-Reference.md)
- [Templates](templates.md)
- [Configuration](Configuration.md)
- [RAG Pipeline](RAG-Pipeline.md)
- [MCP Server](MCP-Server.md)
- [Performance](cascade-performance.md)
- [Settings](cascade-settings.md)

**Tool integrations**
- [Tool Integration](Tool-Integration.md) — Claude Code, OpenCode, Cursor, Aider, Windsurf, Codex, Antigravity

**Plugins**
- [Plugin Development](Plugin-Development.md) — overview
- [Plugin Development Guide](plugin-development-guide.md) — full PDK reference
- [github-issues](plugins-github-issues.md) · [gitlab](plugins-gitlab.md) · [jira](plugins-jira.md) · [linear](plugins-linear.md)

**Daemon & OS**
- [Daemon Architecture](Daemon-Architecture.md)
- [Harness Bridge](Harness-Bridge.md)
- [Hooks](Hooks.md)
- [Scheduler](Scheduler.md)
- [Backup](Backup.md)
- [macOS Widget](cascade-settings.md) · [Windows Widget](cascade-widget-windows.md) · [Linux Widget](linux-widget-install.md)

**Help**
- [Troubleshooting](Troubleshooting.md) · [FAQ](FAQ.md)

**Community**
- [Contributing](Contributing.md) · [Governance](Governance.md) · [Roadmap](Roadmap.md)
- [Building From Source](Building-From-Source.md) · [Development Setup](Development-Setup.md) · [Testing](Testing.md)

**Project info**
- [Security](Security.md) · [Privacy](Privacy.md) · [Telemetry Consent](Telemetry-Consent.md)
- [Code Signing](Code-Signing.md)
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
