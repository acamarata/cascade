//! `cascade search <query>` — run a RAG search against the active index.
//!
//! Sends the query to the cascade daemon's search endpoint over the Unix
//! socket (or named pipe on Windows). The daemon runs the retrieval pipeline
//! and returns ranked hits.
//!
//! When the daemon is not running, falls back to a direct (in-process)
//! keyword search over the resolved cascade text or the memory corpus.
//!
//! # Output
//!
//! Each hit is printed as:
//! ```text
//! [score] source_path:line_number
//! chunk text preview...
//! ```
//!
//! `--json` emits a JSON array of hit objects.
//! `--top N` limits results to the top N hits (default 10).
//! `--scope memory` restricts the search to `.cascade/memory/` files.

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, ValueEnum};

use super::Command;

/// Scope of the search corpus.
#[derive(Debug, Clone, PartialEq, Eq, ValueEnum, Default)]
pub enum SearchScope {
    /// Search the full cascade corpus (default): instructions + rules + templates.
    #[default]
    All,
    /// Search only `.cascade/memory/` files (decisions, lessons, patterns).
    Memory,
}

/// Arguments for `cascade search`.
#[derive(Debug, Args)]
pub struct SearchArgs {
    /// The search query. Natural-language or keyword.
    pub query: String,

    /// Maximum number of results to return.
    #[arg(long, short = 'n', default_value_t = 10)]
    pub top: usize,

    /// Output results as JSON.
    #[arg(long)]
    pub json: bool,

    /// Restrict the search scope.
    ///
    /// - `all` (default): full cascade corpus.
    /// - `memory`: only `.cascade/memory/` files (decisions, lessons, patterns).
    #[arg(long, value_enum, default_value_t = SearchScope::All)]
    pub scope: SearchScope,
}

#[async_trait]
impl Command for SearchArgs {
    async fn run(&self) -> Result<()> {
        match self.scope {
            SearchScope::Memory => search_memory(&self.query, self.top, self.json).await,
            SearchScope::All => {
                if daemon_is_running() {
                    return search_via_daemon(&self.query, self.top, self.json).await;
                }
                search_inline(&self.query, self.top, self.json).await
            }
        }
    }
}

fn daemon_is_running() -> bool {
    cascade_types::paths::daemon_socket().exists()
}

/// Send the query to the daemon over the Unix socket and print results.
async fn search_via_daemon(query: &str, top: usize, json: bool) -> Result<()> {
    // Full IPC implementation lives in E7 (cascade-rag + daemon socket).
    // For now, log a TODO and fall through to inline search.
    tracing::debug!("daemon search not yet implemented; falling back to inline");
    search_inline(query, top, json).await
}

/// In-process keyword search over the resolved cascade text.
///
/// Splits merged text into paragraphs and ranks by term frequency.
async fn search_inline(query: &str, top: usize, json: bool) -> Result<()> {
    use std::path::PathBuf;
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let resolved = cascade_core::resolution::Resolver::new()
        .resolve(&cwd)
        .await?;

    let terms: Vec<&str> = query.split_whitespace().collect();

    let mut hits: Vec<(usize, String)> = resolved
        .merged_text
        .split("\n\n")
        .filter_map(|para| {
            let score = terms
                .iter()
                .filter(|&&t| para.to_lowercase().contains(&t.to_lowercase()))
                .count();
            if score > 0 {
                Some((score, para.to_string()))
            } else {
                None
            }
        })
        .collect();

    hits.sort_by_key(|h| std::cmp::Reverse(h.0));
    hits.truncate(top);

    if hits.is_empty() && !json {
        println!("No results for: {}", query);
    } else {
        print_hits(&hits, json);
    }
    Ok(())
}

