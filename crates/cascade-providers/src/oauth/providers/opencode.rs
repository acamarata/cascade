//! # oauth::providers::opencode
//!
//! OpenCode Go OAuth 2.0 PKCE configuration.
//!
//! ## Purpose
//! Provides `opencode_oauth_config` — a factory that builds an `OAuthProviderConfig`
//! for opencode.ai's PKCE authorization code flow.
//!
//! ## Keychain key
//! Tokens are stored under keychain service `"cascade.oauth.opencode"` (account
//! `"tokens"`) via `TokenStore`.
//!
//! ## Constraints
//! - Client ID comes from the caller (loaded from `~/.cascade/config.toml`).
//! - Client secret is empty string for public/PKCE desktop clients.
//! - Token values are NEVER logged.

use crate::oauth::client::OAuthProviderConfig;

// ── OpenCode OAuth 2.0 endpoints ──────────────────────────────────────────────

/// OpenCode Go authorization endpoint.
const OPENCODE_AUTH_URL: &str = "https://opencode.ai/oauth/authorize";

/// OpenCode Go token endpoint.
const OPENCODE_TOKEN_URL: &str = "https://opencode.ai/oauth/token";

/// OpenCode provider ID — matches keychain service suffix `cascade.oauth.opencode`.
pub const OPENCODE_PROVIDER_ID: &str = "opencode";

// ── Factory ───────────────────────────────────────────────────────────────────

/// Build an `OAuthProviderConfig` for the OpenCode Go PKCE flow.
///
/// ## Purpose
/// Returns a fully-populated `OAuthProviderConfig` targeting opencode.ai's OAuth
/// 2.0 endpoints with API read/write scopes.
///
/// ## Inputs
/// - `client_id`: the OAuth client ID registered for Cascade at opencode.ai.
///
/// ## Outputs
/// An `OAuthProviderConfig` ready to pass to `OAuthClient::new`.
///
/// ## Constraints
/// - `client_secret` is empty — public PKCE desktop client.
/// - Scopes: `["api:read", "api:write"]`.
///
/// ## SPORT
/// MASTER-RUST-CRATES.md: cascade-providers / opencode_oauth_config
pub fn opencode_oauth_config(client_id: &str) -> OAuthProviderConfig {
    OAuthProviderConfig {
        client_id: client_id.to_owned(),
        client_secret: String::new(),
        auth_url: OPENCODE_AUTH_URL.to_owned(),
        token_url: OPENCODE_TOKEN_URL.to_owned(),
        scopes: vec!["api:read".to_owned(), "api:write".to_owned()],
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
    fn opencode_oauth_config_urls() {
        let cfg = opencode_oauth_config("oc-client");
        assert_eq!(cfg.auth_url, OPENCODE_AUTH_URL);
        assert_eq!(cfg.token_url, OPENCODE_TOKEN_URL);
        assert_eq!(cfg.client_id, "oc-client");
        assert!(
            cfg.client_secret.is_empty(),
            "public client must have empty secret"
        );
    }

    #[test]
    fn opencode_oauth_config_scopes() {
        let cfg = opencode_oauth_config("cid");
        assert!(
            cfg.scopes.contains(&"api:read".to_owned()),
            "missing api:read scope"
        );
        assert!(
            cfg.scopes.contains(&"api:write".to_owned()),
            "missing api:write scope"
        );
        assert_eq!(cfg.scopes.len(), 2);
    }

    // ── Token-source refresh path ──────────────────────────────────────────────

    #[tokio::test]
    async fn opencode_refresh_token_path() {
        use crate::oauth::client::OAuthClient;

        let mock_server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "new-oc-at",
                "refresh_token": "new-oc-rt",
                "expires_in": 3600,
                "token_type": "Bearer"
            })))
            .mount(&mock_server)
            .await;

        let mut cfg = opencode_oauth_config("test-client");
        cfg.token_url = format!("{}/token", mock_server.uri());

        let client = OAuthClient::new(cfg);
        let tokens = client.refresh_token("old-rt").await.unwrap();

        assert_eq!(tokens.access_token, "new-oc-at");
        assert_eq!(tokens.refresh_token.as_deref(), Some("new-oc-rt"));
    }

    #[tokio::test]
    async fn opencode_refresh_token_failure() {
        use crate::oauth::client::{OAuthClient, OAuthClientError};

        let mock_server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(401).set_body_string("invalid_grant"))
            .mount(&mock_server)
            .await;

        let mut cfg = opencode_oauth_config("test-client");
        cfg.token_url = format!("{}/token", mock_server.uri());

        let client = OAuthClient::new(cfg);
        assert!(matches!(
            client.refresh_token("bad-rt").await,
            Err(OAuthClientError::TokenExchange(_))
        ));
    }
}
