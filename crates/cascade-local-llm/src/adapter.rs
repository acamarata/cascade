//! `LocalLlmAdapter` — bridges `LocalLlmRunner` into the `ProviderAdapter` trait.
//!
//! # Purpose
//!
//! Wraps a `LocalLlmRunner` (candle-rs gemma-2-2b) inside the standard
//! `cascade_providers::ProviderAdapter` interface so local models register
//! in `ProviderRegistry` exactly like any cloud adapter.
//!
//! # Inputs / Outputs
//!
//! - `LocalLlmAdapter::new(config) -> Result<Self, LocalModelError>`
//!   Constructs the runner and validates the model directory.
//!
//! - `ProviderAdapter::complete()` drains the token stream into a full string.
//! - `ProviderAdapter::complete_stream()` yields `StreamChunk`s from the stream.
//! - `ProviderAdapter::provider_info()` returns a `ProviderInfo` with
//!   `id = "local:gemma-2-2b"`, `auth_method = AuthMethod::None`.
//!
//! # Constraints
//!
//! - `complete_stream()` is the canonical path; `complete()` is a thin
//!   drain wrapper.
//! - `available_models()` returns the single `gemma-2-2b` entry.
//! - `health_check()` verifies the model directory exists (no actual
//!   model load; that would be too slow for a health check).

use std::pin::Pin;
use std::sync::Arc;

use async_trait::async_trait;
use futures_core::Stream;
use futures_util::stream::once;
use tokio_stream::StreamExt as _;
use tracing::debug;

use cascade_providers::{
    AuthMethod, CompletionRequest, CompletionResponse, ModelInfo, ProviderAdapter,
    ProviderCapabilities, ProviderError, ProviderInfo, StreamChunk, TokenUsage,
};

use crate::{
    config::LocalLlmConfig,
    error::LocalModelError,
    prompt::format_gemma2_prompt,
    runner::{LocalLlmRunner, GEMMA_2_2B_MODEL_ID},
};

// ── LocalLlmAdapter ───────────────────────────────────────────────────────────

/// A `ProviderAdapter` backed by a local gemma-2-2b candle-rs runner.
///
/// Register this with `ProviderRegistry::register` to make local inference
/// available under the `local:gemma-2-2b` provider id.
pub struct LocalLlmAdapter {
    runner: Arc<LocalLlmRunner>,
}

impl std::fmt::Debug for LocalLlmAdapter {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("LocalLlmAdapter")
            .field("runner", &self.runner)
            .finish()
    }
}

impl LocalLlmAdapter {
    /// Construct from a `LocalLlmConfig`.
    ///
    /// Validates the model directory exists.  Does NOT load weights.
    ///
    /// # Errors
    ///
    /// - [`LocalModelError::WeightsNotFound`] if the model directory is absent.
    pub fn new(config: LocalLlmConfig) -> Result<Self, LocalModelError> {
        let runner = LocalLlmRunner::new(config)?;
        Ok(Self {
            runner: Arc::new(runner),
        })
    }

    // ── internal helper ───────────────────────────────────────────────────────

    /// Apply per-request overrides from `CompletionRequest` to produce the
    /// formatted Gemma-2 prompt string.
    fn build_prompt(req: &CompletionRequest) -> String {
        format_gemma2_prompt(&req.messages)
    }
}

// ── ProviderAdapter impl ──────────────────────────────────────────────────────

#[async_trait]
impl ProviderAdapter for LocalLlmAdapter {
    /// Non-streaming completion — drains the token stream into a single string.
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let prompt = Self::build_prompt(&req);
        debug!(
            model = %req.model,
            "cascade-local-llm: complete() called"
        );

        let stream = self
            .runner
            .run(&prompt)
            .await
            .map_err(ProviderError::from)?;
        let tokens: Vec<String> = stream.collect().await;
        let content = tokens.join("");
        let completion_tokens = tokens.len() as u32;

