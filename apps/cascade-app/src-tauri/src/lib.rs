// Cascade app library crate — Tauri app builder.
//
// Why: Tauri 2 separates the binary entry point (main.rs) from the app logic
// so the same code can be tested and reused as a library. This crate owns
// the Tauri Builder configuration, plugin registration, and state injection.
//
// State: AppState now holds the daemon socket path (resolved from CASCADE_SOCKET
// env or the canonical ~/.cascade/daemon.sock). T-P3-E01-06 wires this through
// to every command handler via tauri::State<AppState>.
//
// T-P3-E04-15: AppState.provider_health is populated by a background health-check
// task spawned in the Tauri setup hook.  `cascade_providers_health` reads from
// this cache.

pub mod archive;
pub mod commands;
pub mod error;
pub mod merge;
pub mod scanner;
pub mod state;

use std::sync::Arc;
use std::time::{Duration, Instant};

use state::{AppProviderHealth, AppState};
use tauri::Manager;
use tracing::{info, warn};
use tracing_subscriber::{fmt, EnvFilter};

use cascade_core::providers_store::read_providers_store;
use cascade_providers::{NoopProvider, ProviderRegistry};
use cascade_types::paths::global_cascade_dir;

/// Initialize the tracing subscriber.
/// JSON output to stderr; level from RUST_LOG env (default: info).
fn init_tracing() {
    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
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

    let app_state = AppState::from_env();

    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_fs::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_shell::init())
        .manage(app_state)
        // T-P3-E04-15: spawn provider health-check background task after state is managed.
        .setup(|app| {
            let health_map = app.state::<AppState>().provider_health.clone();

            // Build ProviderRegistry from providers.json (NoopProvider placeholders;
            // concrete adapters land in P4).
            let registry = Arc::new(ProviderRegistry::new());
            let config_dir = global_cascade_dir();
            let providers_path = config_dir.join("providers.json");

            {
                let entries = read_providers_store(&providers_path)
                    .map(|s| s.providers)
                    .unwrap_or_default();
                for entry in entries.into_iter().filter(|p| p.enabled) {
                    let id = entry.id.clone();
                    if let Err(e) = registry.register(id.clone(), Arc::new(NoopProvider)) {
                        warn!(provider_id = %id, error = %e, "app: failed to register provider");
                    }
                }
            }

            // Spawn the background health task.  Fire-and-forget: Tauri manages
            // its own async runtime so we use tokio::spawn directly.
            tokio::spawn(run_app_health_task(registry, health_map));
            info!("app provider health-check task spawned");

            Ok(())
        })
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
            // T-P3-E04-26: Provider IPC commands (list/test/remove/add-key/oauth/generic)
            commands::providers::cascade_providers_list,
            commands::providers::cascade_providers_test,
            commands::providers::cascade_providers_remove,
            commands::providers::cascade_providers_add_apikey,
            commands::providers::cascade_providers_oauth_start,
            commands::providers::cascade_providers_oauth_status,
            commands::providers::cascade_providers_add_generic,
            // T-P3-E04-27: Usage today IPC endpoint
            commands::providers::cascade_providers_usage_today,
            // T-P3-E04-26: GFP proxy health probe
            commands::providers::cascade_check_gfp_proxy,
            // T-P3-E04-30: Usage analytics
            commands::usage::cascade_usage_summary,
            commands::usage::cascade_usage_history,
            commands::usage::cascade_usage_ledger,
            // T-P3-E05-15: Template engine IPC (list / apply / diff)
            commands::templates::template_list,
            commands::templates::template_apply,
            commands::templates::template_diff,
            // T-P3-E05-17: Template upgrade IPC
            commands::templates::template_upgrade,
            // T-P3-E06-01: Vault file-system IPC commands
            commands::vault::vault_list,
            commands::vault::vault_read,
            commands::vault::vault_write,
            commands::vault::vault_watch,
            // T-P3-E06-09: Vault full-text search via ripgrep
            commands::vault::vault_search,
            // T-P3-E07-00: Application settings IPC commands
            commands::settings::get_settings,
            commands::settings::update_settings,
            // T-P3-E07-02: Library read IPC commands
            commands::library::list_library_items,
            commands::library::get_library_item,
            // T-P3-E07-03: Library write IPC commands
            commands::library::upsert_library_item,
            commands::library::delete_library_item,
            // T-P3-E07-07: Library injection IPC command
            commands::library::inject_library_item,
            // T-P3-E07-08: Context session IPC commands
            commands::context::context_list,
            commands::context::context_get,
            commands::context::context_upsert,
            commands::context::context_delete,
            // T-P3-E07-09: Codebase packing (repomix-style)
            commands::context::pack_codebase_cmd,
            // T-P3-E07-12: Project maps data providers
            commands::maps::get_project_graph,
            commands::maps::get_cascade_tier_tree,
            commands::maps::get_pews_dag,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}

// ── Provider health background task (T-P3-E04-15) ────────────────────────────

/// Background health-check loop for the Tauri app process.
///
/// Purpose: Runs health_check_all() on the local ProviderRegistry every 5
/// minutes, caches results in AppState.provider_health.  First sweep fires
/// immediately; refresh repeats on a 5-minute interval.  Never panics on an
/// empty registry — empty sweep produces empty state.
///
/// Inputs: `registry` — shared registry (NoopProvider placeholders in P3).
///         `health_map` — AppState.provider_health shared via Arc.
/// Outputs: writes AppProviderHealth entries into health_map after each sweep.
/// Constraints: non-blocking from caller; spawned via tokio::spawn in setup().
async fn run_app_health_task(
    registry: Arc<ProviderRegistry>,
    health_map: state::ProviderHealthMap,
) {
    let interval = Duration::from_secs(300); // 5 minutes

    // First sweep fires immediately.
    app_health_sweep(&registry, &health_map).await;

    let mut ticker = tokio::time::interval(interval);
    ticker.tick().await; // consume the immediate first tick

    loop {
        ticker.tick().await;
        app_health_sweep(&registry, &health_map).await;
    }
}

/// Run one health sweep: call health_check_all, update AppState cache.
async fn app_health_sweep(registry: &ProviderRegistry, health_map: &state::ProviderHealthMap) {
    let results = registry.health_check_all().await;
    let mut map = health_map.write().await;
    for (id, result) in &results {
        let ok = result.is_ok();
        let error_msg = result.as_ref().err().map(|e| e.to_string());
        if !ok {
            warn!(provider_id = %id, "app provider health check failed");
        }
        map.insert(
            id.clone(),
            AppProviderHealth {
                ok,
                checked_at: Instant::now(),
                error_msg,
            },
        );
    }
    let total = results.len();
    let healthy = results.values().filter(|r| r.is_ok()).count();
    info!(total, healthy, "app provider health sweep complete");
}
