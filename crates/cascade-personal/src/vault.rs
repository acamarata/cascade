//! # cascade-personal::vault
//!
//! Purpose: Public API for the personal vault — open, query, upsert, consent, log.
//!
//! Inputs:
//! - A path to `personal.db`.
//! - A `Box<dyn Keychain>` for DEK storage/retrieval.
//! - `CascadeMode` at query time (injected by the daemon per session).
//!
//! Outputs: `VaultRecord` (decrypted payload as `serde_json::Value`) on read;
//!          stored ciphertext on write.
//!
//! Constraints:
//! - Plaintext payloads are NEVER stored; SELECT always returns ciphertext.
//! - DEK bytes MUST NOT appear in errors or tracing.
//! - `query_records` calls `gate::can_expose` before any decryption.
//! - `request_consent` inserts into `exposure_log` unconditionally (it is the
//!   audit record that consent was requested, not that it was granted).
//!
//! Tauri commands: `// TODO(rag-16-ui)` at every public fn that needs a wrapper.
//!
//! SPORT: cascade-personal / vault

use std::path::Path;
use std::sync::{Arc, Mutex};

use chrono::Utc;
use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use cascade_db::open_configured;
use cascade_keychain::Keychain;
#[cfg(test)]
use cascade_keychain::InMemoryKeychain;

use crate::crypto::{open as crypto_open, seal as crypto_seal};
use crate::gate::{can_expose, CascadeMode, Collection};
use crate::migrate;

// ── Error type ───────────────────────────────────────────────────────────────

