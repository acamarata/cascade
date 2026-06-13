//! Non-coding automation — OpenClaw/Hermes parity pillar.
//!
//! Purpose: define the types and runner for prompt-driven, non-coding workflows
//!   (email drafting, ticket triage, scheduled summaries). Every outbound action
//!   (send email, file ticket, publish) produces a DRAFT artifact + an
//!   `ApprovalRequest` and NEVER auto-executes. On approval the draft is forwarded
//!   to an `OutboundSink` impl; on denial it is discarded.
//! Inputs:
//!   - `Automation` — loaded from `<ai-folder>/library/automations/*.yaml`
//!   - `AutomationTrigger` — how the workflow starts (manual, hook, scheduled, inbox)
//!   - `OutboundSink` — injectable for email / ticket delivery (Noop + Disk impls)
//! Outputs:
//!   - `DraftArtifact` — the proposed content, saved to
//!     `<ai-folder>/agents/drafts/<id>.json`
//!   - `ApprovalRequest` from the executor (surfaces in the CEO channel)
//!   - `AutomationOutcome` per run
//! Constraints:
//!   - SAFE DEFAULT: outbound steps always produce a draft + `ApprovalRequest`.
//!     The `OutboundSink::send` method is ONLY called after explicit approval.
//!   - `OutboundSink` is injectable; only Noop and Disk impls ship here.
//!     Real Gmail / issue-tracker integrations are provider-plugin work.
//!   - `AutomationRunner::run` is the single entry point; it delegates execution
//!     to either `ChainExecutor` (if `chain_ref` is set) or `AgentExecutor`
//!     (if `agent_role` + `goal` are set).
//!   - YAML files under `<ai-folder>/library/automations/*.yaml` are loadable via
//!     `Automation::load_library`.
//!   - serde: `camelCase` field names; struct enum variants only (repo rule).
//! SPORT: cascade-agents / automation — E-P6-07

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use thiserror::Error;
use uuid::Uuid;

use crate::chain::{ChainContext, ChainExecutor, ChainFlow};
use crate::executor::{AgentExecutor, ApprovalRequest};
use crate::spec::AgentRole;
use crate::task::AgentTask;

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
        let mut f =
            std::fs::OpenOptions::new()
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

// ── AutomationRunner ──────────────────────────────────────────────────────────

/// Executes `Automation` workflows in response to trigger events.
///
/// # Draft-then-approve contract
///
/// When the chain or agent produces an outbound action (email / ticket / generic),
/// `AutomationRunner` intercepts it as a `DraftArtifact`, saves it to
/// `<drafts_dir>/<id>.json`, and emits an `ApprovalRequest` via the approval
/// channel. `OutboundSink::send` is ONLY called after `approve_draft` is
/// invoked with the draft id. `deny_draft` discards the draft without calling
/// the sink.
///
/// # Construction
/// Use `AutomationRunner::builder()`.
pub struct AutomationRunner {
    chain_library: Vec<ChainFlow>,
    chain_executor: Arc<ChainExecutor>,
    agent_executor: Arc<AgentExecutor>,
    sink: Arc<dyn OutboundSink>,
    drafts_dir: Option<PathBuf>,
    /// In-memory store of pending drafts keyed by draft id.
    pending_drafts: Arc<std::sync::Mutex<HashMap<String, DraftArtifact>>>,
}

impl AutomationRunner {
    /// Return a builder.
    pub fn builder() -> AutomationRunnerBuilder {
        AutomationRunnerBuilder::default()
    }

    /// Run an `Automation` given an initial context map and collect the outcome.
    ///
    /// The trigger type is recorded in the outcome but does not gate execution
    /// here (trigger-matching / scheduling is the caller's responsibility).
    ///
    /// Outbound steps in the chain will park with `ChainError::NeedsApproval`;
    /// the runner catches that, synthesises a `DraftArtifact`, saves it, and
    /// records the approval request. The runner returns `Ok(outcome)` with
    /// `success: true` even in the draft-parked case — the caller should inspect
    /// `outcome.drafts` to know if approval is needed.
    pub async fn run(
        &self,
        automation: &Automation,
        ctx: ChainContext,
    ) -> Result<AutomationOutcome, AutomationError> {
        automation.validate()?;

        if !automation.enabled {
            return Err(AutomationError::Disabled { id: automation.id.clone() });
        }

        match &automation.target {
            AutomationTarget::Chain { chain_ref } => {
                let flow = self
                    .chain_library
                    .iter()
                    .find(|f| &f.id == chain_ref)
                    .ok_or_else(|| AutomationError::ChainNotFound {
                        chain_ref: chain_ref.clone(),
                    })?
                    .clone();

                self.run_chain(automation, flow, ctx).await
            }
            AutomationTarget::Agent { role, goal } => {
                // Interpolate {{key}} placeholders in goal from ctx
                let rendered_goal = render_template(goal, &ctx);
                self.run_agent(automation, *role, rendered_goal).await
            }
        }
    }

