//! ChainExecutor and ChainExecutorBuilder — runs ChainFlow values.
//!
//! Purpose: injectable executor that threads context across steps, enforces
//!   budget/depth guards, runs parallel branches via Tokio tasks + semaphore.
//! Inputs: ChainFlow, ChainContext, ProviderRouter, ToolInvoker, AgentExecutor.
//! Outputs: ChainResult or ChainError.
//! Constraints: max_steps (default 50), max_depth (default 8), parallel semaphore.

use std::collections::HashSet;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;

use serde_json::Value;
use tokio::sync::Semaphore;

use crate::context::{AgentRunContextBuilder, ToolCall};
use crate::executor::{AgentExecutor, ProviderRouter, ToolInvoker};
use crate::grants::{AccessLevel, GrantDecision};
use crate::task::TaskStatus;
use crate::tool_registry::ToolRegistry;

use super::helpers::{
    apply_transform, is_truthy, minimal_spec, minimal_spec_for_role, render_args, render_template,
    step_output_key,
};
use super::types::{
    BranchHandle, ChainContext, ChainError, ChainFlow, ChainResult, ChainStep, ChainStepTrace,
    ChainValidationError, CHAIN_DEFAULT_MAX_DEPTH, CHAIN_DEFAULT_MAX_STEPS, PARALLEL_CAP_MAX,
};

// ── Concurrency cap ───────────────────────────────────────────────────────────

/// Compute the runtime concurrency cap: `min(PARALLEL_CAP_MAX, cpus - 2)`, floor 1.
pub fn parallel_concurrency_cap() -> usize {
    let cpus = std::thread::available_parallelism()
        .map(|n| n.get())
        .unwrap_or(4);
    let cap = cpus.saturating_sub(2);
    cap.clamp(1, PARALLEL_CAP_MAX)
}

// ── ChainExecutor ─────────────────────────────────────────────────────────────

/// Executes `ChainFlow` values with injectable provider, invoker, and executor.
///
/// # Construction
/// Use `ChainExecutor::builder()`.
///
/// # Approval gate
/// When a `ToolCall` step targets a tool with `AccessLevel::Outbound` and
/// the tool is not pre-approved, `ChainExecutor::run` returns
/// `Err(ChainError::NeedsApproval { .. })` immediately without executing the
/// tool. The caller must obtain approval and resume.
///
/// # Budget guards
/// `max_steps` caps the total number of step-executions (counting Map iterations
/// and Parallel sub-steps individually). `max_depth` caps recursion in
/// Map/Branch/Parallel nesting.
pub struct ChainExecutor {
    pub(crate) provider: Arc<dyn ProviderRouter>,
    pub(crate) invoker: Arc<dyn ToolInvoker>,
    pub(crate) agent_executor: Arc<AgentExecutor>,
    pub(crate) tool_registry: Arc<ToolRegistry>,
    pub(crate) max_steps: u32,
    pub(crate) max_depth: u32,
    /// Limits the number of `Parallel` branches running concurrently.
    pub(crate) parallel_sem: Arc<Semaphore>,
}

impl ChainExecutor {
    /// Return a builder.
    pub fn builder() -> ChainExecutorBuilder {
        ChainExecutorBuilder::default()
    }

    /// Run `flow` with `initial_ctx` as the starting context map.
    ///
    /// Validates the flow first (refs + cycles); rejects invalid flows before
    /// any I/O. Threads context outputs → inputs across steps.
    pub async fn run(
        &self,
        flow: &ChainFlow,
        mut initial_ctx: ChainContext,
    ) -> Result<ChainResult, ChainError> {
        // Seed seen outputs from initial_ctx keys so validate_step doesn't
        // reject refs that the caller pre-populated.
        {
            let initial_keys: HashSet<String> = initial_ctx.keys().cloned().collect();
            let errors = self.validate_with_initial_keys(flow, &initial_keys);
            if !errors.is_empty() {
                return Err(ChainError::ValidationFailed(
                    errors
                        .iter()
                        .map(|e| format!("{e:?}"))
                        .collect::<Vec<_>>()
                        .join("; "),
                ));
            }
        }

        let mut traces: Vec<ChainStepTrace> = vec![];
        let mut step_counter: u32 = 0;

        for step in &flow.steps {
            self.exec_step(step, &mut initial_ctx, &mut traces, &mut step_counter, 0)
                .await?;
        }

        Ok(ChainResult {
            context: initial_ctx,
            traces,
        })
    }

