# Cascade Concepts

Two ideas underpin everything in Cascade: the six-tier instruction model and per-harness file generation. Once you understand these, the rest of the tool follows naturally.

---

## The six-tier model

AI coding tools are only as useful as the context they carry. You want some rules everywhere (your error-handling style, your preferred patterns), others only in a specific project, and others only in a particular app inside that project. Cascade models this as a hierarchy of six tiers.

Each tier is a `.cascade/` directory at a specific location on your filesystem. Every tier holds a `CASCADE.md` file — plain Markdown that you write. When a tool needs context, Cascade walks up from your working directory, finds all the tiers in scope, and merges them. Higher tiers have higher precedence; lower tiers add specificity without clobbering the rules above them.

```
GCI   ~/.cascade/CASCADE.md
 |      All tools, all projects, all machines.
 |      Your global preferences, conventions, non-negotiables.
 v
PCI   ~/Sites/.cascade/CASCADE.md   (example: a project-group directory)
 |      All projects within a named group or portfolio.
 v
APC   ~/Sites/my-product/.cascade/CASCADE.md   (example: a product family)
 |      A team-wide or product-wide layer shared across repos.
 v
PPC   ~/Sites/my-product/backend/.cascade/CASCADE.md
 |      A single multi-repo project.
 v
PRC   ~/Sites/my-product/backend/api/.cascade/CASCADE.md
 |      A single git repository.
 v
PAC   ~/Sites/my-product/backend/api/web/.cascade/CASCADE.md
        A single app within a multi-app repo.
        Most specific; wins on conflict.
```

**Conflict resolution:** lower tiers (PAC > PRC > PPC > ...) take precedence over higher ones. If GCI says one thing and PAC says another, PAC wins for that app. Tiers that are not present are simply skipped.

**Typical use:** most people maintain two or three tiers. GCI for machine-wide style, PRC for per-repo context, and occasionally PAC for an app with unusual requirements.

---

## Tier names and locations

| Tier | Acronym | Typical path | Scope |
|---|---|---|---|
| Global Cascade Instructions | GCI | `~/.cascade/` | All tools, all projects |
| Project-group Cascade Instructions | PCI | `~/Sites/.cascade/` | All projects in a named group |
| All-Projects Cascade | APC | `~/Sites/product/.cascade/` | A product family or team layer |
| Per-Project Cascade | PPC | `<project>/.cascade/` | One multi-repo project |
| Per-Repo Cascade | PRC | `<repo>/.cascade/` | One git repository |
| Per-App Cascade | PAC | `<repo>/<app>/.cascade/` | One app within a repo |

`cascade init` detects which tier to scaffold based on your current directory and what already exists. You can also pass `--tier <acronym>` (in lowercase: `gci`, `pci`, `apc`, `ppc`, `prc`, `pac`) to `cascade generate-instructions` to target a specific tier.

---

## Harness file generation

Writing `CASCADE.md` is the source step. The output step is generating the files each AI tool actually reads.

`cascade generate-instructions` reads your full tier hierarchy, merges the cascade content, and writes harness-native files:

**For Claude Code:**
- `{tier_path}/.claude/CLAUDE.md` — the instruction file Claude Code loads on startup
- `{tier_path}/.claude/AGENTS.md` — symlink to `CLAUDE.md` for OpenCode compatibility
- `{tier_path}/.claude/settings.json` — MCP server entry added (additive, does not remove existing keys)

**For OpenCode:**
- `~/.config/opencode/opencode.json` — MCP server entry added
- `{tier_path}/.cascade/opencode-instructions.md` — OpenCode-specific preamble

Generation is idempotent: files that already contain the Cascade header marker are not overwritten. Use `--dry-run` to preview changes before writing.

```sh
# Generate for both harnesses, all found tiers
cascade generate-instructions

# Preview only
cascade generate-instructions --dry-run

# Claude Code only, one tier
cascade generate-instructions --harness cc --tier prc
```

---

## The MCP tool: cascade.provide\_harness\_context

When an AI tool connects to Cascade's MCP server, it calls `cascade.provide_harness_context` to get its merged context for the current working directory. This is the runtime counterpart to file generation: instead of reading a static file, the tool calls the MCP tool and receives the resolved cascade content on demand.

The MCP server merges tiers bottom-up (PAC first, GCI last), applies conflict resolution, and returns the final context. The tool injects this into its prompt. No files are written during an MCP call.

This is why the two approaches coexist: file generation keeps static harness files up to date for tools that load config at startup; the MCP tool handles on-demand resolution for tools that support live context injection.

---

## One source, many outputs

The key mental shift is this: `CASCADE.md` is the source of truth. `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, and similar files are derived outputs. You do not edit derived files directly — you edit `CASCADE.md` and regenerate.

This means:
- You maintain one file per tier, not one file per tool per tier.
- All your tools see the same context for a given project.
- Adding a new tool means teaching Cascade where to write — not duplicating your instructions.

---

See also: [Home](Home.md) · [Quickstart](Quickstart.md) · [CLI Reference](CLI-Reference.md) · [Six-Tier-Taxonomy](Six-Tier-Taxonomy.md)
