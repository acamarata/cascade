//! PluginLoader — discovers and instantiates WASM plugins from `~/.cascade/plugins/`.
//!
//! Purpose: scan a plugins directory (one level deep), load the `plugin.json`
//!   manifest for each subdirectory, read the WASM bytes, and compile the module
//!   via `PluginSandbox`. Returns a partial-load result so one bad plugin never
//!   aborts the rest.
//! Inputs:  A directory path (canonically `~/.cascade/plugins/`).
//! Outputs: `(Vec<LoadedPlugin>, Vec<PluginLoadError>)` — all successes and all
//!   per-plugin failures, separated, so callers can log failures without panicking.
//! Constraints:
//!   - Non-recursive scan (one directory level only).
//!   - Symlinks inside the plugins dir are NOT followed (prevents path-traversal).
//!   - Every plugin is signature-verified BEFORE its WASM is read or compiled
//!     (T-P7-E25-03). Verification is a load-path step, not an advisory check.
//!   - Tracing spans are emitted per plugin attempt (`plugin_id` field populated).
//!
//! SPORT: cascade-plugins / loader layer (T-P4-E03-07)

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;
use tracing::{debug, error, info, instrument, warn};

use crate::manifest::{ManifestError, PluginJsonManifest, PluginType};
use crate::runtime::{LoadedPlugin, PluginSandbox};
use crate::signing::{self, TrustedPublishers};

// ── Error type ────────────────────────────────────────────────────────────────

/// Per-plugin load failure returned by `PluginLoader::scan`.
///
/// Each variant is actionable: it tells the operator exactly what went wrong
/// and which plugin directory triggered the failure.
#[derive(Debug, Error, Serialize, Deserialize)]
pub enum PluginLoadError {
    /// No `plugin.json` file found in the plugin directory.
    #[error("plugin.json not found in '{dir}'")]
    ManifestNotFound { dir: PathBuf },

    /// `plugin.json` exists but failed to parse or validate.
    #[error("manifest error in '{dir}': {reason}")]
    ManifestInvalid { dir: PathBuf, reason: String },

    /// The WASM binary declared in `entry_wasm` was not found.
    #[error("WASM file not found: '{path}'")]
    WasmNotFound { path: PathBuf },

    /// The WASM binary could not be read from disk.
    #[error("failed to read WASM file '{path}': {reason}")]
    WasmReadError { path: PathBuf, reason: String },

    /// The WASM sandbox failed to compile or initialise the module.
    #[error("sandbox error for plugin '{id}': {reason}")]
    SandboxError { id: String, reason: String },

    /// A plugin whose `disable` marker file is present is skipped.
    #[error("plugin '{id}' is disabled (found .disabled marker)")]
    Disabled { id: String },

    /// The filesystem entry is not a directory (files and symlinks are skipped).
    #[error("skipping non-directory entry '{path}'")]
    NotADirectory { path: PathBuf },

    /// Signature verification rejected the plugin: unsigned while unsigned
    /// plugins are blocked, signed by a publisher that is not trusted, or a
    /// signature that does not match the WASM bytes.
    #[error("plugin '{id}' rejected by signature verification: {reason}")]
    SignatureRejected { id: String, reason: String },
}

// ── LoadedPlugin extension: carry the plugin dir path ────────────────────────

/// A compiled plugin plus the directory it was loaded from.
///
/// The `plugin_dir` field is used by the hot-reload watcher to watch for WASM
/// changes and by the registry to persist enable/disable state.
pub struct DiscoveredPlugin {
    /// Compiled, sandboxed plugin module ready for invocation.
    pub loaded: LoadedPlugin,
    /// The directory under `~/.cascade/plugins/` this plugin was loaded from.
    pub plugin_dir: PathBuf,
    /// Parsed manifest (preserved for registry display and dispatch routing).
    pub manifest: PluginJsonManifest,
}

// ── Verification policy ───────────────────────────────────────────────────────

