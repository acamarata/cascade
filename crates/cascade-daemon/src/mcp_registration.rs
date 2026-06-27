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
//! ## TODO(frame-01)
//!
//! This stub marks the trigger point for the frame-01 ticket that owns the
//! actual settings.json write. The full implementation must:
//!   1. Resolve the harness settings path (e.g. `~/.claude/settings.json`).
//!   2. Parse the existing JSON without clobbering unmanaged keys.
//!   3. Upsert the `mcpServers.cascade` entry with the socket path.
//!   4. Write atomically (temp-file + rename).
//!   5. Log the registration at INFO level.
//!
//! SPORT: MASTER-DAEMON.md → mcp_registration (rag-11 trigger point)

use std::path::Path;

use tracing::{info, warn};

/// Register the cascade MCP server with all detected AI harnesses.
///
/// Called from `supervisor::run` when `mcp.enabled = true`. Currently a stub;
/// the settings.json write is implemented in frame-01.
///
/// # Errors
///
/// Never returns an error — all failures are logged as WARN and swallowed so
/// the daemon continues without MCP if registration fails.
pub async fn register(config_dir: &Path) {
    // TODO(frame-01): implement the actual settings.json write.
    // The stub logs the intent and returns, keeping the daemon operational.
    info!(
        config_dir = %config_dir.display(),
        "mcp_registration: trigger point reached — frame-01 owns the settings.json write"
    );

    // Derive the expected socket path so frame-01 can read it from the log.
    let socket_path = config_dir.join("mcp.sock");
    if !socket_path.exists() {
        warn!(
            socket = %socket_path.display(),
            "mcp_registration: socket not yet created — registration deferred until socket is ready"
        );
        return;
    }

    // TODO(frame-01): replace the warn below with the real write.
    warn!(
        socket = %socket_path.display(),
        "mcp_registration: socket exists but harness write is not yet implemented (frame-01)"
    );
}
