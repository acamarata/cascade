//! Internal helper functions for ChainFlow execution.
//!
//! Purpose: template rendering, argument interpolation, truthiness checks,
//!   built-in transforms, and minimal AgentSpec factories used by ChainExecutor.
//! Inputs: ChainContext, ChainStep, Value, fn_ref strings.
//! Outputs: rendered strings/values, minimal AgentSpec instances, ChainError on failure.
//! Constraints: no async; pure functions only.

use serde_json::Value;

use crate::spec::{AgentRole, AgentSpec, Runtime};
use cascade_types::agent::Tier;

use super::types::{ChainContext, ChainError, ChainStep};

// ── Step output key ───────────────────────────────────────────────────────────

/// Returns the output key of a step (for Map collection).
pub(crate) fn step_output_key(step: &ChainStep) -> Option<String> {
    match step {
        ChainStep::Prompt { output, .. } => Some(output.clone()),
        ChainStep::ToolCall { output, .. } => Some(output.clone()),
        ChainStep::AgentTask { output, .. } => Some(output.clone()),
        ChainStep::Map { output, .. } => Some(output.clone()),
        ChainStep::Transform { output, .. } => Some(output.clone()),
        ChainStep::Branch { .. } | ChainStep::Parallel { .. } => None,
    }
}

// ── Template rendering ────────────────────────────────────────────────────────

/// Render `{{key}}` placeholders in `template` from `ctx`.
pub(crate) fn render_template(template: &str, ctx: &ChainContext) -> String {
    let mut result = template.to_string();
    for (key, val) in ctx {
        let placeholder = format!("{{{{{key}}}}}");
        let replacement = match val {
            Value::String(s) => s.clone(),
            other => other.to_string(),
        };
        result = result.replace(&placeholder, &replacement);
    }
    result
}

/// Interpolate `{{key}}` placeholders in JSON arg values.
pub(crate) fn render_args(args: &Value, ctx: &ChainContext) -> Value {
    match args {
        Value::String(s) => Value::String(render_template(s, ctx)),
        Value::Object(map) => {
            let rendered: serde_json::Map<String, Value> = map
                .iter()
                .map(|(k, v)| (k.clone(), render_args(v, ctx)))
                .collect();
            Value::Object(rendered)
        }
        Value::Array(arr) => Value::Array(arr.iter().map(|v| render_args(v, ctx)).collect()),
        other => other.clone(),
    }
}

// ── Truthiness ────────────────────────────────────────────────────────────────

/// Truthiness check for Branch conditions.
pub(crate) fn is_truthy(val: &Value) -> bool {
    match val {
        Value::Null => false,
        Value::Bool(b) => *b,
        Value::String(s) => !s.is_empty(),
        Value::Number(n) => n.as_f64().unwrap_or(0.0) != 0.0,
        Value::Array(a) => !a.is_empty(),
        Value::Object(o) => !o.is_empty(),
    }
}

// ── Built-in transforms ───────────────────────────────────────────────────────

/// Apply a named built-in transform.
pub(crate) fn apply_transform(fn_ref: &str, input: &Value) -> Result<Value, ChainError> {
    match fn_ref {
        "join" => {
            let items = input
                .as_array()
                .ok_or_else(|| ChainError::UnknownTransform {
                    fn_ref: "join (input must be array)".into(),
                })?;
            let joined = items
                .iter()
                .map(|v| match v {
                    Value::String(s) => s.clone(),
                    other => other.to_string(),
                })
                .collect::<Vec<_>>()
                .join(", ");
            Ok(Value::String(joined))
        }
        "first" => {
            let items = input
                .as_array()
                .ok_or_else(|| ChainError::UnknownTransform {
                    fn_ref: "first (input must be array)".into(),
                })?;
            Ok(items.first().cloned().unwrap_or(Value::Null))
        }
        "last" => {
            let items = input
                .as_array()
                .ok_or_else(|| ChainError::UnknownTransform {
                    fn_ref: "last (input must be array)".into(),
                })?;
            Ok(items.last().cloned().unwrap_or(Value::Null))
        }
        "length" => {
            let len = match input {
                Value::Array(a) => a.len(),
                Value::String(s) => s.len(),
                Value::Object(o) => o.len(),
                _ => 0,
            };
            Ok(Value::Number(serde_json::Number::from(len as u64)))
        }
        "to_string" => Ok(Value::String(match input {
            Value::String(s) => s.clone(),
            other => other.to_string(),
        })),
        "to_number" => {
            let s = match input {
                Value::String(s) => s.clone(),
                Value::Number(n) => return Ok(Value::Number(n.clone())),
                _ => return Ok(Value::Null),
            };
            let n: f64 = s.parse().unwrap_or(0.0);
            Ok(Value::Number(
                serde_json::Number::from_f64(n).unwrap_or(serde_json::Number::from(0)),
            ))
        }
        "uppercase" => {
            let s = input.as_str().unwrap_or("").to_uppercase();
            Ok(Value::String(s))
        }
        "lowercase" => {
            let s = input.as_str().unwrap_or("").to_lowercase();
            Ok(Value::String(s))
        }
        other => Err(ChainError::UnknownTransform {
            fn_ref: other.to_string(),
        }),
    }
}

// ── Minimal AgentSpec factories ───────────────────────────────────────────────

/// Build a minimal `AgentSpec` for LLM calls within the chain executor.
pub(crate) fn minimal_spec() -> AgentSpec {
    AgentSpec {
        id: "chain.executor".into(),
        version: "1.0.0".into(),
        name: "Chain Executor".into(),
        role: crate::spec::AgentRole::Generic,
        tier: Tier::T2,
        capabilities: vec![],
        model_pref: None,
        system_prompt_ref: None,
        tool_grants_ref: None,
        runtime: Runtime::Native,
        soul_ref: None,
    }
}

/// Build a minimal `AgentSpec` for a given role (used in AgentTask steps).
pub(crate) fn minimal_spec_for_role(role: AgentRole) -> AgentSpec {
    AgentSpec {
        id: format!("chain.{role:?}").to_lowercase(),
        version: "1.0.0".into(),
        name: format!("{role:?} (chain)"),
        role,
        tier: Tier::T2,
        capabilities: vec![],
        model_pref: None,
        system_prompt_ref: None,
        tool_grants_ref: None,
        runtime: Runtime::Native,
        soul_ref: None,
    }
}
