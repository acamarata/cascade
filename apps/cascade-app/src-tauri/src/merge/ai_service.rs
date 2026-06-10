// merge/ai_service.rs — AI provider call service for the merge engine.
//
// Purpose: Sends legacy instruction content to the connected AI provider and
//   parses the structured MergeResult from the response.
//
// Provider priority (per T-P3-E03-25 spec):
//   1. GeminiPool  — POST http://127.0.0.1:3761/v1beta/models/gemini-2.0-flash:generateContent
//   2. Anthropic   — stub (E-04 fills credentials; returns Err here)
//   3. OpenAI      — stub (E-04 fills credentials; returns Err here)
//   4. Local model — stub (E-04 implements cascade_local_model; returns Err here)
//
// Retry: up to 3 attempts with 2-second backoff on network errors or rate limits.
// Parse failure: returns a MergeResult with a single raw-output section (parseError path).
// Timeout: 60 seconds per HTTP call.
//
// Inputs:  tier (u32), source_files (Vec<MergeSourceFileRaw>), system_prompt, user_prompt,
//          prompt_hash (SHA-256 hex of user_prompt, computed on TypeScript side).
// Outputs: MergeResult or Err(String).
//
// Constraints:
//   - No API keys handled here; proxy rotates credentials.
//   - No panics on malformed AI output; structured Err returned instead.
//   - Uses reqwest::blocking to keep the function signature sync.
//     (run_ai_merge command uses tokio::task::spawn_blocking.)
//
// SPORT: MASTER-COMPONENTS.md — mergeService / ai_service — merge/ai_service.rs
// Task: T-P3-E03-25

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::merge::types::{MergeResult, MergeSection, MergeSourceFile, MergeSourceFileRaw, SectionStatus};
use crate::scanner::types::ToolId;

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const GEMINI_HEALTH_URL: &str = "http://127.0.0.1:3761/health";
const GEMINI_GENERATE_URL: &str =
    "http://127.0.0.1:3761/v1beta/models/gemini-2.0-flash:generateContent";
const MODEL_GEMINI: &str = "gemini-2.0-flash";

const TIMEOUT_SECS: u64 = 60;
const MAX_RETRIES: u32 = 3;
const RETRY_DELAY_SECS: u64 = 2;

// ---------------------------------------------------------------------------
// Gemini API wire types (request/response)
// ---------------------------------------------------------------------------

/// Request body for Gemini generateContent.
#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct GeminiRequest {
    contents: Vec<GeminiContent>,
    system_instruction: GeminiSystemInstruction,
    generation_config: GeminiGenerationConfig,
}

#[derive(Debug, Serialize)]
struct GeminiContent {
    role: String,
    parts: Vec<GeminiPart>,
}

#[derive(Debug, Serialize)]
struct GeminiPart {
    text: String,
}

