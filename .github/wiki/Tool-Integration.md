# Tool Integration

Cascade works with all major AI coding tools by generating the config file each tool expects. You write your rules once in `CASCADE.md` files. Cascade derives the tool-specific file from your cascade hierarchy and keeps it in sync.

---

## How it works

When the daemon detects a change in any `.cascade/` directory, it re-resolves the cascade for each scope and regenerates the derived files for any tools you have enabled.

Enable or disable tool output:

```bash
cascade config set tools.claude_code true
cascade config set tools.cursor false
```

Or run generation manually:

```bash
cascade generate-instructions
cascade generate-instructions --tool cursor
```

---

## Claude Code

Claude Code reads `CLAUDE.md` from the current directory and walks up to parent directories. Cascade writes `CLAUDE.md` at each scope level.

**Auto-setup:**

```bash
cascade link --tool claude-code
```

**What gets generated:** The merged cascade for the current scope is written as `CLAUDE.md`. Tier boundaries are indicated with comments so you can see where each rule came from.

**MCP integration:** Cascade also exposes an MCP server that Claude Code can connect to for semantic search. See [MCP Server](MCP-Server.md) for setup.

---

## OpenCode

OpenCode reads `AGENTS.md` for per-project instructions. It also supports MCP for context.

**Auto-setup:**

```bash
cascade setup-oc
```

This does three things:
1. Writes the MCP server config into OpenCode's project settings
2. Generates `.cascade/AGENTS.md` for the current repo
3. Sets up context injection for `cascade dispatch --harness oc`

**Manual link:**

```bash
cascade link --tool opencode
```

---

## Cursor

Cursor reads `.cursorrules` from the project root.

**Enable:**

```bash
cascade config set tools.cursor true
cascade link --tool cursor
```

Cascade writes `.cursorrules` as a symlink to `CASCADE.md` for the current scope. Cursor sees a plain text file that contains your merged rules.

---

## Aider

Aider reads `.aider.conf.md` from the project root.

**Enable:**

```bash
cascade config set tools.aider true
cascade link --tool aider
```

---

## Windsurf

Windsurf reads `.windsurfrules` from the project root.

**Enable:**

```bash
cascade config set tools.windsurf true
cascade link --tool windsurf
```

---

## Codex (OpenAI)

Codex CLI reads `AGENTS.md` from the project root.

**Enable:**

```bash
cascade config set tools.codex true
cascade link --tool codex
```

---

## Antigravity

Antigravity reads a JSON config at `.antigravity/config.json`.

**Enable:**

```bash
cascade config set tools.antigravity true
cascade link --tool antigravity
```

---

## Tool detection

Run `cascade status` to see which tools are detected on your system and which are currently receiving generated files:

```bash
cascade status
# output includes:
# tool integrations:
#   claude-code: CLAUDE.md generated, linked
#   cursor:      .cursorrules linked
#   aider:       not linked
```

---

## Disabling a tool integration

```bash
cascade unlink --tool cursor
cascade config set tools.cursor false
```

The `unlink` command removes the symlink. The `config set` prevents the file from being regenerated on the next sync.

---

## What the generated files contain

Generated files contain the full merged cascade for that scope. They are not symlinks to raw tier files; they contain the resolved, merged output. This means the tool sees the final result, not intermediate tier files.

Each section is tagged with its source tier in a comment line:

```markdown
<!-- cascade:tier:gci -->
# Global rules

Your global rules here.

<!-- cascade:tier:prc -->
# Project rules

Project-specific rules here.
```

Tools that do not support comments (like `.cursorrules`) receive the merged text without tier markers.

---

See also: [CLI Reference](CLI-Reference.md) · [MCP Server](MCP-Server.md) · [Configuration](Configuration.md)
