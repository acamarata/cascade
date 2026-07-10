//! Quota store — atomic file I/O for the quota/usage JSON snapshot.
//!
//! Purpose: provides [`write_quota_store`] and [`read_quota_store`] for
//! persisting and loading the [`QuotaStore`] at `~/.cascade/quota-store.json`.
//! The store is the canonical single source of truth for all quota/usage data
//! consumed by the fleet widget, round-robin scheduler, and P3 analytics.
//!
//! Data structures ([`QuotaStore`], [`AccountEntry`], [`ModelUsage`],
//! [`HistoryEntry`]) are defined in `cascade_types::quota_store` so every
//! crate can use them without pulling in the I/O layer.
//!
//! Inputs:  populated [`QuotaStore`] + target [`Path`] (for writes); a [`Path`]
//!          (for reads).
//! Outputs: atomic JSON file write (tmp → rename); deserialized [`QuotaStore`].
//! Constraints: partial reads are impossible — write always uses tmp→rename.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — write_quota_store / read_quota_store
//!        rows (T-P2-E02-29)

use std::path::Path;

use cascade_types::error::{CascadeError, Result};
// Re-export everything from cascade-types so callers can use either crate.
pub use cascade_types::quota_store::{
    AccountEntry, HistoryEntry, ModelUsage, QuotaState, QuotaStore, RateWindow,
    PROVIDER_CLAUDE_MAX, PROVIDER_GFP, PROVIDER_GOOGLE_AGY, PROVIDER_OC_GO, PROVIDER_OPENAI_CODEX,
    QUOTA_STORE_SCHEMA_VERSION,
};

// ── Writer ────────────────────────────────────────────────────────────────────

/// Write `store` to `path` atomically.
///
/// Inputs:  `path` — destination file (e.g. `~/.cascade/quota-store.json`);
///          `store` — the [`QuotaStore`] to serialise.
/// Outputs: on success, `path` contains the new JSON; the `.tmp` sibling is
///          deleted by the OS as part of the rename.
/// Constraints: uses `path.with_extension("tmp")` as the staging file, then
///              `std::fs::rename` which is atomic on POSIX when src and dst
///              are on the same filesystem (guaranteed for a tmp sibling).
///
/// # Errors
///
/// Returns [`CascadeError::Io`] if serialisation, the tmp write, or the rename
/// fails. The caller decides whether to retry.
pub fn write_quota_store(path: &Path, store: &QuotaStore) -> Result<()> {
    let bytes = serde_json::to_vec_pretty(store).map_err(|e| CascadeError::Other(e.to_string()))?;

    let tmp = path.with_extension("tmp");

    // Write bytes to the tmp file first.
    std::fs::write(&tmp, &bytes).map_err(|e| CascadeError::io(tmp.clone(), "write", e))?;

    // Atomically replace the target.  `std::fs::rename` is POSIX-atomic when
    // src and dst are on the same filesystem, which is always true for a .tmp
    // sibling of the real file.
    std::fs::rename(&tmp, path).map_err(|e| CascadeError::io(path.to_path_buf(), "rename", e))?;

    Ok(())
}

// ── Reader ────────────────────────────────────────────────────────────────────

