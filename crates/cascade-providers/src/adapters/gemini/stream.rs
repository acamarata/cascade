//! Gemini SSE streaming helpers — SSE parse + stream task.

use crate::{
    error::ProviderError,
    types::StreamChunk,
};

// ── SSE parse helper ──────────────────────────────────────────────────────────

/// Parse one SSE `data:` payload as a Gemini stream event.
///
/// Returns `Ok(Some(chunk))` with text delta, `Ok(None)` if the event carries
/// no text (e.g. safety-only events), or `Err` on parse failure.
pub(super) fn parse_gemini_stream_event(payload: &str) -> Result<Option<StreamChunk>, ProviderError> {
    let val: serde_json::Value = serde_json::from_str(payload)
        .map_err(|e| ProviderError::InvalidResponse(format!("SSE JSON: {e}")))?;

    let candidate = val.get("candidates").and_then(|c| c.get(0));

    let text = candidate
        .and_then(|c| c.get("content"))
        .and_then(|c| c.get("parts"))
        .and_then(|p| p.get(0))
        .and_then(|p| p.get("text"))
        .and_then(|t| t.as_str())
        .unwrap_or("")
        .to_string();

    let finish_reason = candidate
        .and_then(|c| c.get("finishReason"))
        .and_then(|f| f.as_str())
        .filter(|s| *s != "FINISH_REASON_UNSPECIFIED" && !s.is_empty())
        .map(|s| s.to_lowercase());

    if text.is_empty() && finish_reason.is_none() {
        return Ok(None);
    }

    Ok(Some(StreamChunk {
        delta: text,
        finish_reason,
    }))
}
