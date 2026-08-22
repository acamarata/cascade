//! WASI context builder — translates plugin.json permissions into WASIp1 context.
//!
//! Purpose: construct a `WasiP1Ctx` that grants ONLY the capabilities explicitly
//!   declared in the plugin's `JsonPermissions`. All other capabilities are
//!   denied by default (no inherited env, no preopened dirs, no network).
//!
//!   Filesystem scopes go through the real capability engine
//!   (`DeclaredCapabilities::resolve`), which CANONICALISES every declared
//!   root. That matters: `path.exists()` follows symlinks, so a declared root
//!   that is itself a symlink used to be preopened by its symlink name with
//!   the true target never inspected (T-P7-E25-15).
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

use crate::capability::{Capability, CapabilitySet, DeclaredCapabilities};
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
/// Translate the manifest's permission lists into the capability engine's
/// declaration shape.
///
/// A scope with `read=false, write=false` is dropped here rather than
/// resolved: it grants nothing, and carrying it further would canonicalise a
/// path for no reason.
fn declared_from_manifest(permissions: &JsonPermissions) -> DeclaredCapabilities {
    let collect = |want: fn(&crate::manifest::FsPermission) -> bool| {
        let paths: Vec<String> = permissions
            .fs
            .iter()
            .filter(|p| want(p))
            // A declared path that cannot be canonicalised (typically: it does
            // not exist) is skipped rather than failing the whole resolve. One
            // stale entry must not cost a plugin every other scope it declared.
            .filter(|p| match DeclaredCapabilities::canonical_scope_root(&p.path) {
                Ok(_) => true,
                Err(e) => {
                    warn!(path = %p.path, reason = %e, "plugin fs scope does not resolve — skipping preopen");
                    false
                }
            })
            .map(|p| p.path.clone())
            .collect();
        (!paths.is_empty()).then_some(paths)
    };

    DeclaredCapabilities {
        fs_read: collect(|p| p.read),
        fs_write: collect(|p| p.write),
        // The manifest's `net` list is a request for outbound access; the
        // policy decides whether it is granted.
        net_outbound: (!permissions.net.is_empty()).then_some(true),
        ..Default::default()
    }
}

pub fn build_wasi_ctx(permissions: &JsonPermissions) -> Result<WasiP1Ctx> {
    build_wasi_ctx_with_policy(permissions, &HostCapabilityPolicy::default())
}

/// User-level overrides applied on top of what a plugin declares.
///
/// Purpose: a plugin declaring a capability is a REQUEST, not a grant. This
/// carries the user's deny decisions into `DeclaredCapabilities::resolve`, so
/// a declared capability the user has switched off is recorded as denied
/// rather than granted (T-P7-E25-15).
#[derive(Debug, Clone, Default)]
pub struct HostCapabilityPolicy {
    /// Refuse outbound network access even when the plugin declares it.
    pub deny_net_outbound: bool,
}

/// One resolved filesystem scope ready to be preopened.
struct FsScope {
    /// The path string the plugin declared — used as the GUEST path.
    declared: String,
    /// The canonical host path the declaration resolves to.
    canonical: std::path::PathBuf,
    read: bool,
    write: bool,
}

/// Collapse a resolved `CapabilitySet` into one entry per canonical root.
///
/// `resolve` emits a separate `ScopedPath` for FsRead and FsWrite, so a scope
/// declared read+write appears twice. Preopening it twice would register two
/// competing handles for the same directory, so the permissions are merged.
fn fs_scopes(capabilities: &CapabilitySet) -> Vec<FsScope> {
    let mut scopes: Vec<FsScope> = Vec::new();
    for scoped in &capabilities.scoped_paths {
        let (read, write) = match scoped.capability {
            Capability::FsRead => (true, false),
            Capability::FsWrite => (false, true),
            // Non-filesystem capabilities carry no preopen.
            _ => continue,
        };
        match scopes
            .iter_mut()
            .find(|s| s.canonical == scoped.canonical_root)
        {
            Some(existing) => {
                existing.read |= read;
                existing.write |= write;
            }
            None => scopes.push(FsScope {
                declared: scoped.declared_root.clone(),
                canonical: scoped.canonical_root.clone(),
                read,
                write,
            }),
        }
    }
    scopes
}

