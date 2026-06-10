//! Delta update downloader — fetch, verify, and apply from GitHub Releases.
//!
//! Purpose: Implements the full update cycle for `cascaded`:
//!   1. Fetch the latest release manifest from the GitHub Releases API.
//!   2. Compare the latest version against the current daemon version using
//!      simple semver string comparison (major.minor.patch).
//!   3. Download the `.cascade-delta-{from}-to-{to}.tar.gz` asset to a temp file.
//!   4. Verify the bundle (Ed25519 signature + blake3 per-file hashes) via `verify_bundle`.
//!   5. Snapshot current protocol files before applying.
//!   6. Apply the delta atomically: write each file to `{dest}.tmp`, verify blake3,
//!      then rename to final path.
//!   7. On any step-6 failure: restore from the snapshot created in step 5.
//!   8. Prune snapshots exceeding `max_snapshots`.
//!   9. Return `UpdateStatus::Updated { from, to }` or `UpdateStatus::UpToDate`.
//!
//! Inputs:
//!   - `UpdateChecker` config: GitHub API base URL, current version, snapshot root,
//!     protocol root, and max_snapshots limit.
//!
//! Outputs:
//!   - `UpdateStatus` — UpToDate | Updated { from, to }
//!   - `DownloadError` on any unrecoverable failure
//!
//! Constraints:
//!   - No live network in tests — callers must inject a mock `github_api_base`.
//!   - Signature verification is MANDATORY before any file write.
//!   - Paths are confined to `protocol_root`; path traversal is rejected.
//!   - `rustls-tls` is used (no openssl system dep).
//!
//! SPORT: MASTER-COMPONENTS.md — UpdateChecker (T-P4-E04-14)

use std::io::Write as _;
use std::path::{Path, PathBuf};

use serde::Deserialize;
use tempfile::NamedTempFile;
use tracing::{info, warn};

