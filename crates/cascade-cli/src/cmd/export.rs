//! `cascade export` / `cascade import --from-export` — portable archive for ~/.cascade/.
//!
//! Purpose: Produce a `.cascade-archive.tar.gz` of the user's ~/.cascade/ directory
//! that can be restored on any machine. The archive includes a `manifest.json`
//! that records the format version, creation time, content hash, file list, and
//! whether secrets were included.
//!
//! Inputs:
//!   - `ExportArgs.out_path` — destination path for the archive (default: cwd/.cascade-archive.tar.gz)
//!   - `ExportArgs.include_secrets` — if false, credential/key files are excluded
//!   - `ImportFromExportArgs.archive` — path to a previously exported archive
//!
//! Outputs:
//!   - A `.cascade-archive.tar.gz` file on disk (export)
//!   - Reconstructed ~/.cascade/ tree (import-from-export)
//!
//! Constraints:
//!   - Uses cascade_db::content_hash (BLAKE3) for archive integrity.
//!   - Never reads vault.env or *.key/*.pem unless --include-secrets is passed.
//!   - TODO(rag-16): gating on rag-16 sensitive-data API hook when that module lands.
//!
//! SPORT: cascade-cli / export (data-01)

use std::fs::{self, File};
use std::io::{Read as IoRead, Write as IoWrite};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use cascade_db::content_hash;
use cascade_types::error::{CascadeError, Result};
use clap::Args;
use flate2::write::GzEncoder;
use flate2::Compression;
use serde::{Deserialize, Serialize};

use super::Command;

// ── Constants ────────────────────────────────────────────────────────────────

/// Current archive format version. Bump on breaking layout changes.
const FORMAT_VERSION: u32 = 1;

/// Filename written into the root of every archive.
const MANIFEST_FILENAME: &str = "manifest.json";

/// Secret-adjacent file name patterns excluded unless --include-secrets is set.
/// TODO(rag-16): replace this static list with the rag-16 sensitive-data hook.
const SECRET_PATTERNS: &[&str] = &[
    "vault.env",
    ".env",
    "credentials",
];

/// Secret-adjacent file extensions excluded unless --include-secrets is set.
const SECRET_EXTENSIONS: &[&str] = &["key", "pem", "p12", "pfx", "crt", "cer"];

// ── Args ─────────────────────────────────────────────────────────────────────

/// Arguments for `cascade export`.
#[derive(Debug, Args)]
pub struct ExportArgs {
    /// Destination path for the archive.
    ///
    /// Defaults to `.cascade-archive.tar.gz` in the current directory.
    #[arg(long, short = 'o', value_name = "PATH")]
    pub out: Option<PathBuf>,

    /// Include credential / key files in the archive.
    ///
    /// Without this flag, `vault.env`, `*.key`, `*.pem`, and similar files are skipped.
    #[arg(long)]
    pub include_secrets: bool,

    /// Source ~/.cascade/ directory to export (default: auto-detected).
    ///
    /// Usually you leave this unset; it resolves to `~/.cascade/`.
    #[arg(long, value_name = "PATH")]
    pub source: Option<PathBuf>,
}

/// Arguments for `cascade import --from-export <archive>`.
///
/// Extends the existing `cascade import` command with a dedicated restore path
/// for archives produced by `cascade export`.
#[derive(Debug, Args)]
pub struct ImportFromExportArgs {
    /// Path to the `.cascade-archive.tar.gz` produced by `cascade export`.
    #[arg(long, value_name = "ARCHIVE")]
    pub from_export: PathBuf,

    /// Destination for the restored tree (default: ~/.cascade/).
    #[arg(long, value_name = "PATH")]
    pub dest: Option<PathBuf>,

    /// Overwrite existing files without prompting.
    #[arg(long)]
    pub force: bool,
}

// ── Manifest ─────────────────────────────────────────────────────────────────

/// Metadata written into the archive root as `manifest.json`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ArchiveManifest {
    /// Monotonically-increasing layout version.
    pub format_version: u32,

    /// Unix timestamp (seconds since UNIX_EPOCH) when the archive was created.
    pub created_at: u64,

    /// BLAKE3 hex digest of the raw archive bytes (computed after writing,
    /// then the manifest is re-packed as a sidecar `.cascade-archive.tar.gz.sha`
    /// file next to the archive — NOT embedded inside the tarball itself).
    ///
    /// On import this field is read from the `.sha` sidecar and used to verify
    /// the tarball. When no sidecar is present, validation is skipped with a warning.
    pub content_hash: String,

    /// Relative paths of every file included in the archive (excluding manifest.json).
    pub files: Vec<String>,

    /// Whether credential/key files were included.
    pub secrets_included: bool,
}

