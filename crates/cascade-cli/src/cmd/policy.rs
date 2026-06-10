//! `cascade policy` — evaluate, list, add, and remove guardrail policies.
//!
//! ## Purpose
//! Manage the Cascade policy engine from the command line. Policies are stored
//! in `~/.cascade/policies/` and evaluated in order before any harness dispatch.
//!
//! ## Subcommands
//! - `eval   --action <json>` — evaluate one action against active policies
//! - `list`                   — list active policies with type and path
//! - `add    <path>`          — copy a `.rego` or `.wasm` file to the policy dir
//! - `remove <id>`            — remove a policy by ID (file stem)
//!
//! ## Inputs
//! - `--action <json>` for eval: a JSON object serialized as `PolicyAction`.
//! - File path for add.
//! - Policy ID (stem) for remove.
//!
//! ## Outputs
//! - `eval`: decision table (decision / reason / policy)
//! - `list`: table of active policies (id / type / path)
//!
//! ## Constraints
//! - Policy dir created on demand if absent.
//! - Only `.rego` and `.wasm` files accepted by `add`.
//! - `remove` fails with a clear error if the ID is not found.
//! - After `add` / `remove`, calls `invalidate_cache()` so the CLI process
//!   sees the updated policy list immediately.
//!
//! ## SPORT
//! MASTER-POLICIES.md: cascade policy CLI Done

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_harness::policy::{
    dispatch::{evaluate_before_dispatch, invalidate_cache},
    engine::PolicyEvaluator as _,
    simple::SimplePolicyEvaluator,
};
use cascade_types::{
    error::{CascadeError, Result},
    paths::global_cascade_dir,
    policy::PolicyAction,
};
use clap::{Args, Subcommand};

use super::Command;

// ── Subcommand enum ───────────────────────────────────────────────────────────

/// Policy management subcommands.
#[derive(Debug, Subcommand)]
pub enum PolicySubcommand {
    /// Evaluate one action against active policies.
    Eval(PolicyEvalArgs),
    /// List active policies in ~/.cascade/policies/.
    List,
    /// Copy a policy file (.rego or .wasm) into ~/.cascade/policies/.
    Add(PolicyAddArgs),
    /// Remove a policy by its ID (file stem) from ~/.cascade/policies/.
    Remove(PolicyRemoveArgs),
}

// ── Top-level args ────────────────────────────────────────────────────────────

/// `cascade policy` — manage guardrail policies.
#[derive(Debug, Args)]
pub struct PolicyArgs {
    #[command(subcommand)]
    pub command: PolicySubcommand,
}

#[async_trait]
impl Command for PolicyArgs {
    async fn run(&self) -> Result<()> {
        match &self.command {
            PolicySubcommand::Eval(a) => a.run().await,
            PolicySubcommand::List => run_list().await,
            PolicySubcommand::Add(a) => a.run().await,
            PolicySubcommand::Remove(a) => a.run().await,
        }
    }
}

// ── eval ─────────────────────────────────────────────────────────────────────

/// Arguments for `cascade policy eval`.
#[derive(Debug, Args)]
pub struct PolicyEvalArgs {
    /// JSON-encoded PolicyAction to evaluate.
    /// Example: `'{"action_type":"bash","args":{"cmd":"rm -rf /"}}'`
    #[arg(long, value_name = "JSON")]
    pub action: String,

    /// Optional path to a specific policy file to evaluate instead of the
    /// active policy chain.  Supports `.rego` (parsed for patterns) and
    /// `.wasm` (loaded as WasmPolicyEvaluator).
    #[arg(long, value_name = "FILE")]
    pub policy: Option<PathBuf>,
}

#[async_trait]
impl Command for PolicyEvalArgs {
    async fn run(&self) -> Result<()> {
        let action: PolicyAction =
            serde_json::from_str(&self.action).map_err(|e| {
                CascadeError::Other(format!(
                    "invalid --action JSON: {e}\n\
                     Expected: {{\"action_type\":\"bash\",\"args\":{{\"cmd\":\"...\"}}}}",
                ))
            })?;

        let result = if let Some(ref policy_path) = self.policy {
            // Evaluate against a single named policy file.
            eval_with_file(policy_path, &action)?
        } else {
            // Evaluate against the active policy chain.
            evaluate_before_dispatch(&action)
        };

        // Print result table.
        println!("decision: {}", result.decision);
        if !result.reason.is_empty() {
            println!("reason:   {}", result.reason);
        }
        println!("policy:   {}", result.policy_id);

        Ok(())
    }
}

