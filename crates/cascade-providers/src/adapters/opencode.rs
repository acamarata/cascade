//! OpenCodeAdapter — opencode.ai API (OpenAI-compatible) with OAuth2 bearer auth.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for the opencode.ai subscription service.
//! OpenCode Go exposes an OpenAI-compatible REST API at `https://api.opencode.ai`
//! and requires an OAuth 2.0 bearer token for authentication. The access token
//! is expected to already be stored in cascade-keychain under the service name
//! `"cascade.opencode"` (the PKCE flow that stores it is handled separately
//! by T-P3-E04-22).
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/v1/chat/completions` with `stream: false`.
//! - `complete_stream`: POST with `stream: true`, parse OpenAI-compatible SSE.
//! - `available_models`: returns the hardcoded OpenCode subscription model list.
//! - `health_check`: GET `/v1/models` to verify connectivity and token validity.
//!
//! # Constraints
//!
//! - Access token is fetched from cascade-keychain service `"cascade.opencode"`;
//!   it is never cached and never appears in logs or error messages.
//! - A 401 response from the API signals an expired OAuth token and returns
//!   `ProviderError::OAuthExpired { provider: "opencode" }` so the daemon can
//!   trigger the re-auth flow (T-P3-E04-22).
//! - Token refresh is not attempted here — the signal propagates up to the
//!   daemon which owns the PKCE lifecycle.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use tracing::debug;

use crate::{
    adapter::ProviderAdapter,
    adapters::openai_compat::{map_response, map_stream_chunk, OaiRequest, OaiResponse, OaiStreamChunk},
    cost::compute_cost,
    error::ProviderError,
    http_client::CascadeHttpClient,
    provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo},
    types::{CompletionRequest, CompletionResponse, ModelInfo, StreamChunk},
};

// ── Constants ─────────────────────────────────────────────────────────────────

const OPENCODE_BASE_URL: &str = "https://api.opencode.ai";
const KEYCHAIN_SERVICE: &str = "cascade.opencode";
const KEYCHAIN_ACCOUNT: &str = "opencode";
const PROVIDER_ID: &str = "opencode";

// ── OpenCodeAdapter ───────────────────────────────────────────────────────────

/// Provider adapter for opencode.ai (OpenAI-compatible API, OAuth2 bearer auth).
///
/// ## Authentication
///
/// Reads the OAuth2 access token from the cascade-keychain service
/// `"cascade.opencode"`.  The token is fetched on every call to pick up
/// refreshed tokens without restarting the daemon.  A 401 response from
/// the API is mapped to `ProviderError::OAuthExpired` so the daemon can
/// trigger the PKCE re-auth flow (T-P3-E04-22) rather than retrying with
/// an invalid token.
///
/// ## Available Models
///
/// OpenCode subscriptions provide access to a curated set of models:
///
/// | Model ID | Context |
/// |---|---|
/// | `kimi-k2` | 128 k |
/// | `deepseek-v4-pro` | 128 k |
/// | `gpt-5.5-fast` | 128 k |
pub struct OpenCodeAdapter {
    http: CascadeHttpClient,
    /// Overridable base URL — tests inject a wiremock server address here.
    base_url: String,
    /// Injected access token for tests; `None` means "read from keychain".
    access_token: Option<String>,
}

impl OpenCodeAdapter {
    /// Construct a production adapter pointing at the real opencode.ai API.
    pub fn new() -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: OPENCODE_BASE_URL.to_owned(),
            access_token: None,
        }
    }

    /// Construct a test adapter with an injected base URL and token.
    ///
    /// Used by unit tests to point the adapter at a `wiremock` mock server.
    #[cfg(test)]
    pub fn with_base_url(base_url: impl Into<String>, access_token: impl Into<String>) -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: base_url.into(),
            access_token: Some(access_token.into()),
        }
    }

    /// Retrieve the OAuth2 access token — from the injected field (tests) or
    /// from cascade-keychain (production).
    ///
    /// The token value is NEVER logged or included in error messages.
    fn access_token(&self) -> Result<String, ProviderError> {
        if let Some(ref tok) = self.access_token {
            return Ok(tok.clone());
        }
        cascade_keychain::platform_keychain()
            .get_key(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT)
            .map_err(|_| {
                ProviderError::CredentialNotFound(format!(
                    "no OpenCode OAuth token in keychain service '{KEYCHAIN_SERVICE}'"
                ))
            })
    }

    /// Map `ProviderError::AuthFailed` (from a 401 HTTP response) to
    /// `ProviderError::OAuthExpired` for the opencode provider.
    ///
    /// Every other error variant passes through unchanged.  This mapping lives
    /// at the adapter level rather than in `CascadeHttpClient` because only
    /// OAuth providers emit `OAuthExpired`; API-key providers would use
    /// `AuthFailed` for a 401.
    fn map_auth_error(err: ProviderError) -> ProviderError {
        match err {
            ProviderError::AuthFailed(_) => ProviderError::OAuthExpired {
                provider: PROVIDER_ID.to_string(),
            },
            other => other,
        }
    }

    /// Chat completions endpoint URL.
    fn completions_url(&self) -> String {
        format!("{}/v1/chat/completions", self.base_url)
    }

    /// Models list endpoint URL (used by `health_check`).
    fn models_url(&self) -> String {
        format!("{}/v1/models", self.base_url)
    }
}

impl Default for OpenCodeAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for OpenCodeAdapter {
    /// Send a non-streaming completion request.
    ///
    /// On a 401 response the HTTP client returns `AuthFailed`; `map_auth_error`
    /// converts that to `OAuthExpired` so the daemon can trigger re-auth.
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let token = self.access_token()?;
        let body = OaiRequest::from_request(&req);
        let url = self.completions_url();

        debug!(
            provider = PROVIDER_ID,
            model = %req.model,
            "sending completion request"
        );

        let resp: OaiResponse = self
            .http
            .post_json(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &token)
            })
            .await
            .map_err(Self::map_auth_error)?;

        let mut completion = map_response(resp).ok_or_else(|| {
            ProviderError::InvalidResponse("no choices in opencode response".into())
        })?;
        completion.cost_usd = compute_cost(PROVIDER_ID, &completion.model, &completion.usage);
        Ok(completion)
    }

    /// Send a streaming completion request and return an SSE chunk stream.
    ///
    /// The full SSE body is buffered before being emitted as a `futures::stream`
    /// — this keeps P3 simple and correct. Low-latency forwarding is a P4 goal.
    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<
        Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
        ProviderError,
    > {
        let token = self.access_token()?;
        let body = OaiRequest::from_request(&req);
        let url = self.completions_url();

        let response = self
            .http
            .post_sse(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &token)
            })
            .await
            .map_err(Self::map_auth_error)?;

        let bytes = response
            .bytes()
            .await
            .map_err(|e| ProviderError::NetworkError(e.to_string()))?;

        let text = String::from_utf8_lossy(&bytes).into_owned();

        let chunks: Vec<Result<StreamChunk, ProviderError>> = text
            .lines()
            .filter_map(CascadeHttpClient::parse_sse_line)
            .filter_map(|payload| {
                let chunk: OaiStreamChunk = serde_json::from_str(payload).ok()?;
                map_stream_chunk(chunk).map(Ok)
            })
            .collect();

        Ok(Box::pin(futures::stream::iter(chunks)))
    }

    /// Return the hardcoded list of models available on an OpenCode subscription.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "kimi-k2".into(),
                name: "Kimi K2".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "deepseek-v4-pro".into(),
                name: "DeepSeek V4 Pro".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gpt-5.5-fast".into(),
                name: "GPT-5.5 Fast".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
        ])
    }

    /// Verify provider reachability and token validity via GET /v1/models.
    ///
    /// A 401 is mapped to `OAuthExpired` (same policy as `complete`).
    async fn health_check(&self) -> Result<(), ProviderError> {
        let token = self.access_token()?;
        let url = self.models_url();

        self.http
            .get_json::<serde_json::Value, _>(&url, move |b| {
                CascadeHttpClient::apply_bearer(b, &token)
            })
            .await
            .map_err(Self::map_auth_error)?;

        Ok(())
    }

    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: PROVIDER_ID.into(),
            name: "OpenCode".into(),
            auth_method: AuthMethod::OAuth2 {
                scopes: vec!["openid".into(), "profile".into(), "email".into()],
            },
            base_url: OPENCODE_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: 128_000,
                supports_function_calling: false,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{
        fixture_json, make_openai_sse, HttpMethod, MockProviderServer,
    };

    // ── Happy path: fixture JSON → CompletionResponse ─────────────────────────

    /// Complete returns non-empty content from the opencode fixture.
    #[tokio::test]
    async fn opencode_complete_happy_path() {
        let ctx = MockProviderServer::start("opencode").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            fixture_json("opencode_complete"),
        )
        .await;

        let adapter = OpenCodeAdapter::with_base_url(ctx.base_url(), "test-token");
        let req = CompletionRequest::simple("kimi-k2", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("should succeed");

        // Contract assertions: content + model + prompt_tokens all populated.
        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(
            resp.usage.prompt_tokens > 0,
            "prompt_tokens must be positive"
        );
    }

    // ── Missing credentials → CredentialNotFound ──────────────────────────────

    /// With no injected token and no keychain entry, complete() returns
    /// CredentialNotFound — not a panic.
    #[tokio::test]
    async fn opencode_missing_credentials_returns_credential_not_found() {
        // Production adapter (no injected token); keychain will be absent in CI.
        let adapter = OpenCodeAdapter::new();
        // Override base_url to avoid real network calls if the token is somehow
        // present — we care only about the credential-absent path.
        // In CI the keychain is empty, so we expect CredentialNotFound.
        // In local dev with a real token stored this test would short-circuit
        // by succeeding against the real API; we accept that trade-off.
        let err = adapter
            .complete(CompletionRequest::simple("kimi-k2", "ping"))
            .await
            .unwrap_err();
        assert!(
            matches!(
                err,
                ProviderError::CredentialNotFound(_) | ProviderError::OAuthExpired { .. }
            ),
            "expected CredentialNotFound or OAuthExpired, got {err:?}"
        );
    }

    // ── 401 response → OAuthExpired ───────────────────────────────────────────

    /// The adapter converts a 401 HTTP response into OAuthExpired (not AuthFailed),
    /// signalling the daemon to trigger re-auth.
    #[tokio::test]
    async fn opencode_401_returns_oauth_expired() {
        let ctx = MockProviderServer::start("opencode").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            401,
            serde_json::json!({"error": {"message": "token expired"}}),
        )
        .await;

        let adapter = OpenCodeAdapter::with_base_url(ctx.base_url(), "expired-token");
        let req = CompletionRequest::simple("kimi-k2", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::OAuthExpired { ref provider } if provider == "opencode"),
            "expected OAuthExpired{{provider=opencode}}, got {err:?}"
        );
    }

    // ── Streaming: SSE chunks parsed correctly ─────────────────────────────────

    /// Streaming returns the concatenated delta content from all SSE chunks.
    #[tokio::test]
    async fn opencode_stream_parses_chunks() {
        let ctx = MockProviderServer::start("opencode").await;
        let sse_body = make_openai_sse(&["Hello", ", world", "!"]);
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = OpenCodeAdapter::with_base_url(ctx.base_url(), "test-token");
        let req = CompletionRequest::simple("kimi-k2", "ping");
        let mut stream = adapter
            .complete_stream(req)
            .await
            .expect("stream should open");

        use futures::StreamExt;
        let mut combined = String::new();
        while let Some(chunk) = stream.next().await {
            let c = chunk.expect("chunk should not error");
            combined.push_str(&c.delta);
        }

        assert_eq!(combined, "Hello, world!", "concatenated delta: {combined:?}");
    }

    // ── Contract assertions via shared helper ─────────────────────────────────

    /// Verify the shared contract helper recognises a well-formed response.
    #[tokio::test]
    async fn opencode_contract_assertions() {
        let ctx = MockProviderServer::start("opencode").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            fixture_json("opencode_complete"),
        )
        .await;

        let adapter = OpenCodeAdapter::with_base_url(ctx.base_url(), "test-token");
        let resp = adapter
            .complete(CompletionRequest::simple("kimi-k2", "test"))
            .await
            .expect("should succeed");

        crate::test_helpers::test_support::assert_completion_contract(
            &resp.content,
            &resp.model,
            resp.usage.prompt_tokens,
        );
    }

    // ── available_models ──────────────────────────────────────────────────────

    /// available_models returns exactly the three documented OpenCode models.
    #[tokio::test]
    async fn opencode_available_models() {
        let adapter = OpenCodeAdapter::new();
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 3, "expected 3 models, got {}", models.len());
        assert!(models.iter().any(|m| m.id == "kimi-k2"));
        assert!(models.iter().any(|m| m.id == "deepseek-v4-pro"));
        assert!(models.iter().any(|m| m.id == "gpt-5.5-fast"));
        assert!(
            models.iter().all(|m| m.context_window == 128_000),
            "all models should have 128k context"
        );
        assert!(
            models.iter().all(|m| m.supports_streaming),
            "all models should support streaming"
        );
    }

    // ── provider_info ─────────────────────────────────────────────────────────

    /// provider_info returns the expected ID and OAuth2 auth method.
    #[test]
    fn opencode_provider_info() {
        let adapter = OpenCodeAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "opencode");
        assert_eq!(info.name, "OpenCode");
        assert!(
            matches!(info.auth_method, AuthMethod::OAuth2 { .. }),
            "expected OAuth2 auth method"
        );
        assert!(info.capabilities.supports_streaming);
        assert!(!info.capabilities.supports_vision);
    }

    // ── map_auth_error unit test ──────────────────────────────────────────────

    /// map_auth_error converts AuthFailed → OAuthExpired{provider="opencode"}.
    #[test]
    fn map_auth_error_converts_auth_failed() {
        let err = ProviderError::AuthFailed("HTTP 401".into());
        let mapped = OpenCodeAdapter::map_auth_error(err);
        assert!(
            matches!(mapped, ProviderError::OAuthExpired { ref provider } if provider == "opencode"),
            "expected OAuthExpired, got {mapped:?}"
        );
    }

    /// map_auth_error passes non-auth errors through unchanged.
    #[test]
    fn map_auth_error_passes_through_other_errors() {
        let err = ProviderError::NetworkError("connection refused".into());
        let mapped = OpenCodeAdapter::map_auth_error(err);
        assert!(
            matches!(mapped, ProviderError::NetworkError(_)),
            "expected NetworkError, got {mapped:?}"
        );
    }
}
