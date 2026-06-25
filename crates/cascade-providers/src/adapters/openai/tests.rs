//! Tests for `OpenAIAdapter`.

use futures::StreamExt as _;
use serde_json::json;
use wiremock::matchers::{method, path};
use wiremock::{Mock, MockServer, ResponseTemplate};

use super::adapter::OpenAIAdapter;
use super::helpers::{model_context_window, DEFAULT_BASE_URL};
use crate::{
    adapter::ProviderAdapter,
    error::ProviderError,
    test_helpers::test_support::{fixture_json, fixture_text, HttpMethod, MockProviderServer},
    types::{CompletionRequest, Message, StreamChunk},
};

// ── helpers ───────────────────────────────────────────────────────────────

fn adapter_for(server: &MockProviderServer) -> OpenAIAdapter {
    OpenAIAdapter::new("sk-test-key", Some(server.base_url()))
}

// ── complete() happy path ─────────────────────────────────────────────────

/// T-P3-E04-08 acceptance criterion: content non-empty, usage > 0.
#[tokio::test]
async fn openai_complete_happy_path_fixture() {
    let ctx = MockProviderServer::start("openai").await;
    ctx.mount_json(
        HttpMethod::Post,
        "/v1/chat/completions",
        200,
        fixture_json("openai_complete"),
    )
    .await;

    let adapter = adapter_for(&ctx);
    let req = CompletionRequest::simple("gpt-4o", "What is the capital of France?");
    let resp = adapter.complete(req).await.expect("complete ok");

    crate::test_helpers::test_support::assert_completion_contract(
        &resp.content,
        &resp.model,
        resp.usage.prompt_tokens,
    );
    assert!(resp.content.contains("Paris"), "content: {}", resp.content);
    assert_eq!(resp.usage.prompt_tokens, 14);
    assert_eq!(resp.usage.completion_tokens, 9);
    assert_eq!(resp.usage.total_tokens, 23);
}

// ── complete() auth failure ───────────────────────────────────────────────

#[tokio::test]
async fn openai_complete_401_maps_to_auth_failed() {
    let ctx = MockProviderServer::start("openai").await;
    ctx.mount_json(
        HttpMethod::Post,
        "/v1/chat/completions",
        401,
        json!({"error": {"message": "Invalid API key", "type": "invalid_request_error"}}),
    )
    .await;

    let adapter = adapter_for(&ctx);
    let err = adapter
        .complete(CompletionRequest::simple("gpt-4o", "hi"))
        .await
        .expect_err("should fail");

    assert!(
        matches!(err, ProviderError::AuthFailed(_)),
        "expected AuthFailed, got {err:?}"
    );
}

// ── complete() rate limit ─────────────────────────────────────────────────

#[tokio::test]
async fn openai_complete_persistent_429_maps_to_rate_limited() {
    let server = MockServer::start().await;
    Mock::given(method("POST"))
        .and(path("/v1/chat/completions"))
        .respond_with(
            ResponseTemplate::new(429)
                .append_header("retry-after", "0")
                .append_header("content-length", "0"),
        )
        .mount(&server)
        .await;

    let adapter = OpenAIAdapter::new("sk-test-key", Some(server.uri()));
    let err = adapter
        .complete(CompletionRequest::simple("gpt-4o", "hi"))
        .await
        .expect_err("should fail");

    assert!(
        matches!(err, ProviderError::RateLimited { .. }),
        "expected RateLimited, got {err:?}"
    );
}

// ── complete_stream() ─────────────────────────────────────────────────────

