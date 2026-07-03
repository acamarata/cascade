//! Tests for the Gemini adapter.

// tests.rs is itself declared `mod tests;` by the parent module; the inner
// wrapper keeps the cfg(test) scoping self-contained in this file.
#[allow(clippy::module_inception)]
#[cfg(test)]
mod tests {
    use crate::adapters::gemini::{
        adapter::GeminiAdapter,
        config::{GeminiConfig, GEMINI_DIRECT_BASE, GEMINI_PROXY_BASE},
        stream::parse_gemini_stream_event,
    };
    use crate::error::ProviderError;
    use crate::test_helpers::test_support::{
        fixture_json, fixture_text, HttpMethod, MockProviderServer,
    };
    use crate::types::CompletionRequest;
    use crate::adapter::ProviderAdapter;
    use futures::StreamExt;
    use wiremock::matchers::{method, path, query_param};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // ── GFP toggle: proxy mode must NOT send key in URL or headers ────────────

    #[tokio::test]
    async fn proxy_mode_no_key_in_request() {
        let server = MockServer::start().await;

        // This mock will FAIL the test if `key` query param is present.
        Mock::given(method("POST"))
            .and(path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(ResponseTemplate::new(200).set_body_json(fixture_json("gemini_complete")))
            .mount(&server)
            .await;

        let mut config = GeminiConfig::proxy();
        config.base_url = server.uri(); // point to mock

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "hello");
        let result = adapter.complete(req).await;
        assert!(result.is_ok(), "proxy mode should succeed: {:?}", result);

        // Verify no `key` query param was sent.
        let received = server.received_requests().await.unwrap();
        assert_eq!(received.len(), 1);
        let url = received[0].url.to_string();
        assert!(
            !url.contains("key="),
            "proxy mode MUST NOT include key in URL: {url}"
        );
        // Verify no x-goog-api-key or Authorization header.
        let headers = &received[0].headers;
        let has_auth =
            headers.contains_key("x-goog-api-key") || headers.contains_key("authorization");
        assert!(!has_auth, "proxy mode MUST NOT send auth headers");
    }

    // ── Direct mode sends key in URL ──────────────────────────────────────────

    #[tokio::test]
    async fn direct_mode_includes_key_in_url() {
        let server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .and(query_param("key", "test-api-key"))
            .respond_with(ResponseTemplate::new(200).set_body_json(fixture_json("gemini_complete")))
            .mount(&server)
            .await;

        let mut config = GeminiConfig::direct("test-api-key");
        config.base_url = server.uri();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("direct mode complete");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(resp.usage.total_tokens > 0, "total_tokens must be > 0");
    }

    // ── Happy-path complete() from fixture ────────────────────────────────────

    #[tokio::test]
    async fn complete_happy_path() {
        let ctx = MockProviderServer::start("gemini").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1beta/models/gemini-2.0-flash:generateContent",
            200,
            fixture_json("gemini_complete"),
        )
        .await;

        let mut config = GeminiConfig::direct("test-key");
        config.base_url = ctx.base_url();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("complete");

        assert_eq!(resp.content, "The capital of France is Paris.");
        assert_eq!(resp.usage.prompt_tokens, 14);
        assert_eq!(resp.usage.completion_tokens, 9);
        assert_eq!(resp.usage.total_tokens, 23);
        assert!(!resp.model.is_empty());

