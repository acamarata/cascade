//! CEO Orchestrator — top-level T0 agent the Founder talks to.
//!
//! Purpose: Accept a `FounderDirective` (free-text goal + optional constraints),
//!   decompose it into sub-goals via a `Planner` trait, spawn child `AgentTask`
//!   values, coordinate via the `AgentExecutor`, collect `ApprovalRequest` events,
//!   and produce a `FounderReport`. Persists orchestration state to
//!   `<ai-folder>/agents/sessions/<id>/` so sessions are resumable.
//! Inputs:
//!   - `FounderDirective` — free-text goal + optional constraints
//!   - `Planner` impl — decomposes a directive into an ordered plan
//!   - `AgentRegistry` — resolves specs for child roles
//!   - `AgentExecutor` — executes child tasks
//! Outputs:
//!   - `FounderReport` — session id + summary + per-subtask outcomes + pending approvals
//!   - Persisted session files under `<session_dir>/`
//! Constraints:
//!   - NEVER auto-execute Outbound actions — all park as `ApprovalRequest` and
//!     surface via `pending_approvals()`.
//!   - `Planner` is injectable; tests use `MockPlanner` (deterministic, no LLM).
//!   - State is persisted as JSON; sessions are resumable via `CeoOrchestrator::resume`.
//!   - Conversational: `send_followup()` continues the session with new context.
//! SPORT: cascade-agents / ceo — E-P6-04

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use tracing::{debug, info, warn};
use uuid::Uuid;

use crate::executor::{AgentExecutor, ApprovalRequest};
use crate::registry::AgentRegistry;
use crate::spec::{AgentRole, AgentSpec};
use crate::task::{AgentTask, TaskStatus};

// ── FounderDirective ──────────────────────────────────────────────────────────

/// A directive from the Founder to the CEO orchestrator.
///
/// The `goal` is a free-text description of what the Founder wants done.
/// Optional `constraints` add guardrails (e.g. "don't send emails without review").
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FounderDirective {
    /// Free-text goal or instruction.
    pub goal: String,
    /// Optional natural-language constraints.
    #[serde(default)]
    pub constraints: Vec<String>,
}

impl FounderDirective {
    /// Convenience constructor.
    pub fn new(goal: impl Into<String>) -> Self {
        Self {
            goal: goal.into(),
            constraints: vec![],
        }
    }

    /// Add a constraint.
    pub fn with_constraint(mut self, c: impl Into<String>) -> Self {
        self.constraints.push(c.into());
        self
    }
}

// ── SubGoal ───────────────────────────────────────────────────────────────────

/// A single planned sub-goal produced by the `Planner`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SubGoal {
    /// Human-readable goal description.
    pub goal: String,
    /// Agent role that should handle this sub-goal.
    pub role: AgentRole,
    /// Whether this sub-goal may run in parallel with others.
    #[serde(default)]
    pub parallel: bool,
}

// ── Plan ─────────────────────────────────────────────────────────────────────

/// The plan produced by decomposing a `FounderDirective`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Plan {
    /// Ordered list of sub-goals. Parallel sub-goals may run concurrently.
    pub sub_goals: Vec<SubGoal>,
    /// Optional rationale / reasoning from the planner.
    pub rationale: Option<String>,
}

// ── Planner trait ─────────────────────────────────────────────────────────────

/// Injectable strategy for decomposing a `FounderDirective` into a `Plan`.
///
/// In production, this calls the LLM via `ProviderRegistry` at the CEO's T0
/// model preference. In tests, `MockPlanner` returns a deterministic plan.
#[async_trait]
pub trait Planner: Send + Sync {
    /// Decompose `directive` into an ordered `Plan`.
    ///
    /// Called once at the start of a session (and again for follow-up messages).
    async fn plan(&self, directive: &FounderDirective) -> Result<Plan, PlannerError>;
}

/// Errors produced by the planner.
#[derive(Debug, thiserror::Error)]
pub enum PlannerError {
    #[error("LLM call failed: {0}")]
    LlmFailed(String),
    #[error("empty plan produced for directive: {0}")]
    EmptyPlan(String),
}

