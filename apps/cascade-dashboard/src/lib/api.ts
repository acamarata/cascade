/**
 * Purpose: Typed fetch client for the cascade daemon API endpoints.
 * Inputs: path string (e.g. '/api/gci/rules').
 * Outputs: Promise<T> — parsed JSON or thrown ApiError.
 * Constraints: Same-origin in prod; Vite proxy /api→:9761 in dev. No auth header needed.
 * SPORT: T-P3-E02-09 api client
 */

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

export async function apiGet<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) {
    throw new ApiError(res.status, `${res.status} ${res.statusText} — ${path}`)
  }
  return res.json() as Promise<T>
}

// ── Response shape interfaces ──────────────────────────────────────────────

export interface FileEntry {
  name: string
  path: string
  modified_at: string
}

export interface FileListResponse {
  items: FileEntry[]
  total: number
}

export interface SettingsNode {
  [key: string]: SettingsValue
}

export type SettingsValue =
  | string
  | number
  | boolean
  | null
  | SettingsNode
  | SettingsValue[]

export interface SettingsSnapshotResponse {
  settings: SettingsNode
  redacted_fields: string[]
}

export interface CascadeTierItem {
  tier: 'GCI' | 'ASI' | 'PPI' | 'PRI' | 'PAI'
  label: string
  path: string
  exists: boolean
  children?: CascadeTierItem[]
}

export interface CascadeDiagramResponse {
  tiers: CascadeTierItem[]
}

export interface HookEntry {
  event: string
  matcher: string
  command: string
  fire_count: number
}

export interface HooksResponse {
  hooks: HookEntry[]
}

// ── Personal section response shapes ─────────────────────────────────────────

export interface ThreadEntry {
  name: string
  path: string
  modified_at: string
}

export interface ThreadsResponse {
  items: ThreadEntry[]
  total: number
}

export interface IdeaInboxEntry {
  name: string
  path: string
  type: 'idea' | 'inbox'
  project: string
  modified_at: string
}

export interface IdeasInboxResponse {
  items: IdeaInboxEntry[]
  total: number
}

export interface CrdChain {
  id: string
  status: 'active' | 'idle' | 'completed'
  message_count: number
  last_updated: string
  summary: string
}

export interface CrdChainsResponse {
  chains: CrdChain[]
}

export interface ScheduledTask {
  name: string
  schedule: string
  last_run: string | null
  next_run: string | null
  status: 'active' | 'paused' | 'error'
}

export interface ScheduledTasksResponse {
  tasks: ScheduledTask[]
}

export interface AccountQuota {
  account_id: string
  cc_pct: number
  oc_pct: number
  models_available: string[]
  resets_at: string | null
}

export interface FleetQuotaResponse {
  accounts: AccountQuota[]
  updated_at: string
}

export interface LedgerEntry {
  account: string
  model: string
  tokens_in: number
  tokens_out: number
  cost: number
  period: string
}

export interface AccountLedgerResponse {
  entries: LedgerEntry[]
  total_cost: number
}

// ── Projects section response shapes ─────────────────────────────────────────

export interface AsiOverview {
  path: string
  description: string
}

export interface ProjectSummary {
  id: string
  name: string
  description: string
  repo_count: number
  active_phase_id: string | null
}

export interface ProjectsResponse {
  projects: ProjectSummary[]
  asi_overview: AsiOverview
}

export interface RepoCard {
  name: string
  branch: string
  last_commit_at: string
  has_claude_md: boolean
}

export interface ReposResponse {
  repos: RepoCard[]
}

export interface PewsPhaseStatus {
  phase_id: string
  phase_status: string
  pct_done: number
  tickets_total: number
  tickets_done: number
  tickets_blocked: number
  last_updated: string
}

export interface ScaffoldTicket {
  id: string
  title: string
  weight: 'XS' | 'S' | 'M' | 'L' | 'XL'
  status: string
}

