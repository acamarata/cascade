//! Codex `config.yaml` scaffold and MCP server entry injection.
//!
//! ## Purpose
//! Writes or merges `{workspace}/codex/config.yaml` to include the Cascade
//! MCP stdio server entry. Idempotent: if the `cascade` entry already exists
//! the file is left unchanged.
//!
//! ## MCP entry written
//! ```yaml
//! mcp:
//!   servers:
//!     - name: cascade
//!       transport: stdio
//!       command: cascade
//!       args: ["mcp", "stdio"]
//!       env:
//!         CASCADE_SOCKET: <daemon_socket>
//! ```
//!
//! ## Constraints
//! - Atomic write: temp file → `std::fs::rename` (no partial writes).
//! - Non-destructive: existing entries preserved.
//! - Idempotent: key-presence check prevents duplicate injection.
//!
//! ## SPORT
//! MASTER-HARNESSES.md: Codex row — codex_config_scaffold (T-P4-E06-02)

use std::fs;
use std::io::Write;
use std::path::Path;

use cascade_types::error::{CascadeError, Result};
use tracing::{debug, info};

// ── Public API ────────────────────────────────────────────────────────────────

/// Write or merge `{workspace}/codex/config.yaml` with the Cascade MCP entry.
///
/// # Arguments
/// * `workspace` — the workspace root; `codex/config.yaml` is written here.
/// * `daemon_socket` — path (or URL) for the `CASCADE_SOCKET` env var.
///
/// # Idempotency
/// If a server entry with `name: cascade` already exists under `mcp.servers`,
/// logs an info message and returns `Ok(())` without modifying the file.
///
/// # Atomicity
/// Writes to `config.yaml.tmp` then renames to `config.yaml`.
pub fn scaffold_codex_config(workspace: &Path, daemon_socket: &str) -> Result<()> {
    let codex_dir = workspace.join("codex");
    let config_path = codex_dir.join("config.yaml");

    // Load existing config or start with empty map
    let mut root: serde_yaml::Value = if config_path.exists() {
        let content = fs::read_to_string(&config_path).map_err(|e| CascadeError::Io {
            path: config_path.clone(),
            operation: "read",
            source: e,
        })?;
        serde_yaml::from_str(&content).unwrap_or(serde_yaml::Value::Mapping(Default::default()))
    } else {
        serde_yaml::Value::Mapping(Default::default())
    };

    // Navigate mcp.servers, check for existing cascade entry
    if cascade_entry_exists(&root) {
        info!(
            "cascade MCP entry already present in {:?}; skipping",
            config_path
        );
        return Ok(());
    }

    // Build the cascade MCP server entry
    let entry = build_cascade_entry(daemon_socket);

    // Insert into mcp.servers (creating intermediate keys as needed)
    insert_cascade_entry(&mut root, entry);

    // Atomic write: write to .tmp then rename
    fs::create_dir_all(&codex_dir).map_err(|e| CascadeError::Io {
        path: codex_dir.clone(),
        operation: "create_dir",
        source: e,
    })?;

    let tmp_path = codex_dir.join("config.yaml.tmp");
    {
        let mut file = fs::File::create(&tmp_path).map_err(|e| CascadeError::Io {
            path: tmp_path.clone(),
            operation: "create_tmp",
            source: e,
        })?;
        let yaml_str = serde_yaml::to_string(&root)
            .map_err(|e| CascadeError::Other(format!("YAML serialization error: {e}")))?;
        file.write_all(yaml_str.as_bytes())
            .map_err(|e| CascadeError::Io {
                path: tmp_path.clone(),
                operation: "write",
                source: e,
            })?;
    }

    fs::rename(&tmp_path, &config_path).map_err(|e| CascadeError::Io {
        path: config_path.clone(),
        operation: "rename",
        source: e,
    })?;

    debug!("wrote cascade MCP entry to {:?}", config_path);
    Ok(())
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Return true if a `name: cascade` server entry already exists under `mcp.servers`.
fn cascade_entry_exists(root: &serde_yaml::Value) -> bool {
    let servers = match root
        .get("mcp")
        .and_then(|m| m.get("servers"))
        .and_then(|s| s.as_sequence())
    {
        Some(seq) => seq,
        None => return false,
    };

    servers.iter().any(|entry| {
        entry
            .get("name")
            .and_then(|n| n.as_str())
            .map(|n| n == "cascade")
            .unwrap_or(false)
    })
}

/// Build the `cascade` MCP server entry as a YAML value.
fn build_cascade_entry(daemon_socket: &str) -> serde_yaml::Value {
    use serde_yaml::{Mapping, Value};

    let mut entry = Mapping::new();
    entry.insert(
        Value::String("name".into()),
        Value::String("cascade".into()),
    );
    entry.insert(
        Value::String("transport".into()),
        Value::String("stdio".into()),
    );
    entry.insert(
        Value::String("command".into()),
        Value::String("cascade".into()),
    );
    // args: ["mcp", "stdio"]
    entry.insert(
        Value::String("args".into()),
        Value::Sequence(vec![
            Value::String("mcp".into()),
            Value::String("stdio".into()),
        ]),
    );
    // env: { CASCADE_SOCKET: <socket> }
    let mut env = Mapping::new();
    env.insert(
        Value::String("CASCADE_SOCKET".into()),
        Value::String(daemon_socket.into()),
    );
    entry.insert(Value::String("env".into()), Value::Mapping(env));

    Value::Mapping(entry)
}

/// Insert `entry` under `root.mcp.servers`, creating intermediate keys as needed.
fn insert_cascade_entry(root: &mut serde_yaml::Value, entry: serde_yaml::Value) {
    use serde_yaml::{Mapping, Value};

    // Ensure root is a mapping
    if !root.is_mapping() {
        *root = Value::Mapping(Mapping::new());
    }
    let root_map = root.as_mapping_mut().unwrap();

    // Ensure mcp key
    let mcp = root_map
        .entry(Value::String("mcp".into()))
        .or_insert_with(|| Value::Mapping(Mapping::new()));

    if !mcp.is_mapping() {
        *mcp = Value::Mapping(Mapping::new());
    }
    let mcp_map = mcp.as_mapping_mut().unwrap();

    // Ensure mcp.servers
    let servers = mcp_map
        .entry(Value::String("servers".into()))
        .or_insert_with(|| Value::Sequence(vec![]));

    if !servers.is_sequence() {
        *servers = Value::Sequence(vec![]);
    }

    servers.as_sequence_mut().unwrap().push(entry);
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    fn read_yaml(path: &Path) -> serde_yaml::Value {
        let content = fs::read_to_string(path).unwrap();
        serde_yaml::from_str(&content).unwrap()
    }

    #[test]
    #[serial(global_env)]
    fn scaffold_creates_config_when_absent() {
        let tmp = TempDir::new().unwrap();
        scaffold_codex_config(tmp.path(), "/tmp/cascade.sock").unwrap();

        let config_path = tmp.path().join("codex").join("config.yaml");
        assert!(config_path.exists(), "config.yaml should be created");

        let yaml = read_yaml(&config_path);
        assert!(
            cascade_entry_exists(&yaml),
            "cascade entry should be present"
        );
    }

    #[test]
    #[serial(global_env)]
    fn scaffold_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let config_path = tmp.path().join("codex").join("config.yaml");

        // First call
        scaffold_codex_config(tmp.path(), "/tmp/cascade.sock").unwrap();
        let content_after_first = fs::read_to_string(&config_path).unwrap();

        // Second call — should not change the file
        scaffold_codex_config(tmp.path(), "/tmp/cascade.sock").unwrap();
        let content_after_second = fs::read_to_string(&config_path).unwrap();

        assert_eq!(
            content_after_first, content_after_second,
            "file should be unchanged after second scaffold call"
        );
    }

    #[test]
    #[serial(global_env)]
    fn scaffold_preserves_existing_user_entries() {
        let tmp = TempDir::new().unwrap();
        let codex_dir = tmp.path().join("codex");
        fs::create_dir_all(&codex_dir).unwrap();

        // Pre-write a config with a user's existing server entry
        let existing_yaml = r#"
mcp:
  servers:
    - name: my-custom-server
      transport: stdio
      command: my-tool
      args: ["serve"]
"#;
        fs::write(codex_dir.join("config.yaml"), existing_yaml).unwrap();

        scaffold_codex_config(tmp.path(), "/tmp/cascade.sock").unwrap();

        let yaml = read_yaml(&codex_dir.join("config.yaml"));
        // Both servers should be present
        let servers = yaml["mcp"]["servers"].as_sequence().unwrap();
        assert_eq!(servers.len(), 2, "should have both entries");

        let names: Vec<&str> = servers
            .iter()
            .filter_map(|s| s.get("name").and_then(|n| n.as_str()))
            .collect();
        assert!(names.contains(&"my-custom-server"), "user entry preserved");
        assert!(names.contains(&"cascade"), "cascade entry added");
    }

    #[test]
    #[serial(global_env)]
    fn scaffold_skips_when_cascade_entry_exists() {
        let tmp = TempDir::new().unwrap();
        let codex_dir = tmp.path().join("codex");
        fs::create_dir_all(&codex_dir).unwrap();

        // Pre-write a config that already has cascade (user hand-edited)
        let existing_yaml = r#"
mcp:
  servers:
    - name: cascade
      transport: stdio
      command: cascade
      args: ["mcp", "stdio"]
      env:
        CASCADE_SOCKET: /custom/path.sock
"#;
        let config_path = codex_dir.join("config.yaml");
        fs::write(&config_path, existing_yaml).unwrap();
        let original_content = fs::read_to_string(&config_path).unwrap();

        scaffold_codex_config(tmp.path(), "/tmp/different.sock").unwrap();

        let new_content = fs::read_to_string(&config_path).unwrap();
        assert_eq!(
            original_content, new_content,
            "user's hand-edited cascade entry must not be overwritten"
        );
    }

    #[test]
    #[serial(global_env)]
    fn scaffold_entry_has_correct_fields() {
        let tmp = TempDir::new().unwrap();
        scaffold_codex_config(tmp.path(), "unix:///run/cascade.sock").unwrap();

        let config_path = tmp.path().join("codex").join("config.yaml");
        let yaml = read_yaml(&config_path);
        let servers = yaml["mcp"]["servers"].as_sequence().unwrap();
        let cascade = servers
            .iter()
            .find(|s| s.get("name").and_then(|n| n.as_str()) == Some("cascade"))
            .unwrap();

        assert_eq!(cascade["transport"].as_str(), Some("stdio"));
        assert_eq!(cascade["command"].as_str(), Some("cascade"));
        let args: Vec<&str> = cascade["args"]
            .as_sequence()
            .unwrap()
            .iter()
            .filter_map(|v| v.as_str())
            .collect();
        assert_eq!(args, vec!["mcp", "stdio"]);
        assert_eq!(
            cascade["env"]["CASCADE_SOCKET"].as_str(),
            Some("unix:///run/cascade.sock")
        );
    }
}
