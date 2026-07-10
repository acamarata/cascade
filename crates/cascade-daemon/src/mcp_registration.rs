//! Cascade settings-hook registration.
//!
//! Purpose: idempotently install Cascade's two CC lifecycle hooks into
//! `~/.claude/settings.json` at daemon startup:
//!
//!   - `register_cc_stop_hook`  — Stop hook that harvests CC session data.
//!   - `register_security_hook` — PostToolUse hook that runs the secret scanner.
//!
//! The former `register()` function (MCP server self-registration) has been
//! removed because it required a `mcp.sock` that nothing creates and attempted
//! to register `cascaded --mcp` which `main.rs` cannot parse.  The two hook
//! functions below are retained — they are legitimately used and successfully
//! modify `~/.claude/settings.json` at boot.
//!
//! Inputs:  none (reads `$HOME/.claude/settings.json`).
//! Outputs: idempotent side-effect writes to settings.json.
//! Constraints: never panics; errors are logged as WARN and ignored.
//! SPORT: MASTER-DAEMON.md → mcp_registration

use tracing::{info, warn};

/// Register the cascade CC Stop hook in ~/.claude/settings.json.
///
/// Adds a Stop hook entry that POSTs session data to the daemon's harvest endpoint.
/// Idempotent: no-op if the entry already exists. Silently skips if settings.json
/// cannot be read/written (daemon is allowed to run without the hook).
pub async fn register_cc_stop_hook() {
    // Resolve Claude settings path.
    let settings_path = match dirs::home_dir().map(|h| h.join(".claude").join("settings.json")) {
        Some(p) => p,
        None => {
            warn!("mcp_registration: cannot determine home directory — skipping cc_stop_hook");
            return;
        }
    };

    // Read existing JSON (absent = empty object).
    let existing_text = match std::fs::read_to_string(&settings_path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::from("{}"),
        Err(e) => {
            warn!(path = %settings_path.display(), err = %e, "mcp_registration: cannot read settings.json for cc_stop_hook");
            return;
        }
    };

    let mut root: serde_json::Value = match serde_json::from_str(&existing_text) {
        Ok(v) => v,
        Err(e) => {
            warn!(path = %settings_path.display(), err = %e, "mcp_registration: settings.json is not valid JSON — skipping cc_stop_hook");
            return;
        }
    };

    // Idempotency check: look for existing Stop hook referencing our harvest endpoint.
    let harvest_marker = "/api/harvest/cc-session";
    if let Some(stop_arr) = root["hooks"]["Stop"].as_array() {
        for entry in stop_arr {
            // Each entry is { "matcher": "...", "hooks": [...] }
            if let Some(hooks) = entry["hooks"].as_array() {
                for h in hooks {
                    if h["command"]
                        .as_str()
                        .map(|c| c.contains(harvest_marker))
                        .unwrap_or(false)
                    {
                        info!("mcp_registration: cc_stop_hook already registered — skipping");
                        return;
                    }
                }
            }
        }
    }

    // Build the Stop hook entry.
    let hook_command = concat!(
        "bash -c 'curl -sf --max-time 0.45 -X POST http://127.0.0.1:9761/api/harvest/cc-session",
        " -H \"Content-Type: application/json\"",
        " -d \"{\\\"session_id\\\":\\\"${CLAUDE_SESSION_ID:-}\\\",\\\"project_dir\\\":\\\"${PWD}\\\"}\"",
        " || true'"
    );

    let stop_entry = serde_json::json!({
        "matcher": "",
        "hooks": [
            {
                "type": "command",
                "command": hook_command
            }
        ]
    });

    // Upsert under hooks.Stop (array) — build the path step by step to avoid
    // simultaneous mutable borrows of `root`.
    {
        let obj = root.as_object_mut().expect("JSON root must be object");
        obj.entry("hooks")
            .or_insert_with(|| serde_json::Value::Object(serde_json::Map::new()));
    }
    {
        let hooks_obj = root["hooks"].as_object_mut().unwrap();
        hooks_obj
            .entry("Stop")
            .or_insert_with(|| serde_json::Value::Array(Vec::new()));
    }
    root["hooks"]["Stop"]
        .as_array_mut()
        .unwrap()
        .push(stop_entry);

    // Serialize to pretty JSON.
    let new_text = match serde_json::to_string_pretty(&root) {
        Ok(t) => t,
        Err(e) => {
            warn!(err = %e, "mcp_registration: failed to serialize settings.json for cc_stop_hook");
            return;
        }
    };

    // Atomic write via temp file + rename.
    let tmp_path = settings_path.with_extension("json.tmp");
    if let Err(e) = std::fs::write(&tmp_path, &new_text) {
        warn!(path = %tmp_path.display(), err = %e, "mcp_registration: failed to write temp settings file for cc_stop_hook");
        return;
    }
    if let Err(e) = std::fs::rename(&tmp_path, &settings_path) {
        warn!(
            from = %tmp_path.display(),
            to = %settings_path.display(),
            err = %e,
            "mcp_registration: failed to rename temp settings file for cc_stop_hook"
        );
        let _ = std::fs::remove_file(&tmp_path);
        return;
    }

    info!(
        path = %settings_path.display(),
        "mcp_registration: registered CC Stop hook for harvest endpoint"
    );
}

