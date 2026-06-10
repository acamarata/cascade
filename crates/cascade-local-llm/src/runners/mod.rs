//! Runner implementations for all supported local model families.
//!
//! Each module wraps a candle-transformers model behind [`LocalLlmRunnerTrait`].
//! Callers should not construct runners directly — use
//! [`crate::runner_factory::build_runner`] instead.

pub mod llama3;
pub mod phi3;

pub use llama3::Llama3Runner;
pub use phi3::Phi3Runner;
