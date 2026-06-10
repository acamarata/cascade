//! `cascade generate-instructions` — harness-native instruction file generator.
//!
//! Consumes a [`ResolvedCascade`] (from T-P4-E02-26) and writes harness-native
//! instruction files so that Claude Code (CC) and OpenCode (OC) automatically
//! use Cascade MCP + RAG on next startup.
//!
//! ## CC output (per found tier)
//!
//! - `{tier_path}/.claude/CLAUDE.md` — header block + tier instruction text.
//!   Idempotent: skips if the cascade header is already present.
//! - `{tier_path}/.claude/AGENTS.md` — symlink to CLAUDE.md (OC/CC compat).
//! - `{tier_path}/.claude/settings.json` — MCP server entry added (additive).
//!
//! ## OC output (per found tier)
//!
//! - `~/.config/opencode/opencode.json` — MCP server entry added (additive).
//! - `{tier_path}/.cascade/opencode-instructions.md` — OC-specific preamble.
//!
//! ## Flags
//!
//! - `--harness [cc|oc|both]` — default: both.
//! - `--dry-run` — print unified diff without writing.
//! - `--tier [gci|pci|apc|ppc|prc|pac|all]` — default: all found tiers.
//!
//! ## Hard rule compliance
//!
//! Generated CLAUDE.md content follows GCI writing rules (no AI attribution,
//! no AI tells). The cascade header marker ensures idempotency.
//!
//! ## SPORT
//!
//! MASTER-CLI.md — cascade generate-instructions (T-P4-E02-27)

use std::fs;
use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_core::cascade_resolution::{resolve_cascade_full, TierResult};
use cascade_types::cascade_tier::CascadeTier;
use cascade_types::error::{CascadeError, Result};
use clap::Args;
use serde_json::Value;

use super::Command;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Idempotency marker — if this string appears in CLAUDE.md, skip injection.
const CASCADE_HEADER_MARKER: &str = "<!-- cascade:generate-instructions -->";
const CASCADE_HEADER_CLOSE: &str = "<!-- /cascade:generate-instructions -->";

/// OC opencode.json MCP server entry name.
const CASCADE_MCP_NAME: &str = "cascade";

// ── Args ──────────────────────────────────────────────────────────────────────

/// Which harness to generate files for.
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum Harness {
    /// Claude Code (CLAUDE.md + settings.json).
    Cc,
    /// OpenCode (opencode.json + opencode-instructions.md).
    Oc,
    /// Both CC and OC.
    Both,
}

/// Arguments for `cascade generate-instructions`.
#[derive(Debug, Args)]
pub struct GenerateInstructionsArgs {
    /// Harness to generate files for.
    #[arg(long, value_enum, default_value = "both")]
    pub harness: Harness,

    /// Only generate for this tier. Defaults to all found tiers.
    #[arg(long)]
    pub tier: Option<String>,

    /// Project path. Defaults to the current working directory.
    #[arg(long)]
    pub project: Option<PathBuf>,

    /// Print a diff of what would be written without modifying any files.
    #[arg(long)]
    pub dry_run: bool,
}

