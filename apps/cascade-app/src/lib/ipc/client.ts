/**
 * Purpose: Typed async wrappers for all 9 Tauri IPC commands exposed by the
 *   cascade-app Rust backend. Provides a single `ipc` object as the sole API
 *   surface for the frontend to communicate with the cascaded daemon.
 * Inputs:  Tauri invoke() from @tauri-apps/api/core; types from src/types/ipc.ts.
 * Outputs: ipc object with 9 methods — getStatus, resolve, search, inbox.list,
 *   inbox.send, memory.read, memory.write, config.get, config.set.
 *   All methods return a typed Promise; errors are thrown as CascadeIpcError.
 * Constraints: Requires a running Tauri 2 context (invoke not available in plain
 *   browser). Each method wraps invoke in error-catching that re-throws as
 *   CascadeIpcError — never swallows errors silently.
 *   cascade_inbox_send returns NotImplemented until T-P4-E01.
 * SPORT: MASTER-LIBS.md — ipc client, src/lib/ipc/client.ts
 */

import { invoke } from '@tauri-apps/api/core'

import { CascadeIpcError } from '../../types/errors'
import type {
  ConfigGetResult,
  ConfigSetResult,
  InboxSendAck,
  InboxSummaryResult,
  MemoryReadResult,
  MemoryWriteResult,
  ResolveResult,
  SearchResult,
  StatusResult,
} from '../../types/ipc'

// ── Internal helper ───────────────────────────────────────────────────────────

/**
 * Purpose: Wrap a Tauri invoke call and re-throw rejections as CascadeIpcError.
 * Inputs:  A Promise<T> from invoke().
 * Outputs: The resolved T, or throws CascadeIpcError on rejection.
 * Constraints: Tauri serialises Rust CascadeError as a plain string like
 *   "DaemonNotRunning: ..." or "{code:...,message:...}". We attempt to parse a
 *   structured payload first, then fall back to the raw string as both code and
 *   message so the code field is always set.
 */
async function call<T>(promise: Promise<T>): Promise<T> {
  try {
    return await promise
  } catch (raw: unknown) {
    // Tauri serialises Rust errors as strings or objects.
    if (typeof raw === 'string') {
      // Attempt structured parse, e.g. '{"code":"DaemonNotRunning","message":"..."}'
      if (raw.trimStart().startsWith('{')) {
        try {
          const parsed = JSON.parse(raw) as { code?: string; message?: string }
          if (typeof parsed.code === 'string') {
            throw new CascadeIpcError(parsed.code, parsed.message ?? raw)
          }
        } catch (rethrow) {
          if (rethrow instanceof CascadeIpcError) throw rethrow
          // Not valid JSON — fall through to colon-split.
        }
      }
      // Fallback: extract code from "CodeVariant: detail" format.
      const colonIdx = raw.indexOf(':')
      const code = colonIdx > 0 ? raw.slice(0, colonIdx).trim() : raw
      const message = colonIdx > 0 ? raw.slice(colonIdx + 1).trim() : raw
      throw new CascadeIpcError(code, message)
    }
    // Non-string rejection — wrap generically.
    const message = raw instanceof Error ? raw.message : String(raw)
    throw new CascadeIpcError('UnknownError', message)
  }
}

// ── IPC client object ─────────────────────────────────────────────────────────

/**
 * Purpose: Singleton IPC client — the sole frontend entry point for all
 *   cascaded daemon operations. Every method is a typed async function.
 * Inputs:  Method-specific params (documented per method below).
 * Outputs: Typed Promise resolved with daemon result, or CascadeIpcError thrown.
 * Constraints: Must be used inside a Tauri 2 WebView context. All methods
 *   propagate CascadeIpcError on failure — callers must handle errors.
 * SPORT: MASTER-LIBS.md — ipc object
 */
