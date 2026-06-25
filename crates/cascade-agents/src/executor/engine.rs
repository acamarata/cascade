//! AgentExecutor — the multi-step ReAct dispatch engine.
//!
//! Purpose: accepts `AgentTask` values and runs each to completion via a
//!   ReAct-style loop. Tool calls are grant-checked before execution;
//!   `Outbound` calls park the task with an `ApprovalRequest` event.
//! Inputs: `AgentTask` + `AgentSpec` via `run_task`.
//! Outputs: updated `AgentTask` (Done/Failed/Pending); side-channel events
//!   via `approval_tx` and `child_tx`.
//! Constraints: `ToolInvoker` and `ProviderRouter` are injectable (test mocks).
//! SPORT: cascade-agents / executor / engine

use std::sync::Arc;

use tokio::sync::mpsc;
use tracing::{debug, info, instrument, warn, event, Level};

use crate::context::{
    AgentRunContextBuilder, ContextMessage, ContextProvider, ContextRole,
    NoopContextProvider, ToolResult,
};
use crate::grants::{AccessLevel, GrantDecision};
use crate::prompt_gate::{check_prompt_size, PromptSizeConfig};
use crate::spec::AgentSpec;
use crate::task::{AgentTask, TaskStatus};
use crate::tool_registry::ToolRegistry;

use super::store::TaskStore;
use super::types::{
    ApprovalRequest, ExecutorError, ProviderRouter, SUBAGENT_CONTEXT_PREFIX, ToolInvoker,
};

// ── ChildTaskRequest ──────────────────────────────────────────────────────────

/// A request to spawn a child task.
#[derive(Debug, Clone)]
pub struct ChildTaskRequest {
    pub task: AgentTask,
    pub spec: AgentSpec,
}

// ── AgentExecutor ─────────────────────────────────────────────────────────────

/// The Cascade multi-step agent execution engine.
///
/// `AgentExecutor` accepts `AgentTask` values from an async queue and runs each
/// to completion via a ReAct-style loop. Tool calls are grant-checked before
/// execution; `Outbound` calls park the task with an `ApprovalRequest` event.
///
/// # Construction
/// Use `AgentExecutor::builder()` to set injectable dependencies (provider
/// router, tool invoker, tool registry, context provider).
///
/// # Example (with mocks — see tests)
/// ```rust,ignore
/// let executor = AgentExecutor::builder()
///     .provider_router(Arc::new(mock_router))
///     .tool_invoker(Arc::new(mock_invoker))
///     .build();
///
/// let task = AgentTask::new_root("write auth module", AgentRole::Coder);
/// let result = executor.run_task(spec, task).await;
/// assert!(result.is_ok());
/// ```
pub struct AgentExecutor {
    pub(super) provider: Arc<dyn ProviderRouter>,
    pub(super) invoker: Arc<dyn ToolInvoker>,
    pub(super) tool_registry: Arc<ToolRegistry>,
    pub(super) context_provider: Arc<dyn ContextProvider>,
    pub(super) max_steps: u32,
    pub(super) token_budget: u32,
    pub(super) store: TaskStore,
    /// Sender for approval events (None = drop approval events).
    pub(super) approval_tx: Option<mpsc::Sender<ApprovalRequest>>,
    /// Sender for spawned child tasks (loops back into the queue in production).
    pub(super) child_tx: Option<mpsc::Sender<ChildTaskRequest>>,
    /// Prompt-size gate configuration.
    pub(super) prompt_size_cfg: PromptSizeConfig,
}

impl AgentExecutor {
    /// Return a builder to configure the executor.
    pub fn builder() -> AgentExecutorBuilder {
        AgentExecutorBuilder::default()
    }

