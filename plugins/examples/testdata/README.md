# plugins/examples/testdata — fixture provenance

Per Art.2.2 (12-QUALITY-CONSTITUTION.md), this file states the real source,
version, and date of every fixture in this directory. Nothing here is
fabricated: every path and every claim below is checked against the actual
file it describes.

## health-hub-manifest-map.md

- **Source**: `.claude/planning/p1/refs/chatgpt-2026-08-30-cascade-health-hub.md`
  (a ChatGPT research thread, dated 2026-08-30, on this project's own
  `.claude/planning/p1/refs/` path). This is a gitignored planning artifact,
  not a tracked repo file — this map's own tracked content is the durable
  artifact carrying forward what that source required.
- **What it maps**: every numbered requirement from that source document's
  "CPA Health Hub Architecture Draft" section (its own §§1-30, plus its 12
  "Absolute Architecture Rules") against the `cascade.plugin/v2` manifest
  schema (`pkg/plugin/manifest.go`) this ticket's three example plugins are
  written against.
- **Why it exists despite the feature being dropped**: owner ruling
  `DEF-PROD-health-hub` (`.claude/planning/p1/phase/deferrals.yaml`,
  2026-09-05) dropped the health hub as a product feature but explicitly
  kept this ticket's fixture as an ABI torture test, not a health feature.
  The map document is that torture test's written proof: it shows the
  schema is expressive enough to describe the deepest, widest requirement
  set available in this project's own planning corpus, without any health
  domain, health entity, or health-specific code existing anywhere in this
  directory.
- **Version**: this map was written against `pkg/plugin`'s manifest v2
  schema as of this ticket (P1-E15-W4-S33-T2); `SchemaVersion` is
  `cascade.plugin/v2` (`pkg/plugin/manifest.go`).

## The three example plugin manifests

`../example-domain/manifest.toml`, `../example-connector/manifest.toml`, and
`../example-agent-provider/manifest.toml` are original fixtures authored for
this ticket, not harvested or regenerated from any other source. Each was
verified in this ticket's own work by round-tripping it through
`pkg/plugin.ParseManifest` and `pkg/plugin.Validate` (see
`../integration_test.go`, `TestExamplePlugins_ManifestValidates`) — the real
C/S-05.T6 validator, never a self-authored dialect check. Their WASM
compilability (`GOOS=wasip1 GOARCH=wasm go build`) and, for
`example-agent-provider`, a real `plugin_invoke` round trip through the
`github.com/tetratelabs/wazero` runtime library, are likewise verified in
`../integration_test.go` and were run against this ticket's own build (Go
1.26, `github.com/tetratelabs/wazero v1.12.0`, per `go.mod`) — not assumed
or copied from a prior run.