        crate::test_helpers::test_support::assert_completion_contract(
            &resp.content,
            &resp.model,
            resp.usage.prompt_tokens,
        );
    }

    // ── Rate-limit error mapping ──────────────────────────────────────────────

    #[tokio::test]
    async fn complete_rate_limit_maps_to_rate_limited() {
        let server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(ResponseTemplate::new(429).append_header("retry-after", "0"))
            .mount(&server)
            .await;

        let mut config = GeminiConfig::direct("test-key");
        config.base_url = server.uri();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "hi");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::RateLimited { .. }),
            "expected RateLimited, got {err:?}"
        );
    }

    // ── Streaming: multi-event SSE fixture yields ≥1 chunk ───────────────────

    #[tokio::test]
    async fn complete_stream_yields_chunks() {
        let ctx = MockProviderServer::start("gemini-stream").await;
        let sse_body = fixture_text("gemini_stream");
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1beta/models/gemini-2.0-flash:streamGenerateContent",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let mut config = GeminiConfig::direct("test-key");
        config.base_url = ctx.base_url();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "Stream test");
        let stream = adapter.complete_stream(req).await.expect("stream start");

        let chunks: Vec<_> = stream.collect().await;
        assert!(!chunks.is_empty(), "expected at least 1 chunk");

        let ok_chunks: Vec<crate::types::StreamChunk> = chunks.into_iter().map(|r| r.unwrap()).collect();
        let full_text: String = ok_chunks.iter().map(|c| c.delta.as_str()).collect();
        assert!(
            !full_text.is_empty(),
            "combined delta text must not be empty"
        );
    }

    // ── Streaming proxy mode: no key in URL ───────────────────────────────────

    #[tokio::test]
    async fn stream_proxy_mode_no_key() {
        let ctx = MockProviderServer::start("gemini-stream-proxy").await;
        let sse_body = fixture_text("gemini_stream");
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1beta/models/gemini-2.0-flash:streamGenerateContent",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let mut config = GeminiConfig::proxy();
        config.base_url = ctx.base_url();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "proxy stream");
        let stream = adapter.complete_stream(req).await.expect("stream");
        let chunks: Vec<_> = stream.collect().await;
        assert!(!chunks.is_empty());

        // Verify no key in URL.
        let received = ctx.server.received_requests().await.unwrap();
        assert_eq!(received.len(), 1);
        let url = received[0].url.to_string();
        assert!(
            !url.contains("key="),
            "proxy stream MUST NOT include key: {url}"
        );
    }

    // ── provider_info contract ────────────────────────────────────────────────

    #[test]
    fn provider_info_is_valid() {
        let adapter = GeminiAdapter::new(GeminiConfig::direct("dummy"));
        let info = adapter.provider_info();
        assert_eq!(info.id, "google-gemini");
        assert!(!info.name.is_empty());
        assert!(info.capabilities.supports_streaming);
    }

    // ── available_models returns all expected entries ─────────────────────────

    #[tokio::test]
    async fn available_models_includes_flash_and_pro() {
        let adapter = GeminiAdapter::new(GeminiConfig::direct("dummy"));
        let models = adapter.available_models().await.unwrap();
        let ids: Vec<&str> = models.iter().map(|m| m.id.as_str()).collect();
        assert!(
            ids.contains(&"gemini-2.0-flash"),
            "must include gemini-2.0-flash"
        );
        assert!(
            ids.contains(&"gemini-1.5-pro"),
            "must include gemini-1.5-pro"
        );
        assert!(
            ids.contains(&"gemini-1.0-pro"),
            "must include gemini-1.0-pro"
        );
    }

    // ── proxy mode changes base_url ───────────────────────────────────────────

    #[test]
    fn proxy_config_sets_base_url() {
        let config = GeminiConfig::proxy();
        assert_eq!(config.base_url, GEMINI_PROXY_BASE);
        assert!(config.use_gfp_proxy);
        assert!(config.api_key.is_none());
    }

    #[test]
    fn direct_config_sets_base_url() {
        let config = GeminiConfig::direct("my-key");
        assert_eq!(config.base_url, GEMINI_DIRECT_BASE);
        assert!(!config.use_gfp_proxy);
        assert!(config.api_key.is_some());
    }

    // ── parse_gemini_stream_event unit tests ──────────────────────────────────

    #[test]
    fn parse_event_with_text() {
        let payload = r#"{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":""}]}"#;
        let result = parse_gemini_stream_event(payload).unwrap();
        assert!(result.is_some());
        assert_eq!(result.unwrap().delta, "Hello");
    }

    #[test]
    fn parse_event_with_stop() {
        let payload = r#"{"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP"}]}"#;
        let result = parse_gemini_stream_event(payload).unwrap();
        let chunk = result.unwrap();
        assert!(chunk.finish_reason.is_some());
        assert_eq!(chunk.finish_reason.unwrap(), "stop");
    }

    #[test]
    fn parse_empty_event_returns_none() {
        let payload = r#"{"candidates":[{"content":{"parts":[{"text":""}]}}]}"#;
        let result = parse_gemini_stream_event(payload).unwrap();
        assert!(result.is_none());
    }

    // ── OAuth 401 + refresh + retry ───────────────────────────────────────────

    /// 401 from Gemini triggers refresh + retry; second call returns Ok.
    #[tokio::test]
    async fn oauth_401_triggers_refresh_and_retry() {
        use crate::oauth::client::OAuthProviderConfig;
        use wiremock::matchers::{body_string_contains, method as wm_method, path as wm_path};
        use wiremock::{Mock, MockServer, ResponseTemplate};

        let api_server = MockServer::start().await;
        let token_server = MockServer::start().await;

        // First call: 401 (expired token).
        // Second call: 200 (after refresh).
        Mock::given(wm_method("POST"))
            .and(wm_path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
            .up_to_n_times(1)
            .mount(&api_server)
            .await;
        Mock::given(wm_method("POST"))
            .and(wm_path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(ResponseTemplate::new(200).set_body_json(fixture_json("gemini_complete")))
            .mount(&api_server)
            .await;

        // Token server returns a new access token on refresh.
        Mock::given(wm_method("POST"))
            .and(wm_path("/token"))
            .and(body_string_contains("refresh_token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "refreshed-gemini-token",
                "expires_in": 3600,
                "token_type": "Bearer"
            })))
            .mount(&token_server)
            .await;

        let oauth_cfg = OAuthProviderConfig {
            client_id: "test-client".to_owned(),
            client_secret: String::new(),
            auth_url: "https://accounts.google.com/o/oauth2/v2/auth".to_owned(),
            token_url: format!("{}/token", token_server.uri()),
            scopes: vec![
                "https://www.googleapis.com/auth/generative-language.retriever".to_owned(),
            ],
        };

        let config = GeminiConfig {
            base_url: api_server.uri(),
            ..Default::default()
        };

        let adapter = GeminiAdapter::with_oauth_token(
            config,
            "initial-token",
            "refresh-token-xyz",
            oauth_cfg,
        );

        let req = CompletionRequest::simple("gemini-2.0-flash", "hello");
        let result = adapter.complete(req).await;
        assert!(
            result.is_ok(),
            "expected Ok after refresh+retry, got: {:?}",
            result
        );
    }

    /// Double 401 (refresh also fails): returns OAuthExpired.
    #[tokio::test]
    async fn oauth_double_401_returns_oauth_expired() {
        use crate::oauth::client::OAuthProviderConfig;
        use wiremock::matchers::{method as wm_method, path as wm_path};
        use wiremock::{Mock, MockServer, ResponseTemplate};

        let api_server = MockServer::start().await;
        let token_server = MockServer::start().await;

        // All API calls return 401.
        Mock::given(wm_method("POST"))
            .and(wm_path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
            .mount(&api_server)
            .await;

        // Token server also returns 400 (refresh fails).
        Mock::given(wm_method("POST"))
            .and(wm_path("/token"))
            .respond_with(ResponseTemplate::new(400).set_body_string("invalid_grant"))
            .mount(&token_server)
            .await;

        let oauth_cfg = OAuthProviderConfig {
            client_id: "test-client".to_owned(),
            client_secret: String::new(),
            auth_url: "https://accounts.google.com/o/oauth2/v2/auth".to_owned(),
            token_url: format!("{}/token", token_server.uri()),
            scopes: vec![
                "https://www.googleapis.com/auth/generative-language.retriever".to_owned(),
            ],
        };

        let config = GeminiConfig {
            base_url: api_server.uri(),
            ..Default::default()
        };

        let adapter =
            GeminiAdapter::with_oauth_token(config, "bad-token", "bad-refresh-token", oauth_cfg);

        let req = CompletionRequest::simple("gemini-2.0-flash", "hello");
        let err = adapter.complete(req).await.unwrap_err();
        assert!(
            matches!(err, ProviderError::OAuthExpired { ref provider } if provider == "gemini"),
            "expected OAuthExpired(gemini), got: {err:?}"
        );
    }

    // ── system-role hoisting into systemInstruction ────────────────────────────

    /// Gemini has no system role in `contents`, so system-role turns (e.g.
    /// the middleware compression summary) must be HOISTED into
    /// `systemInstruction`, merged after `req.system`. The pre-fix mapper
    /// filtered them out of `contents` without hoisting — the summary was
    /// deleted from the wire request entirely on the default gp-pool path.
    #[test]
    fn map_request_hoists_system_turns_into_system_instruction() {
        use crate::types::Message;

        let adapter = GeminiAdapter::new(GeminiConfig::proxy());
        let req = CompletionRequest {
            model: "gemini-2.0-flash".into(),
            messages: vec![
                Message::system("Summary of the earlier conversation: chose sqlite."),
                Message::user("continue"),
            ],
            max_tokens: None,
            temperature: None,
            stream: false,
            system: Some("# Project context".into()),
        };

        let wire = adapter.map_request(&req);

        let si = wire.system_instruction.expect("systemInstruction present");
        let text = &si.parts[0].text;
        let prefix_pos = text.find("# Project context").expect("req.system kept");
        let summary_pos = text.find("chose sqlite").expect("system turn hoisted");
        assert!(prefix_pos < summary_pos, "req.system must come first: {text}");
        // contents holds only the non-system turns.
        assert_eq!(wire.contents.len(), 1);
        assert_eq!(wire.contents[0].role, "user");
    }

    /// A system turn with NO explicit `req.system` still reaches the wire.
    #[test]
    fn map_request_system_turn_alone_becomes_system_instruction() {
        use crate::types::Message;

        let adapter = GeminiAdapter::new(GeminiConfig::proxy());
        let req = CompletionRequest {
            model: "gemini-2.0-flash".into(),
            messages: vec![Message::system("only summary"), Message::user("q")],
            max_tokens: None,
            temperature: None,
            stream: false,
            system: None,
        };

        let wire = adapter.map_request(&req);
        let si = wire.system_instruction.expect("systemInstruction present");
        assert_eq!(si.parts[0].text, "only summary");
        assert_eq!(wire.contents.len(), 1);
    }
}