    /// Run a single `AgentTask` to completion (or to a parked/failed terminal).
    ///
    /// This is the primary entry point for the dispatch loop. It assembles
    /// the context, runs the ReAct loop, and updates the task store.
    #[instrument(name = "executor_run_task", skip(self, spec, task),
                 fields(task_id = %task.id, agent_id = %spec.id))]
    pub async fn run_task(
        &self,
        spec: AgentSpec,
        mut task: AgentTask,
    ) -> Result<AgentTask, ExecutorError> {
        task.mark_running(spec.id.clone());
        self.store.upsert(task.clone());

        let mut run_ctx = AgentRunContextBuilder::new(spec.clone(), task.goal.clone())
            .tool_registry(self.tool_registry.clone(), spec.id.clone())
            .context_provider(self.context_provider.clone())
            .build()
            .await;

        // Inject the canonical subagent-context prefix as the first user message.
        //
        // WHY: The prefix is a stable `const &str` — identical bytes across all
        // parallel children in a run — so provider implementations that support
        // prompt caching (e.g. Anthropic cache_control) can cache it once and
        // reuse it. Prepend before any existing user messages so it arrives
        // before the task goal in the assembled conversation.
        let prefix_msg = ContextMessage {
            role: ContextRole::User,
            content: SUBAGENT_CONTEXT_PREFIX.to_string(),
        };
        // Insert after any leading system message (system must stay at index 0).
        let insert_at = run_ctx
            .messages
            .iter()
            .position(|m| m.role != ContextRole::System)
            .unwrap_or(run_ctx.messages.len());
        run_ctx.messages.insert(insert_at, prefix_msg);

        // ── Prompt-size gate ─────────────────────────────────────────────────
        //
        // Assemble the full system-prompt text (system messages + prefix) for
        // size estimation.  We join all messages that are visible before the
        // first ReAct step so the estimate reflects what the model will actually
        // receive.
        let assembled_prompt: String = run_ctx
            .messages
            .iter()
            .map(|m| m.content.as_str())
            .collect::<Vec<_>>()
            .join("\n\n");

        let gate_report = check_prompt_size(&assembled_prompt, &self.prompt_size_cfg)
            .map_err(ExecutorError::PromptTooLarge)?;

        // Emit per-agent token-count telemetry regardless of threshold outcome.
        event!(
            Level::DEBUG,
            task_id = %task.id,
            agent_id = %spec.id,
            estimated_prompt_tokens = gate_report.estimated_tokens,
            gate_outcome = ?gate_report.outcome,
            "prompt size gate"
        );

        match self.react_loop(spec, task.clone(), run_ctx).await {
            Ok(final_task) => {
                self.store.upsert(final_task.clone());
                Ok(final_task)
            }
            Err(e) => {
                task.mark_failed(e.to_string());
                self.store.upsert(task.clone());
                Err(e)
            }
        }
    }

    /// The ReAct loop: step → grant check → invoke tools → feed results → repeat.
    async fn react_loop(
        &self,
        spec: AgentSpec,
        mut task: AgentTask,
        mut run_ctx: crate::context::AgentRunContext,
    ) -> Result<AgentTask, ExecutorError> {
        let mut steps: u32 = 0;
        let mut total_tokens: u32 = 0;
        let mut no_progress_steps: u32 = 0;
        const NO_PROGRESS_LIMIT: u32 = 3;

        loop {
            // ── Loop guards ──────────────────────────────────────────────────
            if steps >= self.max_steps {
                return Err(ExecutorError::MaxStepsExceeded {
                    max: self.max_steps,
                });
            }
            if total_tokens >= self.token_budget {
                return Err(ExecutorError::TokenBudgetExceeded {
                    budget: self.token_budget,
                    used: total_tokens,
                });
            }

            steps += 1;
            debug!(step = steps, task_id = %task.id, "react step");

            // ── Provider step ────────────────────────────────────────────────
            let outcome = self.provider.step(&spec, &run_ctx).await?;
            total_tokens += outcome.usage.total();

            // Append assistant turn to messages
            if !outcome.assistant_text.is_empty() {
                run_ctx.messages.push(ContextMessage {
                    role: ContextRole::Assistant,
                    content: outcome.assistant_text.clone(),
                });
            }

            // ── Done? ────────────────────────────────────────────────────────
            if outcome.done && outcome.tool_calls.is_empty() {
                task.mark_done();
                return Ok(task);
            }

            // ── No tool calls but not done — no-progress guard ────────────────
            if outcome.tool_calls.is_empty() {
                no_progress_steps += 1;
                if no_progress_steps >= NO_PROGRESS_LIMIT {
                    return Err(ExecutorError::NoProgress {
                        steps: no_progress_steps,
                    });
                }
                continue;
            }

            no_progress_steps = 0;

            // ── Process each tool call ────────────────────────────────────────
            let mut tool_results: Vec<ToolResult> = vec![];
            for call in &outcome.tool_calls {
                // Special: cascade.spawn_child enqueues a child task
                if call.tool_id == "cascade.spawn_child" {
                    let child_result = self.handle_spawn_child(call, &task, &spec).await;
                    tool_results.push(child_result);
                    continue;
                }

                // Grant check at the tool's actual required access level
                let required_level = self
                    .tool_registry
                    .get_tool(&call.tool_id)
                    .map(|d| d.required_level)
                    .unwrap_or(AccessLevel::Write);
                let decision = self
                    .tool_registry
                    .check(&spec.id, &call.tool_id, required_level);

                match decision {
                    GrantDecision::Denied { reason } => {
                        // Hard error — task fails
                        return Err(ExecutorError::ToolDenied { reason });
                    }
                    GrantDecision::NeedsApproval { reason } => {
                        // Park task — do NOT execute the tool
                        info!(
                            task_id = %task.id,
                            tool_id = %call.tool_id,
                            "tool call requires approval — parking task"
                        );
                        task.status = TaskStatus::Pending; // re-queue as pending-approval
                        self.emit_approval_request(ApprovalRequest {
                            task_id: task.id.clone(),
                            tool_call: call.clone(),
                            reason,
                        })
                        .await;
                        self.store.upsert(task.clone());
                        // Return Ok with Pending status — caller handles re-scheduling
                        return Ok(task);
                    }
                    GrantDecision::Granted => {
                        // Execute
                        let result = match self.invoker.invoke(call).await {
                            Ok(output) => ToolResult {
                                call_id: call.call_id.clone(),
                                output,
                                is_error: false,
                            },
                            Err(e) => ToolResult {
                                call_id: call.call_id.clone(),
                                output: e.to_string(),
                                is_error: true,
                            },
                        };
                        tool_results.push(result);
                    }
                }
            }

            // Feed tool results back as a user turn
            let results_text = tool_results
                .iter()
                .map(|r| {
                    if r.is_error {
                        format!("[tool_error call_id={}] {}", r.call_id, r.output)
                    } else {
                        format!("[tool_result call_id={}] {}", r.call_id, r.output)
                    }
                })
                .collect::<Vec<_>>()
                .join("\n");

            run_ctx.messages.push(ContextMessage {
                role: ContextRole::User,
                content: results_text,
            });
        }
    }

    /// Handle a `cascade.spawn_child` tool call: create a child task and enqueue it.
    async fn handle_spawn_child(
        &self,
        call: &crate::context::ToolCall,
        parent: &AgentTask,
        parent_spec: &AgentSpec,
    ) -> ToolResult {
        let goal = call
            .args
            .get("goal")
            .and_then(|v| v.as_str())
            .unwrap_or("child task");

        let child = AgentTask::new_child(goal, parent.assigned_role, parent);
        let child_id = child.id.clone();
        self.store.upsert(child.clone());

        if let Some(ref tx) = self.child_tx {
            let _ = tx
                .send(ChildTaskRequest {
                    task: child,
                    spec: parent_spec.clone(),
                })
                .await;
        }

        ToolResult {
            call_id: call.call_id.clone(),
            output: format!("spawned child task {child_id}"),
            is_error: false,
        }
    }

    /// Emit an approval request event (non-blocking).
    async fn emit_approval_request(&self, req: ApprovalRequest) {
        if let Some(ref tx) = self.approval_tx {
            if let Err(e) = tx.try_send(req) {
                warn!("approval channel full or closed: {e}");
            }
        }
    }

    /// Expose the task store for test assertions.
    pub fn task_store(&self) -> &TaskStore {
        &self.store
    }
}

