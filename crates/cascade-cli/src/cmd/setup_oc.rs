//! `cascade setup-oc` — idempotent OpenCode MCP + instructions wiring.
//!
//! ## Purpose
//! Configures OpenCode to use the Cascade MCP server and injects a
//! per-project `opencode-instructions.md` that tells OC to call
//! `cascade.search` and `cascade.context_slice` before answering codebase
//! questions. Complements `cascade mcp setup --tool opencode` (which only
//! wires the MCP entry). This command also writes the instructions file and
//! the `instructions` field into the per-project opencode.json.
//!
//! ## Subcommand surface
//! `cascade setup-oc [--dry-run] [--repo <path>]`
//!
//! ## Inputs
//! - `~/.config/opencode/opencode.json` — global OC config (MCP merge target)
//! - `{repo}/opencode.json` — per-project OC config (`instructions` field target)
//! - `{repo}/.cascade/opencode-instructions.md` — written by this command
//!
//! ## Outputs
//! - Global `~/.config/opencode/opencode.json` updated: `mcpServers.cascade` entry
//! - Per-project `opencode.json` updated: `instructions` field set to path
//! - `{repo}/.cascade/opencode-instructions.md` created with preamble
//!
//! ## Constraints
//! - Atomic writes: temp → rename; never direct overwrite
//! - Non-destructive: existing keys preserved; cascade entry upserted
//! - Idempotent: running twice produces identical output
//! - HOME-confined: all paths derived from home_dir()
//!
//! ## SPORT
//! MASTER-CLI.md: cascade setup-oc

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
};
use clap::Args;
use serde_json::{json, Value};

use super::Command;

// ── OC instructions preamble ──────────────────────────────────────────────────

/// The preamble written to `{repo}/.cascade/opencode-instructions.md`.
/// OC reads this file at session start via the `instructions` field in
/// opencode.json.
pub const OC_INSTRUCTIONS_PREAMBLE: &str = "\
# Cascade Context — OpenCode Session Instructions