/// How the loader treats plugin signatures.
///
/// Purpose: the Ed25519 publisher-trust machinery in [`crate::signing`] is only
/// a security control if the LOAD PATH consults it. This type carries the
/// trusted-key set and the unsigned-plugin decision into
/// [`PluginLoader::load_one`] (T-P7-E25-03).
///
/// Inputs:  a `TrustedPublishers` set and an `allow_unsigned` decision.
/// Outputs: consumed by the loader; no I/O of its own except [`Self::enforcing`].
/// Constraints:
///   - [`Self::enforcing`] is the DEFAULT and blocks unsigned plugins. A
///     failure to read `trusted-publishers.json` yields an EMPTY trusted set,
///     never a permissive one — an unreadable trust store must not silently
///     become "trust everything".
///   - [`Self::permissive`] exists for local development and for fixtures in
///     tests. It is never the default anywhere in production code.
pub struct VerificationPolicy {
    /// Publisher keys accepted as trusted signers.
    pub publishers: TrustedPublishers,
    /// When true, an unsigned plugin loads with a warning instead of failing.
    pub allow_unsigned: bool,
}

impl VerificationPolicy {
    /// Enforcing policy built from `~/.cascade/trusted-publishers.json`.
    ///
    /// Unsigned plugins are blocked. If the trust store cannot be read the
    /// policy falls back to an EMPTY publisher set, which rejects everything
    /// rather than accepting everything.
    pub fn enforcing() -> Self {
        let publishers = TrustedPublishers::load().unwrap_or_else(|e| {
            warn!(
                reason = %e,
                "could not read trusted-publishers.json — treating the trust store as EMPTY"
            );
            TrustedPublishers::default()
        });
        Self {
            publishers,
            allow_unsigned: false,
        }
    }

    /// Policy that permits unsigned plugins. Development and tests only.
    pub fn permissive() -> Self {
        Self {
            publishers: TrustedPublishers::default(),
            allow_unsigned: true,
        }
    }
}

impl Default for VerificationPolicy {
    fn default() -> Self {
        Self::enforcing()
    }
}

// ── PluginLoader ──────────────────────────────────────────────────────────────

/// Scans a plugins directory and loads all valid WASM plugins.
///
/// # Design
/// - One sandbox is created per plugin type group (lightweight vs data-heavy)
///   and reused within the scan to amortise `Engine` construction.
/// - Bad plugins are isolated: their error is collected and the scan continues.
/// - Symlinks in the plugins dir are NOT followed.
pub struct PluginLoader;

impl PluginLoader {
    /// Scan `plugins_dir` for plugin subdirectories and load each one.
    ///
    /// Returns a tuple `(loaded, errors)`:
    /// - `loaded`: every plugin that compiled and initialised successfully.
    /// - `errors`: one `PluginLoadError` per failed or skipped plugin directory.
    ///
    /// An empty `plugins_dir` is valid and returns `(vec![], vec![])`.
    #[instrument(name = "plugin_loader_scan", fields(plugins_dir = %plugins_dir.display()))]
    pub fn scan(plugins_dir: &Path) -> (Vec<DiscoveredPlugin>, Vec<PluginLoadError>) {
        Self::scan_with(plugins_dir, &VerificationPolicy::enforcing())
    }