        Ok(CompletionResponse {
            content,
            model: GEMMA_2_2B_MODEL_ID.to_string(),
            usage: TokenUsage {
                prompt_tokens: 0, // candle does not expose prompt token counts
                completion_tokens,
                total_tokens: completion_tokens,
            },
            cost_usd: Some(0.0), // local inference has no API cost
        })
    }

    /// Streaming completion — yields `StreamChunk`s token by token.
    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        let prompt = Self::build_prompt(&req);
        debug!(
            model = %req.model,
            "cascade-local-llm: complete_stream() called"
        );

        let stream = self
            .runner
            .run(&prompt)
            .await
            .map_err(ProviderError::from)?;

        // Map each token string to a StreamChunk
        let mapped = stream.map(|token| {
            Ok(StreamChunk {
                delta: token,
                finish_reason: None,
            })
        });

        // Append a final chunk with finish_reason = "stop"
        let final_chunk = once(async {
            Ok(StreamChunk {
                delta: String::new(),
                finish_reason: Some("stop".to_string()),
            })
        });

        let combined: Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>> =
            Box::pin(mapped.chain(final_chunk));

        Ok(combined)
    }

    /// Return the single available local model.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![ModelInfo {
            id: GEMMA_2_2B_MODEL_ID.to_string(),
            name: "Gemma 2 2B (local, candle-rs)".to_string(),
            context_window: 8192,
            supports_streaming: true,
        }])
    }

    /// Verify the model directory exists.
    ///
    /// Does NOT load weights — only confirms the directory is present so the
    /// check completes in milliseconds.
    async fn health_check(&self) -> Result<(), ProviderError> {
        if self.runner.model_path().exists() {
            Ok(())
        } else {
            Err(ProviderError::LocalModelError(format!(
                "model directory not found: {}",
                self.runner.model_path().display()
            )))
        }
    }

    /// Return static provider metadata.
    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: "local:gemma-2-2b".to_string(),
            name: "Gemma 2 2B (local)".to_string(),
            auth_method: AuthMethod::None,
            base_url: String::new(), // no network endpoint
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: 8192,
                supports_function_calling: false,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_providers::{CompletionRequest, Message};
    use tempfile::TempDir;

    fn adapter_with_dir(dir: &TempDir) -> LocalLlmAdapter {
        let cfg = LocalLlmConfig {
            model_path: dir.path().to_path_buf(),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        LocalLlmAdapter::new(cfg).expect("adapter (directory exists)")
    }

    // ── compile-time object-safety ────────────────────────────────────────────

    #[test]
    fn adapter_is_object_safe_box_dyn() {
        let dir = TempDir::new().expect("tempdir");
        let _: Box<dyn ProviderAdapter> = Box::new(adapter_with_dir(&dir));
    }

    #[test]
    fn adapter_is_object_safe_arc_dyn() {
        let dir = TempDir::new().expect("tempdir");
        let _: Arc<dyn ProviderAdapter> = Arc::new(adapter_with_dir(&dir));
    }

    // ── provider_info ─────────────────────────────────────────────────────────

    #[test]
    fn provider_info_id_is_local_gemma_2_2b() {
        let dir = TempDir::new().expect("tempdir");
        let adapter = adapter_with_dir(&dir);
        let info = adapter.provider_info();
        assert_eq!(info.id, "local:gemma-2-2b");
        assert!(
            matches!(info.auth_method, AuthMethod::None),
            "local model needs no auth"
        );
        assert!(
            info.base_url.is_empty(),
            "no network endpoint for local model"
        );
    }

    // ── available_models ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn available_models_returns_gemma_entry() {
        let dir = TempDir::new().expect("tempdir");
        let adapter = adapter_with_dir(&dir);
        let models = adapter.available_models().await.expect("models");
        assert_eq!(models.len(), 1);
        assert_eq!(models[0].id, GEMMA_2_2B_MODEL_ID);
        assert!(models[0].supports_streaming);
    }

    // ── health_check ──────────────────────────────────────────────────────────

    #[tokio::test]
    async fn health_check_ok_when_dir_exists() {
        let dir = TempDir::new().expect("tempdir");
        let adapter = adapter_with_dir(&dir);
        let result = adapter.health_check().await;
        assert!(result.is_ok(), "dir exists so health_check should pass");
    }

    #[tokio::test]
    async fn health_check_err_when_dir_missing() {
        // new() itself returns Err when dir is missing.
        // Test health_check via a dir that is created then dropped (deleted).
        let dir = TempDir::new().expect("tempdir");
        let path = dir.path().to_path_buf();
        let adapter = LocalLlmAdapter::new(LocalLlmConfig {
            model_path: path.clone(),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        })
        .expect("adapter created while dir exists");
        // Now remove the directory
        drop(dir); // TempDir cleans up on drop
        let result = adapter.health_check().await;
        assert!(
            result.is_err(),
            "health_check should fail after directory removed"
        );
    }

    // ── new() error path ──────────────────────────────────────────────────────

    #[test]
    fn new_with_missing_dir_returns_err() {
        let cfg = LocalLlmConfig {
            model_path: std::path::PathBuf::from("/tmp/__no_such_model_dir__"),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        let result = LocalLlmAdapter::new(cfg);
        assert!(
            result.is_err(),
            "missing model dir must return Err, not panic"
        );
        // Error converts cleanly to LocalModelError
        match result.unwrap_err() {
            LocalModelError::WeightsNotFound { path } => {
                assert!(
                    path.to_string_lossy().contains("no_such_model_dir"),
                    "path: {path:?}"
                );
            }
            other => panic!("expected WeightsNotFound, got: {other:?}"),
        }
    }

    // ── complete / complete_stream with real weights ───────────────────────────

    #[tokio::test]
    #[ignore = "requires real gemma-2-2b weights (~1.5 GB); run with --ignored locally"]
    async fn complete_returns_non_empty_content() {
        let cfg = LocalLlmConfig::default_gemma_2_2b();
        let adapter = LocalLlmAdapter::new(cfg).expect("adapter");
        let req = CompletionRequest {
            model: GEMMA_2_2B_MODEL_ID.to_string(),
            messages: vec![Message::user("Say hello.")],
            max_tokens: Some(20),
            temperature: Some(0.0),
            stream: false,
            system: None,
        };
        let resp = adapter.complete(req).await.expect("complete");
        assert!(!resp.content.is_empty(), "should produce tokens");
        assert_eq!(resp.model, GEMMA_2_2B_MODEL_ID);
        assert_eq!(resp.cost_usd, Some(0.0));
    }
}
