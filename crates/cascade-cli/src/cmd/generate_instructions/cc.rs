//! CC (Claude Code) harness file generation: CLAUDE.md, AGENTS.md, settings.json.

use std::fs;
use std::path::Path;

use cascade_core::cascade_resolution::TierResult;
use cascade_types::error::{CascadeError, Result};
use serde_json::Value;

use super::utils::{atomic_write, ensure_dir, print_diff};

/// Idempotency marker — if this string appears in CLAUDE.md, skip injection.
pub(super) const CASCADE_HEADER_MARKER: &str = "<!-- cascade:generate-instructions -->";
pub(super) const CASCADE_HEADER_CLOSE: &str = "<!-- /cascade:generate-instructions -->";

/// Generate CC harness files for one tier: CLAUDE.md, AGENTS.md, settings.json.
///
/// Returns true if any file was written (or would be written in dry-run).
pub(super) fn generate_cc(
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
            let sep = if existing_claude.ends_with('\n') {
                "\n"
            } else {
                "\n\n"
            };
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
pub(super) fn update_cc_settings_json(settings_path: &Path, dry_run: bool) -> Result<bool> {
    let existing_raw = if settings_path.exists() {
        fs::read_to_string(settings_path).map_err(|e| CascadeError::Io {
            path: settings_path.to_path_buf(),
            operation: "read settings.json",
            source: e,
        })?
    } else {
        String::from("{}")
    };

    let mut root: Value =
        serde_json::from_str(&existing_raw).unwrap_or(Value::Object(serde_json::Map::new()));

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
        source: std::io::Error::other(e.to_string()),
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
