# Per-Repository Cascade (PRC)

**Tier:** PRC — applies to ONE git repository.
**Subordinate to:** GCI → PCI → APC → PPC. PRC adds repo-specific tech stack, conventions, and master list locations; it never re-states higher-tier rules.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## Cascade Position

```
GCI  (~/.cascade/CASCADE.md)
  └─ PCI  (<PROJECTS_ROOT>/.cascade/)
      └─ APC  (<PRODUCT_ROOT>/.cascade/)
          └─ PRC  (<REPO_ROOT>/.cascade/)   ← THIS FILE
              └─ <APP>/.cascade/             PAC tier (if multi-app)
```

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
