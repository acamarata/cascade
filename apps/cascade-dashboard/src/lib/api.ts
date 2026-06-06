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