/// Register the CC PostToolUse security scan hook in ~/.claude/settings.json.
///
/// Adds a `PostToolUse` hook entry with matcher `"Write|Edit"` so CC runs
/// `cascade security scan-hook` after every Write or Edit tool call. The
/// subcommand reads the hook JSON from stdin, extracts `tool_input.file_path`,
/// and runs the secret-scan logic. It always exits 0 — never interrupts CC.
///
/// Idempotent: no-op if the entry already exists. Silently skips on any
/// settings.json read/write failure.
///
/// # Hook JSON written
/// ```json
/// {
///   "matcher": "Write|Edit",
///   "hooks": [{ "type": "command", "command": "cascade security scan-hook" }]
/// }
/// ```
pub async fn register_security_hook() {
    // Resolve Claude settings path.
    let settings_path = match dirs::home_dir().map(|h| h.join(".claude").join("settings.json")) {
        Some(p) => p,
        None => {
            warn!("mcp_registration: cannot determine home directory — skipping security_hook");
            return;
        }
    };

    // Read existing JSON (absent = empty object).
    let existing_text = match std::fs::read_to_string(&settings_path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::from("{}"),
        Err(e) => {
            warn!(path = %settings_path.display(), err = %e, "mcp_registration: cannot read settings.json for security_hook");
            return;
        }
    };

    let mut root: serde_json::Value = match serde_json::from_str(&existing_text) {
        Ok(v) => v,
        Err(e) => {
            warn!(path = %settings_path.display(), err = %e, "mcp_registration: settings.json is not valid JSON — skipping security_hook");
            return;
        }
    };

    // Idempotency check: look for existing PostToolUse hook with our scan-hook command.
    let security_hook_marker = "cascade security scan-hook";
    if let Some(arr) = root["hooks"]["PostToolUse"].as_array() {
        for entry in arr {
            // Each entry is { "matcher": "...", "hooks": [...] }
            if let Some(hooks) = entry["hooks"].as_array() {
                for h in hooks {
                    if h["command"]
                        .as_str()
                        .map(|c| c.contains(security_hook_marker))
                        .unwrap_or(false)
                    {
                        info!("mcp_registration: security_hook already registered — skipping");
                        return;
                    }
                }
            }
        }
    }

    // Build the PostToolUse hook entry.
    // The matcher is a regex on the tool name; "Write|Edit" matches both CC tools.
    // The command uses the `cascade` CLI which is on PATH when cascaded is running.
    let hook_entry = serde_json::json!({
        "matcher": "Write|Edit",
        "hooks": [
            {
                "type": "command",
                "command": "cascade security scan-hook"
            }
        ]
    });

    // Upsert under hooks.PostToolUse (array).
    {
        let obj = root.as_object_mut().expect("JSON root must be object");
        obj.entry("hooks")
            .or_insert_with(|| serde_json::Value::Object(serde_json::Map::new()));
    }
    {
        let hooks_obj = root["hooks"].as_object_mut().unwrap();
        hooks_obj
            .entry("PostToolUse")
            .or_insert_with(|| serde_json::Value::Array(Vec::new()));
    }
    root["hooks"]["PostToolUse"]
        .as_array_mut()
        .unwrap()
        .push(hook_entry);

    // Serialize to pretty JSON.
    let new_text = match serde_json::to_string_pretty(&root) {
        Ok(t) => t,
        Err(e) => {
            warn!(err = %e, "mcp_registration: failed to serialize settings.json for security_hook");
            return;
        }
    };

    // Atomic write via temp file + rename.
    let tmp_path = settings_path.with_extension("json.tmp");
    if let Err(e) = std::fs::write(&tmp_path, &new_text) {
        warn!(path = %tmp_path.display(), err = %e, "mcp_registration: failed to write temp settings file for security_hook");
        return;
    }
    if let Err(e) = std::fs::rename(&tmp_path, &settings_path) {
        warn!(
            from = %tmp_path.display(),
            to = %settings_path.display(),
            err = %e,
            "mcp_registration: failed to rename temp settings file for security_hook"
        );
        let _ = std::fs::remove_file(&tmp_path);
        return;
    }

    info!(
        path = %settings_path.display(),
        "mcp_registration: registered CC PostToolUse security scan hook"
    );
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use std::path::Path;
    use tempfile::TempDir;

    /// Helper: create cascade_dir structure and set HOME to `home_dir`.
    fn setup(cascade_dir: &Path, home_dir: &Path) {
        fs::create_dir_all(cascade_dir).unwrap();
        // Ensure .claude dir exists in home.
        fs::create_dir_all(home_dir.join(".claude")).unwrap();
        std::env::set_var("HOME", home_dir.as_os_str());
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_register_cc_stop_hook_idempotent() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        register_cc_stop_hook().await;
        let settings_path = home.path().join(".claude").join("settings.json");
        let content1 = fs::read_to_string(&settings_path).unwrap_or_default();

        register_cc_stop_hook().await;
        let content2 = fs::read_to_string(&settings_path).unwrap_or_default();

        assert_eq!(content1, content2, "second register_cc_stop_hook must not change content");

        // Verify the hook is present
        let val: serde_json::Value = serde_json::from_str(&content1).unwrap();
        let stop_hooks = val["hooks"]["Stop"].as_array();
        assert!(stop_hooks.is_some(), "hooks.Stop must exist");
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_register_security_hook_writes_post_tool_use_entry() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        register_security_hook().await;

        let settings_path = home.path().join(".claude").join("settings.json");
        let text = fs::read_to_string(&settings_path).unwrap();
        let val: serde_json::Value = serde_json::from_str(&text).unwrap();

        // PostToolUse array must exist with our entry.
        let arr = val["hooks"]["PostToolUse"]
            .as_array()
            .expect("hooks.PostToolUse must be an array");
        assert_eq!(arr.len(), 1, "exactly one PostToolUse entry");
        assert_eq!(arr[0]["matcher"], "Write|Edit");
        let hooks = arr[0]["hooks"].as_array().unwrap();
        assert_eq!(hooks.len(), 1);
        assert_eq!(hooks[0]["type"], "command");
        assert!(
            hooks[0]["command"]
                .as_str()
                .unwrap()
                .contains("cascade security scan-hook")
        );
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_register_security_hook_idempotent() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        register_security_hook().await;
        let settings_path = home.path().join(".claude").join("settings.json");
        let content1 = fs::read_to_string(&settings_path).unwrap();

        // Second call must be a no-op.
        register_security_hook().await;
        let content2 = fs::read_to_string(&settings_path).unwrap();

        assert_eq!(content1, content2, "second register_security_hook must not change content");

        // Still exactly one entry.
        let val: serde_json::Value = serde_json::from_str(&content1).unwrap();
        let arr = val["hooks"]["PostToolUse"].as_array().unwrap();
        assert_eq!(arr.len(), 1, "idempotent: must not duplicate the entry");
    }

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn test_register_security_hook_preserves_other_keys() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let home = TempDir::new().unwrap();
        let cascade_dir = home.path().join(".cascade");
        setup(&cascade_dir, home.path());

        // Pre-populate with an existing Stop hook and a theme key.
        let settings_path = home.path().join(".claude").join("settings.json");
        let initial = serde_json::json!({
            "theme": "dark",
            "hooks": {
                "Stop": [{ "matcher": "", "hooks": [{ "type": "command", "command": "echo done" }] }]
            }
        });
        fs::write(&settings_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        register_security_hook().await;

        let text = fs::read_to_string(&settings_path).unwrap();
        let val: serde_json::Value = serde_json::from_str(&text).unwrap();

        // theme preserved.
        assert_eq!(val["theme"], "dark");
        // Stop hook preserved.
        let stop_arr = val["hooks"]["Stop"].as_array().unwrap();
        assert_eq!(stop_arr.len(), 1);
        // PostToolUse added.
        let ptu_arr = val["hooks"]["PostToolUse"].as_array().unwrap();
        assert_eq!(ptu_arr.len(), 1);
    }
}