/// Errors surfaced by the personal vault.
///
/// # Constraints
/// No variant holds DEK bytes or plaintext payload content.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / VaultError
#[derive(Debug, thiserror::Error)]
pub enum VaultError {
    /// SQLite layer error.
    #[error("sqlite error: {0}")]
    Db(#[from] rusqlite::Error),

    /// Schema migration failure.
    #[error("migration error: {0}")]
    Migration(#[from] cascade_db::DbError),

    /// OS keychain failure.
    #[error("keychain error: {0}")]
    Keychain(#[from] cascade_keychain::KeychainError),

    /// AES-GCM encryption/decryption failure.
    #[error("crypto error: {0}")]
    Crypto(String),

    /// Payload is not valid JSON.
    #[error("json error: {0}")]
    Json(#[from] serde_json::Error),

    /// Requested collection does not exist.
    #[error("unknown collection: {0}")]
    UnknownCollection(String),

    /// Access blocked by mode gate.
    #[error("collection '{0}' is not accessible in the current mode")]
    Gated(String),
}

// ── Types ────────────────────────────────────────────────────────────────────

/// A decrypted record returned by `query_records`.
///
/// # Purpose
/// Carries the decrypted JSON payload plus record metadata.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / VaultRecord
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VaultRecord {
    /// UUID v4 record identifier.
    pub id: String,
    /// Collection this record belongs to.
    pub collection: String,
    /// Decrypted payload (arbitrary JSON object).
    pub payload: serde_json::Value,
    /// RFC 3339 UTC creation timestamp.
    pub created_at: String,
    /// RFC 3339 UTC last-updated timestamp.
    pub updated_at: String,
}

/// A row from the `exposure_log` table.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / ExposureEntry
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExposureEntry {
    /// Autoincrement row id.
    pub id: i64,
    /// Collection the item belongs to.
    pub collection: String,
    /// Specific record UUID, or None for a collection-level event.
    pub item_id: Option<String>,
    /// Audience (e.g. "cc", "mcp", "user").
    pub exposed_to: String,
    /// Who granted consent (default "user").
    pub granted_by: String,
    /// RFC 3339 UTC timestamp of exposure.
    pub exposed_at: String,
}

// ── Keychain constants ────────────────────────────────────────────────────────

const KEYCHAIN_SERVICE: &str = "dev.cascade.personal";
const KEYCHAIN_ACCOUNT: &str = "vault-dek";

// ── PersonalVault handle ──────────────────────────────────────────────────────

/// Handle to the open personal vault.
///
/// # Purpose
/// Wraps the SQLite connection (mutex-guarded) and the 32-byte DEK (in-memory,
/// never logged). All public API functions take `&PersonalVault`.
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / PersonalVault
pub struct PersonalVault {
    conn: Arc<Mutex<Connection>>,
    /// 32-byte AES-256 data-encryption key — loaded from OS keychain at open.
    /// MUST NOT appear in debug output.
    dek: [u8; 32],
}

impl std::fmt::Debug for PersonalVault {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PersonalVault")
            .field("conn", &"<locked>")
            .field("dek", &"<redacted>")
            .finish()
    }
}

// ── DEK helpers ──────────────────────────────────────────────────────────────

/// Load the DEK from `keychain`, or generate and persist a new one if absent.
///
/// The DEK is stored as a hex-encoded string in the keychain so it survives
/// across restarts. NEVER log the raw bytes or the hex string.
fn load_or_create_dek(keychain: &dyn Keychain) -> Result<[u8; 32], VaultError> {
    match keychain.get_key(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT) {
        Ok(hex) => {
            let bytes = hex_decode_dek(&hex)?;
            Ok(bytes)
        }
        Err(cascade_keychain::KeychainError::NotFound) => {
            // Generate a fresh DEK.
            use rand::RngCore;
            let mut dek = [0u8; 32];
            rand::rngs::OsRng.fill_bytes(&mut dek);
            let hex = hex_encode(&dek);
            keychain.set_key(KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT, &hex)?;
            tracing::info!("personal vault: generated and stored new DEK in keychain");
            Ok(dek)
        }
        Err(e) => Err(VaultError::Keychain(e)),
    }
}

/// Hex-decode a 64-char hex string to [u8; 32]. No lossy decoding.
fn hex_decode_dek(hex: &str) -> Result<[u8; 32], VaultError> {
    if hex.len() != 64 {
        return Err(VaultError::Crypto("DEK hex must be 64 chars".into()));
    }
    let mut out = [0u8; 32];
    for (i, chunk) in hex.as_bytes().chunks(2).enumerate() {
        let s = std::str::from_utf8(chunk)
            .map_err(|_| VaultError::Crypto("invalid DEK hex".into()))?;
        out[i] = u8::from_str_radix(s, 16)
            .map_err(|_| VaultError::Crypto("invalid DEK hex digit".into()))?;
    }
    Ok(out)
}

fn hex_encode(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Open (or create) the personal vault at `db_path`, using `keychain` for the DEK.
///
/// # Purpose
/// Runs schema migrations, loads/generates the DEK, records vault_meta (once),
/// and returns a `PersonalVault` handle.
///
/// # Inputs
/// - `db_path`: path to `personal.db` (parent dirs created if missing).
/// - `keychain`: OS keychain or `InMemoryKeychain` for tests.
///
/// # Outputs
/// `PersonalVault` ready for queries.
///
/// # Constraints
/// DEK bytes never logged.
///
/// # Tauri TODO
/// `// TODO(rag-16-ui)` — wire as `#[tauri::command] async fn open_vault_cmd`
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / open_vault
pub fn open_vault(
    db_path: impl AsRef<Path>,
    keychain: &dyn Keychain,
) -> Result<PersonalVault, VaultError> {
    let db_path = db_path.as_ref();
    let mut conn = open_configured(db_path)?;
    migrate::registry().run(&mut conn, Some(db_path))?;

    // Ensure vault_meta row exists (idempotent).
    let now = Utc::now().to_rfc3339();
    conn.execute(
        "INSERT OR IGNORE INTO vault_meta (id, keychain_service, keychain_account, created_at)
         VALUES (1, ?1, ?2, ?3)",
        params![KEYCHAIN_SERVICE, KEYCHAIN_ACCOUNT, now],
    )?;

    let dek = load_or_create_dek(keychain)?;
    Ok(PersonalVault {
        conn: Arc::new(Mutex::new(conn)),
        dek,
    })
}

/// List all collection names and their sensitivity labels from the vault.
///
/// # Purpose
/// Returns `(name, label, sensitivity)` tuples for all seeded + custom collections.
///
/// # Tauri TODO
/// `// TODO(rag-16-ui)` — `#[tauri::command] async fn list_collections_cmd`
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / list_collections
pub fn list_collections(vault: &PersonalVault) -> Result<Vec<(String, String, String)>, VaultError> {
    let conn = vault.conn.lock().unwrap();
    let mut stmt =
        conn.prepare("SELECT name, label, sensitivity FROM collections ORDER BY name")?;
    let rows = stmt
        .query_map([], |r| Ok((r.get(0)?, r.get(1)?, r.get(2)?)))?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    Ok(rows)
}

/// Query all records in `collection`, filtered by `mode`.
///
/// # Purpose
/// Returns decrypted `VaultRecord`s. Returns an empty Vec (not an error) when
/// `mode` is `Normal` and the collection is restricted.
///
/// # Inputs
/// - `vault`: open vault handle.
/// - `collection`: target collection name.
/// - `mode`: current CascadeMode.
///
/// # Outputs
/// `Vec<VaultRecord>` — empty if gated by mode.
///
/// # Tauri TODO
/// `// TODO(rag-16-ui)` — `#[tauri::command] async fn query_records_cmd`
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / query_records
pub fn query_records(
    vault: &PersonalVault,
    collection: &str,
    mode: CascadeMode,
) -> Result<Vec<VaultRecord>, VaultError> {
    let col = Collection(collection.to_string());
    // Mode gate: restricted collections return 0 rows in Normal mode.
    if !can_expose(&col, None, mode) {
        return Ok(vec![]);
    }

    let conn = vault.conn.lock().unwrap();
    let mut stmt = conn.prepare(
        "SELECT id, collection, ciphertext, created_at, updated_at
         FROM records WHERE collection = ?1 ORDER BY updated_at DESC",
    )?;
    let records: Vec<(String, String, Vec<u8>, String, String)> = stmt
        .query_map(params![collection], |r| {
            Ok((
                r.get::<_, String>(0)?,
                r.get::<_, String>(1)?,
                r.get::<_, Vec<u8>>(2)?,
                r.get::<_, String>(3)?,
                r.get::<_, String>(4)?,
            ))
        })?
        .collect::<rusqlite::Result<Vec<_>>>()?;

    // stmt borrows conn — must drop stmt before we can drop conn.
    drop(stmt);
    drop(conn); // release mutex guard before crypto work

    let mut out = Vec::with_capacity(records.len());
    for (id, coll, blob, created_at, updated_at) in records {
        let plain = crypto_open(&vault.dek, &blob)?;
        let payload: serde_json::Value = serde_json::from_slice(&plain)?;
        out.push(VaultRecord {
            id,
            collection: coll,
            payload,
            created_at,
            updated_at,
        });
    }
    Ok(out)
}

/// Insert or update a record in `collection`.
///
/// # Purpose
/// Encrypts `payload` with the vault DEK and upserts into `records`.
/// The plaintext is NEVER stored; only the sealed blob is written.
///
/// # Inputs
/// - `vault`: open vault handle.
/// - `collection`: target collection name.
/// - `id`: UUID v4 string (if empty string, a new UUID is generated).
/// - `payload`: JSON value to encrypt and store.
///
/// # Outputs
/// The record UUID (generated or provided).
///
/// # Tauri TODO
/// `// TODO(rag-16-ui)` — `#[tauri::command] async fn upsert_record_cmd`
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / upsert_record
pub fn upsert_record(
    vault: &PersonalVault,
    collection: &str,
    id: Option<&str>,
    payload: &serde_json::Value,
) -> Result<String, VaultError> {
    let record_id = id
        .filter(|s| !s.is_empty())
        .map(|s| s.to_string())
        .unwrap_or_else(|| Uuid::new_v4().to_string());

    let plain = serde_json::to_vec(payload)?;
    let blob = crypto_seal(&vault.dek, &plain)?;
    let content_hash = cascade_db::content_hash(&plain);
    let now = Utc::now().to_rfc3339();

    let conn = vault.conn.lock().unwrap();
    // Verify collection exists.
    let exists: bool = conn.query_row(
        "SELECT COUNT(*) FROM collections WHERE name = ?1",
        params![collection],
        |r| r.get::<_, i64>(0),
    )? > 0;
    if !exists {
        return Err(VaultError::UnknownCollection(collection.to_string()));
    }

    conn.execute(
        "INSERT INTO records (id, collection, ciphertext, content_hash, created_at, updated_at)
         VALUES (?1, ?2, ?3, ?4, ?5, ?5)
         ON CONFLICT(id) DO UPDATE SET
             ciphertext   = excluded.ciphertext,
             content_hash = excluded.content_hash,
             updated_at   = excluded.updated_at",
        params![record_id, collection, blob, content_hash, now],
    )?;

    Ok(record_id)
}

/// Request (and log) consent to expose `item_id` in `collection` to `exposed_to`.
///
/// # Purpose
/// Inserts an `exposure_log` row. This is the audit trail; the return value
/// reflects `can_expose` — the caller should honour `false` and not proceed.
///
/// # Inputs
/// - `vault`: open vault handle.
/// - `collection`: collection name.
/// - `item_id`: specific record UUID, or `None` for collection-level consent.
/// - `exposed_to`: audience tag, e.g. `"cc"`, `"mcp"`, `"user"`.
/// - `mode`: current CascadeMode (passed to `can_expose`).
///
/// # Outputs
/// `true` if exposure is permitted (and has been logged); `false` if gated.
///
/// # Tauri TODO
/// `// TODO(rag-16-ui)` — `#[tauri::command] async fn request_consent_cmd`
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / request_consent
pub fn request_consent(
    vault: &PersonalVault,
    collection: &str,
    item_id: Option<&str>,
    exposed_to: &str,
    mode: CascadeMode,
) -> Result<bool, VaultError> {
    let col = Collection(collection.to_string());
    let permitted = can_expose(&col, item_id, mode);
    // Always log the request, even if denied (audit trail).
    let now = Utc::now().to_rfc3339();
    let conn = vault.conn.lock().unwrap();
    conn.execute(
        "INSERT INTO exposure_log (collection, item_id, exposed_to, granted_by, exposed_at)
         VALUES (?1, ?2, ?3, 'user', ?4)",
        params![collection, item_id, exposed_to, now],
    )?;
    Ok(permitted)
}

/// Return the exposure log entries for `collection` (most recent first).
///
/// # Purpose
/// Audit trail: who/what/when accessed or attempted to access items.
///
/// # Tauri TODO
/// `// TODO(rag-16-ui)` — `#[tauri::command] async fn exposure_log_cmd`
///
/// # SPORT
/// MASTER-COMPONENTS.md: cascade-personal / exposure_log
pub fn exposure_log(
    vault: &PersonalVault,
    collection: &str,
) -> Result<Vec<ExposureEntry>, VaultError> {
    let conn = vault.conn.lock().unwrap();
    let mut stmt = conn.prepare(
        "SELECT id, collection, item_id, exposed_to, granted_by, exposed_at
         FROM exposure_log WHERE collection = ?1 ORDER BY exposed_at DESC",
    )?;
    let rows = stmt
        .query_map(params![collection], |r| {
            Ok(ExposureEntry {
                id: r.get(0)?,
                collection: r.get(1)?,
                item_id: r.get(2)?,
                exposed_to: r.get(3)?,
                granted_by: r.get(4)?,
                exposed_at: r.get(5)?,
            })
        })?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    Ok(rows)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::crypto::TEST_DEK;
    use tempfile::tempdir;

    /// Build a vault backed by an InMemoryKeychain pre-loaded with the test DEK.
    fn test_vault(dir: &tempfile::TempDir) -> PersonalVault {
        let kc = InMemoryKeychain::new();
        // Pre-seed the keychain with the test DEK so load_or_create_dek uses it.
        kc.set_key(
            KEYCHAIN_SERVICE,
            KEYCHAIN_ACCOUNT,
            &hex_encode(&TEST_DEK),
        )
        .unwrap();
        open_vault(dir.path().join("personal.db"), &kc).unwrap()
    }

    // ── Crypto invariant ─────────────────────────────────────────────────────

    #[test]
    fn stored_field_is_ciphertext_not_plaintext() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let ssn_payload = serde_json::json!({"ssn": "123-45-6789"});
        upsert_record(&vault, "health", None, &ssn_payload).unwrap();

        // Read the raw BLOB from the DB directly — must not contain the plaintext.
        let conn = vault.conn.lock().unwrap();
        let blob: Vec<u8> = conn
            .query_row("SELECT ciphertext FROM records LIMIT 1", [], |r| r.get(0))
            .unwrap();
        let plaintext = serde_json::to_vec(&ssn_payload).unwrap();
        assert!(
            !blob.windows(plaintext.len()).any(|w| w == plaintext.as_slice()),
            "plaintext SSN must not appear in stored ciphertext blob"
        );
    }

    // ── Mode gate ────────────────────────────────────────────────────────────

    #[test]
    fn health_collection_returns_zero_rows_in_normal_mode() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let payload = serde_json::json!({"diagnosis": "private"});
        upsert_record(&vault, "health", None, &payload).unwrap();

        let rows = query_records(&vault, "health", CascadeMode::Normal).unwrap();
        assert_eq!(rows.len(), 0, "health must return 0 rows in Normal mode");
    }

    #[test]
    fn health_collection_returns_rows_in_personal_mode() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let payload = serde_json::json!({"diagnosis": "private"});
        upsert_record(&vault, "health", None, &payload).unwrap();

        let rows = query_records(&vault, "health", CascadeMode::Personal).unwrap();
        assert_eq!(rows.len(), 1, "health must return rows in Personal mode");
        assert_eq!(rows[0].payload["diagnosis"], "private");
    }

    #[test]
    fn finance_returns_zero_rows_in_normal_mode() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        upsert_record(&vault, "finance", None, &serde_json::json!({"amount": 100})).unwrap();
        let rows = query_records(&vault, "finance", CascadeMode::Normal).unwrap();
        assert_eq!(rows.len(), 0);
    }

