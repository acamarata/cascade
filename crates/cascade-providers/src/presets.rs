// presets.rs — Plug-and-play catalog of known OpenAI-compatible providers.
//
// Purpose: Cascade already accepts any OpenAI-compatible endpoint via
//   `ProviderKind::Custom { id, base_url }`. That is fully general, but it is
//   not plug-and-play: the user has to know each vendor's base URL and type it
//   correctly. This module closes that gap with a curated table so a user can
//   say "kimi" or "lmstudio" instead of looking up an endpoint.
//
//   Cascade is a FOSS tool. It must not privilege the maintainer's own
//   subscriptions — anyone should be able to bring their own subscription, API
//   key, or fully local runtime and have it work. Every entry here is reachable
//   with nothing but a key (or nothing at all, for local runtimes).
//
// Inputs:  a preset slug (e.g. "kimi", "xai", "vllm").
// Outputs: `ProviderPreset` metadata, or a ready-built `ProviderKind::Custom`.
// Constraints:
//   - Presets are CONVENIENCE ONLY. `ProviderKind::Custom` remains the general
//     escape hatch, so an unlisted provider is never blocked.
//   - Base URLs are the vendor's documented OpenAI-compatible root. Where a
//     vendor's URL could not be confirmed from first-party docs it is NOT
//     guessed — it is simply omitted from this table.
//   - Local presets carry no key requirement and default to loopback.
//
// SPORT: MASTER-CRATES.md → cascade-providers → presets

use crate::connect::ProviderKind;

/// How a provider expects credentials to be supplied.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuthStyle {
    /// `Authorization: Bearer <key>` — the OpenAI convention.
    Bearer,
    /// No credential at all (local runtimes on loopback).
    None,
}

/// A known OpenAI-compatible provider Cascade can talk to out of the box.
#[derive(Debug, Clone)]
pub struct ProviderPreset {
    /// Stable slug used on the CLI and as the keychain account name.
    pub id: &'static str,
    /// Human-readable name.
    pub display_name: &'static str,
    /// OpenAI-compatible API root (no trailing slash).
    pub base_url: &'static str,
    /// How credentials are passed.
    pub auth: AuthStyle,
    /// True when the endpoint runs on the user's own machine.
    pub local: bool,
    /// Where to get a key / read setup docs.
    pub docs_url: &'static str,
    /// One-line note: what this provider is good for, or a caveat.
    pub note: &'static str,
}

/// Every provider Cascade ships a preset for.
///
/// Hosted entries need only an API key. Local entries need only the runtime
/// running — no account, no key, no network egress.
pub const PRESETS: &[ProviderPreset] = &[
    // ── Hosted, API-key ─────────────────────────────────────────────────────
    ProviderPreset {
        id: "moonshot",
        display_name: "Moonshot AI (Kimi)",
        base_url: "https://api.moonshot.ai/v1",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://platform.moonshot.ai/docs",
        note: "Kimi models; long-context coding. Subscription and pay-as-you-go both use the same OpenAI-compatible endpoint.",
    },
    ProviderPreset {
        id: "zai",
        display_name: "Z.ai (GLM)",
        base_url: "https://api.z.ai/api/paas/v4",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://docs.z.ai",
        note: "GLM coding models. The GLM Coding Plan subscription and direct API keys share this endpoint.",
    },
    ProviderPreset {
        id: "xai",
        display_name: "xAI (Grok)",
        base_url: "https://api.x.ai/v1",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://docs.x.ai",
        note: "Grok models over an OpenAI-compatible surface.",
    },
    ProviderPreset {
        id: "fireworks",
        display_name: "Fireworks AI",
        base_url: "https://api.fireworks.ai/inference/v1",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://docs.fireworks.ai",
        note: "Fast hosted open-weight models; useful as a cheap bulk-execution lane.",
    },
    ProviderPreset {
        id: "cerebras",
        display_name: "Cerebras",
        base_url: "https://api.cerebras.ai/v1",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://inference-docs.cerebras.ai",
        note: "Very high token throughput on open-weight models.",
    },
    ProviderPreset {
        id: "perplexity",
        display_name: "Perplexity",
        base_url: "https://api.perplexity.ai",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://docs.perplexity.ai",
        note: "Search-grounded answers; better for research lanes than code generation.",
    },
    ProviderPreset {
        id: "nebius",
        display_name: "Nebius AI Studio",
        base_url: "https://api.studio.nebius.ai/v1",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://docs.nebius.com/studio/inference",
        note: "Hosted open-weight models.",
    },
    ProviderPreset {
        id: "hyperbolic",
        display_name: "Hyperbolic",
        base_url: "https://api.hyperbolic.xyz/v1",
        auth: AuthStyle::Bearer,
        local: false,
        docs_url: "https://docs.hyperbolic.xyz",
        note: "Hosted open-weight models.",
    },
    // ── Local runtimes — no key, no egress ───────────────────────────────────
    ProviderPreset {
        id: "ollama",
        display_name: "Ollama (local)",
        base_url: "http://127.0.0.1:11434/v1",
        auth: AuthStyle::None,
        local: true,
        docs_url: "https://ollama.com",
        note: "Runs models on your own machine. Cascade also ships a dedicated Ollama adapter; this preset is the OpenAI-compatible route.",
    },
    ProviderPreset {
        id: "lmstudio",
        display_name: "LM Studio (local)",
        base_url: "http://127.0.0.1:1234/v1",
        auth: AuthStyle::None,
        local: true,
        docs_url: "https://lmstudio.ai/docs/local-server",
        note: "Local server mode; start the server in LM Studio first.",
    },
    ProviderPreset {
        id: "llamacpp",
        display_name: "llama.cpp server (local)",
        base_url: "http://127.0.0.1:8080/v1",
        auth: AuthStyle::None,
        local: true,
        docs_url: "https://github.com/ggml-org/llama.cpp",
        note: "llama-server's OpenAI-compatible endpoint. Any GGUF, including quantised 27B-class coding models.",
    },
    ProviderPreset {
        id: "vllm",
        display_name: "vLLM (local or self-hosted)",
        base_url: "http://127.0.0.1:8000/v1",
        auth: AuthStyle::None,
        local: true,
        docs_url: "https://docs.vllm.ai",
        note: "High-throughput serving. Point base_url at a remote host to use a GPU box you control.",
    },
    ProviderPreset {
        id: "jan",
        display_name: "Jan (local)",
        base_url: "http://127.0.0.1:1337/v1",
        auth: AuthStyle::None,
        local: true,
        docs_url: "https://jan.ai/docs",
        note: "Desktop local runtime with an OpenAI-compatible server.",
    },
];

