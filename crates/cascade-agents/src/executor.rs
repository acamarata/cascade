//! AgentExecutor — multi-step ReAct dispatch loop.
//!
//! Purpose: run `AgentTask` values to completion via a bounded async queue +
//!   worker pool. Each task executes a ReAct-style loop: step → check grants →
//!   execute tools (or park for approval / error on denied) → feed results back
//!   → repeat until done or loop-guard trips.
//!
//! Inputs: `AgentTask` enqueued via `AgentExecutor::submit`.
//!
//! Outputs: task transitions (Running → Done/Failed); `ApprovalRequest` events
//!   for outbound tool calls; child tasks spawned and awaited.
//!
//! Constraints:
//!   - `ToolInvoker` and `ProviderRouter` are injectable traits — tests use mocks.
//!   - `AccessLevel::Outbound` → `NeedsApproval` → park in `PendingApproval`, never execute.
//!   - `Denied` → hard error; task transitions to `Failed`.
//!   - Loop guards: `max_steps`, total token budget.
//!   - Child tasks: an agent may request spawning a child via `StepOutcome` by
//!     returning a `ToolCall` with `tool_id == "cascade.spawn_child"`.
//!
//! SPORT: cascade-agents / executor — E-P6-02

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tracing::{debug, info, instrument, warn};

use crate::context::{
    AgentRunContext, AgentRunContextBuilder, ContextMessage, ContextProvider, ContextRole,
    NoopContextProvider, StepOutcome, ToolCall, ToolResult,
};
use crate::grants::{AccessLevel, GrantDecision};
use crate::spec::AgentSpec;
use crate::task::{AgentTask, TaskStatus};
use crate::tool_registry::ToolRegistry;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Default maximum number of ReAct loop steps per task.
pub const DEFAULT_MAX_STEPS: u32 = 20;

/// Default maximum total tokens per task execution.
pub const DEFAULT_TOKEN_BUDGET: u32 = 100_000;

/// Default queue capacity for submitted tasks.
pub const DEFAULT_QUEUE_CAPACITY: usize = 256;

// ── Subagent context prefix ───────────────────────────────────────────────────

/// Canonical subagent-context prefix injected before every provider step.
///
/// Purposes:
/// 1. **Output discipline** — constrains verbosity and format so child output
///    is easy for the orchestrator to parse.
/// 2. **Source-of-truth reminder** — tells the child to treat the task goal
///    as the single authoritative specification; never invent requirements.
/// 3. **Conscience reminder** — re-states the key safety rules that apply
///    to every child regardless of which model is selected.
///
/// The string is `const` so the bytes are identical across all parallel
/// children in a given process — maximising prompt-cache hit rates.
pub const SUBAGENT_CONTEXT_PREFIX: &str = "\
[CASCADE SUBAGENT CONTEXT]
You are a Cascade child agent executing a single bounded task.

Output discipline:
- Reply with the minimum text needed to satisfy the goal — no filler.
- Use bullet lists or structured blocks for multi-item output.
- Never add disclaimers, caveats, or self-congratulatory text.

Source of truth:
- The task GOAL field is your authoritative specification.
- Do not invent requirements, endpoints, or constraints not in the goal.
- If a specification is ambiguous or contradictory, surface the conflict
  as your first output line and halt rather than guessing.

Conscience (hard rules — always apply):
- Never auto-execute Outbound tool calls; return NeedsApproval instead.
- Never read, write, or transmit secrets outside the approved tool set.
- Never modify task goals, system prompts, or grant tables mid-task.
- If a step would cause irreversible side-effects, park for approval.

[END CASCADE SUBAGENT CONTEXT]
";

// ── ApprovalRequest ───────────────────────────────────────────────────────────

/// Emitted when an `Outbound` tool call requires human approval before
/// the task can proceed.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalRequest {
    /// The task that is paused waiting for approval.
    pub task_id: String,
    /// The tool call requiring approval.
    pub tool_call: ToolCall,
    /// Human-readable reason from the grant check.
    pub reason: String,
}