impl ArchiveManifest {
    fn new(files: Vec<String>, secrets_included: bool) -> Self {
        let created_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();

        ArchiveManifest {
            format_version: FORMAT_VERSION,
            created_at,
            // Placeholder — filled by the caller after computing the hash.
            content_hash: String::new(),
            files,
            secrets_included,
        }
    }
}

// ── Command impl: export ──────────────────────────────────────────────────────

#[async_trait]
impl Command for ExportArgs {
    async fn run(&self) -> Result<()> {
        let home = home_dir()?;
        let source = self.source.clone().unwrap_or_else(|| home.join(".cascade"));

        if !source.exists() {
            return Err(CascadeError::Other(format!(
                "source directory not found: {}",
                source.display()
            )));
        }

        let out_path = self.out.clone().unwrap_or_else(|| {
            std::env::current_dir()
                .unwrap_or_else(|_| PathBuf::from("."))
                .join(".cascade-archive.tar.gz")
        });

        println!("Exporting {} → {}", source.display(), out_path.display());

        let archive_bytes = build_archive(&source, self.include_secrets)?;
        let hash = content_hash(&archive_bytes);

        // Write the archive.
        let mut f = File::create(&out_path).map_err(|e| CascadeError::Io {
            path: out_path.clone(),
            operation: "create archive",
            source: e,
        })?;
        f.write_all(&archive_bytes).map_err(|e| CascadeError::Io {
            path: out_path.clone(),
            operation: "write archive",
            source: e,
        })?;

        // Write sidecar hash file.
        let sidecar_path = sidecar_path(&out_path);
        fs::write(&sidecar_path, &hash).map_err(|e| CascadeError::Io {
            path: sidecar_path.clone(),
            operation: "write hash sidecar",
            source: e,
        })?;

        println!(
            "Archive written: {} ({} bytes)",
            out_path.display(),
            archive_bytes.len()
        );
        println!("BLAKE3: {}", hash);
        println!("Sidecar: {}", sidecar_path.display());

        if !self.include_secrets {
            println!("Note: credential/key files were excluded. Use --include-secrets to include them.");
        }

        Ok(())
    }
}

// ── Command impl: import-from-export (called from import.rs) ─────────────────

/// Entry point for `cascade import --from-export <archive>`.
pub async fn run_import_from_export(args: &ImportFromExportArgs) -> Result<()> {
    let home = home_dir()?;
    let dest = args
        .dest
        .clone()
        .unwrap_or_else(|| home.join(".cascade"));

    println!(
        "Restoring {} → {}",
        args.from_export.display(),
        dest.display()
    );

    // Read archive bytes.
    let archive_bytes = fs::read(&args.from_export).map_err(|e| CascadeError::Io {
        path: args.from_export.clone(),
        operation: "read archive",
        source: e,
    })?;

    // Verify hash via sidecar if present.
    let sidecar = sidecar_path(&args.from_export);
    if sidecar.exists() {
        let expected = fs::read_to_string(&sidecar)
            .map_err(|e| CascadeError::Io {
                path: sidecar.clone(),
                operation: "read hash sidecar",
                source: e,
            })?
            .trim()
            .to_string();
        let actual = content_hash(&archive_bytes);
        if actual != expected {
            return Err(CascadeError::Other(format!(
                "archive integrity check failed\n  expected: {}\n  got:      {}\n\
                 The archive may be corrupt or tampered with.",
                expected, actual
            )));
        }
        println!("Integrity verified (BLAKE3 match).");
    } else {
        println!("Warning: no .sha sidecar found — skipping integrity check.");
    }

    // Extract manifest from the tarball.
    let manifest = extract_manifest(&archive_bytes)?;

    // Validate format version.
    if manifest.format_version != FORMAT_VERSION {
        return Err(CascadeError::Other(format!(
            "unsupported archive format version {} (this build supports {})",
            manifest.format_version, FORMAT_VERSION
        )));
    }

    println!(
        "Manifest: format_version={}, {} file(s), secrets_included={}",
        manifest.format_version,
        manifest.files.len(),
        manifest.secrets_included
    );

    // Extract all files.
    let extracted = extract_archive(&archive_bytes, &dest, args.force)?;

    println!(
        "Restored {} file(s) to {}",
        extracted,
        dest.display()
    );

    Ok(())
}