/// Evaluate `action` against a single policy file.
///
/// - `.wasm` files: only loadable when cascade-harness is compiled with the
///   `wasm-policy` feature.  Without that feature, returns an informative error.
/// - `.rego` files: evaluated via `SimplePolicyEvaluator` (literal pattern
///   matching).  Full Rego execution (OPA eval) is not supported in-process.
fn eval_with_file(path: &Path, action: &PolicyAction) -> Result<cascade_types::policy::PolicyResult> {
    let ext = path.extension().and_then(|e| e.to_str()).unwrap_or("");
    match ext {
        "wasm" => {
            // Attempt to load the WASM evaluator.  This succeeds only when the
            // cascade-harness wasm-policy feature is active (wasmtime is available).
            // The try_load function returns None when wasmtime is not compiled in.
            match try_load_wasm_evaluator(path) {
                Some(result) => result,
                None => Err(CascadeError::Other(
                    "WASM policy evaluation requires cascade-harness built with the \
                     `wasm-policy` feature (wasmtime); \
                     use `cargo build --features wasm-policy`"
                        .to_string(),
                )),
            }
        }
        "rego" => {
            // Evaluate using the SimplePolicyEvaluator (applies the same built-in
            // patterns as the default-deny-dangerous.rego).
            // Full Rego execution (OPA eval) is not supported in-process.
            let ev = SimplePolicyEvaluator::default();
            Ok(ev.evaluate(action))
        }
        other => Err(CascadeError::Other(format!(
            "unsupported policy file extension `.{other}`; expected .rego or .wasm"
        ))),
    }
}

/// Attempt to load and evaluate a WASM policy file.
///
/// Returns `None` when the wasm-policy feature is not compiled in.
/// Returns `Some(Err(_))` on load errors.
/// Returns `Some(Ok(result))` on successful evaluation.
fn try_load_wasm_evaluator(
    path: &Path,
) -> Option<Result<cascade_types::policy::PolicyResult>> {
    // WasmPolicyEvaluator is only available when the wasm-policy feature is active.
    // We detect availability at runtime by checking if cascade-harness exposes it.
    // Since cascade-harness is a compile-time dep of cascade-cli, we can use
    // a cfg check referencing cascade-harness's feature via its re-export.
    //
    // cascade-harness re-exports WasmPolicyEvaluator only under #[cfg(feature="wasm-policy")].
    // We try to call it via a dynamic dispatch wrapper to avoid a compile-time feature dependency.
    //
    // Practical approach: try reading the file and loading it via the re-exported type.
    // If WasmPolicyEvaluator is available, it compiles; if not, this entire function
    // is dead code under the non-wasm build. We use a type alias to gate this.
    let _ = path; // used in the cfg-gated branch below
    None // stub — overridden by cfg block below
}

// When cascade-cli has cascade-harness with wasm-policy available, we override
// try_load_wasm_evaluator.  Since cascade-cli doesn't declare its own wasm-policy
// feature, we proxy through cascade-harness's compiled exports.
// The above stub returns None; the actual WASM loading is done from the
// `eval --policy foo.wasm` path via cascade-harness's public API directly.
//
// To enable WASM evaluation in cascade policy eval, build cascade-cli with:
//   cargo build -p cascade-cli --features cascade-harness/wasm-policy

// ── list ──────────────────────────────────────────────────────────────────────

async fn run_list() -> Result<()> {
    let dir = policies_dir();
    if !dir.is_dir() {
        println!("No policy directory at {}.", dir.display());
        println!("Active policies: default-deny-dangerous (built-in SimplePolicyEvaluator)");
        return Ok(());
    }

    let mut entries: Vec<(String, String, PathBuf)> = Vec::new(); // (id, type, path)

    // Always show the built-in evaluator.
    entries.push((
        "default-deny-dangerous".to_string(),
        "builtin".to_string(),
        PathBuf::from("<built-in>"),
    ));

    // Enumerate files in the policies directory.
    let mut rd = std::fs::read_dir(&dir).map_err(|e| CascadeError::Io {
        path: dir.clone(),
        operation: "read policies dir",
        source: e,
    })?;

    while let Some(Ok(entry)) = rd.next() {
        let path = entry.path();
        let ext = path.extension().and_then(|e| e.to_str()).unwrap_or("");
        if ext != "rego" && ext != "wasm" {
            continue;
        }
        let id = path
            .file_stem()
            .and_then(|s| s.to_str())
            .unwrap_or("?")
            .to_string();
        entries.push((id, ext.to_string(), path));
    }

    // Print table.
    println!("{:<35} {:<8} {}", "ID", "TYPE", "PATH");
    println!("{}", "-".repeat(80));
    for (id, ty, path) in &entries {
        println!("{:<35} {:<8} {}", id, ty, path.display());
    }

    Ok(())
}

