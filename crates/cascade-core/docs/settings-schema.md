# Cascade Settings Schema — v2

File: `~/.cascade/settings.json`
Rust type: `cascade_core::settings::types::CascadeSettings`
TS mirror: `apps/cascade-app/src/types/settings.ts`
Updated: T-P3-E07-14

---

## Schema Version History

| Version | Description |
|---------|-------------|
| `"1"` | Baseline: `providers` and `geminiPool` as opaque JSON blobs (T-P3-E07-00). |
| `"2"` | Full typed schema: all E-07 sections added (T-P3-E07-14). |

Unknown top-level fields are preserved in `extra` (serde flatten) so a v2 binary loading a future-version file does not silently drop unknown sections.

---

## Top-Level Fields

| Field (camelCase) | Rust type | Default | Description |
|---|---|---|---|
| `schemaVersion` | `String` | `"2"` | Schema version — increment on incompatible changes. |
| `library` | `LibrarySettings` | see below | Library item defaults. |
| `context` | `ContextSettings` | see below | Context resolution overrides. |
| `projectMap` | `ProjectMapSettings` | see below | Project-map overrides. |
| `providers` | `ProvidersSettings` | see below | Per-provider API keys. |
| `geminiPool` | `GeminiPoolSettings` | see below | Gemini proxy pool. |
| `harnessBridges` | `HarnessBridgesSettings` | see below | Harness config paths. |
| `hooks` | `Vec<HookDefinition>` | `[]` | Lifecycle hook definitions. |
| `scheduledTasks` | `Vec<ScheduledTaskEntry>` | `[]` | Scheduled task definitions. |
| `plugins` | `PluginsSettings` | see below | Plugin management. |
| `widgets` | `WidgetsSettings` | see below | Widget geometry. |
| `mcpServers` | `Vec<McpServerEntry>` | `[]` | MCP server definitions. |
| `vaultDisplay` | `VaultDisplaySettings` | see below | Vault UI preferences. |
| `telemetry` | `TelemetrySettings` | see below | Telemetry preferences. |

---

## `library` — LibrarySettings

| Field | Type | Default | Description |
|---|---|---|---|
| `defaultHarnessTargets` | `string[]` | `[]` | Harness slugs receiving new library items by default. Empty = all harnesses. |

---

## `context` — ContextSettings

| Field | Type | Default | Description |
|---|---|---|---|
| `sitesRoot` | `string \| null` | `null` | Override for `~/Sites/` used in context resolution. Null = system default. |

---

## `projectMap` — ProjectMapSettings

| Field | Type | Default | Description |
|---|---|---|---|
| `phaseRoot` | `string \| null` | `null` | Override for the phase root path. Null = system default. |

---

## `providers` — ProvidersSettings

All key fields are `Option<string>` — absent means not configured. **API keys are stored as plain strings in v2; vault encryption is deferred to P4.**

| Field | Type | Default | Description |
|---|---|---|---|
| `anthropic` | `string \| null` | `null` | Anthropic API key. |
| `openai` | `string \| null` | `null` | OpenAI API key. |
| `google` | `string[]` | `[]` | Google API keys (multiple accounts). |
| `codex` | `string \| null` | `null` | OpenAI Codex API key. |
| `defaultRouting` | `string \| null` | `null` | Default routing target harness slug, e.g. `"anthropic"`. |

---

## `geminiPool` — GeminiPoolSettings

| Field | Type | Default | Description |
|---|---|---|---|
| `keys` | `string[]` | `[]` | API keys in the rotation pool. |
| `proxyPort` | `u16` | `3761` | TCP port the Gemini proxy daemon listens on. |
| `enabled` | `bool` | `false` | Whether the Gemini proxy pool is active. |

---

## `harnessBridges` — HarnessBridgesSettings

| Field | Type | Default | Description |
|---|---|---|---|
| `ccConfigPath` | `string \| null` | `null` | Path to the Claude Code (`cc`) config file. |
| `ocConfigPath` | `string \| null` | `null` | Path to the OpenCode (`oc`) config file. |
| `codexConfigPath` | `string \| null` | `null` | Path to the OpenAI Codex config file. |

---

## `hooks[]` — HookDefinition

| Field | Type | Default | Description |
|---|---|---|---|
| `id` | `string` | required | Unique hook slug, e.g. `"on-session-start"`. |
| `event` | `string` | required | Lifecycle event name, e.g. `"session_start"`. |
| `command` | `string` | required | Shell command or script path to execute. |
| `enabled` | `bool` | `true` | Whether this hook fires. |

Hook execution is handled by the P2/E-02 engine; this section only stores configuration.

---

## `scheduledTasks[]` — ScheduledTaskEntry

| Field | Type | Default | Description |
|---|---|---|---|
| `id` | `string` | required | Unique task identifier. |
| `label` | `string` | required | Human-readable task label. |
| `cron` | `string` | required | Cron expression, e.g. `"0 2 * * *"`. |
| `command` | `string` | required | Command or agent prompt to execute. |
| `enabled` | `bool` | `true` | Whether this task is active. |

---

## `plugins` — PluginsSettings

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `string[]` | `[]` | Slugs of currently enabled plugins. |
| `config` | `Record<string, object>` | `{}` | Per-plugin config keyed by plugin slug. |

---

## `widgets` — WidgetsSettings

| Field | Type | Default | Description |
|---|---|---|---|
| `positions` | `Record<string, WidgetGeometry>` | `{}` | Per-widget geometry keyed by widget slug. |

### WidgetGeometry

| Field | Type | Default | Description |
|---|---|---|---|
| `x` | `i32` | `0` | Screen X in logical pixels. |
| `y` | `i32` | `0` | Screen Y in logical pixels. |
| `width` | `u32` | `320` | Widget width in logical pixels. |
| `height` | `u32` | `200` | Widget height in logical pixels. |

---

## `mcpServers[]` — McpServerEntry

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | `string` | required | Human-readable server name. |
| `command` | `string` | required | Executable (full path or in `$PATH`). |
| `args` | `string[]` | `[]` | Arguments passed to the command. |
| `env` | `Record<string, string>` | `{}` | Environment variables for the server process. |
| `enabled` | `bool` | `true` | Whether this server is active. |

---

## `vaultDisplay` — VaultDisplaySettings

| Field | Type | Default | Description |
|---|---|---|---|
| `showMasked` | `bool` | `false` | Show masked key previews (e.g. `sk-••••••AB`) in the UI. |

---

## `telemetry` — TelemetrySettings

| Field | Type | Default | Description |
|---|---|---|---|
| `enabled` | `bool` | `false` | Opt in to anonymous telemetry. |
| `endpoint` | `string \| null` | `null` | Telemetry endpoint URL override. Null = use default. |

---

## Migration Behaviour

On load, `serde_json` fills any missing field from its `Default` implementation because every field carries `#[serde(default)]`. A v1 file (missing all v2 sections) loads without error — all new sections are populated with empty/disabled defaults. The file is written back with the full schema on the next `update_settings` call.

## Forward Compatibility

Unknown top-level keys are captured in `CascadeSettings::extra` via `#[serde(flatten)]` and are re-serialised on save. This ensures a future v3 field is not lost when a v2 binary loads and re-saves the file.

## Security Note

API keys in `providers` and `geminiPool.keys` are stored as plain strings in schema v2. Vault encryption is deferred to P4. Do not commit `settings.json` to version control if it contains API keys.
