/**
 * Purpose: TypeScript mirror types for the Cascade IPC JSON-RPC 2.0 wire types.
 * Inputs:  Derived from crates/cascade-types/src/ipc.rs (FROZEN schema v1).
 * Outputs: Exported interfaces used by src/lib/ipc/client.ts and consuming modules.
 * Constraints: Must stay in sync with the Rust types. Schema is FROZEN — add fields
 *   by appending only. Never rename or remove without a versioning ticket.
 * SPORT: MASTER-LIBS.md — ipc types, src/types/ipc.ts
 */

// ── cascade_status ────────────────────────────────────────────────────────────

/** Result returned by cascade_status / cascade.status. Mirrors StatusResult. */
export interface StatusResult {
  /** OS process id of the running daemon. */
  pid: number;
  /** Seconds since the daemon started. */
  uptime_secs: number;
  /** Number of requests currently queued awaiting dispatch. */
  queue_depth: number;
  /** Whether the RAG index was last refreshed within its TTL. */
  rag_index_fresh: boolean;
  /** Daemon version string, e.g. "0.1.0". */
  version: string;
  /** TCP IPC port if enabled (schema v1.1 additive). */
  tcp_port?: number | null;
}

// ── cascade_resolve ───────────────────────────────────────────────────────────

/** Result returned by cascade_resolve. Mirrors ResolveResult. */
export interface ResolveResult {
  /** Resolved context content. */
  content: string;
  /** Format of the returned content: "markdown" or "json". */
  format: string;
  /** Tier that was resolved, e.g. "gci" or "full". */
  tier: string;
}

// ── cascade_search ────────────────────────────────────────────────────────────

/** A single search result hit. Mirrors SearchHit. */
export interface SearchHit {
  /** Unique chunk or document id. */
  id: string;
  /** Relevance score (higher = more relevant). */
  score: number;
  /** Short excerpt from the matching document. */
  excerpt: string;
  /** Source document path or URL. */
  source: string;
}

/** Result returned by cascade_search. Mirrors SearchResult. */
export interface SearchResult {
  /** Ranked search hits, best match first. */
  hits: SearchHit[];
}

// ── cascade_inbox_list / cascade_inbox_send ───────────────────────────────────

/** A single inbox item. Mirrors InboxItem. */
export interface InboxItem {
  /** Unique message id (filename slug or UUID). */
  id: string;
  /** Message subject line. */
  subject: string;
  /** Sender identity string. */
  from: string;
  /** Priority label: "critical", "high", "medium", or "low". */
  priority: string;
  /** ISO 8601 creation timestamp. */
  created: string;
}

/** Result returned by cascade_inbox_list. Mirrors InboxSummaryResult. */
export interface InboxSummaryResult {
  /** Inbox items in reverse chronological order. */
  items: InboxItem[];
}

/** Acknowledgement returned by cascade_inbox_send (pending T-P4-E01). Mirrors InboxSendAck. */
export interface InboxSendAck {
  /** The file slug of the created inbox message. */
  id: string;
  /** Absolute path to the written message file. */
  path: string;
}

// ── cascade_memory_read / cascade_memory_write ────────────────────────────────

/** Result returned by cascade_memory_read. Mirrors MemoryReadResult. */
export interface MemoryReadResult {
  /** Full UTF-8 text content of the memory file. */
  content: string;
  /** Absolute path to the file that was read. */
  path: string;
}

/** Result returned by cascade_memory_write. Mirrors MemoryWriteResult. */
export interface MemoryWriteResult {
  /** Absolute path of the file that was written. */
  path: string;
  /** Number of bytes written. */
  bytes: number;
}

// ── cascade_config_get / cascade_config_set ───────────────────────────────────

/** Result returned by cascade_config_get. Mirrors ConfigGetResult. */
export interface ConfigGetResult {
  /** The key that was queried. */
  key: string;
  /** Current value; type depends on the key (use JSON.parse if needed). */
  value: unknown;
}

/** Result returned by cascade_config_set. Mirrors ConfigSetResult. */
export interface ConfigSetResult {
  /** The key that was updated. */
  key: string;
  /** Previous value, if any existed. */
  previous?: unknown;
}
