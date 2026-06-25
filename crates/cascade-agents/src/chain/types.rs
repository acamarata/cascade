//! ChainFlow types: steps, flow, errors, context, traces, result.

use std::collections::{HashMap, HashSet};
use std::path::Path;

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::executor::ExecutorError;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Default maximum number of steps (across all branches/maps) per `ChainFlow` run.
pub const CHAIN_DEFAULT_MAX_STEPS: u32 = 50;

/// Maximum parallel branches that may be in-flight simultaneously.
///
/// Derived at runtime from `available_parallelism()`. This constant defines the
/// upper ceiling; the actual cap is `min(PARALLEL_CAP_MAX, available - 2)` with a
/// floor of 1.
pub const PARALLEL_CAP_MAX: usize = 16;

/// Default maximum recursion/nesting depth for `Map` and `Parallel` steps.
pub const CHAIN_DEFAULT_MAX_DEPTH: u32 = 8;

// ── ChainStep ─────────────────────────────────────────────────────────────────

/// A single composable step in a `ChainFlow`.
///
/// All variants use struct style (NEVER unit or newtype) per repo rule.
/// Serializes to camelCase field names; the `kind` field discriminates.
///
/// # Step semantics
///
/// - `Prompt` — fill a prompt template using `inputs` from the context map,
///   call the LLM via the injected `ProviderRouter`, write result to `output`.
/// - `ToolCall` — invoke a tool by id with `args` (a context map template value);
///   Outbound tools park for approval rather than executing.
/// - `AgentTask` — spin up a sub-task via the injected `AgentExecutor` with the
///   given `role` and `goal` (may be a template string).
/// - `Map` — iterate `over` (a context key holding a JSON array) and execute
///   `step` for each element; collects results into the output key.
/// - `Branch` — evaluate `cond` (a context key; truthy = non-null, non-false,
///   non-empty string) and dispatch to `then_step` or `else_step`.
/// - `Parallel` — execute all sub-`steps` concurrently; fails fast on error.
/// - `Transform` — apply a named built-in transform function (`fn_ref`) to a
///   context value. First-party transforms: `join`, `first`, `last`, `length`,
///   `to_string`, `to_number`, `uppercase`, `lowercase`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum ChainStep {
    /// Call the LLM with a prompt template.
    Prompt {
        /// Handlebars-style `{{variable}}` template string.
        template: String,
        /// Context keys to bind into the template before the LLM call.
        #[serde(default)]
        inputs: Vec<String>,
        /// Context key to write the LLM response to.
        output: String,
    },
    /// Invoke a registered tool.
    ToolCall {
        /// Tool id matching `ToolDescriptor::id`.
        tool: String,
        /// JSON args; string values of the form `"{{key}}"` are interpolated
        /// from the context map before invocation.
        args: Value,
        /// Context key to write the tool result to.
        output: String,
    },
    /// Dispatch an `AgentTask` via the embedded `AgentExecutor`.
    AgentTask {
        /// The agent role to assign.
        role: crate::spec::AgentRole,
        /// Goal string; `{{key}}` patterns are interpolated from the context map.
        goal: String,
        /// Context key to write the agent's final output to.
        output: String,
    },
    /// Map a step over each element of a context-key array.
    Map {
        /// Context key holding a JSON array to iterate over.
        over: String,
        /// The step to execute for each element; the element is bound to `_item`.
        step: Box<ChainStep>,
        /// Context key to collect results into (a JSON array).
        output: String,
    },
    /// Branch on a context-key value.
    Branch {
        /// Context key to evaluate (truthy = non-null/false/empty string).
        cond: String,
        /// Step to run when `cond` is truthy.
        then_step: Box<ChainStep>,
        /// Optional step to run when `cond` is falsy.
        #[serde(skip_serializing_if = "Option::is_none")]
        else_step: Option<Box<ChainStep>>,
    },
    /// Run steps concurrently; collect all results before continuing.
    Parallel {
        /// Sub-steps to execute in parallel.
        steps: Vec<ChainStep>,
    },
    /// Apply a named built-in transform to a context value.
    Transform {
        /// Name of the built-in transform (e.g. `"join"`, `"uppercase"`).
        fn_ref: String,
        /// Context key to read input from.
        input: String,
        /// Context key to write transformed result to.
        output: String,
    },
}

// ── ChainFlow ─────────────────────────────────────────────────────────────────

