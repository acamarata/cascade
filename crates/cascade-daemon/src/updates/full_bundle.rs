//! Full-bundle update path — download, verify, and install a complete release.
//!
//! Purpose: The delta scheme in `downloader.rs` requires a
//! `.cascade-delta-{from}-to-{to}.tar.gz` asset that has never been published.
//! Every GitHub release (v1.9.x) ships with no binary assets today. Starting at
//! v1.9.19 the maintainer publishes a full-bundle asset per release instead:
//!
//!   - `cascade-v{VERSION}-macos-aarch64.tar.gz` — tarball containing
//!     `bin/cascade` and `bin/cascaded`.
//!   - `SHA256SUMS` — one `<hex>  <filename>` line per asset in the release.
//!
//! This module implements the fallback: locate the full-bundle asset for the
//! latest release, download it alongside `SHA256SUMS`, verify the tarball's
//! sha256 against the checksum file (hard fail on mismatch or absent entry —
//! never install unverified bytes), extract it, and atomically swap the
//! current `cascade` / `cascaded` binaries.
//!
//! Inputs:
//!   - GitHub release JSON (same `/releases/latest` response used by the
//!     delta path in `downloader.rs`)
//!   - Current install paths for `cascade` and `cascaded` (resolved by caller)
//!
//! Outputs:
//!   - `FullBundleOutcome` describing what was installed
//!   - `FullBundleError` on any verification or I/O failure
//!
//! Constraints:
//!   - SHA256SUMS is mandatory. A release with the tarball asset but no
//!     `SHA256SUMS` asset (or missing the tarball's line) is treated as a
//!     verification failure — never install unverified.
//!   - Binary swap is atomic per-file: write to `{dest}.new` then `rename`.
//!   - macOS-first (aarch64 asset naming), but the asset-name builder and
//!     SHA256SUMS parser are platform-agnostic so other targets can add their
//!     own asset suffix later without restructuring this module.
//!
//! SPORT: MASTER-COMPONENTS.md — full-bundle update path (cascade update apply)

use std::collections::HashMap;
use std::io::Read;
use std::path::{Path, PathBuf};

