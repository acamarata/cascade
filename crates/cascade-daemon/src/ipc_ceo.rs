//! CEO IPC dispatch — `ceo_directive`, `ceo_status`, `ceo_approvals`, `ceo_approve`, `ceo_deny`.
//!
//! Purpose: expose `CeoOrchestrator` over the JSON-RPC typed dispatch path so
//!   the CLI and app can submit directives, poll status, list pending approvals,
//!   and approve/deny them.
//!
//! Inputs:
//!   - JSON-RPC params for each method (see per-method structs below)
//!   - `CeoRuntime` — shared state holding the active `CeoOrchestrator`
//!
//! Outputs:
//!   - JSON `Response` (success or error)
//!
//! Constraints:
//!   - `CeoOrchestrator` is behind an `Arc<Mutex<_>>`; guard is never held
//!     across an `await` point (clone then release).
//!   - Tests inject a `MockPlanner` — no live LLM, no disk I/O.
//!
//! SPORT: cascade-daemon / ipc_ceo — E-P6-04

use std::sync::{Arc, Mutex};

use async_trait::async_trait;
#[cfg(test)]
use cascade_agents::ceo::MockPlanner;
use cascade_agents::ceo::{CeoOrchestrator, FounderDirective, Plan, Planner, SubGoal};
use cascade_agents::context::{AgentRunContext, StepOutcome, TokenUsage, ToolCall};
use cascade_agents::executor::{AgentExecutorBuilder, ExecutorError, ProviderRouter, ToolInvoker};
use cascade_agents::registry::AgentRegistry;
use cascade_agents::spec::{builtin_ceo, builtin_coder, AgentRole, AgentSpec};
use serde::Deserialize;
use serde_json::Value;
use tracing::info;
use uuid::Uuid;

// ── NopRouter / NopInvoker (daemon default until real provider wired) ─────────

/// No-op provider: always returns Done.
struct NopRouter;

#[async_trait]
impl ProviderRouter for NopRouter {
    async fn step(
        &self,
        _spec: &AgentSpec,
        _ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError> {
        Ok(StepOutcome {
            assistant_text: "task complete (nop router)".into(),
            tool_calls: vec![],
            done: true,
            usage: TokenUsage {
                prompt_tokens: 1,
                completion_tokens: 1,
            },
        })
    }
}

/// No-op tool invoker.
struct NopInvoker;

#[async_trait]
impl ToolInvoker for NopInvoker {
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        Ok(format!("nop:{}", call.tool_id))
    }
}

// ── DefaultDaemonPlanner ──────────────────────────────────────────────────────

/// Default daemon planner: produces a single Generic sub-goal from the directive.
///
/// In production, this calls the LLM via `ProviderRegistry`. For E-P6-04,
/// a no-op planner prevents a hard dep on a live LLM at daemon startup.
pub(crate) struct DefaultDaemonPlanner;

#[async_trait]
impl Planner for DefaultDaemonPlanner {
    async fn plan(
        &self,
        directive: &FounderDirective,
    ) -> Result<Plan, cascade_agents::ceo::PlannerError> {
        Ok(Plan {
            sub_goals: vec![SubGoal {
                goal: directive.goal.clone(),
                role: AgentRole::Generic,
                parallel: false,
            }],
            rationale: Some("default daemon planner (single sub-goal)".into()),
        })
    }
}

// ── CeoRuntime ────────────────────────────────────────────────────────────────

/// Shared daemon-level state for the CEO orchestrator.
///
/// Wrapped in `Arc` so it can be stored in `IpcServer` and shared across connection tasks.
pub struct CeoRuntime {
    orchestrator: Mutex<Option<CeoOrchestrator>>,
    registry: Arc<AgentRegistry>,
    planner: Arc<dyn Planner>,
    session_base_dir: Option<std::path::PathBuf>,
}

impl CeoRuntime {
    /// Create a `CeoRuntime` backed by a no-op executor and the default daemon planner.
    pub fn new(session_base_dir: Option<std::path::PathBuf>) -> Self {
        let registry = Arc::new(AgentRegistry::new());
        registry.register(builtin_ceo()).expect("ceo spec");
        registry.register(builtin_coder()).expect("coder spec");
        let planner: Arc<dyn Planner> = Arc::new(DefaultDaemonPlanner);
        Self {
            orchestrator: Mutex::new(None),
            registry,
            planner,
            session_base_dir,
        }
    }

    /// Inject a custom planner (for testing).
    pub fn with_planner(self, planner: Arc<dyn Planner>) -> Self {
        Self { planner, ..self }
    }

    /// Build a fresh executor for a new session.
    fn build_executor(&self) -> Arc<cascade_agents::executor::AgentExecutor> {
        Arc::new(
            AgentExecutorBuilder::default()
                .provider_router(Arc::new(NopRouter))
                .tool_invoker(Arc::new(NopInvoker))
                .max_steps(10)
                .token_budget(50_000)
                .build(),
        )
    }
}

// ── Param / result types ──────────────────────────────────────────────────────

/// Params for `ceo_directive`.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CeoDirectiveParams {
    /// The Founder's goal (free text).
    pub goal: String,
    /// Optional constraints.
    #[serde(default)]
    pub constraints: Vec<String>,
    /// If set, resumes an existing session (not yet supported; reserved).
    #[serde(default)]
    pub session_id: Option<String>,
}

/// Params for `ceo_approve` / `ceo_deny`.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CeoApproveParams {
    pub task_id: String,
    #[serde(default)]
    pub note: Option<String>,
}

// ── Dispatch handlers ─────────────────────────────────────────────────────────