/// T-P3-E04-08 acceptance: stream yields ≥1 StreamChunk from fixture.
#[tokio::test]
async fn openai_stream_parse_fixture() {
    let ctx = MockProviderServer::start("openai").await;
    let sse_body = fixture_text("openai_stream");
    ctx.mount_raw(
        HttpMethod::Post,
        "/v1/chat/completions",
        200,
        "text/event-stream",
        sse_body,
    )
    .await;

    let adapter = adapter_for(&ctx);
    let req = CompletionRequest::simple("gpt-4o", "What is the capital of France?");
    let mut stream = adapter.complete_stream(req).await.expect("stream open");

    let mut chunks: Vec<StreamChunk> = Vec::new();
    while let Some(result) = stream.next().await {
        chunks.push(result.expect("chunk ok"));
    }

    assert!(!chunks.is_empty(), "expected ≥1 StreamChunk, got 0");

    // At least one chunk should carry a stop finish_reason.
    let has_stop = chunks
        .iter()
        .any(|c| c.finish_reason.as_deref() == Some("stop"));
    assert!(has_stop, "expected a chunk with finish_reason=stop");
}

/// [DONE]-only SSE body closes the stream with zero chunks, no error.
#[tokio::test]
async fn openai_stream_done_sentinel_yields_zero_chunks() {
    let ctx = MockProviderServer::start("openai").await;
    ctx.mount_raw(
        HttpMethod::Post,
        "/v1/chat/completions",
        200,
        "text/event-stream",
        "data: [DONE]\n\n".to_owned(),
    )
    .await;

    let adapter = adapter_for(&ctx);
    let mut stream = adapter
        .complete_stream(CompletionRequest::simple("gpt-4o", "hi"))
        .await
        .expect("stream open");

    let mut count = 0usize;
    while let Some(r) = stream.next().await {
        r.expect("unexpected error in DONE-only stream");
        count += 1;
    }
    assert_eq!(count, 0);
}

// ── available_models() ────────────────────────────────────────────────────

/// T-P3-E04-08 acceptance: ≥1 ModelInfo; filter excludes embeddings/dall-e/whisper.
#[tokio::test]
async fn openai_available_models_filtered() {
    let ctx = MockProviderServer::start("openai").await;
    ctx.mount_json(
        HttpMethod::Get,
        "/v1/models",
        200,
        json!({
            "object": "list",
            "data": [
                {"id": "gpt-4o"},
                {"id": "gpt-4-turbo"},
                {"id": "gpt-3.5-turbo"},
                {"id": "o1-mini"},
                {"id": "o4-mini"},
                {"id": "text-davinci-003"},
                // These must be filtered out:
                {"id": "text-embedding-3-small"},
                {"id": "dall-e-3"},
                {"id": "whisper-1"}
            ]
        }),
    )
    .await;

    let adapter = adapter_for(&ctx);
    let models = adapter.available_models().await.expect("models ok");

    assert!(!models.is_empty(), "expected ≥1 model");
    let ids: Vec<&str> = models.iter().map(|m| m.id.as_str()).collect();
    assert!(ids.contains(&"gpt-4o"));
    assert!(ids.contains(&"o1-mini"));
    assert!(ids.contains(&"text-davinci-003"));
    assert!(!ids.contains(&"dall-e-3"), "dall-e-3 must be filtered");
    assert!(!ids.contains(&"whisper-1"), "whisper-1 must be filtered");
    assert!(
        !ids.contains(&"text-embedding-3-small"),
        "embedding must be filtered"
    );
}

// ── health_check() ────────────────────────────────────────────────────────

#[tokio::test]
async fn openai_health_check_success() {
    let ctx = MockProviderServer::start("openai").await;
    ctx.mount_json(
        HttpMethod::Get,
        "/v1/models",
        200,
        json!({"object": "list", "data": [{"id": "gpt-4o"}]}),
    )
    .await;
    adapter_for(&ctx).health_check().await.expect("health ok");
}

// ── contract assertions ───────────────────────────────────────────────────

#[test]
fn openai_provider_info_contract() {
    let adapter = OpenAIAdapter::new("sk-x", None::<String>);
    let info = adapter.provider_info();
    assert_eq!(info.id, "openai");
    assert_eq!(info.name, "OpenAI");
    assert!(info.capabilities.supports_streaming);
    assert_eq!(info.base_url, DEFAULT_BASE_URL);
}

