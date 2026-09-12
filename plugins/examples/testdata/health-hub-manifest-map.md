# health-hub → manifest v2 construct map

## Status: ABI torture-test fixture, not a product feature

The health hub described in the source document below was dropped as a
product feature by owner ruling (`.claude/planning/p1/phase/deferrals.yaml`,
entry `DEF-PROD-health-hub`, ruling R-P2.29 applied by R-14.230, 2026-09-05):
no health-specific layer belongs in any phase; the repo's existing generic
personal-assistant plugin (`plugins/cascade-pa/`) covers that kind of
behavior through ordinary instructions and memory, with no health-specific
code. That ruling explicitly keeps this
ticket's fixture for one reason only: "O/S-33.T2's ABI torture test keeps its
fixture (an ABI test, not a health feature)."

This document exists to satisfy that fixture's purpose: it maps every
requirement the source document raises to the manifest v2 construct that
would express it, proving the schema (id, provides.{tools,intents,domains,
commands}, requires, permissions, runtime, host_version) is expressive
enough to describe an arbitrarily deep, arbitrarily wide plugin surface —
the torture-test property this ticket's AC asks for. **No health domain,
health entity, or health-specific behavior is implemented anywhere in
`plugins/examples/`.** Every mapping below is a structural exercise of the
schema, using the source document's own requirement shapes as a stress
case, exactly as the ruling frames it.