/// A declarative, ordered multi-step workflow.
///
/// `ChainFlow` is the top-level unit loaded from `library/chains/*.yaml` (or
/// constructed in code). Run it with `ChainExecutor::run`.
///
/// # YAML format
/// ```yaml
/// id: research-summarize-draft
/// name: Research → Summarize → Draft
/// steps:
///   - kind: agentTask
///     role: researcher
///     goal: "Research {{topic}}"
///     output: raw_research
///   - kind: prompt
///     template: "Summarise the following research:\n{{raw_research}}"
///     inputs: [raw_research]
///     output: summary
///   - kind: prompt
///     template: "Draft a blog post based on: {{summary}}"
///     inputs: [summary]
///     output: draft
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ChainFlow {
    /// Stable unique identifier for this chain (e.g. `"research-summarize-draft"`).
    pub id: String,
    /// Human-readable display name.
    pub name: String,
    /// Ordered list of steps to execute.
    pub steps: Vec<ChainStep>,
    /// Optional description / docstring.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
}

impl ChainFlow {
    /// Validate the flow before execution.
    ///
    /// Checks:
    /// 1. No duplicate `output` keys in the linear steps (would silently overwrite).
    /// 2. `inputs` referenced in `Prompt` steps have a prior producer (either a
    ///    declared `output` of an earlier step or in the initial context).
    /// 3. No cycles — since `ChainStep` is a tree (no named step references back
    ///    into the flow), pure structural cycle-check on the step tree itself
    ///    (detect infinitely-nested `Box` types — impossible in practice, but
    ///    validated against depth overflow).
    ///
    /// Returns `Ok(())` or a list of `ChainValidationError` values.
    pub fn validate(&self) -> Result<(), Vec<ChainValidationError>> {
        let mut errors: Vec<ChainValidationError> = vec![];
        let mut seen_outputs: HashSet<String> = HashSet::new();

        for step in &self.steps {
            self.validate_step(step, &mut seen_outputs, &mut errors, 0);
        }

        if errors.is_empty() {
            Ok(())
        } else {
            Err(errors)
        }
    }

    pub(crate) fn validate_step(
        &self,
        step: &ChainStep,
        seen: &mut HashSet<String>,
        errors: &mut Vec<ChainValidationError>,
        depth: u32,
    ) {
        if depth > CHAIN_DEFAULT_MAX_DEPTH {
            errors.push(ChainValidationError::CycleDetected {
                context: format!("nesting depth exceeded {} levels", CHAIN_DEFAULT_MAX_DEPTH),
            });
            return;
        }

        match step {
            ChainStep::Prompt { inputs, output, .. } => {
                for key in inputs {
                    if !seen.contains(key.as_str()) {
                        errors.push(ChainValidationError::UnresolvedRef {
                            step_kind: "prompt".into(),
                            key: key.clone(),
                        });
                    }
                }
                if seen.contains(output.as_str()) {
                    errors.push(ChainValidationError::DuplicateOutput {
                        key: output.clone(),
                    });
                }
                seen.insert(output.clone());
            }
            ChainStep::ToolCall { output, .. } => {
                if seen.contains(output.as_str()) {
                    errors.push(ChainValidationError::DuplicateOutput {
                        key: output.clone(),
                    });
                }
                seen.insert(output.clone());
            }
            ChainStep::AgentTask { output, .. } => {
                if seen.contains(output.as_str()) {
                    errors.push(ChainValidationError::DuplicateOutput {
                        key: output.clone(),
                    });
                }
                seen.insert(output.clone());
            }
            ChainStep::Map {
                over,
                step: inner,
                output,
                ..
            } => {
                if !seen.contains(over.as_str()) {
                    errors.push(ChainValidationError::UnresolvedRef {
                        step_kind: "map".into(),
                        key: over.clone(),
                    });
                }
                // inner step is validated in a child scope (has access to _item)
                let mut child_seen = seen.clone();
                child_seen.insert("_item".into());
                self.validate_step(inner, &mut child_seen, errors, depth + 1);
                if seen.contains(output.as_str()) {
                    errors.push(ChainValidationError::DuplicateOutput {
                        key: output.clone(),
                    });
                }
                seen.insert(output.clone());
            }
            ChainStep::Branch {
                cond,
                then_step,
                else_step,
                ..
            } => {
                if !seen.contains(cond.as_str()) {
                    errors.push(ChainValidationError::UnresolvedRef {
                        step_kind: "branch".into(),
                        key: cond.clone(),
                    });
                }
                let mut then_seen = seen.clone();
                self.validate_step(then_step, &mut then_seen, errors, depth + 1);
                if let Some(else_s) = else_step {
                    let mut else_seen = seen.clone();
                    self.validate_step(else_s, &mut else_seen, errors, depth + 1);
                }
            }
            ChainStep::Parallel { steps } => {
                for s in steps {
                    let mut par_seen = seen.clone();
                    self.validate_step(s, &mut par_seen, errors, depth + 1);
                }
            }
            ChainStep::Transform { input, output, .. } => {
                if !seen.contains(input.as_str()) {
                    errors.push(ChainValidationError::UnresolvedRef {
                        step_kind: "transform".into(),
                        key: input.clone(),
                    });
                }
                if seen.contains(output.as_str()) {
                    errors.push(ChainValidationError::DuplicateOutput {
                        key: output.clone(),
                    });
                }
                seen.insert(output.clone());
            }
        }
    }

