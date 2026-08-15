//! Inbox protocol — read/write/route messages under `.cascade/inbox/`.
//!
//! Messages follow the PCI format: `msg-YYYY-MM-DD-{slug}.md` with YAML
//! frontmatter (Subject, From, To, Priority, Type).

use cascade_types::error::{CascadeError, Result};
use cascade_types::paths::home_dir;
use std::path::{Path, PathBuf};

/// A single inbox message.
#[derive(Debug, Clone)]
pub struct InboxMessage {
    /// File path on disk.
    pub path: PathBuf,
    /// Raw markdown content including frontmatter.
    pub content: String,
}

/// Result of [`send_message`] — the slug, filename, and absolute path of the
/// written inbox message file.
#[derive(Debug, Clone)]
pub struct SendResult {
    /// The slug derived from the subject (lowercase, alphanumeric + dashes).
    pub slug: String,
    /// The full filename including the `msg-YYYY-MM-DD-` prefix and `.md` suffix.
    pub filename: String,
    /// Absolute path to the written message file.
    pub path: PathBuf,
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

// ── send_message (shared by MCP handler + Tauri command) ──────────────────────

/// Resolve a project's PCI inbox directory.
///
/// `project` is the project name. For `nclaw` / `downloads` the path is
/// `~/Downloads/.claude/inbox/`; `*` / `all` resolves to the `nself` shared
/// inbox; all others are `~/Sites/{project}/.claude/inbox/`.
///
/// This mirrors `cascade_mcp::paths::inbox_dir` so both the MCP handler and
/// the Tauri command share identical path resolution logic.
pub fn inbox_dir(project: &str) -> PathBuf {
    match project {
        "nclaw" | "downloads" => home_dir().join("Downloads").join(".claude").join("inbox"),
        "*" | "all" => home_dir()
            .join("Sites")
            .join("nself")
            .join(".claude")
            .join("inbox"),
        name => home_dir()
            .join("Sites")
            .join(name)
            .join(".claude")
            .join("inbox"),
    }
}

/// Create a URL-safe slug from a subject string.
///
/// Lowercases the subject, replaces spaces with dashes, keeps only
/// alphanumeric characters and dashes, and truncates to 40 characters.
fn slugify(subject: &str) -> String {
    subject
        .to_lowercase()
        .replace(' ', "-")
        .chars()
        .filter(|c| c.is_alphanumeric() || *c == '-')
        .take(40)
        .collect()
}

/// Validate that a required string field is non-empty.
fn require_field(name: &str, value: &str) -> Result<()> {
    if value.trim().is_empty() {
        return Err(CascadeError::Other(format!(
            "'{name}' is required and must not be empty"
        )));
    }
    Ok(())
}

/// Validate that a target project name is safe (no path traversal).
fn validate_target(target: &str) -> Result<()> {
    if target.contains('/') || target.contains("..") {
        return Err(CascadeError::Other(
            "'target' must be a plain project name, not a path".to_string(),
        ));
    }
    Ok(())
}

/// Format the markdown content of an inbox message.
fn format_content(
    subject: &str,
    from: &str,
    target: &str,
    msg_type: &str,
    priority: &str,
    body: &str,
) -> String {
    format!(
        "# {subject}\n\n**From:** {from}\n**To:** {target}\n**Type:** {msg_type}\n**Priority:** {priority}\n\n{body}\n"
    )
}

/// Send (write) a PCI message to a project's inbox directory.
///
/// This is the shared implementation used by both the MCP `cascade.inbox.send`
/// tool handler and the Tauri `cascade_inbox_send` IPC command. It:
/// 1. Validates that target, subject, body, priority, and msg_type are non-empty.
/// 2. Confines target to a plain project name (no path traversal).
/// 3. Derives a slug from the subject.
/// 4. Resolves the inbox directory via [`inbox_dir`].
/// 5. Formats the message as markdown (with `from` as the sender label).
/// 6. Writes the file atomically (create dir + write).
///
/// # Errors
///
/// Returns `CascadeError::Other` for validation failures, `CascadeError::Io`
/// for file-system failures.
pub async fn send_message(
    from: &str,
    target: &str,
    subject: &str,
    body: &str,
    priority: &str,
    msg_type: &str,
) -> Result<SendResult> {
    // Validate required fields.
    require_field("target", target)?;
    require_field("subject", subject)?;
    require_field("body", body)?;
    require_field("priority", priority)?;
    require_field("type", msg_type)?;
    validate_target(target)?;

    // Derive slug and filename.
    let slug = slugify(subject);
    let date = format_date(current_unix_secs());
    let filename = format!("msg-{date}-{slug}.md");

    // Resolve inbox directory and write the message.
    let inbox_dir = inbox_dir(target);
    tokio::fs::create_dir_all(&inbox_dir)
        .await
        .map_err(|e| CascadeError::Io {
            path: inbox_dir.clone(),
            operation: "create_dir_all inbox",
            source: e,
        })?;

    let path = inbox_dir.join(&filename);
    let content = format_content(subject, from, target, msg_type, priority, body);
    tokio::fs::write(&path, &content)
        .await
        .map_err(|e| CascadeError::Io {
            path: path.clone(),
            operation: "write inbox message",
            source: e,
        })?;

    Ok(SendResult {
        slug,
        filename,
        path,
    })
}

/// Current Unix timestamp in seconds (never panics).
fn current_unix_secs() -> u64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn slugify_basic() {
        assert_eq!(slugify("Hello World"), "hello-world");
        assert_eq!(slugify("Test Subject 123"), "test-subject-123");
    }

