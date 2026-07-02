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
//! SPORT: MASTER-CLI.md — cascade update check/apply/auto (T-P4-E04-16)

pub mod bundle;
pub mod downloader;
pub mod full_bundle;
pub mod ipc_handlers;
pub mod snapshot;
pub mod verify;

// NOTE: these re-exports are the public API surface for the `cascade-daemon`
// *library* target (consumed by integration tests / cascade-cli). The
// `cascaded` *binary* target (main.rs) only calls through the fully-qualified
// `crate::updates::{check_for_update,apply_update,set_auto_update}` paths in
// ipc.rs, so several of these re-exports look unused from the bin target's
// point of view — allow that rather than dropping API surface the lib target
// still needs.
#[allow(unused_imports)]
pub use bundle::{DeltaBundle, FileEntry, Manifest};
#[allow(unused_imports)]
pub use downloader::{DownloadError, UpdateChecker, UpdateStatus};
#[allow(unused_imports)]
pub use full_bundle::{FullBundleError, FullBundleInstaller, FullBundleOutcome};
pub use ipc_handlers::{apply_update, check_for_update, set_auto_update};
#[allow(unused_imports)]
pub use snapshot::{Snapshot, SnapshotMetadata};
#[allow(unused_imports)]
pub use verify::{verify_bundle, UpdateError};
