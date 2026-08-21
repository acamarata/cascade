//! Tests for `cascade mcp` subcommands.
#![cfg(test)]

use cascade_mcp::McpSecretManager;
use serde_json::{json, Value};
use serial_test::serial;
use tempfile::TempDir;

use super::{
    clients::{
        claude_desktop_config_path, setup_all, setup_claude_code, setup_claude_desktop,
        setup_opencode, setup_vscode,
    },
    token::McpTokenArgs,
};

// Helper: set HOME to a tempdir and return the guard.
fn temp_home() -> TempDir {
    TempDir::new().expect("tempdir")
}

// ── Token ─────────────────────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn token_missing_key_file_returns_error() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let result = McpTokenArgs::mint_token();
    assert!(result.is_err(), "missing key file should error");
    let msg = result.unwrap_err().to_string();
    assert!(
        msg.contains("not running") || msg.contains("not yet initialized"),
        "unexpected error message: {msg}"
    );
}

#[test]
#[serial(global_env)]
fn token_existing_key_file_returns_valid_token() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    // Create the secret key file
    let runtime_dir = tmp.path().join(".cascade").join("runtime");
    std::fs::create_dir_all(&runtime_dir).unwrap();
    let key_path = runtime_dir.join("mcp-secret.key");
    McpSecretManager::load_or_create(&key_path).expect("create key");

    let token = McpTokenArgs::mint_token().expect("token from valid key");
    assert!(
        token.starts_with("cascade-mcp-"),
        "token must start with 'cascade-mcp-': {token}"
    );
}

// ── Claude Code setup ─────────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn setup_claude_code_creates_settings_with_cascade_entry() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let settings_path = tmp.path().join(".claude").join("settings.json");
    std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

    setup_claude_code(false, false).expect("setup should succeed");

    let raw = std::fs::read_to_string(&settings_path).expect("settings.json written");
    let parsed: Value = serde_json::from_str(&raw).expect("valid JSON");
    assert_eq!(
        parsed["mcpServers"]["cascade"]["command"],
        json!("cascade"),
        "command must be 'cascade'"
    );
    assert_eq!(
        parsed["mcpServers"]["cascade"]["args"],
        json!(["mcp", "stdio"]),
        "args must be ['mcp', 'stdio']"
    );
}

#[test]
#[serial(global_env)]
fn setup_claude_code_idempotent() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let settings_path = tmp.path().join(".claude").join("settings.json");
    std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

    setup_claude_code(false, false).expect("first setup");
    setup_claude_code(false, false).expect("second setup");

    let raw = std::fs::read_to_string(&settings_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    let servers = parsed["mcpServers"].as_object().unwrap();
    let count = servers.keys().filter(|k| *k == "cascade").count();
    assert_eq!(count, 1, "exactly one cascade entry after two setup runs");
}

#[test]
#[serial(global_env)]
fn setup_claude_code_remove_clears_entry_preserves_others() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let settings_path = tmp.path().join(".claude").join("settings.json");
    std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

    // Pre-populate with another server + cascade
    let initial = json!({
        "mcpServers": {
            "other-tool": { "command": "other", "args": [] },
            "cascade": { "command": "cascade", "args": ["mcp", "stdio"], "env": {} }
        }
    });
    std::fs::write(
        &settings_path,
        serde_json::to_string_pretty(&initial).unwrap(),
    )
    .unwrap();

    setup_claude_code(true, false).expect("remove should succeed");

    let raw = std::fs::read_to_string(&settings_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    assert!(
        parsed["mcpServers"].get("cascade").is_none() || parsed["mcpServers"]["cascade"].is_null(),
        "cascade entry should be removed"
    );
    assert_eq!(
        parsed["mcpServers"]["other-tool"]["command"],
        json!("other"),
        "other entries must be preserved"
    );
}

#[test]
#[serial(global_env)]
fn setup_claude_code_dry_run_does_not_write() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let settings_path = tmp.path().join(".claude").join("settings.json");
    std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

    setup_claude_code(false, true).expect("dry-run should not error");
    assert!(!settings_path.exists(), "dry-run must not write file");
}

