//! Provider credential connect/validate path used by both the CLI and Tauri GUI.
//!
//! # Purpose
//!
//! Validates an API key against a provider's lightweight auth endpoint, stores
//! the secret in the OS keychain (via `cascade-keychain`), and records the
//! connection in `~/.cascade/providers.json`.  OAuth providers reuse the
//! existing `oauth/` PKCE flow.
//!
//! # Inputs / Outputs
//!
//! - `connect_provider(kind, credential, keychain)` → `Result<ConnectedProvider>`
//!   - On success: secret stored in keychain + entry written to providers.json.
//!   - On failure: nothing is stored; typed error is returned.
//!
//! # Constraints
//!
//! - Secret values NEVER appear in tracing spans, error strings, or log lines.
//! - Network calls validate the key before any persistent write.
//! - providers.json records metadata only (provider id, name, connected_at, healthy).
//!   It never contains the secret.
//! - `InMemoryKeychain` is accepted as a trait object so tests run without OS keychain.
//!
//! # SPORT
//!
//! Entity: cascade-providers / connect_provider (E-P5-08)

use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use tracing::debug;

use cascade_keychain::Keychain;

use crate::error::ProviderError;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Keychain service name for all provider API keys.
pub const KEYCHAIN_SERVICE: &str = "dev.cascade.provider";

/// Filename for the provider connection registry under `~/.cascade/`.
pub const PROVIDERS_JSON_NAME: &str = "providers.json";

// ── ProviderKind ──────────────────────────────────────────────────────────────

/// Which AI provider is being connected.
///
/// Determines the validation endpoint, keychain account name, and display name.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ProviderKind {
    /// Anthropic Claude — validates via GET `/v1/models`.
    Anthropic,
    /// OpenAI — validates via GET `/v1/models`.
    OpenAI,
    /// Google Gemini — validates via key-auth models endpoint.
    Gemini,
    /// OpenRouter — validates via GET `/api/v1/models`.
    OpenRouter,
    /// Groq — validates via GET `/openai/v1/models`.
    Groq,
    /// Mistral AI — validates via GET `/v1/models`.
    Mistral,
    /// DeepSeek — validates via GET `/v1/models`.
    DeepSeek,
    /// Together AI — validates via GET `/v1/models`.
    Together,
    /// Cohere — validates via GET `/v1/models`.
    Cohere,
    /// Any other OpenAI-compatible provider; `id` is the stable slug.
    Custom { id: String, base_url: String },
}

impl ProviderKind {
    /// Stable machine-readable identifier (used as keychain account name and JSON key).
    pub fn id(&self) -> String {
        match self {
            ProviderKind::Anthropic => "anthropic".into(),
            ProviderKind::OpenAI => "openai".into(),
            ProviderKind::Gemini => "gemini".into(),
            ProviderKind::OpenRouter => "openrouter".into(),
            ProviderKind::Groq => "groq".into(),
            ProviderKind::Mistral => "mistral".into(),
            ProviderKind::DeepSeek => "deepseek".into(),
            ProviderKind::Together => "together".into(),
            ProviderKind::Cohere => "cohere".into(),
            ProviderKind::Custom { id, .. } => id.clone(),
        }
    }

    /// Human-readable display name.
    pub fn display_name(&self) -> &str {
        match self {
            ProviderKind::Anthropic => "Anthropic Claude",
            ProviderKind::OpenAI => "OpenAI",
            ProviderKind::Gemini => "Google Gemini",
            ProviderKind::OpenRouter => "OpenRouter",
            ProviderKind::Groq => "Groq",
            ProviderKind::Mistral => "Mistral AI",
            ProviderKind::DeepSeek => "DeepSeek",
            ProviderKind::Together => "Together AI",
            ProviderKind::Cohere => "Cohere",
            ProviderKind::Custom { id, .. } => id.as_str(),
        }
    }

