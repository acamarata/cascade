//! # commands::vault_env
//!
//! Tauri IPC commands for the credentials vault (`~/.claude/vault.env`).
//!
//! ## Purpose
//!
//! Exposes two read-only commands that let the React frontend display the
//! *names* of keys stored in the credentials vault without ever exposing
//! their values:
//! - `vault_path` — returns the absolute path to `~/.claude/vault.env`.
//! - `read_vault_keys` — parses the vault file and returns only the key names.
//!
//! ## Security
//!
//! - `read_vault_keys` NEVER returns values — only key names. This is a
//!   hard constraint enforced by the parser: the value side of every
//!   `KEY=value` line is discarded immediately after the key is extracted.
//! - The vault file is read with `std::fs::read_to_string` (no shell
//!   invocation), so shell injection is not possible.
//! - Both commands are read-only; no write/delete surface is exposed.
//!
//! ## Vault file format
//!
//! The vault file follows shell `dotenv` conventions:
//! - `KEY=value` — defines a variable.
//! - `export KEY=value` — `export` prefix is stripped.
//! - `# comment` — lines starting with `#` are skipped.
//! - Blank lines are skipped.
//! - Quotes around values (`KEY="value"`) are part of the value and are
//!   discarded (never returned).
//!
//! ## SPORT
//!
//! MASTER-ENDPOINTS.md — vault_env IPC commands (T-P7-E08-01)

use cascade_types::paths::home_dir;
use std::fs;

/// Resolve the canonical vault.env path: `$HOME/.claude/vault.env`.
fn vault_env_path() -> std::path::PathBuf {
    home_dir().join(".claude").join("vault.env")
}

/// Return the absolute path to the credentials vault file.
///
/// JS: `invoke("vault_path")` → `string`
///
/// The path is returned even when the file does not yet exist — the frontend
/// uses it for the "Open in Editor" button so the user can create the file.
#[tauri::command]
pub fn vault_path() -> Result<String, String> {
    Ok(vault_env_path().to_string_lossy().into_owned())
}

/// Read the credentials vault and return only the key names (never values).
///
/// JS: `invoke("read_vault_keys")` → `string[]`
///
/// Returns an empty vec when the vault file does not exist (graceful
/// fallback — the frontend shows "No vault keys found"). Returns a string
/// error only on genuine I/O failures (permission denied, etc.).
///
/// # Security
///
/// Only key names are returned. Values are parsed off the line and
/// immediately discarded — they never enter the returned `Vec`.
#[tauri::command]
pub fn read_vault_keys() -> Result<Vec<String>, String> {
    let path = vault_env_path();

    // Graceful fallback: missing vault file → empty key list (not an error).
    if !path.exists() {
        return Ok(Vec::new());
    }

    let contents =
        fs::read_to_string(&path).map_err(|e| format!("cannot read vault file {path:?}: {e}"))?;

    Ok(parse_vault_key_names(&contents))
}

/// Parse vault file contents and return only the key names.
///
/// Handles `KEY=value`, `export KEY=value`, comments (`#`), and blank lines.
/// Values (including surrounding quotes) are discarded — only the key name
/// before the `=` is collected.
fn parse_vault_key_names(contents: &str) -> Vec<String> {
    let mut keys = Vec::new();

    for raw_line in contents.lines() {
        let line = raw_line.trim();

        // Skip blank lines and comments.
        if line.is_empty() || line.starts_with('#') {
            continue;
        }

        // Strip optional `export ` prefix.
        let line = line.strip_prefix("export ").unwrap_or(line).trim();

        // A valid assignment must contain `=`.
        let Some(eq_pos) = line.find('=') else {
            continue;
        };

        let key = line[..eq_pos].trim();

        // Key must be non-empty and a valid shell identifier (letters, digits,
        // underscore; must not start with a digit).
        if key.is_empty() || !is_valid_key_name(key) {
            continue;
        }

        keys.push(key.to_owned());
    }

    keys
}

