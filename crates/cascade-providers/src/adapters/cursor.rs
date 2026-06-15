//! CursorAdapter — subscription detection + config-generation adapter.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Cursor (cursor.sh), a paid AI coding
//! editor with a built-in subscription to frontier models (Claude, GPT-4o, etc.).
//!
//! ## Design — DETECT + CONFIG mode (ToS-safe)
//!
//! Cursor manages its own inference pipeline. Routing real completions through
//! Cursor's paid subscription from a third-party tool would violate Cursor's
//! ToS (§ API Resale / Unauthorized Access).
//!
//! Therefore this adapter operates in **DETECT + CONFIG** mode only:
//!
//! - `health_check` — detects whether Cursor is installed and authenticated
//!   by reading `~/Library/Application Support/Cursor/User/globalStorage/storage.json`
//!   (macOS) and checking for `cursorAuth.cachedEmail`.
//! - `provider_info` — returns static metadata so the system knows Cursor is
//!   a recognised subscription.
//! - `available_models` — returns the well-known Cursor model identifiers
//!   (informational; not routable).
//! - `complete` / `complete_stream` — always returns
//!   `ProviderError::UnsupportedTaskType` explaining that inference routing is
//!   disabled by default to protect ToS compliance.
//!
//! ## Inference routing stub (OFF by default)
//!
//! Real inference routing (if Cursor ever exposes a public API) is gated behind
//! `#[cfg(feature = "inference_routing")]`.  This feature is NOT enabled in the
//! default build — it exists only as documentation of how routing would work.
//! Never enable this feature without explicit legal review of Cursor's ToS.
//!
//! ## Config generation
//!
//! Call `generate_cursor_cascade_config(workspace)` to write
//! `.cursor/rules/cascade.mdc` (a Cursor MDC rules file) that wires Cursor to
//! Cascade's MCP server via `provide_harness_context` and injects the merged
//! cascade instruction file. This is the SAFE, ToS-clean integration path.
//!
//! # Inputs / Outputs
//!
//! - `health_check`: filesystem read — returns `Ok(())` if Cursor is detected
//!   with an authenticated session, else `Err(CredentialNotFound)`.
//! - `complete`: always `Err(UnsupportedTaskType)` unless `inference_routing`
//!   feature is enabled (stub only, see above).
//! - `complete_stream`: same.
//! - `available_models`: static list of Cursor subscription models.
//!
//! # Constraints
//!
//! - Read-only detection. No Cursor config is written by health_check.
//! - Auth token value is NEVER read, logged, or forwarded.
//! - Config generation writes ONLY to `.cursor/rules/cascade.mdc` — never to
//!   any Cursor internal config.

use std::path::{Path, PathBuf};
use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;

use crate::{
    adapter::ProviderAdapter,
    error::ProviderError,
    provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo},
    types::{CompletionRequest, CompletionResponse, ModelInfo, StreamChunk},
};

// ── Constants ─────────────────────────────────────────────────────────────────

const PROVIDER_ID: &str = "cursor";

/// Error message returned by `complete` / `complete_stream` when inference
/// routing is disabled (default).
const ROUTING_DISABLED_MSG: &str = "\
Cursor inference routing is disabled by default (ToS compliance). \
Cascade integrates with Cursor via config generation only: \
run `cascade harness generate --harness cursor` to write \
`.cursor/rules/cascade.mdc` which wires Cascade context into Cursor. \
Direct inference routing would require Cursor to expose a public API and \
explicit ToS clearance — see cfg(feature = \"inference_routing\") stub.";

// ── CursorAdapter ─────────────────────────────────────────────────────────────

/// Provider adapter for Cursor (detect + config-generation mode only).
///
/// Inference routing is OFF by default. See module-level doc for rationale.
pub struct CursorAdapter {
    /// Override the detected auth path for testing (None = use real path).
    #[cfg(any(test, feature = "integration_tests"))]
    pub auth_path_override: Option<PathBuf>,
}

impl CursorAdapter {
    /// Construct a production adapter.
    pub fn new() -> Self {
        Self {
            #[cfg(any(test, feature = "integration_tests"))]
            auth_path_override: None,
        }
    }