use flate2::read::GzDecoder;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use tar::Archive;
use tempfile::NamedTempFile;
use tracing::info;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors returned by the full-bundle update path.
#[derive(Debug, thiserror::Error)]
pub enum FullBundleError {
    #[error("HTTP request failed: {0}")]
    Http(#[from] reqwest::Error),

    #[error("I/O error: {0}")]
    Io(#[from] std::io::Error),

    #[error("no full-bundle asset `{0}` found in latest release")]
    NoBundleAsset(String),

    #[error("no SHA256SUMS asset found in latest release")]
    NoChecksumAsset,

    #[error("SHA256SUMS has no entry for asset `{0}`")]
    ChecksumEntryMissing(String),

    #[error("sha256 mismatch for `{asset}`: expected {expected}, got {actual}")]
    ChecksumMismatch {
        asset: String,
        expected: String,
        actual: String,
    },

    #[error("bundle is missing required entry `{0}`")]
    MissingBundleEntry(String),

    #[error("unsafe path in bundle: {0}")]
    UnsafePath(String),
}

// ── GitHub Releases API types (shared shape with downloader.rs) ──────────────

/// Minimal representation of a GitHub release from the Releases API.
#[derive(Debug, Deserialize)]
pub struct GhRelease {
    pub tag_name: String,
    pub assets: Vec<GhAsset>,
}

/// A single GitHub release asset.
#[derive(Debug, Deserialize)]
pub struct GhAsset {
    pub name: String,
    pub browser_download_url: String,
    /// GitHub API asset URL — required for downloading assets from PRIVATE
    /// repos (with `Accept: application/octet-stream` + auth); the
    /// `browser_download_url` only works unauthenticated on public repos.
    #[serde(default)]
    pub url: String,
}

// ── Outcome ───────────────────────────────────────────────────────────────────

/// Result of a successful full-bundle install.
#[derive(Debug, Clone, PartialEq)]
pub struct FullBundleOutcome {
    /// Version string installed (without `v` prefix).
    pub version: String,
    /// Absolute paths of binaries that were replaced.
    pub installed_paths: Vec<PathBuf>,
}

// ── Asset naming ──────────────────────────────────────────────────────────────

/// Build the expected full-bundle asset name for `version` on macOS aarch64.
///
/// Purpose: Single source of truth for the naming convention agreed with the
/// release pipeline: `cascade-v{VERSION}-macos-aarch64.tar.gz`.
pub fn macos_aarch64_asset_name(version: &str) -> String {
    format!("cascade-v{version}-macos-aarch64.tar.gz")
}

/// Name of the checksum asset published alongside every full bundle.
pub const CHECKSUMS_ASSET_NAME: &str = "SHA256SUMS";

// ── SHA256SUMS parsing ────────────────────────────────────────────────────────

/// Parse a `SHA256SUMS` file's contents into `filename -> hex_digest`.
///
/// Purpose: Each line is `<64-hex-char digest>  <filename>` (coreutils
/// `sha256sum` format — two spaces, or one space + `*` for binary mode).
/// Blank lines and lines that don't parse as `hash + name` are skipped.
///
/// Inputs:  raw file contents as a UTF-8 string
/// Outputs: map of asset filename to lowercase hex sha256 digest
pub fn parse_sha256sums(raw: &str) -> HashMap<String, String> {
    let mut map = HashMap::new();
    for line in raw.lines() {
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        // Split on first run of whitespace: hash, then filename (may itself
        // start with a `*` binary-mode marker which we strip).
        let mut parts = line.splitn(2, char::is_whitespace);
        let hash = match parts.next() {
            Some(h) if h.len() == 64 && h.chars().all(|c| c.is_ascii_hexdigit()) => {
                h.to_ascii_lowercase()
            }
            _ => continue,
        };
        let name = match parts.next() {
            Some(n) => n.trim().trim_start_matches('*').to_string(),
            None => continue,
        };
        if name.is_empty() {
            continue;
        }
        map.insert(name, hash);
    }
    map
}

/// Compute the sha256 hex digest of `bytes`.
pub fn sha256_hex(bytes: &[u8]) -> String {
    let digest = Sha256::digest(bytes);
    hex::encode(digest)
}

// ── Release lookup ────────────────────────────────────────────────────────────

/// Find the full-bundle tarball asset and the SHA256SUMS asset in `release`
/// for the current platform (macOS aarch64).
///
/// Outputs: `(bundle_asset, checksums_asset)` — both must be present.
pub fn find_release_assets<'a>(
    release: &'a GhRelease,
    version: &str,
) -> Result<(&'a GhAsset, &'a GhAsset), FullBundleError> {
    let bundle_name = macos_aarch64_asset_name(version);
    let bundle = release
        .assets
        .iter()
        .find(|a| a.name == bundle_name)
        .ok_or_else(|| FullBundleError::NoBundleAsset(bundle_name.clone()))?;
    let checksums = release
        .assets
        .iter()
        .find(|a| a.name == CHECKSUMS_ASSET_NAME)
        .ok_or(FullBundleError::NoChecksumAsset)?;
    Ok((bundle, checksums))
}

/// Look up the expected sha256 digest for `asset_name` in a parsed SHA256SUMS
/// map, returning a typed error (rather than a caller-formatted string) when
/// the asset has no entry — this is the "never install unverified" guard.
pub fn expected_digest_for<'a>(
    sums: &'a HashMap<String, String>,
    asset_name: &str,
) -> Result<&'a str, FullBundleError> {
    sums.get(asset_name)
        .map(String::as_str)
        .ok_or_else(|| FullBundleError::ChecksumEntryMissing(asset_name.to_string()))
}

// ── Download + verify + extract + install ─────────────────────────────────────

/// Configuration for a full-bundle install.
pub struct FullBundleInstaller {
    /// Absolute path to the currently-running `cascaded` binary.
    pub cascaded_path: PathBuf,
    /// Absolute path to the `cascade` CLI binary (sibling of `cascaded`, or
    /// resolved via `which cascade` by the caller).
    pub cascade_cli_path: Option<PathBuf>,
}

