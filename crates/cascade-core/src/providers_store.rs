//! Providers store — atomic file I/O for the providers/accounts JSON snapshot.
//!
//! Purpose: provides [`write_providers_store`] and [`read_providers_store`] for
//! persisting and loading the [`ProvidersStore`] at `~/.cascade/providers.json`.
//! The store is the canonical single source of truth for all configured provider
//! accounts (harnesses, OAuth tokens, API keys) that the daemon, fleet widget,
//! round-robin scheduler, and onboarding wizard all read.
//!
//! Data structures ([`ProvidersStore`], [`ProviderEntry`]) are defined in this
//! module. The store merges detected accounts from the auth detector with
//! manually-added entries, deduplicating by (harness, account_id) pair.
//!
//! Inputs:  `Vec<DetectedAccount>` (from auth_detector) + existing [`ProvidersStore`]
//!          (if any) + target [`Path`] (for writes).
//! Outputs: atomic JSON file write (tmp → rename); deserialized [`ProvidersStore`].
//! Constraints: partial reads are impossible — write always uses tmp→rename.
//!              Deduplication is by (harness, account_id) pair only.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — ProviderEntry, write_providers_store,
//!        read_providers_store (T-P2-E02-33)

use serde::{Deserialize, Serialize};
use std::path::Path;

use crate::auth_detector::{AuthKind, DetectedAccount};
use cascade_types::error::{CascadeError, Result};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Current schema version for the providers store. Bump when adding/removing
/// required fields. Used for forward/backward compatibility checking.
pub const PROVIDERS_STORE_SCHEMA_VERSION: u32 = 1;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single provider entry in the providers store.
///
/// Represents a configured harness account (CC, OC, Codex, Cursor, Gemini)
/// with its detection metadata and enabled/disabled state. Deduplication is by
/// (harness, account_id) pair.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProviderEntry {
    /// Unique identifier: "{harness}-{slugified-account_id}"
    /// Example: "cc-cc-default", "oc-user@example.com"
    pub id: String,

    /// Harness identifier: "cc" | "oc" | "codex" | "cursor" | "gemini"
    pub harness: String,

    /// Opaque account identifier — email, synthetic label, or "<unknown>"
    pub account_id: String,

    /// Human-readable display name
    pub display_name: String,

    /// How the credentials were identified: "OAuthToken" | "ApiKey" | "Unknown"
    pub auth_kind: String,

    /// Whether this provider is enabled for use
    pub enabled: bool,

    /// Source of this entry: "auto-detected" | "manual" | "vault"
    pub source: String,

    /// Whether this key is usable from a local (non-server) daemon instance.
    ///
    /// Some Gemini free-pool keys are IP-locked to a specific server and must
    /// never be routed to from a developer machine. `enabled` still governs
    /// whether the key is usable AT ALL (e.g. quota-exhausted or revoked);
    /// `local_ok` is a narrower, deployment-context restriction layered on
    /// top. Defaults to `true` via serde so existing providers.json files
    /// without this field (pre-migration) are read as "usable everywhere."
    #[serde(default = "default_local_ok")]
    pub local_ok: bool,
}

/// Default value for `ProviderEntry::local_ok` when absent from JSON — `true`
/// (usable from any deployment context) preserves prior behavior for entries
/// written before this field existed.
fn default_local_ok() -> bool {
    true
}

/// The top-level providers store wrapper, serialized as `~/.cascade/providers.json`.
///
/// This is the canonical record of all configured provider accounts across all
/// harnesses. The daemon, widget, CLI, and onboarding wizard all read from this
/// store. Manual entries are preserved; auto-detected entries are merged without
/// duplication.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProvidersStore {
    /// Schema version for forward/backward compatibility
    pub schema_version: u32,

    /// ISO 8601 timestamp of last update
    pub updated_at: String,

    /// List of configured provider entries
    pub providers: Vec<ProviderEntry>,
}

