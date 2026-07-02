/**
 * Purpose: TypeScript mirror types for CascadeSettings — the full E-07 schema.
 * Inputs:  Derived from crates/cascade-core/src/settings/types.rs (schema v2).
 * Outputs: Exported interfaces consumed by settings IPC commands and the
 *          settings UI panels (T-P3-E07-15, T-P3-E07-16).
 * Constraints:
 *   - Must stay in sync with Rust types. Add fields by appending only.
 *   - Never rename or remove without a versioning ticket.
 *   - All optional fields mirror Rust Option<T>; absent = not configured.
 * SPORT: MASTER-LIBS.md — settings types, src/types/settings.ts (T-P3-E07-14)
 */

// ── Library ───────────────────────────────────────────────────────────────────

/** Controls default routing of new library items to AI harnesses. */
export interface LibrarySettings {
  /** Harness slugs that receive new items by default. Empty = all harnesses. */
  defaultHarnessTargets: string[]
}

// ── Context ───────────────────────────────────────────────────────────────────

/** Context resolution overrides. */
export interface ContextSettings {
  /** Override the default ~/Sites/ root. Absent = system default. */
  sitesRoot?: string | null
  /**
   * Override the default personal-workspace root used by Personal-scope chat
   * and the personal vault. Absent = system default (~/Downloads).
   */
  personalRoot?: string | null
}

// ── Memory ────────────────────────────────────────────────────────────────────

/** Chat-history memory consent settings. */
export interface MemorySettings {
  /**
   * Explicit user consent to persist Personal-scope chat history to the
   * daemon's long-term memory store (`personal` namespace). Default: false.
   * When false, Personal-scope chat is localStorage-only.
   */
  personalChatSync: boolean
}

// ── Project Map ───────────────────────────────────────────────────────────────

/** Project-map resolution overrides. */
export interface ProjectMapSettings {
  /** Override the phase root path. Absent = system default. */
  phaseRoot?: string | null
}

// ── Providers ─────────────────────────────────────────────────────────────────

/**
 * Per-provider API keys and routing defaults.
 * All key fields are optional — absent means not configured.
 * NOTE: Keys are plain strings for now; vault encryption is deferred to P4.
 */
export interface ProvidersSettings {
  /** Anthropic API key. */
  anthropic?: string | null
  /** OpenAI API key. */
  openai?: string | null
  /** Google API keys (multiple accounts). */
  google: string[]
  /** OpenAI Codex API key. */
  codex?: string | null
  /** Default routing target harness slug, e.g. "anthropic". */
  defaultRouting?: string | null
}

// ── Gemini Pool ───────────────────────────────────────────────────────────────

/** Gemini proxy pool configuration. */
export interface GeminiPoolSettings {
  /** API keys in the rotation pool. */
  keys: string[]
  /** TCP port the Gemini proxy daemon listens on. Default: 3761. */
  proxyPort: number
  /** Whether the Gemini proxy pool is enabled. */
  enabled: boolean
}

// ── Harness Bridges ───────────────────────────────────────────────────────────

/** Paths to external AI harness configuration files. */
export interface HarnessBridgesSettings {
  /** Path to the Claude Code (cc) config file. */
  ccConfigPath?: string | null
  /** Path to the OpenCode (oc) config file. */
  ocConfigPath?: string | null
  /** Path to the OpenAI Codex config file. */
  codexConfigPath?: string | null
}

// ── Hooks ─────────────────────────────────────────────────────────────────────

/**
 * A single lifecycle hook definition.
 * Hook execution is handled by the P2/E-02 engine.
 */
export interface HookDefinition {
  /** Unique hook slug, e.g. "on-session-start". */
  id: string
  /** Lifecycle event name that triggers this hook. */
  event: string
  /** Shell command or script path to execute. */
  command: string
  /** Whether this hook is active. Default: true. */
  enabled: boolean
}

// ── Scheduled Tasks ───────────────────────────────────────────────────────────

