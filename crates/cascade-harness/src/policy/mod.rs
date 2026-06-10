//! Policy/guardrails engine for Cascade.
//!
//! Purpose: Evaluate `PolicyAction`s against a chain of policy evaluators before
//!   dispatch.  Provides the `PolicyEvaluator` trait, `SimplePolicyEvaluator`
//!   (regex-free literal matching), `WasmPolicyEvaluator` (wasmtime WASM host),
//!   `PolicyChain` (AND semantics), and `evaluate_before_dispatch` (cached entry).
//!
//! ## Module layout
//!
//! | Module | Purpose |
//! |--------|---------|
//! | `engine` | `PolicyEvaluator` trait + `PolicyChain` |
//! | `simple` | Built-in literal-pattern evaluator (no external deps) |
//! | `wasm`   | WASM policy evaluator (wasmtime, zero ambient authority) |
//! | `dispatch` | `evaluate_before_dispatch` with lazy-cached chain |
//!
//! ## SPORT
//! MASTER-POLICIES.md: cascade-harness policy module Done

pub mod dispatch;
pub mod engine;
pub mod simple;
#[cfg(feature = "wasm-policy")]
pub mod wasm;

pub use dispatch::{evaluate_before_dispatch, invalidate_cache};
pub use engine::{PolicyChain, PolicyEvaluator};
pub use simple::SimplePolicyEvaluator;
#[cfg(feature = "wasm-policy")]
pub use wasm::WasmPolicyEvaluator;