/// Read and deserialise the quota store from `path`.
///
/// Inputs:  `path` — source file (e.g. `~/.cascade/quota-store.json`).
/// Outputs: the deserialised [`QuotaStore`] on success.
/// Constraints:
///   - Returns [`CascadeError::PathNotFound`] when the file is absent (normal
///     on a fresh install — callers should treat this as "no data yet").
///   - Returns [`CascadeError::SchemaMismatch`] when `schema_version` does not
///     equal [`QUOTA_STORE_SCHEMA_VERSION`].
///
/// # Errors
///
/// - [`CascadeError::PathNotFound`] — file does not exist.
/// - [`CascadeError::SchemaMismatch`] — schema version mismatch.
/// - [`CascadeError::Io`] — other I/O failure.
/// - [`CascadeError::ConfigParse`] — JSON is malformed.
pub fn read_quota_store(path: &Path) -> Result<QuotaStore> {
    if !path.exists() {
        return Err(CascadeError::PathNotFound {
            path: path.to_path_buf(),
        });
    }

    let bytes = std::fs::read(path).map_err(|e| CascadeError::io(path.to_path_buf(), "read", e))?;

    let store: QuotaStore =
        serde_json::from_slice(&bytes).map_err(|e| CascadeError::ConfigParse {
            path: path.to_path_buf(),
            detail: e.to_string(),
        })?;

    if store.schema_version != QUOTA_STORE_SCHEMA_VERSION {
        return Err(CascadeError::SchemaMismatch {
            expected: QUOTA_STORE_SCHEMA_VERSION,
            found: store.schema_version,
        });
    }

    Ok(store)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use tempfile::TempDir;

    /// Construct a minimal valid [`QuotaStore`] for test use.
    fn make_store() -> QuotaStore {
        let mut models = HashMap::new();
        models.insert(
            "claude-sonnet-4-6".to_string(),
            ModelUsage {
                used: 1_000,
                limit: Some(10_000),
                reset_at: Some("2026-07-01T00:00:00Z".to_string()),
                pct_used: Some(10.0),
                cost_usd: None,
            },
        );
        let entry = AccountEntry {
            account_id: "cc-acct1".to_string(),
            harness: "cc".to_string(),
            provider: PROVIDER_CLAUDE_MAX.to_string(),
            models,
            week_total_used: 1_000,
            month_total_used: 4_000,
            last_polled: "2026-06-02T12:00:00Z".to_string(),
            rate_windows: vec![],
        };
        let history = HistoryEntry {
            ts: 1_748_865_600,
            accounts_snapshot: vec![entry.clone()],
        };
        let mut week_totals = HashMap::new();
        week_totals.insert("claude-sonnet-4-6".to_string(), 1_000u64);
        let mut month_totals = HashMap::new();
        month_totals.insert("claude-sonnet-4-6".to_string(), 4_000u64);

        QuotaStore {
            schema_version: QUOTA_STORE_SCHEMA_VERSION,
            updated_at: "2026-06-02T12:00:00Z".to_string(),
            accounts: vec![entry],
            week_totals,
            month_totals,
            rolling_history: vec![history],
        }
    }

    /// Serialise to tempdir, read back, and assert structural equality.
    #[test]
    fn roundtrip() {
        let dir = TempDir::new().expect("tempdir");
        let path = dir.path().join("quota-store.json");

        let original = make_store();
        write_quota_store(&path, &original).expect("write");

        let read_back = read_quota_store(&path).expect("read");
        assert_eq!(original, read_back);
    }

    /// A store with `schema_version = 999` must produce `SchemaMismatch`.
    #[test]
    fn schema_mismatch() {
        let dir = TempDir::new().expect("tempdir");
        let path = dir.path().join("quota-store.json");

        // Build a store with a wrong schema_version and write it directly via
        // serde_json, bypassing write_quota_store so we can set an arbitrary version.
        let mut bad = make_store();
        bad.schema_version = 999;
        let bytes = serde_json::to_vec_pretty(&bad).expect("serialise");
        std::fs::write(&path, bytes).expect("write raw");

        let err = read_quota_store(&path).expect_err("should fail on version mismatch");
        match err {
            CascadeError::SchemaMismatch { expected, found } => {
                assert_eq!(expected, QUOTA_STORE_SCHEMA_VERSION);
                assert_eq!(found, 999);
            }
            other => panic!("unexpected error variant: {other:?}"),
        }
    }

    /// After a successful write no `.tmp` sibling must remain on disk.
    #[test]
    fn atomic_write_no_tmp_remains() {
        let dir = TempDir::new().expect("tempdir");
        let path = dir.path().join("quota-store.json");
        let tmp = path.with_extension("tmp");

        let store = make_store();
        write_quota_store(&path, &store).expect("write");

        assert!(path.exists(), "target file must exist after write");
        assert!(
            !tmp.exists(),
            "tmp file must not remain after successful write"
        );
    }

    /// Reading a non-existent path must return `PathNotFound`.
    #[test]
    fn not_found() {
        let dir = TempDir::new().expect("tempdir");
        let path = dir.path().join("nonexistent.json");

        let err = read_quota_store(&path).expect_err("should fail");
        assert!(
            matches!(err, CascadeError::PathNotFound { .. }),
            "expected PathNotFound, got {err:?}"
        );
    }
}
