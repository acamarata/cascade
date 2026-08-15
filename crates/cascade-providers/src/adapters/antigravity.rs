//! AntigravityAdapter — subscription detection + config-generation adapter.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Antigravity, an AI coding tool with a
//! paid subscription to frontier models.
//!
//! ## Design — DETECT + CONFIG mode (ToS-safe)
//!
//! Antigravity manages its own inference pipeline. Routing real completions
//! through Antigravity's paid subscription from a third-party tool would
//! violate Antigravity's ToS (Unauthorized API Access / Subscription Resale).
//!
//! Therefore this adapter operates in **DETECT + CONFIG** mode only:
//!
//! - `health_check` — detects whether Antigravity is installed and authenticated
//!   by reading `~/.config/antigravity/config.json` (or the macOS
//!   `~/Library/Application Support/Antigravity/config.json`).
//! - `provider_info` — returns static metadata so the system knows Antigravity
//!   is a recognised subscription.
//! - `available_models` — returns an empty list: no Antigravity model is
//!   routable through this adapter.
//! - `complete` / `complete_stream` — always returns
//!   `ProviderError::UnsupportedTaskType` explaining that inference routing is
//!   not implemented for this adapter, by design.
//!
//! ## Inference routing — intentionally not implemented
//!
//! This adapter implements no inference routing and exposes no feature flag
//! for it: routing completions through a paid Antigravity subscription from a
//! third-party tool would violate Antigravity's ToS (Unauthorized API Access /
//! Subscription Resale) and is out of scope for this adapter by product design.
//!
//! NOTE: this file is the *subscription detection* adapter only. The real
//! Antigravity-CLI dispatch path (Gemini via the `agy` CLI) lives separately
//! in `cascade-cli`'s conductor (`Provider::Gemini` / `execute_gemini`) — the
//! two must not be conflated.
//!
//! ## Config generation
//!
//! Call `generate_antigravity_cascade_config(workspace, socket)` to write
//! the Antigravity rules/config file that wires Antigravity to Cascade's MCP
//! server via `provide_harness_context`. This is the SAFE, ToS-clean path.
//!
//! # Inputs / Outputs
//!
//! - `health_check`: filesystem read — returns `Ok(())` if installed+authed.
//! - `complete`: always `Err(UnsupportedTaskType)` — inference routing is
//!   not implemented (see above).
//! - `complete_stream`: same.
//! - `available_models`: always an empty list (nothing is routable).
//!
//! # Constraints
//!
//! - Read-only detection. No Antigravity config is written by health_check.
//! - Auth token value is NEVER read, logged, or forwarded.
//! - Config generation writes ONLY to `.antigravity/cascade-rules.md`.

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

const PROVIDER_ID: &str = "antigravity";

/// Error message returned by `complete` / `complete_stream` — this adapter
/// implements no inference routing, by design.
const ROUTING_DISABLED_MSG: &str = "\
Antigravity inference routing is not implemented — out of scope for this \
adapter by design (ToS compliance). Cascade integrates with Antigravity via \
detection and config generation only: run \
`cascade harness generate --harness antigravity` to write \
`.antigravity/cascade-rules.md` which wires Cascade context into Antigravity.";

// ── AntigravityAdapter ────────────────────────────────────────────────────────

/// Provider adapter for Antigravity (detect + config-generation mode only).
///
/// Inference routing is OFF by default. See module-level doc for rationale.
pub struct AntigravityAdapter {
    /// Override the detected config path for testing (None = use real path).
    #[cfg(any(test, feature = "integration_tests"))]
    pub config_path_override: Option<PathBuf>,
}

impl AntigravityAdapter {
    /// Construct a production adapter.
    pub fn new() -> Self {
        Self {
            #[cfg(any(test, feature = "integration_tests"))]
            config_path_override: None,
        }
    }

    /// Construct a test adapter with an injected config path.
    #[cfg(any(test, feature = "integration_tests"))]
    pub fn with_config_path(path: impl Into<PathBuf>) -> Self {
        Self {
            config_path_override: Some(path.into()),
        }
    }