    /// Construct a test adapter with an injected auth-file path.
    #[cfg(any(test, feature = "integration_tests"))]
    pub fn with_auth_path(path: impl Into<PathBuf>) -> Self {
        Self {
            auth_path_override: Some(path.into()),
        }
    }

    /// Resolve the Cursor storage.json path.
    ///
    /// Returns a list of candidate paths in priority order.
    fn storage_candidates(&self) -> Vec<PathBuf> {
        #[cfg(any(test, feature = "integration_tests"))]
        if let Some(ref p) = self.auth_path_override {
            return vec![p.clone()];
        }

        let home = match dirs::home_dir() {
            Some(h) => h,
            None => return vec![],
        };

        // Linux / cross-platform path. Mutated only under the macOS/Windows cfg
        // blocks below; immutable on other targets.
        #[allow(unused_mut)]
        let mut candidates = vec![home
            .join(".config")
            .join("Cursor")
            .join("User")
            .join("globalStorage")
            .join("storage.json")];

        // macOS primary path (inserted at front so it takes priority)
        #[cfg(target_os = "macos")]
        candidates.insert(
            0,
            home.join("Library")
                .join("Application Support")
                .join("Cursor")
                .join("User")
                .join("globalStorage")
                .join("storage.json"),
        );

        candidates
    }

    /// Returns `(installed, email_hint)` by reading Cursor storage.json.
    ///
    /// `installed` = the storage.json file exists.
    /// `email_hint` = `cursorAuth.cachedEmail` value if present, else empty.
    fn detect(&self) -> (bool, String) {
        for path in self.storage_candidates() {
            if !path.exists() {
                continue;
            }
            let contents = match std::fs::read_to_string(&path) {
                Ok(c) => c,
                Err(_) => continue,
            };
            let json: serde_json::Value = match serde_json::from_str(&contents) {
                Ok(j) => j,
                Err(_) => return (true, String::new()), // file exists but malformed
            };
            let email = json
                .get("cursorAuth.cachedEmail")
                .or_else(|| json.get("cursorAuth").and_then(|ca| ca.get("cachedEmail")))
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string();
            return (true, email);
        }
        (false, String::new())
    }
}

impl Default for CursorAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for CursorAdapter {
    /// Always returns `UnsupportedTaskType` — inference routing is disabled.
    ///
    /// The error message explains the ToS rationale and the correct integration
    /// path (`cascade harness generate --harness cursor`).
    async fn complete(&self, _req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        #[cfg(feature = "inference_routing")]
        {
            // STUB: inference routing would POST to Cursor's internal API here.
            // This feature is intentionally undocumented and not enabled in any
            // release build. Enabling it without Cursor's explicit API authorisation
            // constitutes a ToS violation (§ Unauthorized API Access).
            return Err(ProviderError::UnsupportedTaskType(
                "inference_routing feature is a stub — not implemented".into(),
            ));
        }
        #[cfg(not(feature = "inference_routing"))]
        Err(ProviderError::UnsupportedTaskType(
            ROUTING_DISABLED_MSG.into(),
        ))
    }

    /// Always returns `UnsupportedTaskType` — streaming inference routing is disabled.
    async fn complete_stream(
        &self,
        _req: CompletionRequest,
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        Err(ProviderError::UnsupportedTaskType(
            ROUTING_DISABLED_MSG.into(),
        ))
    }