    /// Scan `plugins_dir` under an explicit signature-verification policy.
    ///
    /// [`Self::scan`] delegates here with [`VerificationPolicy::enforcing`].
    /// Callers that legitimately need to load unsigned plugins — development
    /// tooling, test fixtures — pass [`VerificationPolicy::permissive`]
    /// explicitly, so the choice is always visible at the call site.
    #[instrument(name = "plugin_loader_scan_with", skip(policy), fields(plugins_dir = %plugins_dir.display()))]
    pub fn scan_with(
        plugins_dir: &Path,
        policy: &VerificationPolicy,
    ) -> (Vec<DiscoveredPlugin>, Vec<PluginLoadError>) {
        let mut loaded = Vec::new();
        let mut errors = Vec::new();

        let read_dir = match std::fs::read_dir(plugins_dir) {
            Ok(rd) => rd,
            Err(e) => {
                // Non-existent or unreadable plugins dir is not an error: the
                // daemon may start before the user has installed any plugins.
                info!(
                    plugins_dir = %plugins_dir.display(),
                    reason = %e,
                    "plugins directory not found or unreadable — no plugins loaded"
                );
                return (loaded, errors);
            }
        };

        for entry_result in read_dir {
            let entry = match entry_result {
                Ok(e) => e,
                Err(e) => {
                    warn!(reason = %e, "failed to read directory entry — skipping");
                    continue;
                }
            };

            let entry_path = entry.path();

            // Only enter real directories — no symlinks (path-traversal guard).
            let file_type = match entry.file_type() {
                Ok(ft) => ft,
                Err(e) => {
                    warn!(path = %entry_path.display(), reason = %e, "cannot read file type — skipping");
                    continue;
                }
            };
            if !file_type.is_dir() {
                errors.push(PluginLoadError::NotADirectory {
                    path: entry_path.clone(),
                });
                continue;
            }

            // Attempt to load this plugin directory.
            match Self::load_one(&entry_path, policy) {
                Ok(discovered) => {
                    info!(
                        plugin_id = %discovered.manifest.id,
                        plugin_dir = %entry_path.display(),
                        "plugin loaded successfully"
                    );
                    loaded.push(discovered);
                }
                Err(e) => {
                    error!(
                        plugin_dir = %entry_path.display(),
                        reason = %e,
                        "plugin load failed — skipping"
                    );
                    errors.push(e);
                }
            }
        }

        (loaded, errors)
    }

    /// Load a single plugin directory.
    ///
    /// Steps:
    /// 1. Check for `.disabled` marker → `PluginLoadError::Disabled`
    /// 2. Load and validate `plugin.json`
    /// 3. Resolve `entry_wasm` path
    /// 4. Verify the signature against `policy` — BEFORE any bytes are read
    /// 5. Read WASM bytes
    /// 6. Build sandbox + compile module
    #[instrument(name = "plugin_load_one", skip(policy), fields(plugin_dir = %plugin_dir.display()))]
    fn load_one(
        plugin_dir: &Path,
        policy: &VerificationPolicy,
    ) -> Result<DiscoveredPlugin, PluginLoadError> {
        // 1. Skip disabled plugins.
        if plugin_dir.join(".disabled").exists() {
            // Read the manifest id for a nicer error if possible; fall back to dir name.
            let id = plugin_dir
                .file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned());
            return Err(PluginLoadError::Disabled { id });
        }

        // 2. Load manifest.
        let manifest_path = plugin_dir.join("plugin.json");
        if !manifest_path.exists() {
            return Err(PluginLoadError::ManifestNotFound {
                dir: plugin_dir.to_owned(),
            });
        }

        let manifest = PluginJsonManifest::load(&manifest_path).map_err(|e: ManifestError| {
            PluginLoadError::ManifestInvalid {
                dir: plugin_dir.to_owned(),
                reason: e.to_string(),
            }
        })?;

        // 3. Resolve WASM path (entry_wasm must be relative per manifest validation).
        let wasm_path = plugin_dir.join(&manifest.entry_wasm);
        if !wasm_path.exists() {
            return Err(PluginLoadError::WasmNotFound { path: wasm_path });
        }

        // 4. Verify the signature BEFORE the WASM is read or compiled.
        //
        // This is the whole point of the publisher-trust system: an untrusted
        // module must never reach the sandbox, not even to be compiled. The
        // check sits above the read so a rejected plugin costs one stat call.
        let verdict = signing::verify_plugin(
            &manifest.id,
            &wasm_path,
            &policy.publishers,
            policy.allow_unsigned,
        )
        .map_err(|e| PluginLoadError::SignatureRejected {
            id: manifest.id.clone(),
            reason: e.to_string(),
        })?;
        if verdict == signing::VerifyResult::UnsignedBlocked {
            // Defensive: verify_plugin returns Err for this today, but the
            // variant exists, and an unsigned-blocked plugin must never fall
            // through to execution if that ever changes.
            return Err(PluginLoadError::SignatureRejected {
                id: manifest.id.clone(),
                reason: "unsigned plugin blocked by policy".to_owned(),
            });
        }
        debug!(plugin_id = %manifest.id, ?verdict, "plugin signature verified");

