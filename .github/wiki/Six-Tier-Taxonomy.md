# Six-Tier Taxonomy

Cascade organizes instruction files into six tiers. Each tier has a defined scope. When you work in a project, the tiers that apply to that project are merged from top to bottom, with lower tiers taking precedence on conflicts.

## The six tiers

| Tier | Name | Abbrev | Scope |
|---|---|---|---|
| 1 | Global Cascade Instructions | GCI | Your entire machine — every project, every tool |
| 2 | Portfolio Cascade Instructions | PCI | A portfolio, account, or namespace grouping multiple products |
| 3 | Account/Product Cascade | APC | A product family or GitHub organization |
| 4 | Per-Product Cascade | PPC | A single product (may span multiple repos) |
| 5 | Per-Repo Cascade | PRC | A single git repository |
| 6 | Per-App Cascade | PAC | An app or package within a monorepo |

Most users start with just GCI (global) and PRC (per-repo). The intermediate tiers are there for larger setups where the same rules apply across many repos.

## Cascade discovery

When Cascade resolves instructions for a working directory, it:

1. Reads `~/.cascade/CASCADE.md` (GCI).
2. Walks up from the working directory, checking for `CASCADE.md` at each directory level.
3. Assigns each file it finds to a tier based on its position relative to known project roots.
4. Merges the files from GCI down to PAC.

You do not need to use all six tiers. Tiers with no file are simply skipped.

## Conflict resolution

When the same instruction appears at two tiers, the lower tier wins. If your GCI says "prefer functional patterns" and a PRC says "use class-based patterns for this legacy codebase", the PRC wins in that repo.

Conflict resolution operates at the section level, not the file level. If a lower tier adds a new section, it is appended. If it restates a section that already exists at a higher tier, the lower tier's version replaces it.

The full resolution algorithm is documented in the [Architecture](Architecture.md) page under `cascade-core`.

## Worked example

Say you have this structure:

```
~/.cascade/CASCADE.md                 GCI — global rules
~/projects/.cascade/CASCADE.md        PCI — portfolio rules (optional)
~/projects/my-product/
    .cascade/CASCADE.md               APC — product-family rules (optional)
    api/
        .cascade/CASCADE.md           PPC — product rules (optional)
        CASCADE.md                    PRC — repo rules
        web/
            CASCADE.md                PAC — app rules (optional)
```

If you open `~/projects/my-product/api/web/` in your AI tool, Cascade merges:

1. `~/.cascade/CASCADE.md` (GCI)
2. `~/projects/.cascade/CASCADE.md` (PCI, if present)
3. `~/projects/my-product/.cascade/CASCADE.md` (APC, if present)
4. `~/projects/my-product/api/.cascade/CASCADE.md` (PPC, if present)
5. `~/projects/my-product/api/CASCADE.md` (PRC)
6. `~/projects/my-product/api/web/CASCADE.md` (PAC, if present)

The merged result is written to `CLAUDE.md` (or whatever derived file the tool expects) in that directory. The AI tool reads the merged result.

## Tier discovery diagram

```
machine scope
  └─ GCI (~/.cascade/CASCADE.md)
       │
       └─ portfolio scope (optional)
            └─ PCI (~/{portfolio}/.cascade/CASCADE.md)
                 │
                 └─ product family scope (optional)
                      └─ APC (~/{portfolio}/{family}/.cascade/CASCADE.md)
                           │
                           └─ product scope (optional)
                                └─ PPC (~/{portfolio}/{family}/{product}/.cascade/CASCADE.md)
                                     │
                                     └─ repo scope
                                          └─ PRC ({repo}/CASCADE.md)
                                               │
                                               └─ app scope (optional)
                                                    └─ PAC ({repo}/{app}/CASCADE.md)
```

## File naming

The canonical file name is `CASCADE.md` at every tier. Cascade also recognizes `cascade.md` (lowercase). At the GCI tier specifically, it looks for `~/.cascade/CASCADE.md`.

The derived files that Cascade writes — `CLAUDE.md`, `AGENTS.md`, `.cursorrules`, `.aider.md` — are outputs. You should not edit them directly; edit the source `CASCADE.md` files instead. Cascade will overwrite derived files when the source changes.

## Adding a new tier

To add a tier you do not currently use:

1. Create `CASCADE.md` at the appropriate location.
2. Run `cascade sync` or wait for the file watcher to detect the new file.
3. The resolver re-runs automatically and updates derived files.

No configuration is required. Cascade discovers the new tier from the file system.

## See also

- [Cascade Concept](Cascade-Concept.md) — motivation and design
- [Configuration Reference](Configuration-Reference.md) — controlling discovery behavior
- [Glossary](Glossary.md) — all tier names and related terms defined
