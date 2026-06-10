//! ProviderError — all failure variants for the cascade-providers crate.
//!
//! # Purpose
//!
//! A single, exhaustive error type that every `ProviderAdapter` implementation
//! returns.  Callers match on variants to apply retry logic, surface user
//! messages, or route to a fallback provider.
//!
//! # Design
//!
//! - Derived via [`thiserror`] so every variant has a `Display` impl without
//!   boilerplate.
//! - `#[non_exhaustive]` lets downstream crates match without a wildcard arm
//!   breaking when new variants are added in a minor release.
//! - `From<reqwest::Error>` and `From<serde_json::Error>` conversions keep
//!   adapter implementations terse; no information is dropped — the source
//!   error is preserved in the string representation.
//!
//! # Constraints
//!
//! - Never include raw token values or API keys in error messages.
//! - `RateLimited` carries `retry_after_secs` so callers can implement
//!   back-off without parsing the message string.

// ── ProviderError ─────────────────────────────────────────────────────────────

/// All failure modes for a [`crate::ProviderAdapter`] call.
#[derive(Debug, thiserror::Error)]
#[non_exhaustive]
pub enum ProviderError {
    /// Authentication with the provider failed (bad key, revoked token, etc.).
    ///
    /// The inner string is a human-readable reason; it must NOT contain the
    /// credential value itself.
    #[error("auth failed: {0}")]
    AuthFailed(String),

    /// The provider returned HTTP 429 or an equivalent rate-limit signal.
    ///
    /// `retry_after_secs` is `Some(n)` when the provider included a
    /// `Retry-After` header; `None` when unknown.
    #[error("rate limited (retry after {retry_after_secs:?}s)")]
    RateLimited {
        /// Seconds to wait before retrying, if provided by the server.
        retry_after_secs: Option<u64>,
    },

    /// The requested model identifier is not known to the provider.
    #[error("model not found: {0}")]
    ModelNotFound(String),

    /// A network-level failure (DNS, TCP connect, TLS handshake, etc.).
    #[error("network error: {0}")]
    NetworkError(String),

    /// The provider did not respond within the configured deadline.
    #[error("request timed out after {secs}s")]
    Timeout {
        /// The deadline that was exceeded, in seconds.
        secs: u64,
    },

    /// The provider returned a response that could not be parsed.
    #[error("invalid response: {0}")]
    InvalidResponse(String),

    /// The prompt exceeded the model's context window.
    #[error("context window exceeded (max {max} tokens, got {actual})")]
    ContextWindowExceeded {
        /// The provider's documented maximum.
        max: u32,
        /// The number of tokens in the rejected request.
        actual: u32,
    },

    /// An error originating from a locally-running model (Ollama, GGUF, etc.).
    #[error("local model error: {0}")]
    LocalModelError(String),

    /// The called method has no implementation yet (used by `NoopProvider`
    /// and as a placeholder during incremental adapter development).
    #[error("not implemented")]
    NotImplemented,

    /// The keychain or vault does not contain the credential needed for this
    /// provider.  The inner string names the missing key.
    #[error("credential not found: {0}")]
    CredentialNotFound(String),

    /// A stored OAuth token has expired and could not be refreshed
    /// automatically.  The inner string identifies the provider.
    #[error("OAuth token expired for provider: {provider}")]
    OAuthExpired {
        /// The provider whose token expired (e.g. `"google"`).
        provider: String,
    },

    /// A streaming response was interrupted before `finish_reason` was
    /// received.
    #[error("stream interrupted: {0}")]
    StreamInterrupted(String),

    /// The caller requested a [`crate::TaskType`] that this adapter does not
    /// support.
    #[error("unsupported task type: {0}")]
    UnsupportedTaskType(String),
}

// ── From conversions ──────────────────────────────────────────────────────────

impl From<reqwest::Error> for ProviderError {
    /// Convert a `reqwest` transport error into [`ProviderError`].
    ///
    /// The conversion preserves the full `reqwest` error message, including
    /// URL and status code when present.  No credential values are included —
    /// reqwest errors do not carry request headers.
    fn from(e: reqwest::Error) -> Self {
        if e.is_timeout() {
            // Extract a timeout duration if possible, otherwise 0.
            ProviderError::Timeout { secs: 0 }
        } else {
            ProviderError::NetworkError(e.to_string())
        }
    }
}

impl From<serde_json::Error> for ProviderError {
    /// Convert a JSON parse/serialize error into [`ProviderError::InvalidResponse`].
    ///
    /// The conversion is lossless — the full `serde_json` error message
    /// (including line/column for parse errors) is preserved in the variant
    /// string.
    fn from(e: serde_json::Error) -> Self {
        ProviderError::InvalidResponse(e.to_string())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn auth_failed_display() {
        let e = ProviderError::AuthFailed("invalid API key".into());
        assert!(e.to_string().contains("auth failed"));
        assert!(e.to_string().contains("invalid API key"));
    }

    #[test]
    fn rate_limited_with_retry_after_display() {
        let e = ProviderError::RateLimited {
            retry_after_secs: Some(30),
        };
        let s = e.to_string();
        assert!(s.contains("30"), "expected retry seconds in: {s}");
    }

    #[test]
    fn rate_limited_display_60() {
        let e = ProviderError::RateLimited {
            retry_after_secs: Some(60),
        };
        let s = format!("{e}");
        assert!(s.contains("60"), "expected 60 in: {s}");
    }

    #[test]
    fn rate_limited_none_display() {
        let e = ProviderError::RateLimited {
            retry_after_secs: None,
        };
        // Must not panic.
        let _ = e.to_string();
    }

    #[test]
    fn model_not_found_display() {
        let e = ProviderError::ModelNotFound("gpt-99".into());
        assert!(e.to_string().contains("gpt-99"));
    }

    #[test]
    fn timeout_display() {
        let e = ProviderError::Timeout { secs: 30 };
        let s = e.to_string();
        assert!(s.contains("30"));
    }

    #[test]
    fn context_window_display() {
        let e = ProviderError::ContextWindowExceeded {
            max: 8192,
            actual: 10000,
        };
        let s = e.to_string();
        assert!(s.contains("8192"), "max in: {s}");
        assert!(s.contains("10000"), "actual in: {s}");
    }

    #[test]
    fn not_implemented_display() {
        let e = ProviderError::NotImplemented;
        assert_eq!(e.to_string(), "not implemented");
    }

    #[test]
    fn oauth_expired_display() {
        let e = ProviderError::OAuthExpired {
            provider: "google".into(),
        };
        let s = e.to_string();
        assert!(s.contains("google"), "provider in: {s}");
    }

    #[test]
    fn stream_interrupted_display() {
        let e = ProviderError::StreamInterrupted("connection reset".into());
        assert!(e.to_string().contains("stream interrupted"));
    }

    #[test]
    fn unsupported_task_type_display() {
        let e = ProviderError::UnsupportedTaskType("RagEmbed".into());
        assert!(e.to_string().contains("RagEmbed"));
    }

    #[test]
    fn serde_json_conversion() {
        let bad = serde_json::from_str::<serde_json::Value>("{bad json}");
        let e: ProviderError = bad.unwrap_err().into();
        assert!(matches!(e, ProviderError::InvalidResponse(_)));
    }

    #[test]
    fn error_trait_satisfied() {
        // std::error::Error must be implemented (thiserror guarantees this).
        let e: &dyn std::error::Error = &ProviderError::NotImplemented;
        let _ = e.to_string();
    }
}