// ── API key safety ────────────────────────────────────────────────────────

#[tokio::test]
async fn openai_api_key_not_in_error_messages() {
    let ctx = MockProviderServer::start("openai").await;
    ctx.mount_json(
        HttpMethod::Post,
        "/v1/chat/completions",
        401,
        json!({"error": {"message": "Incorrect API key"}}),
    )
    .await;

    let adapter = OpenAIAdapter::new("sk-canary-secret-1234", Some(ctx.base_url()));
    let err = adapter
        .complete(CompletionRequest::simple("gpt-4o", "hi"))
        .await
        .unwrap_err();
    let err_str = err.to_string();
    assert!(
        !err_str.contains("sk-canary-secret-1234"),
        "API key leaked: {err_str}"
    );
}

// ── o-series quirks ───────────────────────────────────────────────────────

#[test]
fn o_series_uses_max_completion_tokens_and_no_temperature() {
    let req = CompletionRequest {
        model: "o1".to_owned(),
        messages: vec![
            Message::system("You are helpful."),
            Message::user("Explain recursion."),
        ],
        max_tokens: Some(500),
        temperature: Some(0.7),
        stream: false,
        system: None,
    };

    let oai = OpenAIAdapter::build_request(&req, false);

    assert_eq!(
        oai.max_completion_tokens,
        Some(500),
        "o-series needs max_completion_tokens"
    );
    assert_eq!(oai.max_tokens, None, "o-series must not send max_tokens");
    assert_eq!(oai.temperature, None, "o-series must not send temperature");
    assert_eq!(
        oai.messages[0].role, "developer",
        "system → developer for o-series"
    );
    assert_eq!(oai.messages[1].role, "user");
}

#[test]
fn standard_gpt4o_uses_max_tokens_and_temperature() {
    let req = CompletionRequest {
        model: "gpt-4o".to_owned(),
        messages: vec![Message::user("hello")],
        max_tokens: Some(100),
        temperature: Some(0.5),
        stream: false,
        system: None,
    };

    let oai = OpenAIAdapter::build_request(&req, false);

    assert_eq!(oai.max_tokens, Some(100));
    assert_eq!(oai.max_completion_tokens, None);
    assert_eq!(oai.temperature, Some(0.5));
}

// ── context window table ──────────────────────────────────────────────────

#[test]
fn context_window_lookup_coverage() {
    assert_eq!(model_context_window("gpt-4o"), 128_000);
    assert_eq!(model_context_window("gpt-4o-mini"), 128_000);
    assert_eq!(model_context_window("gpt-4-turbo"), 128_000);
    assert_eq!(model_context_window("gpt-4"), 8_192);
    assert_eq!(model_context_window("gpt-3.5-turbo"), 16_385);
    assert_eq!(model_context_window("o1"), 128_000);
    assert_eq!(model_context_window("o3-mini"), 128_000);
    assert_eq!(model_context_window("o4-mini"), 128_000);
    assert_eq!(model_context_window("text-davinci-003"), 4_096);
    assert_eq!(model_context_window("unknown-xyz"), 4_096);
}

// ── base_url configurability ──────────────────────────────────────────────

#[test]
fn custom_base_url_stored_in_provider_info() {
    let adapter = OpenAIAdapter::new("sk-k", Some("https://my-tenant.openai.azure.com"));
    assert_eq!(
        adapter.provider_info().base_url,
        "https://my-tenant.openai.azure.com"
    );
}

#[test]
fn default_base_url_is_api_openai_com() {
    let adapter = OpenAIAdapter::new("sk-k", None::<String>);
    assert_eq!(adapter.provider_info().base_url, DEFAULT_BASE_URL);
}

// ── OAuth effective_token precedence ──────────────────────────────────────