    /// Load a `ChainFlow` from a YAML file at `path`.
    pub fn load_from_file(path: &Path) -> Result<Self, ChainError> {
        let content = std::fs::read_to_string(path).map_err(|e| ChainError::YamlLoad {
            path: path.display().to_string(),
            message: e.to_string(),
        })?;
        let flow: ChainFlow = serde_yaml::from_str(&content).map_err(|e| ChainError::YamlLoad {
            path: path.display().to_string(),
            message: e.to_string(),
        })?;
        Ok(flow)
    }

    /// Load all `ChainFlow` values from YAML files in `dir`.
    pub fn load_library(dir: &Path) -> Result<Vec<Self>, ChainError> {
        let mut flows = vec![];
        let rd = std::fs::read_dir(dir).map_err(|e| ChainError::YamlLoad {
            path: dir.display().to_string(),
            message: e.to_string(),
        })?;
        for entry in rd.flatten() {
            let p = entry.path();
            if p.extension().and_then(|e| e.to_str()) == Some("yaml")
                || p.extension().and_then(|e| e.to_str()) == Some("yml")
            {
                flows.push(ChainFlow::load_from_file(&p)?);
            }
        }
        Ok(flows)
    }
}

// ── ChainValidationError ──────────────────────────────────────────────────────

/// A validation problem found in a `ChainFlow` before execution.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum ChainValidationError {
    /// A context key referenced as input does not have a prior producer.
    UnresolvedRef {
        /// The step kind that referenced the unresolved key.
        step_kind: String,
        /// The missing context key.
        key: String,
    },
    /// Two steps write to the same context key.
    DuplicateOutput {
        /// The duplicated key.
        key: String,
    },
    /// Nesting depth exceeded (structural cycle guard).
    CycleDetected {
        /// Additional context.
        context: String,
    },
}

// ── ChainError ────────────────────────────────────────────────────────────────

/// Errors produced by `ChainExecutor::run`.
#[derive(Debug, thiserror::Error)]
pub enum ChainError {
    #[error("chain validation failed: {0} issue(s)")]
    ValidationFailed(String),

    #[error("step budget ({max}) exceeded at step {at}")]
    StepBudgetExceeded { max: u32, at: u32 },

    #[error("nesting depth ({max}) exceeded")]
    DepthExceeded { max: u32 },

    #[error("tool '{tool}' requires approval before executing (outbound gate)")]
    NeedsApproval { tool: String },

    #[error("tool '{tool}' was denied: {reason}")]
    ToolDenied { tool: String, reason: String },

    #[error("executor error: {0}")]
    ExecutorError(#[from] ExecutorError),

    #[error("tool invocation failed for '{tool}': {message}")]
    ToolFailed { tool: String, message: String },

    #[error("YAML load failed at '{path}': {message}")]
    YamlLoad { path: String, message: String },

    #[error("unknown transform '{fn_ref}'")]
    UnknownTransform { fn_ref: String },

    #[error("map target '{key}' is not a JSON array")]
    MapNotArray { key: String },

    #[error("I/O error: {0}")]
    Io(String),
}

// ── ChainContext ──────────────────────────────────────────────────────────────

/// The mutable context map threaded through a `ChainFlow` execution.
///
/// Keys are output names; values are arbitrary JSON. Steps read their
/// `inputs` from this map and write their `output` back into it.
pub type ChainContext = HashMap<String, Value>;

// ── ChainStepTrace ────────────────────────────────────────────────────────────

/// A per-step execution record, useful for debugging and observability.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ChainStepTrace {
    /// Zero-based index of this step in the linear execution order.
    pub step_index: u32,
    /// The `kind` discriminant (e.g. `"prompt"`, `"toolCall"`).
    pub kind: String,
    /// Context key written by this step.
    pub output_key: String,
    /// `true` if the step completed successfully.
    pub success: bool,
    /// Error message, if any.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

// ── Internal type alias ───────────────────────────────────────────────────────

/// Join handle carrying one parallel branch result.
pub(crate) type BranchHandle =
    tokio::task::JoinHandle<Result<(ChainContext, Vec<ChainStepTrace>), ChainError>>;

// ── ChainResult ───────────────────────────────────────────────────────────────

/// The result of a completed `ChainFlow` execution.
#[derive(Debug, Clone)]
pub struct ChainResult {
    /// The final context map (all accumulated outputs).
    pub context: ChainContext,
    /// Per-step execution traces.
    pub traces: Vec<ChainStepTrace>,
}