/// Build a WASI context under an explicit host capability policy.
///
/// [`build_wasi_ctx`] delegates here with the default (nothing denied).
pub fn build_wasi_ctx_with_policy(
    permissions: &JsonPermissions,
    policy: &HostCapabilityPolicy,
) -> Result<WasiP1Ctx> {
    let mut builder = WasiCtxBuilder::new();

    // Route the manifest through the real capability engine rather than
    // reading JsonPermissions directly. resolve() canonicalises every declared
    // root, rejects reserved capabilities, and applies the user's deny
    // overrides — none of which the old direct read did.
    let declared = declared_from_manifest(permissions);
    let capabilities = declared
        .resolve(policy.deny_net_outbound)
        .map_err(|e| anyhow::anyhow!("capability resolution failed: {e}"))?;

    // ── Filesystem preopens ─────────────────────────────────────────────────
    //
    // WHY: WASIp1 filesytem access is entirely mediated by preopened directory
    // handles. A plugin that has no preopens simply cannot open any file —
    // the WASI host returns ENOTCAPABLE for any path not reachable through a
    // preopen. This is the primary filesystem isolation boundary.
    for scope in fs_scopes(&capabilities) {
        let FsScope {
            declared,
            canonical,
            read,
            write,
        } = scope;

        let dir_perms = match (read, write) {
            (true, true) => DirPerms::all(),
            (true, false) => DirPerms::READ,
            (false, true) => DirPerms::MUTATE,
            // resolve() never produces a scope with neither, but the match
            // must stay total.
            (false, false) => continue,
        };
        let file_perms = match (read, write) {
            (true, true) => FilePerms::all(),
            (true, false) => FilePerms::READ,
            (false, true) => FilePerms::WRITE,
            (false, false) => continue,
        };

        // Preopen the CANONICAL host path, mapped to the DECLARED guest path.
        //
        // Preopening the declared name would hand the sandbox whatever a
        // symlink points at while logging the innocuous declared string.
        // Resolving first means the true target is what gets opened and what
        // gets logged; from there cap-std confines the guest to that tree.
        if canonical != std::path::Path::new(&declared) {
            warn!(
                declared = %declared,
                canonical = %canonical.display(),
                "plugin fs scope resolves elsewhere through a link — preopening the resolved target"
            );
        }

        match builder.preopened_dir(&canonical, &declared, dir_perms, file_perms) {
            Ok(_) => {}
            Err(e) => {
                // Non-fatal: log and skip. Plugin will get ENOTCAPABLE if it
                // tries to open this path.
                warn!(path = %declared, error = %e, "failed to preopen plugin fs path");
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

    // ── Capability engine wiring + symlink escape (T-P7-E25-15) ───────────────

    /// Layout: `<root>/scope` is the declared scope, `<root>/secret` is off
    /// limits, and `<root>/scope/escape` is a symlink pointing into it.
    #[cfg(unix)] // only the symlink tests below use it, and those are unix-only
    fn symlink_fixture() -> tempfile::TempDir {
        let tmp = tempfile::TempDir::new().unwrap();
        std::fs::create_dir(tmp.path().join("scope")).unwrap();
        std::fs::create_dir(tmp.path().join("secret")).unwrap();
        std::fs::write(tmp.path().join("secret/passwd"), b"TOP-SECRET").unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(tmp.path().join("secret"), tmp.path().join("scope/escape"))
            .unwrap();
        tmp
    }

    #[cfg(unix)]
    #[test]
    fn symlink_inside_a_declared_scope_cannot_escape_it() {
        let tmp = symlink_fixture();
        let declared = tmp.path().join("scope");

        let caps = DeclaredCapabilities {
            fs_read: Some(vec![declared.to_string_lossy().into_owned()]),
            ..Default::default()
        };
        let set = caps.resolve(false).expect("scope resolves");
        let scope = set
            .scoped_paths
            .iter()
            .find(|s| s.capability == Capability::FsRead)
            .expect("fs_read scope present");

        // Reading a file genuinely inside the scope is allowed.
        std::fs::write(declared.join("ok.txt"), b"fine").unwrap();
        assert!(
            scope.contains(&declared.join("ok.txt")),
            "a file inside the declared scope must be readable"
        );

        // Reaching the secret THROUGH the symlink must not be. This is the
        // escape: the path is lexically inside `scope/`, but resolves outside.
        let via_link = declared.join("escape/passwd");
        assert!(
            via_link.exists(),
            "fixture symlink must actually resolve, or the test proves nothing"
        );
        assert!(
            !scope.contains(&via_link),
            "symlink inside the scope must not grant access to {}",
            via_link.display()
        );
    }

    #[cfg(unix)]
    #[test]
    fn a_declared_root_that_is_itself_a_symlink_resolves_to_its_target() {
        // The old code called path.exists() — which FOLLOWS symlinks — and then
        // preopened the symlink's own name, so the real target was never seen.
        let tmp = symlink_fixture();
        let link = tmp.path().join("declared_link");
        std::os::unix::fs::symlink(tmp.path().join("secret"), &link).unwrap();

        let caps = DeclaredCapabilities {
            fs_read: Some(vec![link.to_string_lossy().into_owned()]),
            ..Default::default()
        };
        let set = caps.resolve(false).expect("resolves");
        let scope = &set.scoped_paths[0];

        assert_eq!(
            scope.canonical_root,
            std::fs::canonicalize(tmp.path().join("secret")).unwrap(),
            "the canonical root must be the link TARGET, not the link name"
        );
        assert_ne!(
            scope.canonical_root, link,
            "resolution must not stop at the symlink itself"
        );
    }

    #[test]
    fn user_deny_override_blocks_a_declared_capability() {
        let perms = JsonPermissions {
            net: vec![NetPermission {
                host: "example.com".to_owned(),
                ports: vec![443],
            }],
            ..Default::default()
        };
        let declared = declared_from_manifest(&perms);
        assert_eq!(
            declared.net_outbound,
            Some(true),
            "a net entry in the manifest must become a net_outbound request"
        );

        // Granted when the user permits it.
        let granted = declared.resolve(false).expect("resolves");
        assert!(granted.has(&Capability::NetOutbound));

        // Denied when the user overrides — and recorded as denied, not simply
        // absent, so the reason is visible.
        let denied = declared.resolve(true).expect("resolves");
        assert!(!denied.has(&Capability::NetOutbound));
        assert!(denied.denied.contains(&Capability::NetOutbound));
    }

    #[test]
    fn deny_policy_reaches_build_wasi_ctx() {
        let perms = JsonPermissions {
            net: vec![NetPermission {
                host: "example.com".to_owned(),
                ports: vec![443],
            }],
            ..Default::default()
        };
        // Both paths must build; the policy changes what is granted, not
        // whether a context can be constructed.
        assert!(build_wasi_ctx(&perms).is_ok());
        assert!(build_wasi_ctx_with_policy(
            &perms,
            &HostCapabilityPolicy {
                deny_net_outbound: true,
            }
        )
        .is_ok());
    }

    #[test]
    fn read_and_write_on_one_path_produce_a_single_merged_preopen() {
        // resolve() emits FsRead and FsWrite as separate scopes; preopening the
        // same directory twice would register competing handles.
        let tmp = tempfile::TempDir::new().unwrap();
        let path = tmp.path().to_string_lossy().into_owned();
        let caps = DeclaredCapabilities {
            fs_read: Some(vec![path.clone()]),
            fs_write: Some(vec![path]),
            ..Default::default()
        };
        let set = caps.resolve(false).expect("resolves");
        assert_eq!(
            set.scoped_paths.len(),
            2,
            "engine emits one scope per capability"
        );

        let merged = fs_scopes(&set);
        assert_eq!(
            merged.len(),
            1,
            "preopens must be merged per canonical root"
        );
        assert!(merged[0].read && merged[0].write);
    }
}
