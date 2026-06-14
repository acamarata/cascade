//! Policy/guardrails engine for Cascade.
//!
//! Purpose: Evaluate `PolicyAction`s against a chain of policy evaluators before
//!   dispatch.  Provides the `PolicyEvaluator` trait, `SimplePolicyEvaluator`
//!   (regex-free literal matching), `WasmPolicyEvaluator` (wasmtime WASM host),
//!   `PolicyChain` (AND semantics), `evaluate_before_dispatch` (cached entry),
//!   and `project_policy` (per-project pre-eval cache with tier merge).
//!
//! ## Module layout
//!
//! | Module | Purpose |
//! |--------|---------|
//! | `engine`           | `PolicyEvaluator` trait + `PolicyChain` |
//! | `simple`           | Built-in literal-pattern evaluator (no external deps) |
//! | `wasm`             | WASM policy evaluator (wasmtime, zero ambient authority) |
//! | `dispatch`         | `evaluate_before_dispatch` with lazy-cached chain |
//! | `project_policy`   | Per-project `PolicySet` pre-eval cache + tier merge |
//!
//! ## SPORT
//! MASTER-POLICIES.md: cascade-harness policy module Done

pub mod dispatch;
pub mod engine;
pub mod project_policy;
pub mod simple;
#[cfg(feature = "wasm-policy")]
pub mod wasm;

pub use dispatch::{evaluate_before_dispatch, invalidate_cache};
pub use engine::{PolicyChain, PolicyEvaluator};
pub use project_policy::{
    build_project_policy_set, effective_policy_set, invalidate_all as invalidate_all_projects,
    invalidate_project, warm_cache,
};
pub use simple::SimplePolicyEvaluator;
#[cfg(feature = "wasm-policy")]
pub use wasm::WasmPolicyEvaluator;