#[test]
#[serial(global_env)]
fn setup_claude_code_preserves_existing_settings() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let settings_path = tmp.path().join(".claude").join("settings.json");
    std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

    // Write existing settings with unrelated keys
    let initial = json!({
        "theme": "dark",
        "verbose": true,
        "mcpServers": { "existing-tool": { "command": "existing" } }
    });
    std::fs::write(
        &settings_path,
        serde_json::to_string_pretty(&initial).unwrap(),
    )
    .unwrap();

    setup_claude_code(false, false).unwrap();

    let raw = std::fs::read_to_string(&settings_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    assert_eq!(parsed["theme"], json!("dark"), "theme must be preserved");
    assert_eq!(parsed["verbose"], json!(true), "verbose must be preserved");
    assert_eq!(
        parsed["mcpServers"]["existing-tool"]["command"],
        json!("existing"),
        "existing server must be preserved"
    );
    assert_eq!(
        parsed["mcpServers"]["cascade"]["command"],
        json!("cascade"),
        "cascade must be added"
    );
}

// ── Claude Desktop setup ──────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn setup_claude_desktop_path_resolves_correctly_on_macos() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    if std::env::consts::OS != "macos" {
        // Only run on macOS
        return;
    }

    let path = claude_desktop_config_path().expect("macOS should return a path");
    let s = path.to_str().unwrap();
    assert!(
        s.contains("Application Support/Claude/claude_desktop_config.json"),
        "unexpected path: {s}"
    );
}

#[test]
#[serial(global_env)]
fn setup_claude_desktop_missing_config_exits_gracefully() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    // Don't create the config file — Claude Desktop "not installed"
    let result = setup_claude_desktop(false, false);
    assert!(result.is_ok(), "missing config should exit 0 gracefully");
}

#[test]
#[serial(global_env)]
fn setup_claude_desktop_creates_entry_with_url_and_auth() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    // Create a MCP key so token mint works
    let runtime_dir = tmp.path().join(".cascade").join("runtime");
    std::fs::create_dir_all(&runtime_dir).unwrap();
    McpSecretManager::load_or_create(&runtime_dir.join("mcp-secret.key")).unwrap();

    // Create the Claude Desktop config directory + empty file
    let Some(config_path) = claude_desktop_config_path() else {
        return;
    };
    std::fs::create_dir_all(config_path.parent().unwrap()).unwrap();
    std::fs::write(&config_path, "{}").unwrap();

    setup_claude_desktop(false, false).expect("setup should succeed");

    let raw = std::fs::read_to_string(&config_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    let entry = &parsed["mcpServers"]["cascade"];
    assert_eq!(
        entry["url"],
        json!("http://127.0.0.1:7722/mcp"),
        "url must be correct"
    );
    assert!(
        entry["headers"]["Authorization"]
            .as_str()
            .unwrap_or("")
            .starts_with("Bearer cascade-mcp-")
            || entry["headers"]["Authorization"]
                .as_str()
                .unwrap_or("")
                .starts_with("Bearer <"),
        "Authorization header must be a Bearer token"
    );
}

// ── VS Code setup ─────────────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn setup_vscode_creates_mcp_json_with_stdio_entry() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    // Use a temp cwd
    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(&project_dir).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    setup_vscode(false, false, false).expect("setup should succeed");

    let mcp_path = project_dir.join(".vscode").join("mcp.json");
    assert!(mcp_path.exists(), ".vscode/mcp.json must be created");

    let raw = std::fs::read_to_string(&mcp_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).expect("must be valid JSON");
    assert_eq!(
        parsed["servers"]["cascade"]["type"],
        json!("stdio"),
        "type must be stdio"
    );
    assert_eq!(
        parsed["servers"]["cascade"]["command"],
        json!("cascade"),
        "command must be cascade"
    );
}