#[test]
fn oauth_token_wins_over_api_key() {
    use crate::oauth::client::OAuthProviderConfig;
    let cfg = OAuthProviderConfig {
        client_id: "cid".to_owned(),
        client_secret: String::new(),
        auth_url: "https://chat.openai.com/oauth/authorize".to_owned(),
        token_url: "https://auth.openai.com/oauth/token".to_owned(),
        scopes: vec!["openid".to_owned()],
    };
    let adapter = OpenAIAdapter::with_oauth_token("oauth-token", "rt", cfg, None::<String>);
    assert_eq!(adapter.effective_token(), "oauth-token");
    assert!(adapter.oauth_access_token.is_some());
}

#[test]
fn api_key_used_when_no_oauth_token() {
    let adapter = OpenAIAdapter::new("sk-test", None::<String>);
    assert_eq!(adapter.effective_token(), "sk-test");
    assert!(adapter.oauth_access_token.is_none());
}

// ── OAuth 401 + refresh + retry ───────────────────────────────────────────

#[tokio::test]
async fn openai_oauth_401_triggers_refresh_and_retry() {
    use crate::oauth::client::OAuthProviderConfig;
    use wiremock::matchers::{body_string_contains, method as wm_method, path as wm_path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    let api_server = MockServer::start().await;
    let token_server = MockServer::start().await;

    // First API call: 401.
    Mock::given(wm_method("POST"))
        .and(wm_path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
        .up_to_n_times(1)
        .mount(&api_server)
        .await;

    // Second API call: 200.
    Mock::given(wm_method("POST"))
        .and(wm_path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
            "model": "gpt-4o",
            "choices": [{"message": {"content": "hello"}}],
            "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
        })))
        .mount(&api_server)
        .await;

    // Token refresh: succeeds.
    Mock::given(wm_method("POST"))
        .and(wm_path("/token"))
        .and(body_string_contains("refresh_token"))
        .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
            "access_token": "refreshed-openai-token",
            "expires_in": 3600,
            "token_type": "Bearer"
        })))
        .mount(&token_server)
        .await;

    let oauth_cfg = OAuthProviderConfig {
        client_id: "test-client".to_owned(),
        client_secret: String::new(),
        auth_url: "https://chat.openai.com/oauth/authorize".to_owned(),
        token_url: format!("{}/token", token_server.uri()),
        scopes: vec!["openid".to_owned()],
    };

    let adapter = OpenAIAdapter::with_oauth_token(
        "initial-token",
        "refresh-token-xyz",
        oauth_cfg,
        Some(api_server.uri()),
    );

    let req = CompletionRequest::simple("gpt-4o", "hello");
    let result = adapter.complete(req).await;
    assert!(
        result.is_ok(),
        "expected Ok after refresh+retry: {:?}",
        result
    );
}

#[tokio::test]
async fn openai_oauth_double_401_returns_oauth_expired() {
    use crate::oauth::client::OAuthProviderConfig;
    use wiremock::matchers::{method as wm_method, path as wm_path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    let api_server = MockServer::start().await;
    let token_server = MockServer::start().await;

    Mock::given(wm_method("POST"))
        .and(wm_path("/v1/chat/completions"))
        .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
        .mount(&api_server)
        .await;

    Mock::given(wm_method("POST"))
        .and(wm_path("/token"))
        .respond_with(ResponseTemplate::new(400).set_body_string("invalid_grant"))
        .mount(&token_server)
        .await;

    let oauth_cfg = OAuthProviderConfig {
        client_id: "tc".to_owned(),
        client_secret: String::new(),
        auth_url: "https://chat.openai.com/oauth/authorize".to_owned(),
        token_url: format!("{}/token", token_server.uri()),
        scopes: vec!["openid".to_owned()],
    };

    let adapter = OpenAIAdapter::with_oauth_token(
        "bad-token",
        "bad-rt",
        oauth_cfg,
        Some(api_server.uri()),
    );

    let err = adapter
        .complete(CompletionRequest::simple("gpt-4o", "hi"))
        .await
        .unwrap_err();
    assert!(
        matches!(err, ProviderError::OAuthExpired { ref provider } if provider == "openai"),
        "expected OAuthExpired(openai), got: {err:?}"
    );
}
