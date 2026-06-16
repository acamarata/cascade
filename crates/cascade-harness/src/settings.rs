//! Harness settings writer — idempotent, non-destructive merge into harness
//! runtime config files (e.g. `~/.claude/settings.json`).
//!
//! Purpose: Let Cascade write a managed block of env vars and deny-list
//!   patterns into a harness's live config without clobbering user-authored
//!   content.
//!
//! Inputs:  `HarnessTarget` (which harness), optional path override, dry-run flag.
//! Outputs: Written settings file (or diff-only in dry-run mode).
//!
//! Constraints:
//!   - JSON round-trips preserve ALL unknown keys.
//!   - Managed content is isolated under a `"_cascade_managed"` sub-object;
//!     user content outside that key is never deleted or modified.
//!   - Writes use a temp-file + atomic rename; no partial-write corruption.
//!   - Idempotent: running twice produces the same file as running once.
//!
//! SPORT: MASTER-CLI.md: cascade configure (P13)

use std::fs;
use std::io::Write as _;
use std::path::{Path, PathBuf};

use serde_json::{Map, Value};
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

use crate::policy::patterns::all_patterns;

// ── Public types ──────────────────────────────────────────────────────────────

/// Supported harness targets for `cascade configure`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HarnessTarget {
    /// Claude Code — writes `~/.claude/settings.json`.
    ClaudeCode,
}

impl HarnessTarget {
    /// Human-readable name used in output.
    pub fn display_name(self) -> &'static str {
        match self {
            HarnessTarget::ClaudeCode => "claude-code",
        }
    }

    /// Default path to the settings file for this harness.
    pub fn default_settings_path(self) -> Option<PathBuf> {
        match self {
            HarnessTarget::ClaudeCode => {
                dirs::home_dir().map(|h| h.join(".claude").join("settings.json"))
            }
        }
    }
}