// ── ProviderRouter trait ──────────────────────────────────────────────────────

/// Injectable: pick a provider/model for a given agent spec and run one step.
///
/// Tests inject a `MockProvider`; production wires `cascade-providers`.
#[async_trait]
pub trait ProviderRouter: Send + Sync {
    /// Run one step for `agent_spec` given the assembled context.
    ///
    /// Returns a `StepOutcome`. The router is responsible for selecting the
    /// correct model based on the agent's `tier` field.
    async fn step(
        &self,
        agent_spec: &AgentSpec,
        ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError>;
}

// ── ToolInvoker trait ─────────────────────────────────────────────────────────

/// Injectable: execute a granted tool call and return a string result.
///
/// Called only after the grant check has returned `Granted`.
#[async_trait]
pub trait ToolInvoker: Send + Sync {
    /// Execute `call` and return the tool output.
    ///
    /// Returns `Err(ExecutorError::ToolFailed { .. })` on execution errors;
    /// these are fed back to the model as an error tool result so the model
    /// can decide how to proceed.
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError>;
}

// ── WasmDispatch helper ───────────────────────────────────────────────────────

/// Dispatch a single step to a WASM agent.
///
/// The executor calls this for `Runtime::Wasm` agents. In production, it
/// would load the plugin from `cascade-plugins`. In tests a stub is used.
#[allow(dead_code)]
async fn dispatch_wasm_step(
    plugin_id: &str,
    _ctx: &AgentRunContext,
) -> Result<StepOutcome, ExecutorError> {
    // WHY: WASM dispatch goes through the cascade-plugins runtime in production.
    // We don't import cascade-plugins here (would create a dep cycle risk);
    // instead the caller (ProviderRouter) handles WASM dispatch via the
    // injected trait. This stub is reached only if someone bypasses the router.
    Err(ExecutorError::WasmDispatchUnsupported {
        plugin_id: plugin_id.to_owned(),
        hint: format!(
            "WASM agent '{}' must be dispatched via ProviderRouter — \
             inject a router that handles Runtime::Wasm",
            plugin_id
        ),
    })
}

// ── ExecutorError ─────────────────────────────────────────────────────────────

/// Errors produced by the agent executor.
#[derive(Debug, thiserror::Error)]
pub enum ExecutorError {
    #[error("max steps ({max}) reached without completion")]
    MaxStepsExceeded { max: u32 },

    #[error("token budget ({budget}) exhausted after {used} tokens")]
    TokenBudgetExceeded { budget: u32, used: u32 },

    #[error("tool call denied: {reason}")]
    ToolDenied { reason: String },

    #[error("tool '{tool_id}' execution failed: {message}")]
    ToolFailed { tool_id: String, message: String },

    #[error("provider step failed: {0}")]
    ProviderFailed(String),

    #[error("WASM dispatch unsupported for plugin '{plugin_id}': {hint}")]
    WasmDispatchUnsupported { plugin_id: String, hint: String },

    #[error("child task spawn failed: {0}")]
    SpawnFailed(String),

    #[error("no-progress detected after {steps} consecutive steps with no new tool calls")]
    NoProgress { steps: u32 },
}

// ── TaskStore ─────────────────────────────────────────────────────────────────

/// In-memory store for task state (used by the executor).
///
/// A thin wrapper so tests can inspect task states without a full DB.
#[derive(Clone, Default)]
pub struct TaskStore {
    inner: Arc<Mutex<HashMap<String, AgentTask>>>,
}

impl TaskStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// Upsert a task.
    pub fn upsert(&self, task: AgentTask) {
        self.inner.lock().unwrap().insert(task.id.clone(), task);
    }

    /// Get a task by id.
    pub fn get(&self, id: &str) -> Option<AgentTask> {
        self.inner.lock().unwrap().get(id).cloned()
    }

