//! Tests for AgentExecutor — ReAct loop, grant checks, approval gating,
//! child spawning, loop guards, and prompt-size gate.
//!
//! SPORT: cascade-agents / executor / tests

use std::sync::{Arc, Mutex};
use std::sync::atomic::{AtomicU32, Ordering};

use async_trait::async_trait;

use crate::context::{AgentRunContext, StepOutcome, ToolCall, TokenUsage};
use crate::grants::{AccessLevel, ToolGrant};
use crate::spec::{builtin_ceo, builtin_coder, AgentRole, Runtime};
use crate::task::{AgentTask, TaskStatus};
use crate::tool_registry::{ToolDescriptor, ToolRegistry};

use super::engine::{AgentExecutor, ChildTaskRequest};
use super::types::{ApprovalRequest, ExecutorError, ProviderRouter, ToolInvoker};

use tokio::sync::mpsc;

// ── MockProvider ─────────────────────────────────────────────────────────────

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
        _spec: &crate::spec::AgentSpec,
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

// ── Helpers ──────────────────────────────────────────────────────────────────

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

// ── T1: single-step task completes ───────────────────────────────────────────

#[tokio::test]
async fn single_step_task_completes() {
    let registry = Arc::new(ToolRegistry::new());
    let (exec, _) = make_executor_with_registry(vec![done_step()], registry);

    let task = AgentTask::new_root("summarise the project", AgentRole::Coder);
    let result = exec.run_task(builtin_coder(), task).await.unwrap();
    assert_eq!(result.status, TaskStatus::Done);
}

// ── T2: multi-step ReAct (tool call → result → done) ─────────────────────────

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

// ── T3: Outbound tool → parked for approval (not executed) ───────────────────

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

// ── T4: Denied tool → error ───────────────────────────────────────────────────

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

// ── T5: max_steps guard trips ─────────────────────────────────────────────────

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

// ── T6: child task spawn + parent links ───────────────────────────────────────

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

// ── T7: native vs wasm dispatch path ─────────────────────────────────────────

/// A provider that checks which runtime was requested.
struct RuntimeCheckProvider {
    expected_runtime: Runtime,
    saw_correct_runtime: Arc<Mutex<bool>>,
}

#[async_trait]
impl ProviderRouter for RuntimeCheckProvider {
    async fn step(
        &self,
        spec: &crate::spec::AgentSpec,
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
        soul_ref: None,
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

// ── T8: no-progress guard ─────────────────────────────────────────────────────

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

// ── T9: prompt gate — small prompt passes silently ────────────────────────────

#[tokio::test]
async fn prompt_gate_small_prompt_passes() {
    use crate::prompt_gate::PromptSizeConfig;

    let registry = Arc::new(ToolRegistry::new());
    let invoker = Arc::new(MockToolInvoker::new());
    let exec = AgentExecutor::builder()
        .provider_router(Arc::new(MockProvider::new(vec![done_step()])))
        .tool_invoker(invoker)
        .tool_registry(registry)
        .max_steps(5)
        .token_budget(50_000)
        // Very tight thresholds; but the goal is a short string so it passes
        .prompt_size_config(PromptSizeConfig {
            warn: 5_000,
            error: 10_000,
            block_on_error: true,
        })
        .build();

    let task = AgentTask::new_root("tiny task", AgentRole::Coder);
    let result = exec.run_task(builtin_coder(), task).await;
    assert!(result.is_ok(), "small prompt should pass the gate");
}

// ── T10: prompt gate — oversized prompt blocked ───────────────────────────────

#[tokio::test]
async fn prompt_gate_oversized_prompt_blocked() {
    use crate::prompt_gate::PromptSizeConfig;

    let registry = Arc::new(ToolRegistry::new());
    let invoker = Arc::new(MockToolInvoker::new());
    let exec = AgentExecutor::builder()
        .provider_router(Arc::new(MockProvider::new(vec![done_step()])))
        .tool_invoker(invoker)
        .tool_registry(registry)
        .max_steps(5)
        .token_budget(50_000)
        // error threshold of 5 tokens — essentially anything will trip it
        .prompt_size_config(PromptSizeConfig {
            warn: 1,
            error: 5,
            block_on_error: true,
        })
        .build();

    // Goal is a long string that pushes the assembled prompt way over 5 tokens
    let task = AgentTask::new_root("word ".repeat(200), AgentRole::Coder);
    let err = exec.run_task(builtin_coder(), task).await.unwrap_err();
    assert!(
        matches!(err, ExecutorError::PromptTooLarge(_)),
        "oversized prompt must be blocked: {err:?}"
    );
}

// ── T11: prompt gate — block_on_error=false proceeds despite large prompt ─────

#[tokio::test]
async fn prompt_gate_oversized_not_blocked_when_flag_off() {
    use crate::prompt_gate::PromptSizeConfig;

    let registry = Arc::new(ToolRegistry::new());
    let invoker = Arc::new(MockToolInvoker::new());
    let exec = AgentExecutor::builder()
        .provider_router(Arc::new(MockProvider::new(vec![done_step()])))
        .tool_invoker(invoker)
        .tool_registry(registry)
        .max_steps(5)
        .token_budget(50_000)
        .prompt_size_config(PromptSizeConfig {
            warn: 1,
            error: 5,
            block_on_error: false, // ← log only, don't block
        })
        .build();

    let task = AgentTask::new_root("word ".repeat(200), AgentRole::Coder);
    let result = exec.run_task(builtin_coder(), task).await;
    assert!(
        result.is_ok(),
        "gate with block_on_error=false must not prevent execution"
    );
}