        // 5. Read WASM bytes.
        let wasm_bytes = std::fs::read(&wasm_path).map_err(|e| PluginLoadError::WasmReadError {
            path: wasm_path.clone(),
            reason: e.to_string(),
        })?;

        // 6. Compile via sandbox.  Map PluginJsonManifest.kind -> PluginType for resource limits.
        let plugin_type = kind_to_type(manifest.kind);
        let sandbox =
            PluginSandbox::new(plugin_type).map_err(|e| PluginLoadError::SandboxError {
                id: manifest.id.clone(),
                reason: e.to_string(),
            })?;

        let loaded = sandbox
            .load_with_permissions_and_data_dir(
                &wasm_bytes,
                &manifest.id,
                manifest.permissions.clone(),
                None,
                &plugin_dir.join("data"),
            )
            .map_err(|e| PluginLoadError::SandboxError {
                id: manifest.id.clone(),
                reason: e.to_string(),
            })?;

        Ok(DiscoveredPlugin {
            loaded,
            plugin_dir: plugin_dir.to_owned(),
            manifest,
        })
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Map a `PluginKind` (from `plugin.json`) to the `PluginType` enum used for
/// sandbox resource-limit selection.
///
/// WHY: `PluginKind` is the public-contract enum; `PluginType` is the internal
/// sandbox enum.  They have different variants but overlap on the resource
/// profiles that matter (small vs large memory budget).
fn kind_to_type(kind: crate::manifest::PluginKind) -> PluginType {
    use crate::manifest::PluginKind;
    match kind {
        PluginKind::Agent => PluginType::Agent,
        PluginKind::Provider => PluginType::EmbeddingProvider,
        PluginKind::DataSource => PluginType::Retriever,
        PluginKind::ChatTool => PluginType::ToolIntegration,
        PluginKind::Widget => PluginType::OutputRenderer,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    /// Minimal valid `plugin.json` for test fixtures.
    fn valid_manifest_json(id: &str) -> String {
        format!(
            r#"{{
                "id": "{id}",
                "name": "Test Plugin",
                "version": "1.0.0",
                "description": "A test plugin",
                "author": "test",
                "license": "MIT",
                "entry_wasm": "{id}.wasm",
                "kind": "ChatTool",
                "min_cascade_version": ">=0.1.0"
            }}"#
        )
    }

    /// Write a minimal valid WAT module as WASM bytes to `path`.
    fn write_minimal_wasm(path: &Path) {
        // A trivial WAT module that exports nothing but compiles cleanly.
        let wat = r#"(module)"#;
        let wasm = wat::parse_str(wat).expect("wat::parse_str");
        fs::write(path, wasm).expect("write wasm");
    }

    /// Create a valid plugin directory with `plugin.json` + WASM binary.
    fn make_valid_plugin(dir: &Path, id: &str) {
        let plugin_dir = dir.join(id);
        fs::create_dir_all(&plugin_dir).unwrap();
        fs::write(plugin_dir.join("plugin.json"), valid_manifest_json(id)).unwrap();
        write_minimal_wasm(&plugin_dir.join(format!("{id}.wasm")));
    }

    // ── Signature enforcement (T-P7-E25-03) ───────────────────────────────────
    //
    // Before this ticket, PluginLoader never consulted signing::verify_plugin,
    // so any .wasm dropped into the plugins dir executed regardless of its
    // signature. These tests pin the load path to the trust store.

