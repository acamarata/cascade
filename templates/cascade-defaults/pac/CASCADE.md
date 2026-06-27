# Per-App Cascade (PAC)

**Tier:** PAC — applies to ONE app within a multi-app repository.
**Subordinate to:** GCI → PCI → APC → PPC → PRC. PAC adds app-specific conventions; it never re-states higher tier rules.
**Sibling files:** `AGENTS.md` and `CLAUDE.md` are symlinks to this file.

---

## Cascade Position

```
GCI  (~/.cascade/CASCADE.md)
  └─ PCI  (<PROJECTS_ROOT>/.cascade/)
      └─ APC  (<PRODUCT_ROOT>/.cascade/)
          └─ PRC  (<REPO_ROOT>/.cascade/)   ← parent
              └─ PAC  (<APP>/.cascade/)      ← THIS FILE
```

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