#[derive(Debug, Serialize)]
struct GeminiSystemInstruction {
    parts: Vec<GeminiPart>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct GeminiGenerationConfig {
    response_mime_type: String,
}

/// Top-level response from Gemini generateContent.
#[derive(Debug, Deserialize)]
struct GeminiResponse {
    candidates: Option<Vec<GeminiCandidate>>,
}

#[derive(Debug, Deserialize)]
struct GeminiCandidate {
    content: Option<GeminiContent2>,
}

#[derive(Debug, Deserialize)]
struct GeminiContent2 {
    parts: Option<Vec<GeminiPart2>>,
}

#[derive(Debug, Deserialize)]
struct GeminiPart2 {
    text: Option<String>,
}

// ---------------------------------------------------------------------------
// AI merge sections wire type (parsed from model output)
// ---------------------------------------------------------------------------

/// JSON shape the model is asked to produce.
#[derive(Debug, Deserialize)]
struct AiMergeOutput {
    sections: Option<Vec<AiSection>>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct AiSection {
    id: Option<String>,
    title: Option<String>,
    content: Option<String>,
    // sourceFiles from model output — accepted for future use; we track sources via the
    // MergeSourceFile list passed in from the reader.
    #[serde(default)]
    #[allow(dead_code)]
    source_files: Vec<String>,
}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Call the connected AI provider and parse a MergeResult for the given tier.
///
/// # Arguments
/// * `tier`          — WizardStep integer value (e.g. 4 = MergeContent).
/// * `source_files`  — Legacy file content prepared by `reader::read_legacy_content`.
/// * `system_prompt` — Versioned system prompt (merge-v1).
/// * `user_prompt`   — Constructed prompt with inline file contents.
/// * `prompt_hash`   — SHA-256 hex of `user_prompt` for re-run identity.
///
/// # Returns
/// `Ok(MergeResult)` with ≥1 section, or `Err` on unrecoverable provider failure.
pub fn call_ai_merge(
    tier: u32,
    source_files: Vec<MergeSourceFileRaw>,
    system_prompt: String,
    user_prompt: String,
    prompt_hash: String,
) -> Result<MergeResult, String> {
    let client = reqwest::blocking::Client::builder()
        .timeout(Duration::from_secs(TIMEOUT_SECS))
        .build()
        .map_err(|e| format!("failed to build HTTP client: {e}"))?;

    // Attempt providers in priority order.
    // Provider 1: GeminiPool (localhost:3761).
    if is_gemini_pool_available(&client) {
        return call_gemini_pool(
            &client,
            tier,
            source_files,
            system_prompt,
            user_prompt,
            prompt_hash,
        );
    }

    // Provider 2–4: Anthropic / OpenAI / Local — stubs; E-04 fills credentials.
    // Return structured Err so the frontend can surface the "no provider" state.
    Err(
        "No AI provider available. \
         Connect a provider in the wizard (Gemini Pool, Anthropic, OpenAI, or local model)."
            .to_string(),
    )
}

// ---------------------------------------------------------------------------
// GeminiPool availability check
// ---------------------------------------------------------------------------

/// Returns `true` if the local Gemini proxy is reachable (fast GET to /health).
fn is_gemini_pool_available(_client: &reqwest::blocking::Client) -> bool {
    let health_client = match reqwest::blocking::Client::builder()
        .timeout(Duration::from_millis(800))
        .build()
    {
        Ok(c) => c,
        Err(_) => return false,
    };

    health_client
        .get(GEMINI_HEALTH_URL)
        .send()
        .map(|r| r.status().is_success())
        .unwrap_or(false)
}

// ---------------------------------------------------------------------------
// GeminiPool call with retry
// ---------------------------------------------------------------------------

fn call_gemini_pool(
    client: &reqwest::blocking::Client,
    tier: u32,
    source_files: Vec<MergeSourceFileRaw>,
    system_prompt: String,
    user_prompt: String,
    prompt_hash: String,
) -> Result<MergeResult, String> {
    let body = build_gemini_request(&system_prompt, &user_prompt);

    let mut last_err = String::new();

    for attempt in 0..MAX_RETRIES {
        if attempt > 0 {
            std::thread::sleep(Duration::from_secs(RETRY_DELAY_SECS));
        }

        match client
            .post(GEMINI_GENERATE_URL)
            .header("Content-Type", "application/json")
            .json(&body)
            .send()
        {
            Ok(resp) => {
                let status = resp.status();

                // Rate limit or server error → retry.
                if status.as_u16() == 429 || status.as_u16() >= 500 {
                    last_err = format!("HTTP {status} from Gemini proxy");
                    continue;
                }

                if !status.is_success() {
                    return Err(format!("Gemini proxy returned HTTP {status}"));
                }

                let raw_text = match resp.text() {
                    Ok(t) => t,
                    Err(e) => return Err(format!("failed to read Gemini response body: {e}")),
                };

                let model_text = extract_model_text(&raw_text)?;
                return parse_ai_output(
                    tier,
                    source_files,
                    &model_text,
                    MODEL_GEMINI,
                    prompt_hash,
                );
            }
            Err(e) => {
                last_err = format!("network error on attempt {}: {e}", attempt + 1);
                // Network error → retry.
            }
        }
    }

    Err(format!(
        "Gemini Pool call failed after {MAX_RETRIES} attempts: {last_err}"
    ))
}

// ---------------------------------------------------------------------------
// Request builder
// ---------------------------------------------------------------------------

fn build_gemini_request(system_prompt: &str, user_prompt: &str) -> GeminiRequest {
    GeminiRequest {
        contents: vec![GeminiContent {
            role: "user".to_string(),
            parts: vec![GeminiPart {
                text: user_prompt.to_string(),
            }],
        }],
        system_instruction: GeminiSystemInstruction {
            parts: vec![GeminiPart {
                text: system_prompt.to_string(),
            }],
        },
        generation_config: GeminiGenerationConfig {
            response_mime_type: "application/json".to_string(),
        },
    }
}

// ---------------------------------------------------------------------------
// Response text extraction
// ---------------------------------------------------------------------------

/// Extract the model's text from a raw Gemini JSON response body.
fn extract_model_text(raw: &str) -> Result<String, String> {
    let resp: GeminiResponse = serde_json::from_str(raw)
        .map_err(|e| format!("failed to parse Gemini response envelope: {e} — raw: {raw}"))?;

    let text = resp
        .candidates
        .as_deref()
        .and_then(|c| c.first())
        .and_then(|c| c.content.as_ref())
        .and_then(|c| c.parts.as_deref())
        .and_then(|p| p.first())
        .and_then(|p| p.text.as_deref())
        .unwrap_or("")
        .to_string();

    Ok(text)
}

// ---------------------------------------------------------------------------
// AI output parser
// ---------------------------------------------------------------------------

/// Parse the model's text output into a MergeResult.
///
/// Strategy:
///   1. Try `serde_json::from_str` directly.
///   2. If that fails, strip a leading ` ```json ` / trailing ` ``` ` fence.
///   3. If both fail, return a single "parse error" section containing the raw text.
fn parse_ai_output(
    tier: u32,
    source_files: Vec<MergeSourceFileRaw>,
    model_text: &str,
    model_used: &str,
    prompt_hash: String,
) -> Result<MergeResult, String> {
    let generated_at = iso8601_now();

    // Convert raw source files to the lighter MergeSourceFile form used in sections.
    let light_sources: Vec<MergeSourceFile> = source_files
        .iter()
        .map(|f| MergeSourceFile {
            tool_id: ToolId::from_str_lossy(&f.tool_id),
            path: f.path.clone(),
            content: f.content.clone(),
            kind: f.kind.clone(),
        })
        .collect();

    // Attempt 1: direct JSON parse.
    let maybe_output: Option<AiMergeOutput> = serde_json::from_str(model_text).ok();

    // Attempt 2: strip markdown code fence.
    let maybe_output = maybe_output.or_else(|| {
        let stripped = strip_json_fence(model_text);
        serde_json::from_str(stripped).ok()
    });

    match maybe_output {
        Some(output) => {
            let sections = build_sections_from_output(output, &light_sources);

            if sections.is_empty() {
                // Parsed but empty sections — treat as parse error.
                return Ok(make_parse_error_result(
                    tier,
                    model_text,
                    light_sources,
                    model_used,
                    prompt_hash,
                    generated_at,
                ));
            }

            Ok(MergeResult {
                tier,
                sections,
                generated_at,
                model_used: model_used.to_string(),
                prompt_hash,
            })
        }
        None => {
            // Both parse attempts failed — return parse-error section.
            Ok(make_parse_error_result(
                tier,
                model_text,
                light_sources,
                model_used,
                prompt_hash,
                generated_at,
            ))
        }
    }
}

/// Build MergeSection vec from a successfully parsed AiMergeOutput.
fn build_sections_from_output(
    output: AiMergeOutput,
    default_sources: &[MergeSourceFile],
) -> Vec<MergeSection> {
    let raw_sections = match output.sections {
        Some(s) if !s.is_empty() => s,
        _ => return vec![],
    };

    raw_sections
        .into_iter()
        .enumerate()
        .filter_map(|(i, s)| {
            let title = s.title.filter(|t| !t.is_empty())?;
            let content = s.content.unwrap_or_default();
            let id = s
                .id
                .filter(|v| !v.is_empty())
                .unwrap_or_else(|| format!("section-{i}"));

            Some(MergeSection {
                id,
                title,
                source_files: default_sources.to_vec(),
                proposed_content: content,
                status: SectionStatus::Pending,
                edited_content: None,
                conflict_with: None,
            })
        })
        .collect()
}

/// Return a single-section MergeResult with parse_error encoded in the section id.
fn make_parse_error_result(
    tier: u32,
    raw_text: &str,
    sources: Vec<MergeSourceFile>,
    model_used: &str,
    prompt_hash: String,
    generated_at: String,
) -> MergeResult {
    MergeResult {
        tier,
        sections: vec![MergeSection {
            id: "parse-error".to_string(),
            title: "Raw AI Output (parse failed)".to_string(),
            source_files: sources,
            proposed_content: raw_text.to_string(),
            status: SectionStatus::Pending,
            edited_content: None,
            conflict_with: None,
        }],
        generated_at,
        model_used: model_used.to_string(),
        prompt_hash,
    }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Strip a leading ```json fence and trailing ``` from `text`.
fn strip_json_fence(text: &str) -> &str {
    let text = text.trim();
    let text = text
        .strip_prefix("```json")
        .or_else(|| text.strip_prefix("```"))
        .unwrap_or(text);
    let text = text.strip_suffix("```").unwrap_or(text);
    text.trim()
}

/// Return current UTC time as ISO 8601 string (second precision).
fn iso8601_now() -> String {
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let (y, mo, d, h, mi, s) = secs_to_ymd_hms(secs);
    format!("{y:04}-{mo:02}-{d:02}T{h:02}:{mi:02}:{s:02}Z")
}

/// Convert Unix seconds to (year, month, day, hour, min, sec) UTC.
fn secs_to_ymd_hms(secs: u64) -> (u32, u32, u32, u32, u32, u32) {
    let s = (secs % 60) as u32;
    let mins = secs / 60;
    let mi = (mins % 60) as u32;
    let hours = mins / 60;
    let h = (hours % 24) as u32;
    let mut days = (hours / 24) as u32;

    let mut y = 1970u32;
    loop {
        let dy = if is_leap(y) { 366 } else { 365 };
        if days < dy {
            break;
        }
        days -= dy;
        y += 1;
    }

    let month_days: [u32; 12] = if is_leap(y) {
        [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
    } else {
        [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
    };

    let mut mo = 0u32;
    for &md in &month_days {
        if days < md {
            break;
        }
        days -= md;
        mo += 1;
    }

    (y, mo + 1, days + 1, h, mi, s)
}

fn is_leap(y: u32) -> bool {
    (y % 4 == 0 && y % 100 != 0) || y % 400 == 0
}

// ---------------------------------------------------------------------------
// SHA-256 helper (used by TS side; exposed here for parity testing)
// ---------------------------------------------------------------------------

/// Compute SHA-256 hex of a string.
pub fn sha256_hex(input: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(input.as_bytes());
    let result = hasher.finalize();
    result
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>()
}

// ---------------------------------------------------------------------------
// ToolId helper — lossy string conversion for MergeSourceFileRaw.tool_id
// ---------------------------------------------------------------------------

impl ToolId {
    /// Parse a string into a ToolId, defaulting to ClaudeCode on unknown values.
    pub fn from_str_lossy(s: &str) -> Self {
        match s {
            "claude-code" | "claude_code" | "ClaudeCode" => ToolId::ClaudeCode,
            "cursor" | "Cursor" => ToolId::Cursor,
            "aider" | "Aider" => ToolId::Aider,
            "windsurf" | "Windsurf" => ToolId::Windsurf,
            "opencode" | "Opencode" | "OpenCode" => ToolId::Opencode,
            "antigravity" | "Antigravity" => ToolId::Antigravity,
            "codex" | "Codex" => ToolId::Codex,
            _ => ToolId::ClaudeCode,
        }
    }
}

// ---------------------------------------------------------------------------
// Tests — response parsing with canned model output (no live network)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::scanner::types::FileKind;

    fn make_raw_source(path: &str, content: &str) -> MergeSourceFileRaw {
        MergeSourceFileRaw {
            tool_id: "claude_code".to_string(),
            path: path.to_string(),
            content: Some(content.to_string()),
            kind: FileKind::Instructions,
            size_bytes: content.len() as u64,
        }
    }

    // -----------------------------------------------------------------------
    // Test 1: valid JSON with sections → MergeResult with sections
    // -----------------------------------------------------------------------
    #[test]
    fn parse_valid_json_produces_sections() {
        let model_text = r#"{
            "sections": [
                {
                    "id": "s1",
                    "title": "Code Quality",
                    "content": "No any types. Strict TS.",
                    "sourceFiles": ["/Users/admin/.claude/CLAUDE.md"]
                },
                {
                    "id": "s2",
                    "title": "Testing Rules",
                    "content": "Always write tests.",
                    "sourceFiles": []
                }
            ]
        }"#;

        let sources = vec![make_raw_source(
            "/Users/admin/.claude/CLAUDE.md",
            "no any types",
        )];
        let result = parse_ai_output(4, sources, model_text, MODEL_GEMINI, "abc123".to_string())
            .expect("parse_ai_output should succeed");

        assert_eq!(result.sections.len(), 2, "expected 2 sections");
        assert_eq!(result.sections[0].title, "Code Quality");
        assert_eq!(result.sections[0].id, "s1");
        assert_eq!(result.sections[1].title, "Testing Rules");
        assert_eq!(result.model_used, MODEL_GEMINI);
        assert_eq!(result.prompt_hash, "abc123");
        // No parse-error section.
        assert!(!result.sections[0].id.contains("parse-error"));
    }

    // -----------------------------------------------------------------------
    // Test 2: JSON wrapped in markdown code fence → sections
    // -----------------------------------------------------------------------
    #[test]
    fn parse_json_in_markdown_fence_produces_sections() {
        let model_text = r#"```json
{
    "sections": [
        {
            "id": "fence-s1",
            "title": "Fenced Section",
            "content": "Content from fenced JSON.",
            "sourceFiles": []
        }
    ]
}
```"#;

        let sources = vec![make_raw_source("/some/file.md", "content")];
        let result = parse_ai_output(4, sources, model_text, MODEL_GEMINI, "hash1".to_string())
            .expect("should succeed");

        assert_eq!(result.sections.len(), 1);
        assert_eq!(result.sections[0].title, "Fenced Section");
    }

    // -----------------------------------------------------------------------
    // Test 3: malformed / non-JSON output → single parse-error section
    // -----------------------------------------------------------------------
    #[test]
    fn parse_malformed_json_returns_parse_error_section() {
        let model_text = "I am sorry, here is a list:\n- Rule 1\n- Rule 2";

        let sources = vec![make_raw_source("/some/file.md", "content")];
        let result = parse_ai_output(4, sources, model_text, MODEL_GEMINI, "hash2".to_string())
            .expect("should return Ok, not Err");

        assert_eq!(result.sections.len(), 1);
        assert_eq!(result.sections[0].id, "parse-error");
        assert!(
            result.sections[0].proposed_content.contains("Rule 1"),
            "raw output should be in proposed_content"
        );
    }

    // -----------------------------------------------------------------------
    // Test 4: empty JSON sections array → parse-error section
    // -----------------------------------------------------------------------
    #[test]
    fn parse_empty_sections_array_returns_parse_error_section() {
        let model_text = r#"{"sections": []}"#;

        let sources = vec![];
        let result = parse_ai_output(4, sources, model_text, MODEL_GEMINI, "hash3".to_string())
            .expect("should return Ok");

        assert_eq!(result.sections.len(), 1);
        assert_eq!(result.sections[0].id, "parse-error");
    }

    // -----------------------------------------------------------------------
    // Test 5: section missing required title → section filtered out
    // -----------------------------------------------------------------------
    #[test]
    fn parse_section_missing_title_filtered_out() {
        let model_text = r#"{
            "sections": [
                { "id": "good", "title": "Good Section", "content": "Some content.", "sourceFiles": [] },
                { "id": "bad", "content": "No title here.", "sourceFiles": [] }
            ]
        }"#;

        let sources = vec![];
        let result = parse_ai_output(4, sources, model_text, MODEL_GEMINI, "hash4".to_string())
            .expect("should succeed");

        // Only the section with a title is kept.
        assert_eq!(result.sections.len(), 1, "expected 1 filtered section");
        assert_eq!(result.sections[0].title, "Good Section");
    }

    // -----------------------------------------------------------------------
    // Test 6: sha256_hex produces known value
    // -----------------------------------------------------------------------
    #[test]
    fn sha256_hex_known_value() {
        // SHA-256("abc") = ba7816bf8f01cfea414140de5dae2ec73b00361bbef0469346f912ead9a89b9
        let h = sha256_hex("abc");
        assert!(
            h.starts_with("ba7816bf"),
            "unexpected SHA-256 prefix: {h}"
        );
        assert_eq!(h.len(), 64, "SHA-256 hex must be 64 chars");
    }

    // -----------------------------------------------------------------------
    // Test 7: strip_json_fence round-trips
    // -----------------------------------------------------------------------
    #[test]
    fn strip_json_fence_various_forms() {
        assert_eq!(strip_json_fence("```json\n{}\n```"), "{}");
        assert_eq!(strip_json_fence("```\n{}\n```"), "{}");
        assert_eq!(strip_json_fence("{}"), "{}");
        assert_eq!(strip_json_fence("  {} "), "{}");
    }
}
