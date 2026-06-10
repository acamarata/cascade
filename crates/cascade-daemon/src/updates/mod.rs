//! Update system — delta bundle format, snapshot layout, and signature verification.
//!
//! Purpose: Implements the `.cascade-delta` bundle format for atomic protocol-file
//! updates and the snapshot directory layout used by the update + rollback system.
//! Provides Ed25519 signature verification over bundle manifests.
//!
//! Inputs:
//!   - Tarball paths (`.cascade-delta` files, tar.gz format)
//!   - Snapshot root path (`~/.cascade/snapshots/`)
//!   - Protocol file paths to snapshot before applying a delta
//!
//! Outputs:
//!   - Parsed `DeltaBundle` structs with manifest + extracted payload bytes
//!   - `Snapshot` directories under `snapshot-{unix_timestamp}/` with `metadata.json`
//!   - `UpdateError` variants for signature failures, hash mismatches, I/O errors
//!
//! Constraints:
//!   - Tarball extraction uses a unique temp dir per call (prevents TOCTOU).
//!   - Serde structs use `deny_unknown_fields` to prevent silent manifest parsing bugs.
//!   - Ed25519 signature is over SHA-256(manifest.json bytes) — see verify.rs.
//!   - The embedded public key comes from `CASCADE_UPDATE_PUBKEY` env at compile time.
//!   - Private keys are NEVER stored in the repo; test keypairs are generated in-test.
//!
//! SPORT: MASTER-COMPONENTS.md — DeltaBundle, Snapshot, SignatureVerifier (T-P4-E04-12/13)

pub mod bundle;
pub mod downloader;
pub mod snapshot;
pub mod verify;

pub use bundle::{DeltaBundle, FileEntry, Manifest};
pub use downloader::{DownloadError, UpdateChecker, UpdateStatus};
pub use snapshot::{Snapshot, SnapshotMetadata};
pub use verify::{verify_bundle, UpdateError};
