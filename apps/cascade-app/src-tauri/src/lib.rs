// Cascade app library crate — Tauri app builder.
//
// Why: Tauri 2 separates the binary entry point (main.rs) from the app logic
// so the same code can be tested and reused as a library. This crate owns
// the Tauri Builder configuration, plugin registration, and state injection.
//
// State: AppState now holds the daemon socket path (resolved from CASCADE_SOCKET
// env or the canonical ~/.cascade/daemon.sock). T-P3-E01-06 wires this through
// to every command handler via tauri::State<AppState>.

pub mod commands;
pub mod error;
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
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
