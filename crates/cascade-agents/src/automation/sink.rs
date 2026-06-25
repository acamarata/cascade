//! Outbound sink trait and first-party implementations.
//!
//! `OutboundSink::send` is ONLY called after explicit approval — never by the
//! automation engine directly. Real provider integrations (Gmail, issue trackers)
//! are provider-plugin work and do not belong here.

use std::path::PathBuf;

use async_trait::async_trait;

use super::types::{AutomationError, DraftArtifact};

// ── OutboundSink ─────────────────────────────────────────────────────────────

/// Injectable outbound delivery mechanism.
///
/// Called ONLY after explicit approval — never by the automation engine
/// directly. Two first-party impls ship here:
/// - `NoopSink` — silently discards (tests / safe default)
/// - `DiskSink` — appends to a JSONL file (dev / integration testing)
///
/// Real Gmail / issue-tracker integrations are provider-plugin work.
#[async_trait]
pub trait OutboundSink: Send + Sync {
    /// Send (or simulate sending) the approved draft.
    ///
    /// Implementations MUST be idempotent — on retry, a duplicate `draft_id`
    /// must not result in a duplicate send.
    async fn send(&self, draft: &DraftArtifact) -> Result<(), AutomationError>;
}

// ── NoopSink ─────────────────────────────────────────────────────────────────

/// Outbound sink that silently discards all drafts.
///
/// Use in tests and as the safe default in environments without outbound
/// provider configuration.
pub struct NoopSink;

#[async_trait]
impl OutboundSink for NoopSink {
    async fn send(&self, _draft: &DraftArtifact) -> Result<(), AutomationError> {
        // WHY: Noop is the safe default — ensures no accidental outbound sends
        // during tests or when providers are not yet configured.
        Ok(())
    }
}

// ── DiskSink ─────────────────────────────────────────────────────────────────

/// Outbound sink that appends approved drafts as JSONL to a file on disk.
///
/// Useful for integration testing and development: inspection of the output
/// file verifies that send was called with the correct content.
pub struct DiskSink {
    /// Path to the JSONL output file.
    pub path: PathBuf,
}

impl DiskSink {
    /// Create a new `DiskSink` writing to `path`.
    pub fn new(path: impl Into<PathBuf>) -> Self {
        Self { path: path.into() }
    }
}

#[async_trait]
impl OutboundSink for DiskSink {
    async fn send(&self, draft: &DraftArtifact) -> Result<(), AutomationError> {
        use std::io::Write;
        let json = serde_json::to_string(draft).map_err(|e| AutomationError::Internal {
            message: format!("DiskSink serialize failed: {e}"),
        })?;
        let mut f = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&self.path)
            .map_err(|e| AutomationError::Io {
                path: self.path.display().to_string(),
                message: e.to_string(),
            })?;
        writeln!(f, "{json}").map_err(|e| AutomationError::Io {
            path: self.path.display().to_string(),
            message: e.to_string(),
        })?;
        Ok(())
    }
}
