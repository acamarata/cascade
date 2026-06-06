// Cascade app library crate — Tauri app builder.
//
// Why: Tauri 2 separates the binary entry point (main.rs) from the app logic
// so the same code can be tested and reused as a library. This crate owns
// the Tauri Builder configuration, plugin registration, and state injection.
//
// Scaffold state (P3-E01 wave 1): CascadeCore is a P3-E02 Rust deliverable.
// Until that lands, AppState holds a unit placeholder so the app compiles and
// the frontend can be developed. See commands.rs TODO markers.

mod commands;

use tracing_subscriber::{EnvFilter, fmt};

/// Application state managed by Tauri.
/// Placeholder until cascade_core::CascadeCore is available (P3-E02).
pub struct AppState;

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
        .manage(AppState)
        .invoke_handler(tauri::generate_handler![
            commands::get_daemon_status,
            commands::start_daemon,
            commands::stop_daemon,
            commands::load_cascade_doc,
            commands::save_cascade_doc,
            commands::validate_cascade_doc,
            commands::list_inbox,
            commands::rag_query,
            commands::get_config,
            commands::set_config,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