    /// Drain all stored tasks (for test assertions).
    pub fn drain(&self) -> Vec<AgentTask> {
        self.inner.lock().unwrap().values().cloned().collect()
    }
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
    provider: Arc<dyn ProviderRouter>,
    invoker: Arc<dyn ToolInvoker>,
    tool_registry: Arc<ToolRegistry>,
    context_provider: Arc<dyn ContextProvider>,
    max_steps: u32,
    token_budget: u32,
    store: TaskStore,
    /// Sender for approval events (None = drop approval events).
    approval_tx: Option<mpsc::Sender<ApprovalRequest>>,
    /// Sender for spawned child tasks (loops back into the queue in production).
    child_tx: Option<mpsc::Sender<ChildTaskRequest>>,
}

/// A request to spawn a child task.
#[derive(Debug, Clone)]
pub struct ChildTaskRequest {
    pub task: AgentTask,
    pub spec: AgentSpec,
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
        mut run_ctx: AgentRunContext,
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
        call: &ToolCall,
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
}

impl Default for AgentExecutorBuilder {
    fn default() -> Self {
        Self {
            provider: None,
            invoker: None,
            tool_registry: Arc::new(ToolRegistry::new()),
            context_provider: Arc::new(NoopContextProvider),
            max_steps: DEFAULT_MAX_STEPS,
            token_budget: DEFAULT_TOKEN_BUDGET,
            approval_tx: None,
            child_tx: None,
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
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::context::TokenUsage;
    use crate::grants::{AccessLevel, ToolGrant};
    use crate::spec::{builtin_ceo, builtin_coder, AgentRole, Runtime};
    use crate::task::AgentTask;
    use crate::tool_registry::{ToolDescriptor, ToolRegistry};
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;

    // ── MockProvider ─────────────────────────────────────────────────────────

    /// A scripted provider: returns outcomes from a queue in order.
    struct MockProvider {
        outcomes: Mutex<Vec<StepOutcome>>,
    }

    impl MockProvider {
        fn new(outcomes: Vec<StepOutcome>) -> Self {
            Self {
                outcomes: Mutex::new(outcomes),
            }
        }
    }

    #[async_trait]
    impl ProviderRouter for MockProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            let mut q = self.outcomes.lock().unwrap();
            if q.is_empty() {
                Ok(StepOutcome {
                    assistant_text: "done".into(),
                    tool_calls: vec![],
                    done: true,
                    usage: TokenUsage {
                        prompt_tokens: 5,
                        completion_tokens: 5,
                    },
                })
            } else {
                Ok(q.remove(0))
            }
        }
    }

    // ── MockToolInvoker ──────────────────────────────────────────────────────

    struct MockToolInvoker {
        call_count: Arc<AtomicU32>,
    }

    impl MockToolInvoker {
        fn new() -> Self {
            Self {
                call_count: Arc::new(AtomicU32::new(0)),
            }
        }
        fn call_count(&self) -> u32 {
            self.call_count.load(Ordering::SeqCst)
        }
    }

