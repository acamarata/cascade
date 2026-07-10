//! Purpose: read-only personal topic index for the dashboard chat selector.
//! Inputs:  `$HOME/Downloads/.claude/threads/` where each subdirectory is a topic.
//! Outputs: JSON `{ "topics": [{ "id": "...", "title": "..." }] }`.
//! Constraints: missing directories return an empty list; no auth at this router level.

use std::path::{Path, PathBuf};

use axum::{routing::get, Json, Router};
use serde::Serialize;
use serde_json::json;
use tokio::io::AsyncReadExt;

use crate::dashboard::DashboardState;

const TITLE_READ_MAX_BYTES: u64 = 4096;
const TITLE_MAX_CHARS: usize = 96;

#[derive(Debug, Serialize)]
struct TopicEntry {
    id: String,
    title: String,
}

pub fn router() -> Router<DashboardState> {
    Router::new().route("/", get(list_topics))
}

async fn list_topics() -> Json<serde_json::Value> {
    let topics_dir = match personal_threads_dir() {
        Some(path) => path,
        None => return Json(json!({ "topics": [] })),
    };

    let mut entries = match sorted_children(&topics_dir).await {
        Some(entries) => entries,
        None => return Json(json!({ "topics": [] })),
    };

    let mut topics = Vec::new();
    for path in entries.drain(..) {
        let Ok(meta) = tokio::fs::metadata(&path).await else {
            continue;
        };
        if !meta.is_dir() {
            continue;
        }
        let Some(id) = path
            .file_name()
            .and_then(|n| n.to_str())
            .map(str::to_string)
        else {
            continue;
        };
        let title = topic_title(&path, &id).await;
        topics.push(TopicEntry { id, title });
    }

    Json(json!({ "topics": topics }))
}

fn personal_threads_dir() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .map(|home| home.join("Downloads").join(".claude").join("threads"))
}

async fn sorted_children(dir: &Path) -> Option<Vec<PathBuf>> {
    let mut rd = tokio::fs::read_dir(dir).await.ok()?;
    let mut out = Vec::new();
    while let Ok(Some(entry)) = rd.next_entry().await {
        out.push(entry.path());
    }
    out.sort_by_key(|p| {
        p.file_name()
            .map(|n| n.to_string_lossy().to_ascii_lowercase())
            .unwrap_or_default()
    });
    Some(out)
}

async fn topic_title(topic_dir: &Path, id: &str) -> String {
    if let Some(children) = sorted_children(topic_dir).await {
        for path in children {
            let Ok(meta) = tokio::fs::metadata(&path).await else {
                continue;
            };
            if !meta.is_file() {
                continue;
            }
            if let Some(text) = read_text_prefix(&path).await {
                if let Some(title) = title_from_text(&text) {
                    return title;
                }
            }
        }
    }
    prettify_topic_id(id)
}

async fn read_text_prefix(path: &Path) -> Option<String> {
    let file = tokio::fs::File::open(path).await.ok()?;
    let mut limited = file.take(TITLE_READ_MAX_BYTES);
    let mut bytes = Vec::new();
    limited.read_to_end(&mut bytes).await.ok()?;
    let text = String::from_utf8_lossy(&bytes).to_string();
    if text.trim().is_empty() {
        None
    } else {
        Some(text)
    }
}

fn title_from_text(text: &str) -> Option<String> {
    for line in text.lines().take(12) {
        let line = line.trim();
        if line.is_empty() || line == "---" {
            continue;
        }
        let lower = line.to_ascii_lowercase();
        let candidate = if lower.starts_with("title:") {
            line.split_once(':')
                .map(|(_, rest)| rest.trim())
                .unwrap_or(line)
        } else if line.starts_with('#') {
            line.trim_start_matches('#').trim()
        } else {
            line
        };
        let title = clean_title(candidate);
        if !title.is_empty() {
            return Some(title);
        }
    }
    None
}

fn clean_title(raw: &str) -> String {
    raw.trim()
        .trim_matches('"')
        .trim_matches('\'')
        .chars()
        .take(TITLE_MAX_CHARS)
        .collect::<String>()
}

fn prettify_topic_id(id: &str) -> String {
    let words: Vec<String> = id
        .split(['-', '_'])
        .filter(|w| !w.is_empty())
        .map(|word| {
            let mut chars = word.chars();
            match chars.next() {
                Some(first) => {
                    let mut out = first.to_uppercase().collect::<String>();
                    out.push_str(chars.as_str());
                    out
                }
                None => String::new(),
            }
        })
        .collect();
    if words.is_empty() {
        id.to_string()
    } else {
        words.join(" ")
    }
}
