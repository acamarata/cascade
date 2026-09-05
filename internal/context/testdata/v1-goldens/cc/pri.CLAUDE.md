<!-- cascade:generate-instructions digest=sha256:<DIGEST> -->
## Cascade Context — PRI Tier (Per-Repo Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# Per-Repository Cascade (PRC)

**Tier:** PRC — applies to ONE git repository.
**Subordinate to:** GCI → PCI → APC → PPC. PRC adds repo-specific tech stack, conventions, and master list locations; it never re-states higher-tier rules.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## Repository Identity

**Repo name:** `<REPO_NAME>`
**GitHub:** `<GITHUB_ORG>/<REPO_SLUG>`
**Language:** `<PRIMARY_LANGUAGE>`
**Framework:** `<PRIMARY_FRAMEWORK>`
**Package manager:** `<PKG_MANAGER>`

Replace these slots when initializing a repository.

---

## Repository Rules

(Add repo-specific rules, naming conventions, and patterns below. Higher tiers are inherited.)

## Master List

(Link to master entity lists tracked in `.cascade/phases/masters/` or `.cascade/docs/`.)

<!-- /cascade:generate-instructions -->
