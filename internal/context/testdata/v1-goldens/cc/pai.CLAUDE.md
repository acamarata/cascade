<!-- cascade:generate-instructions digest=sha256:<DIGEST> -->
## Cascade Context — PAI Tier (Per-App Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# Per-App Cascade (PAC)

**Tier:** PAC — applies to ONE app within a multi-app repository.
**Subordinate to:** GCI → PCI → APC → PPC → PRC. PAC adds app-specific conventions; it never re-states higher tier rules.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## App Identity

**App name:** `<APP_NAME>`
**App type:** (web / mobile / desktop / server / CLI / library)
**Source root:** `<APP_DIR>/`
**Build command:** `<BUILD_CMD>`

Replace these slots when initializing an app.

---

## App-Specific Rules

(Add rules and conventions specific to this app below. Higher tiers are inherited.)

<!-- /cascade:generate-instructions -->
