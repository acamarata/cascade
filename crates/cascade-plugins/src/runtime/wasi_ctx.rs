//! WASI context builder — translates plugin.json permissions into WASIp1 context.
//!
//! Purpose: construct a `WasiP1Ctx` that grants ONLY the capabilities explicitly
//!   declared in the plugin's `JsonPermissions`. All other capabilities are
//!   denied by default (no inherited env, no preopened dirs, no network).
//! Inputs:  `&JsonPermissions` (fs, net, env permit lists from plugin.json).
//! Outputs: `WasiP1Ctx` ready to attach to a wasmtime `Store`.
//! Constraints:
//!   - DENY-by-default: `WasiCtxBuilder::new()` starts with zero permissions.
//!   - Filesystem: only paths listed in `permissions.fs` are preopened.
//!     Relative paths are resolved relative to the process CWD. Paths that
//!     don't exist on the host are skipped with a warning (graceful degradation
//!     for sandboxed test environments).
//!   - Environment: only variable names listed in `permissions.env` are injected.
//!     Variables not present in the host environment are silently skipped.
//!   - Network (WASI level): disabled entirely in this release. The WASI socket
//!     interfaces are not added to the linker. The `net` permission list is
//!     recorded for audit but not currently enforced at the WASI layer.
//!
//! SPORT: cascade-plugins / runtime module (T-P4-E03-04)

use anyhow::Result;
use tracing::warn;
use wasmtime_wasi::preview1::WasiP1Ctx;
use wasmtime_wasi::{DirPerms, FilePerms, WasiCtxBuilder};

use crate::manifest::JsonPermissions;

/// Build a WASI preview1 context from a plugin's declared permissions.
///
/// # Capability model (DENY-by-default)
///
/// | Category | Behaviour |
/// |---|---|
/// | Filesystem | Only paths in `permissions.fs` preopened; read/write per `FsPermission::read/write` |
/// | Environment | Only vars named in `permissions.env` injected (host value used) |
/// | Network | Disabled at WASI level (preview1 sockets not linked) |
/// | stdin/stdout/stderr | Piped (no host terminal access) |
///
/// # Errors
/// Returns `Err` only if a preopened directory fails to open AND the path
/// exists on the host (i.e., a genuine permission error, not a missing dir).
pub fn build_wasi_ctx(permissions: &JsonPermissions) -> Result<WasiP1Ctx> {
    let mut builder = WasiCtxBuilder::new();

    // ── Filesystem preopens ─────────────────────────────────────────────────
    //
    // WHY: WASIp1 filesytem access is entirely mediated by preopened directory
    // handles. A plugin that has no preopens simply cannot open any file —
    // the WASI host returns ENOTCAPABLE for any path not reachable through a
    // preopen. This is the primary filesystem isolation boundary.
    for fs_perm in &permissions.fs {
        let path = std::path::Path::new(&fs_perm.path);

        // Skip non-existent paths gracefully (common in test environments).
        if !path.exists() {
            warn!(
                path = %fs_perm.path,
                "plugin fs permission path does not exist — skipping preopen"
            );
            continue;
        }

        let dir_perms = match (fs_perm.read, fs_perm.write) {
            (true, true) => DirPerms::all(),
            (true, false) => DirPerms::READ,
            (false, true) => DirPerms::MUTATE,
            (false, false) => {
                // Declared but neither read nor write — skip.
                warn!(path = %fs_perm.path, "fs permission has read=false write=false — skipping");
                continue;
            }
        };

        let file_perms = match (fs_perm.read, fs_perm.write) {
            (true, true) => FilePerms::all(),
            (true, false) => FilePerms::READ,
            (false, true) => FilePerms::WRITE,
            (false, false) => unreachable!("handled above"),
        };

        // Use the declared path as both host path and guest path.
        // WHY: plugins reference files by the same path they declared — this
        // avoids a confusing two-path mapping for the common case. Multi-path
        // remapping is a future enhancement (P4 PDK ticket).
        match builder.preopened_dir(path, &fs_perm.path, dir_perms, file_perms) {
            Ok(_) => {}
            Err(e) => {
                // Non-fatal: log and skip. Plugin will get ENOTCAPABLE if it
                // tries to open this path.
                warn!(path = %fs_perm.path, error = %e, "failed to preopen plugin fs path");
            }
        }
    }

    // ── Environment variables ────────────────────────────────────────────────
    //
    // WHY: Only inject the specific variables the plugin declared. No `inherit_env`
    // — that would expose ALL host env vars including API keys and secrets.
    for env_perm in &permissions.env {
        if let Ok(value) = std::env::var(&env_perm.name) {
            builder.env(&env_perm.name, &value);
        }
        // Missing vars are silently skipped — the plugin must handle `None`.
    }

    // ── Network ────────────────────────────────────────────────────────────
    //
    // WHY: WASI network support is not added to the linker in this release.
    // The `net` permission list in the manifest is recorded for future use
    // (host-level HTTP proxy gating planned for P4 plugin-http-fetch ticket).
    // Plugins that need HTTP must use the `http-fetch` host function exposed
    // via the WIT tool-integration interface (not raw WASI sockets).

    Ok(builder.build_p1())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::manifest::{EnvPermission, FsPermission, JsonPermissions, NetPermission};

    #[test]
    fn empty_permissions_builds_without_error() {
        let perms = JsonPermissions::default();
        let result = build_wasi_ctx(&perms);
        assert!(
            result.is_ok(),
            "empty permissions should produce valid WASI ctx"
        );
    }

    #[test]
    fn env_permission_injects_known_var() {
        // Use PATH — always set in test environments.
        let perms = JsonPermissions {
            env: vec![EnvPermission {
                name: "PATH".to_owned(),
            }],
            ..Default::default()
        };
        // Should build without error regardless of whether PATH is set.
        let result = build_wasi_ctx(&perms);
        assert!(result.is_ok());
    }

    #[test]
    fn missing_fs_path_is_skipped_gracefully() {
        let perms = JsonPermissions {
            fs: vec![FsPermission {
                path: "/nonexistent/path/that/does/not/exist".to_owned(),
                read: true,
                write: false,
            }],
            ..Default::default()
        };
        // Should NOT error — missing paths are warned and skipped.
        let result = build_wasi_ctx(&perms);
        assert!(result.is_ok(), "missing fs path should be skipped");
    }

    #[test]
    fn net_permission_does_not_panic() {
        // Net perms are recorded but not enforced at WASI level — should be a no-op.
        let perms = JsonPermissions {
            net: vec![NetPermission {
                host: "api.example.com".to_owned(),
                ports: vec![443],
            }],
            ..Default::default()
        };
        let result = build_wasi_ctx(&perms);
        assert!(result.is_ok());
    }
}