    /// Validate while treating `initial_keys` as already-resolved outputs.
    fn validate_with_initial_keys(
        &self,
        flow: &ChainFlow,
        initial_keys: &HashSet<String>,
    ) -> Vec<ChainValidationError> {
        let mut errors: Vec<ChainValidationError> = vec![];
        let mut seen: HashSet<String> = initial_keys.clone();
        for step in &flow.steps {
            flow.validate_step(step, &mut seen, &mut errors, 0);
        }
        errors
    }

    /// Recursively execute one step.
    ///
    /// Uses `Box::pin` to allow async recursion (Rust requires explicit boxing
    /// for recursive async functions).
    fn exec_step<'a>(
        &'a self,
        step: &'a ChainStep,
        ctx: &'a mut ChainContext,
        traces: &'a mut Vec<ChainStepTrace>,
        counter: &'a mut u32,
        depth: u32,
    ) -> std::pin::Pin<Box<dyn std::future::Future<Output = Result<(), ChainError>> + Send + 'a>>
    {
        Box::pin(async move {
            self.exec_step_inner(step, ctx, traces, counter, depth)
                .await
        })
    }

    async fn exec_step_inner(
        &self,
        step: &ChainStep,
        ctx: &mut ChainContext,
        traces: &mut Vec<ChainStepTrace>,
        counter: &mut u32,
        depth: u32,
    ) -> Result<(), ChainError> {
        if *counter >= self.max_steps {
            return Err(ChainError::StepBudgetExceeded {
                max: self.max_steps,
                at: *counter,
            });
        }
        if depth > self.max_depth {
            return Err(ChainError::DepthExceeded {
                max: self.max_depth,
            });
        }
        *counter += 1;

        let idx = *counter - 1;

        match step {
            ChainStep::Prompt {
                template,
                inputs,
                output,
            } => {
                let rendered = render_template(template, ctx);
                let spec = minimal_spec();
                let run_ctx = AgentRunContextBuilder::new(spec.clone(), rendered.clone())
                    .build()
                    .await;
                let outcome = self.provider.step(&spec, &run_ctx).await?;
                let result_text = outcome.assistant_text;
                ctx.insert(output.clone(), Value::String(result_text.clone()));
                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "prompt".into(),
                    output_key: output.clone(),
                    success: true,
                    error: None,
                });
                let _ = inputs; // already interpolated via template rendering
            }
            ChainStep::ToolCall { tool, args, output } => {
                // Grant check
                let required_level = self
                    .tool_registry
                    .get_tool(tool)
                    .map(|d| d.required_level)
                    .unwrap_or(AccessLevel::Write);

                let decision = self
                    .tool_registry
                    .check("chain.executor", tool, required_level);

                match decision {
                    GrantDecision::NeedsApproval { .. } => {
                        traces.push(ChainStepTrace {
                            step_index: idx,
                            kind: "toolCall".into(),
                            output_key: output.clone(),
                            success: false,
                            error: Some("needs approval".into()),
                        });
                        return Err(ChainError::NeedsApproval { tool: tool.clone() });
                    }
                    GrantDecision::Denied { reason } => {
                        traces.push(ChainStepTrace {
                            step_index: idx,
                            kind: "toolCall".into(),
                            output_key: output.clone(),
                            success: false,
                            error: Some(reason.clone()),
                        });
                        return Err(ChainError::ToolDenied {
                            tool: tool.clone(),
                            reason,
                        });
                    }
                    GrantDecision::Granted => {}
                }

                let rendered_args = render_args(args, ctx);
                let call = ToolCall {
                    tool_id: tool.clone(),
                    args: rendered_args,
                    call_id: format!("chain-step-{idx}"),
                };
                let result =
                    self.invoker
                        .invoke(&call)
                        .await
                        .map_err(|e| ChainError::ToolFailed {
                            tool: tool.clone(),
                            message: e.to_string(),
                        })?;
                ctx.insert(output.clone(), Value::String(result));
                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "toolCall".into(),
                    output_key: output.clone(),
                    success: true,
                    error: None,
                });
            }
            ChainStep::AgentTask { role, goal, output } => {
                let rendered_goal = render_template(goal, ctx);
                let task = crate::task::AgentTask::new_root(rendered_goal.clone(), *role);
                let spec = minimal_spec_for_role(*role);
                let result = self.agent_executor.run_task(spec, task).await?;
                let out_val = match result.status {
                    TaskStatus::Done => Value::String(format!("agent-done:{}", result.id)),
                    _ => Value::String(format!("agent-status:{}", result.status)),
                };
                ctx.insert(output.clone(), out_val);
                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "agentTask".into(),
                    output_key: output.clone(),
                    success: true,
                    error: None,
                });
            }
            ChainStep::Map {
                over,
                step: inner,
                output,
            } => {
                let array = ctx
                    .get(over)
                    .cloned()
                    .ok_or_else(|| ChainError::MapNotArray { key: over.clone() })?;
                let items = match &array {
                    Value::Array(arr) => arr.clone(),
                    _ => return Err(ChainError::MapNotArray { key: over.clone() }),
                };
                let mut collected: Vec<Value> = vec![];
                for item in items {
                    let mut item_ctx = ctx.clone();
                    item_ctx.insert("_item".into(), item);
                    self.exec_step(inner, &mut item_ctx, traces, counter, depth + 1)
                        .await?;
                    let inner_output_key = step_output_key(inner);
                    if let Some(val) = inner_output_key.and_then(|k| item_ctx.get(&k)) {
                        collected.push(val.clone());
                    }
                }
                ctx.insert(output.clone(), Value::Array(collected));
                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "map".into(),
                    output_key: output.clone(),
                    success: true,
                    error: None,
                });
            }
            ChainStep::Branch {
                cond,
                then_step,
                else_step,
            } => {
                let cond_val = ctx.get(cond).cloned().unwrap_or(Value::Null);
                let truthy = is_truthy(&cond_val);
                if truthy {
                    self.exec_step(then_step, ctx, traces, counter, depth + 1)
                        .await?;
                } else if let Some(else_s) = else_step {
                    self.exec_step(else_s, ctx, traces, counter, depth + 1)
                        .await?;
                }
                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "branch".into(),
                    output_key: String::new(),
                    success: true,
                    error: None,
                });
            }
            ChainStep::Parallel { steps } => {
                // True concurrent execution: each branch gets a snapshot of the
                // current context and executes independently. The semaphore caps
                // in-flight tasks to `parallel_concurrency_cap()`.
                let shared_counter = Arc::new(AtomicU32::new(*counter));
                let branch_count = steps.len();
                let sem = Arc::clone(&self.parallel_sem);

                let mut join_handles: Vec<BranchHandle> = Vec::with_capacity(branch_count);

                for s in steps {
                    let branch_ctx = ctx.clone();
                    let step_owned = s.clone();
                    let executor = Arc::new(ChainExecutor {
                        provider: Arc::clone(&self.provider),
                        invoker: Arc::clone(&self.invoker),
                        agent_executor: Arc::clone(&self.agent_executor),
                        tool_registry: Arc::clone(&self.tool_registry),
                        max_steps: self.max_steps,
                        max_depth: self.max_depth,
                        parallel_sem: Arc::clone(&self.parallel_sem),
                    });
                    let sem_clone = Arc::clone(&sem);
                    let counter_clone = Arc::clone(&shared_counter);
                    let branch_depth = depth + 1;

                    join_handles.push(tokio::spawn(async move {
                        let _permit = sem_clone
                            .acquire()
                            .await
                            .map_err(|_| ChainError::Io("semaphore closed".into()))?;

                        let mut branch_ctx = branch_ctx;
                        let mut branch_traces: Vec<ChainStepTrace> = vec![];
                        let mut local_counter = counter_clone.load(Ordering::Relaxed);

                        executor
                            .exec_step(
                                &step_owned,
                                &mut branch_ctx,
                                &mut branch_traces,
                                &mut local_counter,
                                branch_depth,
                            )
                            .await?;

                        counter_clone.fetch_max(local_counter, Ordering::Relaxed);
                        Ok((branch_ctx, branch_traces))
                    }));
                }

                let mut max_counter = *counter;
                for handle in join_handles {
                    let (branch_ctx, branch_traces) = handle
                        .await
                        .map_err(|e| ChainError::Io(format!("parallel join error: {e}")))??;

                    for (k, v) in branch_ctx {
                        ctx.entry(k).or_insert(v);
                    }
                    traces.extend(branch_traces);
                    max_counter = max_counter.max(shared_counter.load(Ordering::Relaxed));
                }
                *counter = max_counter;

                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "parallel".into(),
                    output_key: String::new(),
                    success: true,
                    error: None,
                });
            }
            ChainStep::Transform {
                fn_ref,
                input,
                output,
            } => {
                let input_val = ctx.get(input).cloned().unwrap_or(Value::Null);
                let result = apply_transform(fn_ref, &input_val)?;
                ctx.insert(output.clone(), result);
                traces.push(ChainStepTrace {
                    step_index: idx,
                    kind: "transform".into(),
                    output_key: output.clone(),
                    success: true,
                    error: None,
                });
            }
        }

        Ok(())
    }
}