#[test]
#[serial(global_env)]
fn setup_vscode_idempotent() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(&project_dir).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    setup_vscode(false, false, false).unwrap();
    setup_vscode(false, false, false).unwrap();

    let mcp_path = project_dir.join(".vscode").join("mcp.json");
    let raw = std::fs::read_to_string(&mcp_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    let count = parsed["servers"]
        .as_object()
        .unwrap()
        .keys()
        .filter(|k| *k == "cascade")
        .count();
    assert_eq!(count, 1, "exactly one cascade entry after two runs");
}

#[test]
#[serial(global_env)]
fn setup_vscode_remove_preserves_other_servers() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(project_dir.join(".vscode")).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    let mcp_path = project_dir.join(".vscode").join("mcp.json");
    let initial = json!({
        "servers": {
            "other": { "type": "stdio", "command": "other" },
            "cascade": { "type": "stdio", "command": "cascade", "args": ["mcp", "stdio"] }
        }
    });
    std::fs::write(&mcp_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

    setup_vscode(true, false, false).unwrap();

    let raw = std::fs::read_to_string(&mcp_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    assert!(
        parsed["servers"].get("cascade").is_none() || parsed["servers"]["cascade"].is_null(),
        "cascade must be removed"
    );
    assert_eq!(
        parsed["servers"]["other"]["command"],
        json!("other"),
        "other server must survive"
    );
}

#[test]
#[serial(global_env)]
fn setup_vscode_dry_run_does_not_write() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(&project_dir).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    setup_vscode(false, false, true).expect("dry-run should not error");
    let mcp_path = project_dir.join(".vscode").join("mcp.json");
    assert!(!mcp_path.exists(), "dry-run must not write file");
}

// ── OpenCode setup ────────────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn setup_opencode_global_not_installed_exits_gracefully() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());
    // Isolate PATH to avoid finding a real opencode binary
    std::env::set_var("PATH", tmp.path().to_str().unwrap());

    let result = setup_opencode(false, false, false, false);
    assert!(result.is_ok(), "missing opencode should exit 0 gracefully");
}

#[test]
#[serial(global_env)]
fn setup_opencode_local_creates_opencode_json() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(&project_dir).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    setup_opencode(false, true, false, false).expect("local setup should succeed");

    let oc_path = project_dir.join("opencode.json");
    assert!(oc_path.exists(), "opencode.json must be created");

    let raw = std::fs::read_to_string(&oc_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).expect("valid JSON");
    assert_eq!(
        parsed["mcpServers"]["cascade"]["type"],
        json!("stdio"),
        "type must be stdio"
    );
}

#[test]
#[serial(global_env)]
fn setup_opencode_http_flag_writes_http_entry() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    // Create a MCP key for token mint
    let runtime_dir = tmp.path().join(".cascade").join("runtime");
    std::fs::create_dir_all(&runtime_dir).unwrap();
    McpSecretManager::load_or_create(&runtime_dir.join("mcp-secret.key")).unwrap();

    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(&project_dir).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    setup_opencode(false, true, true, false).expect("http setup should succeed");

    let oc_path = project_dir.join("opencode.json");
    let raw = std::fs::read_to_string(&oc_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    let entry = &parsed["mcpServers"]["cascade"];
    assert_eq!(entry["type"], json!("http"), "type must be http");
    assert_eq!(
        entry["url"],
        json!("http://127.0.0.1:7722/mcp"),
        "url must be correct"
    );
}

#[test]
#[serial(global_env)]
fn setup_opencode_idempotent() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let project_dir = tmp.path().join("project");
    std::fs::create_dir_all(&project_dir).unwrap();
    std::env::set_current_dir(&project_dir).unwrap();

    setup_opencode(false, true, false, false).unwrap();
    setup_opencode(false, true, false, false).unwrap();

    let oc_path = project_dir.join("opencode.json");
    let raw = std::fs::read_to_string(&oc_path).unwrap();
    let parsed: Value = serde_json::from_str(&raw).unwrap();
    let count = parsed["mcpServers"]
        .as_object()
        .unwrap()
        .keys()
        .filter(|k| *k == "cascade")
        .count();
    assert_eq!(count, 1, "exactly one cascade entry");
}