// ── Archive builder ───────────────────────────────────────────────────────────

/// Walk `source` and build an in-memory `.tar.gz`.
///
/// Returns the raw compressed bytes. The manifest is injected as the first
/// entry so readers can parse it without extracting the whole archive.
fn build_archive(source: &Path, include_secrets: bool) -> Result<Vec<u8>> {
    // Collect files first (to build manifest).
    let files = collect_files(source, include_secrets)?;
    let rel_paths: Vec<String> = files
        .iter()
        .map(|(rel, _)| rel.clone())
        .collect();

    let mut manifest = ArchiveManifest::new(rel_paths, include_secrets);

    // Encode everything into a tar.gz in memory.
    let mut gz_buf: Vec<u8> = Vec::new();
    {
        let encoder = GzEncoder::new(&mut gz_buf, Compression::best());
        let mut archive = tar::Builder::new(encoder);

        // Write manifest first (with empty content_hash — filled by sidecar).
        let manifest_json = serde_json::to_vec_pretty(&manifest)
            .map_err(|e| CascadeError::Other(format!("manifest serialization: {e}")))?;
        append_bytes_to_tar(&mut archive, MANIFEST_FILENAME, &manifest_json)?;

        // Write each file.
        for (rel_path, abs_path) in &files {
            let mut f = File::open(abs_path).map_err(|e| CascadeError::Io {
                path: abs_path.clone(),
                operation: "open file for export",
                source: e,
            })?;
            let mut buf = Vec::new();
            f.read_to_end(&mut buf).map_err(|e| CascadeError::Io {
                path: abs_path.clone(),
                operation: "read file for export",
                source: e,
            })?;
            append_bytes_to_tar(&mut archive, rel_path, &buf)?;
        }

        archive
            .finish()
            .map_err(|e| CascadeError::Other(format!("tar finish: {e}")))?;
    }

    // Update manifest content_hash (BLAKE3 of final bytes) — stored in sidecar.
    manifest.content_hash = content_hash(&gz_buf);
    // We don't re-embed the hash inside the tarball (chicken-and-egg); the
    // sidecar carries it. The manifest field is a convenience for readers who
    // independently verify.

    Ok(gz_buf)
}

/// Collect all files under `source`, filtering secrets unless permitted.
fn collect_files(source: &Path, include_secrets: bool) -> Result<Vec<(String, PathBuf)>> {
    let mut result = Vec::new();
    collect_recursive(source, source, include_secrets, &mut result)?;
    result.sort_by(|a, b| a.0.cmp(&b.0));
    Ok(result)
}

fn collect_recursive(
    root: &Path,
    current: &Path,
    include_secrets: bool,
    out: &mut Vec<(String, PathBuf)>,
) -> Result<()> {
    let entries = fs::read_dir(current).map_err(|e| CascadeError::Io {
        path: current.to_path_buf(),
        operation: "read directory",
        source: e,
    })?;

    for entry in entries.flatten() {
        let path = entry.path();
        let file_name = entry.file_name();
        let name = file_name.to_string_lossy();

        if path.is_dir() {
            collect_recursive(root, &path, include_secrets, out)?;
        } else if path.is_file() {
            if !include_secrets && is_sensitive(name.as_ref(), &path) {
                continue;
            }
            // Relative path from the source root.
            let rel = path
                .strip_prefix(root)
                .unwrap_or(&path)
                .to_string_lossy()
                .replace('\\', "/");
            out.push((rel, path));
        }
    }
    Ok(())
}

/// Returns true if the file looks like a credential/secret.
/// TODO(rag-16): replace with rag-16 sensitive-data hook once that module lands.
fn is_sensitive(name: &str, path: &Path) -> bool {
    let lower = name.to_lowercase();
    for pattern in SECRET_PATTERNS {
        if lower == *pattern || lower.starts_with(pattern) {
            return true;
        }
    }
    if let Some(ext) = path.extension() {
        let ext_lower = ext.to_string_lossy().to_lowercase();
        for secret_ext in SECRET_EXTENSIONS {
            if ext_lower == *secret_ext {
                return true;
            }
        }
    }
    false
}