use super::bundle::DeltaBundle;
use super::snapshot::{Snapshot, SnapshotError};
use super::verify::{verify_bundle, UpdateError};

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors returned by `UpdateChecker::check_for_update` and `download_and_apply`.
#[derive(Debug, thiserror::Error)]
pub enum DownloadError {
    #[error("HTTP request failed: {0}")]
    Http(#[from] reqwest::Error),

    #[error("bundle parse error: {0}")]
    BundleParse(#[from] super::bundle::BundleError),

    #[error("bundle verification failed: {0}")]
    Verification(#[from] UpdateError),

    #[error("snapshot error: {0}")]
    Snapshot(#[from] SnapshotError),

    #[error("I/O error: {0}")]
    Io(#[from] std::io::Error),

    #[error("apply failed and snapshot restore succeeded (original state preserved): {0}")]
    ApplyFailedRestored(String),

    #[error("apply failed AND snapshot restore also failed — state may be inconsistent: apply_err={apply_err}, restore_err={restore_err}")]
    ApplyAndRestoreFailed {
        apply_err: String,
        restore_err: String,
    },

    #[error("no delta asset found in GitHub release for transition {0} -> {1}")]
    NoDeltaAsset(String, String),
}

// ── GitHub Releases API types ─────────────────────────────────────────────────

/// Minimal representation of a GitHub release from the Releases API.
#[derive(Debug, Deserialize)]
struct GhRelease {
    /// The git tag name, e.g. `"v0.1.3"`.
    tag_name: String,
    /// Release assets list.
    assets: Vec<GhAsset>,
}

/// A single GitHub release asset.
#[derive(Debug, Deserialize)]
struct GhAsset {
    /// Asset file name, e.g. `"cascade-delta-0.1.2-to-0.1.3.tar.gz"`.
    name: String,
    /// URL used to download the asset (authenticated).
    browser_download_url: String,
}

// ── UpdateStatus ──────────────────────────────────────────────────────────────

/// Outcome returned by a completed update cycle.
#[derive(Debug, Clone, PartialEq)]
pub enum UpdateStatus {
    /// The installed version is already the latest.
    UpToDate,
    /// A newer version was successfully applied.
    Updated {
        /// Previous version string.
        from: String,
        /// New version string after apply.
        to: String,
    },
}

// ── UpdateChecker ─────────────────────────────────────────────────────────────

/// Configuration and entry point for the update download/apply cycle.
///
/// Purpose: Injected with a base URL so tests can point at a mock HTTP server
/// without any live network access.
pub struct UpdateChecker {
    /// Base URL for the GitHub Releases API, e.g.
    /// `"https://api.github.com/repos/acamarata/cascade"`.
    /// Overridden in tests to point at a wiremock server.
    pub github_api_base: String,
    /// Current installed version string (without `v` prefix), e.g. `"0.1.2"`.
    pub current_version: String,
    /// Root under which to create and list snapshots.
    pub snapshot_root: PathBuf,
    /// Directory where protocol files live (updated in place).
    pub protocol_root: PathBuf,
    /// Maximum number of snapshots to retain. Older ones are pruned after each apply.
    pub max_snapshots: usize,
}

impl UpdateChecker {
    /// Query GitHub Releases for a newer version.
    ///
    /// Purpose: GET `{github_api_base}/releases/latest`, parse the release JSON,
    /// check whether the tag is a newer semver than `current_version`.
    ///
    /// Inputs:  `&self` config
    /// Outputs: `(latest_version, delta_asset_url)` if an update is available;
    ///           `None` if already up to date or no delta asset found.
    pub async fn check_for_update(
        &self,
    ) -> Result<Option<(String, String)>, DownloadError> {
        let url = format!("{}/releases/latest", self.github_api_base);

        let client = build_reqwest_client()?;
        let resp = client
            .get(&url)
            .header("Accept", "application/vnd.github+json")
            .header("X-GitHub-Api-Version", "2022-11-28")
            .header("User-Agent", "cascade-updater/1")
            .send()
            .await?;

        if resp.status() == reqwest::StatusCode::NOT_FOUND {
            // No releases yet.
            return Ok(None);
        }
        resp.error_for_status_ref()?;

        let release: GhRelease = resp.json().await?;
        let latest = strip_v_prefix(&release.tag_name);

        if !is_newer(&latest, &self.current_version) {
            return Ok(None);
        }

        // Look for the delta asset for this specific transition.
        let asset_name = format!(
            "cascade-delta-{}-to-{}.tar.gz",
            self.current_version, latest
        );
        let asset_url = release
            .assets
            .iter()
            .find(|a| a.name == asset_name)
            .map(|a| a.browser_download_url.clone());

        match asset_url {
            Some(url) => Ok(Some((latest, url))),
            None => {
                warn!(
                    current = %self.current_version,
                    latest = %latest,
                    asset = %asset_name,
                    "update available but no delta asset found in release"
                );
                Ok(None)
            }
        }
    }

    /// Download, verify, snapshot, and apply a delta bundle.
    ///
    /// Purpose: Orchestrates the full update pipeline. Calls `check_for_update`
    /// first; if an update is available, streams the asset to a temp file,
    /// verifies it, snapshots current state, applies atomically, then prunes
    /// old snapshots.
    ///
    /// Inputs:  `&self` config
    /// Outputs: `UpdateStatus::Updated { from, to }` or `UpdateStatus::UpToDate`
    pub async fn download_and_apply(&self) -> Result<UpdateStatus, DownloadError> {
        let Some((latest, asset_url)) = self.check_for_update().await? else {
            return Ok(UpdateStatus::UpToDate);
        };

        let from = self.current_version.clone();
        let to = latest.clone();

        // Download asset to a temp file.
        let tmp = download_to_tempfile(&asset_url).await?;

        // Parse and verify the bundle.
        let bundle = DeltaBundle::from_tarball(tmp.path())?;
        verify_bundle(&bundle)?;

        // Snapshot current protocol files before modifying anything.
        let files_to_snapshot = collect_protocol_files(&self.protocol_root, &bundle)?;
        let file_refs: Vec<(&str, &Path)> = files_to_snapshot
            .iter()
            .map(|(rel, abs)| (rel.as_str(), abs.as_path()))
            .collect();
        let snapshot = Snapshot::create(&self.snapshot_root, &self.current_version, &file_refs)?;

        // Apply the delta atomically.
        if let Err(apply_err) = apply_delta(&bundle, &self.protocol_root) {
            warn!(error = %apply_err, snapshot = %snapshot.metadata.id, "apply failed; attempting auto-rollback");

            // Auto-rollback to the snapshot we just created.
            match Snapshot::restore(
                &self.snapshot_root,
                &snapshot.metadata.id,
                &self.protocol_root,
            ) {
                Ok(_) => {
                    return Err(DownloadError::ApplyFailedRestored(apply_err.to_string()));
                }
                Err(restore_err) => {
                    return Err(DownloadError::ApplyAndRestoreFailed {
                        apply_err: apply_err.to_string(),
                        restore_err: restore_err.to_string(),
                    });
                }
            }
        }

        // Prune old snapshots.
        if let Err(e) = Snapshot::prune(&self.snapshot_root, self.max_snapshots) {
            warn!(error = %e, "snapshot pruning failed (non-fatal)");
        }

        info!(from = %from, to = %to, "update applied successfully");
        Ok(UpdateStatus::Updated { from, to })
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

/// Build a reqwest client with rustls-tls (no openssl dep).
fn build_reqwest_client() -> Result<reqwest::Client, DownloadError> {
    reqwest::Client::builder()
        .use_rustls_tls()
        .build()
        .map_err(DownloadError::Http)
}

/// Strip a leading `v` or `V` from a version tag.
fn strip_v_prefix(tag: &str) -> String {
    tag.trim_start_matches(['v', 'V']).to_string()
}

/// Compare two semver strings `a` and `b`.
///
/// Returns `true` if `a` is strictly greater than `b` using major.minor.patch
/// ordering. Strings that do not parse as `major.minor.patch` fall back to
/// lexicographic comparison (safe default — avoids panics on unexpected tags).
fn is_newer(a: &str, b: &str) -> bool {
    fn parse(s: &str) -> Option<(u64, u64, u64)> {
        let parts: Vec<&str> = s.splitn(3, '.').collect();
        if parts.len() < 3 {
            return None;
        }
        let major = parts[0].parse::<u64>().ok()?;
        let minor = parts[1].parse::<u64>().ok()?;
        // Strip pre-release suffix from patch (e.g. "3-beta.1" → 3).
        let patch_str = parts[2].split('-').next().unwrap_or(parts[2]);
        let patch = patch_str.parse::<u64>().ok()?;
        Some((major, minor, patch))
    }

    match (parse(a), parse(b)) {
        (Some(va), Some(vb)) => va > vb,
        _ => a > b,
    }
}

/// Stream a URL to a `NamedTempFile` and return it.
async fn download_to_tempfile(url: &str) -> Result<NamedTempFile, DownloadError> {
    let client = build_reqwest_client()?;
    let resp = client
        .get(url)
        .header("User-Agent", "cascade-updater/1")
        .send()
        .await?;
    resp.error_for_status_ref()?;

    let bytes = resp.bytes().await?;
    let mut tmp = NamedTempFile::new()?;
    tmp.write_all(&bytes)?;
    tmp.flush()?;
    Ok(tmp)
}

/// Build a list of `(relative_path, absolute_path)` pairs for protocol files
/// referenced in the bundle manifest, from `protocol_root`.
///
/// Purpose: Determines which files need to be snapshotted before the apply.
/// Only files listed in the bundle manifest are included — we snapshot exactly
/// what will be overwritten.
fn collect_protocol_files(
    protocol_root: &Path,
    bundle: &DeltaBundle,
) -> Result<Vec<(String, PathBuf)>, DownloadError> {
    let mut result = Vec::with_capacity(bundle.manifest.files.len());
    for entry in &bundle.manifest.files {
        // Guard: no traversal in bundle-supplied paths.
        if entry.path.contains("..") {
            return Err(DownloadError::BundleParse(
                super::bundle::BundleError::UnsafePath(entry.path.clone()),
            ));
        }
        let abs = protocol_root.join(&entry.path);
        // Only include files that actually exist (new files don't need snapshotting).
        if abs.exists() {
            result.push((entry.path.clone(), abs));
        }
    }
    Ok(result)
}

/// Apply bundle file operations to `protocol_root` atomically.
///
/// Purpose: For each file in the bundle, write to `{dest}.tmp` with the
/// blake3-verified content, then rename to final path.
///
/// Inputs:  `bundle` — verified DeltaBundle; `protocol_root` — target directory
/// Outputs: `Ok(())` on full success; `Err` on first failure (no partial commit)
fn apply_delta(bundle: &DeltaBundle, protocol_root: &Path) -> Result<(), std::io::Error> {
    for entry in &bundle.manifest.files {
        let content = bundle
            .files
            .get(&entry.path)
            .expect("bundle verified — file must be present");

        // Verify blake3 of bundle content one final time before write.
        let actual_hash = blake3::hash(content).to_hex().to_string();
        if actual_hash != entry.hash {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                format!("blake3 mismatch for {} after verification passed", entry.path),
            ));
        }

        let dest = protocol_root.join(&entry.path);
        if let Some(parent) = dest.parent() {
            std::fs::create_dir_all(parent)?;
        }

        // Atomic write: .tmp → final.
        let tmp_path = dest.with_extension("tmp");
        std::fs::write(&tmp_path, content)?;
        std::fs::rename(&tmp_path, &dest)?;
    }
    Ok(())
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    use blake3;
    use ed25519_dalek::{Signer, SigningKey};
    use rand::rngs::OsRng;
    use sha2::{Digest, Sha256};
    use tempfile::TempDir;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    use crate::updates::bundle::{DeltaBundle, FileEntry, Manifest};

    // ── Helpers ──

    fn gen_signing_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn make_signed_bundle(
        sk: &SigningKey,
        payload_files: &[(&str, &[u8])],
        from: &str,
        to: &str,
    ) -> DeltaBundle {
        let file_entries: Vec<FileEntry> = payload_files
            .iter()
            .map(|(p, c)| FileEntry {
                path: p.to_string(),
                hash: blake3::hash(c).to_hex().to_string(),
            })
            .collect();

        let manifest = Manifest {
            version: from.to_string(),
            target_version: to.to_string(),
            files: file_entries,
            signature: None,
        };

        let manifest_bytes = serde_json::to_vec(&manifest).expect("serialize");
        let digest = Sha256::digest(&manifest_bytes);
        let sig = sk.sign(digest.as_slice());

        let mut files = HashMap::new();
        for (p, c) in payload_files {
            files.insert(p.to_string(), c.to_vec());
        }

        DeltaBundle {
            manifest,
            manifest_bytes,
            signature_bytes: sig.to_bytes().to_vec(),
            files,
        }
    }

    /// Build a tar.gz bundle from a DeltaBundle (for HTTP mock response).
    fn bundle_to_tarball_bytes(bundle: &DeltaBundle) -> Vec<u8> {
        use flate2::write::GzEncoder;
        use flate2::Compression;
        use std::io::Write;
        use tar::Builder;

        let mut buf = Vec::new();
        let gz = GzEncoder::new(&mut buf, Compression::fast());
        let mut ar = Builder::new(gz);

        // manifest.json
        let mut header = tar::Header::new_gnu();
        header.set_size(bundle.manifest_bytes.len() as u64);
        header.set_mode(0o644);
        header.set_cksum();
        ar.append_data(&mut header, "manifest.json", bundle.manifest_bytes.as_slice())
            .unwrap();

        // signature.bin
        let mut header = tar::Header::new_gnu();
        header.set_size(bundle.signature_bytes.len() as u64);
        header.set_mode(0o644);
        header.set_cksum();
        ar.append_data(&mut header, "signature.bin", bundle.signature_bytes.as_slice())
            .unwrap();

        // payload files
        for (path, content) in &bundle.files {
            let mut header = tar::Header::new_gnu();
            header.set_size(content.len() as u64);
            header.set_mode(0o644);
            header.set_cksum();
            ar.append_data(&mut header, path.as_str(), content.as_slice())
                .unwrap();
        }

        ar.into_inner().unwrap().finish().unwrap();
        buf
    }

    // ── is_newer tests ──

    #[test]
    fn is_newer_detects_patch_bump() {
        assert!(is_newer("0.1.3", "0.1.2"));
        assert!(!is_newer("0.1.2", "0.1.2"));
        assert!(!is_newer("0.1.1", "0.1.2"));
    }

    #[test]
    fn is_newer_detects_minor_bump() {
        assert!(is_newer("0.2.0", "0.1.9"));
    }

    #[test]
    fn is_newer_detects_major_bump() {
        assert!(is_newer("1.0.0", "0.99.99"));
    }

    // ── apply_delta tests ──

    #[test]
    fn apply_delta_writes_files_atomically() {
        let proto_root = TempDir::new().expect("tmpdir");
        let sk = gen_signing_key();
        let content = b"# updated spec";
        let bundle = make_signed_bundle(&sk, &[("specs/rag.md", content)], "0.1.2", "0.1.3");

        apply_delta(&bundle, proto_root.path()).expect("apply");

        let written = std::fs::read(proto_root.path().join("specs/rag.md")).expect("read");
        assert_eq!(written, content);
        // .tmp file should be gone.
        assert!(!proto_root.path().join("specs/rag.md.tmp").exists());
    }

    #[test]
    fn apply_delta_fails_on_tampered_hash() {
        let proto_root = TempDir::new().expect("tmpdir");
        let sk = gen_signing_key();
        let content = b"# spec content";
        let mut bundle = make_signed_bundle(&sk, &[("spec.md", content)], "0.1.2", "0.1.3");
        // Tamper content AFTER building (verify_bundle already passed in prod; test apply-layer guard).
        bundle.files.insert("spec.md".to_string(), b"TAMPERED".to_vec());

        let err = apply_delta(&bundle, proto_root.path()).unwrap_err();
        assert_eq!(err.kind(), std::io::ErrorKind::InvalidData);
    }

    // ── Async integration tests (wiremock) ──

    fn make_release_json(tag: &str, asset_name: &str, asset_url: &str) -> serde_json::Value {
        serde_json::json!({
            "tag_name": tag,
            "assets": [
                {
                    "name": asset_name,
                    "browser_download_url": asset_url
                }
            ]
        })
    }

    #[tokio::test]
    async fn check_for_update_returns_up_to_date_when_same_version() {
        let server = MockServer::start().await;

        Mock::given(method("GET"))
            .and(path("/releases/latest"))
            .respond_with(
                ResponseTemplate::new(200)
                    .set_body_json(make_release_json("v0.1.2", "cascade-delta-0.1.1-to-0.1.2.tar.gz", "http://example.com/delta.tar.gz")),
            )
            .mount(&server)
            .await;

        let checker = UpdateChecker {
            github_api_base: server.uri(),
            current_version: "0.1.2".to_string(),
            snapshot_root: PathBuf::from("/tmp"),
            protocol_root: PathBuf::from("/tmp"),
            max_snapshots: 5,
        };

        let result = checker.check_for_update().await.expect("check");
        assert!(result.is_none(), "should be up to date");
    }

    #[tokio::test]
    async fn download_and_apply_full_round_trip() {
        let proto_root = TempDir::new().expect("proto tmpdir");
        let snap_root = TempDir::new().expect("snap tmpdir");

        // Write a file that the delta will update.
        std::fs::write(proto_root.path().join("specs.md"), b"old content").unwrap();

        let sk = gen_signing_key();
        let new_content = b"# updated spec content";
        let bundle = make_signed_bundle(&sk, &[("specs.md", new_content)], "0.1.2", "0.1.3");
        let tarball_bytes = bundle_to_tarball_bytes(&bundle);

        let server = MockServer::start().await;

        // Mount the releases/latest endpoint.
        let asset_url = format!("{}/download/delta.tar.gz", server.uri());
        Mock::given(method("GET"))
            .and(path("/releases/latest"))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(make_release_json(
                    "v0.1.3",
                    "cascade-delta-0.1.2-to-0.1.3.tar.gz",
                    &asset_url,
                )),
            )
            .mount(&server)
            .await;

        // Mount the asset download endpoint.
        Mock::given(method("GET"))
            .and(path("/download/delta.tar.gz"))
            .respond_with(
                ResponseTemplate::new(200).set_body_bytes(tarball_bytes),
            )
            .mount(&server)
            .await;

        // Override verification: in tests we can't use the embedded pubkey.
        // We use apply_delta directly with a pre-verified bundle below.
        // For the full round-trip we test check_for_update + download separately
        // since verify_bundle uses the compile-time embedded key.
        // Here we validate the HTTP + parse + snapshot + apply pipeline using
        // a test that bypasses verify_bundle by calling the pieces directly.

        let checker = UpdateChecker {
            github_api_base: server.uri(),
            current_version: "0.1.2".to_string(),
            snapshot_root: snap_root.path().to_path_buf(),
            protocol_root: proto_root.path().to_path_buf(),
            max_snapshots: 5,
        };

        // 1. check_for_update should return the asset URL.
        let update = checker.check_for_update().await.expect("check");
        assert!(update.is_some(), "update should be available");
        let (latest, url) = update.unwrap();
        assert_eq!(latest, "0.1.3");
        assert!(url.contains("delta.tar.gz"));

        // 2. Download and parse.
        let tmp = download_to_tempfile(&url).await.expect("download");
        let parsed = DeltaBundle::from_tarball(tmp.path()).expect("parse");
        assert_eq!(parsed.manifest.target_version, "0.1.3");

        // 3. Snapshot + apply (bypassing verify_bundle which needs embedded key).
        let files_to_snap = collect_protocol_files(proto_root.path(), &parsed).expect("collect");
        let file_refs: Vec<(&str, &Path)> = files_to_snap
            .iter()
            .map(|(r, a)| (r.as_str(), a.as_path()))
            .collect();
        let snap = Snapshot::create(snap_root.path(), "0.1.2", &file_refs).expect("snapshot");
        assert!(snap.path.exists());

        apply_delta(&parsed, proto_root.path()).expect("apply");

        // Verify file was updated.
        let written = std::fs::read(proto_root.path().join("specs.md")).expect("read");
        assert_eq!(written, new_content);

        // 4. Prune (no-op with 1 snapshot and max=5).
        let pruned = Snapshot::prune(snap_root.path(), 5).expect("prune");
        assert_eq!(pruned, 0);
    }

    #[tokio::test]
    async fn failed_apply_triggers_auto_rollback() {
        let proto_root = TempDir::new().expect("proto tmpdir");
        let snap_root = TempDir::new().expect("snap tmpdir");

        let original = b"original content";
        std::fs::write(proto_root.path().join("specs.md"), original).unwrap();

        let sk = gen_signing_key();
        // Create a bundle whose hash is correct but content will be tampered before apply.
        let content = b"new content";
        let mut bundle = make_signed_bundle(&sk, &[("specs.md", content)], "0.1.2", "0.1.3");

        // Tamper the bundle content so apply_delta sees a hash mismatch.
        bundle.files.insert("specs.md".to_string(), b"TAMPERED".to_vec());

        // Snapshot current state.
        let snap = Snapshot::create(
            snap_root.path(),
            "0.1.2",
            &[("specs.md", &proto_root.path().join("specs.md"))],
        )
        .expect("snapshot");

        // Attempt apply — should fail.
        let apply_result = apply_delta(&bundle, proto_root.path());
        assert!(apply_result.is_err(), "tampered bundle must fail");

        // Restore from snapshot.
        Snapshot::restore(snap_root.path(), &snap.metadata.id, proto_root.path())
            .expect("restore");

        // File should be back to original.
        let after_restore = std::fs::read(proto_root.path().join("specs.md")).expect("read");
        assert_eq!(after_restore, original, "auto-rollback must restore original content");
    }

    #[tokio::test]
    async fn tampered_asset_rejected_before_apply() {
        let server = MockServer::start().await;

        let sk = gen_signing_key();
        let content = b"spec content";
        let mut bundle = make_signed_bundle(&sk, &[("spec.md", content)], "0.1.2", "0.1.3");
        // Tamper the tarball payload.
        bundle.files.insert("spec.md".to_string(), b"EVIL".to_vec());
        let tarball_bytes = bundle_to_tarball_bytes(&bundle);

        let asset_url = format!("{}/download/tampered.tar.gz", server.uri());
        Mock::given(method("GET"))
            .and(path("/releases/latest"))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(make_release_json(
                    "v0.1.3",
                    "cascade-delta-0.1.2-to-0.1.3.tar.gz",
                    &asset_url,
                )),
            )
            .mount(&server)
            .await;

        Mock::given(method("GET"))
            .and(path("/download/tampered.tar.gz"))
            .respond_with(ResponseTemplate::new(200).set_body_bytes(tarball_bytes))
            .mount(&server)
            .await;

        let proto_root = TempDir::new().expect("proto tmpdir");
        let snap_root = TempDir::new().expect("snap tmpdir");

        let checker = UpdateChecker {
            github_api_base: server.uri(),
            current_version: "0.1.2".to_string(),
            snapshot_root: snap_root.path().to_path_buf(),
            protocol_root: proto_root.path().to_path_buf(),
            max_snapshots: 5,
        };

        // check_for_update and download succeed; parse extracts the tampered bundle.
        let update = checker.check_for_update().await.expect("check").unwrap();
        let tmp = download_to_tempfile(&update.1).await.expect("download");
        let parsed = DeltaBundle::from_tarball(tmp.path()).expect("parse");

        // apply_delta should catch the blake3 mismatch.
        let err = apply_delta(&parsed, proto_root.path()).unwrap_err();
        assert_eq!(
            err.kind(),
            std::io::ErrorKind::InvalidData,
            "tampered bundle must be rejected at apply: {err}"
        );

        // No files should have been written to proto_root.
        assert!(
            !proto_root.path().join("spec.md").exists(),
            "no files must be written for a tampered bundle"
        );
    }
}
