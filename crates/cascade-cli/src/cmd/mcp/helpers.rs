//! Shared JSON read/write helpers for MCP client config management.

use std::path::Path;

use cascade_types::error::{CascadeError, Result};
use serde_json::{json, Value};

// ── Setup helpers — shared ────────────────────────────────────────────────────

/// Read a JSON file or return `{}` if absent.
pub fn read_json_or_empty(path: &Path) -> Result<Value> {
    if !path.exists() {
        return Ok(json!({}));
    }
    let raw = std::fs::read_to_string(path)
        .map_err(|e| CascadeError::Other(format!("failed to read {}: {e}", path.display())))?;
    serde_json::from_str(&raw)
        .map_err(|e| CascadeError::Other(format!("failed to parse JSON {}: {e}", path.display())))
}

/// Atomically write JSON to `path` (write to `.tmp`, then rename).
/// Creates parent directories if needed. Backs up existing file to `.bak` on
/// first write.
pub fn write_json_atomic(path: &Path, value: &Value, dry_run: bool) -> Result<()> {
    let pretty = serde_json::to_string_pretty(value)
        .map_err(|e| CascadeError::Other(format!("JSON serialise error: {e}")))?;

    if dry_run {
        println!("--- (dry-run) would write: {} ---", path.display());
        println!("{pretty}");
        println!("--- end ---");
        return Ok(());
    }

    // Ensure parent exists
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

    // Write to temp then rename (atomic)
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, pretty.as_bytes())
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {e}", tmp.display())))?;
    std::fs::rename(&tmp, path)
        .map_err(|e| CascadeError::Other(format!("rename to {}: {e}", path.display())))?;
    Ok(())
}

/// Ensure `obj["mcp_key"]` is a JSON object, upsert `entry_key → entry_value`.
/// Returns `true` if a change was made.
pub fn upsert_key(obj: &mut Value, mcp_key: &str, entry_key: &str, entry_value: Value) -> bool {
    let top = obj.as_object_mut().expect("caller must pass a JSON object");
    let servers = top.entry(mcp_key).or_insert_with(|| json!({}));
    let servers_obj = servers
        .as_object_mut()
        .expect("mcpServers/servers must be an object");
    let old = servers_obj.get(entry_key).cloned();
    servers_obj.insert(entry_key.to_string(), entry_value.clone());
    old.as_ref() != Some(&entry_value)
}

/// Remove `obj["mcp_key"]["cascade"]`. Returns `true` if it was present.
pub fn remove_cascade(obj: &mut Value, mcp_key: &str) -> bool {
    obj.get_mut(mcp_key)
        .and_then(|v| v.as_object_mut())
        .and_then(|m| m.remove("cascade"))
        .is_some()
}
