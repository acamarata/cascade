//! `runner_factory` — model-family dispatch and `LocalLlmRunnerTrait` definition.
//!
//! # Purpose
//!
//! Defines the object-safe [`LocalLlmRunnerTrait`] shared by all local model
//! runners, and provides [`build_runner`] — a factory function that maps a
//! [`ModelFamily`] discriminant to the concrete runner type so callers never
//! need to import individual runner structs.
//!
//! # Inputs / Outputs
//!
//! - `build_runner(family, config) -> Result<Box<dyn LocalLlmRunnerTrait>, LocalModelError>`
//!   Returns an owned, type-erased runner or a typed error.
//!
//! # Constraints
//!
//! - [`LocalLlmRunnerTrait`] must remain object-safe (`Box<dyn …>` must compile).
//! - `run()` is `async` via `#[async_trait]` — see object-safety notes in
//!   `async-trait` crate docs.
//! - Adding a new model family requires: (1) a new variant in `ModelFamily`,
//!   (2) a new arm in `build_runner`, (3) a new module under `runners/`.
//!   No changes required in callers.
//!
//! # Registry notes
//!
//! | Family | Model id | RAM floor | Notes |
//! |--------|----------|-----------|-------|
//! | Gemma2 | `local:gemma-2-2b` | ~4 GB | Default quality model |
//! | Llama3 | `local:llama-3.2-3b` | ~6 GB | 8 GB-floor model per repo conventions |
//! | Phi3   | `local:phi-3-mini`   | ~7 GB | Compact instruction-tuned model |

use async_trait::async_trait;
use tokio_stream::wrappers::ReceiverStream;

use crate::{config::LocalLlmConfig, error::LocalModelError};

// ── LocalLlmRunnerTrait ────────────────────────────────────────────────────────

/// Object-safe async trait shared by all local model runners.
///
/// # Object safety
///
/// This trait is object-safe: `run()` uses `async_trait` which rewrites the
/// async method to return `Pin<Box<dyn Future + Send>>`, satisfying the
/// object-safety constraint.  Callers may use `Box<dyn LocalLlmRunnerTrait>`.
#[async_trait]
pub trait LocalLlmRunnerTrait: Send + Sync {
    /// Run text generation on `prompt`, returning an async stream of decoded
    /// token strings.
    ///
    /// Model weights are loaded lazily on the first call; subsequent calls
    /// reuse resident weights.  All tensor ops run in `spawn_blocking`.
    ///
    /// # Errors
    ///
    /// - [`LocalModelError::WeightsNotFound`] if weights are absent at load time.
    /// - [`LocalModelError::Candle`] for any tensor operation failure.
    async fn run(&self, prompt: &str) -> Result<ReceiverStream<String>, LocalModelError>;
}

// ── ModelFamily ───────────────────────────────────────────────────────────────

/// Discriminant for supported local model families.
///
/// Used by [`build_runner`] to select the concrete runner implementation.
///
/// # Extending
///
/// Add a new variant here and a corresponding arm in `build_runner` to support
/// additional models.  No changes to call sites required.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum ModelFamily {
    /// Gemma 2 2B — default quality model, ~4 GB RAM.
    ///
    /// Runner: [`crate::runner::LocalLlmRunner`]
    Gemma2,

    /// Llama 3.2 3B — low-RAM option for 8 GB machines (repo convention).
    ///
    /// Runner: [`crate::runners::llama3::Llama3Runner`]
    Llama3,

    /// Phi-3 Mini — compact instruction-tuned model, ~7 GB F32.
    ///
    /// Runner: [`crate::runners::phi3::Phi3Runner`]
    Phi3,
}

impl ModelFamily {
    /// Return the canonical model id string for this family.
    pub fn model_id(&self) -> &'static str {
        match self {
            ModelFamily::Gemma2 => crate::runner::GEMMA_2_2B_MODEL_ID,
            ModelFamily::Llama3 => crate::runners::llama3::LLAMA3_2_3B_MODEL_ID,
            ModelFamily::Phi3 => crate::runners::phi3::PHI3_MINI_MODEL_ID,
        }
    }
}

// ── build_runner ──────────────────────────────────────────────────────────────

/// Factory: construct the correct runner for `family` from `config`.
///
/// Returns `Box<dyn LocalLlmRunnerTrait>` so callers are decoupled from the
/// concrete runner type.
///
/// # Errors
///
/// - [`LocalModelError::WeightsNotFound`] if the model directory in `config`
///   does not exist.
/// - [`LocalModelError::UnsupportedModel`] is NOT returned here (all
///   `ModelFamily` variants are matched); it is only produced when a caller
///   passes a raw model-id string to a runner that does not support it.
///
/// # Example
///
/// ```rust,ignore
/// use cascade_local_llm::{build_runner, ModelFamily, LocalLlmConfig};
///
/// let config = LocalLlmConfig { model_path: "/tmp/my-model".into(), .. };
/// let runner = build_runner(ModelFamily::Llama3, config)?;
/// ```
pub fn build_runner(
    family: ModelFamily,
    config: LocalLlmConfig,
) -> Result<Box<dyn LocalLlmRunnerTrait>, LocalModelError> {
    match family {
        ModelFamily::Gemma2 => {
            let runner = crate::runner::LocalLlmRunner::new(config)?;
            // Wrap LocalLlmRunner behind the trait.
            // LocalLlmRunner predates the trait; we add a thin newtype wrapper.
            Ok(Box::new(GemmaRunnerWrapper(runner)))
        }
        ModelFamily::Llama3 => {
            let runner = crate::runners::llama3::Llama3Runner::new(config)?;
            Ok(Box::new(runner))
        }
        ModelFamily::Phi3 => {
            let runner = crate::runners::phi3::Phi3Runner::new(config)?;
            Ok(Box::new(runner))
        }
    }
}

