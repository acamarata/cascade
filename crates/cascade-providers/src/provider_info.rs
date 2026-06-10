//! ProviderInfo, ProviderCapabilities, AuthMethod, and TaskType.
//!
//! # Purpose
//!
//! Describes what a provider _is_ and what it _can do_.  The routing table
//! (T-P3-E04-06) uses `TaskType` as a key to select the right adapter at
//! runtime.  `ProviderInfo` is returned by `ProviderAdapter::provider_info()`
//! so callers can interrogate a provider without inspecting its concrete type.
//!
//! # Inputs / Outputs
//!
//! All types derive `Serialize + Deserialize`.  `TaskType` also derives
//! `Hash + Eq` so it can be used as a `HashMap` key.
//!
//! # Constraints
//!
//! - `AuthMethod::OAuth2` carries `scopes` (not just a flag) because
//!   different workflows require different Google/GitHub OAuth scopes.
//! - `TaskType::LocalFallback` covers offline scenarios where no cloud
//!   provider is reachable.

use serde::{Deserialize, Serialize};

// ── TaskType ──────────────────────────────────────────────────────────────────

/// The class of work a provider is being asked to perform.
///
/// The routing table maps each `TaskType` to the best available provider.
/// Adapters that cannot handle a given task return
/// [`crate::ProviderError::UnsupportedTaskType`].
///
/// `Hash + Eq` are derived so `TaskType` can be used as a `HashMap` key.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TaskType {
    /// Merge and resolve a tiered instruction cascade (the core Cascade
    /// operation — context merging, deduplication, priority ordering).
    CascadeMerge,

    /// Generate dense vector embeddings for RAG ingestion.
    RagEmbed,

    /// Re-rank retrieved RAG candidates by relevance.
    RagRerank,

    /// General-purpose conversational completion.
    Chat,

    /// Code completion or generation (fills/suggestions/refactors).
    CodeCompletion,

    /// Use a locally-running model as a fallback when all cloud providers
    /// are unreachable.
    LocalFallback,
}

// ── AuthMethod ────────────────────────────────────────────────────────────────

/// How a provider authenticates API calls.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthMethod {
    /// A static API key passed in a request header.
    ApiKey,

    /// OAuth 2.0 PKCE flow.  `scopes` lists the OAuth scopes required by
    /// this provider (e.g. `["https://www.googleapis.com/auth/cloud-platform"]`).
    OAuth2 {
        /// OAuth scopes required for this provider.
        scopes: Vec<String>,
    },

    /// No authentication required (local models, open endpoints).
    None,
}

// ── ProviderCapabilities ──────────────────────────────────────────────────────

/// What a specific model/provider combination can do.
///
/// Populated by each adapter in its `provider_info()` implementation.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ProviderCapabilities {
    /// Whether the provider supports token-by-token streaming via
    /// `complete_stream`.
    pub supports_streaming: bool,

    /// Whether the provider accepts image content in messages (multimodal
    /// vision input).
    pub supports_vision: bool,

    /// Maximum combined token count (prompt + completion) for a single call.
    pub max_context_tokens: u32,

    /// Whether the provider supports structured function/tool calling.
    pub supports_function_calling: bool,
}

// ── ProviderInfo ──────────────────────────────────────────────────────────────

/// Static metadata about a provider.
///
/// Returned by `ProviderAdapter::provider_info()`.  Callers use this to
/// display provider information in the UI, select an appropriate provider for
/// a given `TaskType`, and determine what authentication flow to initiate.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ProviderInfo {
    /// Stable machine-readable identifier (e.g. `"anthropic"`, `"openai"`,
    /// `"google-gemini"`).
    pub id: String,

    /// Human-readable display name (e.g. `"Anthropic Claude"`).
    pub name: String,

    /// How this provider authenticates requests.
    pub auth_method: AuthMethod,

    /// The base URL for API requests (e.g. `"https://api.anthropic.com"`).
    pub base_url: String,

    /// Runtime capabilities of the provider's selected model.
    pub capabilities: ProviderCapabilities,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn round_trip<T: serde::Serialize + for<'de> serde::Deserialize<'de> + PartialEq + std::fmt::Debug>(
        v: &T,
    ) {
        let json = serde_json::to_string(v).expect("serialize");
        let back: T = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(v, &back);
    }

    #[test]
    fn task_type_round_trip() {
        for variant in [
            TaskType::CascadeMerge,
            TaskType::RagEmbed,
            TaskType::RagRerank,
            TaskType::Chat,
            TaskType::CodeCompletion,
            TaskType::LocalFallback,
        ] {
            round_trip(&variant);
        }
    }

    #[test]
    fn task_type_as_hashmap_key() {
        let mut map: HashMap<TaskType, &str> = HashMap::new();
        map.insert(TaskType::Chat, "chat-provider");
        map.insert(TaskType::CodeCompletion, "code-provider");
        map.insert(TaskType::LocalFallback, "local-ollama");

        assert_eq!(map[&TaskType::Chat], "chat-provider");
        assert_eq!(map[&TaskType::LocalFallback], "local-ollama");
    }

    #[test]
    fn auth_method_api_key_round_trip() {
        round_trip(&AuthMethod::ApiKey);
    }

    #[test]
    fn auth_method_oauth2_carries_scopes() {
        let method = AuthMethod::OAuth2 {
            scopes: vec![
                "https://www.googleapis.com/auth/cloud-platform".into(),
                "openid".into(),
            ],
        };
        let json = serde_json::to_string(&method).unwrap();
        let back: AuthMethod = serde_json::from_str(&json).unwrap();
        assert_eq!(method, back);

        if let AuthMethod::OAuth2 { scopes } = back {
            assert_eq!(scopes.len(), 2);
        } else {
            panic!("expected OAuth2 variant");
        }
    }

    #[test]
    fn auth_method_none_round_trip() {
        round_trip(&AuthMethod::None);
    }

    #[test]
    fn provider_capabilities_round_trip() {
        let caps = ProviderCapabilities {
            supports_streaming: true,
            supports_vision: true,
            max_context_tokens: 200_000,
            supports_function_calling: true,
        };
        round_trip(&caps);
    }

    #[test]
    fn provider_info_round_trip() {
        let info = ProviderInfo {
            id: "anthropic".into(),
            name: "Anthropic Claude".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: "https://api.anthropic.com".into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: true,
                max_context_tokens: 200_000,
                supports_function_calling: true,
            },
        };
        round_trip(&info);
    }

    #[test]
    fn task_type_serializes_snake_case() {
        let json = serde_json::to_string(&TaskType::CascadeMerge).unwrap();
        assert_eq!(json, r#""cascade_merge""#);
        let json = serde_json::to_string(&TaskType::RagEmbed).unwrap();
        assert_eq!(json, r#""rag_embed""#);
        let json = serde_json::to_string(&TaskType::LocalFallback).unwrap();
        assert_eq!(json, r#""local_fallback""#);
    }
}