/// Append raw bytes as a tar entry with the given path name.
fn append_bytes_to_tar<W: std::io::Write>(
    archive: &mut tar::Builder<W>,
    path: &str,
    data: &[u8],
) -> Result<()> {
    let mut header = tar::Header::new_gnu();
    header.set_size(data.len() as u64);
    header.set_mode(0o644);
    header.set_cksum();
    archive
        .append_data(&mut header, path, data)
        .map_err(|e| CascadeError::Other(format!("tar append {path}: {e}")))?;
    Ok(())
}

// ── Archive reader ────────────────────────────────────────────────────────────

/// Extract `manifest.json` from an in-memory tar.gz.
fn extract_manifest(archive_bytes: &[u8]) -> Result<ArchiveManifest> {
    use flate2::read::GzDecoder;
    use std::io::Cursor;

    let cursor = Cursor::new(archive_bytes);
    let gz = GzDecoder::new(cursor);
    let mut tar_archive = tar::Archive::new(gz);

    for entry in tar_archive
        .entries()
        .map_err(|e| CascadeError::Other(format!("tar entries: {e}")))?
    {
        let mut entry = entry.map_err(|e| CascadeError::Other(format!("tar entry: {e}")))?;
        let entry_path = entry
            .path()
            .map_err(|e| CascadeError::Other(format!("entry path: {e}")))?
            .to_path_buf();

        if entry_path == Path::new(MANIFEST_FILENAME) {
            let mut buf = String::new();
            entry
                .read_to_string(&mut buf)
                .map_err(|e| CascadeError::Other(format!("read manifest: {e}")))?;
            let manifest: ArchiveManifest = serde_json::from_str(&buf)
                .map_err(|e| CascadeError::Other(format!("parse manifest: {e}")))?;
            return Ok(manifest);
        }
    }

    Err(CascadeError::Other(
        "archive does not contain manifest.json — may not be a cascade archive".into(),
    ))
}

/// Extract all non-manifest files from an in-memory tar.gz into `dest`.
fn extract_archive(archive_bytes: &[u8], dest: &Path, force: bool) -> Result<usize> {
    use flate2::read::GzDecoder;
    use std::io::Cursor;

    let cursor = Cursor::new(archive_bytes);
    let gz = GzDecoder::new(cursor);
    let mut tar_archive = tar::Archive::new(gz);

    fs::create_dir_all(dest).map_err(|e| CascadeError::Io {
        path: dest.to_path_buf(),
        operation: "create dest dir",
        source: e,
    })?;

    let mut count = 0usize;

    for entry in tar_archive
        .entries()
        .map_err(|e| CascadeError::Other(format!("tar entries: {e}")))?
    {
        let mut entry = entry.map_err(|e| CascadeError::Other(format!("tar entry: {e}")))?;
        let entry_path = entry
            .path()
            .map_err(|e| CascadeError::Other(format!("entry path: {e}")))?
            .to_path_buf();

        // Skip manifest — it's metadata, not user data.
        if entry_path == Path::new(MANIFEST_FILENAME) {
            continue;
        }

        let out_path = dest.join(&entry_path);

        if out_path.exists() && !force {
            println!("  skip (exists): {}", entry_path.display());
            continue;
        }

        // Create parent dirs.
        if let Some(parent) = out_path.parent() {
            fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
                path: parent.to_path_buf(),
                operation: "create parent dir",
                source: e,
            })?;
        }

        let mut buf = Vec::new();
        entry
            .read_to_end(&mut buf)
            .map_err(|e| CascadeError::Other(format!("read entry {}: {e}", entry_path.display())))?;

        fs::write(&out_path, &buf).map_err(|e| CascadeError::Io {
            path: out_path.clone(),
            operation: "write file",
            source: e,
        })?;

        println!("  restored: {}", entry_path.display());
        count += 1;
    }

    Ok(count)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn home_dir() -> Result<PathBuf> {
    dirs::home_dir().ok_or_else(|| CascadeError::Other("cannot determine home directory".into()))
}

