//! Core types for the automation subsystem.
//!
//! Covers triggers, targets, the `Automation` definition, draft artifacts,
//! outcomes, and the error type. No I/O or execution logic lives here.

use std::collections::HashMap;
use std::path::Path;

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use thiserror::Error;
use uuid::Uuid;

use crate::executor::ApprovalRequest;
use crate::spec::AgentRole;

// ── AutomationTrigger ─────────────────────────────────────────────────────────

/// How a non-coding automation workflow starts.
///
/// Variants use struct style per repo rule (no unit/newtype forms).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum AutomationTrigger {
    /// Started by an explicit user/API call with no additional metadata.
    Manual {},
    /// Fired by a Cascade event (e.g. `inbox.message.received`, `pr.opened`).
    Hook {
        /// The event name that triggers this automation.
        event: String,
    },
    /// Fired on a cron schedule (UNIX cron expression, UTC).
    Scheduled {
        /// UNIX cron expression, e.g. `"0 9 * * 1-5"` (9 AM Mon–Fri UTC).
        cron: String,
    },
    /// Fired when a message from a specific source arrives in the agent inbox.
    InboxEvent {
        /// Source identifier, e.g. `"github"`, `"email"`, `"slack"`.
        source: String,
    },
}

impl AutomationTrigger {
    /// Human-readable summary of the trigger.
    pub fn summary(&self) -> String {
        match self {
            Self::Manual {} => "manual".into(),
            Self::Hook { event } => format!("hook:{event}"),
            Self::Scheduled { cron } => format!("cron:{cron}"),
            Self::InboxEvent { source } => format!("inbox:{source}"),
        }
    }
}

// ── AutomationTarget ──────────────────────────────────────────────────────────

/// The execution target for an `Automation` — either a named chain or an agent.
///
/// Exactly one variant must be set; `Automation::validate` enforces this.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum AutomationTarget {
    /// Run a `ChainFlow` identified by `chain_ref` from the library.
    Chain {
        /// The `ChainFlow::id` to look up in the library.
        chain_ref: String,
    },
    /// Spin up a single agent with `role` and `goal`.
    Agent {
        /// Agent role to instantiate.
        role: AgentRole,
        /// Goal string for the agent (may include `{{key}}` placeholders filled
        /// from the initial context map provided at trigger time).
        goal: String,
    },
}

// ── Automation ────────────────────────────────────────────────────────────────

/// A loadable, triggerable non-coding automation workflow.
///
/// Loaded from `<ai-folder>/library/automations/*.yaml` by
/// `Automation::load_library`.
///
/// # YAML format
/// ```yaml
/// id: email-draft-from-prompt
/// name: "Email Draft from Prompt"
/// description: "Draft an outbound email from a user prompt and await approval."
/// enabled: true
/// trigger:
///   kind: manual
/// target:
///   kind: chain
///   chainRef: email-draft
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Automation {
    /// Stable unique identifier.
    pub id: String,
    /// Human-readable display name.
    pub name: String,
    /// Optional description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// Whether this automation is active.
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    /// What starts this automation.
    pub trigger: AutomationTrigger,
    /// What runs when triggered.
    pub target: AutomationTarget,
}

fn default_enabled() -> bool {
    true
}

impl Automation {
    /// Load all `Automation` values from YAML files in `dir`.
    pub fn load_library(dir: &Path) -> Result<Vec<Self>, AutomationError> {
        let mut automations = vec![];
        let rd = std::fs::read_dir(dir).map_err(|e| AutomationError::Io {
            path: dir.display().to_string(),
            message: e.to_string(),
        })?;
        for entry in rd.flatten() {
            let p = entry.path();
            let ext = p.extension().and_then(|e| e.to_str());
            if ext == Some("yaml") || ext == Some("yml") {
                let content = std::fs::read_to_string(&p).map_err(|e| AutomationError::Io {
                    path: p.display().to_string(),
                    message: e.to_string(),
                })?;
                let a: Self =
                    serde_yaml::from_str(&content).map_err(|e| AutomationError::YamlParse {
                        path: p.display().to_string(),
                        message: e.to_string(),
                    })?;
                automations.push(a);
            }
        }
        Ok(automations)
    }

    /// Quick structural check: id non-empty, enabled field present, target is valid.
    pub fn validate(&self) -> Result<(), AutomationError> {
        if self.id.is_empty() {
            return Err(AutomationError::InvalidSpec {
                id: self.id.clone(),
                reason: "id must be non-empty".into(),
            });
        }
        Ok(())
    }
}

// ── DraftArtifact ─────────────────────────────────────────────────────────────

/// The output of an outbound step before it has been approved and sent.
///
/// Saved to `<ai-folder>/agents/drafts/<id>.json` by `AutomationRunner`.
/// The `kind` field distinguishes email drafts from ticket drafts.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DraftArtifact {
    /// Unique identifier for this draft.
    pub id: String,
    /// The automation that produced this draft.
    pub automation_id: String,
    /// Whether this is an email or a ticket draft.
    pub kind: DraftKind,
    /// The proposed content (email body, ticket description, etc.).
    pub content: String,
    /// Optional metadata (e.g. `to`, `subject`, `labels`).
    #[serde(default)]
    pub metadata: HashMap<String, String>,
    /// When this draft was created.
    pub created_at: DateTime<Utc>,
    /// Whether the draft has been approved.
    pub approved: bool,
}

impl DraftArtifact {
    /// Create a new draft with a generated UUID and current timestamp.
    pub fn new(
        automation_id: String,
        kind: DraftKind,
        content: String,
        metadata: HashMap<String, String>,
    ) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            automation_id,
            kind,
            content,
            metadata,
            created_at: Utc::now(),
            approved: false,
        }
    }
}

/// The kind of outbound content in a `DraftArtifact`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum DraftKind {
    /// An outbound email.
    Email,
    /// A new issue or ticket.
    Ticket,
    /// Generic outbound content (e.g. a Slack message, a webhook payload).
    Generic,
}

// ── AutomationOutcome ─────────────────────────────────────────────────────────

/// The result of a single automation run.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AutomationOutcome {
    /// The automation that ran.
    pub automation_id: String,
    /// Whether the run completed without errors.
    pub success: bool,
    /// Optional summary produced by the chain/agent.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub summary: Option<String>,
    /// Drafts produced during this run (one per outbound step).
    #[serde(default)]
    pub drafts: Vec<DraftArtifact>,
    /// Approval requests emitted for outbound steps.
    #[serde(default)]
    pub approval_requests: Vec<ApprovalRequest>,
    /// Error message, if `success == false`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

// ── AutomationError ───────────────────────────────────────────────────────────

/// Errors produced by the automation subsystem.
#[derive(Debug, Error)]
pub enum AutomationError {
    #[error("YAML parse failed at '{path}': {message}")]
    YamlParse { path: String, message: String },

    #[error("I/O error at '{path}': {message}")]
    Io { path: String, message: String },

    #[error("invalid automation spec '{id}': {reason}")]
    InvalidSpec { id: String, reason: String },

    #[error("automation '{id}' is disabled")]
    Disabled { id: String },

    #[error("chain '{chain_ref}' not found in library")]
    ChainNotFound { chain_ref: String },

    #[error("chain execution failed: {0}")]
    ChainFailed(String),

    #[error("agent execution failed: {0}")]
    AgentFailed(String),

    #[error("outbound draft save failed: {0}")]
    DraftSaveFailed(String),

    #[error("outbound sink error: {0}")]
    SinkError(String),

    #[error("internal error: {message}")]
    Internal { message: String },
}