    /// Return the known Cursor subscription model identifiers (informational).
    ///
    /// These model IDs are what Cursor exposes in its UI — not routable through
    /// this adapter.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "claude-sonnet-4-5".into(),
                name: "Claude Sonnet 4.5 (via Cursor)".into(),
                context_window: 200_000,
                supports_streaming: false,
            },
            ModelInfo {
                id: "gpt-4o".into(),
                name: "GPT-4o (via Cursor)".into(),
                context_window: 128_000,
                supports_streaming: false,
            },
            ModelInfo {
                id: "gemini-2.5-pro".into(),
                name: "Gemini 2.5 Pro (via Cursor)".into(),
                context_window: 1_000_000,
                supports_streaming: false,
            },
        ])
    }

    /// Detect whether Cursor is installed and has an authenticated session.
    ///
    /// Returns `Ok(())` when `cursorAuth.cachedEmail` is present in storage.json,
    /// indicating an active Cursor subscription session.
    ///
    /// Returns `Err(CredentialNotFound)` when:
    /// - Cursor storage.json does not exist (not installed).
    /// - storage.json exists but `cursorAuth.cachedEmail` is absent or empty
    ///   (installed but not authenticated / trial expired).
    async fn health_check(&self) -> Result<(), ProviderError> {
        let (installed, email) = self.detect();
        if !installed {
            return Err(ProviderError::CredentialNotFound(
                "Cursor is not installed (storage.json not found)".into(),
            ));
        }
        if email.is_empty() {
            return Err(ProviderError::CredentialNotFound(
                "Cursor is installed but no authenticated session found \
                 (cursorAuth.cachedEmail absent)"
                    .into(),
            ));
        }
        Ok(())
    }

    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: PROVIDER_ID.into(),
            name: "Cursor".into(),
            auth_method: AuthMethod::OAuth2 {
                scopes: vec!["openid".into(), "email".into()],
            },
            base_url: "https://cursor.sh".into(),
            capabilities: ProviderCapabilities {
                supports_streaming: false,
                supports_vision: false,
                max_context_tokens: 200_000,
                supports_function_calling: false,
            },
        }
    }
}

// ── Config generation ─────────────────────────────────────────────────────────

