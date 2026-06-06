/**
 * Purpose: RAG Explorer — semantic search over the indexed knowledge base.
 * Inputs:  Query string entered by user
 * Outputs: Ranked list of matching chunks with source path and score
 * Constraints:
 *   - Queries the `rag_query` Tauri command (backed by cascade-rag crate).
 *   - Debounce 400ms to avoid flooding the index on every keystroke.
 *   - Score is a float 0-1; display as a percentage.
 * SPORT: MASTER-COMPONENTS.md — RagExplorer
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { Search, FileText, Loader2 } from "lucide-react";

interface RagResult {
  content: string;
  score: number;
  source_path: string;
  chunk_index: number;
}

export function RagExplorer() {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<RagResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const runQuery = useCallback(async (q: string) => {
    if (!q.trim()) {
      setResults([]);
      return;
    }
    setIsLoading(true);
    setError(null);
    try {
      const res = await invoke<RagResult[]>("rag_query", {
        query: q,
        topK: 10,
      });
      setResults(res);
    } catch (err) {
      setError(String(err));
      setResults([]);
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => runQuery(query), 400);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, runQuery]);

  return (
    <div className="flex h-full flex-col gap-4">
      <h1 className="text-lg font-semibold">RAG Explorer</h1>

      {/* Search input */}
      <div className="relative">
        <Search
          className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search the knowledge base…"
          className="w-full rounded-md border border-border bg-background py-2 pl-9 pr-4 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="Search query"
          autoFocus
        />
        {isLoading && (
          <Loader2
            className="absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 animate-spin text-muted-foreground"
            aria-hidden="true"
          />
        )}
      </div>

      {/* Error state */}
      {error && (
        <div role="alert" className="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </div>
      )}

      {/* Results */}
      <ul className="flex flex-col gap-3 overflow-auto" aria-label="Search results" aria-live="polite">
        {results.length === 0 && query && !isLoading && (
          <li className="text-sm text-muted-foreground">No results found.</li>
        )}
        {results.map((r, _i) => (
          <li
            key={`${r.source_path}-${r.chunk_index}`}
            className="rounded-lg border border-border bg-card p-4 text-sm"
          >
            <div className="mb-2 flex items-center justify-between gap-2">
              <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                <FileText className="h-3.5 w-3.5" aria-hidden="true" />
                <span title={r.source_path}>
                  {r.source_path.split("/").slice(-2).join("/")}
                </span>
                <span aria-hidden="true">·</span>
                <span>chunk {r.chunk_index}</span>
              </div>
              <span
                className={[
                  "shrink-0 rounded-full px-2 py-0.5 text-xs font-medium tabular-nums",
                  r.score >= 0.8
                    ? "bg-green-500/15 text-green-700 dark:text-green-400"
                    : r.score >= 0.5
                    ? "bg-yellow-500/15 text-yellow-700 dark:text-yellow-400"
                    : "bg-muted text-muted-foreground",
                ].join(" ")}
                aria-label={`Relevance score ${Math.round(r.score * 100)}%`}
              >
                {Math.round(r.score * 100)}%
              </span>
            </div>
            <p className="whitespace-pre-wrap text-foreground/90 leading-relaxed">
              {r.content}
            </p>
          </li>
        ))}
      </ul>
    </div>
  );
}
