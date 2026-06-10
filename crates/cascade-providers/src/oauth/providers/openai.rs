//! # oauth::providers::openai
//!
//! OpenAI OAuth 2.0 PKCE configuration.
//!
//! ## Purpose
//! Provides `openai_oauth_config` — a factory that builds an `OAuthProviderConfig`
//! for OpenAI's PKCE authorization code flow, targeting the standard OpenID
//! Connect scopes plus `offline_access` for refresh tokens.
//!
//! ## Keychain key
//! Tokens are stored under keychain service `"cascade.oauth.openai"` (account
//! `"tokens"`) via `TokenStore`.
//!
//! ## Constraints
//! - Client ID comes from the caller (loaded from `~/.cascade/config.toml`).
//! - Client secret is empty string for public/PKCE desktop clients.
//! - Token values are NEVER logged.

use crate::oauth::client::OAuthProviderConfig;

// ── OpenAI OAuth 2.0 endpoints ────────────────────────────────────────────────

/// OpenAI authorization endpoint.
const OPENAI_AUTH_URL: &str = "https://chat.openai.com/oauth/authorize";

/// OpenAI token endpoint.
const OPENAI_TOKEN_URL: &str = "https://auth.openai.com/oauth/token";

/// OpenAI provider ID — matches keychain service suffix `cascade.oauth.openai`.
pub const OPENAI_PROVIDER_ID: &str = "openai";

// ── Factory ───────────────────────────────────────────────────────────────────

/// Build an `OAuthProviderConfig` for the OpenAI PKCE flow.
///
/// ## Purpose
/// Returns a fully-populated `OAuthProviderConfig` targeting OpenAI's OAuth 2.0
/// endpoints with OIDC + offline_access scopes.
///
/// ## Inputs
/// - `client_id`: the OAuth client ID registered for Cascade at platform.openai.com.
///
/// ## Outputs
/// An `OAuthProviderConfig` ready to pass to `OAuthClient::new`.
///
/// ## Constraints
/// - `client_secret` is empty — public PKCE desktop client.
/// - Scopes: `["openid", "email", "profile", "offline_access"]`.
///
/// ## SPORT
/// MASTER-RUST-CRATES.md: cascade-providers / openai_oauth_config
pub fn openai_oauth_config(client_id: &str) -> OAuthProviderConfig {
    OAuthProviderConfig {
        client_id: client_id.to_owned(),
        client_secret: String::new(),
        auth_url: OPENAI_AUTH_URL.to_owned(),
        token_url: OPENAI_TOKEN_URL.to_owned(),
        scopes: vec![
            "openid".to_owned(),
            "email".to_owned(),
            "profile".to_owned(),
            "offline_access".to_owned(),
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
    fn openai_oauth_config_urls() {
        let cfg = openai_oauth_config("oai-client");
        assert_eq!(cfg.auth_url, OPENAI_AUTH_URL);
        assert_eq!(cfg.token_url, OPENAI_TOKEN_URL);
        assert_eq!(cfg.client_id, "oai-client");
        assert!(
            cfg.client_secret.is_empty(),
            "public client must have empty secret"
        );
    }

    #[test]
    fn openai_oauth_config_scopes() {
        let cfg = openai_oauth_config("cid");
        assert!(
            cfg.scopes.contains(&"openid".to_owned()),
            "missing openid scope"
        );
        assert!(
            cfg.scopes.contains(&"email".to_owned()),
            "missing email scope"
        );
        assert!(
            cfg.scopes.contains(&"profile".to_owned()),
            "missing profile scope"
        );
        assert!(
            cfg.scopes.contains(&"offline_access".to_owned()),
            "missing offline_access scope"
        );
        assert_eq!(cfg.scopes.len(), 4);
    }

    // ── Token-source refresh path ──────────────────────────────────────────────

    #[tokio::test]
    async fn openai_refresh_token_path() {
        use crate::oauth::client::OAuthClient;

        let mock_server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "new-oai-at",
                "refresh_token": "new-oai-rt",
                "expires_in": 3600,
                "token_type": "Bearer"
            })))
            .mount(&mock_server)
            .await;

        let mut cfg = openai_oauth_config("test-client");
        cfg.token_url = format!("{}/token", mock_server.uri());

        let client = OAuthClient::new(cfg);
        let tokens = client.refresh_token("old-rt").await.unwrap();

        assert_eq!(tokens.access_token, "new-oai-at");
        assert_eq!(tokens.refresh_token.as_deref(), Some("new-oai-rt"));
    }

    #[tokio::test]
    async fn openai_refresh_token_failure() {
        use crate::oauth::client::{OAuthClient, OAuthClientError};

        let mock_server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(401).set_body_string("invalid_grant"))
            .mount(&mock_server)
            .await;

        let mut cfg = openai_oauth_config("test-client");
        cfg.token_url = format!("{}/token", mock_server.uri());

        let client = OAuthClient::new(cfg);
        assert!(matches!(
            client.refresh_token("bad-rt").await,
            Err(OAuthClientError::TokenExchange(_))
        ));
    }
}
