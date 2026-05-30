# Quickstart

This guide gets you from a fresh install to a working cascade in about 30 minutes. You will end up with a global instruction file, a project-level override, and your first semantic search working.

## Before you start

Make sure Cascade is installed and accessible:

```bash
cascade --version
```

If that fails, see the [Install](Install.md) page.

## Step 1: Run the setup wizard (5 min)

```bash
cascade init
```

The wizard opens in your terminal. It will:

1. Detect which AI coding tools are installed on your machine.
2. Ask which tools you want Cascade to manage.
3. Create `~/.cascade/CASCADE.md` — your global instruction file.
4. Write the derived files each tool expects (`CLAUDE.md`, `AGENTS.md`, `.aider.md`, etc.) and set up symlinks where appropriate.
5. Start the background daemon and indexer.

Answer the questions; the defaults are sensible for most setups. The wizard takes about 3-5 minutes to complete.

## Step 2: Edit your global instructions (10 min)

Open `~/.cascade/CASCADE.md` in any editor. This file is your **GCI** (Global Cascade Instructions) — rules and context that apply to every project on your machine.

A starter template is already there. Add your preferences:

```markdown
# My Global Context

## About me
I'm a backend engineer working primarily in Rust and TypeScript.
Prefer functional patterns. Avoid mutable global state.

## Code style
- 80-char line limit where practical
- Explicit error handling — no silent ignores
- Prefer composition over inheritance
```

Save the file. Cascade detects the change and updates all derived files within a few seconds.

## Step 3: Add project-level context (10 min)

Navigate to one of your projects:

```bash
cd ~/projects/my-app
cascade edit
```

This opens (or creates) `CASCADE.md` in the current directory — your **PRC** (Per-Repo Cascade). Rules here apply only to this project, and they can override or extend your global rules.

Add something project-specific:

```markdown
# my-app context

This is a Next.js 14 App Router project using TypeScript strict mode.
The API is at `app/api/`. Shared utilities live in `lib/`.
Do not modify files in `generated/` — they are auto-generated.
```

Save. Cascade merges this with your global context and updates the tool-specific files.

## Step 4: Verify the tool sees your context (2 min)

Open your AI tool in this project (Claude Code, Cursor, etc.) and ask it something context-specific:

```
What patterns should I follow in this project?
```

It should reflect the rules you just wrote. If it does not, run `cascade status` to check that the indexer is running and the derived files are up to date.

## Step 5: Try a search (3 min)

```bash
cascade search "how should I handle API errors"
```

Cascade searches your instruction files and returns the most relevant context. Results include the source file and a confidence score. This is the same search the MCP server uses when a tool queries for context.

## Next steps

- Read [The Cascade Concept](Cascade-Concept.md) to understand the six-tier hierarchy.
- See [Six-Tier Taxonomy](Six-Tier-Taxonomy.md) for a complete picture of GCI / PCI / APC / PPC / PRC / PAC.
- Set up [RAG](RAG-Setup.md) to index your codebase (not just your instruction files).
- Connect the [MCP Server](MCP-Server.md) if your tool supports it.
- Follow a complete [tutorial](https://github.com/acamarata/cascade/tree/main/docs/handbook/src/tutorials/) for deeper walkthroughs.
