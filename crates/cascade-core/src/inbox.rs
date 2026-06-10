//! Inbox protocol — read/write/route messages under `.cascade/inbox/`.
//!
//! Messages follow the PCI format: `msg-YYYY-MM-DD-{slug}.md` with YAML
//! frontmatter (Subject, From, To, Priority, Type).

use cascade_types::error::{CascadeError, Result};
use std::path::{Path, PathBuf};

/// A single inbox message.
#[derive(Debug, Clone)]
pub struct InboxMessage {
    /// File path on disk.
    pub path: PathBuf,
    /// Raw markdown content including frontmatter.
    pub content: String,
}

/// List all unarchived messages in `cascade_dir/inbox/`.
pub async fn list(cascade_dir: &Path) -> Result<Vec<InboxMessage>> {
    let inbox = cascade_dir.join("inbox");
    if !inbox.exists() {
        return Ok(vec![]);
    }
    let mut messages = Vec::new();
    let mut entries = tokio::fs::read_dir(&inbox)
        .await
        .map_err(|e| CascadeError::Io {
            path: inbox.clone(),
            operation: "read_dir inbox",
            source: e,
        })?;
    while let Some(entry) = entries.next_entry().await.map_err(|e| CascadeError::Io {
        path: inbox.clone(),
        operation: "next_entry inbox",
        source: e,
    })? {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) == Some("md") {
            if let Ok(content) = tokio::fs::read_to_string(&path).await {
                messages.push(InboxMessage { path, content });
            }
        }
    }
    Ok(messages)
}

/// Write a message file to the target inbox directory.
pub async fn send(inbox_dir: &Path, slug: &str, content: &str) -> Result<PathBuf> {
    use std::time::{SystemTime, UNIX_EPOCH};
    tokio::fs::create_dir_all(inbox_dir).await.ok();
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let date = format_date(ts);
    let filename = format!("msg-{}-{}.md", date, slug);
    let path = inbox_dir.join(&filename);
    tokio::fs::write(&path, content)
        .await
        .map_err(|e| CascadeError::Io {
            path: path.clone(),
            operation: "write inbox message",
            source: e,
        })?;
    Ok(path)
}

/// Move a message to `archive/inbox/` under the cascade dir.
pub async fn archive(cascade_dir: &Path, msg_path: &Path) -> Result<()> {
    let archive = cascade_dir.join("archive").join("inbox");
    tokio::fs::create_dir_all(&archive).await.ok();
    let dest = archive.join(msg_path.file_name().unwrap_or_default());
    tokio::fs::rename(msg_path, &dest)
        .await
        .map_err(|e| CascadeError::Io {
            path: dest,
            operation: "archive inbox message",
            source: e,
        })
}

fn format_date(unix_secs: u64) -> String {
    // Minimal date formatter — avoids pulling in chrono for a stub.
    let days_since_epoch = unix_secs / 86400;
    // Approximate: good enough for filenames in this context.
    let year = 1970 + (days_since_epoch / 365);
    let month = (days_since_epoch % 365 / 30) + 1;
    let day = (days_since_epoch % 30) + 1;
    format!("{:04}-{:02}-{:02}", year, month.min(12), day.min(31))
}