/// Result of a configure operation.
#[derive(Debug)]
pub struct ConfigureResult {
    /// Path that was written (or would be written in dry-run).
    pub path: PathBuf,
    /// Human-readable diff of the changes applied.
    pub diff: String,
    /// Whether the file was actually modified on disk.
    pub wrote: bool,
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Configure a harness settings file by merging Cascade-managed values.
///
/// When `dry_run` is `true`, the function computes what would change but does
/// not touch the file on disk.  When `false`, it writes atomically.
///
/// The managed block lives under the `"_cascade_managed"` top-level key so it
/// can be updated or removed cleanly without affecting user content.
pub fn configure_harness(
    target: HarnessTarget,
    path_override: Option<&Path>,
    dry_run: bool,
) -> Result<ConfigureResult> {
    let path = match path_override {
        Some(p) => p.to_path_buf(),
        None => target
            .default_settings_path()
            .ok_or_else(|| CascadeError::Other("could not determine home directory".into()))?,
    };

    // Read existing content (absent file → empty object).
    let existing_json = read_or_empty(&path)?;

    // Build the new managed block.
    let managed = build_managed_block(target);

    // Merge: clone existing, inject managed block.
    let mut merged = existing_json.clone();
    if let Value::Object(ref mut map) = merged {
        map.insert("_cascade_managed".into(), managed.clone());
    }

    // Compute a human-readable diff.
    let before = serde_json::to_string_pretty(&existing_json).unwrap_or_default();
    let after = serde_json::to_string_pretty(&merged).unwrap_or_default();
    let diff = build_diff(&before, &after);

    let wrote = if dry_run {
        false
    } else if before == after {
        // Nothing changed — skip the write to avoid a spurious mtime bump.
        debug!(path = %path.display(), "settings unchanged, skipping write");
        false
    } else {
        write_atomic(&path, &after)?;
        true
    };

    Ok(ConfigureResult { path, diff, wrote })
}

/// Return the deny-list shell-glob strings Cascade would write to a harness.
///
/// Each entry is the literal pattern substring from the built-in policy
/// engine.  Harness deny-lists use glob-style matching, so the patterns are
/// surfaced verbatim; they remain effective as substring matchers inside
/// the harness.
pub fn deny_patterns() -> Vec<String> {
    all_patterns().map(|(p, _)| p.to_owned()).collect()
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Read the JSON file at `path`, or return an empty object if it is absent.
fn read_or_empty(path: &Path) -> Result<Value> {
    if !path.exists() {
        return Ok(Value::Object(Map::new()));
    }
    let raw = fs::read_to_string(path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "read",
        source: e,
    })?;
    if raw.trim().is_empty() {
        return Ok(Value::Object(Map::new()));
    }
    serde_json::from_str(&raw).map_err(|e| {
        CascadeError::Other(format!(
            "could not parse {} as JSON: {e}",
            path.display()
        ))
    })
}

/// Build the `_cascade_managed` block for the given target.
fn build_managed_block(target: HarnessTarget) -> Value {
    let mut block = Map::new();
    block.insert(
        "version".into(),
        Value::String(env!("CARGO_PKG_VERSION").into()),
    );
    block.insert(
        "harness".into(),
        Value::String(target.display_name().into()),
    );

    // env block — cascade-relevant runtime vars.
    let mut env = Map::new();
    env.insert(
        "CASCADE_MANAGED".into(),
        Value::String("1".into()),
    );
    block.insert("env".into(), Value::Object(env));

    // permissions.deny — derived from the built-in policy engine.
    let patterns: Vec<Value> = deny_patterns()
        .into_iter()
        .map(Value::String)
        .collect();
    let mut permissions = Map::new();
    permissions.insert("deny".into(), Value::Array(patterns));
    block.insert("permissions".into(), Value::Object(permissions));

    Value::Object(block)
}

/// Build a simple line-diff string between `before` and `after`.
fn build_diff(before: &str, after: &str) -> String {
    if before == after {
        return "(no changes)".into();
    }

    let mut lines = Vec::new();
    let before_lines: Vec<&str> = before.lines().collect();
    let after_lines: Vec<&str> = after.lines().collect();

    // Removed lines.
    for l in &before_lines {
        if !after_lines.contains(l) {
            lines.push(format!("- {l}"));
        }
    }
    // Added lines.
    for l in &after_lines {
        if !before_lines.contains(l) {
            lines.push(format!("+ {l}"));
        }
    }

    if lines.is_empty() {
        "(no changes)".into()
    } else {
        lines.join("\n")
    }
}

/// Write `content` to `path` atomically via a temp file + rename.
fn write_atomic(path: &Path, content: &str) -> Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir",
            source: e,
        })?;
    }
    let tmp = path.with_extension("json.tmp");
    {
        let mut f = fs::File::create(&tmp).map_err(|e| CascadeError::Io {
            path: tmp.clone(),
            operation: "create_tmp",
            source: e,
        })?;
        f.write_all(content.as_bytes()).map_err(|e| CascadeError::Io {
            path: tmp.clone(),
            operation: "write",
            source: e,
        })?;
    }
    fs::rename(&tmp, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename",
        source: e,
    })?;
    debug!(path = %path.display(), "wrote settings file");
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use tempfile::TempDir;

    fn settings_path(dir: &TempDir) -> PathBuf {
        dir.path().join("settings.json")
    }

    // Helper: run configure pointing at a temp dir.
    fn configure(dir: &TempDir, dry_run: bool) -> ConfigureResult {
        configure_harness(
            HarnessTarget::ClaudeCode,
            Some(&settings_path(dir)),
            dry_run,
        )
        .expect("configure_harness failed")
    }

    #[test]
    fn test_writes_managed_block_into_empty_file() {
        let dir = TempDir::new().unwrap();
        let result = configure(&dir, false);
        assert!(result.wrote, "should have written a new file");

        let content = fs::read_to_string(&result.path).unwrap();
        let parsed: Value = serde_json::from_str(&content).unwrap();
        let managed = parsed.get("_cascade_managed").expect("_cascade_managed key");
        assert_eq!(managed["harness"], json!("claude-code"));
        assert!(managed["permissions"]["deny"].is_array());
        assert!(!managed["permissions"]["deny"].as_array().unwrap().is_empty());
    }

    #[test]
    fn test_idempotent_no_duplication() {
        let dir = TempDir::new().unwrap();
        configure(&dir, false);
        configure(&dir, false); // second run

        let content = fs::read_to_string(settings_path(&dir)).unwrap();
        let parsed: Value = serde_json::from_str(&content).unwrap();

        // _cascade_managed must appear exactly once (no array duplication).
        let obj = parsed.as_object().unwrap();
        let count = obj.keys().filter(|k| *k == "_cascade_managed").count();
        assert_eq!(count, 1, "_cascade_managed should appear exactly once");

        // deny list should not be doubled.
        let deny = parsed["_cascade_managed"]["permissions"]["deny"]
            .as_array()
            .unwrap();
        let expected_len = deny_patterns().len();
        assert_eq!(deny.len(), expected_len);
    }

    #[test]
    fn test_preserves_user_keys() {
        let dir = TempDir::new().unwrap();
        let path = settings_path(&dir);

        // Write a pre-existing user config.
        let user_config = json!({
            "theme": "dark",
            "userKey": "do-not-touch",
            "preferences": { "vim": true }
        });
        fs::write(&path, serde_json::to_string_pretty(&user_config).unwrap()).unwrap();

        configure(&dir, false);

        let content = fs::read_to_string(&path).unwrap();
        let parsed: Value = serde_json::from_str(&content).unwrap();

        assert_eq!(parsed["theme"], json!("dark"), "theme preserved");
        assert_eq!(parsed["userKey"], json!("do-not-touch"), "userKey preserved");
        assert_eq!(parsed["preferences"]["vim"], json!(true), "preferences preserved");
        assert!(parsed.get("_cascade_managed").is_some(), "managed block added");
    }

    #[test]
    fn test_dry_run_writes_nothing() {
        let dir = TempDir::new().unwrap();
        let path = settings_path(&dir);

        let result = configure(&dir, true);
        assert!(!result.wrote, "dry-run must not write");
        assert!(!path.exists(), "file must not exist after dry-run");
    }

    #[test]
    fn test_dry_run_on_existing_file_writes_nothing() {
        let dir = TempDir::new().unwrap();
        let path = settings_path(&dir);

        // Write initial file.
        configure(&dir, false);
        let before = fs::read_to_string(&path).unwrap();

        // Modify it on disk to simulate a change.
        let mut parsed: Value = serde_json::from_str(&before).unwrap();
        parsed.as_object_mut().unwrap().remove("_cascade_managed");
        fs::write(&path, serde_json::to_string_pretty(&parsed).unwrap()).unwrap();
        let modified = fs::read_to_string(&path).unwrap();

        // Dry-run should not restore it.
        let result = configure(&dir, true);
        assert!(!result.wrote);
        let after = fs::read_to_string(&path).unwrap();
        assert_eq!(modified, after, "dry-run must not change file");
    }

    #[test]
    fn test_second_run_unchanged_skips_write() {
        let dir = TempDir::new().unwrap();
        configure(&dir, false);
        // Second run: content is identical, wrote should be false.
        let result = configure(&dir, false);
        assert!(!result.wrote, "second run with no changes must not write");
    }

    #[test]
    fn test_deny_patterns_non_empty() {
        let patterns = deny_patterns();
        assert!(!patterns.is_empty());
        // Spot-check a well-known dangerous pattern.
        assert!(
            patterns.iter().any(|p| p.contains("rm -rf /")),
            "expected rm -rf / in deny patterns"
        );
    }
}