export const ipc = {
  /**
   * Purpose: Get daemon runtime status (PID, uptime, queue depth, RAG freshness).
   * Inputs:  None.
   * Outputs: StatusResult — pid, uptime_secs, queue_depth, rag_index_fresh, version, tcp_port?.
   * Constraints: Throws CascadeIpcError("DaemonNotRunning") if daemon is not up.
   * SPORT: MASTER-COMMANDS.md — cascade_status
   */
  getStatus(): Promise<StatusResult> {
    return call(invoke<StatusResult>('cascade_status'))
  },

  /**
   * Purpose: Resolve the instruction cascade for a given tier or the full cascade.
   * Inputs:  tier? — one of "gci", "pci", "apc", "ppc", "prc", "pac", or omit for full.
   *          format? — "markdown" (default) or "json".
   * Outputs: ResolveResult — content, format, tier.
   * Constraints: Throws CascadeIpcError on failure. format defaults to "markdown" server-side.
   * SPORT: MASTER-COMMANDS.md — cascade_resolve
   */
  resolve(tier?: string, format?: string): Promise<ResolveResult> {
    return call(invoke<ResolveResult>('cascade_resolve', { tier, format }))
  },

  /**
   * Purpose: Search the indexed knowledge base with a natural language query.
   * Inputs:  query — natural language search string (required).
   *          limit? — max hits to return (daemon defaults to 10 when absent).
   * Outputs: SearchResult — { hits: SearchHit[] } ranked best-first.
   * Constraints: Throws CascadeIpcError on failure. Returns empty hits when index is empty.
   * SPORT: MASTER-COMMANDS.md — cascade_search
   */
  search(query: string, limit?: number): Promise<SearchResult> {
    return call(invoke<SearchResult>('cascade_search', { query, limit }))
  },

  inbox: {
    /**
     * Purpose: List inbox messages for the active cascade context.
     * Inputs:  limit? — max items to return (all items when absent).
     * Outputs: InboxSummaryResult — { items: InboxItem[] } in reverse-chronological order.
     * Constraints: Throws CascadeIpcError on failure.
     * SPORT: MASTER-COMMANDS.md — cascade_inbox_list
     */
    list(limit?: number): Promise<InboxSummaryResult> {
      return call(invoke<InboxSummaryResult>('cascade_inbox_list', { limit }))
    },

    /**
     * Purpose: Send (write) a message into a project inbox.
     * Inputs:  to — target project slug or path.
     *          subject — message subject line.
     *          body — full message body.
     *          priority? — "critical" | "high" | "medium" | "low" (default: "medium").
     * Outputs: InboxSendAck — { id, path } of the written message file.
     * Constraints: NOT IMPLEMENTED — stubs until T-P4-E01 adds the daemon handler.
     *   Throws CascadeIpcError("NotImplemented") until then.
     * SPORT: MASTER-COMMANDS.md — cascade_inbox_send
     */
    send(to: string, subject: string, body: string, priority?: string): Promise<InboxSendAck> {
      return call(invoke<InboxSendAck>('cascade_inbox_send', { to, subject, body, priority }))
    },
  },

  memory: {
    /**
     * Purpose: Read a memory file from a project's .claude/memory/ directory.
     * Inputs:  project — absolute path to the project root or a slug registered with the daemon.
     *          file — filename relative to .claude/memory/, e.g. "decisions.md".
     * Outputs: MemoryReadResult — { content, path }.
     * Constraints: Throws CascadeIpcError("ResourceNotFound") if the file does not exist.
     * SPORT: MASTER-COMMANDS.md — cascade_memory_read
     */
    read(project: string, file: string): Promise<MemoryReadResult> {
      return call(invoke<MemoryReadResult>('cascade_memory_read', { project, file }))
    },

    /**
     * Purpose: Write (overwrite) a memory file in a project's .claude/memory/ directory.
     * Inputs:  project — absolute path or slug.
     *          file — filename relative to .claude/memory/, e.g. "lessons.md".
     *          content — full UTF-8 text to write (overwrites existing content).
     * Outputs: MemoryWriteResult — { path, bytes }.
     * Constraints: Creates the file if absent; overwrites if present. No append support.
     * SPORT: MASTER-COMMANDS.md — cascade_memory_write
     */
    write(project: string, file: string, content: string): Promise<MemoryWriteResult> {
      return call(invoke<MemoryWriteResult>('cascade_memory_write', { project, file, content }))
    },
  },

  config: {
    /**
     * Purpose: Read a configuration key from the daemon.
     * Inputs:  key — dot-separated config key, e.g. "daemon.socket_path".
     * Outputs: ConfigGetResult — { key, value } where value is an unknown JSON value.
     * Constraints: Throws CascadeIpcError("ResourceNotFound") for unregistered keys.
     * SPORT: MASTER-COMMANDS.md — cascade_config_get
     */
    get(key: string): Promise<ConfigGetResult> {
      return call(invoke<ConfigGetResult>('cascade_config_get', { key }))
    },

    /**
     * Purpose: Update a configuration key in the daemon.
     * Inputs:  key — dot-separated config key.
     *          value — new JSON value; type depends on the key (pass JS primitives directly).
     * Outputs: ConfigSetResult — { key, previous? }.
     * Constraints: Throws CascadeIpcError on invalid key or value type mismatch.
     * SPORT: MASTER-COMMANDS.md — cascade_config_set
     */
    set(key: string, value: unknown): Promise<ConfigSetResult> {
      return call(invoke<ConfigSetResult>('cascade_config_set', { key, value }))
    },
  },
} as const

export type Ipc = typeof ipc