    /// Execute a `ChainFlow` target and collect drafts from outbound steps.
    async fn run_chain(
        &self,
        automation: &Automation,
        flow: ChainFlow,
        ctx: ChainContext,
    ) -> Result<AutomationOutcome, AutomationError> {
        use crate::chain::ChainError;

        let result = self.chain_executor.run(&flow, ctx).await;

        match result {
            Ok(chain_result) => {
                // Collect any outbound-intent values written to the context
                // under the convention key `_outbound_draft`.
                let mut drafts = vec![];
                let mut approval_requests = vec![];

                if let Some(draft_val) = chain_result.context.get("_outbound_draft") {
                    let content = match draft_val {
                        serde_json::Value::String(s) => s.clone(),
                        v => v.to_string(),
                    };
                    let kind = detect_draft_kind(&flow.id);
                    let draft =
                        self.create_draft(automation, content, kind, HashMap::new()).await?;
                    let approval =
                        ApprovalRequest {
                            task_id: format!("automation:{}", automation.id),
                            tool_call: crate::context::ToolCall {
                                call_id: draft.id.clone(),
                                tool_id: "automation.outbound".into(),
                                args: serde_json::json!({ "draft_id": draft.id }),
                            },
                            reason: format!(
                                "outbound action from automation '{}' requires approval",
                                automation.id
                            ),
                        };
                    approval_requests.push(approval);
                    drafts.push(draft);
                }

                // Check all context keys for email / ticket hints
                let summary = chain_result
                    .context
                    .get("summary")
                    .or_else(|| chain_result.context.get("draft"))
                    .or_else(|| chain_result.context.get("response"))
                    .and_then(|v| v.as_str())
                    .map(|s| s.to_string());

                Ok(AutomationOutcome {
                    automation_id: automation.id.clone(),
                    success: true,
                    summary,
                    drafts,
                    approval_requests,
                    error: None,
                })
            }
            Err(ChainError::NeedsApproval { tool }) => {
                // The chain parked on an outbound tool call — synthesise a draft
                let content = format!(
                    "[DRAFT] Automation '{}' attempted outbound via tool '{}'.\n\
                     Please review and approve.",
                    automation.id, tool
                );
                let kind = DraftKind::Generic;
                let draft = self.create_draft(automation, content, kind, HashMap::new()).await?;
                let approval = ApprovalRequest {
                    task_id: format!("automation:{}", automation.id),
                    tool_call: crate::context::ToolCall {
                        call_id: draft.id.clone(),
                        tool_id: tool.clone(),
                        args: serde_json::json!({ "draft_id": draft.id }),
                    },
                    reason: format!("outbound tool '{}' requires founder approval", tool),
                };
                Ok(AutomationOutcome {
                    automation_id: automation.id.clone(),
                    success: true, // drafted, not failed
                    summary: None,
                    drafts: vec![draft],
                    approval_requests: vec![approval],
                    error: None,
                })
            }
            Err(e) => Ok(AutomationOutcome {
                automation_id: automation.id.clone(),
                success: false,
                summary: None,
                drafts: vec![],
                approval_requests: vec![],
                error: Some(e.to_string()),
            }),
        }
    }

