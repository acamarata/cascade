// Tauri IPC command surface — bridges the React frontend to the cascaded daemon.
//
// Why: every public function here becomes a callable IPC endpoint via
// `invoke("<command_name>")` on the JS side. Naming: snake_case here maps to
// camelCase on the JS side via Tauri's automatic transformation.
//
// Architecture: each command builds an IpcClient using the socket path from
// AppState, calls the daemon via JSON-RPC 2.0, and maps IpcClientError to
// CascadeError so the frontend receives a structured JSON error payload.
//
// IPC contract: cascade_types::ipc — FROZEN at schema v1. Add methods by
// appending only; never rename or remove a method in this file.
//
// Remaining TODOs (scope boundaries):
//   cascade_inbox_send — T-P4-E01: daemon inbox_send method not yet in the
//     JSON-RPC contract (ipc.rs v1). Stubbed with NotImplemented.
//   load_cascade_doc, save_cascade_doc, validate_cascade_doc — T-P3-E03
//   list_inbox — replaced by cascade_inbox_list / cascade_inbox_send split (T-P3-E01-07)
//   rag_query — T-P4-E01: cascade_rag crate not yet available.
//   get_config / set_config renamed cascade_config_get / cascade_config_set in this ticket.
//
// SPORT: MASTER-COMMANDS.md in .claude/docs — update when adding/removing commands.

// ---------------------------------------------------------------------------
// Sub-modules — domain split (B1 modularization)
// ---------------------------------------------------------------------------

/// Core IPC bridge: cascade_status, resolve, search, inbox, memory, config.
pub mod core;
/// Daemon lifecycle (start/stop/status), window management, CASCADE.md stubs.
pub mod daemon;
/// RAG search commands wired to the daemon rag.search endpoint.
pub mod rag;
/// AI provider connection, local model download, daemon install.
pub mod setup;
/// Wizard state types and checkpoint persistence / audit-log commands.
pub mod wizard;

// Sub-modules added by W-02 (T-P3-E03-09 scaffold):
/// Encrypted personal vault commands (rag-16-ui).
pub mod personal_vault;
pub mod scanner;
pub mod symlinks;
// Sub-module added by W-03 (T-P3-E03-15 scaffold):
pub mod archive;
// Sub-module added by W-04 (T-P3-E03-23 scaffold):
pub mod merge;
// Sub-module added by W-05 (T-P3-E03-39/39b/40/41/42 scaffold):
pub mod provision;
// T-P3-E04-26/27: Provider IPC commands + usage today
pub mod providers;
// T-P3-E04-30: Usage analytics IPC commands
pub mod usage;
// T-P3-E05-15/17: Template engine IPC commands (list/apply/diff/upgrade)
pub mod templates;
// T-P3-E06-01: Vault file-system IPC commands (list/read/write/watch)
pub mod vault;
// T-P3-E07-00: Application settings IPC commands (get/update)
pub mod settings;
// T-E01-01: Accounts page IPC commands (list/reauth/remove/refresh)
pub mod accounts;
// T-P3-E07-02/03: Library item IPC commands (list/get/upsert/delete)
pub mod library;
// T-P3-E07-08: Context session IPC commands (list/get/upsert/delete)
pub mod context;
// T-P3-E07-12: Project maps data providers (project graph, cascade tier tree, PEWS DAG)
pub mod maps;
// E-P8-02: Kanban task IPC commands (create/get/list/update/delete/move)
pub mod tasks;
// E-P8-05: PBD PEWS tree visualization + mutation IPC commands
pub mod pbd;
// Wizard state persistence + dry-run log + merge audit (fix-6 wizard end-to-end)
pub mod wizard_state;
// T-P7-E08-01: Credentials vault.env read-only IPC commands (vault_path, read_vault_keys)
pub mod vault_env;
// T-P7-E10-02: Harness config-file auto-detection (detect_harness_path)
pub mod harness;

// ---------------------------------------------------------------------------
// Public re-exports — keep every previously-public item at its original path
// ---------------------------------------------------------------------------

// core
pub use core::{
    cascade_config_get,
    cascade_config_set,
    cascade_inbox_list,
    cascade_inbox_send,
    cascade_memory_read,
    cascade_memory_write,
    cascade_resolve,
    cascade_search,
    cascade_status,
    // type re-exports
    CascadeConfigGetResult,
    CascadeConfigSetResult,
    CascadeInboxSummaryResult,
    CascadeMemoryReadResult,
    CascadeMemoryWriteResult,
    CascadeResolveResult,
    CascadeSearchResult,
    CascadeStatusResult,
    InboxSendAck,
    RagResult,
};

// daemon
pub use daemon::{
    cascade_close_window,
    cascade_focus_window,
    cascade_open_window,
    get_daemon_status,
    load_cascade_doc,
    save_cascade_doc,
    start_daemon,
    stop_daemon,
    validate_cascade_doc,
    // types
    CascadeDocument,
    DaemonStatus,
    ValidationResult,
};

// rag
pub use rag::rag_query;

// wizard
pub use wizard::{
    check_wizard_status,
    wizard_check_audit_format,
    wizard_clear_checkpoint,
    wizard_load_checkpoint,
    wizard_rotate_audit_log,
    wizard_save_checkpoint,
    // types
    AuditCheckResult,
    MergeResult,
    ScanResult,
    WizardCheckpoint,
    WizardStatus,
};

// setup
pub use setup::{
    detect_gemini_pool,
    download_local_model,
    install_daemon,
    provider_connect,
    wizard_mark_complete,
    // types
    DaemonInstallResult,
    GeminiPoolDetectResult,
    LocalModelDownloadResult,
    ProviderConnectResult,
};