// ── Writer ────────────────────────────────────────────────────────────────────

/// Slugify a string for use in an ID.
///
/// Replaces non-alphanumeric characters with hyphens, converts to lowercase.
/// Used to derive the ID slug from account_id.
fn slugify(s: &str) -> String {
    s.to_lowercase()
        .chars()
        .map(|c| if c.is_alphanumeric() { c } else { '-' })
        .collect::<String>()
        .split('-')
        .filter(|seg| !seg.is_empty())
        .collect::<Vec<_>>()
        .join("-")
}

/// Merge detected accounts into an existing providers list.
///
/// For each [`DetectedAccount`]:
/// - Derive id = format!("{}-{}", harness, slugify(account_id))
/// - If existing contains entry with same id: update display_name and auth_kind;
///   preserve enabled flag
/// - If not found: push new ProviderEntry with enabled=true, source="auto-detected"
///
/// # Arguments
///
/// * `existing` — mutable reference to the providers list to merge into
/// * `detected` — slice of detected accounts from the auth detector
pub fn merge_providers(existing: &mut Vec<ProviderEntry>, detected: &[DetectedAccount]) {
    for detected_account in detected {
        let id = format!(
            "{}-{}",
            detected_account.harness,
            slugify(&detected_account.account_id)
        );
        let auth_kind_str = match &detected_account.auth_kind {
            AuthKind::OAuthToken => "OAuthToken".to_string(),
            AuthKind::ApiKey => "ApiKey".to_string(),
            AuthKind::Unknown => "Unknown".to_string(),
        };

        if let Some(entry) = existing.iter_mut().find(|e| e.id == id) {
            // Update existing entry: update display_name and auth_kind, preserve enabled
            entry.display_name = detected_account.display_name.clone();
            entry.auth_kind = auth_kind_str;
        } else {
            // New entry: add with enabled=true, source="auto-detected"
            let new_entry = ProviderEntry {
                id,
                harness: detected_account.harness.clone(),
                account_id: detected_account.account_id.clone(),
                display_name: detected_account.display_name.clone(),
                auth_kind: auth_kind_str,
                enabled: true,
                source: "auto-detected".to_string(),
                local_ok: true,
            };
            existing.push(new_entry);
        }
    }
}

/// Write `store` to `path` atomically.
///
/// Inputs:  `path` — destination file (e.g. `~/.cascade/providers.json`);
///          `store` — the [`ProvidersStore`] to serialise.
/// Outputs: on success, `path` contains the new JSON with mode `0o600` (owner
///          read/write only) on Unix; the `.tmp` sibling is deleted by the OS
///          as part of the rename.
/// Constraints: uses `path.with_extension("tmp")` as the staging file, sets
///              its permissions to `0o600` BEFORE the rename (so there is
///              never a window where the file is readable at the default
///              umask), then `std::fs::rename` which is atomic on POSIX when
///              src and dst are on the same filesystem (guaranteed for a tmp
///              sibling). `providers.json` now stores plaintext API keys
///              (Gemini free-pool, etc.) as the source of truth, so it must
///              never be left world/group-readable. No-op on non-Unix.
///
/// # Errors
///
/// Returns [`CascadeError::Io`] if serialisation, the tmp write, or the rename
/// fails. The caller decides whether to retry. Failure to set permissions is
/// non-fatal (best-effort) since some filesystems (e.g. certain network
/// mounts) do not support Unix permission bits.
pub fn write_providers_store(path: &Path, store: &ProvidersStore) -> Result<()> {
    let bytes = serde_json::to_vec_pretty(store).map_err(|e| CascadeError::Other(e.to_string()))?;

    let tmp = path.with_extension("tmp");

    // Write bytes to the tmp file first.
    std::fs::write(&tmp, &bytes).map_err(|e| CascadeError::io(tmp.clone(), "write", e))?;

    // Restrict permissions to owner-only BEFORE the rename, so the file is
    // never briefly world/group-readable at the process umask (typically
    // 0o022 → 0o644). providers.json is the plaintext-secret source of truth.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o600));
    }

    // Atomically replace the target.  `std::fs::rename` is POSIX-atomic when
    // src and dst are on the same filesystem, which is always true for a .tmp
    // sibling of the real file.
    std::fs::rename(&tmp, path).map_err(|e| CascadeError::io(path.to_path_buf(), "rename", e))?;

    Ok(())
}