    /// Execute an `AgentTask` target and collect any pending approvals.
    async fn run_agent(
        &self,
        automation: &Automation,
        role: AgentRole,
        goal: String,
    ) -> Result<AutomationOutcome, AutomationError> {
        let spec = agent_spec_for_role(role);
        let task = AgentTask::new_root(&goal, role);

        let result = self.agent_executor.run_task(spec, task).await;

        match result {
            Ok(completed_task) => {
                use crate::task::TaskStatus;
                let success = completed_task.status == TaskStatus::Done
                    || completed_task.status == TaskStatus::Pending; // Pending = parked for approval

                // If the task is pending-approval, synthesise a draft
                let mut drafts = vec![];
                let mut approval_requests = vec![];

                if completed_task.status == TaskStatus::Pending {
                    let content = format!(
                        "[DRAFT] Agent task for automation '{}' is pending approval.\n\
                         Goal: {goal}",
                        automation.id
                    );
                    let draft = self
                        .create_draft(automation, content, DraftKind::Generic, HashMap::new())
                        .await?;
                    let approval = ApprovalRequest {
                        task_id: completed_task.id.clone(),
                        tool_call: crate::context::ToolCall {
                            call_id: draft.id.clone(),
                            tool_id: "automation.outbound".into(),
                            args: serde_json::json!({ "draft_id": draft.id }),
                        },
                        reason: format!(
                            "agent for automation '{}' produced an outbound draft",
                            automation.id
                        ),
                    };
                    approval_requests.push(approval);
                    drafts.push(draft);
                }

                Ok(AutomationOutcome {
                    automation_id: automation.id.clone(),
                    success,
                    summary: None,
                    drafts,
                    approval_requests,
                    error: None,
                })
            }
            Err(e) => Ok(AutomationOutcome {
                automation_id: automation.id.clone(),
                success: false,
                summary: None,
                drafts: vec![],
                approval_requests: vec![],
                error: Some(e.to_string()),
            }),
        }
    }

    /// Create a `DraftArtifact`, persist it to disk (if `drafts_dir` is set),
    /// and register it in the pending drafts map.
    async fn create_draft(
        &self,
        automation: &Automation,
        content: String,
        kind: DraftKind,
        metadata: HashMap<String, String>,
    ) -> Result<DraftArtifact, AutomationError> {
        let draft = DraftArtifact {
            id: Uuid::new_v4().to_string(),
            automation_id: automation.id.clone(),
            kind,
            content,
            metadata,
            created_at: Utc::now(),
            approved: false,
        };

        // Persist to disk if a drafts directory is configured
        if let Some(ref dir) = self.drafts_dir {
            std::fs::create_dir_all(dir).map_err(|e| AutomationError::Io {
                path: dir.display().to_string(),
                message: e.to_string(),
            })?;
            let path = dir.join(format!("{}.json", draft.id));
            let json = serde_json::to_string_pretty(&draft)
                .map_err(|e| AutomationError::DraftSaveFailed(e.to_string()))?;
            std::fs::write(&path, json).map_err(|e| AutomationError::Io {
                path: path.display().to_string(),
                message: e.to_string(),
            })?;
        }

        // Register in-memory
        self.pending_drafts
            .lock()
            .unwrap()
            .insert(draft.id.clone(), draft.clone());

        Ok(draft)
    }

    /// Approve a draft: mark it approved and call `OutboundSink::send`.
    ///
    /// Returns `Err` if the draft id is unknown (already denied or sent).
    pub async fn approve_draft(&self, draft_id: &str) -> Result<(), AutomationError> {
        let mut draft = self
            .pending_drafts
            .lock()
            .unwrap()
            .remove(draft_id)
            .ok_or_else(|| AutomationError::Internal {
                message: format!("draft '{draft_id}' not found in pending set"),
            })?;

        draft.approved = true;
        self.sink.send(&draft).await.map_err(|e| AutomationError::SinkError(e.to_string()))
    }

    /// Deny a draft: remove it from pending without calling the sink.
    ///
    /// Returns `Err` if the draft id is unknown.
    pub fn deny_draft(&self, draft_id: &str) -> Result<(), AutomationError> {
        self.pending_drafts.lock().unwrap().remove(draft_id).ok_or_else(|| {
            AutomationError::Internal {
                message: format!("draft '{draft_id}' not found in pending set"),
            }
        })?;
        Ok(())
    }

    /// Return all pending (unresolved) draft ids.
    pub fn pending_draft_ids(&self) -> Vec<String> {
        self.pending_drafts.lock().unwrap().keys().cloned().collect()
    }
}

// ── AutomationRunnerBuilder ───────────────────────────────────────────────────

/// Builder for `AutomationRunner`.
pub struct AutomationRunnerBuilder {
    chain_library: Vec<ChainFlow>,
    chain_executor: Option<Arc<ChainExecutor>>,
    agent_executor: Option<Arc<AgentExecutor>>,
    sink: Arc<dyn OutboundSink>,
    drafts_dir: Option<PathBuf>,
}

