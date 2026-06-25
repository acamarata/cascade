//! OC (OpenCode) harness file generation: opencode.json, opencode-instructions.md.

use std::fs;
use std::path::Path;

use cascade_core::cascade_resolution::TierResult;
use cascade_types::error::{CascadeError, Result};
use serde_json::Value;

use super::cc::{CASCADE_HEADER_CLOSE, CASCADE_HEADER_MARKER};
use super::utils::{atomic_write, ensure_dir, home_dir, print_diff};

/// OC opencode.json MCP server entry name.
const CASCADE_MCP_NAME: &str = "cascade";

/// Generate OC harness files for one tier.
///
/// Returns true if any file was written.
pub(super) fn generate_oc(tier: &TierResult, tier_root: &Path, dry_run: bool) -> Result<bool> {
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
    let instr_updated = update_project_oc_instructions_field(&project_oc_json, tier_root, dry_run)?;
    any_written |= instr_updated;

    Ok(any_written)
}

/// Set `instructions: ".cascade/opencode-instructions.md"` in the per-project
/// `opencode.json`. Creates the file if absent; preserves all other keys.
///
/// Returns true if any change was made.
pub(super) fn update_project_oc_instructions_field(
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

    let mut root: Value =
        serde_json::from_str(&existing_raw).unwrap_or(Value::Object(serde_json::Map::new()));

    let expected_value = Value::String(".cascade/opencode-instructions.md".to_string());

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
        source: std::io::Error::other(e.to_string()),
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

    let mut root: Value =
        serde_json::from_str(&existing_raw).unwrap_or(Value::Object(serde_json::Map::new()));

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
        .map(|arr| {
            arr.iter()
                .any(|e| e.get("name").and_then(|n| n.as_str()) == Some(CASCADE_MCP_NAME))
        })
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
        source: std::io::Error::other(e.to_string()),
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