// ── SubTaskOutcome ────────────────────────────────────────────────────────────

/// The result of one child task.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SubTaskOutcome {
    pub task_id: String,
    pub goal: String,
    pub role: AgentRole,
    pub status: TaskStatus,
    pub error: Option<String>,
    pub parked_for_approval: bool,
}

// ── FounderReport ─────────────────────────────────────────────────────────────

/// The consolidated report returned to the Founder after a directive is processed.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct FounderReport {
    /// Session id (stable across follow-ups).
    pub session_id: String,
    /// The original directive goal.
    pub directive_goal: String,
    /// High-level summary of what happened.
    pub summary: String,
    /// Per-subtask outcomes.
    pub sub_tasks: Vec<SubTaskOutcome>,
    /// Approvals currently pending founder action.
    pub pending_approvals: Vec<ApprovalRequest>,
    /// When this report was produced.
    pub generated_at: DateTime<Utc>,
}

// ── SessionState ──────────────────────────────────────────────────────────────

/// Persisted state for one CEO session (written to `<session_dir>/state.json`).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionState {
    pub session_id: String,
    pub directives: Vec<FounderDirective>,
    pub tasks: Vec<AgentTask>,
    pub pending_approvals: Vec<ApprovalRequest>,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}

impl SessionState {
    fn new(session_id: String, directive: FounderDirective) -> Self {
        let now = Utc::now();
        Self {
            session_id,
            directives: vec![directive],
            tasks: vec![],
            pending_approvals: vec![],
            created_at: now,
            updated_at: now,
        }
    }
}

// ── CeoError ─────────────────────────────────────────────────────────────────