#[async_trait]
impl Command for GenerateInstructionsArgs {
    async fn run(&self) -> Result<()> {
        let cwd = self
            .project
            .clone()
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."));

        let resolved = resolve_cascade_full(&cwd).map_err(|e| CascadeError::Io {
            path: cwd.clone(),
            operation: "resolve cascade for generate-instructions",
            source: std::io::Error::new(std::io::ErrorKind::Other, e.to_string()),
        })?;

        // Filter tiers by --tier flag
        let tiers_to_process: Vec<&TierResult> = if let Some(tier_str) = &self.tier {
            if tier_str == "all" {
                resolved.tiers_found.iter().filter(|t| t.found).collect()
            } else {
                let target: CascadeTier = tier_str.parse().map_err(|_| CascadeError::Io {
                    path: cwd.clone(),
                    operation: "parse --tier argument",
                    source: std::io::Error::new(
                        std::io::ErrorKind::InvalidInput,
                        format!("unknown tier: {tier_str}"),
                    ),
                })?;
                resolved
                    .tiers_found
                    .iter()
                    .filter(|t| t.found && t.tier == target)
                    .collect()
            }
        } else {
            resolved.tiers_found.iter().filter(|t| t.found).collect()
        };

        if tiers_to_process.is_empty() {
            eprintln!("No cascade tiers found. Run `cascade init` to scaffold a .cascade/ directory.");
            return Ok(());
        }

        let mut any_written = false;

        for tier_result in &tiers_to_process {
            // Compute the tier root: parent of the path_searched (.cascade/ dir)
            let cascade_dir = &tier_result.path_searched;
            let tier_root = if cascade_dir.ends_with(".cascade") {
                cascade_dir.parent().map(|p| p.to_path_buf())
            } else if cascade_dir.is_file() {
                // path is a file (CASCADE.md); go up to .cascade parent then tier root
                cascade_dir
                    .parent()
                    .and_then(|p| p.parent())
                    .map(|p| p.to_path_buf())
            } else {
                cascade_dir.parent().map(|p| p.to_path_buf())
            };

            let Some(tier_root) = tier_root else {
                eprintln!(
                    "warning: cannot determine tier root for {:?}, skipping",
                    cascade_dir
                );
                continue;
            };

            match self.harness {
                Harness::Cc => {
                    any_written |= generate_cc(
                        tier_result,
                        &tier_root,
                        &resolved.mcp_server_url,
                        self.dry_run,
                    )?;
                }
                Harness::Oc => {
                    any_written |= generate_oc(tier_result, &tier_root, self.dry_run)?;
                }
                Harness::Both => {
                    any_written |= generate_cc(
                        tier_result,
                        &tier_root,
                        &resolved.mcp_server_url,
                        self.dry_run,
                    )?;
                    any_written |= generate_oc(tier_result, &tier_root, self.dry_run)?;
                }
            }
        }

        if !any_written && !self.dry_run {
            println!("All targets already up to date.");
        }

        Ok(())
    }
}

// ── CC generation ─────────────────────────────────────────────────────────────

/// Generate CC harness files for one tier: CLAUDE.md, AGENTS.md, settings.json.
///
/// Returns true if any file was written (or would be written in dry-run).
fn generate_cc(
    tier: &TierResult,
    tier_root: &Path,
    mcp_server_url: &str,
    dry_run: bool,
) -> Result<bool> {
    let claude_dir = tier_root.join(".claude");
    let claude_md = claude_dir.join("CLAUDE.md");
    let agents_md = claude_dir.join("AGENTS.md");
    let settings_json = claude_dir.join("settings.json");

    let mut any_written = false;

    // 1. CLAUDE.md — append header block + tier instructions (idempotent)
    let header_block = render_cc_header(tier, mcp_server_url);
    let existing_claude = if claude_md.exists() {
        fs::read_to_string(&claude_md).map_err(|e| CascadeError::Io {
            path: claude_md.clone(),
            operation: "read CLAUDE.md",
            source: e,
        })?
    } else {
        String::new()
    };

    if existing_claude.contains(CASCADE_HEADER_MARKER) {
        // Already injected — idempotent skip
        if dry_run {
            println!(
                "[dry-run] {}: cascade header already present, skipping",
                claude_md.display()
            );
        }
    } else {
        let new_content = if existing_claude.is_empty() {
            format!("{header_block}\n")
        } else {
            let sep = if existing_claude.ends_with('\n') { "\n" } else { "\n\n" };
            format!("{existing_claude}{sep}{header_block}\n")
        };

        if dry_run {
            print_diff(&claude_md, &existing_claude, &new_content);
        } else {
            ensure_dir(&claude_dir)?;
            atomic_write(&claude_md, &new_content)?;
            println!("wrote: {}", claude_md.display());
        }
        any_written = true;
    }

    // 2. AGENTS.md — symlink to CLAUDE.md (idempotent)
    if !agents_md.exists() && !agents_md.is_symlink() {
        if dry_run {
            println!(
                "[dry-run] would create symlink: {} -> CLAUDE.md",
                agents_md.display()
            );
        } else {
            ensure_dir(&claude_dir)?;
            create_agents_symlink(&agents_md)?;
            println!("created symlink: {}", agents_md.display());
        }
        any_written = true;
    }

    // 3. settings.json — add cascade MCP server entry (additive)
    let settings_updated = update_cc_settings_json(&settings_json, dry_run)?;
    any_written |= settings_updated;

    Ok(any_written)
}

