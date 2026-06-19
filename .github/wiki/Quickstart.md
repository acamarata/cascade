# Quickstart

Get Cascade working in about 5 minutes.

---

## Step 1: Install

Run the one-liner for your platform. See [Installation](Installation.md) for Homebrew, Scoop, AUR, and build-from-source options.

**macOS / Linux:**
```sh
curl -fsSL https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.sh | sh
```

**Windows:**
```powershell
irm https://raw.githubusercontent.com/acamarata/cascade/main/scripts/install.ps1 | iex
```

Both scripts install the `cascade` binary, register the background daemon, and run a basic setup. When they finish, confirm the binary is on your PATH:

```sh
cascade --version
```

---

## Step 2: Initialize a project

Navigate to the directory you want to set up, then run:

```sh
cd ~/my-project
cascade init --accept-defaults
```

`--accept-defaults` skips all prompts and uses sensible defaults. It creates a `.cascade/` directory with a `CASCADE.md` skeleton and the standard subdirectories.

To connect a cloud provider at the same time:

```sh
cascade init --accept-defaults --provider anthropic --api-key sk-ant-...
```

---

## Step 3: Add a rule

Open `.cascade/CASCADE.md` in any editor. The file is plain Markdown. Add one rule:

```markdown
# My project context

Always write tests before implementation.
```

Save the file. That rule is now part of your project's cascade at the PRC (Per-Repo Cascade) tier.

---

## Step 4: Generate harness files

Run:

```sh
cascade generate-instructions
```

This reads your `.cascade/CASCADE.md` and writes the files each tool expects:

- `.claude/CLAUDE.md` — picked up by Claude Code on next launch
- `.claude/AGENTS.md` — symlink for OpenCode compatibility

Open your project in Claude Code (or any connected tool). The rule you just wrote appears in its context.

---

## Step 5: Verify

```sh
cascade verify
```

`cascade verify` checks six requirements and exits 0 only when all pass:

- AI folder exists and is readable
- Cascade resolves to non-empty output
- Daemon is reachable
- AI provider is configured
- `config.toml` parses cleanly
- OS keychain is accessible

If any check fails, run `cascade doctor` for a detailed report with suggested fixes.

---

## What's next

- [Cascade Concepts](Cascade-Concepts.md) — understand the six-tier model and why it works this way
- [CLI Reference](CLI-Reference.md) — every command and flag
- [MCP Server](MCP-Server.md) — live context injection without static files
