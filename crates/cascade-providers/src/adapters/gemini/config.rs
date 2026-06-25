//! Gemini adapter configuration — `GeminiConfig` and URL constants.

// ── Constants ─────────────────────────────────────────────────────────────────

/// Direct Gemini API base URL (without trailing slash).
pub(super) const GEMINI_DIRECT_BASE: &str = "https://generativelanguage.googleapis.com";

/// Local pool-proxy URL — the legacy cascade-gemini-proxy daemon.
pub(super) const GEMINI_PROXY_BASE: &str = "http://127.0.0.1:3761";

/// Default model when none is specified in `GeminiConfig`.
pub(super) const DEFAULT_MODEL: &str = "gemini-2.0-flash";

// ── GeminiConfig ──────────────────────────────────────────────────────────────

/// Configuration for `GeminiAdapter`.
///
/// ## GFP/GPP toggle
///
/// When `use_gfp_proxy` is `true` the adapter targets the local Gemini Fleet
/// Proxy (`http://127.0.0.1:3761`) which handles key rotation itself.  No API
/// key is sent by the adapter in that mode.  When `false` (the default), a
/// valid `api_key` must be supplied and is passed as `?key=` on every request.
#[derive(Debug, Clone)]
pub struct GeminiConfig {
    /// Resolved base URL.  Overridden to `GEMINI_PROXY_BASE` when
    /// `use_gfp_proxy` is `true`.
    pub base_url: String,

    /// Route through the local GFP pool-proxy daemon instead of calling
    /// Google directly.  When `true`, `api_key` is ignored.
    pub use_gfp_proxy: bool,

    /// Google API key.  Required when `use_gfp_proxy = false`.
    /// Must come from the cascade-keychain service `"cascade.gemini"`.
    pub api_key: Option<String>,

    /// Gemini model identifier.  Defaults to `gemini-2.0-flash`.
    pub model: String,
}

impl GeminiConfig {
    /// Build a direct-mode config with the provided API key and default model.
    pub fn direct(api_key: impl Into<String>) -> Self {
        Self {
            base_url: GEMINI_DIRECT_BASE.to_string(),
            use_gfp_proxy: false,
            api_key: Some(api_key.into()),
            model: DEFAULT_MODEL.to_string(),
        }
    }

    /// Build a pool-proxy config.  No API key is sent.
    pub fn proxy() -> Self {
        Self {
            base_url: GEMINI_PROXY_BASE.to_string(),
            use_gfp_proxy: true,
            api_key: None,
            model: DEFAULT_MODEL.to_string(),
        }
    }

    /// Override the model identifier on an existing config.
    pub fn with_model(mut self, model: impl Into<String>) -> Self {
        self.model = model.into();
        self
    }
}

impl Default for GeminiConfig {
    fn default() -> Self {
        Self {
            base_url: GEMINI_DIRECT_BASE.to_string(),
            use_gfp_proxy: false,
            api_key: None,
            model: DEFAULT_MODEL.to_string(),
        }
    }
}
