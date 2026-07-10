//! Tests for ChainFlow execution, validation, YAML I/O, and parallel semantics.
#![cfg(test)]

use std::collections::HashSet;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Arc, Mutex};

use serde_json::json;
use tempfile::TempDir;

use crate::context::{AgentRunContext, StepOutcome, TokenUsage};
use crate::executor::{AgentExecutor, ExecutorError, ProviderRouter, ToolInvoker};
use crate::grants::{AccessLevel, ToolGrant};
use crate::spec::AgentSpec;
use crate::tool_registry::{ToolDescriptor, ToolRegistry};

use super::builtins::{builtin_research_summarize_draft, builtin_triage_branch_respond};
use super::executor::ChainExecutor;
use super::types::{ChainContext, ChainError, ChainFlow, ChainStep, ChainValidationError};

// ── Mock provider ─────────────────────────────────────────────────────────

struct ScriptedProvider {
    responses: Mutex<Vec<String>>,
}
impl ScriptedProvider {
    fn new(responses: Vec<&str>) -> Self {
        Self {
            responses: Mutex::new(responses.iter().map(|s| s.to_string()).collect()),
        }
    }
}

#[async_trait::async_trait]
impl ProviderRouter for ScriptedProvider {
    async fn step(
        &self,
        _spec: &AgentSpec,
        _ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError> {
        let text = self
            .responses
            .lock()
            .unwrap()
            .drain(..1)
            .next()
            .unwrap_or_else(|| "done".into());
        Ok(StepOutcome {
            assistant_text: text,
            tool_calls: vec![],
            done: true,
            usage: TokenUsage {
                prompt_tokens: 5,
                completion_tokens: 5,
            },
        })
    }
}

// ── Mock invoker ──────────────────────────────────────────────────────────

struct EchoInvoker {
    count: Arc<AtomicU32>,
}
impl EchoInvoker {
    fn new() -> Self {
        Self {
            count: Arc::new(AtomicU32::new(0)),
        }
    }
    #[allow(dead_code)]
    fn count(&self) -> u32 {
        self.count.load(Ordering::SeqCst)
    }
}
#[async_trait::async_trait]
impl ToolInvoker for EchoInvoker {
    async fn invoke(&self, call: &crate::context::ToolCall) -> Result<String, ExecutorError> {
        self.count.fetch_add(1, Ordering::SeqCst);
        Ok(format!("echo:{}", call.tool_id))
    }
}

fn make_agent_executor(
    provider: Arc<dyn ProviderRouter>,
    invoker: Arc<dyn ToolInvoker>,
) -> Arc<AgentExecutor> {
    Arc::new(
        AgentExecutor::builder()
            .provider_router(provider)
            .tool_invoker(invoker)
            .max_steps(10)
            .token_budget(50_000)
            .build(),
    )
}

fn make_executor(
    provider: Arc<dyn ProviderRouter>,
    invoker: Arc<dyn ToolInvoker>,
    agent_exec: Arc<AgentExecutor>,
    registry: Arc<ToolRegistry>,
) -> ChainExecutor {
    ChainExecutor::builder()
        .provider_router(provider)
        .tool_invoker(invoker)
        .agent_executor(agent_exec)
        .tool_registry(registry)
        .max_steps(20)
        .max_depth(4)
        .build()
}

// ── T1: linear chain threads io ───────────────────────────────────────────

#[tokio::test]
async fn linear_chain_threads_io() {
    let provider = Arc::new(ScriptedProvider::new(vec!["step1-result", "step2-result"]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec!["done"])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "linear".into(),
        name: "Linear".into(),
        description: None,
        steps: vec![
            ChainStep::Prompt {
                template: "Step 1: {{initial}}".into(),
                inputs: vec!["initial".into()],
                output: "out1".into(),
            },
            ChainStep::Prompt {
                template: "Step 2: {{out1}}".into(),
                inputs: vec!["out1".into()],
                output: "out2".into(),
            },
        ],
    };

    let mut ctx = ChainContext::new();
    ctx.insert("initial".into(), json!("hello"));
    let result = exec.run(&flow, ctx).await.unwrap();

    assert_eq!(result.context.get("out1"), Some(&json!("step1-result")));
    assert_eq!(result.context.get("out2"), Some(&json!("step2-result")));
    assert_eq!(result.traces.len(), 2);
}

// ── T2: Branch true path ──────────────────────────────────────────────────

#[tokio::test]
async fn branch_true_path_executes() {
    let provider = Arc::new(ScriptedProvider::new(vec!["then-result"]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "branch-true".into(),
        name: "Branch True".into(),
        description: None,
        steps: vec![ChainStep::Branch {
            cond: "flag".into(),
            then_step: Box::new(ChainStep::Prompt {
                template: "then branch".into(),
                inputs: vec![],
                output: "result".into(),
            }),
            else_step: None,
        }],
    };

    let mut ctx = ChainContext::new();
    ctx.insert("flag".into(), json!(true));
    let result = exec.run(&flow, ctx).await.unwrap();
    assert_eq!(result.context.get("result"), Some(&json!("then-result")));
}

// ── T3: Branch false path ─────────────────────────────────────────────────

#[tokio::test]
async fn branch_false_path_executes() {
    let provider = Arc::new(ScriptedProvider::new(vec!["else-result"]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "branch-false".into(),
        name: "Branch False".into(),
        description: None,
        steps: vec![ChainStep::Branch {
            cond: "flag".into(),
            then_step: Box::new(ChainStep::Prompt {
                template: "then branch".into(),
                inputs: vec![],
                output: "result".into(),
            }),
            else_step: Some(Box::new(ChainStep::Prompt {
                template: "else branch".into(),
                inputs: vec![],
                output: "result".into(),
            })),
        }],
    };

    let mut ctx = ChainContext::new();
    ctx.insert("flag".into(), json!(false));
    let result = exec.run(&flow, ctx).await.unwrap();
    assert_eq!(result.context.get("result"), Some(&json!("else-result")));
}

// ── T4: Map over list ─────────────────────────────────────────────────────

#[tokio::test]
async fn map_over_list_collects_results() {
    let provider = Arc::new(ScriptedProvider::new(vec!["r1", "r2", "r3"]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "map-test".into(),
        name: "Map Test".into(),
        description: None,
        steps: vec![ChainStep::Map {
            over: "items".into(),
            step: Box::new(ChainStep::Prompt {
                template: "process: {{_item}}".into(),
                inputs: vec!["_item".into()],
                output: "processed".into(),
            }),
            output: "results".into(),
        }],
    };

    let mut ctx = ChainContext::new();
    ctx.insert("items".into(), json!(["a", "b", "c"]));
    let result = exec.run(&flow, ctx).await.unwrap();
    let results = result.context.get("results").unwrap();
    assert_eq!(results, &json!(["r1", "r2", "r3"]));
}

// ── T5: Parallel collects results ─────────────────────────────────────────

#[tokio::test]
async fn parallel_collects_results() {
    let provider = Arc::new(ScriptedProvider::new(vec!["pa", "pb"]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "parallel-test".into(),
        name: "Parallel Test".into(),
        description: None,
        steps: vec![ChainStep::Parallel {
            steps: vec![
                ChainStep::Prompt {
                    template: "branch A".into(),
                    inputs: vec![],
                    output: "out_a".into(),
                },
                ChainStep::Prompt {
                    template: "branch B".into(),
                    inputs: vec![],
                    output: "out_b".into(),
                },
            ],
        }],
    };

    let ctx = ChainContext::new();
    let result = exec.run(&flow, ctx).await.unwrap();
    assert_eq!(result.context.get("out_a"), Some(&json!("pa")));
    assert_eq!(result.context.get("out_b"), Some(&json!("pb")));
}

// ── T6: ToolCall outbound parks for approval ──────────────────────────────

#[tokio::test]
async fn tool_call_outbound_parks_for_approval() {
    let provider = Arc::new(ScriptedProvider::new(vec![]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );

    let registry = Arc::new(ToolRegistry::new());
    registry
        .register_tool(ToolDescriptor {
            id: "email.send".into(),
            name: "Send Email".into(),
            description: "Send email".into(),
            required_level: AccessLevel::Outbound,
        })
        .unwrap();
    registry.set_grants(
        "chain.executor",
        vec![ToolGrant {
            tool_id: "email.send".into(),
            level: AccessLevel::Outbound,
            approved: false,
        }],
    );

    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "outbound-test".into(),
        name: "Outbound Test".into(),
        description: None,
        steps: vec![ChainStep::ToolCall {
            tool: "email.send".into(),
            args: json!({ "to": "user@example.com", "body": "Hello" }),
            output: "email_result".into(),
        }],
    };

    let ctx = ChainContext::new();
    let err = exec.run(&flow, ctx).await.unwrap_err();
    assert!(
        matches!(&err, ChainError::NeedsApproval { tool } if tool == "email.send"),
        "expected NeedsApproval for email.send, got: {err}"
    );
}

// ── T7: YAML load round-trip ──────────────────────────────────────────────

#[test]
fn yaml_load_round_trip() {
    let flow = builtin_research_summarize_draft();
    let yaml = serde_yaml::to_string(&flow).unwrap();
    let decoded: ChainFlow = serde_yaml::from_str(&yaml).unwrap();
    assert_eq!(decoded.id, flow.id);
    assert_eq!(decoded.steps.len(), flow.steps.len());
}

#[test]
fn yaml_load_from_tempfile() {
    let dir = TempDir::new().unwrap();
    let path = dir.path().join("research.yaml");
    let flow = builtin_research_summarize_draft();
    let yaml = serde_yaml::to_string(&flow).unwrap();
    std::fs::write(&path, yaml).unwrap();

    let loaded = ChainFlow::load_from_file(&path).unwrap();
    assert_eq!(loaded.id, "research-summarize-draft");
    assert_eq!(loaded.steps.len(), 3);
}

#[test]
fn yaml_library_loads_multiple_files() {
    let dir = TempDir::new().unwrap();
    for flow in [
        builtin_research_summarize_draft(),
        builtin_triage_branch_respond(),
    ] {
        let yaml = serde_yaml::to_string(&flow).unwrap();
        std::fs::write(dir.path().join(format!("{}.yaml", flow.id)), yaml).unwrap();
    }
    let library = ChainFlow::load_library(dir.path()).unwrap();
    assert_eq!(library.len(), 2);
}

// ── T8: Cycle/ref validation rejects bad flow ─────────────────────────────

#[test]
fn validation_rejects_unresolved_ref() {
    let flow = ChainFlow {
        id: "bad".into(),
        name: "Bad".into(),
        description: None,
        steps: vec![ChainStep::Prompt {
            template: "use {{missing_key}}".into(),
            inputs: vec!["missing_key".into()],
            output: "out".into(),
        }],
    };
    let errs = flow.validate().unwrap_err();
    assert!(errs.iter().any(
        |e| matches!(e, ChainValidationError::UnresolvedRef { key, .. } if key == "missing_key")
    ));
}

#[test]
fn validation_rejects_duplicate_output() {
    let flow = ChainFlow {
        id: "dup".into(),
        name: "Dup".into(),
        description: None,
        steps: vec![
            ChainStep::Prompt {
                template: "first".into(),
                inputs: vec![],
                output: "out".into(),
            },
            ChainStep::Prompt {
                template: "second".into(),
                inputs: vec![],
                output: "out".into(),
            },
        ],
    };
    let errs = flow.validate().unwrap_err();
    assert!(errs
        .iter()
        .any(|e| matches!(e, ChainValidationError::DuplicateOutput { key } if key == "out")));
}

#[test]
fn validation_accepts_valid_flow() {
    let flow = builtin_research_summarize_draft();
    let mut seen: HashSet<String> = HashSet::new();
    seen.insert("topic".into());
    let mut errors = vec![];
    for step in &flow.steps {
        flow.validate_step(step, &mut seen, &mut errors, 0);
    }
    assert!(errors.is_empty(), "expected no errors, got: {errors:?}");
}

// ── T9: Budget guard ──────────────────────────────────────────────────────

#[tokio::test]
async fn budget_guard_trips_on_large_map() {
    let provider = Arc::new(ScriptedProvider::new(
        (0..10)
            .map(|i| format!("r{i}"))
            .collect::<Vec<_>>()
            .iter()
            .map(|s| s.as_str())
            .collect(),
    ));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());

    let exec = ChainExecutor::builder()
        .provider_router(provider)
        .tool_invoker(invoker)
        .agent_executor(agent_exec)
        .tool_registry(registry)
        .max_steps(3)
        .build();

    let flow = ChainFlow {
        id: "budget-test".into(),
        name: "Budget Test".into(),
        description: None,
        steps: vec![ChainStep::Map {
            over: "items".into(),
            step: Box::new(ChainStep::Prompt {
                template: "process {{_item}}".into(),
                inputs: vec!["_item".into()],
                output: "processed".into(),
            }),
            output: "results".into(),
        }],
    };

    let mut ctx = ChainContext::new();
    ctx.insert(
        "items".into(),
        json!(["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"]),
    );
    let err = exec.run(&flow, ctx).await.unwrap_err();
    assert!(
        matches!(err, ChainError::StepBudgetExceeded { .. }),
        "expected budget exceeded, got: {err}"
    );
}

// ── T10: Transform steps ──────────────────────────────────────────────────

#[tokio::test]
async fn transform_join_works() {
    let provider = Arc::new(ScriptedProvider::new(vec![]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "transform-test".into(),
        name: "Transform Test".into(),
        description: None,
        steps: vec![ChainStep::Transform {
            fn_ref: "join".into(),
            input: "tags".into(),
            output: "joined".into(),
        }],
    };

    let mut ctx = ChainContext::new();
    ctx.insert("tags".into(), json!(["rust", "async", "agents"]));
    let result = exec.run(&flow, ctx).await.unwrap();
    assert_eq!(
        result.context.get("joined"),
        Some(&json!("rust, async, agents"))
    );
}

// ── T11: ToolCall granted → executes ─────────────────────────────────────

#[tokio::test]
async fn tool_call_granted_executes() {
    let provider = Arc::new(ScriptedProvider::new(vec![]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );

    let registry = Arc::new(ToolRegistry::new());
    registry
        .register_tool(ToolDescriptor {
            id: "cascade.search".into(),
            name: "Search".into(),
            description: "Semantic search".into(),
            required_level: AccessLevel::Search,
        })
        .unwrap();
    registry.set_grants(
        "chain.executor",
        vec![ToolGrant {
            tool_id: "cascade.search".into(),
            level: AccessLevel::Search,
            approved: false,
        }],
    );

    let exec = make_executor(provider, invoker, agent_exec, registry);

    let flow = ChainFlow {
        id: "search-test".into(),
        name: "Search Test".into(),
        description: None,
        steps: vec![ChainStep::ToolCall {
            tool: "cascade.search".into(),
            args: json!({ "query": "{{query}}" }),
            output: "search_result".into(),
        }],
    };

    let mut ctx = ChainContext::new();
    ctx.insert("query".into(), json!("agent design patterns"));
    let result = exec.run(&flow, ctx).await.unwrap();
    assert_eq!(
        result.context.get("search_result"),
        Some(&json!("echo:cascade.search"))
    );
}

// ── P1: N=5 parallel children all complete, results collected ─────────────

#[tokio::test]
async fn parallel_n5_all_complete() {
    let provider = Arc::new(ScriptedProvider::new(vec!["r0", "r1", "r2", "r3", "r4"]));
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let steps: Vec<ChainStep> = (0..5)
        .map(|i| ChainStep::Prompt {
            template: format!("branch {i}"),
            inputs: vec![],
            output: format!("out_{i}"),
        })
        .collect();

    let flow = ChainFlow {
        id: "parallel-n5".into(),
        name: "Parallel N=5".into(),
        description: None,
        steps: vec![ChainStep::Parallel { steps }],
    };

    let result = exec.run(&flow, ChainContext::new()).await.unwrap();

    for i in 0..5 {
        assert!(
            result.context.contains_key(&format!("out_{i}")),
            "missing out_{i}"
        );
    }
    assert_eq!(result.context.len(), 5);
}

// ── P2: Wall-clock of parallel < sum-of-sequential ────────────────────────

#[tokio::test]
async fn parallel_wall_clock_less_than_sequential_sum() {
    use std::time::{Duration, Instant};

    const BRANCH_DELAY_MS: u64 = 50;
    const BRANCHES: usize = 4;

    struct SleepyProvider {
        delay: Duration,
    }

    #[async_trait::async_trait]
    impl ProviderRouter for SleepyProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            tokio::time::sleep(self.delay).await;
            Ok(StepOutcome {
                assistant_text: "done".into(),
                tool_calls: vec![],
                done: true,
                usage: crate::context::TokenUsage {
                    prompt_tokens: 1,
                    completion_tokens: 1,
                },
            })
        }
    }

    let delay = Duration::from_millis(BRANCH_DELAY_MS);
    let provider = Arc::new(SleepyProvider { delay });
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(SleepyProvider {
            delay: Duration::ZERO,
        }),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());
    let exec = make_executor(provider, invoker, agent_exec, registry);

    let steps: Vec<ChainStep> = (0..BRANCHES)
        .map(|i| ChainStep::Prompt {
            template: format!("branch {i}"),
            inputs: vec![],
            output: format!("par_{i}"),
        })
        .collect();

    let flow = ChainFlow {
        id: "parallel-timing".into(),
        name: "Parallel Timing".into(),
        description: None,
        steps: vec![ChainStep::Parallel { steps }],
    };

    let t0 = Instant::now();
    exec.run(&flow, ChainContext::new()).await.unwrap();
    let wall = t0.elapsed();

    let sequential_sum = delay * BRANCHES as u32;
    assert!(
        wall < sequential_sum,
        "parallel wall time ({wall:?}) should be < sequential sum ({sequential_sum:?})"
    );
    assert!(
        wall >= delay,
        "wall time ({wall:?}) must be >= one branch delay ({delay:?})"
    );
}

// ── P3: Semaphore cap respected ───────────────────────────────────────────

#[tokio::test]
async fn semaphore_cap_limits_concurrency() {
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::time::Duration;

    const CAP: usize = 2;
    const BRANCHES: usize = 6;

    let active = Arc::new(AtomicU32::new(0));
    let max_observed = Arc::new(AtomicU32::new(0));

    struct GaugedProvider {
        active: Arc<AtomicU32>,
        max_observed: Arc<AtomicU32>,
    }

    #[async_trait::async_trait]
    impl ProviderRouter for GaugedProvider {
        async fn step(
            &self,
            _spec: &AgentSpec,
            _ctx: &AgentRunContext,
        ) -> Result<StepOutcome, ExecutorError> {
            let now = self.active.fetch_add(1, Ordering::SeqCst) + 1;
            self.max_observed.fetch_max(now, Ordering::SeqCst);
            tokio::time::sleep(Duration::from_millis(20)).await;
            self.active.fetch_sub(1, Ordering::SeqCst);
            Ok(StepOutcome {
                assistant_text: "done".into(),
                tool_calls: vec![],
                done: true,
                usage: crate::context::TokenUsage {
                    prompt_tokens: 1,
                    completion_tokens: 1,
                },
            })
        }
    }

    let provider = Arc::new(GaugedProvider {
        active: Arc::clone(&active),
        max_observed: Arc::clone(&max_observed),
    });
    let invoker = Arc::new(EchoInvoker::new());
    let agent_exec = make_agent_executor(
        Arc::new(ScriptedProvider::new(vec![])),
        Arc::new(EchoInvoker::new()),
    );
    let registry = Arc::new(ToolRegistry::new());

    let exec = ChainExecutor::builder()
        .provider_router(provider)
        .tool_invoker(invoker)
        .agent_executor(agent_exec)
        .tool_registry(registry)
        .max_steps(50)
        .max_depth(4)
        .parallel_cap(CAP)
        .build();

    let steps: Vec<ChainStep> = (0..BRANCHES)
        .map(|i| ChainStep::Prompt {
            template: format!("branch {i}"),
            inputs: vec![],
            output: format!("cap_{i}"),
        })
        .collect();

    let flow = ChainFlow {
        id: "semaphore-cap".into(),
        name: "Semaphore Cap".into(),
        description: None,
        steps: vec![ChainStep::Parallel { steps }],
    };

    exec.run(&flow, ChainContext::new()).await.unwrap();

    let observed = max_observed.load(Ordering::SeqCst);
    assert!(
        observed <= CAP as u32,
        "max concurrent in-flight ({observed}) exceeded the cap ({CAP})"
    );
    assert_eq!(
        active.load(Ordering::SeqCst),
        0,
        "active count should be 0 after completion"
    );
}
