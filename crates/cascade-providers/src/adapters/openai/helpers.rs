//! Constants and helper functions for the OpenAI adapter.

// ── Constants ──────────────────────────────────────────────────────────────────

/// Default OpenAI REST base URL (no trailing slash).
pub(super) const DEFAULT_BASE_URL: &str = "https://api.openai.com";

/// Model ID prefixes this adapter considers chat-capable.
/// Embedding, DALL-E, Whisper, and TTS models are excluded.
pub(super) const CHAT_PREFIXES: &[&str] = &["gpt-", "chatgpt-", "o1", "o3", "o4", "text-davinci"];

/// Prefixes for o-series reasoning models with API quirks.
pub(super) const O_SERIES_PREFIXES: &[&str] = &["o1", "o3", "o4"];

// ── Private helpers ────────────────────────────────────────────────────────────

/// Best-effort context window for well-known OpenAI model families.
/// Falls back to 4 096 for unrecognised model ids.
pub(super) fn model_context_window(id: &str) -> u32 {
    if id.starts_with("gpt-4o")
        || id.starts_with("chatgpt-4o")
        || id.starts_with("gpt-4-turbo")
        || id.starts_with("o1")
        || id.starts_with("o3")
        || id.starts_with("o4")
    {
        128_000
    } else if id.starts_with("gpt-4") {
        8_192
    } else if id.starts_with("gpt-3.5") {
        16_385
    } else {
        // text-davinci and unrecognised models both default to 4 096.
        4_096
    }
}
