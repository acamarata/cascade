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