/// Render the CC CLAUDE.md header block for a tier.
///
/// The block is wrapped in cascade markers for idempotency detection.
fn render_cc_header(tier: &TierResult, mcp_server_url: &str) -> String {
    let tier_name = tier.tier.acronym().to_uppercase();
    let tier_desc = tier.tier.description();
    let instr = tier.instructions.trim();

    let mut block = format!(
        "{marker}\n\
         ## Cascade Context — {tier_name} Tier ({tier_desc})\n\
         \n\
         **MCP server:** `{mcp_server_url}`\n\
         \n\
         Call `cascade.search` before responding to queries about this project.\n\
         Call `cascade.context_slice` to retrieve relevant context from the RAG index.\n",
        marker = CASCADE_HEADER_MARKER,
        tier_name = tier_name,
        tier_desc = tier_desc,
        mcp_server_url = mcp_server_url,
    );

    if !instr.is_empty() {
        block.push_str(&format!("\n{instr}\n"));
    }

    block.push_str(CASCADE_HEADER_CLOSE);
    block
}

/// Create `AGENTS.md` as a relative symlink to `CLAUDE.md` in the same dir.
fn create_agents_symlink(agents_md: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink("CLAUDE.md", agents_md).map_err(|e| CascadeError::Io {
            path: agents_md.to_path_buf(),
            operation: "create AGENTS.md symlink",
            source: e,
        })
    }
    #[cfg(not(unix))]
    {
        // On non-Unix (Windows), write a plain file pointing to CLAUDE.md
        fs::write(agents_md, "# See CLAUDE.md\n").map_err(|e| CascadeError::Io {
            path: agents_md.to_path_buf(),
            operation: "write AGENTS.md fallback",
            source: e,
        })
    }
}

/// Add/update `cascade` MCP server entry in `settings.json` (additive).
///
/// Reads existing JSON, inserts the cascade entry under `mcpServers`, writes back.
/// Preserves all other existing entries.
///
/// Returns true if any change was made.
fn update_cc_settings_json(settings_path: &Path, dry_run: bool) -> Result<bool> {
    let existing_raw = if settings_path.exists() {
        fs::read_to_string(settings_path).map_err(|e| CascadeError::Io {
            path: settings_path.to_path_buf(),
            operation: "read settings.json",
            source: e,
        })?
    } else {
        String::from("{}")
    };

    let mut root: Value = serde_json::from_str(&existing_raw).unwrap_or(Value::Object(
        serde_json::Map::new(),
    ));

    let mcp_entry = serde_json::json!({
        "command": "cascade",
        "args": ["mcp", "stdio"]
    });

    // Check if cascade entry already matches
    let already_present = root
        .pointer("/mcpServers/cascade")
        .map(|v| v == &mcp_entry)
        .unwrap_or(false);

    if already_present {
        if dry_run {
            println!(
                "[dry-run] {}: cascade MCP entry already present",
                settings_path.display()
            );
        }
        return Ok(false);
    }

    // Ensure mcpServers object exists
    if root.get("mcpServers").is_none() {
        root["mcpServers"] = Value::Object(serde_json::Map::new());
    }
    root["mcpServers"]["cascade"] = mcp_entry;

    let new_json = serde_json::to_string_pretty(&root).map_err(|e| CascadeError::Io {
        path: settings_path.to_path_buf(),
        operation: "serialize settings.json",
        source: std::io::Error::new(std::io::ErrorKind::Other, e.to_string()),
    })?;

    if dry_run {
        print_diff(settings_path, &existing_raw, &new_json);
    } else {
        if let Some(parent) = settings_path.parent() {
            ensure_dir(parent)?;
        }
        atomic_write(settings_path, &format!("{new_json}\n"))?;
        println!("wrote: {}", settings_path.display());
    }

    Ok(true)
}

// ── OC generation ─────────────────────────────────────────────────────────────