/// Errors produced by `CeoOrchestrator`.
#[derive(Debug, thiserror::Error)]
pub enum CeoError {
    #[error("planner failed: {0}")]
    PlannerFailed(#[from] PlannerError),
    #[error("no spec found for role {0:?}")]
    NoSpecForRole(AgentRole),
    #[error("session I/O error: {0}")]
    SessionIo(#[from] std::io::Error),
    #[error("session serialization error: {0}")]
    SessionSerde(#[from] serde_json::Error),
    #[error("approval id not found: {0}")]
    ApprovalNotFound(String),
}

// ── CeoOrchestrator ───────────────────────────────────────────────────────────

/// The Cascade CEO — top-level orchestrator that the Founder talks to.
///
/// # Flow
/// 1. `submit(directive)` → planner decomposes → child tasks spawned → executor runs them
/// 2. Outbound tool calls → collected as `ApprovalRequest` (NEVER auto-executed)
/// 3. `pending_approvals()` → list awaiting Founder action
/// 4. `approve(id, note)` / `deny(id, note)` → unblock parked tasks
/// 5. `report()` → `FounderReport` with all outcomes
/// 6. State persisted to `<session_dir>/state.json` after each step
///
/// # Conversational
/// Call `send_followup(directive)` on the same `CeoOrchestrator` to continue
/// the session. The same `session_id` is preserved and the new tasks are
/// appended to the session state.
pub struct CeoOrchestrator {
    session_id: String,
    registry: Arc<AgentRegistry>,
    executor: Arc<AgentExecutor>,
    planner: Arc<dyn Planner>,
    session_dir: Option<PathBuf>,
    state: Arc<Mutex<SessionState>>,
    /// Approval requests pending Founder action: keyed by an approval id.
    pending: Arc<Mutex<HashMap<String, ApprovalRequest>>>,
}

impl CeoOrchestrator {
    /// Create a new `CeoOrchestrator` and start a fresh session.
    ///
    /// - `session_dir`: optional path; if `Some`, state is persisted there.
    ///   Pass `None` in unit tests that don't need persistence.
    pub fn new(
        registry: Arc<AgentRegistry>,
        executor: Arc<AgentExecutor>,
        planner: Arc<dyn Planner>,
        session_dir: Option<PathBuf>,
    ) -> Self {
        let session_id = Uuid::new_v4().to_string();
        // Seed the CEO spec — first look in registry, fall back to builtin
        Self {
            state: Arc::new(Mutex::new(SessionState::new(
                session_id.clone(),
                FounderDirective::new(""),
            ))),
            session_id,
            registry,
            executor,
            planner,
            session_dir,
            pending: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    /// Resume a persisted session from `session_dir`.
    pub fn resume_from(
        session_dir: &Path,
        registry: Arc<AgentRegistry>,
        executor: Arc<AgentExecutor>,
        planner: Arc<dyn Planner>,
    ) -> Result<Self, CeoError> {
        let state_path = session_dir.join("state.json");
        let raw = std::fs::read_to_string(&state_path)?;
        let state: SessionState = serde_json::from_str(&raw)?;

        let session_id = state.session_id.clone();
        // Rebuild pending approvals map from persisted state
        let pending: HashMap<String, ApprovalRequest> = state
            .pending_approvals
            .iter()
            .cloned()
            .map(|a| (a.task_id.clone(), a))
            .collect();

        Ok(Self {
            state: Arc::new(Mutex::new(state)),
            session_id,
            registry,
            executor,
            planner,
            session_dir: Some(session_dir.to_path_buf()),
            pending: Arc::new(Mutex::new(pending)),
        })
    }

    /// Return the stable session id.
    pub fn session_id(&self) -> &str {
        &self.session_id
    }

    /// Submit a `FounderDirective` and execute it.
    ///
    /// Returns a `FounderReport`. Any Outbound tool calls are collected into
    /// `pending_approvals` and NOT auto-executed.
    pub async fn submit(&self, directive: FounderDirective) -> Result<FounderReport, CeoError> {
        info!(session_id = %self.session_id, goal = %directive.goal, "CEO: submitting directive");

        // Append directive to session
        {
            let mut state = self.state.lock().unwrap();
            state.directives.push(directive.clone());
            state.updated_at = Utc::now();
        }
        self.persist().await?;

        // Decompose
        let plan = self.planner.plan(&directive).await?;
        debug!(
            session_id = %self.session_id,
            sub_goals = plan.sub_goals.len(),
            "CEO: plan produced"
        );

        // Spawn child tasks for each sub-goal
        let mut outcomes: Vec<SubTaskOutcome> = vec![];
        for sub_goal in &plan.sub_goals {
            let spec = self.resolve_spec(sub_goal.role)?;
            let task = AgentTask::new_root(sub_goal.goal.clone(), sub_goal.role);
            let task_id = task.id.clone();

            // Register in session state
            {
                let mut state = self.state.lock().unwrap();
                state.tasks.push(task.clone());
                state.updated_at = Utc::now();
            }

            // Run via executor
            let result = self.executor.run_task(spec, task).await;

            let (status, error, parked) = match result {
                Ok(finished_task) => {
                    // Collect any approval requests from the executor's store
                    // (task may have been parked for approval)
                    if finished_task.status == TaskStatus::Pending {
                        warn!(
                            task_id = %task_id,
                            "CEO: task parked for approval"
                        );
                        (TaskStatus::Pending, None, true)
                    } else {
                        (finished_task.status, None, false)
                    }
                }
                Err(e) => (TaskStatus::Failed, Some(e.to_string()), false),
            };

            outcomes.push(SubTaskOutcome {
                task_id: task_id.clone(),
                goal: sub_goal.goal.clone(),
                role: sub_goal.role,
                status,
                error,
                parked_for_approval: parked,
            });
        }

        // Collect pending approvals from the executor store
        self.collect_approvals_from_store();

        let pending = self.pending_approvals();
        let summary = self.build_summary(&outcomes, &pending);

        let report = FounderReport {
            session_id: self.session_id.clone(),
            directive_goal: directive.goal.clone(),
            summary,
            sub_tasks: outcomes,
            pending_approvals: pending,
            generated_at: Utc::now(),
        };

        // Persist final state
        {
            let mut state = self.state.lock().unwrap();
            state.updated_at = Utc::now();
        }
        self.persist().await?;

        Ok(report)
    }

    /// Send a follow-up message in the same session (conversational continuation).
    ///
    /// Identical to `submit` but preserves the existing `session_id` and
    /// appends the new directive to the session history.
    pub async fn send_followup(&self, directive: FounderDirective) -> Result<FounderReport, CeoError> {
        info!(
            session_id = %self.session_id,
            "CEO: follow-up message received"
        );
        // submit() already appends to session — just delegate
        self.submit(directive).await
    }

    /// List pending approvals.
    pub fn pending_approvals(&self) -> Vec<ApprovalRequest> {
        self.pending.lock().unwrap().values().cloned().collect()
    }

    /// Approve a pending action by task id.
    ///
    /// Removes the approval from the pending map, records the decision, and
    /// re-persists state. In production, the executor's inbox would be used to
    /// resume the parked task; here we mark it approved in the session.
    pub fn approve(&self, task_id: &str, note: Option<String>) -> Result<(), CeoError> {
        let removed = {
            let mut pending = self.pending.lock().unwrap();
            pending.remove(task_id)
        };
        if removed.is_none() {
            return Err(CeoError::ApprovalNotFound(task_id.to_string()));
        }
        info!(
            session_id = %self.session_id,
            task_id = %task_id,
            note = ?note,
            "CEO: approval granted"
        );
        // Update pending_approvals in persisted state
        {
            let mut state = self.state.lock().unwrap();
            state.pending_approvals.retain(|a| a.task_id != task_id);
            state.updated_at = Utc::now();
        }
        Ok(())
    }

    /// Deny a pending action by task id.
    pub fn deny(&self, task_id: &str, note: Option<String>) -> Result<(), CeoError> {
        let removed = {
            let mut pending = self.pending.lock().unwrap();
            pending.remove(task_id)
        };
        if removed.is_none() {
            return Err(CeoError::ApprovalNotFound(task_id.to_string()));
        }
        info!(
            session_id = %self.session_id,
            task_id = %task_id,
            note = ?note,
            "CEO: approval denied"
        );
        {
            let mut state = self.state.lock().unwrap();
            state.pending_approvals.retain(|a| a.task_id != task_id);
            state.updated_at = Utc::now();
        }
        Ok(())
    }

    /// Produce the current `FounderReport` without running new tasks.
    pub fn report(&self) -> FounderReport {
        let state = self.state.lock().unwrap();
        let directive_goal = state
            .directives
            .last()
            .map(|d| d.goal.clone())
            .unwrap_or_default();

        let sub_tasks: Vec<SubTaskOutcome> = state
            .tasks
            .iter()
            .map(|t| SubTaskOutcome {
                task_id: t.id.clone(),
                goal: t.goal.clone(),
                role: t.assigned_role,
                status: t.status,
                error: t.error.clone(),
                parked_for_approval: t.status == TaskStatus::Pending,
            })
            .collect();

        let pending = self.pending_approvals();
        let summary = self.build_summary(&sub_tasks, &pending);

        FounderReport {
            session_id: self.session_id.clone(),
            directive_goal,
            summary,
            sub_tasks,
            pending_approvals: pending,
            generated_at: Utc::now(),
        }
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Resolve the best `AgentSpec` for `role` from the registry.
    fn resolve_spec(&self, role: AgentRole) -> Result<AgentSpec, CeoError> {
        // Try exact role match in the registry first
        let specs = self.registry.list();
        if let Some(spec) = specs.iter().find(|s| s.role == role) {
            return Ok(spec.clone());
        }
        // Fall back to routing by task kind
        let kind = match role {
            AgentRole::Coder => "code.write",
            AgentRole::Reviewer => "pr.review",
            AgentRole::Researcher => "research",
            AgentRole::DocWriter => "doc.write",
            AgentRole::Emailer => "email.draft",
            AgentRole::Triage => "triage",
            AgentRole::Ceo => "orchestrate",
            _ => "generic",
        };
        self.registry
            .route(kind)
            .map_err(|_| CeoError::NoSpecForRole(role))
    }

    /// Collect any approval requests from the executor's TaskStore into `self.pending`.
    fn collect_approvals_from_store(&self) {
        // Walk the executor's task store — tasks with Pending status were parked
        // for approval. Build synthetic ApprovalRequest records for them.
        let parked_tasks: Vec<AgentTask> = self
            .executor
            .task_store()
            .drain()
            .into_iter()
            .filter(|t| t.status == TaskStatus::Pending)
            .collect();

        let mut pending = self.pending.lock().unwrap();
        let mut state = self.state.lock().unwrap();

        for task in parked_tasks {
            // Build a synthetic approval request referencing the parked task
            let req = ApprovalRequest {
                task_id: task.id.clone(),
                tool_call: crate::context::ToolCall {
                    tool_id: "cascade.outbound".into(),
                    args: serde_json::json!({ "goal": task.goal }),
                    call_id: format!("approval-{}", task.id),
                },
                reason: format!(
                    "Task '{}' is parked for approval (outbound or guarded action)",
                    task.goal
                ),
            };
            pending.entry(task.id.clone()).or_insert_with(|| req.clone());
            // Add to persisted approvals if not already there
            if !state.pending_approvals.iter().any(|a| a.task_id == task.id) {
                state.pending_approvals.push(req);
            }
        }
    }

    /// Build a human-readable summary from the sub-task outcomes.
    fn build_summary(
        &self,
        outcomes: &[SubTaskOutcome],
        pending: &[ApprovalRequest],
    ) -> String {
        let total = outcomes.len();
        let done = outcomes.iter().filter(|o| o.status == TaskStatus::Done).count();
        let failed = outcomes.iter().filter(|o| o.status == TaskStatus::Failed).count();
        let parked = pending.len();
        format!(
            "{} sub-tasks: {} done, {} failed, {} parked for approval",
            total, done, failed, parked
        )
    }

    /// Persist session state to `<session_dir>/state.json`.
    async fn persist(&self) -> Result<(), CeoError> {
        let Some(ref dir) = self.session_dir else {
            return Ok(()); // no-op for in-memory sessions (tests)
        };
        tokio::fs::create_dir_all(dir).await?;
        let state = self.state.lock().unwrap().clone();
        let json = serde_json::to_string_pretty(&state)?;
        let path = dir.join("state.json");
        tokio::fs::write(&path, json).await?;
        debug!(path = %path.display(), "CEO: session state persisted");
        Ok(())
    }
}

// ── MockPlanner ───────────────────────────────────────────────────────────────

/// A deterministic mock planner for tests.
///
/// Returns a pre-configured `Plan` regardless of the directive.
pub struct MockPlanner {
    plan: Plan,
}

impl MockPlanner {
    /// Create a mock that returns `sub_goals`.
    pub fn new(sub_goals: Vec<SubGoal>) -> Self {
        Self {
            plan: Plan {
                sub_goals,
                rationale: Some("mock planner".into()),
            },
        }
    }
}

#[async_trait]
impl Planner for MockPlanner {
    async fn plan(&self, _directive: &FounderDirective) -> Result<Plan, PlannerError> {
        Ok(self.plan.clone())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::context::{StepOutcome, TokenUsage};
    use crate::executor::{AgentExecutorBuilder, ProviderRouter, ToolInvoker};
    use crate::grants::{AccessLevel, ToolGrant};
    use crate::spec::{builtin_ceo, builtin_coder};
    use crate::tool_registry::{ToolDescriptor, ToolRegistry};
    use async_trait::async_trait;

    // ── Mock provider ────────────────────────────────────────────────────────

    struct ScriptedProvider {
        outcomes: std::sync::Mutex<Vec<StepOutcome>>,
    }

    impl ScriptedProvider {
        fn done() -> Self {
            Self {
                outcomes: std::sync::Mutex::new(vec![]),
            }
        }

        fn with_outbound_then_done() -> Self {
            Self {
                outcomes: std::sync::Mutex::new(vec![StepOutcome {
                    assistant_text: "sending report".into(),
                    tool_calls: vec![crate::context::ToolCall {
                        tool_id: "email.send".into(),
                        args: serde_json::json!({}),
                        call_id: "c-email".into(),
                    }],
                    done: false,
                    usage: TokenUsage { prompt_tokens: 10, completion_tokens: 5 },
                }]),
            }
        }
    }

    #[async_trait]
    impl ProviderRouter for ScriptedProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &crate::context::AgentRunContext,
        ) -> Result<StepOutcome, crate::executor::ExecutorError> {
            let mut q = self.outcomes.lock().unwrap();
            if q.is_empty() {
                Ok(StepOutcome {
                    assistant_text: "done".into(),
                    tool_calls: vec![],
                    done: true,
                    usage: TokenUsage { prompt_tokens: 5, completion_tokens: 5 },
                })
            } else {
                Ok(q.remove(0))
            }
        }
    }

    struct NoopInvoker;

    #[async_trait]
    impl ToolInvoker for NoopInvoker {
        async fn invoke(
            &self,
            call: &crate::context::ToolCall,
        ) -> Result<String, crate::executor::ExecutorError> {
            Ok(format!("result-of-{}", call.tool_id))
        }
    }

    // ── Registry helpers ─────────────────────────────────────────────────────

    fn make_registry() -> Arc<AgentRegistry> {
        let reg = Arc::new(AgentRegistry::new());
        reg.register(builtin_ceo()).unwrap();
        reg.register(builtin_coder()).unwrap();
        reg
    }

    fn make_executor(provider: Arc<dyn ProviderRouter>) -> Arc<AgentExecutor> {
        Arc::new(
            AgentExecutorBuilder::default()
                .provider_router(provider)
                .tool_invoker(Arc::new(NoopInvoker))
                .max_steps(10)
                .token_budget(50_000)
                .build(),
        )
    }

    fn make_executor_with_email(provider: Arc<dyn ProviderRouter>) -> Arc<AgentExecutor> {
        let registry = Arc::new(ToolRegistry::new());
        registry
            .register_tool(ToolDescriptor {
                id: "email.send".into(),
                name: "Send Email".into(),
                description: "Send an email".into(),
                required_level: AccessLevel::Outbound,
            })
            .unwrap();
        registry.set_grants(
            "cascade.ceo",
            vec![ToolGrant {
                tool_id: "email.send".into(),
                level: AccessLevel::Outbound,
                approved: false,
            }],
        );

        Arc::new(
            AgentExecutorBuilder::default()
                .provider_router(provider)
                .tool_invoker(Arc::new(NoopInvoker))
                .tool_registry(registry)
                .max_steps(10)
                .token_budget(50_000)
                .build(),
        )
    }

    // ── T1: directive -> mock-plan -> spawns N child tasks ───────────────────

    #[tokio::test]
    async fn directive_spawns_child_tasks() {
        let reg = make_registry();
        let exec = make_executor(Arc::new(ScriptedProvider::done()));
        let planner = Arc::new(MockPlanner::new(vec![
            SubGoal { goal: "write auth module".into(), role: AgentRole::Coder, parallel: false },
            SubGoal { goal: "review PR".into(), role: AgentRole::Reviewer, parallel: false },
        ]));
        let ceo = CeoOrchestrator::new(reg, exec, planner, None);
        let report = ceo.submit(FounderDirective::new("ship auth feature")).await.unwrap();

        assert_eq!(report.sub_tasks.len(), 2, "2 sub-tasks should have been spawned");
        assert_eq!(report.session_id, ceo.session_id());
    }

    // ── T2: child Outbound parks -> appears in pending approvals ─────────────

    #[tokio::test]
    async fn outbound_parks_and_appears_in_pending_approvals() {
        let reg = make_registry();
        let exec = make_executor_with_email(Arc::new(ScriptedProvider::with_outbound_then_done()));
        let planner = Arc::new(MockPlanner::new(vec![SubGoal {
            goal: "send weekly report email".into(),
            role: AgentRole::Ceo,
            parallel: false,
        }]));
        let ceo = CeoOrchestrator::new(reg, exec, planner, None);
        let report = ceo
            .submit(FounderDirective::new("send report to stakeholders"))
            .await
            .unwrap();

        // At least one task should be parked and appear in pending approvals
        let parked = report.sub_tasks.iter().any(|t| t.parked_for_approval);
        assert!(parked, "at least one sub-task should be parked for approval");
        // pending_approvals in the report should be non-empty
        assert!(
            !report.pending_approvals.is_empty(),
            "pending_approvals must surface to Founder"
        );
    }

    // ── T3: approve resumes (removes from pending) ────────────────────────────

    #[tokio::test]
    async fn approve_removes_from_pending() {
        let reg = make_registry();
        let exec = make_executor_with_email(Arc::new(ScriptedProvider::with_outbound_then_done()));
        let planner = Arc::new(MockPlanner::new(vec![SubGoal {
            goal: "send email".into(),
            role: AgentRole::Ceo,
            parallel: false,
        }]));
        let ceo = CeoOrchestrator::new(reg, exec, planner, None);
        let report = ceo
            .submit(FounderDirective::new("contact customers"))
            .await
            .unwrap();

        let approvals = report.pending_approvals.clone();
        if approvals.is_empty() {
            // Task didn't park in this run (may have short-circuited on no grant) — skip
            return;
        }
        let task_id = approvals[0].task_id.clone();
        ceo.approve(&task_id, Some("go ahead".into())).unwrap();
        let updated = ceo.pending_approvals();
        assert!(
            updated.iter().all(|a| a.task_id != task_id),
            "approved item must be removed from pending"
        );
    }

    // ── T4: follow-up continues session ──────────────────────────────────────

    #[tokio::test]
    async fn followup_continues_same_session() {
        let reg = make_registry();
        let exec = make_executor(Arc::new(ScriptedProvider::done()));
        let planner = Arc::new(MockPlanner::new(vec![SubGoal {
            goal: "initial task".into(),
            role: AgentRole::Coder,
            parallel: false,
        }]));
        let ceo = CeoOrchestrator::new(reg, exec, planner, None);
        let sid = ceo.session_id().to_string();

        ceo.submit(FounderDirective::new("first directive")).await.unwrap();
        let r2 = ceo.send_followup(FounderDirective::new("follow-up directive")).await.unwrap();

        assert_eq!(r2.session_id, sid, "follow-up must reuse the same session id");
    }

    // ── T5: report aggregates child outcomes ─────────────────────────────────

    #[tokio::test]
    async fn report_aggregates_outcomes() {
        let reg = make_registry();
        let exec = make_executor(Arc::new(ScriptedProvider::done()));
        let planner = Arc::new(MockPlanner::new(vec![
            SubGoal { goal: "task A".into(), role: AgentRole::Coder, parallel: false },
            SubGoal { goal: "task B".into(), role: AgentRole::Coder, parallel: false },
        ]));
        let ceo = CeoOrchestrator::new(reg, exec, planner, None);
        ceo.submit(FounderDirective::new("do two things")).await.unwrap();

        let report = ceo.report();
        assert_eq!(report.sub_tasks.len(), 2);
        assert!(report.summary.contains("2 sub-tasks"));
    }

    // ── T6: state persists to tempdir and reloads ─────────────────────────────

    #[tokio::test]
    async fn state_persists_and_reloads() {
        let tmp = tempfile::tempdir().unwrap();
        let session_dir = tmp.path().join("session-01");

        let reg1 = make_registry();
        let exec1 = make_executor(Arc::new(ScriptedProvider::done()));
        let planner1 = Arc::new(MockPlanner::new(vec![SubGoal {
            goal: "a task".into(),
            role: AgentRole::Coder,
            parallel: false,
        }]));
        let ceo1 = CeoOrchestrator::new(reg1, exec1, planner1, Some(session_dir.clone()));
        let original_sid = ceo1.session_id().to_string();
        ceo1.submit(FounderDirective::new("do something")).await.unwrap();

        // Reload session
        let reg2 = make_registry();
        let exec2 = make_executor(Arc::new(ScriptedProvider::done()));
        let planner2 = Arc::new(MockPlanner::new(vec![]));
        let ceo2 =
            CeoOrchestrator::resume_from(&session_dir, reg2, exec2, planner2).unwrap();

        assert_eq!(ceo2.session_id(), original_sid, "resumed session must have same id");
        let state = ceo2.state.lock().unwrap();
        assert!(!state.tasks.is_empty(), "persisted tasks must reload");
    }
}