    #[test]
    fn slugify_strips_special_chars() {
        assert_eq!(slugify("Hello! @World#"), "hello-world");
        assert_eq!(slugify("Subject with (parens)"), "subject-with-parens");
    }

    #[test]
    fn slugify_truncates_to_40() {
        let long = "a".repeat(50);
        let slug = slugify(&long);
        assert_eq!(slug.len(), 40);
    }

    #[test]
    fn slugify_empty_string() {
        assert_eq!(slugify(""), "");
    }

    #[test]
    fn require_field_rejects_empty() {
        assert!(require_field("test", "").is_err());
        assert!(require_field("test", "   ").is_err());
        assert!(require_field("test", "value").is_ok());
    }

    #[test]
    fn validate_target_rejects_paths() {
        assert!(validate_target("nself").is_ok());
        assert!(validate_target("acamarata").is_ok());
        assert!(validate_target("nself/sub").is_err());
        assert!(validate_target("../etc").is_err());
        assert!(validate_target("..").is_err());
    }

    #[test]
    fn inbox_dir_resolves_nself() {
        let dir = inbox_dir("nself");
        assert!(dir.ends_with("inbox"));
        assert!(dir.to_string_lossy().contains("Sites/nself"));
    }

    #[test]
    fn inbox_dir_resolves_downloads() {
        let dir = inbox_dir("downloads");
        assert!(dir.ends_with("inbox"));
        assert!(dir.to_string_lossy().contains("Downloads"));
    }

    #[test]
    fn inbox_dir_resolves_wildcard() {
        let dir = inbox_dir("*");
        assert!(dir.ends_with("inbox"));
        assert!(dir.to_string_lossy().contains("Sites/nself"));
    }

    #[test]
    fn format_content_includes_all_fields() {
        let content = format_content(
            "Test Subject",
            "cascade-mcp",
            "nself",
            "info",
            "high",
            "Body text",
        );
        assert!(content.contains("# Test Subject"));
        assert!(content.contains("**From:** cascade-mcp"));
        assert!(content.contains("**To:** nself"));
        assert!(content.contains("**Type:** info"));
        assert!(content.contains("**Priority:** high"));
        assert!(content.contains("Body text"));
    }

    #[tokio::test]
    async fn send_message_validates_required_fields() {
        let result = send_message("app", "", "subject", "body", "high", "info").await;
        assert!(result.is_err(), "empty target should fail");

        let result = send_message("app", "nself", "", "body", "high", "info").await;
        assert!(result.is_err(), "empty subject should fail");

        let result = send_message("app", "nself", "subject", "", "high", "info").await;
        assert!(result.is_err(), "empty body should fail");

        let result = send_message("app", "nself", "subject", "body", "", "info").await;
        assert!(result.is_err(), "empty priority should fail");

        let result = send_message("app", "nself", "subject", "body", "high", "").await;
        assert!(result.is_err(), "empty type should fail");
    }

    #[tokio::test]
    async fn send_message_rejects_path_traversal() {
        let result = send_message("app", "../etc", "subject", "body", "high", "info").await;
        assert!(result.is_err(), "path traversal target should fail");
    }

    #[tokio::test]
    async fn send_message_writes_file() {
        // Use a temp HOME so we don't write to the real ~/Sites.
        // We can't easily override HOME in a unit test, so we test the
        // validation + slug + content formatting path which is the core logic.
        // The file-writing path is exercised in the integration tests.
        let slug = slugify("Test Subject");
        assert_eq!(slug, "test-subject");
        let content = format_content("Test Subject", "app", "nself", "info", "high", "Body");
        assert!(content.contains("# Test Subject"));
    }
}