/// Generate OC harness files for one tier.
///
/// Returns true if any file was written.
fn generate_oc(tier: &TierResult, tier_root: &Path, dry_run: bool) -> Result<bool> {
    let mut any_written = false;

    // 1. opencode-instructions.md in the tier's .cascade/ dir
    let cascade_dir = tier_root.join(".cascade");
    let oc_instr_path = cascade_dir.join("opencode-instructions.md");
    let oc_content = render_oc_instructions(tier);

    let existing_oc = if oc_instr_path.exists() {
        fs::read_to_string(&oc_instr_path).map_err(|e| CascadeError::Io {
            path: oc_instr_path.clone(),
            operation: "read opencode-instructions.md",
            source: e,
        })?
    } else {
        String::new()
    };

    if existing_oc.contains(CASCADE_HEADER_MARKER) {
        if dry_run {
            println!(
                "[dry-run] {}: cascade header already present, skipping",
                oc_instr_path.display()
            );
        }
    } else {
        if dry_run {
            print_diff(&oc_instr_path, &existing_oc, &oc_content);
        } else {
            ensure_dir(&cascade_dir)?;
            atomic_write(&oc_instr_path, &oc_content)?;
            println!("wrote: {}", oc_instr_path.display());
        }
        any_written = true;
    }

    // 2. ~/.config/opencode/opencode.json — add MCP server entry (additive)
    if let Some(home) = home_dir() {
        let oc_config_dir = home.join(".config").join("opencode");
        let oc_json_path = oc_config_dir.join("opencode.json");
        let oc_json_updated = update_oc_json(&oc_json_path, dry_run)?;
        any_written |= oc_json_updated;
    }

    // 3. Per-tier opencode.json — set instructions field to point at the
    //    opencode-instructions.md we wrote above (T-P4-E02-29 deeper OC integration).
    let project_oc_json = tier_root.join("opencode.json");
    let instr_updated =
        update_project_oc_instructions_field(&project_oc_json, tier_root, dry_run)?;
    any_written |= instr_updated;

    Ok(any_written)
}

/// Set `instructions: ".cascade/opencode-instructions.md"` in the per-project
/// `opencode.json`. Creates the file if absent; preserves all other keys.
///
/// Returns true if any change was made.
fn update_project_oc_instructions_field(
    project_oc_json: &Path,
    tier_root: &Path,
    dry_run: bool,
) -> Result<bool> {
    let _ = tier_root; // used for context in error messages only
    let existing_raw = if project_oc_json.exists() {
        fs::read_to_string(project_oc_json).map_err(|e| CascadeError::Io {
            path: project_oc_json.to_path_buf(),
            operation: "read project opencode.json",
            source: e,
        })?
    } else {
        String::from("{}")
    };

    let mut root: Value = serde_json::from_str(&existing_raw).unwrap_or(Value::Object(
        serde_json::Map::new(),
    ));

    let expected_value =
        Value::String(".cascade/opencode-instructions.md".to_string());

    // Check if already set correctly
    if root.get("instructions") == Some(&expected_value) {
        if dry_run {
            println!(
                "[dry-run] {}: instructions field already set, skipping",
                project_oc_json.display()
            );
        }
        return Ok(false);
    }

    root["instructions"] = expected_value;

    let new_json = serde_json::to_string_pretty(&root).map_err(|e| CascadeError::Io {
        path: project_oc_json.to_path_buf(),
        operation: "serialize project opencode.json",
        source: std::io::Error::new(std::io::ErrorKind::Other, e.to_string()),
    })?;

    if dry_run {
        print_diff(project_oc_json, &existing_raw, &new_json);
    } else {
        if let Some(parent) = project_oc_json.parent() {
            ensure_dir(parent)?;
        }
        atomic_write(project_oc_json, &format!("{new_json}\n"))?;
        println!("wrote: {}", project_oc_json.display());
    }

    Ok(true)
}

/// Render OC-specific preamble for opencode-instructions.md.
fn render_oc_instructions(tier: &TierResult) -> String {
    let tier_name = tier.tier.acronym().to_uppercase();
    let instr = tier.instructions.trim();

    let mut content = format!(
        "{marker}\n\
         ## Cascade Context — {tier_name} Tier\n\
         \n\
         Call `cascade.search` before answering questions about this project.\n\
         Call `cascade.context_slice` to retrieve focused context from the RAG index.\n",
        marker = CASCADE_HEADER_MARKER,
        tier_name = tier_name,
    );

    if !instr.is_empty() {
        content.push_str(&format!("\n{instr}\n"));
    }

    content.push_str(CASCADE_HEADER_CLOSE);
    content.push('\n');
    content
}