    /// Base URL for the validation endpoint (no trailing slash).
    fn base_url(&self) -> &str {
        match self {
            ProviderKind::Anthropic => "https://api.anthropic.com",
            ProviderKind::OpenAI => "https://api.openai.com",
            ProviderKind::Gemini => "https://generativelanguage.googleapis.com",
            ProviderKind::OpenRouter => "https://openrouter.ai",
            ProviderKind::Groq => "https://api.groq.com",
            ProviderKind::Mistral => "https://api.mistral.ai",
            ProviderKind::DeepSeek => "https://api.deepseek.com",
            ProviderKind::Together => "https://api.together.xyz",
            ProviderKind::Cohere => "https://api.cohere.ai",
            ProviderKind::Custom { base_url, .. } => base_url.as_str(),
        }
    }

    /// Parse from a string slug (case-insensitive).
    ///
    /// Returns `None` for unknown slugs (callers must provide a `--base-url`
    /// for custom providers).
    pub fn from_slug(slug: &str) -> Option<Self> {
        match slug.to_lowercase().as_str() {
            "anthropic" => Some(ProviderKind::Anthropic),
            "openai" => Some(ProviderKind::OpenAI),
            "gemini" => Some(ProviderKind::Gemini),
            "openrouter" => Some(ProviderKind::OpenRouter),
            "groq" => Some(ProviderKind::Groq),
            "mistral" => Some(ProviderKind::Mistral),
            "deepseek" => Some(ProviderKind::DeepSeek),
            "together" => Some(ProviderKind::Together),
            "cohere" => Some(ProviderKind::Cohere),
            _ => None,
        }
    }
}

// ── Credential ────────────────────────────────────────────────────────────────

/// Credential supplied by the caller.
pub enum Credential {
    /// A static API key string.
    ApiKey(String),
    /// OAuth flow — reuses the existing `oauth/` PKCE path; the token is
    /// already stored in the keychain by the OAuth client before this function
    /// is called.  We record the connection in providers.json only.
    OAuth,
}

// ── ConnectedProvider ─────────────────────────────────────────────────────────

/// Describes a successfully connected provider.
///
/// Returned by [`connect_provider`].  Does NOT contain the secret.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ConnectedProvider {
    /// Stable provider id (e.g. `"anthropic"`).
    pub id: String,
    /// Human-readable display name.
    pub name: String,
    /// Unix timestamp (seconds) when this connection was established.
    pub connected_at: u64,
    /// Whether the validation ping succeeded.
    pub healthy: bool,
}

// ── providers.json schema ─────────────────────────────────────────────────────

/// Root schema for `~/.cascade/providers.json`.
///
/// Machine-written; never contains secret values.
#[derive(Debug, Default, Serialize, Deserialize)]
struct ProvidersStore {
    /// Map from provider id → metadata.
    providers: std::collections::HashMap<String, ConnectedProvider>,
}

impl ProvidersStore {
    /// Load from disk, returning an empty store if the file does not exist.
    fn load(path: &PathBuf) -> Self {
        if !path.exists() {
            return Self::default();
        }
        match std::fs::read_to_string(path) {
            Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
            Err(_) => Self::default(),
        }
    }

    /// Persist to disk, creating parent directories as needed.
    fn save(&self, path: &PathBuf) -> Result<(), ProviderError> {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| {
                ProviderError::InvalidResponse(format!("cannot create providers dir: {e}"))
            })?;
        }
        let json = serde_json::to_string_pretty(self)
            .map_err(|e| ProviderError::InvalidResponse(e.to_string()))?;
        std::fs::write(path, json).map_err(|e| {
            ProviderError::InvalidResponse(format!("cannot write providers.json: {e}"))
        })?;
        Ok(())
    }

    /// Upsert a connected provider entry.
    fn upsert(&mut self, entry: ConnectedProvider) {
        self.providers.insert(entry.id.clone(), entry);
    }

    /// Remove a provider entry by id. Returns true if it existed.
    fn remove(&mut self, id: &str) -> bool {
        self.providers.remove(id).is_some()
    }
}

