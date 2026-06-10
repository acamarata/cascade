//! Test fixture helpers for cascade provider adapter tests.
//!
//! # Purpose
//!
//! Provides a wiremock-based test harness that W-02 adapter tickets reuse to
//! replay recorded HTTP responses without hitting live APIs.  All helpers are
//! compiled only under `#[cfg(test)]`.
//!
//! # Usage
//!
//! ```rust,ignore
//! #[cfg(test)]
//! mod tests {
//!     use cascade_providers::test_helpers::*;
//!
//!     #[tokio::test]
//!     async fn my_adapter_test() {
//!         let ctx = MockProviderServer::start("anthropic").await;
//!         ctx.mount_json(HttpMethod::Post, "/v1/messages", 200,
//!                        fixture_json("anthropic_complete")).await;
//!         // … call your adapter …
//!     }
//! }
//! ```
//!
//! # Constraints
//!
//! - Every public symbol is `#[cfg(test)]` — zero overhead in production builds.
//! - Fixture JSON files live at `tests/fixtures/*.json`.
//! - `MockProviderServer` is thread-safe; spawn one per test function.

#[cfg(test)]
pub mod test_support {
    use std::path::PathBuf;

    use serde_json::Value;
    use wiremock::matchers::{method as wm_method, path as wm_path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // ── HTTP method enum (mirrors wiremock, re-exported for callers) ──────────

    /// HTTP method selector for `MockProviderServer::mount_json`.
    #[derive(Clone, Copy, Debug)]
    pub enum HttpMethod {
        Get,
        Post,
        Put,
        Delete,
    }

    impl HttpMethod {
        fn as_str(self) -> &'static str {
            match self {
                Self::Get => "GET",
                Self::Post => "POST",
                Self::Put => "PUT",
                Self::Delete => "DELETE",
            }
        }
    }

    // ── MockProviderServer ────────────────────────────────────────────────────

    /// A running wiremock server pre-configured for a specific provider.
    ///
    /// The server is automatically stopped when the value is dropped (end of
    /// the test).  Create one per test function; they listen on random ports.
    pub struct MockProviderServer {
        /// The underlying wiremock mock server.
        pub server: MockServer,
        /// Provider identifier ("anthropic", "openai", "gemini", "ollama").
        pub provider: &'static str,
    }

    impl MockProviderServer {
        /// Start a mock server for the given provider.
        ///
        /// `provider` is a label used in error messages; it does not affect
        /// the server behaviour.
        pub async fn start(provider: &'static str) -> Self {
            Self {
                server: MockServer::start().await,
                provider,
            }
        }

        /// Base URL of the mock server (e.g. `"http://127.0.0.1:PORT"`).
        pub fn base_url(&self) -> String {
            self.server.uri()
        }

        /// Mount a mock that returns a JSON body for the given method + path.
        ///
        /// The `status` is the HTTP status code (200, 201, etc.).
        /// `body` is any `serde_json::Value`.
        pub async fn mount_json(
            &self,
            http_method: HttpMethod,
            route: &str,
            status: u16,
            body: Value,
        ) {
            Mock::given(wm_method(http_method.as_str()))
                .and(wm_path(route))
                .respond_with(ResponseTemplate::new(status).set_body_json(body))
                .mount(&self.server)
                .await;
        }

        /// Mount a mock that returns a raw string body (e.g. SSE text/event-stream).
        pub async fn mount_raw(
            &self,
            http_method: HttpMethod,
            route: &str,
            status: u16,
            content_type: &str,
            body: String,
        ) {
            Mock::given(wm_method(http_method.as_str()))
                .and(wm_path(route))
                .respond_with(
                    ResponseTemplate::new(status)
                        .insert_header("content-type", content_type)
                        .set_body_string(body),
                )
                .mount(&self.server)
                .await;
        }

        /// Mount a 429 response followed by a 200 JSON response for the same
        /// method + path — convenient for retry-path tests.
        pub async fn mount_retry_then_success(
            &self,
            http_method: HttpMethod,
            route: &str,
            retries: u32,
            success_body: Value,
        ) {
            for _ in 0..retries {
                Mock::given(wm_method(http_method.as_str()))
                    .and(wm_path(route))
                    .respond_with(
                        ResponseTemplate::new(429)
                            .append_header("retry-after", "0")
                            .append_header("content-length", "0"),
                    )
                    .up_to_n_times(1)
                    .mount(&self.server)
                    .await;
            }

            Mock::given(wm_method(http_method.as_str()))
                .and(wm_path(route))
                .respond_with(
                    ResponseTemplate::new(200).set_body_json(success_body),
                )
                .mount(&self.server)
                .await;
        }

        /// Assert that the server received exactly `n` requests total.
        pub async fn assert_request_count(&self, n: u64) {
            let received = self.server.received_requests().await.unwrap_or_default();
            assert_eq!(
                received.len() as u64,
                n,
                "[{}] expected {n} requests, got {}",
                self.provider,
                received.len()
            );
        }
    }

    // ── Fixture loading ───────────────────────────────────────────────────────

    /// Returns the path to the `tests/fixtures/` directory.
    ///
    /// Works from any working directory because `CARGO_MANIFEST_DIR` is set at
    /// compile time.
    fn fixtures_dir() -> PathBuf {
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("tests")
            .join("fixtures")
    }

