//! MCP self-registration — write the cascade MCP server entry into the active
//! AI harness settings so the harness can discover and connect to cascaded.
//!
//! Purpose: on daemon activation (when `mcp.enabled = true`), register the
//! cascade MCP server socket path into `~/.claude/settings.json` (or the
//! equivalent for other supported harnesses) so that Claude Code / OpenCode
//! automatically connects on next startup.
//!
//! Inputs:  config_dir — path to the active `.cascade/` directory.
//! Outputs: side-effect write to harness settings file.
//! Constraints:
//!   - Write is idempotent: re-registering an already-registered server is a no-op.
//!   - Only called when `config.mcp.enabled = true`.
//!   - Never panics; errors are logged as WARN and the daemon continues.
//!
//! SPORT: MASTER-DAEMON.md → mcp_registration (frame-01)

use std::path::Path;

use tracing::{info, warn};

/// Register the cascade MCP server with all detected AI harnesses.
///
/// Called from `supervisor::run` when `mcp.enabled = true`. Upserts the
/// `mcpServers.cascade` entry in `~/.claude/settings.json` atomically,
/// preserving all other keys. Idempotent: skips the write if the entry
/// is already present and unchanged.
///
/// # Errors
///
/// Never returns an error — all failures are logged as WARN and swallowed so
/// the daemon continues without MCP if registration fails.
pub async fn register(config_dir: &Path) {
    // 1. Derive expected socket path.
    let socket_path = config_dir.join("mcp.sock");
    if !socket_path.exists() {
        warn!(
            socket = %socket_path.display(),
            "mcp_registration: socket not yet created — registration deferred"
        );
        return;
    }

    // 2. Resolve Claude settings path.
    let settings_path = match dirs::home_dir().map(|h| h.join(".claude").join("settings.json")) {
        Some(p) => p,
        None => {
            warn!("mcp_registration: cannot determine home directory — skipping");
            return;
        }
    };

    // 3. Read existing JSON (absent = empty object).
    let existing_text = match std::fs::read_to_string(&settings_path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::from("{}"),
        Err(e) => {
            warn!(path = %settings_path.display(), err = %e, "mcp_registration: cannot read settings.json");
            return;
        }
    };

    let mut root: serde_json::Value = match serde_json::from_str(&existing_text) {
        Ok(v) => v,
        Err(e) => {
            warn!(path = %settings_path.display(), err = %e, "mcp_registration: settings.json is not valid JSON — skipping");
            return;
        }
    };

    // 4. Build the mcpServers.cascade entry.
    let entry = serde_json::json!({
        "command": "cascaded",
        "args": ["--mcp"],
        "env": {}
    });

    // 5. Upsert — preserve all other keys.
    root.as_object_mut()
        .expect("JSON root must be object")
        .entry("mcpServers")
        .or_insert_with(|| serde_json::Value::Object(serde_json::Map::new()));

    root["mcpServers"]["cascade"] = entry;

    // 6. Serialize to pretty JSON.
    let new_text = match serde_json::to_string_pretty(&root) {
        Ok(t) => t,
        Err(e) => {
            warn!(err = %e, "mcp_registration: failed to serialize settings.json");
            return;
        }
    };

    // 7. Skip write if content is unchanged.
    if new_text == existing_text {
        info!(
            path = %settings_path.display(),
            "mcp_registration: already registered — skipping write"
        );
        return;
    }

    // 8. Atomic write via temp file + rename.
    let tmp_path = settings_path.with_extension("json.tmp");
    if let Err(e) = std::fs::write(&tmp_path, &new_text) {
        warn!(path = %tmp_path.display(), err = %e, "mcp_registration: failed to write temp settings file");
        return;
    }
    if let Err(e) = std::fs::rename(&tmp_path, &settings_path) {
        warn!(
            from = %tmp_path.display(),
            to = %settings_path.display(),
            err = %e,
            "mcp_registration: failed to rename temp settings file"
        );
        let _ = std::fs::remove_file(&tmp_path);
        return;
    }

    info!(
        path = %settings_path.display(),
        "mcp_registration: registered cascade MCP server"
    );
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Helper: create a fake mcp.sock in `cascade_dir` and set HOME to `home_dir`.
    fn setup(cascade_dir: &Path, home_dir: &Path) {
        fs::create_dir_all(cascade_dir).unwrap();
        // Create a fake mcp.sock (just an empty file for existence check).
        fs::write(cascade_dir.join("mcp.sock"), b"").unwrap();
        // Ensure .claude dir exists in home.
        fs::create_dir_all(home_dir.join(".claude")).unwrap();
        std::env::set_var("HOME", home_dir.as_os_str());
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn test_register_writes_mcp_entry() {
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        register(&cascade_dir).await;

        let settings_path = home.path().join(".claude").join("settings.json");
        assert!(settings_path.exists(), "settings.json must be written");
        let text = fs::read_to_string(&settings_path).unwrap();
        let val: serde_json::Value = serde_json::from_str(&text).unwrap();
        assert_eq!(val["mcpServers"]["cascade"]["command"], "cascaded");
        assert_eq!(val["mcpServers"]["cascade"]["args"][0], "--mcp");
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn test_register_idempotent() {
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        register(&cascade_dir).await;
        let settings_path = home.path().join(".claude").join("settings.json");
        let content1 = fs::read_to_string(&settings_path).unwrap();

        register(&cascade_dir).await;
        let content2 = fs::read_to_string(&settings_path).unwrap();

        assert_eq!(content1, content2, "second register must not change content");
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn test_register_non_clobbering() {
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        // Pre-populate settings.json with other keys.
        let settings_path = home.path().join(".claude").join("settings.json");
        let initial = serde_json::json!({
            "theme": "dark",
            "mcpServers": {
                "other": { "command": "other" }
            }
        });
        fs::write(&settings_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        register(&cascade_dir).await;

        let text = fs::read_to_string(&settings_path).unwrap();
        let val: serde_json::Value = serde_json::from_str(&text).unwrap();

        // cascade entry added.
        assert_eq!(val["mcpServers"]["cascade"]["command"], "cascaded");
        // "other" preserved.
        assert_eq!(val["mcpServers"]["other"]["command"], "other");
        // "theme" preserved.
        assert_eq!(val["theme"], "dark");
    }
}
