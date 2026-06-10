//! # oauth::providers::gemini
//!
//! Google OAuth 2.0 PKCE configuration for the Gemini Generative Language API.
//!
//! ## Purpose
//! Provides `gemini_oauth_config` — a factory that builds an `OAuthProviderConfig`
//! for Google's OAuth 2.0 PKCE flow, targeting the `generative-language.retriever`
//! scope required by the Gemini API.
//!
//! ## Keychain key
//! Tokens for this provider are stored under keychain service
//! `"cascade.oauth.gemini"` (account `"tokens"`) via `TokenStore`.
//!
//! ## Constraints
//! - Client ID comes from the caller (loaded from `~/.cascade/config.toml`).
//! - Client secret is empty string for public/PKCE-only desktop clients.
//! - Scopes are fixed; callers must not narrow or expand them.
//! - Token values are NEVER logged.

use crate::oauth::client::OAuthProviderConfig;

// ── Google OAuth 2.0 endpoints ────────────────────────────────────────────────

/// Google authorization endpoint (PKCE, offline access).
const GOOGLE_AUTH_URL: &str = "https://accounts.google.com/o/oauth2/v2/auth";

/// Google token endpoint.
const GOOGLE_TOKEN_URL: &str = "https://oauth2.googleapis.com/token";

/// Gemini provider ID — matches the keychain service suffix `cascade.oauth.gemini`.
pub const GEMINI_PROVIDER_ID: &str = "gemini";

// ── Factory ───────────────────────────────────────────────────────────────────

/// Build an `OAuthProviderConfig` for the Google/Gemini PKCE flow.
///
/// ## Purpose
/// Returns a fully-populated `OAuthProviderConfig` targeting Google's OAuth 2.0
/// endpoints with the scope needed to call the Generative Language API.
///
/// ## Inputs
/// - `client_id`: the Google OAuth client ID registered for Cascade (or the
///   user's own app). Loaded from `~/.cascade/config.toml` at startup.
///
/// ## Outputs
/// An `OAuthProviderConfig` ready to pass to `OAuthClient::new`.
///
/// ## Constraints
/// - `client_secret` is always empty — Google PKCE desktop flows are public clients.
/// - Scopes are fixed: `["https://www.googleapis.com/auth/generative-language.retriever"]`.
///
/// ## SPORT
/// MASTER-RUST-CRATES.md: cascade-providers / gemini_oauth_config
pub fn gemini_oauth_config(client_id: &str) -> OAuthProviderConfig {
    OAuthProviderConfig {
        client_id: client_id.to_owned(),
        client_secret: String::new(), // public PKCE client — no secret
        auth_url: GOOGLE_AUTH_URL.to_owned(),
        token_url: GOOGLE_TOKEN_URL.to_owned(),
        scopes: vec![
            "https://www.googleapis.com/auth/generative-language.retriever".to_owned(),
        ],
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // ── Config URL/scope assertions ────────────────────────────────────────────

    #[test]
    fn gemini_oauth_config_urls() {
        let cfg = gemini_oauth_config("my-client-id");
        assert_eq!(cfg.auth_url, GOOGLE_AUTH_URL);
        assert_eq!(cfg.token_url, GOOGLE_TOKEN_URL);
        assert_eq!(cfg.client_id, "my-client-id");
        assert!(cfg.client_secret.is_empty(), "public client must have empty secret");
    }

    #[test]
    fn gemini_oauth_config_scopes() {
        let cfg = gemini_oauth_config("cid");
        assert_eq!(cfg.scopes.len(), 1);
        assert_eq!(
            cfg.scopes[0],
            "https://www.googleapis.com/auth/generative-language.retriever"
        );
    }

    // ── Token-source refresh path — mock token endpoint ────────────────────────

    /// Token refresh: OAuthClient.refresh_token() hits the token endpoint and
    /// returns new tokens when given a valid refresh token.
    #[tokio::test]
    async fn gemini_refresh_token_path() {
        use crate::oauth::client::OAuthClient;

        let mock_server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "new-gemini-at",
                "refresh_token": "new-gemini-rt",
                "expires_in": 3600,
                "token_type": "Bearer"
            })))
            .mount(&mock_server)
            .await;

        let mut cfg = gemini_oauth_config("test-client");
        cfg.token_url = format!("{}/token", mock_server.uri());

        let client = OAuthClient::new(cfg);
        let tokens = client.refresh_token("old-rt").await.unwrap();

        assert_eq!(tokens.access_token, "new-gemini-at");
        assert_eq!(tokens.refresh_token.as_deref(), Some("new-gemini-rt"));
        assert_eq!(tokens.expires_in, 3600);
    }

    /// Token refresh failure: non-200 from token endpoint returns Err.
    #[tokio::test]
    async fn gemini_refresh_token_failure() {
        use crate::oauth::client::{OAuthClient, OAuthClientError};

        let mock_server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(400).set_body_string("invalid_grant"))
            .mount(&mock_server)
            .await;

        let mut cfg = gemini_oauth_config("test-client");
        cfg.token_url = format!("{}/token", mock_server.uri());

        let client = OAuthClient::new(cfg);
        assert!(matches!(
            client.refresh_token("bad-rt").await,
            Err(OAuthClientError::TokenExchange(_))
        ));
    }
}