/// Look up a preset by slug (case-insensitive).
pub fn find(id: &str) -> Option<&'static ProviderPreset> {
    let want = id.trim().to_ascii_lowercase();
    PRESETS.iter().find(|p| p.id == want)
}

/// Build a ready-to-use [`ProviderKind`] from a preset slug.
///
/// Returns `None` for an unknown slug — callers should fall back to
/// [`ProviderKind::Custom`] with a user-supplied base URL, which keeps
/// unlisted providers usable.
pub fn to_provider_kind(id: &str) -> Option<ProviderKind> {
    find(id).map(|p| ProviderKind::Custom {
        id: p.id.to_string(),
        base_url: p.base_url.to_string(),
    })
}

/// All preset slugs, for CLI help and shell completion.
pub fn all_ids() -> Vec<&'static str> {
    PRESETS.iter().map(|p| p.id).collect()
}

/// Presets that run on the user's own machine.
pub fn local_ids() -> Vec<&'static str> {
    PRESETS.iter().filter(|p| p.local).map(|p| p.id).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lookup_is_case_insensitive_and_trims() {
        assert!(find("moonshot").is_some());
        assert!(find("  MoonShot ").is_some());
        assert!(find("definitely-not-a-provider").is_none());
    }

    #[test]
    fn every_preset_has_a_usable_base_url() {
        for p in PRESETS {
            assert!(
                p.base_url.starts_with("http://") || p.base_url.starts_with("https://"),
                "{} base_url must be an absolute URL",
                p.id
            );
            assert!(
                !p.base_url.ends_with('/'),
                "{} base_url must not have a trailing slash (callers append paths)",
                p.id
            );
        }
    }

    #[test]
    fn slugs_are_unique_and_lowercase() {
        let mut seen = std::collections::HashSet::new();
        for p in PRESETS {
            assert_eq!(
                p.id,
                p.id.to_ascii_lowercase(),
                "{} must be lowercase",
                p.id
            );
            assert!(seen.insert(p.id), "duplicate preset slug: {}", p.id);
        }
    }

    #[test]
    fn local_presets_need_no_credential_and_use_loopback() {
        for p in PRESETS.iter().filter(|p| p.local) {
            assert_eq!(
                p.auth,
                AuthStyle::None,
                "{} is local so it must not require a key by default",
                p.id
            );
            assert!(
                p.base_url.contains("127.0.0.1"),
                "{} is local so it must default to loopback, not a public host",
                p.id
            );
        }
    }

    #[test]
    fn hosted_presets_require_a_bearer_key_over_tls() {
        for p in PRESETS.iter().filter(|p| !p.local) {
            assert_eq!(p.auth, AuthStyle::Bearer, "{} should use bearer auth", p.id);
            assert!(
                p.base_url.starts_with("https://"),
                "{} sends a credential so it must be https",
                p.id
            );
        }
    }

    #[test]
    fn to_provider_kind_round_trips_the_base_url() {
        let kind = to_provider_kind("lmstudio").expect("lmstudio preset exists");
        match kind {
            ProviderKind::Custom { id, base_url } => {
                assert_eq!(id, "lmstudio");
                assert_eq!(base_url, "http://127.0.0.1:1234/v1");
            }
            other => panic!("expected Custom, got {other:?}"),
        }
        assert!(to_provider_kind("nope").is_none());
    }

    #[test]
    fn catalog_covers_both_hosted_and_local() {
        assert!(!local_ids().is_empty(), "must ship local options");
        assert!(
            all_ids().len() > local_ids().len(),
            "must ship hosted options too"
        );
    }
}