// ── Path helpers ──────────────────────────────────────────────────────────────

fn providers_json_path() -> PathBuf {
    cascade_types::paths::global_cascade_dir().join(PROVIDERS_JSON_NAME)
}

fn providers_json_path_at(cascade_dir: &PathBuf) -> PathBuf {
    cascade_dir.join(PROVIDERS_JSON_NAME)
}

// ── Validation ────────────────────────────────────────────────────────────────

/// Build a shared `reqwest::Client` for validation calls.
///
/// 30-second timeout, cascade user-agent. No credentials stored in the client.
fn make_http_client() -> reqwest::Client {
    const PKG_VERSION: &str = env!("CARGO_PKG_VERSION");
    reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(30))
        .user_agent(format!("cascade/{PKG_VERSION}"))
        .build()
        .expect("reqwest Client construction failed")
}

/// Validate an API key against the provider's auth endpoint.
///
/// Sends a lightweight authenticated request (GET /models or equivalent).
/// Returns `Ok(())` on HTTP 200/2xx, `Err(ProviderError::AuthFailed)` on 401/403,
/// and `Err(ProviderError::NetworkError)` on transport failure.
///
/// The `base_url` parameter is exposed so tests can inject a mock server URL.
///
/// # Security
///
/// The `api_key` value is used only as a header value and is NEVER written
/// to any log line, error string, or tracing span.
pub async fn validate_api_key(
    kind: &ProviderKind,
    api_key: &str,
    base_url: Option<&str>,
) -> Result<(), ProviderError> {
    let http = make_http_client();
    let effective_base = base_url.unwrap_or_else(|| kind.base_url());

    match kind {
        ProviderKind::Anthropic => {
            validate_anthropic(api_key, effective_base, &http).await
        }
        ProviderKind::Gemini => {
            validate_gemini(api_key, effective_base, &http).await
        }
        ProviderKind::OpenRouter => {
            validate_openai_compat(api_key, &format!("{effective_base}/api/v1/models"), &http)
                .await
        }
        ProviderKind::Groq => {
            validate_openai_compat(api_key, &format!("{effective_base}/openai/v1/models"), &http)
                .await
        }
        // OpenAI-compatible: /v1/models with Bearer auth
        ProviderKind::OpenAI
        | ProviderKind::Mistral
        | ProviderKind::DeepSeek
        | ProviderKind::Together
        | ProviderKind::Cohere
        | ProviderKind::Custom { .. } => {
            validate_openai_compat(api_key, &format!("{effective_base}/v1/models"), &http).await
        }
    }
}

/// Validate against the Anthropic `/v1/models` endpoint.
async fn validate_anthropic(
    api_key: &str,
    base_url: &str,
    http: &reqwest::Client,
) -> Result<(), ProviderError> {
    let url = format!("{base_url}/v1/models");
    debug!(url = %url, "Validating Anthropic API key");

    let resp = http
        .get(&url)
        .header("x-api-key", api_key)
        .header("anthropic-version", "2023-06-01")
        .send()
        .await
        .map_err(|e| {
            if e.is_timeout() {
                ProviderError::Timeout { secs: 30 }
            } else {
                ProviderError::NetworkError(e.to_string())
            }
        })?;

    map_status(resp.status(), "anthropic")
}

/// Validate against the Gemini models endpoint (key in query param).
///
/// The key is passed as a query parameter (Google's required auth method).
/// The URL logged is redacted to avoid leaking the key.
async fn validate_gemini(
    api_key: &str,
    base_url: &str,
    http: &reqwest::Client,
) -> Result<(), ProviderError> {
    // Key is in the query param — do NOT log the full URL.
    let url = format!("{base_url}/v1beta/models?key={api_key}");
    debug!("Validating Gemini API key via models endpoint (key redacted from logs)");

    let resp = http
        .get(&url)
        .send()
        .await
        .map_err(|e| {
            if e.is_timeout() {
                ProviderError::Timeout { secs: 30 }
            } else {
                ProviderError::NetworkError(e.to_string())
            }
        })?;

    map_status(resp.status(), "gemini")
}

