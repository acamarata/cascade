// commands::instructions — Tier instruction-file write IPC (T-P7-E21-07).
//
// Purpose: Allow the instructions browser to persist edits to a named tier's
//   CASCADE.md file. Implements two commands:
//     - cascade_write_tier: atomic write with path security validation.
//     - cascade_tier_cascade_dir: resolve the .cascade/ directory for a tier.
//
// Security:
//   - tier_root must end with the ".cascade" component (prevents writing
//     outside a managed cascade directory).
//   - tier_root is canonicalised before use (rejects ".." traversal and
//     symlink escapes).
//   - The target file ("CASCADE.md") is a literal constant joined to the
//     validated root — no user-controlled path component reaches the write.
//   - New tier directories are never created implicitly.
//   - Writes only the tier the caller names; no bulk write across tiers.
//   - Unchanged content produces a no-op (written: false).
//
// Constraints:
//   - cascade_tier_cascade_dir returns None for PPC/PRC/PAC (project-relative
//     tiers require a project root the UI does not carry).
//   - Atomic write: temp file → std::fs::rename.
//
// SPORT: MASTER-COMMANDS.md — cascade_write_tier, cascade_tier_cascade_dir (T-P7-E21-07)

use std::io::Write as IoWrite;
use std::path::{Component, Path, PathBuf};

use serde::{Deserialize, Serialize};
use tauri::State;

use cascade_types::paths::CASCADE_DIR_NAME;
use cascade_types::tiers::TierName;

use crate::error::CascadeError;
use crate::state::AppState;

// ── Types ─────────────────────────────────────────────────────────────────────

/// Result returned by `cascade_write_tier`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct WriteTierResult {
    /// `true` when content was written; `false` when content was unchanged
    /// (no-op). Both outcomes are successes.
    pub written: bool,
    /// Absolute path to the CASCADE.md file that was (or would be) written.
    pub path: String,
}

/// Result returned by `cascade_tier_cascade_dir`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TierCascadeDirResult {
    /// The requested tier slug.
    pub tier: String,
    /// Absolute path to the `.cascade/` directory for this tier, or `None`
    /// when the path cannot be resolved without a project root (PPC/PRC/PAC).
    pub cascade_dir: Option<String>,
}

// ── Helpers (pub(crate) so tests in the same crate can reach them) ────────────

/// Parse a tier slug ("gci", "GCI", …) into a [`TierName`].
pub(crate) fn parse_tier(tier: &str) -> Result<TierName, CascadeError> {
    match tier.to_uppercase().as_str() {
        "GCI" => Ok(TierName::GCI),
        "PCI" => Ok(TierName::PCI),
        "APC" => Ok(TierName::APC),
        "PPC" => Ok(TierName::PPC),
        "PRC" => Ok(TierName::PRC),
        "PAC" => Ok(TierName::PAC),
        _ => Err(CascadeError::InvalidParams(format!(
            "unknown tier '{tier}' — expected one of: gci pci apc ppc prc pac"
        ))),
    }
}

/// Validate `tier_root` and return its canonical [`PathBuf`].
///
/// Checks (in order):
///   1. No `..` components in the raw path (traversal pre-check).
///   2. Path exists and is a directory (via `canonicalize`).
///   3. Final path component equals `.cascade`.
pub(crate) fn validate_cascade_dir(tier_root: &str) -> Result<PathBuf, CascadeError> {
    let raw = PathBuf::from(tier_root);

    // Reject any ".." component before resolving symlinks.
    if raw.components().any(|c| c == Component::ParentDir) {
        return Err(CascadeError::InvalidParams(
            "tier_root must not contain '..' — path traversal rejected".to_string(),
        ));
    }

    // Canonicalize: rejects non-existent paths and resolves real symlinks.
    let canon = raw.canonicalize().map_err(|e| {
        CascadeError::InvalidParams(format!(
            "tier_root does not exist or cannot be resolved: {e}"
        ))
    })?;

    if !canon.is_dir() {
        return Err(CascadeError::InvalidParams(
            "tier_root must be a directory".to_string(),
        ));
    }

    // Final component must be ".cascade".
    let last = canon.file_name().and_then(|n| n.to_str()).unwrap_or("");
    if last != CASCADE_DIR_NAME {
        return Err(CascadeError::InvalidParams(format!(
            "tier_root must end with '.cascade', got: '{last}'"
        )));
    }

    Ok(canon)
}