/// Handle `ceo_directive` — submit a new directive and return a `FounderReport`.
pub async fn handle_ceo_directive(
    runtime: &CeoRuntime,
    params: CeoDirectiveParams,
) -> Result<Value, String> {
    info!(goal = %params.goal, "ceo_directive: received");

    let session_dir = runtime
        .session_base_dir
        .as_ref()
        .map(|base| base.join(Uuid::new_v4().to_string()));

    let ceo = CeoOrchestrator::new(
        runtime.registry.clone(),
        runtime.build_executor(),
        runtime.planner.clone(),
        session_dir,
    );

    let mut directive = FounderDirective::new(params.goal.clone());
    for c in &params.constraints {
        directive = directive.with_constraint(c.clone());
    }

    let report = ceo.submit(directive).await.map_err(|e| e.to_string())?;

    // Store for subsequent status/approval queries
    *runtime.orchestrator.lock().unwrap() = Some(ceo);

    serde_json::to_value(&report).map_err(|e| e.to_string())
}

/// Handle `ceo_status` — return the current session report.
pub fn handle_ceo_status(runtime: &CeoRuntime) -> Result<Value, String> {
    let guard = runtime.orchestrator.lock().unwrap();
    match &*guard {
        None => Err("no active CEO session".into()),
        Some(ceo) => serde_json::to_value(ceo.report()).map_err(|e| e.to_string()),
    }
}

/// Handle `ceo_approvals` — list pending approvals.
pub fn handle_ceo_approvals(runtime: &CeoRuntime) -> Result<Value, String> {
    let guard = runtime.orchestrator.lock().unwrap();
    match &*guard {
        None => Err("no active CEO session".into()),
        Some(ceo) => serde_json::to_value(ceo.pending_approvals()).map_err(|e| e.to_string()),
    }
}

/// Handle `ceo_approve` — approve a pending action.
pub fn handle_ceo_approve(runtime: &CeoRuntime, params: CeoApproveParams) -> Result<Value, String> {
    let guard = runtime.orchestrator.lock().unwrap();
    match &*guard {
        None => Err("no active CEO session".into()),
        Some(ceo) => {
            ceo.approve(&params.task_id, params.note)
                .map_err(|e| e.to_string())?;
            Ok(serde_json::json!({ "approved": true, "taskId": params.task_id }))
        }
    }
}

/// Handle `ceo_deny` — deny a pending action.
pub fn handle_ceo_deny(runtime: &CeoRuntime, params: CeoApproveParams) -> Result<Value, String> {
    let guard = runtime.orchestrator.lock().unwrap();
    match &*guard {
        None => Err("no active CEO session".into()),
        Some(ceo) => {
            ceo.deny(&params.task_id, params.note)
                .map_err(|e| e.to_string())?;
            Ok(serde_json::json!({ "denied": true, "taskId": params.task_id }))
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_agents::spec::AgentRole;
    use serial_test::serial;

    fn make_runtime() -> CeoRuntime {
        let planner: Arc<dyn Planner> = Arc::new(MockPlanner::new(vec![SubGoal {
            goal: "do something".into(),
            role: AgentRole::Coder,
            parallel: false,
        }]));
        CeoRuntime::new(None).with_planner(planner)
    }

    // ── T1: ceo_directive dispatches and returns a FounderReport ────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn ceo_directive_returns_report() {
        let runtime = make_runtime();
        let params = CeoDirectiveParams {
            goal: "build the login page".into(),
            constraints: vec![],
            session_id: None,
        };
        let result = handle_ceo_directive(&runtime, params).await.unwrap();
        let session_id = result.get("sessionId").and_then(|v| v.as_str());
        assert!(session_id.is_some(), "report must include sessionId");
    }

    // ── T2: ceo_status returns report after directive ─────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn ceo_status_after_directive() {
        let runtime = make_runtime();
        let params = CeoDirectiveParams {
            goal: "write tests".into(),
            constraints: vec![],
            session_id: None,
        };
        handle_ceo_directive(&runtime, params).await.unwrap();
        let status = handle_ceo_status(&runtime).unwrap();
        assert!(
            status.get("sessionId").is_some(),
            "status must include sessionId"
        );
    }

    // ── T3: ceo_approvals + approve ──────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn ceo_approvals_then_approve() {
        let runtime = make_runtime();
        handle_ceo_directive(
            &runtime,
            CeoDirectiveParams {
                goal: "contact customers".into(),
                constraints: vec![],
                session_id: None,
            },
        )
        .await
        .unwrap();

        let approvals_val = handle_ceo_approvals(&runtime).unwrap();
        let approvals = approvals_val.as_array().unwrap();
        if approvals.is_empty() {
            // No outbound task in this mock run — approve with nonexistent id should error
            let result = handle_ceo_approve(
                &runtime,
                CeoApproveParams {
                    task_id: "nonexistent".into(),
                    note: None,
                },
            );
            assert!(result.is_err());
        } else {
            let task_id = approvals[0]["taskId"].as_str().unwrap().to_string();
            handle_ceo_approve(
                &runtime,
                CeoApproveParams {
                    task_id: task_id.clone(),
                    note: Some("go ahead".into()),
                },
            )
            .unwrap();
            let remaining = handle_ceo_approvals(&runtime).unwrap();
            assert!(remaining
                .as_array()
                .unwrap()
                .iter()
                .all(|a| a["taskId"] != task_id));
        }
    }

    // ── T4: status before any directive returns error ─────────────────────────

    #[test]
    #[serial(global_env)]
    fn ceo_status_before_directive_errors() {
        let runtime = CeoRuntime::new(None);
        assert!(handle_ceo_status(&runtime).is_err());
    }
}