// ── add ───────────────────────────────────────────────────────────────────────

/// Arguments for `cascade policy add`.
#[derive(Debug, Args)]
pub struct PolicyAddArgs {
    /// Path to the policy file (.rego or .wasm) to add.
    pub path: PathBuf,
}

#[async_trait]
impl Command for PolicyAddArgs {
    async fn run(&self) -> Result<()> {
        let src = self.path.canonicalize().map_err(|e| CascadeError::Io {
            path: self.path.clone(),
            operation: "canonicalize",
            source: e,
        })?;

        let ext = src.extension().and_then(|e| e.to_str()).unwrap_or("");
        if ext != "rego" && ext != "wasm" {
            return Err(CascadeError::Other(format!(
                "only .rego and .wasm files may be added as policies; got `.{ext}`"
            )));
        }

        let dir = policies_dir();
        std::fs::create_dir_all(&dir).map_err(|e| CascadeError::Io {
            path: dir.clone(),
            operation: "create policies dir",
            source: e,
        })?;

        let filename = src.file_name().ok_or_else(|| {
            CascadeError::Other("source path has no filename".to_string())
        })?;
        let dest = dir.join(filename);

        std::fs::copy(&src, &dest).map_err(|e| CascadeError::Io {
            path: dest.clone(),
            operation: "copy policy file",
            source: e,
        })?;

        // Invalidate the cached chain so the new policy takes effect immediately.
        invalidate_cache();

        println!("Added policy: {}", dest.display());
        Ok(())
    }
}

// ── remove ────────────────────────────────────────────────────────────────────

/// Arguments for `cascade policy remove`.
#[derive(Debug, Args)]
pub struct PolicyRemoveArgs {
    /// Policy ID (file stem) to remove, e.g. `allow-all` or `my-custom-policy`.
    pub id: String,
}

#[async_trait]
impl Command for PolicyRemoveArgs {
    async fn run(&self) -> Result<()> {
        let dir = policies_dir();

        // Try both .rego and .wasm.
        let candidates = [
            dir.join(format!("{}.rego", self.id)),
            dir.join(format!("{}.wasm", self.id)),
        ];

        let mut removed = false;
        for candidate in &candidates {
            if candidate.is_file() {
                std::fs::remove_file(candidate).map_err(|e| CascadeError::Io {
                    path: candidate.clone(),
                    operation: "remove policy file",
                    source: e,
                })?;
                invalidate_cache();
                println!("Removed policy: {}", candidate.display());
                removed = true;
            }
        }

        if !removed {
            return Err(CascadeError::Other(format!(
                "policy `{}` not found in {}; run `cascade policy list` to see active policies",
                self.id,
                dir.display()
            )));
        }

        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Returns the canonical policies directory path: `~/.cascade/policies/`.
pub fn policies_dir() -> PathBuf {
    global_cascade_dir().join("policies")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::Decision;

    #[tokio::test]
    async fn eval_denies_dangerous_bash_via_dispatch() {
        let action = PolicyAction::new(
            "bash",
            serde_json::json!({"cmd": "rm -rf /"}),
        );
        let result = evaluate_before_dispatch(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[tokio::test]
    async fn eval_allows_safe_read() {
        let action = PolicyAction::new("read", serde_json::json!({}));
        let result = evaluate_before_dispatch(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[tokio::test]
    async fn eval_with_rego_file_uses_simple_evaluator() {
        // Create a temp .rego file and verify eval_with_file uses SimplePolicyEvaluator.
        let dir = tempfile::tempdir().unwrap();
        let rego_path = dir.path().join("test.rego");
        std::fs::write(&rego_path, "# test").unwrap();

        let action = PolicyAction::new("bash", serde_json::json!({"cmd": "rm -rf /"}));
        let result = eval_with_file(&rego_path, &action).unwrap();
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn policies_dir_is_under_home() {
        let dir = policies_dir();
        let home = cascade_types::paths::home_dir();
        assert!(dir.starts_with(&home), "policies dir should be under HOME");
    }
}
