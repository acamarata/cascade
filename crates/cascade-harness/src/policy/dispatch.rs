//! Policy dispatch integration — evaluate_before_dispatch with cached chain.
//!
//! Purpose: Provide `evaluate_before_dispatch(action)` called by the daemon
//!   before executing any harness dispatch (codex invoke, generate, mcp tool call).
//!   Policy chain is loaded lazily on first call and invalidated when the policies
//!   directory mtime changes.
//! Inputs: `PolicyAction` from the harness dispatch path.
//! Outputs: `PolicyResult` (Allow/Deny).
//! Constraints:
//!   - Thread-safe: `RwLock` protects the cached chain.
//!   - Cache invalidation: per-directory mtime check on every call (cheap stat).
//!   - If the policies dir does not exist, falls back to `SimplePolicyEvaluator`.
//!   - Deny → caller must block the action and surface reason + policy_id.
//!
//! SPORT: MASTER-POLICIES.md

use std::sync::RwLock;
use std::time::SystemTime;

use cascade_types::policy::{PolicyAction, PolicyResult};

use crate::policy::{
    engine::{PolicyChain, PolicyEvaluator},
    simple::SimplePolicyEvaluator,
};

// ── Cached chain ──────────────────────────────────────────────────────────────

/// Cached policy chain with the mtime of the policies directory when loaded.
struct CachedChain {
    chain: PolicyChain,
    /// The `modified()` timestamp of `~/.cascade/policies/` when the chain was built.
    /// `None` means the directory did not exist at load time.
    dir_mtime: Option<SystemTime>,
}

/// The global lazy-cached policy chain.
static CHAIN_CACHE: std::sync::OnceLock<RwLock<Option<CachedChain>>> =
    std::sync::OnceLock::new();

fn cache() -> &'static RwLock<Option<CachedChain>> {
    CHAIN_CACHE.get_or_init(|| RwLock::new(None))
}

// ── Directory mtime helper ────────────────────────────────────────────────────

/// Returns the `modified()` time of the policies directory, or `None` if absent.
fn policies_dir_mtime() -> Option<SystemTime> {
    let dir = cascade_types::paths::global_cascade_dir().join("policies");
    std::fs::metadata(dir).ok()?.modified().ok()
}

// ── Chain builder ─────────────────────────────────────────────────────────────

/// Build a fresh policy chain.
///
/// Loads WASM policies from `~/.cascade/policies/*.wasm` when the `wasm-policy`
/// feature is enabled; always prepends `SimplePolicyEvaluator` as a backstop.
fn build_chain() -> PolicyChain {
    let mut evaluators: Vec<Box<dyn PolicyEvaluator>> = Vec::new();

    // Always include the built-in SimplePolicyEvaluator.
    evaluators.push(Box::new(SimplePolicyEvaluator::default()));

    // If wasm-policy feature is active, also load any WASM files.
    #[cfg(feature = "wasm-policy")]
    {
        let dir = cascade_types::paths::global_cascade_dir().join("policies");
        if let Ok(rd) = std::fs::read_dir(&dir) {
            for entry in rd.flatten() {
                let path = entry.path();
                if path.extension().and_then(|e| e.to_str()) == Some("wasm") {
                    let stem = path
                        .file_stem()
                        .and_then(|s| s.to_str())
                        .unwrap_or("unknown")
                        .to_string();
                    if let Ok(bytes) = std::fs::read(&path) {
                        if let Ok(ev) = crate::policy::wasm::WasmPolicyEvaluator::load(&stem, &bytes) {
                            evaluators.push(Box::new(ev));
                        }
                    }
                }
            }
        }
    }

    PolicyChain::new(evaluators)
}

// ── evaluate_before_dispatch ──────────────────────────────────────────────────

/// Evaluate `action` against the cached policy chain before dispatch.
///
/// If the result is `Deny`, the caller MUST block the action and surface
/// `result.reason` and `result.policy_id` to the user.
///
/// # Cache semantics
/// The chain is rebuilt if:
/// - No chain is cached yet (first call).
/// - The `~/.cascade/policies/` directory mtime has changed since the chain was built.
pub fn evaluate_before_dispatch(action: &PolicyAction) -> PolicyResult {
    let current_mtime = policies_dir_mtime();

    // Fast path: read lock, check if cache is still valid.
    {
        let guard = cache().read().expect("RwLock poisoned");
        if let Some(ref cached) = *guard {
            if cached.dir_mtime == current_mtime {
                return cached.chain.evaluate(action);
            }
        }
    }

    // Slow path: rebuild chain and update cache.
    let new_chain = build_chain();
    let result = new_chain.evaluate(action);
    {
        let mut guard = cache().write().expect("RwLock poisoned");
        *guard = Some(CachedChain {
            chain: new_chain,
            dir_mtime: current_mtime,
        });
    }
    result
}

/// Invalidate the cached policy chain, forcing a rebuild on the next call.
///
/// Call this after `cascade policy add` or `cascade policy remove` to ensure
/// the CLI's own process sees the updated policy list immediately.
pub fn invalidate_cache() {
    let mut guard = cache().write().expect("RwLock poisoned");
    *guard = None;
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyAction};
    use serde_json::json;

    // Note: these tests call evaluate_before_dispatch directly, which will always
    // pick up the SimplePolicyEvaluator via build_chain().

    #[test]
    fn dispatch_denies_dangerous_bash() {
        let action = PolicyAction::new("bash", json!({"cmd": "rm -rf /"}));
        let result = evaluate_before_dispatch(&action);
        assert_eq!(result.decision, Decision::Deny);
    }

    #[test]
    fn dispatch_allows_safe_bash() {
        let action = PolicyAction::new("bash", json!({"cmd": "cargo check"}));
        let result = evaluate_before_dispatch(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn dispatch_allows_read_action() {
        let action = PolicyAction::new("read", json!({"path": "/tmp/foo"}));
        let result = evaluate_before_dispatch(&action);
        assert_eq!(result.decision, Decision::Allow);
    }

    #[test]
    fn cache_invalidate_rebuilds_on_next_call() {
        // Prime the cache.
        let action = PolicyAction::new("bash", json!({"cmd": "cargo build"}));
        evaluate_before_dispatch(&action);
        // Invalidate.
        invalidate_cache();
        // Verify cache is None.
        let guard = cache().read().expect("RwLock poisoned");
        assert!(guard.is_none(), "cache should be None after invalidation");
    }
}