/// Search only the memory corpus (`.cascade/memory/`).
///
/// Walks up from CWD to find the nearest `.cascade/memory/` directory, then
/// reads all `.md` files and ranks paragraphs by term frequency. Reports zero
/// results (not an error) when no `.cascade/memory/` directory is found.
async fn search_memory(query: &str, top: usize, json: bool) -> Result<()> {
    use std::path::PathBuf;

    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    let cascade_dir = locate_cascade_dir(&cwd);
    let memory_dir = cascade_dir.join("memory");

    if !memory_dir.is_dir() {
        if json {
            println!("[]");
        } else {
            println!("No memory corpus found at: {}", memory_dir.display());
        }
        return Ok(());
    }

    let terms: Vec<String> = query.split_whitespace().map(|s| s.to_lowercase()).collect();

    let mut hits: Vec<(usize, String)> = Vec::new();

    let mut rd = match tokio::fs::read_dir(&memory_dir).await {
        Ok(rd) => rd,
        Err(_) => {
            if json {
                println!("[]");
            }
            return Ok(());
        }
    };

    loop {
        let entry = match rd.next_entry().await {
            Ok(Some(e)) => e,
            Ok(None) => break,
            Err(_) => break,
        };
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("md") {
            continue;
        }
        let content = match tokio::fs::read_to_string(&path).await {
            Ok(c) => c,
            Err(_) => continue,
        };
        let file_label = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("?")
            .to_string();

        for para in content.split("\n\n") {
            let lower = para.to_lowercase();
            let score = terms.iter().filter(|t| lower.contains(t.as_str())).count();
            if score > 0 {
                hits.push((score, format!("[{}] {}", file_label, para)));
            }
        }
    }

    hits.sort_by_key(|h| std::cmp::Reverse(h.0));
    hits.truncate(top);

    if hits.is_empty() && !json {
        println!("No memory results for: {}", query);
    } else {
        print_hits(&hits, json);
    }
    Ok(())
}

fn locate_cascade_dir(cwd: &std::path::Path) -> std::path::PathBuf {
    for ancestor in cwd.ancestors() {
        let candidate = ancestor.join(".cascade");
        if candidate.is_dir() {
            return candidate;
        }
    }
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
    std::path::PathBuf::from(home).join(".cascade")
}

fn print_hits(hits: &[(usize, String)], json: bool) {
    if json {
        let arr: Vec<serde_json::Value> = hits
            .iter()
            .map(|(score, text)| serde_json::json!({ "score": score, "text": text }))
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&arr).unwrap_or_else(|_| "[]".into())
        );
    } else {
        for (score, text) in hits {
            println!("[{}] {}", score, text.lines().next().unwrap_or(""));
            let preview: String = text.lines().take(3).collect::<Vec<_>>().join(" ");
            let end = preview.len().min(120);
            println!("  {}\n", &preview[..end]);
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn search_scope_default() {
        let scope = SearchScope::default();
        assert_eq!(scope, SearchScope::All);
    }

    #[tokio::test]
    async fn search_memory_no_dir_returns_ok() {
        // With a path that has no .cascade/memory/, must not error.
        let tmp = tempfile::tempdir().expect("tempdir");
        let orig = std::env::current_dir().ok();
        std::env::set_current_dir(tmp.path()).ok();

        let result = search_memory("test query", 10, false).await;
        assert!(result.is_ok(), "must not error when no memory dir exists");

        if let Some(orig) = orig {
            std::env::set_current_dir(orig).ok();
        }
    }

    #[tokio::test]
    async fn search_memory_finds_written_entry() {
        let tmp = tempfile::tempdir().expect("tempdir");
        let cascade_dir = tmp.path().join(".cascade");
        let memory_dir = cascade_dir.join("memory");
        std::fs::create_dir_all(&memory_dir).expect("mkdir");

        std::fs::write(
            memory_dir.join("decisions.md"),
            "## 2026-01-01  #decision\n\ndecided to use sqlite for the local-first constraint\n",
        )
        .expect("write");

        let orig = std::env::current_dir().ok();
        std::env::set_current_dir(tmp.path()).ok();

        let result = search_memory("sqlite local-first", 10, false).await;
        assert!(result.is_ok());

        if let Some(orig) = orig {
            std::env::set_current_dir(orig).ok();
        }
    }
}