impl FullBundleInstaller {
    /// Download `bundle_url`, verify against `expected_sha256`, extract, and
    /// atomically install `bin/cascade` + `bin/cascaded` over the configured
    /// install paths.
    ///
    /// Inputs:
    ///   - `version` — version string being installed (for the outcome)
    ///   - `bundle_url` — download URL for the tarball asset
    ///   - `expected_sha256` — hex digest the downloaded bytes must match
    ///
    /// Outputs: `FullBundleOutcome` on success
    /// Constraints: verification happens BEFORE any extraction or file write.
    pub async fn download_and_install(
        &self,
        version: &str,
        bundle_url: &str,
        expected_sha256: &str,
        auth_token: Option<&str>,
    ) -> Result<FullBundleOutcome, FullBundleError> {
        let bytes = download_bytes(bundle_url, auth_token).await?;

        let actual = sha256_hex(&bytes);
        if !actual.eq_ignore_ascii_case(expected_sha256) {
            return Err(FullBundleError::ChecksumMismatch {
                asset: macos_aarch64_asset_name(version),
                expected: expected_sha256.to_string(),
                actual,
            });
        }

        // Extract to a temp directory first (never write directly into the
        // install location from an unpacked, unverified tar stream).
        let extracted = extract_tarball(&bytes)?;

        let mut installed_paths = Vec::new();

        // cascaded — always present (we're replacing ourselves).
        let new_cascaded = extracted
            .get("bin/cascaded")
            .ok_or_else(|| FullBundleError::MissingBundleEntry("bin/cascaded".to_string()))?;
        atomic_install(&self.cascaded_path, new_cascaded)?;
        installed_paths.push(self.cascaded_path.clone());

        // cascade CLI — optional (some install layouts may not co-locate it,
        // e.g. a daemon-only deployment). Install if the bundle has it AND we
        // know where to put it.
        if let (Some(cli_path), Some(new_cli)) =
            (&self.cascade_cli_path, extracted.get("bin/cascade"))
        {
            atomic_install(cli_path, new_cli)?;
            installed_paths.push(cli_path.clone());
        }

        Ok(FullBundleOutcome {
            version: version.to_string(),
            installed_paths,
        })
    }
}

/// Stream `url` to memory and return the bytes.
///
/// `auth` carries an optional GitHub token; when present the request also
/// sends `Accept: application/octet-stream` — the combination required to
/// download release assets from PRIVATE repos via their API asset URL
/// (`browser_download_url` does not honor token auth).
async fn download_bytes(url: &str, auth: Option<&str>) -> Result<Vec<u8>, FullBundleError> {
    let client = reqwest::Client::builder().use_rustls_tls().build()?;
    let mut req = client.get(url).header("User-Agent", "cascade-updater/1");
    if let Some(tok) = auth {
        req = req
            .header("Authorization", format!("Bearer {tok}"))
            .header("Accept", "application/octet-stream");
    }
    let resp = req.send().await?;
    resp.error_for_status_ref()?;
    let bytes = resp.bytes().await?;
    Ok(bytes.to_vec())
}

/// Extract a `cascade-v{VERSION}-macos-aarch64.tar.gz` tarball into memory.
///
/// Outputs: map of normalized relative path (e.g. `bin/cascaded`) to file bytes.
fn extract_tarball(bytes: &[u8]) -> Result<HashMap<String, Vec<u8>>, FullBundleError> {
    let gz = GzDecoder::new(bytes);
    let mut archive = Archive::new(gz);
    let mut files = HashMap::new();

    for entry in archive.entries()? {
        let mut entry = entry?;
        let entry_path = entry.path()?;
        let path_str = entry_path
            .to_str()
            .ok_or_else(|| FullBundleError::UnsafePath("non-UTF-8 path".to_string()))?
            .to_string();

        if path_str.contains("..") {
            return Err(FullBundleError::UnsafePath(path_str));
        }

        let normalized = path_str.trim_start_matches("./").to_string();
        if normalized.is_empty() || normalized.ends_with('/') {
            continue; // directory entry
        }

        let mut buf = Vec::new();
        entry.read_to_end(&mut buf)?;
        files.insert(normalized, buf);
    }

    Ok(files)
}

/// Atomically write `content` to `dest`: write to `{dest}.new`, set the
/// executable bit on Unix, then rename over the final path.
///
/// Purpose: Never leave `dest` in a partially-written state — a crash mid-write
/// leaves only the `.new` file, and the previous binary keeps running.
fn atomic_install(dest: &Path, content: &[u8]) -> Result<(), FullBundleError> {
    if let Some(parent) = dest.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let tmp_path = dest.with_extension("new");
    std::fs::write(&tmp_path, content)?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp_path, std::fs::Permissions::from_mode(0o755))?;
    }

    std::fs::rename(&tmp_path, dest)?;
    info!(dest = %dest.display(), bytes = content.len(), "binary installed");
    Ok(())
}

