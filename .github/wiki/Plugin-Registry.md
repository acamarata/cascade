# Plugin Registry

Cascade ships a fixed set of builtin plugins (compiled in, always active) and
can optionally discover more from a signed registry index.

## Searching the catalog

```
cascade plugin search [<query>]
cascade plugin search --json
```

An empty query browses the whole catalog: every builtin plugin, plus every
registry entry when a verified registry is configured. A query filters by
substring match against name, description, and (for registry entries) tags.

Full command reference: [`docs/cli-reference/plugin.md`](../../docs/cli-reference/plugin.md).

Programmatically, the same catalog is reachable as the `cascade_plugin_search`
MCP tool.

## Configuring a registry

```toml
[registry]
url = "https://your-registry.example/index"
pubkey_path = "/path/to/registry-signing-key.pub"
```

The index is a static, signed JSON document — `pkg/plugin.RegistryIndex` —
verified with Ed25519 before any entry in it is trusted or cached. Signing key
management (generating an index, publishing it, rotating the key) is
publisher-side tooling, not a `cascade` subcommand; see
`internal/tools/registry-gen` for the reference generator.

**No production registry is configured by default.** Shipping one requires a
real, dedicated Ed25519 signing keypair — separate from the release-signing
key — which this project has not yet generated. Until it has, `cascade plugin
search` and the `cascade init` catalog page both answer from the builtin set
only, and `cascade doctor` names the gap (`registry_pubkey` check) whenever
`[registry].url` is set without a `[registry].pubkey_path`.

## `cascade init`'s plugin page

Step 4 of the setup wizard renders the same catalog `plugin search` does — a
verified registry entry appears there too, unchecked by default (a discovered,
opt-in offering), next to the always-present, always-checked builtins.

## Updating a plugin

```
cascade plugin update <name> [--yes] [--accept-grant=<grant> ...]
```

`cascade plugin update` checks the registry for a newer published version of
an installed plugin, downloads and fully verifies the candidate artifact —
checksum **and** Ed25519 signature — **before** anything else looks at it,
and — only if that succeeds — compares the candidate manifest's requested
capabilities against what is currently granted. Both this path and the
local-manifest `--from <path>` form below commit through the SAME
elevation-gated function (`internal/plugins.UpdatePlugin`); the registry
path is not a separate, lighter-weight commit path.

- **Already at the latest version**: `no update available`, exit 0 — a
  no-op, safe to run repeatedly (idempotent).
- **Pre-release versions**: a comparison where either the installed or the
  registry's published version carries a pre-release suffix (`-rc1`, ...)
  is refused, naming both versions — this build has no dependency capable
  of ordering pre-release identifiers correctly, so it refuses rather than
  guesses.
- **No new capabilities requested, and the runtime tier is unchanged**: the
  update proceeds without asking.
- **New capabilities requested, OR the runtime tier changes** (e.g.
  `builtin` to `process`, even with the same capability set): elevation is
  required. A grant diff is printed before anything is written; `--yes`
  accepts ordinary non-interactive defaults but never a grant expansion by
  itself — each new capability needs its own `--accept-grant=<grant>`, and
  even with it, an expanding or tier-changing update still refuses on a
  host with no daemon configured (the acknowledgement is required, but it
  is never the elevation authority). Omitting `--accept-grant` refuses with
  a non-zero exit naming exactly what is missing. `CASCADE_NO_INPUT=1`
  without `--yes` hard-errors at this point rather than silently choosing
  either way. A removed capability is shown too but never itself needs
  `--accept-grant`.
- **Checksum or signature mismatch**: the candidate is discarded, its
  staging file removed, and the previously installed version is left
  exactly as it was — nothing partial is ever left behind or installed.

A local-manifest candidate (bypassing the registry) is still available via
`--from <path>`; see [`docs/cli-reference/plugin.md`](../../docs/cli-reference/plugin.md)
for both forms in full.
