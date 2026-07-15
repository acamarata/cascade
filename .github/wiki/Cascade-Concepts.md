# Cascade Concepts

Two ideas underpin Cascade: a six-tier instruction hierarchy and harness-specific files generated from that hierarchy.

---

## The six-tier model

Each tier is represented by a `.cascade/` directory containing a plain-Markdown `CASCADE.md`. Broad rules live near the top of the hierarchy; project-, repository-, and app-specific context lives farther down.

```text
GCI   ~/.cascade/CASCADE.md
 |      Global preferences for every project.
 v
PCI   ~/Downloads/.cascade/CASCADE.md
 |      Personal instructions and private working context.
 v
APC   ~/Sites/.cascade/CASCADE.md
 |      Instructions shared by all projects under the configured projects root.
 v
PPC   ~/Sites/my-product/.cascade/CASCADE.md
 |      One project or multi-repo product.
 v
PRC   ~/Sites/my-product/api/.cascade/CASCADE.md
 |      One Git repository.
 v
PAC   ~/Sites/my-product/api/apps/web/.cascade/CASCADE.md
        One app within a repository.
```

These are conventional paths. `personal_dir` and `projects_dirs` configuration can move PCI and APC, and project-scoped tiers follow the directory tree for the working directory being resolved.

### What “merge” means today

`cascade resolve` reads every applicable `CASCADE.md` and concatenates the found content in tier order, with GCI first and increasingly specific tiers after it. Missing tiers are skipped. `--dedup` can remove identical lines.

The resolver does not parse Markdown headings and automatically replace a conflicting section. If two tiers contain contradictory prose, both statements remain in the merged output, so keep rules unambiguous and avoid duplicating them across tiers. Structured configuration has field-specific rules: for example, a more-specific tier wins for duplicate `[vars]` keys and `mcp_server_url`.

Most installations need only GCI plus PRC, with PAC added when an app genuinely needs different context.

---

## Tier names and scopes

| Tier | Acronym | Typical path | Scope |
|---|---|---|---|
| Global Cascade Instructions | GCI | `~/.cascade/` | All tools and projects |
| Personal Cascade Instructions | PCI | `~/Downloads/.cascade/` | Personal/private context |
| All-Projects Cascade | APC | `~/Sites/.cascade/` | All projects in the configured projects root |
| Per-Project Cascade | PPC | `<project>/.cascade/` | One project or multi-repo product |
| Per-Repo Cascade | PRC | `<repo>/.cascade/` | One Git repository |
| Per-App Cascade | PAC | `<repo>/<app>/.cascade/` | One app within a repository |

`cascade init [PATH]` labels the new setup using path heuristics; it does not accept a tier argument. To limit instruction generation to a known tier, use `cascade generate-instructions --tier <gci|pci|apc|ppc|prc|pac|all>`.

---

## Harness file generation

`CASCADE.md` is the editable source. `cascade generate-instructions` resolves the hierarchy, then creates or updates the files used by the selected harness for each found tier.

For Claude Code (`--harness cc`):

- `{tier_root}/.claude/CLAUDE.md` receives a Cascade-marked instruction block.
- `{tier_root}/.claude/AGENTS.md` is a relative symlink to `CLAUDE.md` on Unix; Windows gets a small fallback file.
- `{tier_root}/.claude/settings.json` receives an additive `cascade mcp stdio` server entry.

For OpenCode (`--harness oc`):

- `{tier_root}/.cascade/opencode-instructions.md` receives the tier instructions.
- `{tier_root}/opencode.json` points its `instructions` field at that file.
- `~/.config/opencode/opencode.json` receives an additive Cascade MCP server entry.

The default harness is `both`. Use `--project <PATH>` to resolve from another working directory and `--dry-run` to preview the diff.

Instruction files use Cascade header markers. The current command skips an instruction file once that marker exists; it does not append a second block or refresh the existing block. JSON configuration is upserted while preserving unrelated keys. If you intentionally need to rematerialize a generated instruction file, remove that generated target before rerunning and preview with `--dry-run`.

```sh
cascade generate-instructions
cascade generate-instructions --dry-run
cascade generate-instructions --harness cc --tier prc
```

Other tool-specific compatibility files are managed separately with `cascade link --tool <claude|opencode|cursor|aider|codex|continue>`.

---

## Live context through MCP

An MCP-compatible harness can call `cascade.provide_harness_context` with its harness name and absolute working directory. The tool resolves the same six-tier hierarchy and returns the merged instructions together with harness configuration, policy metadata, MCP connection details, and active-work context.

The returned instruction text uses the same broad-to-specific order as the resolver: GCI first, then each applicable tier through PAC. An MCP call does not write harness files.

Static generation and live MCP context therefore serve different startup models: generated files support tools that load instructions from disk, while MCP provides current context on demand.

---

## One source, multiple consumers

Edit the tier's `CASCADE.md` as the durable source. Live MCP resolution reads the current hierarchy. For static output, generate after editing; because the current command skips an already-marked instruction file, explicitly remove a generated target before rerunning only when you intend to rematerialize it.

This keeps the durable instruction source in one place while allowing Claude Code, OpenCode, and MCP clients to consume the format they expect.

---

See also: [Home](Home.md) · [Quickstart](Quickstart.md) · [CLI Reference](CLI-Reference.md) · [Six-Tier Taxonomy](Six-Tier-Taxonomy.md)
