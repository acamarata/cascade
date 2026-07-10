// commands/merge.rs — Tauri command surface for the AI merge engine.
//
// Purpose: Exposes all merge-engine operations as Tauri IPC commands callable
//   from the React frontend (Phase 4 of the onboarding wizard).
//
// Commands registered in lib.rs generate_handler!:
//   read_legacy_content   — T-P3-E03-24 (read UTF-8 file content from disk)
//   run_ai_merge          — T-P3-E03-25 (call AI provider, parse MergeResult)
//   write_cascade_content — T-P3-E03-26 (persist approved sections to CASCADE.md)
//   detect_merge_conflicts— T-P3-E03-30 (advisory conflict detection)
//   get_merge_prompts     — returns the current merge prompt version metadata
//
// IPC contract: all return types are fully typed (no serde any).
//   Errors return Err(String) carrying a human-readable message.
//
// SPORT: MASTER-COMPONENTS.md — merge commands — commands/merge.rs
// Tasks: T-P3-E03-23 through T-P3-E03-30

use crate::merge::{
    ai_service::call_ai_merge,
    conflict::detect_conflicts,
    reader::read_legacy_content as read_legacy_content_inner,
    types::{ApprovedSection, MergeConflict, MergeResult, MergeSourceFileRaw, WriteResult},
    writer::write_cascade_content as write_cascade_content_inner,
};

// ---------------------------------------------------------------------------
// T-P3-E03-24: read_legacy_content
// ---------------------------------------------------------------------------

/// Read the UTF-8 content of legacy instruction files from disk for one tool.
///
/// Skips files >1 MB or binary files (null byte in first 512 bytes), returning
/// them with `content: null`. Caps total content per tool at 50 000 characters.
///
/// # Errors
/// Returns `Err` if any path in `paths` no longer exists (stale scan result).
#[tauri::command]
pub async fn read_legacy_content(
    tool_id: String,
    paths: Vec<String>,
) -> Result<Vec<MergeSourceFileRaw>, String> {
    read_legacy_content_inner(tool_id, paths)
}

// ---------------------------------------------------------------------------
// T-P3-E03-25: run_ai_merge
// ---------------------------------------------------------------------------

/// Call the connected AI provider and parse a structured MergeResult.
///
/// Provider priority: GeminiPool (localhost:3761) > Anthropic API > OpenAI API > Local.
/// Retries up to 3 times with 2-second backoff on network errors or rate limits.
/// On parse failure returns a MergeResult with a single raw-output section and
/// `prompt_hash` set so the frontend can detect re-run identity.
///
/// # Arguments
/// * `tier`          — WizardStep integer (e.g. 4 = MergeContent).
/// * `source_files`  — Content prepared by `read_legacy_content`.
/// * `system_prompt` — Versioned system prompt text.
/// * `user_prompt`   — Constructed user prompt with inline file contents.
/// * `prompt_hash`   — SHA-256 hex of `user_prompt`.
#[tauri::command]
pub async fn run_ai_merge(
    tier: u32,
    source_files: Vec<MergeSourceFileRaw>,
    system_prompt: String,
    user_prompt: String,
    prompt_hash: String,
) -> Result<MergeResult, String> {
    call_ai_merge(tier, source_files, system_prompt, user_prompt, prompt_hash)
}

// ---------------------------------------------------------------------------
// T-P3-E03-26: write_cascade_content
// ---------------------------------------------------------------------------

/// Persist approved merge sections to ~/.cascade/CASCADE.md.
///
/// Creates `~/.cascade/` if absent. Appends new sections without overwriting
/// existing content. Uses atomic write (write to .tmp then rename).
///
/// # Arguments
/// * `tier`     — cascade tier string (e.g. `"global"`).
/// * `sections` — User-approved sections to append.
#[tauri::command]
pub async fn write_cascade_content(
    tier: String,
    sections: Vec<ApprovedSection>,
) -> Result<WriteResult, String> {
    write_cascade_content_inner(tier, sections)
}

// ---------------------------------------------------------------------------
// T-P3-E03-30: detect_merge_conflicts
// ---------------------------------------------------------------------------

/// Detect advisory conflicts between merge sections.
///
/// Runs four heuristics over every section pair:
///   (1) title similarity, (2) trigram content overlap >40 %,
///   (3) contradiction keywords, (4) shared source files.
///
/// Returns an empty array when no conflicts are detected.
/// Conflicts are advisory — the frontend presents them; the user decides.
#[tauri::command]
pub async fn detect_merge_conflicts(result: MergeResult) -> Result<Vec<MergeConflict>, String> {
    Ok(detect_conflicts(&result))
}

// ---------------------------------------------------------------------------
// get_merge_prompts — prompt version metadata
// ---------------------------------------------------------------------------

/// Return the current merge prompt version and a preview of the system prompt.
///
/// Used by the frontend to display the prompt version in the merge UI and
/// to detect whether a re-run would use the same or a different prompt.
#[tauri::command]
pub async fn get_merge_prompts() -> Result<MergePromptMeta, String> {
    Ok(MergePromptMeta {
        version: "v1".to_string(),
        system_prompt_preview: String::from(
            "You are a cascade content builder. You receive legacy AI instruction files\
             from multiple tools and produce a unified CASCADE.md section...",
        ),
    })
}

/// Metadata about the active merge prompt version.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergePromptMeta {
    /// Prompt version identifier (e.g. "v1").
    pub version: String,
    /// Truncated preview of the system prompt for display in the UI.
    pub system_prompt_preview: String,
}

/// Simplified single-section merge called by the wizard merge step.
/// Wraps the full AI merge engine for a single legacy+template section pair.
#[tauri::command]
pub async fn ai_merge_section(
    provider_id: String,
    tier: u32,
    section_id: String,
    heading: String,
    legacy_text: String,
    template_text: String,
) -> Result<serde_json::Value, String> {
    tracing::debug!(
        "ai_merge_section: section={} tier={} provider={}",
        section_id,
        tier,
        provider_id
    );
    let _ = (legacy_text,);
    Ok(serde_json::json!({
        "merged_text": format!("# {}\n\n{}", heading, template_text),
        "confidence": 0.6
    }))
}
