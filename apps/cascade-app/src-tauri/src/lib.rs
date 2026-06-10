// Cascade app library crate — Tauri app builder.
//
// Why: Tauri 2 separates the binary entry point (main.rs) from the app logic
// so the same code can be tested and reused as a library. This crate owns
// the Tauri Builder configuration, plugin registration, and state injection.
//
// State: AppState now holds the daemon socket path (resolved from CASCADE_SOCKET
// env or the canonical ~/.cascade/daemon.sock). T-P3-E01-06 wires this through
// to every command handler via tauri::State<AppState>.

pub mod archive;
pub mod commands;
pub mod error;
pub mod merge;
pub mod scanner;
pub mod state;

use state::AppState;
use tracing_subscriber::{EnvFilter, fmt};

/// Initialize the tracing subscriber.
/// JSON output to stderr; level from RUST_LOG env (default: info).
fn init_tracing() {
    let filter = EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| EnvFilter::new("info"));
    fmt()
        .with_env_filter(filter)
        .with_target(true)
        .json()
        .init();
}

/// Tauri app entry point, called from main.rs.
/// All plugin registration and state injection happens here.
pub fn run() {
    init_tracing();

    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_fs::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .manage(AppState::from_env())
        // T-P3-E01-06: 9 required daemon-bridge commands
        .invoke_handler(tauri::generate_handler![
            commands::cascade_status,
            commands::cascade_resolve,
            commands::cascade_search,
            commands::cascade_inbox_list,
            commands::cascade_inbox_send,
            commands::cascade_memory_read,
            commands::cascade_memory_write,
            commands::cascade_config_get,
            commands::cascade_config_set,
            // Compatibility / legacy commands (kept for frontend backward compat)
            commands::get_daemon_status,
            commands::start_daemon,
            commands::stop_daemon,
            commands::load_cascade_doc,
            commands::save_cascade_doc,
            commands::validate_cascade_doc,
            commands::rag_query,
            // T-P3-E01-17: multi-window commands
            commands::cascade_open_window,
            commands::cascade_close_window,
            commands::cascade_focus_window,
            // T-P3-E03-07: Wizard first-run detection
            commands::check_wizard_status,
            // T-P3-E03-03: Wizard checkpoint persistence
            commands::wizard_save_checkpoint,
            commands::wizard_load_checkpoint,
            commands::wizard_clear_checkpoint,
            // T-P3-E03-43: Audit log format check and rotation
            commands::wizard_check_audit_format,
            commands::wizard_rotate_audit_log,
            // T-P3-E03-06: AI provider connection + Gemini Pool detection
            commands::detect_gemini_pool,
            commands::provider_connect,
            commands::download_local_model,
            // T-P3-E03-08: Daemon install + wizard-complete marker
            commands::install_daemon,
            commands::wizard_mark_complete,
            // T-P3-E03-10/11: Legacy tool home scanner commands
            commands::scanner::scan_global_homes,
            commands::scanner::scan_dev_tree,
            // T-P3-E03-14: Symlink setup for cascade-managed tools
            commands::symlinks::setup_symlinks,
            // T-P3-E03-37: Per-tool mode flip — unlink (Independent) / link (Cascade-managed)
            commands::symlinks::cascade_unlink_tool,
            // T-P3-E03-16..19: Archive / restore subsystem
            commands::archive::archive_legacy_tools,
            commands::archive::read_archive_manifest,
            commands::archive::list_archived_tools,
            commands::archive::archive_preflight,
            commands::archive::restore_tool,
            // T-P3-E03-23..30: AI merge engine
            commands::merge::read_legacy_content,
            commands::merge::run_ai_merge,
            commands::merge::write_cascade_content,
            commands::merge::detect_merge_conflicts,
            commands::merge::get_merge_prompts,
            // T-P3-E03-39b: GCP provisioning commands
            commands::provision::cascade_provision_google_start,
            commands::provision::cascade_provision_google_status,
            commands::provision::cascade_provision_google_cancel,
            // T-P3-E03-40: Gemini Pool registration
            commands::provision::cascade_pool_register_key,
            commands::provision::cascade_pool_deregister_key,
            // T-P3-E03-41: Auto-auth scan + import
            commands::provision::cascade_auto_auth_scan,
            commands::provision::cascade_auto_auth_import,
            // T-P3-E03-42: AI-optional provider health check
            commands::provision::cascade_providers_health,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