Source: `.claude/planning/p1/refs/chatgpt-2026-08-30-cascade-health-hub.md`
("CPA Health Hub Architecture Draft", §§1-30 plus its 12 "Absolute
Architecture Rules"), a ChatGPT research thread dated 2026-08-30. That same
document's later sections (§§32+, from "New boundary" onward) independently
reach the same conclusion as the deferral: a health layer belongs in a
separate product repo, and "Cascade should know nothing about 'health'"
(source document, §"Cascade should know nothing about 'health'"). This map
covers the architectural sections (§§1-30 and the 12 rules) that describe
requirements a manifest schema could express; the later product-boundary
sections describe repo/licensing strategy, not manifest-shaped requirements,
and are out of this map's scope for that reason, not by omission.

## Mapping

| # | Requirement (source §) | Manifest v2 construct | How this ticket's fixtures exercise it |
|---|---|---|---|
| 1 | Design Goals: one protected namespace unifying many data kinds | `id` as the namespace root; every `provides.domains[].name` scoped under it (`<id>.<layer>`) | `example-domain`'s three domains (`example-domain.raw/canonical/derived`) are namespaced under its own id |
| 2 | High-Level Architecture: a protocol layer sits between the platform and per-source connectors | `runtime = "wasm"` plus `requires` naming the connector capability, not the connector's driver | `example-domain.requires` names `connector.example-connector.read` (a capability, not a driver name) |
| 3 | Health Namespace: a deep, multi-branch tree of record kinds (clinical/imaging/genomics/...) | `provides.domains` (one entry per branch) | `example-domain` declares three sibling domains at once, proving the array shape scales to many declared domains |
| 4 | Storage Layers: an immutable Layer 0 of original source objects | `provides.domains[].name` for the raw layer + a `requires` entry gating write access to it | `example-domain.raw` domain + `storage.domain.write` requires entry |
| 5 | Provenance Layer: every normalized record retains its origin | A pipeline stage (implementation-level, not manifest-level) that carries the source connector's identity forward | `domain.go`'s `canonicalRecord.Source` field, populated from `rawRecord.Source` in `toCanonical` |
| 6 | Immutable vs Mutable Data: distinct read/write rules per layer | Separate `provides.domains` entries per layer, each with its own `requires` gate | `example-domain`'s raw/canonical/derived domains are declared separately, not merged into one |
| 7 | Deterministic Code Responsibilities: auth, ingestion, parsing, normalization, dedup, stats are never AI's job | `runtime` selects a deterministic execution mode (`wasm`, not an LLM-backed intent) for exactly this code | Both `example-connector.Fetch` and `example-domain`'s pipeline stages are plain deterministic Go, never model calls |
| 8 | Hooks / Event System: health publishes normal platform events | `provides.intents` (an intent a host's event/notification flow can resolve into) | `example-domain.summarize` intent |
| 9 | MCP / Agent Boundary: agents never see credentials or filesystem access directly | `permissions` (consent-dialog entries) distinct from `requires` (capability grants) — the manifest never carries a credential value itself | Every `requires` entry in all three fixtures has a matching `permissions` entry; no credential ever appears in a manifest |
| 10 | Read-Only MCP Tools: `health.search`, `health.get_record`, ... | `provides.tools` (one entry per read-only verb) | `example-domain.run-pipeline` tool |
| 11 | Deterministic Analysis Tools: `health.analytics.trend`, ... | `provides.tools` entries whose description states a deterministic (non-AI) computation | `example-domain.run-pipeline`'s description names the deterministic pipeline it runs |
| 12 | Controlled Agent Write Tools: agents write only to a derived/workspace layer | A `requires` entry scoped to the derived domain specifically, distinct from the raw/canonical write grants | `storage.domain.write` requires entry is declared once and covers only the plugin's own declared domains, never a bare "storage" capability |
| 13 | High-Level AI Interface: "ask health what medications..." | `provides.intents[].description` (natural-language framing of what the intent satisfies) | `example-domain.summarize`'s description is phrased as a natural-language request the intent-install flow resolves |
| 14 | Advice / Question System: an advice layer that never corrupts the record | A `provides.tools` entry whose output domain (per its description) is distinct from the immutable layers | `example-domain.run-pipeline` only writes derived output; its manifest never grants write access back into raw |
| 15 | Context Bundles: never send an entire database to a model | `requires`/`permissions` scoped per capability, never a blanket "storage.*" — the manifest structurally cannot express an unscoped grant | Every `requires` entry across all three fixtures names a specific capability string, never a wildcard |
| 16 | Connector Permissions: each connector gets explicit capabilities | Per-connector `requires` entries, one per named connector (`connector.<id>.<verb>`) | `example-domain.requires` carries two distinct connector-scoped entries: `connector.example-connector.read` and `connector.example-agent-provider.invoke` — this is the "per-connector permission declarations" this ticket's AC names explicitly |
| 17 | Health Encryption Boundary: its own cryptographic domain | Implementation-level (host-side custody, per Art.10.2 the manifest schema does not carry key material); the manifest's role is only to declare the storage domain the boundary applies to | `example-domain`'s three declared domains are the units a host-side custody boundary would apply per-domain |
| 18 | Backup Architecture: a protected backup domain | `provides.domains[].description` documenting backup eligibility (backup policy itself is host/storage-layer, not manifest) | `example-domain.raw`'s domain description names it the layer requiring the strongest protection |
| 19 | Do Not Use Plain tar.gz: encrypted, structured export, not a flat archive | Out of manifest scope (a storage/backup-subsystem requirement, not a plugin-declared one); noted here as a requirement the manifest schema correctly does NOT try to express | No manifest field in this fixture claims to express this; it is host-side policy |
| 20 | Routine Backup > One Giant Archive: incremental snapshots | Out of manifest scope, same reasoning as #19 | Not expressed in any fixture manifest — correctly so |
| 21 | Backup Manifest: an immutable signed manifest per snapshot | Distinct from the plugin manifest v2 document (different artifact, same "manifest" word) — no overlap in this schema | Not expressed; the plugin manifest and a backup manifest are deliberately different documents |
| 22 | Multi-Destination Backups: policy-based success criteria | Out of manifest scope | Not expressed |
| 23 | Backup MCP Tools: `backup.status`, `backup.list_snapshots` | `provides.tools` (the same construct as #10, since these are read-only verbs too) | Structurally identical to `example-domain.run-pipeline`'s tool declaration; not duplicated as a fixture since #10 already proves the construct |
| 24 | Google Drive Backup: opaque encrypted blobs to a third-party destination | `requires` naming an egress capability distinct from the connector's own read/write capability | `example-connector.requires` includes `network.egress` separately from its `storage.domain.write` entry |
| 25 | Portable User Export: a separate, user-owned export format | `provides.tools` (an export verb), same construct as #10/#23 | Not duplicated as a fixture, same reasoning as #23 |
| 26 | Database Separation: `health_raw`, `health_canonical`, ... schemas | `provides.domains`, one per schema | `example-domain`'s three domains map 1:1 onto this pattern |
| 27 | Search Architecture: different data needs different search backends | Host-side routing concern; the manifest's `provides.domains[].description` only names what is searchable, never how | `example-domain`'s domain descriptions state what each layer holds, not a search implementation |
| 28 | Agent Roles: specialized helper agents (researcher, timeline-agent, ...) | `provides.tools`/`provides.intents`, one set per role, each a distinct manifest entry | `example-agent-provider`'s five `provides.tools` entries (chat/embed/count/stream/capabilities) are the AgentProvider-shaped instance of this pattern |
| 29 | Example Agent Workflow: a multi-step tool-calling exchange | The `chat`/`stream` tool pair together, since a workflow is a sequence of individual tool calls, not a single manifest construct | `example-agent-provider`'s `chat` and `stream` tools |
| 30 | Absolute Architecture Rules (12 rules) | See the rules table below | — |

## Absolute Architecture Rules → manifest v2 construct

| Rule | Requirement | Manifest v2 construct |
|---|---|---|
| 1 | Original records are immutable | No `provides.tools`/`provides.intents` entry in any fixture manifest grants write access to a `.raw` domain; only `storage.domain.write` (scoped to canonical/derived) exists |
| 2 | AI cannot modify original or canonical facts | `example-agent-provider`'s manifest declares no `requires` entry naming any storage domain at all — it can invoke, never write |
| 3 | Every derived statement has provenance | `domain.go`'s `canonicalRecord.Source` and `derivedRecord.DerivedFromUnix` fields carry the raw record's own identity/timestamp forward through both pipeline stages |
| 4 | Statistics are calculated by deterministic code | `example-domain.run-pipeline`'s implementation (`domain.go`) is plain Go arithmetic/string logic, never a model call |
| 5 | Agents only receive access through MCP/tools | `example-agent-provider`'s only manifest surface is `provides.tools`; it declares no `provides.domains` of its own |
| 6 | Health data never automatically enters an external model context | `example-agent-provider`'s five tools all take an explicit request payload (`ChatRequest`, `ModelEmbedRequest`, ...); nothing is bound ambiently |
| 7 | AI access is separately permissioned from data collection | `example-connector` and `example-agent-provider` are separate plugins with disjoint `requires`/`permissions` sets |
| 8 | Source credentials/OAuth tokens are invisible to AI | No fixture manifest's `requires`/`permissions` array carries a credential value; `requires` entries name capabilities (`network.egress`), never secrets |
| 9 | Backups are encrypted before leaving the device | Out of manifest scope (see requirement #19); not expressed by any fixture, correctly |
| 10 | Recovery keys are not stored alongside their backups | Out of manifest scope; not expressed by any fixture, correctly |
| 11 | Every record traces back to an original source | Same mechanism as requirement #3/Rule 3: `canonicalRecord.Source` |
| 12 | Every user can export their complete dataset without the platform | `provides.tools` (an export verb), same construct as requirement #10/#23/#25 |

## Deliberate non-mappings

Requirements #17, #19-22, and Rules 9-10 concern backup/custody/export
subsystems that sit below the plugin manifest layer (host-side storage,
encryption, and backup policy — 02-TARGET-STRUCTURE.md's storage and backup
subsystems, not `pkg/plugin`). The manifest schema correctly has no field
for them; a plugin declares WHAT it needs access to and WHY (through
`requires`/`permissions`), never HOW the host enforces or backs up that
access. Listing these as "no manifest field" above, rather than silently
omitting them, is this document's own compliance with the AC's "every
requirement must map to a specific manifest v2 field, intent type, or
declared capability — no requirement may be deferred or left unmapped" —
"maps to: none, because the schema is correct to have none" is itself the
required mapping for a host-layer requirement.