    /// Sign `wasm_path` with a fresh key and return a publisher set trusting it.
    fn sign_and_trust(wasm_path: &Path, publisher: &str) -> TrustedPublishers {
        use base64::Engine;
        use ed25519_dalek::{Signer, SigningKey};
        use rand::RngCore;

        let mut seed = [0u8; 32];
        rand::rngs::OsRng.fill_bytes(&mut seed);
        let sk = SigningKey::from_bytes(&seed);
        let b64 = base64::engine::general_purpose::STANDARD;

        let wasm = fs::read(wasm_path).expect("read wasm to sign");
        let sig = b64.encode(sk.sign(&wasm).to_bytes());
        fs::write(format!("{}.sig", wasm_path.display()), sig).expect("write .sig");

        TrustedPublishers {
            publishers: vec![crate::signing::TrustedPublisher {
                name: publisher.to_owned(),
                public_key: b64.encode(sk.verifying_key().as_bytes()),
            }],
        }
    }

    #[test]
    fn unsigned_plugin_is_rejected_under_the_enforcing_policy() {
        let tmp = TempDir::new().unwrap();
        make_valid_plugin(tmp.path(), "com.example.unsigned");

        // Enforcing policy with an empty trust store — no .sig file exists.
        let policy = VerificationPolicy {
            publishers: TrustedPublishers::default(),
            allow_unsigned: false,
        };
        let (loaded, errors) = PluginLoader::scan_with(tmp.path(), &policy);

        assert!(
            loaded.is_empty(),
            "an unsigned plugin must not load under an enforcing policy"
        );
        assert!(
            errors
                .iter()
                .any(|e| matches!(e, PluginLoadError::SignatureRejected { .. })),
            "expected SignatureRejected, got: {errors:?}"
        );
    }

    #[test]
    fn plugin_signed_by_an_untrusted_publisher_is_rejected() {
        let tmp = TempDir::new().unwrap();
        make_valid_plugin(tmp.path(), "com.example.wrongkey");
        let wasm = tmp
            .path()
            .join("com.example.wrongkey/com.example.wrongkey.wasm");

        // Sign it, then DISCARD the publisher set — the signature is valid but
        // the key that made it is not trusted.
        let _ = sign_and_trust(&wasm, "someone-else");
        let policy = VerificationPolicy {
            publishers: TrustedPublishers::default(),
            allow_unsigned: false,
        };

        let (loaded, errors) = PluginLoader::scan_with(tmp.path(), &policy);
        assert!(
            loaded.is_empty(),
            "a signature from an untrusted key must not load"
        );
        assert!(
            errors
                .iter()
                .any(|e| matches!(e, PluginLoadError::SignatureRejected { .. })),
            "expected SignatureRejected, got: {errors:?}"
        );
    }

    #[test]
    fn plugin_signed_by_a_trusted_publisher_loads() {
        let tmp = TempDir::new().unwrap();
        make_valid_plugin(tmp.path(), "com.example.trusted");
        let wasm = tmp
            .path()
            .join("com.example.trusted/com.example.trusted.wasm");

        let publishers = sign_and_trust(&wasm, "acamarata");
        let policy = VerificationPolicy {
            publishers,
            allow_unsigned: false,
        };

        let (loaded, errors) = PluginLoader::scan_with(tmp.path(), &policy);
        assert_eq!(
            loaded.len(),
            1,
            "a correctly signed plugin from a trusted publisher must still load (errors: {errors:?})"
        );
        assert_eq!(loaded[0].manifest.id, "com.example.trusted");
    }

    #[test]
    fn tampered_wasm_fails_verification_even_with_a_trusted_publisher() {
        let tmp = TempDir::new().unwrap();
        make_valid_plugin(tmp.path(), "com.example.tampered");
        let wasm = tmp
            .path()
            .join("com.example.tampered/com.example.tampered.wasm");

        let publishers = sign_and_trust(&wasm, "acamarata");
        // Rewrite the module AFTER signing — the signature no longer matches.
        let mut bytes = fs::read(&wasm).unwrap();
        bytes.extend_from_slice(&[0x00, 0x0b]);
        fs::write(&wasm, bytes).unwrap();

        let policy = VerificationPolicy {
            publishers,
            allow_unsigned: false,
        };
        let (loaded, errors) = PluginLoader::scan_with(tmp.path(), &policy);
        assert!(
            loaded.is_empty(),
            "modified bytes must invalidate the signature"
        );
        assert!(
            errors
                .iter()
                .any(|e| matches!(e, PluginLoadError::SignatureRejected { .. })),
            "expected SignatureRejected, got: {errors:?}"
        );
    }