    /// Resolve the Antigravity config.json candidates in priority order.
    fn config_candidates(&self) -> Vec<PathBuf> {
        #[cfg(any(test, feature = "integration_tests"))]
        if let Some(ref p) = self.config_path_override {
            return vec![p.clone()];
        }

        let home = match dirs::home_dir() {
            Some(h) => h,
            None => return vec![],
        };

        // Mutated only under the macOS cfg block below; immutable on other targets.
        #[allow(unused_mut)]
        let mut candidates = vec![
            home.join(".config").join("antigravity").join("config.json"),
            home.join(".antigravity").join("config.json"),
        ];

        #[cfg(target_os = "macos")]
        candidates.push(
            home.join("Library")
                .join("Application Support")
                .join("Antigravity")
                .join("config.json"),
        );

        candidates
    }

    /// Returns `(installed, email_hint)`.
    ///
    /// `installed` = at least one config candidate exists.
    /// `email_hint` = email/user.email from config JSON if present, else empty.
    fn detect(&self) -> (bool, String) {
        for path in self.config_candidates() {
            if !path.exists() {
                continue;
            }
            let contents = match std::fs::read_to_string(&path) {
                Ok(c) => c,
                Err(_) => return (true, String::new()),
            };
            let json: serde_json::Value = match serde_json::from_str(&contents) {
                Ok(j) => j,
                Err(_) => return (true, String::new()), // file exists, malformed
            };
            let email = json
                .get("email")
                .or_else(|| json.get("user").and_then(|u| u.get("email")))
                .or_else(|| json.get("userEmail"))
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string();
            return (true, email);
        }
        (false, String::new())
    }
}

impl Default for AntigravityAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for AntigravityAdapter {
    /// Always returns `UnsupportedTaskType` — inference routing is not
    /// implemented for this adapter (out of scope by design).
    async fn complete(&self, _req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
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

    /// Returns an empty list — no Antigravity model is routable through this
    /// adapter.
    ///
    /// Per the `ProviderAdapter` contract, `available_models` feeds the routing
    /// model-picker; this adapter routes no inference, so it exposes no
    /// routable models.  Detection reads only the config.json auth fields —
    /// no model lineup — from the local Antigravity install.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![])
    }

    /// Detect whether Antigravity is installed and authenticated.
    ///
    /// Returns `Ok(())` when a config.json with an email field is found.
    /// Returns `Err(CredentialNotFound)` when not installed or not authenticated.
    async fn health_check(&self) -> Result<(), ProviderError> {
        let (installed, email) = self.detect();
        if !installed {
            return Err(ProviderError::CredentialNotFound(
                "Antigravity is not installed (config.json not found)".into(),
            ));
        }
        if email.is_empty() {
            return Err(ProviderError::CredentialNotFound(
                "Antigravity is installed but no authenticated session found \
                 (email field absent from config.json)"
                    .into(),
            ));
        }
        Ok(())
    }

    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: PROVIDER_ID.into(),
            name: "Antigravity".into(),
            auth_method: AuthMethod::OAuth2 {
                scopes: vec!["openid".into(), "email".into()],
            },
            base_url: "https://antigravity.sh".into(),
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

/// Write `.antigravity/cascade-rules.md` in `workspace`, wiring Antigravity to
/// the Cascade MCP server via `provide_harness_context`.
///
/// # Purpose
///
/// The SAFE, ToS-clean integration path. The generated rules file tells
/// Antigravity to call `provide_harness_context` from the Cascade MCP server
/// before every response, injecting the merged cascade instruction context.
///
/// # Inputs
/// - `workspace`: directory root to write into (`.antigravity/` is created
///   if it does not exist).
/// - `cascade_socket`: the Cascade daemon socket path.
///
/// # Outputs
/// The absolute path of the written `.antigravity/cascade-rules.md` file.
///
/// # Constraints
/// - Only writes to `.antigravity/cascade-rules.md`.
/// - Idempotent: overwrites on every call (content is deterministic).
pub fn generate_antigravity_cascade_config(
    workspace: &Path,
    cascade_socket: &str,
) -> std::io::Result<PathBuf> {
    let dir = workspace.join(".antigravity");
    std::fs::create_dir_all(&dir)?;
    let out_path = dir.join("cascade-rules.md");

    let content = format!(
        r#"<!-- Generated by `cascade harness generate --harness antigravity`. Do not edit manually. -->
<!-- Re-run the command to update after changing your cascade instruction files. -->

# Cascade Context Rules

This file wires Antigravity to the Cascade AI context framework via MCP.

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
- Run `cascade harness generate --harness antigravity` to regenerate after
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

    fn write_at(path: &Path, content: &str) {
        if let Some(p) = path.parent() {
            std::fs::create_dir_all(p).unwrap();
        }
        let mut f = std::fs::File::create(path).unwrap();
        f.write_all(content.as_bytes()).unwrap();
    }

    // ── health_check ─────────────────────────────────────────────────────────

    #[tokio::test]
    async fn health_check_ok_when_email_present() {
        let tmp = TempDir::new().unwrap();
        let cfg = tmp.path().join("config.json");
        write_at(&cfg, r#"{"email":"user@example.com","token":"tok123"}"#);

        let adapter = AntigravityAdapter::with_config_path(&cfg);
        assert!(adapter.health_check().await.is_ok());
    }

    #[tokio::test]
    async fn health_check_err_no_email() {
        let tmp = TempDir::new().unwrap();
        let cfg = tmp.path().join("config.json");
        write_at(&cfg, r#"{"someOtherKey":"value"}"#);

        let adapter = AntigravityAdapter::with_config_path(&cfg);
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::CredentialNotFound(_)));
    }

    #[tokio::test]
    async fn health_check_err_not_installed() {
        let tmp = TempDir::new().unwrap();
        let missing = tmp.path().join("no_config.json");

        let adapter = AntigravityAdapter::with_config_path(&missing);
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::CredentialNotFound(_)));
    }

