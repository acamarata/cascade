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
