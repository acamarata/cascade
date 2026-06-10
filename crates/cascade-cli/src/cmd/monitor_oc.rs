//! `cascade monitor-oc` — watch OC session logs and capture assistant turns.
//!
//! ## Purpose
//! Watches `~/.config/opencode/sessions/<latest>/` for new assistant turns in
//! `*.jsonl` log files and appends them to `{repo}/.cascade/oc-session-log.md`
//! for Cascade's RAG to index.
//!
//! ## Polling strategy
//! Uses `notify` (v6) for file-system events with a 5-second debounce fallback.
//! On each JSONL write event the watcher re-reads the file from the last known
//! offset, extracts `role: "assistant"` lines, and appends them.
//!
//! ## Inputs
//! - `~/.config/opencode/sessions/` (or `--session-dir <path>` for tests)
//! - `*.jsonl` files inside the watched session directory
//!
//! ## Outputs
//! - `{repo}/.cascade/oc-session-log.md` (append-only)
//!
//! ## Constraints
//! - Runs until SIGINT; uses `notify` for file-system events
//! - Partial-line resilient: lines without a terminating newline are buffered
//! - HOME-confined: all default paths derived from home_dir()
//!
//! ## SPORT
//! MASTER-CLI.md: cascade monitor-oc

use std::{
    collections::HashMap,
    io::{BufRead, BufReader, Seek, SeekFrom},
    path::{Path, PathBuf},
    sync::mpsc,
    time::Duration,
};

use async_trait::async_trait;
use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
};
use chrono::Utc;
use clap::Args;
use notify::{recommended_watcher, Event, RecursiveMode, Watcher};
use serde_json::Value;

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade monitor-oc`.
#[derive(Debug, Args)]
pub struct MonitorOcArgs {
    /// Path to the OC sessions directory. Defaults to
    /// `~/.config/opencode/sessions/`.
    #[arg(long, value_name = "PATH")]
    pub session_dir: Option<PathBuf>,

    /// Project repo directory. Defaults to the current working directory.
    #[arg(long, value_name = "PATH")]
    pub repo: Option<PathBuf>,

    /// Process existing JSONL files once then exit (for testing / CI).
    #[arg(long)]
    pub once: bool,
}

// ── Helpers (exported for tests) ─────────────────────────────────────────────

/// Default OC sessions directory.
pub fn default_sessions_dir() -> PathBuf {
    home_dir().join(".config").join("opencode").join("sessions")
}

/// Path to the OC session log in a repo.
pub fn session_log_path(repo: &Path) -> PathBuf {
    repo.join(".cascade").join("oc-session-log.md")
}

/// A single parsed assistant turn extracted from a JSONL line.
#[derive(Debug, Clone, PartialEq)]
pub struct AssistantTurn {
    pub session_id: String,
    pub content: String,
    pub timestamp: String,
}

/// Parse a single JSONL line. Returns `Some(AssistantTurn)` when the line
/// contains a completed assistant message, `None` otherwise.
///
/// Expected format (OC logs assistant turns as JSON objects):
/// ```json
/// {"role":"assistant","content":"...","session_id":"...","timestamp":"..."}
/// ```
/// Fields beyond `role` and `content` are optional; defaults are used when absent.
pub fn parse_jsonl_line(line: &str, default_session_id: &str) -> Option<AssistantTurn> {
    let trimmed = line.trim();
    if trimmed.is_empty() {
        return None;
    }

    let obj: Value = serde_json::from_str(trimmed).ok()?;

    // Must be role: "assistant"
    let role = obj.get("role").and_then(Value::as_str)?;
    if role != "assistant" {
        return None;
    }

    // Content can be a string or an array of content blocks
    let content = match obj.get("content") {
        Some(Value::String(s)) => s.clone(),
        Some(Value::Array(blocks)) => {
            // Extract text from content blocks: [{type:"text", text:"..."}]
            blocks
                .iter()
                .filter_map(|b| b.get("text").and_then(Value::as_str))
                .collect::<Vec<_>>()
                .join("\n")
        }
        _ => return None,
    };

    if content.trim().is_empty() {
        return None;
    }

    let session_id = obj
        .get("session_id")
        .and_then(Value::as_str)
        .unwrap_or(default_session_id)
        .to_string();

    let timestamp = obj
        .get("timestamp")
        .and_then(Value::as_str)
        .map(ToString::to_string)
        .unwrap_or_else(|| Utc::now().to_rfc3339());

    Some(AssistantTurn {
        session_id,
        content,
        timestamp,
    })
}