/// Derives the sidecar path: `<archive>.sha`
fn sidecar_path(archive: &Path) -> PathBuf {
    let mut p = archive.to_path_buf();
    let mut name = p
        .file_name()
        .unwrap_or_default()
        .to_string_lossy()
        .to_string();
    name.push_str(".sha");
    p.set_file_name(name);
    p
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    // Helper: create a minimal ~/.cascade/ tree in a temp dir.
    fn make_fake_cascade(dir: &Path) {
        fs::create_dir_all(dir.join("memory")).unwrap();
        fs::write(dir.join("memory").join("decisions.md"), "# Decisions\n## D-01\nUse BLAKE3.").unwrap();
        fs::write(dir.join("memory").join("lessons.md"), "# Lessons\nLesson 1.").unwrap();
        fs::create_dir_all(dir.join("inbox")).unwrap();
        fs::write(dir.join("inbox").join("msg-001.md"), "hello").unwrap();
        // A secret file that should be excluded by default.
        fs::write(dir.join("vault.env"), "GEMINI_FREE_KEY_01=secret").unwrap();
    }

    #[test]
    fn export_excludes_secrets_by_default() {
        let td = TempDir::new().unwrap();
        let source = td.path().join(".cascade");
        make_fake_cascade(&source);

        let archive_bytes = build_archive(&source, false).unwrap();
        // Sanity: archive is non-empty.
        assert!(!archive_bytes.is_empty());

        // vault.env must not appear in the manifest.
        let manifest = extract_manifest(&archive_bytes).unwrap();
        assert!(!manifest.secrets_included);
        let has_vault = manifest.files.iter().any(|f| f.contains("vault.env"));
        assert!(!has_vault, "vault.env should be excluded without --include-secrets");
    }

    #[test]
    fn export_includes_secrets_when_flag_set() {
        let td = TempDir::new().unwrap();
        let source = td.path().join(".cascade");
        make_fake_cascade(&source);

        let archive_bytes = build_archive(&source, true).unwrap();
        let manifest = extract_manifest(&archive_bytes).unwrap();
        assert!(manifest.secrets_included);
        let has_vault = manifest.files.iter().any(|f| f.contains("vault.env"));
        assert!(has_vault, "vault.env should be included with --include-secrets");
    }

    #[test]
    fn round_trip_export_import() {
        let td = TempDir::new().unwrap();
        let source = td.path().join(".cascade");
        make_fake_cascade(&source);

        // Export (no secrets).
        let archive_bytes = build_archive(&source, false).unwrap();
        let hash = content_hash(&archive_bytes);

        // Verify manifest.
        let manifest = extract_manifest(&archive_bytes).unwrap();
        assert_eq!(manifest.format_version, FORMAT_VERSION);
        assert!(!manifest.files.is_empty());

        // Write archive + sidecar to disk.
        let archive_path = td.path().join("test.tar.gz");
        fs::write(&archive_path, &archive_bytes).unwrap();
        let sidecar = sidecar_path(&archive_path);
        fs::write(&sidecar, &hash).unwrap();

        // Import into a fresh directory.
        let dest = td.path().join("restored");
        let args = ImportFromExportArgs {
            from_export: archive_path.clone(),
            dest: Some(dest.clone()),
            force: false,
        };

        let rt = tokio::runtime::Runtime::new().unwrap();
        rt.block_on(run_import_from_export(&args)).unwrap();

        // Check a restored file.
        let restored_decisions = dest.join("memory").join("decisions.md");
        assert!(restored_decisions.exists(), "decisions.md should be restored");
        let content = fs::read_to_string(&restored_decisions).unwrap();
        assert!(content.contains("BLAKE3"), "content should round-trip intact");

        // vault.env must NOT be restored (was not in archive).
        assert!(
            !dest.join("vault.env").exists(),
            "vault.env should not appear in restored tree"
        );
    }

    #[test]
    fn corrupt_archive_is_refused() {
        let td = TempDir::new().unwrap();
        let archive_path = td.path().join("corrupt.tar.gz");
        let garbage = b"this is not a valid gzip stream at all";
        fs::write(&archive_path, garbage).unwrap();

        // Write a wrong hash to the sidecar so integrity fails.
        let sidecar = sidecar_path(&archive_path);
        fs::write(&sidecar, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").unwrap();

        let args = ImportFromExportArgs {
            from_export: archive_path,
            dest: Some(td.path().join("dest")),
            force: false,
        };
        let rt = tokio::runtime::Runtime::new().unwrap();
        let err = rt.block_on(run_import_from_export(&args));
        assert!(err.is_err(), "corrupt archive must be refused");
        let msg = format!("{}", err.unwrap_err());
        assert!(
            msg.contains("integrity check failed"),
            "error should mention integrity check: {msg}"
        );
    }
}