    // ── Exposure log ─────────────────────────────────────────────────────────

    #[test]
    fn consent_request_is_logged() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let id = upsert_record(
            &vault,
            "people",
            None,
            &serde_json::json!({"name": "Alice"}),
        )
        .unwrap();

        request_consent(&vault, "people", Some(&id), "cc", CascadeMode::Normal).unwrap();

        let log = exposure_log(&vault, "people").unwrap();
        assert_eq!(log.len(), 1);
        assert_eq!(log[0].exposed_to, "cc");
        assert_eq!(log[0].item_id, Some(id));
    }

    #[test]
    fn denied_consent_is_also_logged() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        // Finance is restricted in Normal mode — consent denied but still logged.
        let permitted =
            request_consent(&vault, "finance", None, "mcp", CascadeMode::Normal).unwrap();
        assert!(!permitted, "finance consent must be denied in Normal mode");

        let log = exposure_log(&vault, "finance").unwrap();
        assert_eq!(log.len(), 1, "denied consent must still be logged");
    }

    // ── DEK from keychain ────────────────────────────────────────────────────

    #[test]
    fn dek_comes_from_keychain_not_hardcoded() {
        let dir = tempdir().unwrap();
        // Use InMemoryKeychain (mocked OS keychain) — confirms the keychain path.
        let kc = InMemoryKeychain::new();
        let vault = open_vault(dir.path().join("personal.db"), &kc).unwrap();
        // Insert and retrieve to confirm the DEK round-trips correctly.
        let id = upsert_record(
            &vault,
            "people",
            None,
            &serde_json::json!({"test": true}),
        )
        .unwrap();
        let rows = query_records(&vault, "people", CascadeMode::Normal).unwrap();
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].id, id);
    }

    // ── Upsert round-trip ────────────────────────────────────────────────────

    #[test]
    fn upsert_and_query_roundtrip() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let payload = serde_json::json!({"name": "Bob", "email": "bob@example.com"});
        let id = upsert_record(&vault, "people", None, &payload).unwrap();
        let rows = query_records(&vault, "people", CascadeMode::Normal).unwrap();
        assert_eq!(rows.len(), 1);
        assert_eq!(rows[0].id, id);
        assert_eq!(rows[0].payload["name"], "Bob");
    }

    #[test]
    fn upsert_updates_existing_record() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let id = upsert_record(
            &vault,
            "people",
            None,
            &serde_json::json!({"name": "Alice"}),
        )
        .unwrap();
        // Update with same ID.
        upsert_record(
            &vault,
            "people",
            Some(&id),
            &serde_json::json!({"name": "Alice Updated"}),
        )
        .unwrap();
        let rows = query_records(&vault, "people", CascadeMode::Normal).unwrap();
        assert_eq!(rows.len(), 1, "no duplicate rows on update");
        assert_eq!(rows[0].payload["name"], "Alice Updated");
    }

    #[test]
    fn list_collections_returns_six_seeded() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let cols = list_collections(&vault).unwrap();
        assert_eq!(cols.len(), 6);
    }

    #[test]
    fn unknown_collection_returns_error() {
        let dir = tempdir().unwrap();
        let vault = test_vault(&dir);
        let err = upsert_record(
            &vault,
            "does_not_exist",
            None,
            &serde_json::json!({}),
        )
        .unwrap_err();
        assert!(matches!(err, VaultError::UnknownCollection(_)));
    }
}