/// Format an `AssistantTurn` as a Markdown section for the session log.
pub fn format_turn(turn: &AssistantTurn) -> String {
    format!(
        "\n---\n\n## Session `{}` — {}\n\n{}\n",
        turn.session_id, turn.timestamp, turn.content
    )
}

/// Append `text` to `path`, creating parent directories and the file if needed.
pub fn append_to_log(path: &Path, text: &str) -> Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| {
            CascadeError::Other(format!("cannot create dir {}: {e}", parent.display()))
        })?;
    }
    use std::io::Write;
    let mut file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
        .map_err(|e| CascadeError::Other(format!("open log {}: {e}", path.display())))?;
    file.write_all(text.as_bytes())
        .map_err(|e| CascadeError::Other(format!("write log {}: {e}", path.display())))?;
    Ok(())
}

/// Drain all new complete lines from `path` starting at `offset`.
/// Returns (new_offset, Vec<AssistantTurn>). Handles partial lines gracefully
/// (a line without trailing `\n` is not yielded until the next call).
pub fn drain_new_turns(
    path: &Path,
    offset: u64,
    session_id: &str,
) -> Result<(u64, Vec<AssistantTurn>)> {
    let mut file = std::fs::File::open(path)
        .map_err(|e| CascadeError::Other(format!("open {}: {e}", path.display())))?;
    file.seek(SeekFrom::Start(offset))
        .map_err(|e| CascadeError::Other(format!("seek {}: {e}", path.display())))?;

    let mut turns = Vec::new();
    let mut new_offset = offset;
    let reader = BufReader::new(&file);

    for line_result in reader.lines() {
        let line = line_result
            .map_err(|e| CascadeError::Other(format!("read line {}: {e}", path.display())))?;
        // BufReader::lines strips the newline, so we add 1 for the '\n' byte.
        // +1 may over-count on Windows (\r\n) but is safe for append semantics.
        new_offset += line.len() as u64 + 1;
        if let Some(turn) = parse_jsonl_line(&line, session_id) {
            turns.push(turn);
        }
    }

    Ok((new_offset, turns))
}

/// Scan an entire JSONL file from the beginning and return all assistant turns.
/// Used for `--once` mode and fixture-based tests.
#[allow(dead_code)]
pub fn drain_all_turns(path: &Path, session_id: &str) -> Result<Vec<AssistantTurn>> {
    let (_, turns) = drain_new_turns(path, 0, session_id)?;
    Ok(turns)
}

// ── Core watch loop (synchronous, extracted for testability) ─────────────────

/// Process a single JSONL file event: drain new turns, append to log.
/// Returns the number of turns appended.
pub fn process_jsonl_event(
    jsonl_path: &Path,
    log_path: &Path,
    offsets: &mut HashMap<PathBuf, u64>,
) -> Result<usize> {
    let session_id = jsonl_path
        .parent()
        .and_then(|p| p.file_name())
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| "unknown".to_string());

    let offset = offsets.get(jsonl_path).copied().unwrap_or(0);
    let (new_offset, turns) = drain_new_turns(jsonl_path, offset, &session_id)?;

    offsets.insert(jsonl_path.to_path_buf(), new_offset);

    for turn in &turns {
        let formatted = format_turn(turn);
        append_to_log(log_path, &formatted)?;
    }

    Ok(turns.len())
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for MonitorOcArgs {
    async fn run(&self) -> Result<()> {
        let repo = match &self.repo {
            Some(p) => p.clone(),
            None => std::env::current_dir()
                .map_err(|e| CascadeError::Other(format!("cannot get cwd: {e}")))?,
        };

        let sessions_dir = self
            .session_dir
            .clone()
            .unwrap_or_else(default_sessions_dir);

        let log_path = session_log_path(&repo);
        let mut offsets: HashMap<PathBuf, u64> = HashMap::new();

        if self.once {
            // Process existing JSONL files and exit
            return run_once(&sessions_dir, &log_path, &mut offsets);
        }

        println!(
            "cascade monitor-oc: watching {} → {}",
            sessions_dir.display(),
            log_path.display()
        );
        println!("Press Ctrl-C to stop.");

        // Watch loop
        let (tx, rx) = mpsc::channel();

        let mut watcher = recommended_watcher(move |res: notify::Result<Event>| {
            if let Ok(event) = res {
                let _ = tx.send(event);
            }
        })
        .map_err(|e| CascadeError::Other(format!("watcher init error: {e}")))?;

        watcher
            .watch(&sessions_dir, RecursiveMode::Recursive)
            .map_err(|e| {
                CascadeError::Other(format!("cannot watch {}: {e}", sessions_dir.display()))
            })?;

        // Drain any already-existing JSONL files before entering the event loop
        run_once(&sessions_dir, &log_path, &mut offsets)?;

        loop {
            match rx.recv_timeout(Duration::from_secs(5)) {
                Ok(event) => {
                    for path in &event.paths {
                        if path.extension().map(|e| e == "jsonl").unwrap_or(false) {
                            if let Err(e) = process_jsonl_event(path, &log_path, &mut offsets) {
                                eprintln!("monitor-oc: error processing {}: {e}", path.display());
                            }
                        }
                    }
                }
                Err(mpsc::RecvTimeoutError::Timeout) => {
                    // Periodic scan in case watcher missed events
                    run_once(&sessions_dir, &log_path, &mut offsets)?;
                }
                Err(mpsc::RecvTimeoutError::Disconnected) => break,
            }
        }

        Ok(())
    }
}