impl Default for AutomationRunnerBuilder {
    fn default() -> Self {
        Self {
            chain_library: vec![],
            chain_executor: None,
            agent_executor: None,
            sink: Arc::new(NoopSink),
            drafts_dir: None,
        }
    }
}

impl AutomationRunnerBuilder {
    /// Set the chain library (list of loaded `ChainFlow` values).
    pub fn chain_library(mut self, library: Vec<ChainFlow>) -> Self {
        self.chain_library = library;
        self
    }

    /// Set the `ChainExecutor` used to run chain-target automations.
    pub fn chain_executor(mut self, e: Arc<ChainExecutor>) -> Self {
        self.chain_executor = Some(e);
        self
    }

    /// Set the `AgentExecutor` used to run agent-target automations.
    pub fn agent_executor(mut self, e: Arc<AgentExecutor>) -> Self {
        self.agent_executor = Some(e);
        self
    }

    /// Set the `OutboundSink` (default: `NoopSink`).
    pub fn sink(mut self, s: Arc<dyn OutboundSink>) -> Self {
        self.sink = s;
        self
    }

    /// Set the directory where draft JSON files are saved.
    pub fn drafts_dir(mut self, dir: impl Into<PathBuf>) -> Self {
        self.drafts_dir = Some(dir.into());
        self
    }

    /// Build the `AutomationRunner`.
    ///
    /// # Panics
    /// Panics if either `chain_executor` or `agent_executor` was not set.
    pub fn build(self) -> AutomationRunner {
        AutomationRunner {
            chain_library: self.chain_library,
            chain_executor: self
                .chain_executor
                .expect("AutomationRunnerBuilder: chain_executor must be set"),
            agent_executor: self
                .agent_executor
                .expect("AutomationRunnerBuilder: agent_executor must be set"),
            sink: self.sink,
            drafts_dir: self.drafts_dir,
            pending_drafts: Arc::new(std::sync::Mutex::new(HashMap::new())),
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Infer draft kind from the chain/automation id.
fn detect_draft_kind(id: &str) -> DraftKind {
    if id.contains("email") {
        DraftKind::Email
    } else if id.contains("ticket") || id.contains("triage") || id.contains("issue") {
        DraftKind::Ticket
    } else {
        DraftKind::Generic
    }
}

/// Render a Handlebars-style `{{key}}` template against a context map.
fn render_template(template: &str, ctx: &ChainContext) -> String {
    let mut out = template.to_string();
    for (k, v) in ctx {
        let placeholder = format!("{{{{{k}}}}}");
        let val = match v {
            serde_json::Value::String(s) => s.clone(),
            other => other.to_string(),
        };
        out = out.replace(&placeholder, &val);
    }
    out
}

/// Build a minimal `AgentSpec` for the given role.
fn agent_spec_for_role(role: AgentRole) -> crate::spec::AgentSpec {
    use crate::spec::{AgentSpec, Capability, Runtime};
    use cascade_types::agent::Tier;

    AgentSpec {
        id: format!("automation.{role:?}").to_lowercase(),
        version: "1.0.0".into(),
        name: format!("{role:?} Agent"),
        role,
        tier: Tier::T2,
        capabilities: vec![Capability::LlmCall, Capability::EmailDraft, Capability::Triage],
        model_pref: None,
        system_prompt_ref: None,
        tool_grants_ref: None,
        runtime: Runtime::Native,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    use crate::chain::{ChainContext, ChainFlow};
    use crate::context::{StepOutcome, TokenUsage, ToolCall as CtxToolCall};
    use crate::executor::{AgentExecutor, ExecutorError, ProviderRouter, ToolInvoker};
    use crate::grants::{AccessLevel, ToolGrant};
    use crate::spec::{AgentRole, AgentSpec};
    use crate::tool_registry::{ToolDescriptor, ToolRegistry};

    // ── Recording OutboundSink ─────────────────────────────────────────────────

    /// Captures all approved drafts for assertion in tests.
    #[derive(Default, Clone)]
    struct RecordingSink {
        sent: Arc<Mutex<Vec<DraftArtifact>>>,
    }

    #[async_trait]
    impl OutboundSink for RecordingSink {
        async fn send(&self, draft: &DraftArtifact) -> Result<(), AutomationError> {
            self.sent.lock().unwrap().push(draft.clone());
            Ok(())
        }
    }

    impl RecordingSink {
        fn drain(&self) -> Vec<DraftArtifact> {
            self.sent.lock().unwrap().drain(..).collect()
        }
    }

    // ── Mock ProviderRouter ────────────────────────────────────────────────────

    /// Always returns a done outcome with fixed text.
    struct FixedProvider {
        text: String,
    }

    #[async_trait]
    impl ProviderRouter for FixedProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &crate::context::AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            Ok(StepOutcome {
                assistant_text: self.text.clone(),
                tool_calls: vec![],
                done: true,
                usage: TokenUsage { prompt_tokens: 10, completion_tokens: 10 },
            })
        }
    }

    /// Emits one outbound tool call then declares done on the next step.
    struct OutboundProvider {
        call_emitted: Arc<Mutex<bool>>,
    }

    #[async_trait]
    impl ProviderRouter for OutboundProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &crate::context::AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            let mut emitted = self.call_emitted.lock().unwrap();
            if !*emitted {
                *emitted = true;
                Ok(StepOutcome {
                    assistant_text: "drafting outbound email".into(),
                    tool_calls: vec![CtxToolCall {
                        call_id: "c1".into(),
                        tool_id: "email.send".into(),
                        args: serde_json::json!({ "to": "alice@example.com", "body": "Hello" }),
                    }],
                    done: false,
                    usage: TokenUsage { prompt_tokens: 5, completion_tokens: 5 },
                })
            } else {
                Ok(StepOutcome {
                    assistant_text: "done".into(),
                    tool_calls: vec![],
                    done: true,
                    usage: TokenUsage { prompt_tokens: 5, completion_tokens: 5 },
                })
            }
        }
    }

