# pkg/plugin/testdata — golden manifest fixture provenance

Per 12-QUALITY-CONSTITUTION.md Art.2 (real-counterpart verification: recorded
fixtures state their provenance) and this ticket's `docs_updates`, these
three fixtures are the golden manifests for `TestParseManifest_Goldens` and
the seed corpus for `FuzzParseManifest`.

Each fixture is a real `cascade.plugin/v2` manifest for the named entry in
02-TARGET-STRUCTURE.md's First-party plugin catalog v1 (owner-ratified
2026-08-31) — not a self-authored dialect. Field values (intents, tools,
domains, commands, requires, permissions) are spec-derived from that
catalog row plus the corresponding capability/epic named in
02-TARGET-STRUCTURE.md, since the catalog table itself does not enumerate a
plugin's full manifest body.

| Fixture | Catalog row | Runtime | Epic | Default |
|---|---|---|---|---|
| `example-connector.toml` | `examples: connector / agent-provider / domain` | wasm | O | off |
| `example-pbd.toml` | `cascade-pbd` | builtin | N | opt-in |
| `example-agent-provider.toml` | `examples: connector / agent-provider / domain` | wasm | O | off |

Provenance for all three:

- tool: spec-derived (hand-authored to satisfy the cascade.plugin/v2 schema
  defined in `pkg/plugin/manifest.go`; no automated generator)
- version: 02-TARGET-STRUCTURE.md §First-party plugin catalog v1
- date: 2026-08-31 (catalog ratification date cited by the ticket contract)

These fixtures double as `FuzzParseManifest`'s seed corpus, physically
copied to `internal/testdata/fuzz/manifest/` per 06-FORGE-SPEC.md §5.7 (fuzz
corpus must live under `internal/testdata/fuzz/`, never beside the package).
`pkg/plugin/fuzz_test.go` reads the copies from that directory at fuzz-setup
time; the copies here are also the fixtures the table-driven unit tests in
`manifest_test.go` load directly for the ParseManifest→Validate round-trip.