// ── ChainExecutorBuilder ──────────────────────────────────────────────────────

/// Builder for `ChainExecutor`.
pub struct ChainExecutorBuilder {
    provider: Option<Arc<dyn ProviderRouter>>,
    invoker: Option<Arc<dyn ToolInvoker>>,
    agent_executor: Option<Arc<AgentExecutor>>,
    tool_registry: Arc<ToolRegistry>,
    max_steps: u32,
    max_depth: u32,
    /// Override the concurrency cap (default: `parallel_concurrency_cap()`).
    parallel_cap: Option<usize>,
}

impl Default for ChainExecutorBuilder {
    fn default() -> Self {
        Self {
            provider: None,
            invoker: None,
            agent_executor: None,
            tool_registry: Arc::new(ToolRegistry::new()),
            max_steps: CHAIN_DEFAULT_MAX_STEPS,
            max_depth: CHAIN_DEFAULT_MAX_DEPTH,
            parallel_cap: None,
        }
    }
}

impl ChainExecutorBuilder {
    pub fn provider_router(mut self, p: Arc<dyn ProviderRouter>) -> Self {
        self.provider = Some(p);
        self
    }
    pub fn tool_invoker(mut self, i: Arc<dyn ToolInvoker>) -> Self {
        self.invoker = Some(i);
        self
    }
    pub fn agent_executor(mut self, e: Arc<AgentExecutor>) -> Self {
        self.agent_executor = Some(e);
        self
    }
    pub fn tool_registry(mut self, r: Arc<ToolRegistry>) -> Self {
        self.tool_registry = r;
        self
    }
    pub fn max_steps(mut self, n: u32) -> Self {
        self.max_steps = n;
        self
    }
    pub fn max_depth(mut self, n: u32) -> Self {
        self.max_depth = n;
        self
    }
    /// Override the maximum number of parallel branches in-flight simultaneously.
    ///
    /// Useful in tests to exercise the semaphore cap with a small value (e.g. 2).
    /// If not called, the cap is derived from `available_parallelism()`.
    pub fn parallel_cap(mut self, cap: usize) -> Self {
        self.parallel_cap = Some(cap.max(1));
        self
    }

    /// Build the `ChainExecutor`.
    ///
    /// # Panics
    /// Panics if `provider_router`, `tool_invoker`, or `agent_executor` was not set.
    pub fn build(self) -> ChainExecutor {
        let cap = self.parallel_cap.unwrap_or_else(parallel_concurrency_cap);
        ChainExecutor {
            provider: self
                .provider
                .expect("ChainExecutorBuilder: provider_router must be set"),
            invoker: self
                .invoker
                .expect("ChainExecutorBuilder: tool_invoker must be set"),
            agent_executor: self
                .agent_executor
                .expect("ChainExecutorBuilder: agent_executor must be set"),
            tool_registry: self.tool_registry,
            max_steps: self.max_steps,
            max_depth: self.max_depth,
            parallel_sem: Arc::new(Semaphore::new(cap)),
        }
    }
}