export interface ScaffoldSprint {
  id: string
  title: string
  tickets: ScaffoldTicket[]
  done: number
  total: number
}

export interface ScaffoldWave {
  id: string
  title: string
  sprints: ScaffoldSprint[]
}

export interface ScaffoldEpic {
  id: string
  title: string
  status: string
  waves: ScaffoldWave[]
}

export interface ScaffoldTree {
  phase_id: string
  phase_name: string
  pct_done: number
  last_updated: string
  epics: ScaffoldEpic[]
}

// ── Harness section response shapes (mirrors cascade-types HarnessStatus) ────

/** A single harness integration entry (cc or oc). */
export interface HarnessEntry {
  /** Harness identifier: "cc" (Claude Code) or "oc" (OpenCode). */
  name: 'cc' | 'oc'
  /** Whether the harness settings file was found at the expected path. */
  detected: boolean
  /** Absolute path of the instruction file (CLAUDE.md / AGENTS.md), or empty string. */
  instructionFilePath: string
  /** Whether the instruction file is a symlink pointing to CASCADE.md. */
  instructionFileLinked: boolean
}

/** GET /api/gci/harness-status response. */
export interface HarnessStatus {
  harnesses: HarnessEntry[]
}

/** POST /api/gci/harness-regenerate response. */
export interface RegenerateResponse {
  /** Number of instruction-file symlinks created or updated. */
  regeneratedCount: number
}

// ── GP Chat streaming types ────────────────────────────────────────────────

/** Speaker role for a chat message. */
export type ChatRole = 'user' | 'assistant'

/** A single message in the /api/chat POST request body. */
export interface ChatRequestMessage {
  role: ChatRole
  content: string
}

/** POST /api/chat request body. */
export interface ChatRequest {
  messages: ChatRequestMessage[]
  tools?: string[]
  session_id?: string
}

/** Union of all SSE event payloads from the /api/chat stream. */
export type ChatStreamEvent =
  | { type: 'token'; content: string }
  | { type: 'tool_result'; result: unknown }
  | { type: 'error'; message: string }
  | { type: 'done' }

// ── Usage History response shapes (mirrors crates/cascade-types/src/usage.rs) ─

/** Token/cost totals for a single UTC calendar day across all accounts. */
export interface DailyTotal {
  /** UTC date in "YYYY-MM-DD" format. */
  date: string
  /** Sum of input tokens for this day across all accounts. */
  tokens_in: number
  /** Sum of output tokens for this day across all accounts. */
  tokens_out: number
  /** Estimated cost in USD for this day (display precision). */
  cost_usd: number
}

/** Per-account usage broken down by day, aligned to daily_totals. */
export interface AccountUsage {
  account_id: string
  label: string
  totals_by_day: DailyTotal[]
}

/** Period-wide aggregate across all accounts and all days in the requested range. */
export interface UsageSummary {
  total_tokens_in: number
  total_tokens_out: number
  total_cost_usd: number
  date_range_days: number
}

/** Wire-format response for GET /api/personal/usage-history?days=N (max 90). */
export interface UsageHistoryResponse {
  daily_totals: DailyTotal[]
  per_account: AccountUsage[]
  summary: UsageSummary
  date_range_days: number
}

// ── RAG/RRF serve-status response shapes (mirrors cascade-types RagStatusResponse) ─

/**
 * GET /api/gci/rag-status response.
 * P3: stub values (serving=false, index_count=0, etc.).
 * P4 will populate real RAG index data without field renames.
 */
export interface RagStatusResponse {
  /** Whether the cascade-core resolution engine is running with a loaded index. */
  serving: boolean
  /** Number of indexed entries (0 in P3 stub). */
  index_count: number
  /** ISO-8601 timestamp of last index run, or null if never indexed. */
  last_indexed: string | null
  /** Total index size in bytes (0 in P3 stub). */
  index_size_bytes: number
  /** Serve endpoint URL (e.g. "localhost:9762/rag"). */
  serve_endpoint: string
}
