/// Decode the payload section of a JWT and extract `sub` or `email`.
///
/// # Purpose
/// Extracts the email/sub claim from the middle (payload) segment of a JWT without
/// verifying the signature. Used for identity hints only — not for authorization.
///
/// Returns `None` for any malformed token (not an error — callers skip silently).
pub(super) fn decode_jwt_email(token: &str) -> Option<String> {
    use base64::Engine;

    let parts: Vec<&str> = token.splitn(3, '.').collect();
    if parts.len() < 2 {
        return None;
    }

    // base64url decode the payload segment (pad to multiple of 4)
    let payload_b64 = parts[1];
    let padding = (4 - payload_b64.len() % 4) % 4;
    let padded = format!("{}{}", payload_b64, "=".repeat(padding));
    let decoded = base64::engine::general_purpose::URL_SAFE_NO_PAD
        .decode(payload_b64)
        .or_else(|_| base64::engine::general_purpose::URL_SAFE.decode(&padded))
        .ok()?;

    let payload: serde_json::Value = serde_json::from_slice(&decoded).ok()?;

    // Prefer "email" over "sub" since sub may be an opaque ID
    payload
        .get("email")
        .or_else(|| payload.get("sub"))
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
        .map(str::to_string)
}

/// Normalize a provider ID string to a canonical name.
pub(super) fn normalize_provider(id: &str) -> String {
    match id.to_lowercase().as_str() {
        s if s.contains("anthropic") || s.contains("claude") => "anthropic".to_string(),
        s if s.contains("openai") || s.contains("gpt") || s.contains("codex") => {
            "openai".to_string()
        }
        s if s.contains("google") || s.contains("gemini") || s.contains("vertex") => {
            "google".to_string()
        }
        other => other.to_string(),
    }
}