// ── AgentExecutorBuilder ──────────────────────────────────────────────────────

/// Builder for `AgentExecutor`.
pub struct AgentExecutorBuilder {
    provider: Option<Arc<dyn ProviderRouter>>,
    invoker: Option<Arc<dyn ToolInvoker>>,
    tool_registry: Arc<ToolRegistry>,
    context_provider: Arc<dyn ContextProvider>,
    max_steps: u32,
    token_budget: u32,
    approval_tx: Option<mpsc::Sender<ApprovalRequest>>,
    child_tx: Option<mpsc::Sender<ChildTaskRequest>>,
    prompt_size_cfg: PromptSizeConfig,
}

impl Default for AgentExecutorBuilder {
    fn default() -> Self {
        Self {
            provider: None,
            invoker: None,
            tool_registry: Arc::new(ToolRegistry::new()),
            context_provider: Arc::new(NoopContextProvider),
            max_steps: super::types::DEFAULT_MAX_STEPS,
            token_budget: super::types::DEFAULT_TOKEN_BUDGET,
            approval_tx: None,
            child_tx: None,
            prompt_size_cfg: PromptSizeConfig::default(),
        }
    }
}

impl AgentExecutorBuilder {
    pub fn provider_router(mut self, p: Arc<dyn ProviderRouter>) -> Self {
        self.provider = Some(p);
        self
    }
    pub fn tool_invoker(mut self, i: Arc<dyn ToolInvoker>) -> Self {
        self.invoker = Some(i);
        self
    }
    pub fn tool_registry(mut self, r: Arc<ToolRegistry>) -> Self {
        self.tool_registry = r;
        self
    }
    pub fn context_provider(mut self, p: Arc<dyn ContextProvider>) -> Self {
        self.context_provider = p;
        self
    }
    pub fn max_steps(mut self, n: u32) -> Self {
        self.max_steps = n;
        self
    }
    pub fn token_budget(mut self, n: u32) -> Self {
        self.token_budget = n;
        self
    }
    pub fn approval_channel(mut self, tx: mpsc::Sender<ApprovalRequest>) -> Self {
        self.approval_tx = Some(tx);
        self
    }
    pub fn child_channel(mut self, tx: mpsc::Sender<ChildTaskRequest>) -> Self {
        self.child_tx = Some(tx);
        self
    }
    /// Override the prompt-size gate configuration.
    ///
    /// By default, `PromptSizeConfig::default()` is used (warn=2000, error=4000,
    /// block_on_error=true).
    pub fn prompt_size_config(mut self, cfg: PromptSizeConfig) -> Self {
        self.prompt_size_cfg = cfg;
        self
    }

    /// Build the `AgentExecutor`.
    ///
    /// # Panics
    /// Panics if neither `provider_router` nor `tool_invoker` was set.
    pub fn build(self) -> AgentExecutor {
        AgentExecutor {
            provider: self
                .provider
                .expect("AgentExecutorBuilder: provider_router must be set"),
            invoker: self
                .invoker
                .expect("AgentExecutorBuilder: tool_invoker must be set"),
            tool_registry: self.tool_registry,
            context_provider: self.context_provider,
            max_steps: self.max_steps,
            token_budget: self.token_budget,
            store: TaskStore::new(),
            approval_tx: self.approval_tx,
            child_tx: self.child_tx,
            prompt_size_cfg: self.prompt_size_cfg,
        }
    }
}