/// Core write logic: write `content` to `{canon_dir}/CASCADE.md`.
///
/// Returns `WriteTierResult { written: false, … }` when content is unchanged.
/// Otherwise writes atomically via a `.tmp` sibling and renames.
pub(crate) fn write_tier_content(
    canon_dir: &Path,
    content: &str,
) -> Result<WriteTierResult, CascadeError> {
    let target = canon_dir.join("CASCADE.md");

    // No-op when content is identical to what is already on disk.
    let existing = std::fs::read_to_string(&target).unwrap_or_default();
    if existing == content {
        return Ok(WriteTierResult {
            written: false,
            path: target.to_string_lossy().into_owned(),
        });
    }

    // Atomic write: .tmp → rename.
    let tmp = target.with_extension("md.tmp");
    {
        let mut f = std::fs::File::create(&tmp)
            .map_err(|e| CascadeError::Custom(format!("failed to create temp file: {e}")))?;
        f.write_all(content.as_bytes())
            .map_err(|e| CascadeError::Custom(format!("failed to write content: {e}")))?;
        f.flush()
            .map_err(|e| CascadeError::Custom(format!("failed to flush: {e}")))?;
    }

    std::fs::rename(&tmp, &target).map_err(|e| {
        let _ = std::fs::remove_file(&tmp);
        CascadeError::Custom(format!("failed to rename temp file to CASCADE.md: {e}"))
    })?;

    Ok(WriteTierResult {
        written: true,
        path: target.to_string_lossy().into_owned(),
    })
}

// ── Tauri commands ─────────────────────────────────────────────────────────────

/// Persist edited instruction content to a named tier's CASCADE.md.
///
/// JS: `invoke("cascade_write_tier", { tier, tierRoot, content })`
///
/// # Security
///
/// `tier_root` must be an **existing** `.cascade/` directory — no new tier
/// directories are created. The path is canonicalised; any `..` component or
/// symlink that escapes the `.cascade` boundary is rejected.  The write target
/// is always the literal `CASCADE.md` inside the validated root.
///
/// # Returns
///
/// `WriteTierResult { written: bool, path: string }`. `written` is `false`
/// when `content` matches the file already on disk (no-op, still a success).
#[tauri::command]
pub async fn cascade_write_tier(
    tier: String,
    tier_root: String,
    content: String,
    _state: State<'_, AppState>,
) -> Result<WriteTierResult, CascadeError> {
    // Validate tier slug.
    let _tier_name = parse_tier(&tier)?;

    // Validate and canonicalise tier_root.
    let canon_dir = validate_cascade_dir(&tier_root)?;

    write_tier_content(&canon_dir, &content)
}