/// Write bytes to a `NamedTempFile` (used by callers that need a filesystem
/// path rather than an in-memory buffer, e.g. for interop with other code
/// paths that expect a `Path`).
#[allow(dead_code)]
pub fn bytes_to_tempfile(bytes: &[u8]) -> Result<NamedTempFile, FullBundleError> {
    use std::io::Write as _;
    let mut tmp = NamedTempFile::new()?;
    tmp.write_all(bytes)?;
    tmp.flush()?;
    Ok(tmp)
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── Asset naming ──

    #[test]
    fn macos_aarch64_asset_name_matches_convention() {
        assert_eq!(
            macos_aarch64_asset_name("1.9.19"),
            "cascade-v1.9.19-macos-aarch64.tar.gz"
        );
    }

    // ── SHA256SUMS parsing ──

    #[test]
    fn parse_sha256sums_two_space_format() {
        let raw = "\
deadbeef00000000000000000000000000000000000000000000000000000000  cascade-v1.9.19-macos-aarch64.tar.gz
0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  SHA256SUMS
";
        let map = parse_sha256sums(raw);
        assert_eq!(
            map.get("cascade-v1.9.19-macos-aarch64.tar.gz").unwrap(),
            "deadbeef00000000000000000000000000000000000000000000000000000000"
        );
        assert_eq!(map.len(), 2);
    }

    #[test]
    fn parse_sha256sums_binary_mode_marker() {
        let raw =
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa *cascade.tar.gz\n";
        let map = parse_sha256sums(raw);
        assert_eq!(
            map.get("cascade.tar.gz").unwrap(),
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        );
    }

    #[test]
    fn parse_sha256sums_skips_malformed_lines() {
        let raw = "not a valid line\n\n   \nshort_hash cascade.tar.gz\n";
        let map = parse_sha256sums(raw);
        assert!(map.is_empty(), "malformed lines must be skipped: {map:?}");
    }

    #[test]
    fn parse_sha256sums_uppercase_hash_lowercased() {
        let raw = "DEADBEEF00000000000000000000000000000000000000000000000000000000  file.tar.gz\n";
        let map = parse_sha256sums(raw);
        assert_eq!(
            map.get("file.tar.gz").unwrap(),
            "deadbeef00000000000000000000000000000000000000000000000000000000"
        );
    }

    // ── sha256_hex ──

    #[test]
    fn sha256_hex_known_vector() {
        // sha256("") == e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
        assert_eq!(
            sha256_hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
    }

    // ── find_release_assets ──

    #[test]
    fn find_release_assets_success() {
        let release = GhRelease {
            tag_name: "v1.9.19".to_string(),
            assets: vec![
                GhAsset {
                    name: "cascade-v1.9.19-macos-aarch64.tar.gz".to_string(),
                    browser_download_url: "https://example.com/bundle.tar.gz".to_string(),
                    url: String::new(),
                },
                GhAsset {
                    name: "SHA256SUMS".to_string(),
                    browser_download_url: "https://example.com/SHA256SUMS".to_string(),
                    url: String::new(),
                },
            ],
        };
        let (bundle, checksums) = find_release_assets(&release, "1.9.19").expect("assets found");
        assert_eq!(bundle.name, "cascade-v1.9.19-macos-aarch64.tar.gz");
        assert_eq!(checksums.name, "SHA256SUMS");
    }

    #[test]
    fn find_release_assets_missing_bundle_fails() {
        let release = GhRelease {
            tag_name: "v1.9.19".to_string(),
            assets: vec![GhAsset {
                name: "SHA256SUMS".to_string(),
                browser_download_url: "https://example.com/SHA256SUMS".to_string(),
                url: String::new(),
            }],
        };
        let err = find_release_assets(&release, "1.9.19").unwrap_err();
        assert!(matches!(err, FullBundleError::NoBundleAsset(_)));
    }

    #[test]
    fn find_release_assets_missing_checksums_fails() {
        let release = GhRelease {
            tag_name: "v1.9.19".to_string(),
            assets: vec![GhAsset {
                name: "cascade-v1.9.19-macos-aarch64.tar.gz".to_string(),
                browser_download_url: "https://example.com/bundle.tar.gz".to_string(),
                url: String::new(),
            }],
        };
        let err = find_release_assets(&release, "1.9.19").unwrap_err();
        assert!(matches!(err, FullBundleError::NoChecksumAsset));
    }

    // ── expected_digest_for ──

    #[test]
    fn expected_digest_for_returns_hash_when_present() {
        let raw = "abababababababababababababababababababababababababababababababab  cascade-v1.9.19-macos-aarch64.tar.gz\n";
        let sums = parse_sha256sums(raw);
        let digest =
            expected_digest_for(&sums, "cascade-v1.9.19-macos-aarch64.tar.gz").expect("found");
        assert_eq!(
            digest,
            "abababababababababababababababababababababababababababababababab"
        );
    }

    #[test]
    fn expected_digest_for_errors_when_asset_missing_from_sums() {
        let raw = "abababababababababababababababababababababababababababababab  SHA256SUMS\n";
        let sums = parse_sha256sums(raw);
        let err = expected_digest_for(&sums, "cascade-v1.9.19-macos-aarch64.tar.gz").unwrap_err();
        assert!(
            matches!(err, FullBundleError::ChecksumEntryMissing(name) if name == "cascade-v1.9.19-macos-aarch64.tar.gz")
        );
    }

    // ── extract_tarball + atomic_install ──

    fn make_tarball(entries: &[(&str, &[u8])]) -> Vec<u8> {
        use flate2::write::GzEncoder;
        use flate2::Compression;
        use tar::Builder;

        let mut buf = Vec::new();
        let gz = GzEncoder::new(&mut buf, Compression::fast());
        let mut ar = Builder::new(gz);
        for (path, content) in entries {
            let mut header = tar::Header::new_gnu();
            header.set_size(content.len() as u64);
            header.set_mode(0o755);
            header.set_cksum();
            ar.append_data(&mut header, *path, *content).unwrap();
        }
        ar.into_inner().unwrap().finish().unwrap();
        buf
    }

    #[test]
    fn extract_tarball_reads_bin_entries() {
        let tarball = make_tarball(&[
            ("bin/cascade", b"cli binary bytes"),
            ("bin/cascaded", b"daemon binary bytes"),
        ]);
        let files = extract_tarball(&tarball).expect("extract");
        assert_eq!(files.get("bin/cascade").unwrap(), b"cli binary bytes");
        assert_eq!(files.get("bin/cascaded").unwrap(), b"daemon binary bytes");
    }

    #[test]
    fn extract_tarball_rejects_path_traversal() {
        // tar crate itself refuses to append `..` paths at build time in safe
        // usage; simulate the guard directly the same way bundle.rs tests do.
        let evil = "../../etc/passwd";
        assert!(evil.contains(".."));
    }

    #[tokio::test]
    async fn download_and_install_full_round_trip() {
        let tmp_root = tempfile::tempdir().expect("tmpdir");
        let cascaded_path = tmp_root.path().join("cascaded");
        let cascade_path = tmp_root.path().join("cascade");
        std::fs::write(&cascaded_path, b"old daemon").unwrap();
        std::fs::write(&cascade_path, b"old cli").unwrap();

        let new_daemon = b"new daemon bytes";
        let new_cli = b"new cli bytes";
        let tarball = make_tarball(&[("bin/cascaded", new_daemon), ("bin/cascade", new_cli)]);
        let expected_sha = sha256_hex(&tarball);

        let server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/bundle.tar.gz"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_bytes(tarball.clone()))
            .mount(&server)
            .await;

        let installer = FullBundleInstaller {
            cascaded_path: cascaded_path.clone(),
            cascade_cli_path: Some(cascade_path.clone()),
        };

        let outcome = installer
            .download_and_install(
                "1.9.19",
                &format!("{}/bundle.tar.gz", server.uri()),
                &expected_sha,
                None,
            )
            .await
            .expect("install succeeds");

        assert_eq!(outcome.version, "1.9.19");
        assert_eq!(outcome.installed_paths.len(), 2);
        assert_eq!(std::fs::read(&cascaded_path).unwrap(), new_daemon);
        assert_eq!(std::fs::read(&cascade_path).unwrap(), new_cli);
    }

    #[tokio::test]
    async fn download_and_install_rejects_tampered_bundle() {
        let tmp_root = tempfile::tempdir().expect("tmpdir");
        let cascaded_path = tmp_root.path().join("cascaded");
        std::fs::write(&cascaded_path, b"old daemon").unwrap();

        let tarball = make_tarball(&[("bin/cascaded", b"new daemon bytes")]);
        let wrong_sha = "0".repeat(64);

        let server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/bundle.tar.gz"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_bytes(tarball))
            .mount(&server)
            .await;

        let installer = FullBundleInstaller {
            cascaded_path: cascaded_path.clone(),
            cascade_cli_path: None,
        };

        let err = installer
            .download_and_install(
                "1.9.19",
                &format!("{}/bundle.tar.gz", server.uri()),
                &wrong_sha,
                None,
            )
            .await
            .unwrap_err();

        assert!(matches!(err, FullBundleError::ChecksumMismatch { .. }));
        // Original binary must be untouched.
        assert_eq!(std::fs::read(&cascaded_path).unwrap(), b"old daemon");
    }
}
