<!-- cascade:generate-instructions digest=sha256:<DIGEST> -->
## Cascade Context — ASI Tier (All-Sites Instructions)

**MCP server:** `stdio: cascade mcp stdio`

Call `cascade.search` before responding to queries about this project.
Call `cascade.context_slice` to retrieve relevant context from the RAG index.
If the cascade MCP tools are unavailable, run `cascade recall` and `cascade context slice` through Bash instead.

# All Projects Cascade (APC)

**Tier:** APC — applies to ONE multi-repo product (all repos within `<PRODUCT_ROOT>/`).
**Subordinate to:** GCI → PCI. APC adds product identity and shared infrastructure; it never re-states GCI or PCI rules.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## Cascade Position

```
GCI  (~/.cascade/CASCADE.md)
  └─ PCI  (<PROJECTS_ROOT>/.cascade/)
      └─ APC  (<PRODUCT_ROOT>/.cascade/)   ← THIS FILE
          ├─ <REPO_1>/.cascade/             PRC tier
          ├─ <REPO_2>/.cascade/             PRC tier
          └─ <REPO_3>/.cascade/             PRC tier
```

---

## Product Identity

**Product name:** `<PRODUCT_NAME>`
**GitHub org:** `<GITHUB_ORG>`
**Primary domain:** `<PRODUCT_DOMAIN>`
**Infrastructure project:** `<INFRA_PROJECT_NAME>`

Replace these slots when initializing a product.

---

## Repository Inventory

| Repo | Path | Purpose | Status |
|---|---|---|---|
| `<REPO_1>` | `<PRODUCT_ROOT>/<REPO_1>/` | <!-- e.g. CLI tool --> | ✅ Active |
| `<REPO_2>` | `<PRODUCT_ROOT>/<REPO_2>/` | <!-- e.g. Web frontend --> | ✅ Active |

Add one row per repository in this product. Update status when a repo is deprecated or archived.

---

## Backend Adapter

This product uses a single backend adapter instance. All repositories connect to it through the adapter abstraction layer.

**Adapter config:** `<PRODUCT_ROOT>/backend/`
**Environment variables:** sourced from `<VAULT_PATH>` at the start of any shell command

No repo in this product connects directly to storage. All data access goes through the adapter layer.

---

## Shared Infrastructure

| Resource | Identifier | Notes |
|---|---|---|
| Hosting provider | `<HOSTING_PROVIDER>` | Region: `<REGION>` |
| Deploy platform | `<DEPLOY_PLATFORM>` | Target: `<DEPLOY_TARGET>` |
| Database | via backend adapter | Never direct access |
| Email (transactional) | `<EMAIL_PROVIDER>` | API key: `<VAULT_PATH>` |
| Error tracking | `<ERROR_TRACKER>` | Per-app DSN |

---

## SPORT Paths

All SPORT files for this product live at:

```
<PRODUCT_ROOT>/.cascade/phases/sport/
  MASTER-FEATURES.md
  MASTER-COMPONENTS.md
  MASTER-ROUTES.md
  MASTER-ENDPOINTS.md
  MASTER-ENV.md
  ...
```

Read these before starting any cross-repo work. Update after every task that adds or changes a tracked entity.

---

## Phase State

Active phase state for this product:

```
<PRODUCT_ROOT>/.cascade/phases/
  registry.yaml          — all phases
  status.yaml            — top-level mirror (zero-LLM reads)
  current/p<N>/          — active phase YAML tree
  archive/               — completed phases
```

Both harnesses share this directory. Write to it using `cascade-write` scripts.

---

## Inbox

Cross-repo messages for this product land in `<PRODUCT_ROOT>/.cascade/inbox/`. Use `cascade-send` to write messages.

Sub-repo messages go to the sub-repo's PRC inbox. Product-level messages go here.

---

## Cross-Repo Contracts

Any task that modifies a shared API, schema, or type must:

1. List all affected repos in the ticket's `references` field.
2. Create integration tasks for each affected consumer repo.
3. Update the relevant SPORT master list entry before marking the source task done.

Never mark a cross-repo contract change done until all consumer integration tasks are in a valid state (done, explicitly deferred, or tracked as a follow-on ticket).

---

## Credentials

All credentials live in `<VAULT_PATH>`. Source it at the start of any shell command that calls an external service. Never hardcode credentials. Never ask the user for a credential that is in the vault.

<!-- /cascade:generate-instructions -->