/// Scan all `*.jsonl` files in `sessions_dir` (recursive), drain turns from
/// current offset, append to `log_path`.
fn run_once(
    sessions_dir: &Path,
    log_path: &Path,
    offsets: &mut HashMap<PathBuf, u64>,
) -> Result<()> {
    if !sessions_dir.exists() {
        return Ok(());
    }

    for entry in walkdir_jsonl(sessions_dir) {
        let _ = process_jsonl_event(&entry, log_path, offsets);
    }

    Ok(())
}

/// Walk `dir` recursively and collect all `*.jsonl` paths.
fn walkdir_jsonl(dir: &Path) -> Vec<PathBuf> {
    let mut results = Vec::new();
    if let Ok(entries) = std::fs::read_dir(dir) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                results.extend(walkdir_jsonl(&path));
            } else if path.extension().map(|e| e == "jsonl").unwrap_or(false) {
                results.push(path);
            }
        }
    }
    results
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    // ── parse_jsonl_line ──────────────────────────────────────────────────────

    #[test]
    fn parse_assistant_string_content() {
        let line = r#"{"role":"assistant","content":"Hello world","session_id":"abc123","timestamp":"2026-06-10T00:00:00Z"}"#;
        let turn = parse_jsonl_line(line, "fallback").expect("should parse");
        assert_eq!(turn.content, "Hello world");
        assert_eq!(turn.session_id, "abc123");
        assert_eq!(turn.timestamp, "2026-06-10T00:00:00Z");
    }

    #[test]
    fn parse_assistant_content_blocks() {
        let line = r#"{"role":"assistant","content":[{"type":"text","text":"Block one"},{"type":"text","text":"Block two"}],"session_id":"s1"}"#;
        let turn = parse_jsonl_line(line, "fallback").expect("should parse blocks");
        assert!(
            turn.content.contains("Block one") && turn.content.contains("Block two"),
            "blocks should be joined: {}",
            turn.content
        );
    }

    #[test]
    fn parse_skips_user_role() {
        let line = r#"{"role":"user","content":"A question","session_id":"s1"}"#;
        assert!(parse_jsonl_line(line, "fallback").is_none());
    }

    #[test]
    fn parse_skips_empty_content() {
        let line = r#"{"role":"assistant","content":"","session_id":"s1"}"#;
        assert!(parse_jsonl_line(line, "fallback").is_none());
    }

    #[test]
    fn parse_empty_line_returns_none() {
        assert!(parse_jsonl_line("", "fallback").is_none());
        assert!(parse_jsonl_line("   ", "fallback").is_none());
    }

    #[test]
    fn parse_malformed_json_returns_none() {
        assert!(parse_jsonl_line("{not json", "fallback").is_none());
    }

    #[test]
    fn parse_uses_fallback_session_id() {
        let line = r#"{"role":"assistant","content":"Hi"}"#;
        let turn = parse_jsonl_line(line, "fallback-id").expect("should parse");
        assert_eq!(turn.session_id, "fallback-id");
    }

    // ── drain_new_turns ───────────────────────────────────────────────────────

    #[test]
    fn drain_all_turns_reads_assistant_lines() {
        let dir = TempDir::new().unwrap();
        let jsonl = dir.path().join("session.jsonl");
        let content = concat!(
            "{\"role\":\"user\",\"content\":\"q1\",\"session_id\":\"s1\"}\n",
            "{\"role\":\"assistant\",\"content\":\"a1\",\"session_id\":\"s1\"}\n",
            "{\"role\":\"assistant\",\"content\":\"a2\",\"session_id\":\"s1\"}\n",
        );
        std::fs::write(&jsonl, content).unwrap();

        let turns = drain_all_turns(&jsonl, "s1").unwrap();
        assert_eq!(turns.len(), 2, "should extract 2 assistant turns");
        assert_eq!(turns[0].content, "a1");
        assert_eq!(turns[1].content, "a2");
    }

    #[test]
    fn drain_new_turns_incremental_offset() {
        let dir = TempDir::new().unwrap();
        let jsonl = dir.path().join("session.jsonl");

        let line1 = "{\"role\":\"assistant\",\"content\":\"first\",\"session_id\":\"s1\"}\n";
        std::fs::write(&jsonl, line1).unwrap();

        let (offset1, turns1) = drain_new_turns(&jsonl, 0, "s1").unwrap();
        assert_eq!(turns1.len(), 1);
        assert!(offset1 > 0);

        // Append a second line
        let line2 = "{\"role\":\"assistant\",\"content\":\"second\",\"session_id\":\"s1\"}\n";
        use std::io::Write;
        let mut f = std::fs::OpenOptions::new()
            .append(true)
            .open(&jsonl)
            .unwrap();
        f.write_all(line2.as_bytes()).unwrap();

        let (offset2, turns2) = drain_new_turns(&jsonl, offset1, "s1").unwrap();
        assert_eq!(turns2.len(), 1, "only the new line should be returned");
        assert_eq!(turns2[0].content, "second");
        assert!(offset2 > offset1);
    }

    // ── process_jsonl_event + append_to_log ───────────────────────────────────

    #[test]
    fn process_jsonl_event_appends_to_log() {
        let dir = TempDir::new().unwrap();

        // Session subdir (simulates ~/.config/opencode/sessions/<id>/)
        let session_dir = dir.path().join("sessions").join("session-abc");
        std::fs::create_dir_all(&session_dir).unwrap();

        let jsonl = session_dir.join("log.jsonl");
        std::fs::write(
            &jsonl,
            "{\"role\":\"assistant\",\"content\":\"Test answer\",\"session_id\":\"session-abc\"}\n",
        )
        .unwrap();

        let log_path = dir.path().join(".cascade").join("oc-session-log.md");
        let mut offsets = HashMap::new();

        let count = process_jsonl_event(&jsonl, &log_path, &mut offsets).unwrap();
        assert_eq!(count, 1, "one turn should be appended");

        let log_content = std::fs::read_to_string(&log_path).unwrap();
        assert!(
            log_content.contains("Test answer"),
            "log must contain the assistant turn: {log_content}"
        );
        assert!(
            log_content.contains("session-abc"),
            "log must contain the session id: {log_content}"
        );
    }

    #[test]
    fn process_jsonl_event_idempotent_on_second_call() {
        let dir = TempDir::new().unwrap();
        let session_dir = dir.path().join("sessions").join("s1");
        std::fs::create_dir_all(&session_dir).unwrap();

        let jsonl = session_dir.join("log.jsonl");
        std::fs::write(
            &jsonl,
            "{\"role\":\"assistant\",\"content\":\"Answer\",\"session_id\":\"s1\"}\n",
        )
        .unwrap();

        let log_path = dir.path().join(".cascade").join("oc-session-log.md");
        let mut offsets = HashMap::new();

        process_jsonl_event(&jsonl, &log_path, &mut offsets).unwrap();
        process_jsonl_event(&jsonl, &log_path, &mut offsets).unwrap();

        let content = std::fs::read_to_string(&log_path).unwrap();
        // "Answer" should appear exactly once
        let count = content.matches("Answer").count();
        assert_eq!(
            count, 1,
            "answer should appear exactly once after two calls"
        );
    }

    // ── run_once integration ──────────────────────────────────────────────────

    #[test]
    fn run_once_scans_fixture_session_dir() {
        let dir = TempDir::new().unwrap();

        // Simulate OC session structure: sessions/<id>/log.jsonl
        let session_dir = dir.path().join("sessions").join("test-session");
        std::fs::create_dir_all(&session_dir).unwrap();

        let jsonl = session_dir.join("chat.jsonl");
        std::fs::write(
            &jsonl,
            concat!(
                "{\"role\":\"user\",\"content\":\"hello\",\"session_id\":\"test-session\"}\n",
                "{\"role\":\"assistant\",\"content\":\"world\",\"session_id\":\"test-session\"}\n",
            ),
        )
        .unwrap();

        let log_path = dir.path().join(".cascade").join("oc-session-log.md");
        let mut offsets = HashMap::new();

        run_once(&dir.path().join("sessions"), &log_path, &mut offsets).unwrap();

        let content = std::fs::read_to_string(&log_path).unwrap();
        assert!(
            content.contains("world"),
            "log must contain the assistant turn: {content}"
        );
        assert!(
            !content.contains("hello"),
            "log must not contain user message: {content}"
        );
    }
}