    // ── complete — routing disabled ───────────────────────────────────────────

    #[tokio::test]
    async fn complete_returns_routing_disabled() {
        let adapter = AntigravityAdapter::new();
        let req = CompletionRequest::simple("antigravity-model", "hello");
        let err = adapter.complete(req).await.unwrap_err();
        assert!(
            matches!(err, ProviderError::UnsupportedTaskType(_)),
            "expected UnsupportedTaskType, got: {err:?}"
        );
        let msg = err.to_string();
        assert!(
            msg.contains("routing") || msg.contains("ToS") || msg.contains("disabled"),
            "message should mention routing/ToS: {msg}"
        );
    }

    #[tokio::test]
    async fn complete_stream_returns_routing_disabled() {
        let adapter = AntigravityAdapter::new();
        let req = CompletionRequest::simple("antigravity-model", "hello");
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
    async fn available_models_is_empty_no_routable_models() {
        let adapter = AntigravityAdapter::new();
        let models = adapter.available_models().await.unwrap();
        assert!(
            models.is_empty(),
            "detection-only adapter must expose no routable models, got: {models:?}"
        );
    }

    // ── provider_info ─────────────────────────────────────────────────────────

    #[test]
    fn provider_info_id_is_antigravity() {
        let adapter = AntigravityAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "antigravity");
        assert_eq!(info.name, "Antigravity");
    }

    // ── config generation ─────────────────────────────────────────────────────

    #[test]
    fn generate_antigravity_config_writes_file() {
        let ws = TempDir::new().unwrap();
        let out = generate_antigravity_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock")
            .expect("generate failed");

        assert!(out.exists());
        let content = std::fs::read_to_string(&out).unwrap();
        assert!(content.contains("provide_harness_context"));
        assert!(content.contains("cascade.sock"));
    }

    #[test]
    fn generate_antigravity_config_creates_dirs() {
        let ws = TempDir::new().unwrap();
        assert!(!ws.path().join(".antigravity").exists());
        generate_antigravity_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock").unwrap();
        assert!(ws.path().join(".antigravity").exists());
    }

    #[test]
    fn generate_antigravity_config_idempotent() {
        let ws = TempDir::new().unwrap();
        generate_antigravity_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock").unwrap();
        generate_antigravity_cascade_config(ws.path(), "unix://~/.cascade/cascade.sock").unwrap();
        assert!(ws
            .path()
            .join(".antigravity")
            .join("cascade-rules.md")
            .exists());
    }
}