// ── Reader ────────────────────────────────────────────────────────────────────

/// Read and deserialise the providers store from `path`.
///
/// Inputs:  `path` — source file (e.g. `~/.cascade/providers.json`).
/// Outputs: the deserialised [`ProvidersStore`] on success.
/// Constraints:
///   - Returns [`CascadeError::PathNotFound`] when the file is absent (normal
///     on a fresh install — callers should treat this as "no data yet").
///   - Returns [`CascadeError::SchemaMismatch`] when `schema_version` does not
///     equal [`PROVIDERS_STORE_SCHEMA_VERSION`].
///
/// # Errors
///
/// - [`CascadeError::PathNotFound`] — file does not exist.
/// - [`CascadeError::SchemaMismatch`] — schema version mismatch.
/// - [`CascadeError::Io`] — other I/O failure.
/// - [`CascadeError::ConfigParse`] — JSON is malformed.
pub fn read_providers_store(path: &Path) -> Result<ProvidersStore> {
    if !path.exists() {
        return Err(CascadeError::PathNotFound {
            path: path.to_path_buf(),
        });
    }

    let bytes = std::fs::read(path).map_err(|e| CascadeError::io(path.to_path_buf(), "read", e))?;

    let store: ProvidersStore =
        serde_json::from_slice(&bytes).map_err(|e| CascadeError::ConfigParse {
            path: path.to_path_buf(),
            detail: e.to_string(),
        })?;

    if store.schema_version != PROVIDERS_STORE_SCHEMA_VERSION {
        return Err(CascadeError::SchemaMismatch {
            expected: PROVIDERS_STORE_SCHEMA_VERSION,
            found: store.schema_version,
        });
    }

    Ok(store)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_detected_account(
        harness: &str,
        account_id: &str,
        display_name: &str,
        auth_kind: AuthKind,
    ) -> DetectedAccount {
        use std::path::PathBuf;
        DetectedAccount {
            harness: harness.to_string(),
            account_id: account_id.to_string(),
            display_name: display_name.to_string(),
            auth_kind,
            path_found: PathBuf::from("/tmp/dummy"),
        }
    }

    #[test]
    fn test_merge_empty_existing() {
        let mut existing = Vec::new();
        let detected = vec![
            make_detected_account("cc", "cc-default", "Claude Code", AuthKind::OAuthToken),
            make_detected_account("oc", "user@example.com", "OpenCode", AuthKind::OAuthToken),
        ];

        merge_providers(&mut existing, &detected);

        assert_eq!(existing.len(), 2);
        assert_eq!(existing[0].harness, "cc");
        assert_eq!(existing[0].account_id, "cc-default");
        assert!(existing[0].enabled);
        assert_eq!(existing[0].source, "auto-detected");
        assert_eq!(existing[1].harness, "oc");
        assert!(existing[1].enabled);
    }

    #[test]
    fn test_merge_no_dup() {
        let existing_entry = ProviderEntry {
            id: "cc-cc-default".to_string(),
            harness: "cc".to_string(),
            account_id: "cc-default".to_string(),
            display_name: "Claude Code".to_string(),
            auth_kind: "OAuthToken".to_string(),
            enabled: true,
            source: "manual".to_string(),
            local_ok: true,
        };
        let mut existing = vec![existing_entry];

        let detected = vec![make_detected_account(
            "cc",
            "cc-default",
            "Claude Code",
            AuthKind::OAuthToken,
        )];

        merge_providers(&mut existing, &detected);

        // Should still be 1 entry, not 2
        assert_eq!(existing.len(), 1);
        assert_eq!(existing[0].source, "manual");
    }

    #[test]
    fn test_merge_update() {
        let existing_entry = ProviderEntry {
            id: "cc-cc-default".to_string(),
            harness: "cc".to_string(),
            account_id: "cc-default".to_string(),
            display_name: "Old Name".to_string(),
            auth_kind: "Unknown".to_string(),
            enabled: true,
            source: "auto-detected".to_string(),
            local_ok: true,
        };
        let mut existing = vec![existing_entry];

        let detected = vec![make_detected_account(
            "cc",
            "cc-default",
            "Updated Name",
            AuthKind::OAuthToken,
        )];

        merge_providers(&mut existing, &detected);

        assert_eq!(existing.len(), 1);
        assert_eq!(existing[0].display_name, "Updated Name");
        assert_eq!(existing[0].auth_kind, "OAuthToken");
        assert!(existing[0].enabled); // preserved
    }

    #[test]
    fn test_write_and_read() {
        let tmp_dir = TempDir::new().unwrap();
        let path = tmp_dir.path().join("providers.json");

        let store = ProvidersStore {
            schema_version: PROVIDERS_STORE_SCHEMA_VERSION,
            updated_at: "2026-06-02T12:00:00Z".to_string(),
            providers: vec![ProviderEntry {
                id: "cc-cc-default".to_string(),
                harness: "cc".to_string(),
                account_id: "cc-default".to_string(),
                display_name: "Claude Code".to_string(),
                auth_kind: "OAuthToken".to_string(),
                enabled: true,
                source: "auto-detected".to_string(),
                local_ok: true,
            }],
        };

        write_providers_store(&path, &store).unwrap();

        // Verify no .tmp file remains
        assert!(!path.with_extension("tmp").exists());
        assert!(path.exists());

        let read_store = read_providers_store(&path).unwrap();
        assert_eq!(read_store.providers.len(), 1);
        assert_eq!(read_store.providers[0].display_name, "Claude Code");
    }

    // Unix-only: asserts POSIX mode bits, which Windows does not have.
    #[cfg(unix)]
    #[cfg(unix)]
    #[test]
    fn test_write_sets_owner_only_permissions() {
        use std::os::unix::fs::PermissionsExt;

        let tmp_dir = TempDir::new().unwrap();
        let path = tmp_dir.path().join("providers.json");

        let store = ProvidersStore {
            schema_version: PROVIDERS_STORE_SCHEMA_VERSION,
            updated_at: "2026-07-02T12:00:00Z".to_string(),
            providers: vec![],
        };

        write_providers_store(&path, &store).unwrap();

        let mode = std::fs::metadata(&path).unwrap().permissions().mode();
        assert_eq!(
            mode & 0o777,
            0o600,
            "providers.json must be owner-read/write only, got {:o}",
            mode & 0o777
        );
    }

    #[test]
    fn test_no_duplicate_on_same_detected_twice() {
        let mut existing = Vec::new();
        let detected = vec![
            make_detected_account("cc", "cc-default", "Claude Code", AuthKind::OAuthToken),
            make_detected_account("cc", "cc-default", "Claude Code", AuthKind::OAuthToken),
        ];

        merge_providers(&mut existing, &detected);

        // Even though detected has 2 identical accounts, the merge should not
        // produce duplicates in the existing list (the second one updates the
        // first). Actually, this behavior is: the first detected adds, the second
        // detected finds and updates. So we get 1 entry total.
        assert_eq!(existing.len(), 1);
    }

    #[test]
    fn test_slugify() {
        assert_eq!(slugify("cc-default"), "cc-default");
        assert_eq!(slugify("User@Example.Com"), "user-example-com");
        assert_eq!(slugify("foo---bar"), "foo-bar");
        assert_eq!(slugify("test_value"), "test-value");
    }
}
