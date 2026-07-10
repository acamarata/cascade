//! # cascade-personal
//!
//! Purpose: Encrypted, isolated structured personal-data store for Cascade.
//! All records are AES-256-GCM encrypted at rest; the data-encryption key (DEK)
//! lives in the OS keychain via `cascade-keychain`. Medical and financial
//! collections are hidden from the query layer in non-Personal mode. Any
//! CC/MCP exposure requires explicit per-item consent logged to `exposure_log`.
//!
//! ## Architecture
//! ```text
//! open_vault(path, keychain)
//!   → migrate schema (personal.db)
//!   → load/generate DEK via OS keychain
//!
//! upsert_record(collection, item)  → encrypt(item.payload) → BLOB in DB
//! query_records(collection, mode)  → gate check → SELECT ciphertext → decrypt
//! request_consent(collection, id)  → exposure_log INSERT
//! exposure_log(collection)         → SELECT from exposure_log
//! ```
//!
//! ## Security invariants
//! - Secret values (DEK, plaintext payloads) MUST NEVER appear in tracing, errors, or logs.
//! - Encrypted BLOBs are the only form stored; SELECT returns ciphertext.
//! - In non-Personal mode, medical/financial collections return 0 rows.
//!
//! ## Tauri wiring
//!
//! SPORT: cascade-personal / lib

pub mod crypto;
pub mod gate;
pub mod migrate;
pub mod vault;

pub use gate::{can_expose, CascadeMode, Collection};
pub use vault::{
    exposure_log, list_collections, open_vault, query_records, request_consent, upsert_record,
    ExposureEntry, PersonalVault, VaultError, VaultRecord,
};