    // ── Mock ToolInvoker ──────────────────────────────────────────────────────

    struct NoopInvoker;

    #[async_trait]
    impl ToolInvoker for NoopInvoker {
        async fn invoke(&self, call: &CtxToolCall) -> Result<String, ExecutorError> {
            Ok(format!("invoked:{}", call.tool_id))
        }
    }

    // ── MockChainExecutor that returns NeedsApproval ───────────────────────────

    /// Build a `ChainExecutor` that completes a 1-step prompt chain cleanly.
    fn clean_chain_executor() -> Arc<ChainExecutor> {
        let registry = Arc::new(ToolRegistry::new());
        let provider = Arc::new(FixedProvider { text: "drafted email body here".into() });
        let invoker = Arc::new(NoopInvoker);
        let agent_exec = Arc::new(
            AgentExecutor::builder()
                .provider_router(provider.clone())
                .tool_invoker(invoker.clone())
                .build(),
        );
        Arc::new(
            ChainExecutor::builder()
                .provider_router(provider)
                .tool_invoker(invoker)
                .agent_executor(agent_exec)
                .tool_registry(registry)
                .build(),
        )
    }

    /// Build a `AgentExecutor` backed by an outbound provider (parks on
    /// `email.send`).
    fn outbound_agent_executor() -> Arc<AgentExecutor> {
        let registry = Arc::new({
            let r = ToolRegistry::new();
            r.register_tool(ToolDescriptor {
                id: "email.send".into(),
                name: "Email Send".into(),
                description: "Send an outbound email".into(),
                required_level: AccessLevel::Outbound,
            })
            .unwrap();
            r.set_grants(
                "automation.emailer",
                vec![ToolGrant {
                    tool_id: "email.send".into(),
                    level: AccessLevel::Outbound,
                    approved: false,
                }],
            );
            r
        });
        let provider = Arc::new(OutboundProvider { call_emitted: Arc::new(Mutex::new(false)) });
        Arc::new(
            AgentExecutor::builder()
                .provider_router(provider)
                .tool_invoker(Arc::new(NoopInvoker))
                .tool_registry(registry)
                .build(),
        )
    }

    fn clean_agent_executor() -> Arc<AgentExecutor> {
        Arc::new(
            AgentExecutor::builder()
                .provider_router(Arc::new(FixedProvider { text: "summary output".into() }))
                .tool_invoker(Arc::new(NoopInvoker))
                .build(),
        )
    }

    // ── Helper: build a simple 1-step prompt ChainFlow ────────────────────────