/** A single scheduled task entry. */
export interface ScheduledTaskEntry {
  /** Unique task identifier. */
  id: string
  /** Human-readable task label. */
  label: string
  /** Cron expression, e.g. "0 2 * * *". */
  cron: string
  /** Command or agent prompt to execute on schedule. */
  command: string
  /** Whether this task is active. Default: true. */
  enabled: boolean
}

// ── Plugins ───────────────────────────────────────────────────────────────────

/** Plugin management settings. */
export interface PluginsSettings {
  /** Slugs of currently enabled plugins. */
  enabled: string[]
  /** Per-plugin configuration keyed by plugin slug. */
  config: Record<string, unknown>
}

// ── Widgets ───────────────────────────────────────────────────────────────────

/** Display geometry for a single widget. */
export interface WidgetGeometry {
  /** Screen X position in logical pixels. */
  x: number
  /** Screen Y position in logical pixels. */
  y: number
  /** Widget width in logical pixels. Default: 320. */
  width: number
  /** Widget height in logical pixels. Default: 200. */
  height: number
}

/** Widget display settings keyed by widget slug. */
export interface WidgetsSettings {
  /** Per-widget geometry keyed by widget slug. */
  positions: Record<string, WidgetGeometry>
}

// ── MCP Servers ───────────────────────────────────────────────────────────────

/** A single MCP server definition. */
export interface McpServerEntry {
  /** Human-readable server name. */
  name: string
  /** Executable command (full path or in $PATH). */
  command: string
  /** Arguments passed to the command. */
  args: string[]
  /** Environment variables injected into the server process. */
  env: Record<string, string>
  /** Whether this server is active. Default: true. */
  enabled: boolean
}

// ── Vault Display ─────────────────────────────────────────────────────────────

/** Controls how vault credentials are shown in the UI. */
export interface VaultDisplaySettings {
  /** Show masked previews (e.g. sk-••••••AB). Default: false. */
  showMasked: boolean
}

// ── Telemetry ─────────────────────────────────────────────────────────────────

/** Anonymous usage telemetry preferences. */
export interface TelemetrySettings {
  /** Opt in to anonymous telemetry. Default: false. */
  enabled: boolean
  /** Telemetry endpoint URL override. Absent = use default. */
  endpoint?: string | null
}

// ── CascadeSettings (top-level) ───────────────────────────────────────────────

/**
 * Top-level application settings.
 *
 * Mirrors `CascadeSettings` in crates/cascade-core/src/settings/types.rs.
 * Schema version history:
 * - "1": baseline (providers + geminiPool as opaque blobs, T-P3-E07-00).
 * - "2": full typed schema, all E-07 sections (T-P3-E07-14).
 *
 * Partial objects are accepted by `update_settings` — only send the sections
 * you want to change.
 */
export interface CascadeSettings {
  /** Schema version string. */
  schemaVersion: string
  /** Library defaults. */
  library: LibrarySettings
  /** Context resolution overrides. */
  context: ContextSettings
  /** Chat-history memory consent settings. */
  memory: MemorySettings
  /** Project-map resolution overrides. */
  projectMap: ProjectMapSettings
  /** Per-provider API keys and routing defaults. */
  providers: ProvidersSettings
  /** Gemini proxy pool configuration. */
  geminiPool: GeminiPoolSettings
  /** Harness bridge configuration file paths. */
  harnessBridges: HarnessBridgesSettings
  /** Lifecycle hook definitions. */
  hooks: HookDefinition[]
  /** Scheduled task definitions. */
  scheduledTasks: ScheduledTaskEntry[]
  /** Plugin management settings. */
  plugins: PluginsSettings
  /** Widget display geometry per widget slug. */
  widgets: WidgetsSettings
  /** MCP server definitions. */
  mcpServers: McpServerEntry[]
  /** Vault display preferences. */
  vaultDisplay: VaultDisplaySettings
  /** Anonymous telemetry preferences. */
  telemetry: TelemetrySettings
}

/**
 * Partial settings update payload.
 *
 * Pass to `invoke('update_settings', { partial: … })`. Only the sections
 * included in the object are updated on disk.
 */
export type SettingsUpdate = Partial<CascadeSettings>