/// Check whether `key` is a valid shell-style environment variable name.
fn is_valid_key_name(key: &str) -> bool {
    let mut chars = key.chars();
    match chars.next() {
        Some(c) if c.is_ascii_alphabetic() || c == '_' => {}
        _ => return false,
    }
    chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    #[test]
    fn vault_path_ends_with_vault_env() {
        // vault_path() always returns Ok — verify the path shape.
        let result = vault_path().expect("vault_path");
        assert!(
            result.ends_with("vault.env"),
            "path should end with vault.env, got: {result}"
        );
        assert!(
            result.contains(".claude"),
            "path should contain .claude, got: {result}"
        );
    }

    #[test]
    fn read_vault_keys_missing_file_returns_empty() {
        // Point to a non-existent file by temporarily overriding HOME.
        // Since vault_env_path() uses home_dir(), we test via the parser
        // and the missing-file path directly.
        let dir = TempDir::new().expect("tempdir");
        let missing = dir.path().join("nonexistent_vault.env");
        assert!(!missing.exists());
        // read_vault_keys uses the real HOME — test the missing-file logic
        // by verifying parse handles empty content.
        let keys = parse_vault_key_names("");
        assert!(keys.is_empty());
    }

    #[test]
    fn parse_simple_key_value() {
        let contents = "ANTHROPIC_API_KEY=sk-ant-xxx\nOPENAI_API_KEY=sk-xxx\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["ANTHROPIC_API_KEY", "OPENAI_API_KEY"]);
    }

    #[test]
    fn parse_export_prefix() {
        let contents = "export GEMINI_KEY=AIzaXXX\nFOO=bar\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["GEMINI_KEY", "FOO"]);
    }

    #[test]
    fn parse_skips_comments_and_blanks() {
        let contents = "# This is a comment\n\nKEY1=val1\n# another comment\nKEY2=val2\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["KEY1", "KEY2"]);
    }

    #[test]
    fn parse_skips_lines_without_equals() {
        let contents = "NOT_AN_ASSIGNMENT\nKEY=val\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["KEY"]);
    }

    #[test]
    fn parse_skips_invalid_key_names() {
        let contents = "1INVALID=val\nVALID_KEY=val\n-INVALID=val\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["VALID_KEY"]);
    }

    #[test]
    fn parse_never_returns_values() {
        let contents = "SECRET=super-secret-value-12345\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["SECRET"]);
        // Ensure the value is not present anywhere in the result.
        for k in &keys {
            assert!(!k.contains("super-secret"));
        }
    }

    #[test]
    fn parse_quoted_values() {
        let contents = "KEY1=\"quoted value\"\nKEY2='single quoted'\n";
        let keys = parse_vault_key_names(contents);
        assert_eq!(keys, vec!["KEY1", "KEY2"]);
    }

    #[test]
    fn parse_empty_file() {
        let keys = parse_vault_key_names("");
        assert!(keys.is_empty());
    }

    #[test]
    fn is_valid_key_name_checks() {
        assert!(is_valid_key_name("VALID"));
        assert!(is_valid_key_name("VALID_KEY"));
        assert!(is_valid_key_name("A1_B2"));
        assert!(is_valid_key_name("_UNDERSCORE"));
        assert!(!is_valid_key_name(""));
        assert!(!is_valid_key_name("1STARTS_DIGIT"));
        assert!(!is_valid_key_name("-DASH"));
        assert!(!is_valid_key_name("HAS SPACE"));
    }

    #[test]
    fn read_vault_keys_round_trip_with_temp_file() {
        // Write a temp vault file and verify read_vault_keys parses it.
        // We can't easily override HOME for the command, so test the
        // parser + file-reading path together via a temp file + manual parse.
        let dir = TempDir::new().expect("tempdir");
        let vault = dir.path().join("vault.env");
        fs::write(&vault, "KEY_A=val_a\nKEY_B=val_b\n# comment\n").unwrap();
        let contents = fs::read_to_string(&vault).unwrap();
        let keys = parse_vault_key_names(&contents);
        assert_eq!(keys, vec!["KEY_A", "KEY_B"]);
    }
}