    /// Load a JSON fixture from `tests/fixtures/<name>.json`.
    ///
    /// Panics if the file is missing or invalid JSON — both are test-setup
    /// errors that should never reach a passing CI run.
    pub fn fixture_json(name: &str) -> Value {
        let path = fixtures_dir().join(format!("{name}.json"));
        let content = std::fs::read_to_string(&path).unwrap_or_else(|e| {
            panic!("fixture file missing at {}: {e}", path.display())
        });
        serde_json::from_str(&content)
            .unwrap_or_else(|e| panic!("invalid JSON in fixture {name}: {e}"))
    }

    /// Load a text fixture from `tests/fixtures/<name>.txt` (for SSE bodies).
    pub fn fixture_text(name: &str) -> String {
        let path = fixtures_dir().join(format!("{name}.txt"));
        std::fs::read_to_string(&path)
            .unwrap_or_else(|e| panic!("fixture file missing at {}: {e}", path.display()))
    }

    // ── assert_adapter_contract ───────────────────────────────────────────────

    /// Shared contract assertions for any `ProviderAdapter` implementation.
    ///
    /// W-02 adapter tests call this helper after mounting a success fixture to
    /// verify common invariants without duplicating assertion code across every
    /// adapter test file.
    ///
    /// # Arguments
    ///
    /// * `content` — text returned by `adapter.complete(…).await?.content`
    /// * `model` — model string from the `CompletionResponse`
    /// * `prompt_tokens` — usage field from the response
    pub fn assert_completion_contract(content: &str, model: &str, prompt_tokens: u32) {
        assert!(
            !content.is_empty(),
            "completion content must not be empty"
        );
        assert!(
            !model.is_empty(),
            "completion model identifier must not be empty"
        );
        assert!(
            prompt_tokens > 0,
            "prompt_tokens must be positive; got {prompt_tokens}"
        );
    }

    // ── Canned SSE helpers ────────────────────────────────────────────────────

    /// Build a minimal OpenAI-style SSE stream string (multiple data lines,
    /// ending with `[DONE]`).
    pub fn make_openai_sse(chunks: &[&str]) -> String {
        let mut out = String::new();
        for chunk in chunks {
            let json = serde_json::json!({
                "choices": [{"delta": {"content": chunk}, "finish_reason": null}]
            });
            out.push_str(&format!("data: {json}\n\n"));
        }
        // Final stop chunk.
        let stop = serde_json::json!({
            "choices": [{"delta": {}, "finish_reason": "stop"}]
        });
        out.push_str(&format!("data: {stop}\n\n"));
        out.push_str("data: [DONE]\n\n");
        out
    }

    /// Build a minimal Anthropic-style SSE stream string.
    pub fn make_anthropic_sse(chunks: &[&str]) -> String {
        let mut out = String::new();
        for chunk in chunks {
            let json = serde_json::json!({
                "type": "content_block_delta",
                "delta": {"type": "text_delta", "text": chunk}
            });
            out.push_str(&format!("data: {json}\n\n"));
        }
        let done = serde_json::json!({"type": "message_stop"});
        out.push_str(&format!("data: {done}\n\n"));
        out
    }

    // ── Self-tests ────────────────────────────────────────────────────────────

    #[cfg(test)]
    mod self_tests {
        use super::*;
        use crate::http_client::CascadeHttpClient;

        #[test]
        fn fixture_json_loads_anthropic() {
            let v = fixture_json("anthropic_complete");
            assert!(v["content"].is_array() || v["content"].is_string() || !v["content"].is_null());
        }

        #[test]
        fn fixture_json_loads_openai() {
            let v = fixture_json("openai_complete");
            assert!(v["choices"].is_array());
        }

        #[test]
        fn fixture_json_loads_gemini() {
            let v = fixture_json("gemini_complete");
            assert!(v["candidates"].is_array());
        }

        #[test]
        fn make_openai_sse_contains_done() {
            let sse = make_openai_sse(&["hello", " world"]);
            assert!(sse.contains("[DONE]"));
            assert!(sse.contains("hello"));
        }

        #[test]
        fn make_anthropic_sse_contains_stop() {
            let sse = make_anthropic_sse(&["hi"]);
            assert!(sse.contains("message_stop"));
            assert!(sse.contains("hi"));
        }

        #[test]
        fn sse_line_parser_roundtrip() {
            let sse = make_openai_sse(&["tok"]);
            let parsed: Vec<&str> = sse
                .lines()
                .filter_map(CascadeHttpClient::parse_sse_line)
                .collect();
            assert!(!parsed.is_empty(), "expected at least one data line");
        }

        #[tokio::test]
        async fn mock_server_mount_and_get() {
            let ctx = MockProviderServer::start("test").await;
            ctx.mount_json(
                HttpMethod::Get,
                "/ping",
                200,
                serde_json::json!({"pong": true}),
            )
            .await;

            let client = CascadeHttpClient::new();
            let url = format!("{}/ping", ctx.base_url());
            let resp: serde_json::Value = client.get_json(&url, |b| b).await.unwrap();
            assert_eq!(resp["pong"], true);

            ctx.assert_request_count(1).await;
        }

        #[tokio::test]
        async fn mount_retry_then_success_works() {
            let ctx = MockProviderServer::start("test").await;
            ctx.mount_retry_then_success(
                HttpMethod::Get,
                "/r",
                2,
                serde_json::json!({"ok": true}),
            )
            .await;

            let client = CascadeHttpClient::new();
            let url = format!("{}/r", ctx.base_url());
            let resp: serde_json::Value = client.get_json(&url, |b| b).await.unwrap();
            assert_eq!(resp["ok"], true);
        }

        #[test]
        fn assert_completion_contract_passes() {
            assert_completion_contract("some text", "gpt-4o", 10);
        }
    }
}