// ── GemmaRunnerWrapper ────────────────────────────────────────────────────────

/// Newtype wrapper that adapts `LocalLlmRunner` (Gemma2) into
/// `LocalLlmRunnerTrait`.
///
/// `LocalLlmRunner` was written before the trait existed; rather than modifying
/// its public API, we wrap it here to satisfy the trait contract.
struct GemmaRunnerWrapper(crate::runner::LocalLlmRunner);

#[async_trait]
impl LocalLlmRunnerTrait for GemmaRunnerWrapper {
    async fn run(&self, prompt: &str) -> Result<ReceiverStream<String>, LocalModelError> {
        self.0.run(prompt).await
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;
    use tempfile::TempDir;

    fn nonexistent_config(suffix: &str) -> LocalLlmConfig {
        LocalLlmConfig {
            model_path: PathBuf::from(format!("/tmp/__cascade_factory_nonexistent_{suffix}__")),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        }
    }

    fn existing_config() -> (TempDir, LocalLlmConfig) {
        let dir = TempDir::new().expect("tempdir");
        let cfg = LocalLlmConfig {
            model_path: dir.path().to_path_buf(),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        (dir, cfg)
    }

    // ── build_runner dispatch ─────────────────────────────────────────────────

    #[test]
    fn build_runner_gemma2_nonexistent_path_returns_err() {
        let result = build_runner(ModelFamily::Gemma2, nonexistent_config("gemma2"));
        assert!(
            matches!(result, Err(LocalModelError::WeightsNotFound { .. })),
            "expected WeightsNotFound for Gemma2 with missing dir"
        );
    }

    #[test]
    fn build_runner_llama3_nonexistent_path_returns_err() {
        let result = build_runner(ModelFamily::Llama3, nonexistent_config("llama3"));
        assert!(
            matches!(result, Err(LocalModelError::WeightsNotFound { .. })),
            "expected WeightsNotFound for Llama3 with missing dir"
        );
    }

    #[test]
    fn build_runner_phi3_nonexistent_path_returns_err() {
        let result = build_runner(ModelFamily::Phi3, nonexistent_config("phi3"));
        assert!(
            matches!(result, Err(LocalModelError::WeightsNotFound { .. })),
            "expected WeightsNotFound for Phi3 with missing dir"
        );
    }

    #[test]
    fn build_runner_gemma2_existing_dir_returns_ok() {
        let (_dir, cfg) = existing_config();
        let result = build_runner(ModelFamily::Gemma2, cfg);
        assert!(result.is_ok(), "expected Ok for Gemma2 with existing dir");
    }

    #[test]
    fn build_runner_llama3_existing_dir_returns_ok() {
        let (_dir, cfg) = existing_config();
        let result = build_runner(ModelFamily::Llama3, cfg);
        assert!(result.is_ok(), "expected Ok for Llama3 with existing dir");
    }

    #[test]
    fn build_runner_phi3_existing_dir_returns_ok() {
        let (_dir, cfg) = existing_config();
        let result = build_runner(ModelFamily::Phi3, cfg);
        assert!(result.is_ok(), "expected Ok for Phi3 with existing dir");
    }

    // ── object safety ─────────────────────────────────────────────────────────

    #[test]
    fn runner_trait_is_object_safe_box_dyn() {
        // Compile-time check: Box<dyn LocalLlmRunnerTrait> must be constructable.
        let (_dir, cfg) = existing_config();
        let _runner: Box<dyn LocalLlmRunnerTrait> = build_runner(ModelFamily::Llama3, cfg)
            .unwrap_or_else(|e| panic!("expected Ok, got: {e}"));
    }

    // ── ModelFamily model_id ──────────────────────────────────────────────────

    #[test]
    fn model_family_ids_are_distinct() {
        let ids = [
            ModelFamily::Gemma2.model_id(),
            ModelFamily::Llama3.model_id(),
            ModelFamily::Phi3.model_id(),
        ];
        // All three ids must be unique
        let mut seen = std::collections::HashSet::new();
        for id in &ids {
            assert!(seen.insert(*id), "duplicate model_id: {id}");
        }
    }

    #[test]
    fn model_family_gemma2_id() {
        assert_eq!(ModelFamily::Gemma2.model_id(), "local:gemma-2-2b");
    }

    #[test]
    fn model_family_llama3_id() {
        assert_eq!(ModelFamily::Llama3.model_id(), "local:llama-3.2-3b");
    }

    #[test]
    fn model_family_phi3_id() {
        assert_eq!(ModelFamily::Phi3.model_id(), "local:phi-3-mini");
    }
}