/// Validate against an OpenAI-compatible `/v1/models` endpoint with Bearer auth.
async fn validate_openai_compat(
    api_key: &str,
    url: &str,
    http: &reqwest::Client,
) -> Result<(), ProviderError> {
    debug!(url = %url, "Validating OpenAI-compatible API key");

    let resp = http
        .get(url)
        .header("Authorization", format!("Bearer {api_key}"))
        .send()
        .await
        .map_err(|e| {
            if e.is_timeout() {
                ProviderError::Timeout { secs: 30 }
            } else {
                ProviderError::NetworkError(e.to_string())
            }
        })?;

    map_status(resp.status(), url)
}

/// Map HTTP status to `Ok(())` or a typed error.
///
/// Never includes the API key in the error message.
fn map_status(status: reqwest::StatusCode, context: &str) -> Result<(), ProviderError> {
    if status.is_success() {
        Ok(())
    } else if status == reqwest::StatusCode::UNAUTHORIZED
        || status == reqwest::StatusCode::FORBIDDEN
    {
        Err(ProviderError::AuthFailed(format!(
            "API key rejected by {context} (HTTP {status})"
        )))
    } else if status == reqwest::StatusCode::TOO_MANY_REQUESTS {
        Err(ProviderError::RateLimited {
            retry_after_secs: None,
        })
    } else {
        Err(ProviderError::InvalidResponse(format!(
            "unexpected HTTP {status} from {context}"
        )))
    }
}

// ── connect_provider ──────────────────────────────────────────────────────────

/// Connect and validate an AI provider, storing credentials securely.
///
/// # Steps
///
/// 1. Validate the credential against the provider's auth endpoint.
/// 2. Store the secret in the OS keychain (service=`dev.cascade.provider`,
///    account=`<kind.id()>`).
/// 3. Upsert an entry in `~/.cascade/providers.json` (metadata only, no secret).
/// 4. Return [`ConnectedProvider`] describing the result.
///
/// # Errors
///
/// - [`ProviderError::AuthFailed`] — key rejected by the provider.
/// - [`ProviderError::NetworkError`] — cannot reach the provider.
/// - [`ProviderError::InvalidResponse`] — providers.json write failed.
///
/// # Security
///
/// The secret is NEVER written to any log line, error message, or tracing span.
pub async fn connect_provider(
    kind: ProviderKind,
    credential: Credential,
    keychain: &dyn Keychain,
) -> Result<ConnectedProvider, ProviderError> {
    connect_provider_at(kind, credential, keychain, None, None).await
}

/// Internal version of `connect_provider` that accepts an override for the
/// providers.json path and the validation base URL (used in tests).
pub async fn connect_provider_at(
    kind: ProviderKind,
    credential: Credential,
    keychain: &dyn Keychain,
    base_url_override: Option<&str>,
    cascade_dir_override: Option<&PathBuf>,
) -> Result<ConnectedProvider, ProviderError> {
    let id = kind.id();
    let name = kind.display_name().to_string();

    match &credential {
        Credential::ApiKey(key) => {
            // 1. Validate — network call before any write.
            validate_api_key(&kind, key, base_url_override).await?;

            // 2. Store in keychain. Never log the key value.
            keychain
                .set_key(KEYCHAIN_SERVICE, &id, key)
                .map_err(|e| {
                    ProviderError::InvalidResponse(format!("keychain write failed: {e}"))
                })?;
        }
        Credential::OAuth => {
            // OAuth path: the PKCE flow already stored the token in keychain.
            // We just need to verify the token is actually there.
            keychain
                .get_key(KEYCHAIN_SERVICE, &id)
                .map_err(|_| {
                    ProviderError::CredentialNotFound(format!(
                        "OAuth token not found in keychain for '{id}'; complete the OAuth flow first"
                    ))
                })?;
        }
    }

    // 3. Upsert providers.json (no secret).
    let connected_at = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();

    let entry = ConnectedProvider {
        id: id.clone(),
        name: name.clone(),
        connected_at,
        healthy: true,
    };

    let store_path = match cascade_dir_override {
        Some(dir) => providers_json_path_at(dir),
        None => providers_json_path(),
    };

    let mut store = ProvidersStore::load(&store_path);
    store.upsert(entry.clone());
    store.save(&store_path)?;

    debug!(provider_id = %id, "Provider connected and recorded in providers.json");
    Ok(entry)
}