    #[async_trait]
    impl ToolInvoker for MockToolInvoker {
        async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
            self.call_count.fetch_add(1, Ordering::SeqCst);
            Ok(format!("result-of-{}", call.tool_id))
        }
    }

    // ── Helpers ──────────────────────────────────────────────────────────────

    fn search_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "cascade.search".into(),
            name: "Search".into(),
            description: "Semantic search".into(),
            required_level: AccessLevel::Search,
        }
    }

    fn email_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "email.send".into(),
            name: "Send Email".into(),
            description: "Send an email".into(),
            required_level: AccessLevel::Outbound,
        }
    }

    fn write_tool() -> ToolDescriptor {
        ToolDescriptor {
            id: "file.write".into(),
            name: "Write File".into(),
            description: "Write file".into(),
            required_level: AccessLevel::Write,
        }
    }

    fn tool_call(tool_id: &str) -> ToolCall {
        ToolCall {
            tool_id: tool_id.into(),
            args: serde_json::json!({}),
            call_id: format!("c-{tool_id}"),
        }
    }

    fn step_with_tool(tool_id: &str) -> StepOutcome {
        StepOutcome {
            assistant_text: "calling tool".into(),
            tool_calls: vec![tool_call(tool_id)],
            done: false,
            usage: TokenUsage {
                prompt_tokens: 10,
                completion_tokens: 2,
            },
        }
    }

    fn done_step() -> StepOutcome {
        StepOutcome {
            assistant_text: "all done".into(),
            tool_calls: vec![],
            done: true,
            usage: TokenUsage {
                prompt_tokens: 5,
                completion_tokens: 5,
            },
        }
    }

    fn make_executor_with_registry(
        outcomes: Vec<StepOutcome>,
        registry: Arc<ToolRegistry>,
    ) -> (AgentExecutor, Arc<MockToolInvoker>) {
        let invoker = Arc::new(MockToolInvoker::new());
        let exec = AgentExecutor::builder()
            .provider_router(Arc::new(MockProvider::new(outcomes)))
            .tool_invoker(invoker.clone())
            .tool_registry(registry)
            .max_steps(10)
            .token_budget(50_000)
            .build();
        (exec, invoker)
    }

    // ── T1: single-step task completes ───────────────────────────────────────

    #[tokio::test]
    async fn single_step_task_completes() {
        let registry = Arc::new(ToolRegistry::new());
        let (exec, _) = make_executor_with_registry(vec![done_step()], registry);

        let task = AgentTask::new_root("summarise the project", AgentRole::Coder);
        let result = exec.run_task(builtin_coder(), task).await.unwrap();
        assert_eq!(result.status, TaskStatus::Done);
    }

    // ── T2: multi-step ReAct (tool call → result → done) ─────────────────────

    #[tokio::test]
    async fn multi_step_react_tool_call_then_done() {
        let registry = Arc::new(ToolRegistry::new());
        registry.register_tool(search_tool()).unwrap();
        registry.set_grants(
            "cascade.coder",
            vec![ToolGrant {
                tool_id: "cascade.search".into(),
                level: AccessLevel::Search,
                approved: false,
            }],
        );

        let (exec, invoker) = make_executor_with_registry(
            vec![step_with_tool("cascade.search"), done_step()],
            registry,
        );

        let task = AgentTask::new_root("search then summarise", AgentRole::Coder);
        let result = exec.run_task(builtin_coder(), task).await.unwrap();
        assert_eq!(result.status, TaskStatus::Done);
        assert_eq!(invoker.call_count(), 1, "tool should have been called once");
    }

    // ── T3: Outbound tool → parked for approval (not executed) ───────────────

    #[tokio::test]
    async fn outbound_tool_parked_for_approval_not_executed() {
        let registry = Arc::new(ToolRegistry::new());
        registry.register_tool(email_tool()).unwrap();
        registry.set_grants(
            "cascade.ceo",
            vec![ToolGrant {
                tool_id: "email.send".into(),
                level: AccessLevel::Outbound,
                approved: false, // NOT pre-approved — must park
            }],
        );

        let (tx, mut rx) = mpsc::channel::<ApprovalRequest>(8);
        let invoker = Arc::new(MockToolInvoker::new());
        let exec = AgentExecutor::builder()
            .provider_router(Arc::new(MockProvider::new(vec![
                step_with_tool("email.send"),
                done_step(),
            ])))
            .tool_invoker(invoker.clone())
            .tool_registry(registry)
            .approval_channel(tx)
            .max_steps(10)
            .token_budget(50_000)
            .build();

        let task = AgentTask::new_root("send report", AgentRole::Ceo);
        let result = exec.run_task(builtin_ceo(), task).await.unwrap();

        // Task must be in Pending (parked), not Done
        assert_eq!(
            result.status,
            TaskStatus::Pending,
            "parked task should be Pending, not Done"
        );
        // Tool must NOT have been invoked
        assert_eq!(
            invoker.call_count(),
            0,
            "outbound tool must not be auto-executed"
        );
        // Approval request must have been emitted
        let approval = rx
            .try_recv()
            .expect("approval request should have been emitted");
        assert_eq!(approval.tool_call.tool_id, "email.send");
    }

    // ── T4: Denied tool → error ───────────────────────────────────────────────

    #[tokio::test]
    async fn denied_tool_returns_error() {
        let registry = Arc::new(ToolRegistry::new());
        registry.register_tool(write_tool()).unwrap();
        // Agent has READ grant but calls WRITE — denied
        registry.set_grants(
            "cascade.coder",
            vec![ToolGrant {
                tool_id: "file.write".into(),
                level: AccessLevel::Read, // only read, not write
                approved: false,
            }],
        );

        let (exec, invoker) =
            make_executor_with_registry(vec![step_with_tool("file.write"), done_step()], registry);

        let task = AgentTask::new_root("write file", AgentRole::Coder);
        let err = exec.run_task(builtin_coder(), task).await.unwrap_err();
        assert!(matches!(err, ExecutorError::ToolDenied { .. }));
        assert_eq!(invoker.call_count(), 0, "denied tool must not be invoked");
    }

    // ── T5: max_steps guard trips ─────────────────────────────────────────────

    #[tokio::test]
    async fn max_steps_guard_trips() {
        let registry = Arc::new(ToolRegistry::new());
        registry.register_tool(search_tool()).unwrap();
        registry.set_grants(
            "cascade.coder",
            vec![ToolGrant {
                tool_id: "cascade.search".into(),
                level: AccessLevel::Search,
                approved: false,
            }],
        );

        // 5 consecutive tool steps, never done — should hit max_steps = 3
        let outcomes: Vec<StepOutcome> = (0..5).map(|_| step_with_tool("cascade.search")).collect();
        let invoker = Arc::new(MockToolInvoker::new());
        let exec = AgentExecutor::builder()
            .provider_router(Arc::new(MockProvider::new(outcomes)))
            .tool_invoker(invoker)
            .tool_registry(registry)
            .max_steps(3)
            .token_budget(50_000)
            .build();

        let task = AgentTask::new_root("loop forever", AgentRole::Coder);
        let err = exec.run_task(builtin_coder(), task).await.unwrap_err();
        assert!(matches!(err, ExecutorError::MaxStepsExceeded { max: 3 }));
    }

    // ── T6: child task spawn + parent links ───────────────────────────────────

    #[tokio::test]
    async fn child_task_spawn_and_parent_linkage() {
        let registry = Arc::new(ToolRegistry::new());
        // cascade.spawn_child is a virtual tool — no registry entry needed
        // Grant it as a Write so it passes the grant check
        registry.set_grants(
            "cascade.ceo",
            vec![ToolGrant {
                tool_id: "cascade.spawn_child".into(),
                level: AccessLevel::Write,
                approved: false,
            }],
        );
        // Register a virtual spawn_child descriptor
        registry
            .register_tool(ToolDescriptor {
                id: "cascade.spawn_child".into(),
                name: "Spawn Child".into(),
                description: "Spawn a child task".into(),
                required_level: AccessLevel::Write,
            })
            .unwrap();

        let (child_tx, mut child_rx) = mpsc::channel::<ChildTaskRequest>(8);
        let spawn_step = StepOutcome {
            assistant_text: "spawning child".into(),
            tool_calls: vec![ToolCall {
                tool_id: "cascade.spawn_child".into(),
                args: serde_json::json!({ "goal": "write tests" }),
                call_id: "c-spawn".into(),
            }],
            done: false,
            usage: TokenUsage {
                prompt_tokens: 10,
                completion_tokens: 5,
            },
        };

        let invoker = Arc::new(MockToolInvoker::new());
        let exec = AgentExecutor::builder()
            .provider_router(Arc::new(MockProvider::new(vec![spawn_step, done_step()])))
            .tool_invoker(invoker)
            .tool_registry(registry)
            .child_channel(child_tx)
            .max_steps(10)
            .token_budget(50_000)
            .build();

        let parent = AgentTask::new_root("orchestrate writing", AgentRole::Ceo);
        let parent_id = parent.id.clone();
        let result = exec.run_task(builtin_ceo(), parent).await.unwrap();
        assert_eq!(result.status, TaskStatus::Done);

        // A child task should have been emitted
        let child_req = child_rx
            .try_recv()
            .expect("child task should have been spawned");
        assert_eq!(
            child_req.task.parent_id.as_deref(),
            Some(parent_id.as_str())
        );
        assert_eq!(child_req.task.goal, "write tests");
    }

    // ── T7: native vs wasm dispatch path ─────────────────────────────────────

    /// A provider that checks which runtime was requested.
    struct RuntimeCheckProvider {
        expected_runtime: Runtime,
        saw_correct_runtime: Arc<Mutex<bool>>,
    }

    #[async_trait]
    impl ProviderRouter for RuntimeCheckProvider {
        async fn step(
            &self,
            spec: &AgentSpec,
            _ctx: &AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            let correct = spec.runtime == self.expected_runtime;
            *self.saw_correct_runtime.lock().unwrap() = correct;
            Ok(done_step())
        }
    }

    #[tokio::test]
    async fn native_runtime_dispatched_correctly() {
        let saw = Arc::new(Mutex::new(false));
        let provider = Arc::new(RuntimeCheckProvider {
            expected_runtime: Runtime::Native,
            saw_correct_runtime: saw.clone(),
        });

        let exec = AgentExecutor::builder()
            .provider_router(provider)
            .tool_invoker(Arc::new(MockToolInvoker::new()))
            .max_steps(5)
            .token_budget(50_000)
            .build();

        let task = AgentTask::new_root("native task", AgentRole::Coder);
        exec.run_task(builtin_coder(), task).await.unwrap();
        assert!(
            *saw.lock().unwrap(),
            "provider should have seen Native runtime"
        );
    }

    #[tokio::test]
    async fn wasm_runtime_dispatched_via_router() {
        let wasm_spec = crate::spec::AgentSpec {
            id: "cascade.wasm-agent".into(),
            version: "1.0.0".into(),
            name: "WASM Agent".into(),
            role: AgentRole::Coder,
            tier: cascade_types::agent::Tier::T3,
            capabilities: vec![],
            model_pref: None,
            system_prompt_ref: None,
            tool_grants_ref: None,
            runtime: Runtime::Wasm {
                plugin_id: "my-wasm-plugin".into(),
            },
        };

        let saw = Arc::new(Mutex::new(false));
        let provider = Arc::new(RuntimeCheckProvider {
            expected_runtime: Runtime::Wasm {
                plugin_id: "my-wasm-plugin".into(),
            },
            saw_correct_runtime: saw.clone(),
        });

        let exec = AgentExecutor::builder()
            .provider_router(provider)
            .tool_invoker(Arc::new(MockToolInvoker::new()))
            .max_steps(5)
            .token_budget(50_000)
            .build();

        let task = AgentTask::new_root("wasm task", AgentRole::Coder);
        exec.run_task(wasm_spec, task).await.unwrap();
        assert!(
            *saw.lock().unwrap(),
            "provider should have seen Wasm runtime"
        );
    }

    // ── T8: no-progress guard ─────────────────────────────────────────────────

    #[tokio::test]
    async fn no_progress_guard_trips() {
        // Provider returns done=false, no tool calls, repeatedly
        let stuck_step = StepOutcome {
            assistant_text: "thinking…".into(),
            tool_calls: vec![],
            done: false,
            usage: TokenUsage {
                prompt_tokens: 5,
                completion_tokens: 1,
            },
        };
        let outcomes: Vec<StepOutcome> = (0..5).map(|_| stuck_step.clone()).collect();
        let (exec, _) = make_executor_with_registry(outcomes, Arc::new(ToolRegistry::new()));
        let task = AgentTask::new_root("stuck forever", AgentRole::Coder);
        let err = exec.run_task(builtin_coder(), task).await.unwrap_err();
        assert!(matches!(err, ExecutorError::NoProgress { .. }));
    }
}
