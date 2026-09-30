# Migration documentation

This directory holds the committed, generated output of the v1-legacy-pointer
sweep (`internal/migration/sweep`; see
[`../migration-guide.md`](../migration-guide.md#legacy-pointer-sweep) for the
tool itself). All of the sweep's inputs are public: the term set
(`internal/migration/sweep/testdata/v1-terms.json`) and the provenance refs
(`internal/migration/sweep/testdata/provenance-refs.json`) are committed
alongside the code, and the sweep reads only the tracked file list it is
given — no private, non-public input of any kind.

- [`v1-pointer-inventory.md`](v1-pointer-inventory.md) — one classified row
  per real `(pointer, path)` hit found in the tracked v2 tree: pointer, path,
  disposition (DONE/OPEN/UNRESOLVED/PROVENANCE), replacement file(s), and
  whether it is safe to decommission (`yes` only for a DONE row whose anchor
  no non-migration production code references; PROVENANCE, including live v2
  markers, is always `no`). No line numbers or hit counts — those appear only
  in `--dry-run` output.
- `unresolved.json` — the machine-readable subset of the inventory holding
  exactly the UNRESOLVED `(pointer, path)` pairs of the latest run, each with
  a non-empty reason. Rewritten from scratch on every non-dry run; a resolved
  pointer disappears and no unresolved pointer is silently dropped.

Both files are generated, not hand-authored — do not edit them directly.
Regenerate both after adding or removing a v1-term mention, from the module
root:

```console
go run ./internal/migration/sweep --files <(git ls-files -z)
```

then commit the two files above. `TestInventoryMatchesRealTree`
(`internal/migration/sweep`, no build tags) fails the default CI lane if a
committed row no longer matches a real hit in the tracked tree, or a real hit
is missing from the committed rows — the drift gate that makes "regenerate
and commit" a hard requirement rather than a convention.

The sweep never deletes a v1 artifact: it only reports what still needs an
explicit, verified v2 replacement (a real file, containing a real anchor
literal) before the v1 archive reference can be retired.

## Excluded v1 term classes

The committed term file (`internal/migration/sweep/testdata/v1-terms.json`)
tracks only the v1 command names and config keys the sweep can classify
unambiguously. Some real v1 surface names are deliberately left out of the
term set: scanning for them as a literal string would false-hit on an
unrelated, live v2 identifier of the same name. Both v1 surfaces are
re-derived from v1's command enum (`cascade-cli/src/cmd/mod.rs`) and
config-key structs (`cascade-types`/`cascade-daemon` `config.rs`); the per-term
derivation was recorded privately at build time. This section is the committed,
auditable summary of the exclusions that derivation made.

### Commands: 21 of 59 excluded

| class | count | terms | reason |
|---|---|---|---|
| live v2 CLI verb collision (ratified class 1) | 14 | backup, config, context, daemon, doctor, init, mcp, memory, pbd, plugin, policy, provider, status, migrate | The v1 command name is also a live v2 top-level command with evolved semantics, so a literal scan would misclassify real v2 usage as a stale v1 pointer. Verified against `cmd/cascade/testdata/golden_help.txt` (13 of the 14, visible) and `cmd/cascade/migrate.go`'s `Hidden` treatment (`migrate`, the 14th). |
| pervasive identifier (ratified class 2) | 7 | accounts, completions, inbox, models, subs, uninstall, widget | A real-tree probe showed the name collides pervasively with a live v2 concept or identifier, but not as an exact top-level CLI verb. Ratified as its own class: pervasive identifiers stay out of the term set because a literal scan would flood the inventory with unrelated live v2 hits. |

### Config keys: 9 excluded under classes 2 and 3

Config-key exclusions also include an "also a live v2
config/schema key" class (`enabled`, `endpoint`, `mcp`, `plugins`,
`schema_version`) and a dictionary-word class (e.g. `daemon`, `budget`,
`provider`); those are listed in the term derivation. The 9 below fall under the three
ratified classes and are recorded here so the exclusion is auditable:

| class | count | terms | reason |
|---|---|---|---|
| config key with no struct tag (ratified class 3) | 5 | context_sync, middleware, min_score, quota_store, scheduler | A grep for a `json`/`toml`/`yaml` struct tag of this name found none anywhere in the tree, so no v2 config surface carries the key and a literal scan would only hit unrelated identifiers. |
| pervasive identifier (ratified class 2) | 4 | base_url, hooks, key_id, url | A `json`/`toml`/`yaml` struct tag of this exact name exists elsewhere in the tree, tagging an unrelated field (`key_id` collides with the live v2 policy `KeyID` signing-key-rotation concept), so the name is pervasive rather than a v1-only pointer. |

Neither table is a decommission list: an excluded term is never scanned
for and never appears as a DONE/OPEN/UNRESOLVED row in
`v1-pointer-inventory.md` — it is simply outside the term set, on the
stated basis. Re-deriving the term set is a separate, not-yet-scheduled
ticket; the exclusions are documented here so the decision is public and
auditable.

Two further documents are planned for this directory as later P1 tickets
land: a decommission protocol (the exact steps to retire a DONE pointer) and
a rollback plan. Neither exists yet; this README will link them once they do.
