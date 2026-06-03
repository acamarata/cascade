//! `cascade search <query>` — run a RAG search against the active index.
//!
//! Sends the query to the cascade daemon's search endpoint over the Unix
//! socket (or named pipe on Windows). The daemon runs the retrieval pipeline
//! and returns ranked hits.
//!
//! When the daemon is not running, falls back to a direct (in-process)
//! keyword search over the resolved cascade text.
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

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::Args;

use super::Command;

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
}

#[async_trait]
impl Command for SearchArgs {
    async fn run(&self) -> Result<()> {
        // Try daemon first; fall back to in-process keyword search.
        if daemon_is_running() {
            return search_via_daemon(&self.query, self.top, self.json).await;
        }
        search_inline(&self.query, self.top, self.json).await
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

    if json {
        let arr: Vec<serde_json::Value> = hits
            .iter()
            .map(|(score, text)| serde_json::json!({ "score": score, "text": text }))
            .collect();
        println!("{}", serde_json::to_string_pretty(&arr).unwrap());
    } else {
        for (score, text) in &hits {
            println!("[{}] {}", score, text.lines().next().unwrap_or(""));
            let preview: String = text.lines().take(3).collect::<Vec<_>>().join(" ");
            println!("  {}\n", &preview[..preview.len().min(120)]);
        }
        if hits.is_empty() {
            println!("No results for: {}", query);
        }
    }
    Ok(())
}