// ── Setup --all ───────────────────────────────────────────────────────────

#[test]
#[serial(global_env)]
fn setup_all_detects_at_least_claude_code_when_settings_present() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    // Create ~/.claude/settings.json so Claude Code is detected
    let claude_dir = tmp.path().join(".claude");
    std::fs::create_dir_all(&claude_dir).unwrap();
    std::fs::write(claude_dir.join("settings.json"), "{}").unwrap();

    let result = setup_all(false, false);
    assert!(
        result.is_ok(),
        "setup_all should succeed when at least one client detected"
    );
}

#[test]
#[serial(global_env)]
fn setup_all_no_clients_returns_error() {
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());
    std::env::set_var("PATH", tmp.path().to_str().unwrap());

    // No clients present
    let result = setup_all(false, false);
    assert!(result.is_err(), "should error when no clients detected");
}

// ── The written config must be a command the CLI actually accepts ────────────
//
// T-P7-E25-17: every client config wrote `["mcp", "--stdio"]`, but the CLI
// defines `stdio` as a SUBCOMMAND, not a flag. clap rejected it with exit 2, so
// the MCP server never started for any tool wired up by `cascade mcp setup` —
// Claude Code, Claude Desktop, VS Code, Codex and OpenCode alike. The old tests
// asserted the broken string, so the suite confirmed the bug instead of
// catching it.
//
// These tests pin the written args against the CLI's OWN parser, so the two
// cannot drift apart again.

/// Parse an argv through the real `cascade mcp` argument types.
///
/// Wrapping `McpArgs` in a throwaway `Parser` exercises the SAME derive the
/// binary uses, so a config the binary would reject is rejected here too.
fn cli_accepts(args: &[&str]) -> bool {
    use clap::Parser;

    #[derive(Parser)]
    struct Harness {
        #[command(subcommand)]
        _cmd: HarnessCmd,
    }

    #[derive(clap::Subcommand)]
    enum HarnessCmd {
        Mcp(super::args::McpArgs),
    }

    let mut argv = vec!["cascade"];
    argv.extend_from_slice(args);
    Harness::try_parse_from(argv).is_ok()
}

#[test]
fn the_stdio_invocation_we_write_is_accepted_by_the_cli() {
    assert!(
        cli_accepts(&["mcp", "stdio"]),
        "`cascade mcp stdio` must parse — it is what every client config invokes"
    );
}

#[test]
fn the_old_flag_form_is_genuinely_rejected() {
    // Guards against "fixing" this by adding a --stdio alias and quietly
    // leaving the configs on a form that only sometimes works.
    assert!(
        !cli_accepts(&["mcp", "--stdio"]),
        "`mcp --stdio` is not a real invocation; if it becomes one, revisit T-P7-E25-17"
    );
}

#[test]
#[serial(global_env)]
fn what_the_real_writer_emits_is_accepted_by_the_cli() {
    // The strongest form of this check: run the ACTUAL config writer, read the
    // args it wrote, and feed them to the ACTUAL parser. A hardcoded expected
    // string is what let the original bug live in the test suite.
    let _env_guard = crate::test_support::ENV_TEST_LOCK
        .lock()
        .unwrap_or_else(|e| e.into_inner());
    let tmp = temp_home();
    std::env::set_var("HOME", tmp.path());

    let settings_path = tmp.path().join(".claude").join("settings.json");
    std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();
    setup_claude_code(false, false).expect("setup should succeed");

    let parsed: Value =
        serde_json::from_str(&std::fs::read_to_string(&settings_path).expect("settings written"))
            .expect("valid JSON");
    let args: Vec<String> = parsed["mcpServers"]["cascade"]["args"]
        .as_array()
        .expect("args array")
        .iter()
        .map(|v| v.as_str().expect("string arg").to_owned())
        .collect();
    let argv: Vec<&str> = args.iter().map(String::as_str).collect();

    assert!(
        cli_accepts(&argv),
        "the writer emitted {argv:?}, which the CLI parser rejects"
    );
}
