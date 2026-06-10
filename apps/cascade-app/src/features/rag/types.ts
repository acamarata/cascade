/**
 * Purpose: TypeScript types for the RAG Explorer panel.
 *   Mirrors the shapes returned by Tauri commands rag_list_sources,
 *   rag_search, rag_index_stats, and the rag-ingest-progress event.
 * Inputs:  N/A — pure type module.
 * Outputs: Exported interfaces consumed by RAG panel components.
 * Constraints:
 *   - Must stay in sync with src-tauri/src/rag_commands.rs return types.
 *   - Add fields by appending only (frozen schema until T-P4-E01-29).
 * SPORT: MASTER-COMPONENTS.md — RAG Explorer types (T-P4-E01-27)
 */

/** Status of a single indexed source. */
export type SourceStatus = 'indexed' | 'indexing' | 'error' | 'pending'

/** One row in the SourceList table. */
export interface RagSource {
  /** Absolute path to the indexed file or directory. */
  path: string
  /** Number of text chunks stored for this source. */
  chunk_count: number
  /** ISO-8601 timestamp of last successful index, or null if never. */
  indexed_at: string | null
  /** Current status of this source. */
  status: SourceStatus
}

/** A single search result (Citation). */
export interface RagCitation {
  /** Absolute path to the source file. */
  path: string
  /** Heading breadcrumb within the file, e.g. "## Setup > ### Install". */
  heading_path: string
  /** Short snippet preview from the matching chunk. */
  snippet: string
  /** Reciprocal-rank fusion score (higher = more relevant). */
  rrf_score: number
}

/** Response from rag_search. */
export interface RagSearchResponse {
  /** Up to 10 ranked citations. */
  citations: RagCitation[]
  /** Total time taken for the query, in milliseconds. */
  query_ms: number
}

/** Response from rag_list_sources. */
export interface RagListSourcesResponse {
  sources: RagSource[]
}

/** Response from rag_index_stats. */
export interface RagIndexStats {
  /** Total number of indexed files. */
  total_files: number
  /** Total number of stored chunks across all files. */
  total_chunks: number
  /** Index size on disk in bytes. */
  index_size_bytes: number
  /** ISO-8601 timestamp of the most recent index operation, or null. */
  last_updated: string | null
}

/** Payload carried by the "rag-ingest-progress" Tauri event. */
export interface RagIngestProgress {
  /** Name of the file currently being ingested (basename). */
  file_name: string
  /** Number of chunks processed so far. */
  chunks_done: number
  /** Total number of chunks expected (0 if unknown). */
  chunks_total: number
  /** Integer 0–100 completion percentage. */
  percent: number
  /** True when the ingest operation is complete. */
  done: boolean
}
