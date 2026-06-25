//! `AutomationRunner` and its builder — the single entry point for executing
//! automation workflows. Delegates to `ChainExecutor` or `AgentExecutor`
//! depending on the target kind, then wraps outbound actions in draft artifacts
//! pending explicit approval.

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::Arc;

use crate::chain::{ChainContext, ChainExecutor, ChainFlow};
use crate::executor::{AgentExecutor, ApprovalRequest};
use crate::spec::AgentRole;
use crate::task::AgentTask;

use super::sink::{NoopSink, OutboundSink};
use super::types::{
    Automation, AutomationError, AutomationOutcome, AutomationTarget, DraftArtifact, DraftKind,
};

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
    pub(super) chain_library: Vec<ChainFlow>,
    pub(super) chain_executor: Arc<ChainExecutor>,
    pub(super) agent_executor: Arc<AgentExecutor>,
    pub(super) sink: Arc<dyn OutboundSink>,
    pub(super) drafts_dir: Option<PathBuf>,
    /// In-memory store of pending drafts keyed by draft id.
    pub(super) pending_drafts: Arc<std::sync::Mutex<HashMap<String, DraftArtifact>>>,
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
            return Err(AutomationError::Disabled {
                id: automation.id.clone(),
            });
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
                    let draft = self
                        .create_draft(automation, content, kind, HashMap::new())
                        .await?;
                    let approval = ApprovalRequest {
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
                let draft = self
                    .create_draft(automation, content, kind, HashMap::new())
                    .await?;
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
    pub(super) async fn create_draft(
        &self,
        automation: &Automation,
        content: String,
        kind: DraftKind,
        metadata: HashMap<String, String>,
    ) -> Result<DraftArtifact, AutomationError> {
        let draft = DraftArtifact::new(automation.id.clone(), kind, content, metadata);

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
        self.sink
            .send(&draft)
            .await
            .map_err(|e| AutomationError::SinkError(e.to_string()))
    }

    /// Deny a draft: remove it from pending without calling the sink.
    ///
    /// Returns `Err` if the draft id is unknown.
    pub fn deny_draft(&self, draft_id: &str) -> Result<(), AutomationError> {
        self.pending_drafts
            .lock()
            .unwrap()
            .remove(draft_id)
            .ok_or_else(|| AutomationError::Internal {
                message: format!("draft '{draft_id}' not found in pending set"),
            })?;
        Ok(())
    }

    /// Return all pending (unresolved) draft ids.
    pub fn pending_draft_ids(&self) -> Vec<String> {
        self.pending_drafts
            .lock()
            .unwrap()
            .keys()
            .cloned()
            .collect()
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
pub(super) fn detect_draft_kind(id: &str) -> DraftKind {
    if id.contains("email") {
        DraftKind::Email
    } else if id.contains("ticket") || id.contains("triage") || id.contains("issue") {
        DraftKind::Ticket
    } else {
        DraftKind::Generic
    }
}

/// Render a Handlebars-style `{{key}}` template against a context map.
pub(super) fn render_template(template: &str, ctx: &ChainContext) -> String {
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
pub(super) fn agent_spec_for_role(role: AgentRole) -> crate::spec::AgentSpec {
    use crate::spec::{AgentSpec, Capability, Runtime};
    use cascade_types::agent::Tier;

    AgentSpec {
        id: format!("automation.{role:?}").to_lowercase(),
        version: "1.0.0".into(),
        name: format!("{role:?} Agent"),
        role,
        tier: Tier::T2,
        capabilities: vec![
            Capability::LlmCall,
            Capability::EmailDraft,
            Capability::Triage,
        ],
        model_pref: None,
        system_prompt_ref: None,
        tool_grants_ref: None,
        runtime: Runtime::Native,
    }
}