/// Write `.cursor/rules/cascade.mdc` in `workspace`, wiring Cursor to the
/// Cascade MCP server via `provide_harness_context`.
///
/// # Purpose
///
/// This is the SAFE, ToS-clean integration path. The generated MDC rules file
/// tells Cursor to call `provide_harness_context` from the Cascade MCP server
/// before every response, injecting the merged cascade instruction context.
/// No inference is re-routed through Cursor's paid subscription.
///
/// # Inputs
/// - `workspace`: directory root to write into (`.cursor/rules/` is created
///   if it does not exist).
/// - `cascade_socket`: the Cascade daemon socket path (e.g.
///   `unix://~/.cascade/cascade.sock`).
///
/// # Outputs
/// The absolute path of the written `.cursor/rules/cascade.mdc` file.
///
/// # Constraints
/// - Only writes to `.cursor/rules/cascade.mdc` — never to Cursor internals.
/// - Idempotent: overwrites on every call (content is deterministic).
pub fn generate_cursor_cascade_config(
    workspace: &Path,
    cascade_socket: &str,
) -> std::io::Result<PathBuf> {
    let rules_dir = workspace.join(".cursor").join("rules");
    std::fs::create_dir_all(&rules_dir)?;
    let out_path = rules_dir.join("cascade.mdc");

    let content = format!(
        r#"---
description: Cascade AI context framework — MCP integration
globs:
alwaysApply: true
---

<!-- Generated by `cascade harness generate --harness cursor`. Do not edit manually. -->
<!-- Re-run the command to update after changing your cascade instruction files. -->

# Cascade Context

This rules file wires Cursor to the Cascade AI context framework via MCP.

## MCP Server

Cascade exposes a `provide_harness_context` tool via its MCP server at:

```
{cascade_socket}
```

Before generating any response, call `provide_harness_context` to retrieve
the merged cascade instruction context for the current workspace. The tool
returns a `context_md` string — prepend it to your system context.

## Integration Notes

- Cascade context is merged from `.cascade/`, `.claude/`, and project
  instruction files in priority order.
- This file was generated from your cascade — do NOT edit it manually.
- Run `cascade harness generate --harness cursor` to regenerate after
  updating your cascade instruction files.
"#,
        cascade_socket = cascade_socket,
    );

    std::fs::write(&out_path, content)?;
    Ok(out_path)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::TempDir;

    // Helper: write a file at a path (creating parents).
    fn write_at(path: &Path, content: &str) {
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p).unwrap();
        }
        let mut f = std::fs::File::create(path).unwrap();
        f.write_all(content.as_bytes()).unwrap();
    }

    // ── health_check ─────────────────────────────────────────────────────────

    #[tokio::test]
    async fn health_check_returns_ok_when_email_present() {
        let tmp = TempDir::new().unwrap();
        let storage = tmp.path().join("storage.json");
        write_at(
            &storage,
            r#"{"cursorAuth.cachedEmail":"user@example.com","cursorAuth.accessToken":"tok"}"#,
        );

        let adapter = CursorAdapter::with_auth_path(&storage);
        assert!(adapter.health_check().await.is_ok());
    }

    #[tokio::test]
    async fn health_check_returns_err_when_no_email() {
        let tmp = TempDir::new().unwrap();
        let storage = tmp.path().join("storage.json");
        write_at(&storage, r#"{"someOtherKey":"value"}"#);

        let adapter = CursorAdapter::with_auth_path(&storage);
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::CredentialNotFound(_)));
    }

    #[tokio::test]
    async fn health_check_returns_err_when_not_installed() {
        let tmp = TempDir::new().unwrap();
        let missing = tmp.path().join("no_such_file.json");

        let adapter = CursorAdapter::with_auth_path(&missing);
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::CredentialNotFound(_)));
    }

    // ── complete — routing disabled ───────────────────────────────────────────

    #[tokio::test]
    async fn complete_returns_routing_disabled() {
        let adapter = CursorAdapter::new();
        let req = CompletionRequest::simple("cursor-model", "hello");
        let err = adapter.complete(req).await.unwrap_err();
        assert!(
            matches!(err, ProviderError::UnsupportedTaskType(_)),
            "expected UnsupportedTaskType, got: {err:?}"
        );
        let msg = err.to_string();
        assert!(
            msg.contains("routing") || msg.contains("ToS") || msg.contains("disabled"),
            "error message should mention routing/ToS: {msg}"
        );
    }

    #[tokio::test]
    async fn complete_stream_returns_routing_disabled() {
        let adapter = CursorAdapter::new();
        let req = CompletionRequest::simple("cursor-model", "hello");
        let result = adapter.complete_stream(req).await;
        assert!(result.is_err(), "expected Err for routing-disabled stream");
        if let Err(e) = result {
            assert!(
                matches!(e, ProviderError::UnsupportedTaskType(_)),
                "expected UnsupportedTaskType, got: {e:?}"
            );
        }
    }

    // ── available_models ─────────────────────────────────────────────────────

    #[tokio::test]
    async fn available_models_returns_non_empty() {
        let adapter = CursorAdapter::new();
        let models = adapter.available_models().await.unwrap();
        assert!(!models.is_empty());
        // All model ids should be non-empty strings
        for m in &models {
            assert!(!m.id.is_empty());
        }
    }

    // ── provider_info ─────────────────────────────────────────────────────────

    #[test]
    fn provider_info_id_is_cursor() {
        let adapter = CursorAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "cursor");
        assert_eq!(info.name, "Cursor");
    }

    // ── config generation ─────────────────────────────────────────────────────

    #[test]
    fn generate_cursor_config_writes_mdc_file() {
        let ws = TempDir::new().unwrap();
        let out = generate_cursor_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock")
            .expect("generate failed");

        assert!(out.exists(), "mdc file must be written");
        let content = std::fs::read_to_string(&out).unwrap();
        assert!(
            content.contains("provide_harness_context"),
            "must reference MCP tool"
        );
        assert!(
            content.contains("cascade.sock"),
            "must include the socket path"
        );
        assert!(
            content.contains("alwaysApply: true"),
            "must set alwaysApply"
        );
    }

    #[test]
    fn generate_cursor_config_creates_dirs() {
        let ws = TempDir::new().unwrap();
        // .cursor/rules/ does not exist yet
        assert!(!ws.path().join(".cursor").exists());
        generate_cursor_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock").unwrap();
        assert!(ws.path().join(".cursor").join("rules").exists());
    }

    #[test]
    fn generate_cursor_config_is_idempotent() {
        let ws = TempDir::new().unwrap();
        generate_cursor_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock").unwrap();
        // Second call overwrites deterministically — must not error
        generate_cursor_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock").unwrap();
        let p = ws.path().join(".cursor").join("rules").join("cascade.mdc");
        assert!(p.exists());
    }
}