    #[test]
    fn default_policy_is_enforcing_not_permissive() {
        // A permissive default would silently reinstate the vulnerability this
        // ticket closed, so pin it.
        assert!(
            !VerificationPolicy::default().allow_unsigned,
            "VerificationPolicy::default() must block unsigned plugins"
        );
    }

    #[test]
    fn scan_empty_dir_returns_no_errors() {
        let tmp = TempDir::new().unwrap();
        let (loaded, errors) =
            PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());
        assert!(loaded.is_empty(), "expected no loaded plugins");
        assert!(errors.is_empty(), "expected no errors");
    }

    #[test]
    fn scan_nonexistent_dir_returns_empty() {
        let (loaded, errors) = PluginLoader::scan_with(
            Path::new("/tmp/__cascade_no_such_dir_xyz__"),
            &VerificationPolicy::permissive(),
        );
        assert!(loaded.is_empty());
        assert!(
            errors.is_empty(),
            "non-existent dir should not produce errors"
        );
    }

    #[test]
    fn scan_partial_load_two_valid_one_invalid() {
        let tmp = TempDir::new().unwrap();

        // Two valid plugins.
        make_valid_plugin(tmp.path(), "com.example.plugin-a");
        make_valid_plugin(tmp.path(), "com.example.plugin-b");

        // One invalid: directory present but no plugin.json.
        fs::create_dir_all(tmp.path().join("com.example.bad")).unwrap();

        let (loaded, errors) =
            PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());

        assert_eq!(
            loaded.len(),
            2,
            "should load 2 valid plugins (errors: {})",
            errors
                .iter()
                .map(|e| e.to_string())
                .collect::<Vec<_>>()
                .join(", ")
        );
        assert_eq!(errors.len(), 1, "should report 1 error; errors: {errors:?}");

        // The error for the missing manifest must be ManifestNotFound.
        assert!(
            matches!(errors[0], PluginLoadError::ManifestNotFound { .. }),
            "expected ManifestNotFound, got {:?}",
            errors[0]
        );
    }

    #[test]
    fn scan_missing_manifest_produces_manifest_not_found() {
        let tmp = TempDir::new().unwrap();
        // Directory with no plugin.json.
        fs::create_dir_all(tmp.path().join("com.example.no-manifest")).unwrap();

        let (loaded, errors) =
            PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());

        assert!(loaded.is_empty());
        assert_eq!(errors.len(), 1);
        assert!(matches!(
            errors[0],
            PluginLoadError::ManifestNotFound { .. }
        ));
    }

    #[test]
    fn scan_disabled_plugin_is_skipped_not_panicked() {
        let tmp = TempDir::new().unwrap();
        let plugin_dir = tmp.path().join("com.example.disabled-plugin");
        fs::create_dir_all(&plugin_dir).unwrap();
        fs::write(
            plugin_dir.join("plugin.json"),
            valid_manifest_json("com.example.disabled-plugin"),
        )
        .unwrap();
        write_minimal_wasm(&plugin_dir.join("com.example.disabled-plugin.wasm"));
        // Place the disable marker.
        fs::write(plugin_dir.join(".disabled"), "").unwrap();

        let (loaded, errors) =
            PluginLoader::scan_with(tmp.path(), &VerificationPolicy::permissive());

        assert!(loaded.is_empty(), "disabled plugin should not be loaded");
        assert_eq!(errors.len(), 1);
        assert!(matches!(errors[0], PluginLoadError::Disabled { .. }));
    }
}