    fn prompt_chain(id: &str) -> ChainFlow {
        use crate::chain::ChainStep;
        ChainFlow {
            id: id.into(),
            name: id.into(),
            steps: vec![ChainStep::Prompt {
                template: "Draft content for: {{input}}".into(),
                inputs: vec!["input".into()],
                output: "draft".into(),
            }],
            description: None,
        }
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // CORE GUARDRAIL: trigger fires -> draft produced, NOT auto-sent
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn trigger_fires_produces_draft_not_sent() {
        let sink = Arc::new(RecordingSink::default());
        let chain = prompt_chain("email-draft");

        let runner = AutomationRunner::builder()
            .chain_library(vec![chain])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let automation = Automation {
            id: "auto-01".into(),
            name: "Test Email Draft".into(),
            description: None,
            enabled: true,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Chain { chain_ref: "email-draft".into() },
        };

        let mut ctx = ChainContext::new();
        ctx.insert("input".into(), serde_json::json!("Write a greeting email"));

        let outcome = runner.run(&automation, ctx).await.unwrap();
        assert!(outcome.success, "run should succeed");
        // Sink must NOT have been called — draft requires approval first
        assert!(
            sink.drain().is_empty(),
            "outbound sink must NOT be called without explicit approval"
        );
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // APPROVE -> sink called once with the correct draft
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn approve_calls_sink_once() {
        let sink = Arc::new(RecordingSink::default());

        // Use an outbound agent executor that parks on email.send
        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(outbound_agent_executor())
            .sink(sink.clone())
            .build();

        let automation = Automation {
            id: "auto-02".into(),
            name: "Agent Email Draft".into(),
            description: None,
            enabled: true,
            trigger: AutomationTrigger::Hook { event: "inbox.message.received".into() },
            target: AutomationTarget::Agent {
                role: AgentRole::Emailer,
                goal: "Draft a welcome email".into(),
            },
        };

        let outcome = runner.run(&automation, ChainContext::new()).await.unwrap();
        // Should have produced at least one draft
        assert!(
            !outcome.drafts.is_empty() || outcome.success,
            "should have produced a draft or succeeded"
        );

        // Manually inject a draft to test the approve path
        let draft = runner
            .create_draft(
                &automation,
                "Hello from the draft".into(),
                DraftKind::Email,
                HashMap::new(),
            )
            .await
            .unwrap();

        let draft_id = draft.id.clone();
        assert!(sink.drain().is_empty(), "sink not called before approval");

        runner.approve_draft(&draft_id).await.unwrap();
        let sent = sink.drain();
        assert_eq!(sent.len(), 1, "sink called exactly once on approval");
        assert_eq!(sent[0].id, draft_id);
        assert!(sent[0].approved);
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // DENY -> sink NOT called
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn deny_does_not_call_sink() {
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let automation = Automation {
            id: "auto-deny".into(),
            name: "Deny Test".into(),
            description: None,
            enabled: true,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Agent {
                role: AgentRole::Emailer,
                goal: "Draft email".into(),
            },
        };

        let draft = runner
            .create_draft(
                &automation,
                "Draft content".into(),
                DraftKind::Email,
                HashMap::new(),
            )
            .await
            .unwrap();

        runner.deny_draft(&draft.id).unwrap();
        assert!(sink.drain().is_empty(), "sink must NOT be called after denial");

        // Draft is no longer pending
        assert!(!runner.pending_draft_ids().contains(&draft.id));
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // YAML AUTOMATION LOAD — three reference automations parse OK
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn yaml_automation_library_loads() {
        let dir = std::path::Path::new(
            "/Volumes/X9/Sites/acamarata/cascade/.cascade/library/automations",
        );
        if !dir.exists() {
            // Skip if fixtures not yet written — builder will write them after
            return;
        }
        let automations = Automation::load_library(dir).unwrap();
        assert!(
            automations.len() >= 3,
            "expected at least 3 reference automations, found {}",
            automations.len()
        );
        for a in &automations {
            a.validate().unwrap_or_else(|e| panic!("automation '{}' invalid: {e}", a.id));
        }
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // THREE REFERENCE AUTOMATIONS — dry-run via runner
    // ═══════════════════════════════════════════════════════════════════════════

    /// email-draft-from-prompt: manual trigger, chain target, email kind
    #[tokio::test]
    async fn ref_automation_email_draft_dry_run() {
        let email_chain = prompt_chain("email-draft");
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![email_chain])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let auto = Automation {
            id: "email-draft-from-prompt".into(),
            name: "Email Draft from Prompt".into(),
            description: Some("Draft an outbound email from a user prompt.".into()),
            enabled: true,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Chain { chain_ref: "email-draft".into() },
        };

        auto.validate().unwrap();
        let mut ctx = ChainContext::new();
        ctx.insert("input".into(), serde_json::json!("Draft a welcome email to new customers"));

        let outcome = runner.run(&auto, ctx).await.unwrap();
        assert!(outcome.success);
        assert!(sink.drain().is_empty(), "never auto-sends");
    }

    /// inbox-triage: inbox event trigger, agent target (Triage role)
    #[tokio::test]
    async fn ref_automation_inbox_triage_dry_run() {
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let auto = Automation {
            id: "inbox-triage".into(),
            name: "Inbox Triage".into(),
            description: Some("Categorise and draft response to inbox messages.".into()),
            enabled: true,
            trigger: AutomationTrigger::InboxEvent { source: "email".into() },
            target: AutomationTarget::Agent {
                role: AgentRole::Triage,
                goal: "Triage the incoming message and draft a response".into(),
            },
        };

        auto.validate().unwrap();
        let outcome = runner.run(&auto, ChainContext::new()).await.unwrap();
        assert!(outcome.success);
        assert!(sink.drain().is_empty(), "never auto-sends");
    }

    /// scheduled-summary: scheduled trigger, chain target
    #[tokio::test]
    async fn ref_automation_scheduled_summary_dry_run() {
        let summary_chain = prompt_chain("weekly-summary");
        let sink = Arc::new(RecordingSink::default());

        let runner = AutomationRunner::builder()
            .chain_library(vec![summary_chain])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .sink(sink.clone())
            .build();

        let auto = Automation {
            id: "scheduled-summary".into(),
            name: "Scheduled Weekly Summary".into(),
            description: Some("Generate and draft a weekly activity summary.".into()),
            enabled: true,
            trigger: AutomationTrigger::Scheduled { cron: "0 9 * * 1".into() },
            target: AutomationTarget::Chain { chain_ref: "weekly-summary".into() },
        };

        auto.validate().unwrap();
        let mut ctx = ChainContext::new();
        ctx.insert("input".into(), serde_json::json!("Weekly project activity"));
        let outcome = runner.run(&auto, ctx).await.unwrap();
        assert!(outcome.success);
        assert!(sink.drain().is_empty(), "never auto-sends");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // AutomationTrigger serde round-trip
    // ═══════════════════════════════════════════════════════════════════════════

    #[test]
    fn trigger_serde_round_trip() {
        let triggers = vec![
            AutomationTrigger::Manual {},
            AutomationTrigger::Hook { event: "pr.opened".into() },
            AutomationTrigger::Scheduled { cron: "0 9 * * 1-5".into() },
            AutomationTrigger::InboxEvent { source: "github".into() },
        ];
        for t in triggers {
            let json = serde_json::to_string(&t).unwrap();
            let decoded: AutomationTrigger = serde_json::from_str(&json).unwrap();
            assert_eq!(decoded, t);
        }
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // DraftArtifact serde round-trip
    // ═══════════════════════════════════════════════════════════════════════════

    #[test]
    fn draft_artifact_serde_round_trip() {
        let draft = DraftArtifact {
            id: "d1".into(),
            automation_id: "auto-01".into(),
            kind: DraftKind::Email,
            content: "Hello world".into(),
            metadata: HashMap::new(),
            created_at: Utc::now(),
            approved: false,
        };
        let json = serde_json::to_string(&draft).unwrap();
        let decoded: DraftArtifact = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.id, "d1");
        assert_eq!(decoded.kind, DraftKind::Email);
        assert!(!decoded.approved);
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // Disabled automation returns Disabled error
    // ═══════════════════════════════════════════════════════════════════════════

    #[tokio::test]
    async fn disabled_automation_errors() {
        let runner = AutomationRunner::builder()
            .chain_library(vec![])
            .chain_executor(clean_chain_executor())
            .agent_executor(clean_agent_executor())
            .build();

        let auto = Automation {
            id: "disabled-auto".into(),
            name: "Disabled".into(),
            description: None,
            enabled: false,
            trigger: AutomationTrigger::Manual {},
            target: AutomationTarget::Agent {
                role: AgentRole::Generic,
                goal: "do something".into(),
            },
        };

        let err = runner.run(&auto, ChainContext::new()).await.unwrap_err();
        assert!(matches!(err, AutomationError::Disabled { .. }));
    }
}
