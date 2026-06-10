//! ProviderAdapter trait — the core abstraction for all AI provider adapters.
//!
//! # Purpose
//!
//! Defines the single interface that every AI provider must implement.
//! Concrete adapters (Anthropic, OpenAI, Gemini, Ollama, etc.) implement
//! this trait; callers use `Box<dyn ProviderAdapter>` or
//! `Arc<dyn ProviderAdapter>` so they never depend on a concrete type.
//!
//! # Design
//!
//! - `#[async_trait]` from the `async-trait` crate makes the trait
//!   object-safe (Rust does not natively support `async fn` in `dyn` traits
//!   without the macro).
//! - `Send + Sync` bounds enable adapters to be shared across async tasks.
//! - Streaming is exposed as a `Pin<Box<dyn Stream<...> + Send>>` to avoid
//!   naming the concrete future type.
//!
//! # Constraints
//!
//! - Every method returns `Result<_, ProviderError>` so callers have a
//!   uniform error type to match against.
//! - `provider_info` is a synchronous `&self` method — it must return
//!   cached/static data with no I/O.
//! - Adapters that do not support a method MUST return
//!   `Err(ProviderError::NotImplemented)` rather than panicking.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;

use crate::{
    CompletionRequest, CompletionResponse, ModelInfo, ProviderError, ProviderInfo, StreamChunk,
};

// ── ProviderAdapter ───────────────────────────────────────────────────────────

/// The core abstraction for an AI completion provider.
///
/// Implement this trait for every provider (Anthropic, OpenAI, Google Gemini,
/// Mistral, Cohere, Ollama, etc.).  The router in `cascade-core` holds a
/// `HashMap<TaskType, Box<dyn ProviderAdapter>>` and dispatches calls through
/// this interface.
///
/// ## Object safety
///
/// The trait is object-safe via `#[async_trait]`.  Verify with:
/// ```rust,ignore
/// let _: Box<dyn ProviderAdapter>;
/// ```
///
/// ## Error handling
///
/// Methods that cannot be completed MUST return a variant of
/// [`ProviderError`] — never panic.  Unimplemented methods (stubs) return
/// `Err(ProviderError::NotImplemented)`.
#[async_trait]
pub trait ProviderAdapter: Send + Sync {
    /// Send a blocking (non-streaming) completion request and wait for the
    /// full response.
    ///
    /// Returns the complete generated text along with token usage statistics.
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError>;

    /// Send a streaming completion request and return an async stream of
    /// incremental [`StreamChunk`] values.
    ///
    /// The stream ends when a chunk with `finish_reason: Some(_)` is yielded.
    /// Callers must consume the stream to completion to release the underlying
    /// TCP connection.
    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>;

    /// List all model identifiers available on this provider.
    ///
    /// The returned `ModelInfo` values are used to populate the model-picker
    /// UI (T-P3-E04-03 routing table) and to validate `CompletionRequest.model`
    /// before sending.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError>;

    /// Verify that the provider is reachable and the stored credential is
    /// valid.
    ///
    /// A successful `Ok(())` means the provider can accept completion calls
    /// immediately.  Callers use this to surface connectivity issues in the
    /// UI before the user attempts a real request.
    async fn health_check(&self) -> Result<(), ProviderError>;

    /// Return static metadata about this provider.
    ///
    /// This is a synchronous method that returns pre-computed data — it
    /// must NOT perform any I/O.
    fn provider_info(&self) -> ProviderInfo;
}

// ── NoopProvider ──────────────────────────────────────────────────────────────

/// A no-op `ProviderAdapter` implementation used in tests and as a compile
/// check for object safety.
///
/// Every method returns `Err(ProviderError::NotImplemented)`.  This is
/// intentional — `NoopProvider` is a build-time sentinel, not a usable
/// adapter.
pub struct NoopProvider;

#[async_trait]
impl ProviderAdapter for NoopProvider {
    async fn complete(&self, _req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        Err(ProviderError::NotImplemented)
    }

    async fn complete_stream(
        &self,
        _req: CompletionRequest,
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        Err(ProviderError::NotImplemented)
    }

    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Err(ProviderError::NotImplemented)
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        Err(ProviderError::NotImplemented)
    }

    fn provider_info(&self) -> ProviderInfo {
        use crate::{AuthMethod, ProviderCapabilities};
        ProviderInfo {
            id: "noop".into(),
            name: "NoopProvider".into(),
            auth_method: AuthMethod::None,
            base_url: String::new(),
            capabilities: ProviderCapabilities {
                supports_streaming: false,
                supports_vision: false,
                max_context_tokens: 0,
                supports_function_calling: false,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Compile-time object-safety check: `Box<dyn ProviderAdapter>` must be
    /// constructable.
    #[test]
    fn object_safety_box_dyn() {
        let _adapter: Box<dyn ProviderAdapter> = Box::new(NoopProvider);
    }

    /// Object safety via `Arc` (shared ownership across async tasks).
    #[test]
    fn object_safety_arc_dyn() {
        use std::sync::Arc;
        let _adapter: Arc<dyn ProviderAdapter> = Arc::new(NoopProvider);
    }

    #[tokio::test]
    async fn noop_complete_returns_not_implemented() {
        let adapter = NoopProvider;
        let req = CompletionRequest::simple("noop-model", "hello");
        let result = adapter.complete(req).await;
        assert!(
            matches!(result, Err(ProviderError::NotImplemented)),
            "unexpected: {result:?}"
        );
    }

    #[tokio::test]
    async fn noop_health_check_returns_not_implemented() {
        let adapter = NoopProvider;
        let result = adapter.health_check().await;
        assert!(matches!(result, Err(ProviderError::NotImplemented)));
    }

    #[tokio::test]
    async fn noop_available_models_returns_not_implemented() {
        let adapter = NoopProvider;
        let result = adapter.available_models().await;
        assert!(matches!(result, Err(ProviderError::NotImplemented)));
    }

    #[tokio::test]
    async fn noop_complete_stream_returns_not_implemented() {
        let adapter = NoopProvider;
        let req = CompletionRequest::simple("noop-model", "stream test");
        let result = adapter.complete_stream(req).await;
        assert!(matches!(result, Err(ProviderError::NotImplemented)));
    }

    #[test]
    fn noop_provider_info_is_valid() {
        let adapter = NoopProvider;
        let info = adapter.provider_info();
        assert_eq!(info.id, "noop");
        assert_eq!(info.name, "NoopProvider");
    }

    /// Ensure `Box<dyn ProviderAdapter>` can call all trait methods — the
    /// vtable is populated correctly.
    #[tokio::test]
    async fn dyn_adapter_all_methods() {
        let adapter: Box<dyn ProviderAdapter> = Box::new(NoopProvider);

        let _ = adapter.complete(CompletionRequest::simple("m", "q")).await;
        let _ = adapter.health_check().await;
        let _ = adapter.available_models().await;
        let _ = adapter
            .complete_stream(CompletionRequest::simple("m", "q"))
            .await;
        let info = adapter.provider_info();
        assert!(!info.id.is_empty());
    }
}