You are operating inside a repository managed by the **Cascade AI context
framework** (https://github.com/acamarata/cascade).

## Required workflow before answering codebase questions

1. Call `cascade.search` with the user's query to retrieve semantically
   relevant context chunks from the Cascade RAG index.
2. Call `cascade.context_slice` to get the merged instruction cascade
   (GCI→PCI→APC→PPC→PRC→PAC) for the current working directory.
3. Incorporate the returned context into your answer.

This ensures your responses reflect the project's conventions, decisions, and
architecture as indexed by Cascade — not just what is visible in the open
files.
";

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade setup-oc`.
#[derive(Debug, Args)]
pub struct SetupOcArgs {
    /// Print what would be written without modifying any files.
    #[arg(long)]
    pub dry_run: bool,

    /// Project repo directory. Defaults to the current working directory.
    #[arg(long, value_name = "PATH")]
    pub repo: Option<PathBuf>,
}

// ── Helpers (re-exported for tests) ──────────────────────────────────────────

/// Path to the global OpenCode config file.
pub fn global_opencode_json_path() -> PathBuf {
    home_dir()
        .join(".config")
        .join("opencode")
        .join("opencode.json")
}

/// Path to the per-project opencode.json.
pub fn project_opencode_json_path(repo: &Path) -> PathBuf {
    repo.join("opencode.json")
}

/// Path to the per-project OC instructions file.
pub fn oc_instructions_path(repo: &Path) -> PathBuf {
    repo.join(".cascade").join("opencode-instructions.md")
}

/// Read a JSON file or return `{}` if absent.
pub fn read_json_or_empty(path: &Path) -> Result<Value> {
    if !path.exists() {
        return Ok(json!({}));
    }
    let raw = std::fs::read_to_string(path)
        .map_err(|e| CascadeError::Other(format!("failed to read {}: {e}", path.display())))?;
    serde_json::from_str(&raw).map_err(|e| {
        CascadeError::Other(format!("failed to parse JSON {}: {e}", path.display()))
    })
}

/// Atomically write pretty-printed JSON (write to `.tmp`, rename).
pub fn write_json_atomic(path: &Path, value: &Value, dry_run: bool) -> Result<()> {
    let pretty = serde_json::to_string_pretty(value)
        .map_err(|e| CascadeError::Other(format!("JSON serialise error: {e}")))?;

    if dry_run {
        println!("--- (dry-run) would write: {} ---", path.display());
        println!("{pretty}");
        println!("--- end ---");
        return Ok(());
    }

    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| {
            CascadeError::Other(format!("cannot create dir {}: {e}", parent.display()))
        })?;
    }

    // Backup existing file (best-effort, once)
    if path.exists() {
        let bak = path.with_extension("json.bak");
        if !bak.exists() {
            let _ = std::fs::copy(path, &bak);
        }
    }

    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, pretty.as_bytes())
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {e}", tmp.display())))?;
    std::fs::rename(&tmp, path)
        .map_err(|e| CascadeError::Other(format!("rename to {}: {e}", path.display())))?;
    Ok(())
}

/// Atomically write text to a file.
pub fn write_text_atomic(path: &Path, content: &str, dry_run: bool) -> Result<()> {
    if dry_run {
        println!("--- (dry-run) would write: {} ---", path.display());
        println!("{content}");
        println!("--- end ---");
        return Ok(());
    }

    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| {
            CascadeError::Other(format!("cannot create dir {}: {e}", parent.display()))
        })?;
    }

    let tmp = path.with_extension("md.tmp");
    std::fs::write(&tmp, content.as_bytes())
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {e}", tmp.display())))?;
    std::fs::rename(&tmp, path)
        .map_err(|e| CascadeError::Other(format!("rename to {}: {e}", path.display())))?;
    Ok(())
}

// ── Core logic (exported for `generate-instructions` OC path) ─────────────────

/// Upsert the cascade MCP server entry in the global `~/.config/opencode/opencode.json`.
///
/// The cascade entry uses `type: "stdio"` and `command: "cascade"` with
/// `args: ["mcp", "--stdio"]`. Any existing entry named "cascade" is replaced;
/// all other entries are preserved.
///
/// Returns `true` if the config was modified (or would be in dry-run mode).
pub fn upsert_global_mcp_entry(dry_run: bool) -> Result<bool> {
    let path = global_opencode_json_path();

    let mut config = read_json_or_empty(&path)?;

    let cascade_entry = json!({
        "type": "stdio",
        "command": "cascade",
        "args": ["mcp", "--stdio"]
    });

    // opencode.json mcpServers is an object keyed by name (same as CC format).
    let top = config
        .as_object_mut()
        .ok_or_else(|| CascadeError::Other("opencode.json root is not an object".into()))?;
    let servers = top
        .entry("mcpServers")
        .or_insert_with(|| json!({}));
    let servers_obj = servers
        .as_object_mut()
        .ok_or_else(|| CascadeError::Other("mcpServers is not an object".into()))?;

    let old = servers_obj.get("cascade").cloned();
    servers_obj.insert("cascade".to_string(), cascade_entry.clone());
    let changed = old.as_ref() != Some(&cascade_entry);

    write_json_atomic(&path, &config, dry_run)?;
    Ok(changed)
}

/// Write `{repo}/.cascade/opencode-instructions.md` and update
/// `{repo}/opencode.json` with `"instructions": ".cascade/opencode-instructions.md"`.
///
/// Both operations are idempotent and non-destructive.
pub fn inject_oc_instructions(repo: &Path, dry_run: bool) -> Result<()> {
    // 1. Write the instructions file
    let instructions_file = oc_instructions_path(repo);
    write_text_atomic(&instructions_file, OC_INSTRUCTIONS_PREAMBLE, dry_run)?;

    // 2. Update per-project opencode.json with the instructions field
    let project_json = project_opencode_json_path(repo);
    let mut config = read_json_or_empty(&project_json)?;

    let top = config
        .as_object_mut()
        .ok_or_else(|| CascadeError::Other("project opencode.json root is not an object".into()))?;
    top.insert(
        "instructions".to_string(),
        json!(".cascade/opencode-instructions.md"),
    );

    write_json_atomic(&project_json, &config, dry_run)?;
    Ok(())
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for SetupOcArgs {
    async fn run(&self) -> Result<()> {
        let repo = match &self.repo {
            Some(p) => p.clone(),
            None => std::env::current_dir()
                .map_err(|e| CascadeError::Other(format!("cannot get cwd: {e}")))?,
        };

        // Step 1: upsert global MCP entry
        let changed = upsert_global_mcp_entry(self.dry_run)?;
        if !self.dry_run {
            if changed {
                println!("cascade MCP server configured in opencode.json");
            } else {
                println!("cascade MCP server already configured in opencode.json (no change)");
            }
        }

        // Step 2: inject per-project instructions
        inject_oc_instructions(&repo, self.dry_run)?;
        if !self.dry_run {
            println!(
                "OC instructions written to {}",
                oc_instructions_path(&repo).display()
            );
            println!(
                "Per-project opencode.json updated with instructions field ({})",
                project_opencode_json_path(&repo).display()
            );
        }

        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    fn temp_home() -> TempDir {
        TempDir::new().expect("tempdir")
    }

    // ── upsert_global_mcp_entry ───────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn setup_oc_global_creates_mcp_entry() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Pre-create the opencode config dir
        let oc_dir = tmp.path().join(".config").join("opencode");
        std::fs::create_dir_all(&oc_dir).unwrap();

        let result = upsert_global_mcp_entry(false);
        assert!(result.is_ok(), "should succeed: {result:?}");

        let path = global_opencode_json_path();
        let raw = std::fs::read_to_string(&path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();

        assert_eq!(
            parsed["mcpServers"]["cascade"]["type"],
            json!("stdio"),
            "type must be stdio"
        );
        assert_eq!(
            parsed["mcpServers"]["cascade"]["command"],
            json!("cascade"),
            "command must be cascade"
        );
        assert_eq!(
            parsed["mcpServers"]["cascade"]["args"],
            json!(["mcp", "--stdio"]),
            "args must match"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_oc_idempotent_global_mcp() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let oc_dir = tmp.path().join(".config").join("opencode");
        std::fs::create_dir_all(&oc_dir).unwrap();

        upsert_global_mcp_entry(false).unwrap();
        let changed2 = upsert_global_mcp_entry(false).unwrap();
        assert!(!changed2, "second run must report no change");

        let path = global_opencode_json_path();
        let raw = std::fs::read_to_string(&path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        let count = parsed["mcpServers"]
            .as_object()
            .unwrap()
            .keys()
            .filter(|k| *k == "cascade")
            .count();
        assert_eq!(count, 1, "exactly one cascade entry after two runs");
    }

    #[test]
    #[serial(global_env)]
    fn setup_oc_preserves_other_mcp_servers() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let oc_dir = tmp.path().join(".config").join("opencode");
        std::fs::create_dir_all(&oc_dir).unwrap();

        // Pre-populate with another server
        let initial = json!({
            "mcpServers": {
                "other-tool": { "type": "stdio", "command": "other" }
            },
            "theme": "dark"
        });
        let path = global_opencode_json_path();
        std::fs::write(&path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        upsert_global_mcp_entry(false).unwrap();

        let raw = std::fs::read_to_string(&path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        assert_eq!(
            parsed["mcpServers"]["other-tool"]["command"],
            json!("other"),
            "other server must be preserved"
        );
        assert_eq!(parsed["theme"], json!("dark"), "other keys must be preserved");
        assert_eq!(
            parsed["mcpServers"]["cascade"]["type"],
            json!("stdio"),
            "cascade entry must be present"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_oc_dry_run_does_not_write() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // No config dir — dry-run should still not create it
        upsert_global_mcp_entry(true).unwrap();
        assert!(
            !global_opencode_json_path().exists(),
            "dry-run must not create file"
        );
    }

    // ── inject_oc_instructions ────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn inject_oc_instructions_writes_preamble() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let repo = tmp.path().join("myrepo");
        std::fs::create_dir_all(&repo).unwrap();

        inject_oc_instructions(&repo, false).unwrap();

        let instructions_path = oc_instructions_path(&repo);
        assert!(instructions_path.exists(), "instructions file must be created");

        let content = std::fs::read_to_string(&instructions_path).unwrap();
        assert!(
            content.contains("cascade.search"),
            "preamble must mention cascade.search"
        );
        assert!(
            content.contains("cascade.context_slice"),
            "preamble must mention cascade.context_slice"
        );
    }

    #[test]
    #[serial(global_env)]
    fn inject_oc_instructions_updates_project_opencode_json() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let repo = tmp.path().join("myrepo");
        std::fs::create_dir_all(&repo).unwrap();

        inject_oc_instructions(&repo, false).unwrap();

        let project_json_path = project_opencode_json_path(&repo);
        assert!(project_json_path.exists(), "project opencode.json must be created");

        let raw = std::fs::read_to_string(&project_json_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        assert_eq!(
            parsed["instructions"],
            json!(".cascade/opencode-instructions.md"),
            "instructions field must point to the generated file"
        );
    }

    #[test]
    #[serial(global_env)]
    fn inject_oc_instructions_preserves_existing_project_json_keys() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let repo = tmp.path().join("myrepo");
        std::fs::create_dir_all(&repo).unwrap();

        // Pre-populate project opencode.json
        let initial = json!({ "model": "claude-sonnet-4-5", "theme": "dark" });
        let project_json_path = project_opencode_json_path(&repo);
        std::fs::write(
            &project_json_path,
            serde_json::to_string_pretty(&initial).unwrap(),
        )
        .unwrap();

        inject_oc_instructions(&repo, false).unwrap();

        let raw = std::fs::read_to_string(&project_json_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        assert_eq!(parsed["model"], json!("claude-sonnet-4-5"), "model must be preserved");
        assert_eq!(parsed["theme"], json!("dark"), "theme must be preserved");
        assert_eq!(
            parsed["instructions"],
            json!(".cascade/opencode-instructions.md"),
            "instructions field must be set"
        );
    }

    #[test]
    #[serial(global_env)]
    fn inject_oc_instructions_idempotent() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let repo = tmp.path().join("myrepo");
        std::fs::create_dir_all(&repo).unwrap();

        inject_oc_instructions(&repo, false).unwrap();
        inject_oc_instructions(&repo, false).unwrap();

        let raw =
            std::fs::read_to_string(project_opencode_json_path(&repo)).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        // instructions field should appear exactly once (it's a single JSON key)
        assert_eq!(
            parsed["instructions"],
            json!(".cascade/opencode-instructions.md"),
        );
    }

    #[test]
    #[serial(global_env)]
    fn inject_oc_instructions_dry_run_no_write() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let repo = tmp.path().join("myrepo");
        std::fs::create_dir_all(&repo).unwrap();

        inject_oc_instructions(&repo, true).unwrap();

        assert!(
            !oc_instructions_path(&repo).exists(),
            "dry-run must not write instructions file"
        );
        assert!(
            !project_opencode_json_path(&repo).exists(),
            "dry-run must not write project opencode.json"
        );
    }
}