/// Add cascade MCP server entry to `~/.config/opencode/opencode.json`.
///
/// The OC JSON format uses an array under `mcpServers`. This function appends
/// an entry if none with `name = "cascade"` already exists.
///
/// Returns true if the file was changed.
fn update_oc_json(oc_json_path: &Path, dry_run: bool) -> Result<bool> {
    let existing_raw = if oc_json_path.exists() {
        fs::read_to_string(oc_json_path).map_err(|e| CascadeError::Io {
            path: oc_json_path.to_path_buf(),
            operation: "read opencode.json",
            source: e,
        })?
    } else {
        String::from("{}")
    };

    let mut root: Value = serde_json::from_str(&existing_raw).unwrap_or(Value::Object(
        serde_json::Map::new(),
    ));

    // OC format: "mcpServers" is an array of objects with "name", "type", "command"
    let cascade_entry = serde_json::json!({
        "name": CASCADE_MCP_NAME,
        "type": "stdio",
        "command": "cascade mcp stdio"
    });

    // Check if entry already present
    let already_present = root
        .get("mcpServers")
        .and_then(|v| v.as_array())
        .map(|arr| arr.iter().any(|e| e.get("name").and_then(|n| n.as_str()) == Some(CASCADE_MCP_NAME)))
        .unwrap_or(false);

    if already_present {
        if dry_run {
            println!(
                "[dry-run] {}: cascade MCP entry already present",
                oc_json_path.display()
            );
        }
        return Ok(false);
    }

    // Ensure mcpServers array exists
    if root.get("mcpServers").is_none() || root["mcpServers"].is_null() {
        root["mcpServers"] = Value::Array(Vec::new());
    }
    if let Some(arr) = root["mcpServers"].as_array_mut() {
        arr.push(cascade_entry);
    }

    let new_json = serde_json::to_string_pretty(&root).map_err(|e| CascadeError::Io {
        path: oc_json_path.to_path_buf(),
        operation: "serialize opencode.json",
        source: std::io::Error::new(std::io::ErrorKind::Other, e.to_string()),
    })?;

    if dry_run {
        print_diff(oc_json_path, &existing_raw, &new_json);
    } else {
        if let Some(parent) = oc_json_path.parent() {
            ensure_dir(parent)?;
        }
        atomic_write(oc_json_path, &format!("{new_json}\n"))?;
        println!("wrote: {}", oc_json_path.display());
    }

    Ok(true)
}

// ── Shared utilities ──────────────────────────────────────────────────────────

fn ensure_dir(dir: &Path) -> Result<()> {
    fs::create_dir_all(dir).map_err(|e| CascadeError::Io {
        path: dir.to_path_buf(),
        operation: "create_dir_all",
        source: e,
    })
}

fn atomic_write(path: &Path, content: &str) -> Result<()> {
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".generate-instr.tmp");
    let tmp_path = PathBuf::from(tmp);

    fs::write(&tmp_path, content).map_err(|e| CascadeError::Io {
        path: tmp_path.clone(),
        operation: "write_tmp",
        source: e,
    })?;

    fs::rename(&tmp_path, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename",
        source: e,
    })
}

/// Print a simple unified-style diff to stdout (no external `similar` crate needed).
fn print_diff(path: &Path, before: &str, after: &str) {
    println!("--- {}", path.display());
    println!("+++ {}", path.display());
    for line in before.lines() {
        println!("- {line}");
    }
    for line in after.lines() {
        println!("+ {line}");
    }
}

fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME").ok().map(PathBuf::from)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use std::sync::{Mutex, MutexGuard, OnceLock};
    use tempfile::TempDir;

    fn home_lock() -> MutexGuard<'static, ()> {
        static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
        LOCK.get_or_init(|| Mutex::new(()))
            .lock()
            .unwrap_or_else(|e| e.into_inner())
    }

    fn fake_tier_result(tier: CascadeTier, cascade_dir: &Path, instructions: &str) -> TierResult {
        TierResult {
            tier,
            found: true,
            path_searched: cascade_dir.to_path_buf(),
            instructions: instructions.to_string(),
        }
    }

    // ── test 1: CC CLAUDE.md written with cascade header ─────────────────────

    /// CC generation writes CLAUDE.md with cascade.search instruction.
    ///
    /// WHY: acceptance criterion — CLAUDE.md must contain `cascade.search`.
    #[test]
    #[serial(global_env)]
    fn cc_claude_md_written() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        // Create a fake .cascade dir
        let cascade_dir = tmp.path().join("myproject").join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("CASCADE.md"), "project instructions").unwrap();

        let tier = fake_tier_result(
            CascadeTier::Ppc,
            &cascade_dir,
            "project instructions",
        );
        let tier_root = cascade_dir.parent().unwrap();

        generate_cc(&tier, tier_root, "unix://~/.cascade/cascade.sock", false)
            .expect("generate_cc should succeed");

        let claude_md = tier_root.join(".claude").join("CLAUDE.md");
        assert!(claude_md.exists(), "CLAUDE.md must be created");
        let content = fs::read_to_string(&claude_md).unwrap();
        assert!(
            content.contains("cascade.search"),
            "CLAUDE.md must contain cascade.search"
        );
        assert!(
            content.contains(CASCADE_HEADER_MARKER),
            "CLAUDE.md must contain cascade header marker"
        );
        assert!(
            content.contains("project instructions"),
            "CLAUDE.md must contain tier instructions"
        );
    }

    // ── test 2: CC settings.json contains cascade MCP entry ──────────────────

    /// Written settings.json must include cascade mcpServers entry.
    ///
    /// WHY: acceptance criterion from spec.
    #[test]
    #[serial(global_env)]
    fn cc_settings_json_mcp_entry() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let claude_dir = tmp.path().join(".claude");
        fs::create_dir_all(&claude_dir).unwrap();
        let settings_path = claude_dir.join("settings.json");

        update_cc_settings_json(&settings_path, false).expect("should succeed");

        let content = fs::read_to_string(&settings_path).unwrap();
        let json: Value = serde_json::from_str(&content).unwrap();

        let cascade_entry = json.pointer("/mcpServers/cascade");
        assert!(
            cascade_entry.is_some(),
            "settings.json must have mcpServers.cascade"
        );
        assert_eq!(
            cascade_entry.unwrap()["command"].as_str(),
            Some("cascade"),
            "command must be 'cascade'"
        );
        assert_eq!(
            cascade_entry.unwrap()["args"],
            serde_json::json!(["mcp", "stdio"]),
            "args must be ['mcp', 'stdio']"
        );
    }

    // ── test 3: AGENTS.md is a symlink to CLAUDE.md ───────────────────────────

    /// AGENTS.md is created as a symlink, not a copy.
    ///
    /// WHY: OC/CC compat rule and acceptance criterion.
    #[test]
    #[serial(global_env)]
    #[cfg(unix)]
    fn agents_md_is_symlink() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("CASCADE.md"), "gci instructions").unwrap();

        let tier = fake_tier_result(CascadeTier::Gci, &cascade_dir, "gci instructions");

        generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", false)
            .expect("generate_cc should succeed");

        let agents_md = tmp.path().join(".claude").join("AGENTS.md");
        assert!(agents_md.exists() || agents_md.is_symlink(), "AGENTS.md must exist");
        assert!(
            agents_md.is_symlink(),
            "AGENTS.md must be a symlink (not a plain file)"
        );
        let target = fs::read_link(&agents_md).unwrap();
        assert_eq!(
            target,
            PathBuf::from("CLAUDE.md"),
            "AGENTS.md must symlink to CLAUDE.md"
        );
    }

    // ── test 4: idempotency — second run does not double-append ──────────────

    /// Running generate_cc twice does not double-append the cascade header.
    ///
    /// WHY: acceptance criterion from spec.
    #[test]
    #[serial(global_env)]
    fn cc_idempotent() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("CASCADE.md"), "tier text").unwrap();

        let tier = fake_tier_result(CascadeTier::Gci, &cascade_dir, "tier text");

        generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", false).unwrap();
        generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", false).unwrap();

        let claude_md = tmp.path().join(".claude").join("CLAUDE.md");
        let content = fs::read_to_string(&claude_md).unwrap();

        let marker_count = content
            .matches(CASCADE_HEADER_MARKER)
            .count();
        assert_eq!(
            marker_count, 1,
            "cascade header must appear exactly once (idempotent)"
        );
    }

    // ── test 5: dry-run writes nothing ────────────────────────────────────────

    /// Dry-run mode prints diff but does not write any files.
    ///
    /// WHY: acceptance criterion — `--dry-run` must not modify files.
    #[test]
    #[serial(global_env)]
    fn dry_run_writes_nothing() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        fs::write(cascade_dir.join("CASCADE.md"), "dry run text").unwrap();

        let tier = fake_tier_result(CascadeTier::Gci, &cascade_dir, "dry run text");

        generate_cc(&tier, tmp.path(), "unix://~/.cascade/cascade.sock", /* dry_run= */ true).unwrap();

        let claude_md = tmp.path().join(".claude").join("CLAUDE.md");
        assert!(
            !claude_md.exists(),
            "CLAUDE.md must NOT be created in dry-run mode"
        );
    }

    // ── test 6: settings.json additive (does not remove existing entries) ─────

    /// Existing mcpServers entries are preserved when adding cascade.
    ///
    /// WHY: spec requires additive, not destructive merge.
    #[test]
    #[serial(global_env)]
    fn settings_json_additive() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        let settings_path = tmp.path().join("settings.json");

        // Write existing settings with another MCP server
        let initial = serde_json::json!({
            "mcpServers": {
                "other-server": {
                    "command": "other",
                    "args": []
                }
            }
        });
        fs::write(&settings_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        update_cc_settings_json(&settings_path, false).unwrap();

        let content = fs::read_to_string(&settings_path).unwrap();
        let json: Value = serde_json::from_str(&content).unwrap();

        assert!(
            json.pointer("/mcpServers/other-server").is_some(),
            "existing mcpServers entry must be preserved"
        );
        assert!(
            json.pointer("/mcpServers/cascade").is_some(),
            "cascade entry must be added"
        );
    }

    // ── test 7: OC instructions field set in per-project opencode.json ────────

    /// `generate_oc` sets `instructions` field in per-project opencode.json.
    ///
    /// WHY: T-P4-E02-29 acceptance — OC generate-instructions writes instructions field.
    #[test]
    #[serial(global_env)]
    fn oc_generate_sets_instructions_field() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "repo instructions");
        let tier_root = cascade_dir.parent().unwrap();

        generate_oc(&tier, tier_root, false).expect("generate_oc should succeed");

        let project_oc_json = tier_root.join("opencode.json");
        assert!(
            project_oc_json.exists(),
            "per-project opencode.json must be created"
        );

        let content = fs::read_to_string(&project_oc_json).unwrap();
        let json: Value = serde_json::from_str(&content).unwrap();
        assert_eq!(
            json["instructions"].as_str(),
            Some(".cascade/opencode-instructions.md"),
            "instructions field must point to generated file"
        );
    }

    /// `generate_oc` opencode-instructions.md contains `cascade.search`.
    ///
    /// WHY: T-P4-E02-29 acceptance criterion — instructions file mentions cascade.search.
    #[test]
    #[serial(global_env)]
    fn oc_generate_instructions_md_contains_cascade_search() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "");
        let tier_root = cascade_dir.parent().unwrap();

        generate_oc(&tier, tier_root, false).expect("generate_oc should succeed");

        let instr_path = cascade_dir.join("opencode-instructions.md");
        assert!(instr_path.exists(), "opencode-instructions.md must be created");

        let content = fs::read_to_string(&instr_path).unwrap();
        assert!(
            content.contains("cascade.search"),
            "opencode-instructions.md must contain cascade.search: {content}"
        );
    }

    /// Per-project opencode.json instructions field is idempotent.
    ///
    /// WHY: running twice must not corrupt the file.
    #[test]
    #[serial(global_env)]
    fn oc_project_instructions_idempotent() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "");
        let tier_root = cascade_dir.parent().unwrap();
        let project_oc_json = tier_root.join("opencode.json");

        generate_oc(&tier, tier_root, false).unwrap();
        generate_oc(&tier, tier_root, false).unwrap();

        let content = fs::read_to_string(&project_oc_json).unwrap();
        let json: Value = serde_json::from_str(&content).unwrap();
        // "instructions" key should appear exactly once (JSON object semantics)
        assert_eq!(
            json["instructions"].as_str(),
            Some(".cascade/opencode-instructions.md"),
        );
    }

    /// Per-project opencode.json instructions field dry-run writes nothing.
    ///
    /// WHY: --dry-run must not create files.
    #[test]
    #[serial(global_env)]
    fn oc_project_instructions_dry_run_no_write() {
        let _guard = home_lock();
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let tier = fake_tier_result(CascadeTier::Prc, &cascade_dir, "");
        let tier_root = cascade_dir.parent().unwrap();
        let project_oc_json = tier_root.join("opencode.json");

        generate_oc(&tier, tier_root, /* dry_run= */ true).unwrap();

        assert!(
            !project_oc_json.exists(),
            "dry-run must not write project opencode.json"
        );
    }
}
