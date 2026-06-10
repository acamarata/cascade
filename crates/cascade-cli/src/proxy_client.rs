//! Proxy client utilities for cascade-cli commands that send requests to the
//! local Gemini proxy on `localhost:3761`.
//!
//! Purpose: read the per-daemon-session token from `~/.cascade/proxy.token` and
//! inject it as the `X-Cascade-Token` header so the proxy middleware accepts the
//! request. All CLI commands that invoke the proxy (e.g. `cascade search`,
//! `cascade resolve`) must call [`read_proxy_token`] and inject the header.
//!
//! Inputs:  `~/.cascade/proxy.token` — written by the daemon on startup
//!          (mode 0o600, hex-encoded 64-char string).
//! Outputs: the token string, or a user-friendly `ProxyClientError` variant.
//!
//! Constraints:
//!   - The token file is never written by the CLI — it is daemon-owned.
//!   - The token is never logged at any log level.
//!   - Token is trimmed before use (the file may contain a trailing newline).
//!
//! SPORT: `.claude/docs/MASTER-CLI.md` — proxy_client module (T-P2-E07-01)

use std::path::PathBuf;

use cascade_types::paths::global_cascade_dir;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced when reading or using the proxy token.
#[derive(Debug)]
pub enum ProxyClientError {
    /// The daemon has not written a token file yet (daemon not running or very
    /// early in startup). User should run `cascade daemon start`.
    TokenNotFound { path: PathBuf },
    /// The token file exists but could not be read.
    TokenReadError { path: PathBuf, detail: String },
    /// The token file is empty or contains only whitespace — corrupt write.
    TokenEmpty { path: PathBuf },
}

impl std::fmt::Display for ProxyClientError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::TokenNotFound { path } => write!(
                f,
                "proxy token not found at {} — is the cascade daemon running? \
                 Start with: cascade daemon start",
                path.display()
            ),
            Self::TokenReadError { path, detail } => write!(
                f,
                "failed to read proxy token from {}: {detail}",
                path.display()
            ),
            Self::TokenEmpty { path } => write!(
                f,
                "proxy token at {} is empty — daemon may be starting up",
                path.display()
            ),
        }
    }
}

impl std::error::Error for ProxyClientError {}

// ── Token reader ──────────────────────────────────────────────────────────────

/// Read the proxy token from `~/.cascade/proxy.token`.
///
/// Purpose: provide the `X-Cascade-Token` header value for any CLI command
/// that needs to send a request to the local Gemini proxy.
///
/// Inputs:  none (path derived from `global_cascade_dir()`).
/// Outputs: the trimmed 64-char hex token string on success.
/// Constraints:
///   - Never logs the token.
///   - Returns `TokenNotFound` when the file is absent (daemon not started).
///   - Returns `TokenEmpty` when the file is empty (daemon starting up).
pub fn read_proxy_token() -> Result<String, ProxyClientError> {
    read_proxy_token_from(global_cascade_dir().join("proxy.token"))
}

/// Read the proxy token from an explicit path (used by tests with tempdir).
///
/// Inputs:  `token_path` — absolute path to the token file.
/// Outputs: trimmed 64-char hex token string, or `ProxyClientError`.
pub fn read_proxy_token_from(token_path: PathBuf) -> Result<String, ProxyClientError> {
    if !token_path.exists() {
        return Err(ProxyClientError::TokenNotFound { path: token_path });
    }

    let raw =
        std::fs::read_to_string(&token_path).map_err(|e| ProxyClientError::TokenReadError {
            path: token_path.clone(),
            detail: e.to_string(),
        })?;

    let token = raw.trim().to_owned();
    if token.is_empty() {
        return Err(ProxyClientError::TokenEmpty { path: token_path });
    }

    Ok(token)
}

/// Return the `X-Cascade-Token` header name (canonical form).
///
/// Callers should use this constant rather than a bare string to prevent
/// typos and ensure the header name matches what the daemon middleware expects.
pub const TOKEN_HEADER: &str = "X-Cascade-Token";

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    /// read_proxy_token_from returns Ok(token) when the file exists and is non-empty.
    #[test]
    fn test_read_proxy_token_returns_token() {
        let tmp = TempDir::new().unwrap();
        let token = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899";
        let path = tmp.path().join("proxy.token");
        std::fs::write(&path, format!("{token}\n")).unwrap(); // file with trailing newline

        let result = read_proxy_token_from(path);
        assert!(result.is_ok(), "expected Ok, got: {:?}", result.err());
        assert_eq!(result.unwrap(), token, "trailing newline should be trimmed");
    }

    /// read_proxy_token_from returns TokenNotFound when file is absent.
    #[test]
    fn test_read_proxy_token_not_found() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("proxy.token");
        let result = read_proxy_token_from(path);
        match result {
            Err(ProxyClientError::TokenNotFound { .. }) => {}
            other => panic!("expected TokenNotFound, got: {:?}", other),
        }
    }

    /// read_proxy_token_from returns TokenEmpty when file is empty.
    #[test]
    fn test_read_proxy_token_empty_file() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("proxy.token");
        std::fs::write(&path, "").unwrap();
        let result = read_proxy_token_from(path);
        match result {
            Err(ProxyClientError::TokenEmpty { .. }) => {}
            other => panic!("expected TokenEmpty, got: {:?}", other),
        }
    }

    /// TOKEN_HEADER constant matches the daemon middleware expectation.
    #[test]
    fn test_token_header_constant() {
        assert_eq!(TOKEN_HEADER, "X-Cascade-Token");
    }
}