/// Resolve the `.cascade/` directory path for a named tier.
///
/// JS: `invoke("cascade_tier_cascade_dir", { tier })`
///
/// Returns `{ tier, cascade_dir: string | null }`. `cascade_dir` is:
///   - The absolute path to the `.cascade/` directory when it exists on disk.
///   - `null` for project-relative tiers (PPC / PRC / PAC) because the UI
///     does not carry a project root, and for any tier whose directory does
///     not yet exist (prevent implicit creation).
#[tauri::command]
pub async fn cascade_tier_cascade_dir(
    tier: String,
    _state: State<'_, AppState>,
) -> Result<TierCascadeDirResult, CascadeError> {
    let tier_name = parse_tier(&tier)?;

    // Only resolve for tiers with a deterministic, HOME-relative path.
    let dir = match tier_name {
        TierName::GCI | TierName::PCI | TierName::APC => {
            let placeholder = Path::new("");
            tier_name
                .default_path(placeholder)
                .filter(|p| p.is_dir())
                .map(|p| p.to_string_lossy().into_owned())
        }
        // PPC / PRC / PAC require a project root the UI does not have.
        TierName::PPC | TierName::PRC | TierName::PAC => None,
    };

    Ok(TierCascadeDirResult {
        tier,
        cascade_dir: dir,
    })
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    // ── parse_tier ────────────────────────────────────────────────────────────

    #[test]
    fn parse_tier_accepts_lowercase() {
        for slug in &["gci", "pci", "apc", "ppc", "prc", "pac"] {
            assert!(parse_tier(slug).is_ok(), "expected Ok for {slug}");
        }
    }

    #[test]
    fn parse_tier_accepts_uppercase() {
        for slug in &["GCI", "PCI", "APC", "PPC", "PRC", "PAC"] {
            assert!(parse_tier(slug).is_ok(), "expected Ok for {slug}");
        }
    }

    #[test]
    fn parse_tier_rejects_unknown() {
        for bad in &["unknown", "", "gc", "global", "../etc"] {
            assert!(parse_tier(bad).is_err(), "expected Err for '{bad}'");
        }
    }

    // ── validate_cascade_dir ─────────────────────────────────────────────────

    #[test]
    fn validate_rejects_dotdot_traversal() {
        // Path with ".." must be rejected before canonicalization.
        let result = validate_cascade_dir("/tmp/x/../y/.cascade");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains(".."), "expected '..' mention in: {msg}");
    }

    #[test]
    fn validate_rejects_nonexistent_path() {
        let result = validate_cascade_dir("/tmp/nonexistent-cascade-xyzzy/.cascade");
        assert!(result.is_err(), "expected Err for nonexistent path");
    }

    #[test]
    fn validate_rejects_dir_not_named_dot_cascade() {
        // /tmp exists but is not named ".cascade".
        let result = validate_cascade_dir("/tmp");
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains(".cascade"),
            "expected '.cascade' mention in: {msg}"
        );
    }

    #[test]
    fn validate_accepts_existing_dot_cascade_dir() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        let result = validate_cascade_dir(cascade_dir.to_str().unwrap());
        assert!(
            result.is_ok(),
            "expected Ok for real .cascade dir: {result:?}"
        );
    }

    // ── write_tier_content ────────────────────────────────────────────────────

    #[test]
    fn write_creates_cascade_md_when_absent() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let content = "# GCI\nApplies everywhere.";
        let result = write_tier_content(&cascade_dir, content).unwrap();

        assert!(result.written, "expected written=true");
        assert!(result.path.ends_with("CASCADE.md"));
        assert_eq!(fs::read_to_string(&result.path).unwrap(), content);
    }

    #[test]
    fn write_returns_noop_when_content_unchanged() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        let target = cascade_dir.join("CASCADE.md");

        let content = "# GCI\nSame content.";
        fs::write(&target, content).unwrap();

        let result = write_tier_content(&cascade_dir, content).unwrap();
        assert!(!result.written, "expected written=false (no-op)");
        assert!(result.path.ends_with("CASCADE.md"));
    }

    #[test]
    fn write_overwrites_changed_content() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        let target = cascade_dir.join("CASCADE.md");

        fs::write(&target, "# Old content").unwrap();
        let new_content = "# New content\n\nRevised.";
        let result = write_tier_content(&cascade_dir, new_content).unwrap();

        assert!(result.written, "expected written=true");
        assert_eq!(fs::read_to_string(&result.path).unwrap(), new_content);
    }

    // ── integration: validate + write pipeline ────────────────────────────────

    /// Tests the full validate → write pipeline: a well-formed tier_root and
    /// new content result in a successful, persisted write.
    #[test]
    fn full_pipeline_write_to_valid_cascade_dir() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let canon = validate_cascade_dir(cascade_dir.to_str().unwrap()).unwrap();
        let result = write_tier_content(&canon, "# GCI\nFull pipeline test.").unwrap();

        assert!(result.written);
        assert!(result.path.ends_with("CASCADE.md"));
        assert_eq!(
            fs::read_to_string(&result.path).unwrap(),
            "# GCI\nFull pipeline test."
        );
    }

    /// Tests that a path ending in something other than ".cascade" (even if it
    /// exists) is rejected — prevents out-of-tier writes.
    #[test]
    fn validate_rejects_out_of_tier_path() {
        let tmp = TempDir::new().unwrap();
        // The tmp dir itself exists but is not named ".cascade".
        let result = validate_cascade_dir(tmp.path().to_str().unwrap());
        assert!(result.is_err(), "expected Err for non-.cascade dir");
    }
}