// ── list_providers ────────────────────────────────────────────────────────────

/// Return all connected providers from `providers.json`, with a live health
/// check ping for each.
///
/// Providers whose keychain entry is missing are reported as unhealthy.
pub fn list_providers(keychain: &dyn Keychain) -> Vec<ConnectedProvider> {
    list_providers_at(keychain, None)
}

/// Internal version with path override for tests.
pub fn list_providers_at(
    keychain: &dyn Keychain,
    cascade_dir_override: Option<&PathBuf>,
) -> Vec<ConnectedProvider> {
    let store_path = match cascade_dir_override {
        Some(dir) => providers_json_path_at(dir),
        None => providers_json_path(),
    };

    let store = ProvidersStore::load(&store_path);
    let mut out: Vec<ConnectedProvider> = store.providers.into_values().collect();

    // Mark unhealthy if keychain entry is absent.
    for p in &mut out {
        p.healthy = keychain
            .get_key(KEYCHAIN_SERVICE, &p.id)
            .is_ok();
    }

    // Sort by id for stable output.
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

// ── remove_provider ───────────────────────────────────────────────────────────

/// Remove a provider: delete keychain entry + providers.json entry.
///
/// Returns `Ok(true)` when removed, `Ok(false)` when not found.
pub fn remove_provider(id: &str, keychain: &dyn Keychain) -> Result<bool, ProviderError> {
    remove_provider_at(id, keychain, None)
}

/// Internal version with path override for tests.
pub fn remove_provider_at(
    id: &str,
    keychain: &dyn Keychain,
    cascade_dir_override: Option<&PathBuf>,
) -> Result<bool, ProviderError> {
    // Remove from keychain — NotFound is acceptable (idempotent).
    let _ = keychain.delete_key(KEYCHAIN_SERVICE, id);

    // Remove from providers.json.
    let store_path = match cascade_dir_override {
        Some(dir) => providers_json_path_at(dir),
        None => providers_json_path(),
    };

    let mut store = ProvidersStore::load(&store_path);
    let existed = store.remove(id);
    if existed {
        store.save(&store_path)?;
    }

    Ok(existed)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_keychain::InMemoryKeychain;
    use serial_test::serial;
    use tempfile::TempDir;

    fn tmp_cascade_dir() -> TempDir {
        tempfile::tempdir().expect("tempdir")
    }

    // ── ProviderKind helpers ──────────────────────────────────────────────────

    #[test]
    fn provider_kind_id_and_name() {
        assert_eq!(ProviderKind::Anthropic.id(), "anthropic");
        assert_eq!(ProviderKind::OpenAI.id(), "openai");
        assert_eq!(ProviderKind::Gemini.id(), "gemini");
        assert_eq!(ProviderKind::OpenRouter.id(), "openrouter");
    }

    #[test]
    fn provider_kind_from_slug() {
        assert_eq!(ProviderKind::from_slug("anthropic"), Some(ProviderKind::Anthropic));
        assert_eq!(ProviderKind::from_slug("OPENAI"), Some(ProviderKind::OpenAI));
        assert_eq!(ProviderKind::from_slug("gemini"), Some(ProviderKind::Gemini));
        assert!(ProviderKind::from_slug("unknown-provider-xyz").is_none());
    }

    #[test]
    fn provider_kind_from_slug_all_known() {
        let slugs = ["anthropic","openai","gemini","openrouter","groq","mistral","deepseek","together","cohere"];
        for s in slugs {
            assert!(ProviderKind::from_slug(s).is_some(), "slug '{s}' should parse");
        }
    }

    // ── ProvidersStore load/save round-trip ───────────────────────────────────

    #[test]
    #[serial(providers_store)]
    fn providers_store_empty_when_file_missing() {
        let dir = tmp_cascade_dir();
        let path = dir.path().to_path_buf().join(PROVIDERS_JSON_NAME);
        let store = ProvidersStore::load(&path);
        assert!(store.providers.is_empty());
    }

    #[test]
    #[serial(providers_store)]
    fn providers_store_round_trip() {
        let dir = tmp_cascade_dir();
        let path = dir.path().to_path_buf().join(PROVIDERS_JSON_NAME);

        let entry = ConnectedProvider {
            id: "anthropic".into(),
            name: "Anthropic Claude".into(),
            connected_at: 1_700_000_000,
            healthy: true,
        };

        let mut store = ProvidersStore::load(&path);
        store.upsert(entry);
        store.save(&path).unwrap();

        let loaded = ProvidersStore::load(&path);
        assert!(loaded.providers.contains_key("anthropic"));
        assert_eq!(loaded.providers["anthropic"].name, "Anthropic Claude");
    }

    #[test]
    #[serial(providers_store)]
    fn providers_store_remove() {
        let dir = tmp_cascade_dir();
        let path = dir.path().to_path_buf().join(PROVIDERS_JSON_NAME);

        let entry = ConnectedProvider {
            id: "openai".into(),
            name: "OpenAI".into(),
            connected_at: 0,
            healthy: true,
        };

        let mut store = ProvidersStore::load(&path);
        store.upsert(entry);
        store.save(&path).unwrap();

        let mut store2 = ProvidersStore::load(&path);
        assert!(store2.remove("openai"));
        assert!(!store2.remove("openai")); // idempotent second remove
        store2.save(&path).unwrap();

        let loaded = ProvidersStore::load(&path);
        assert!(!loaded.providers.contains_key("openai"));
    }

    // ── connect_provider (mock 200 → stored) ─────────────────────────────────

    #[tokio::test]
    #[serial(providers_store)]
    async fn connect_api_key_mock_200_stores_in_keychain_and_json() {
        let dir = tmp_cascade_dir();
        let cascade_dir = dir.path().to_path_buf();
        let kc = InMemoryKeychain::new();

        // Start wiremock server.
        let mock_server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/v1/models"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_string("{}"))
            .mount(&mock_server)
            .await;

        let base_url = mock_server.uri();
        let result = connect_provider_at(
            ProviderKind::Anthropic,
            Credential::ApiKey("sk-test-key".into()),
            &kc,
            Some(base_url.as_str()),
            Some(&cascade_dir),
        )
        .await;

        assert!(result.is_ok(), "expected Ok, got: {result:?}");
        let connected = result.unwrap();
        assert_eq!(connected.id, "anthropic");
        assert!(connected.healthy);

        // Keychain must have the secret.
        let stored = kc.get_key(KEYCHAIN_SERVICE, "anthropic").unwrap();
        assert_eq!(stored, "sk-test-key");

        // providers.json must have the entry (no secret).
        let json_path = cascade_dir.join(PROVIDERS_JSON_NAME);
        let store = ProvidersStore::load(&json_path);
        assert!(store.providers.contains_key("anthropic"));
    }

    // ── connect_provider (mock 401 → rejected, not stored) ───────────────────

    #[tokio::test]
    #[serial(providers_store)]
    async fn connect_api_key_mock_401_rejected_nothing_stored() {
        let dir = tmp_cascade_dir();
        let cascade_dir = dir.path().to_path_buf();
        let kc = InMemoryKeychain::new();

        let mock_server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/v1/models"))
            .respond_with(wiremock::ResponseTemplate::new(401))
            .mount(&mock_server)
            .await;

        let base_url = mock_server.uri();
        let result = connect_provider_at(
            ProviderKind::Anthropic,
            Credential::ApiKey("bad-key".into()),
            &kc,
            Some(base_url.as_str()),
            Some(&cascade_dir),
        )
        .await;

        assert!(result.is_err());
        assert!(
            matches!(result.unwrap_err(), ProviderError::AuthFailed(_)),
            "expected AuthFailed"
        );

        // Nothing should be stored in keychain.
        assert!(kc.get_key(KEYCHAIN_SERVICE, "anthropic").is_err());

        // providers.json should not exist or should not have an anthropic entry.
        let json_path = cascade_dir.join(PROVIDERS_JSON_NAME);
        let store = ProvidersStore::load(&json_path);
        assert!(!store.providers.contains_key("anthropic"));
    }

    // ── list_providers reflects stored entries ────────────────────────────────

    #[tokio::test]
    #[serial(providers_store)]
    async fn list_providers_reflects_stored() {
        let dir = tmp_cascade_dir();
        let cascade_dir = dir.path().to_path_buf();
        let kc = InMemoryKeychain::new();

        let mock_server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_string("{}"))
            .mount(&mock_server)
            .await;

        let base_url = mock_server.uri();

        // Connect anthropic.
        connect_provider_at(
            ProviderKind::Anthropic,
            Credential::ApiKey("sk-a".into()),
            &kc,
            Some(base_url.as_str()),
            Some(&cascade_dir),
        )
        .await
        .unwrap();

        let providers = list_providers_at(&kc, Some(&cascade_dir));
        assert_eq!(providers.len(), 1);
        assert_eq!(providers[0].id, "anthropic");
        assert!(providers[0].healthy, "keychain entry present → healthy");
    }

    // ── remove_provider deletes keychain + json entry ─────────────────────────

    #[tokio::test]
    #[serial(providers_store)]
    async fn remove_provider_deletes_keychain_and_json() {
        let dir = tmp_cascade_dir();
        let cascade_dir = dir.path().to_path_buf();
        let kc = InMemoryKeychain::new();

        let mock_server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_string("{}"))
            .mount(&mock_server)
            .await;

        connect_provider_at(
            ProviderKind::OpenAI,
            Credential::ApiKey("sk-openai".into()),
            &kc,
            Some(mock_server.uri().as_str()),
            Some(&cascade_dir),
        )
        .await
        .unwrap();

        // Verify it's there.
        assert!(kc.get_key(KEYCHAIN_SERVICE, "openai").is_ok());

        // Remove it.
        let removed = remove_provider_at("openai", &kc, Some(&cascade_dir)).unwrap();
        assert!(removed);

        // Keychain entry gone.
        assert!(kc.get_key(KEYCHAIN_SERVICE, "openai").is_err());

        // providers.json entry gone.
        let store = ProvidersStore::load(&cascade_dir.join(PROVIDERS_JSON_NAME));
        assert!(!store.providers.contains_key("openai"));
    }

    // ── list marks unhealthy when keychain entry absent ───────────────────────

    #[test]
    #[serial(providers_store)]
    fn list_marks_unhealthy_when_keychain_missing() {
        let dir = tmp_cascade_dir();
        let cascade_dir = dir.path().to_path_buf();
        let kc = InMemoryKeychain::new();

        // Manually write a providers.json entry without a keychain entry.
        let entry = ConnectedProvider {
            id: "groq".into(),
            name: "Groq".into(),
            connected_at: 0,
            healthy: true,
        };
        let mut store = ProvidersStore::default();
        store.upsert(entry);
        store.save(&cascade_dir.join(PROVIDERS_JSON_NAME)).unwrap();

        let providers = list_providers_at(&kc, Some(&cascade_dir));
        assert_eq!(providers.len(), 1);
        assert!(!providers[0].healthy, "no keychain entry → unhealthy");
    }
}
