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
//!   - Never includes vault.env, *.key/*.pem, or files whose CONTENT looks like
//!     credentials (AWS keys, PEM private keys, JWTs, high-entropy secret
//!     assignments) unless --include-secrets is passed.
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
/// Fast path only — the content-aware scan below catches everything else.
const SECRET_PATTERNS: &[&str] = &["vault.env", ".env", "credentials"];

/// Secret-adjacent file extensions excluded unless --include-secrets is set.
const SECRET_EXTENSIONS: &[&str] = &["key", "pem", "p12", "pfx", "crt", "cer"];

/// Maximum file size scanned by the content-aware secret heuristic (bytes).
/// Larger files are presumed data (DBs, model caches), not credentials.
const SECRET_SCAN_MAX_BYTES: u64 = 64 * 1024;

/// Minimum Shannon entropy (bits/char) for an assignment value to count as a
/// secret. Random base64/hex runs ≈4.0+; natural-language text ≈<3.5.
const SECRET_ENTROPY_THRESHOLD: f64 = 4.0;

/// Minimum length of a candidate secret value. Short values ("secret",
/// "hunter2") are not high-entropy material regardless of entropy.
const SECRET_MIN_VALUE_LEN: usize = 20;

/// Variable-name fragments marking an assignment as secret-adjacent.
const SECRET_NAME_MARKERS: &[&str] = &[
    "secret",
    "token",
    "password",
    "passwd",
    "api_key",
    "apikey",
    "private_key",
    "access_key",
    "credential",
    "auth",
];

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
            println!(
                "Note: credential/key files (matched by name or content) were excluded. \
                 Use --include-secrets to include them."
            );
        }

        Ok(())
    }
}

// ── Command impl: import-from-export (called from import.rs) ─────────────────

/// Entry point for `cascade import --from-export <archive>`.
pub async fn run_import_from_export(args: &ImportFromExportArgs) -> Result<()> {
    let home = home_dir()?;
    let dest = args.dest.clone().unwrap_or_else(|| home.join(".cascade"));

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

    println!("Restored {} file(s) to {}", extracted, dest.display());

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
    let rel_paths: Vec<String> = files.iter().map(|(rel, _)| rel.clone()).collect();

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
///
/// Two layers: the filename fast path (name/extension patterns above), then a
/// content-aware scan for known secret formats and high-entropy assignments.
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
    content_looks_secret(path)
}

// ── Content-aware secret detection ────────────────────────────────────────────

/// Read the file and run the content heuristic. Binary or oversized files are
/// presumed non-secret (credentials are small text files).
fn content_looks_secret(path: &Path) -> bool {
    let Ok(meta) = fs::metadata(path) else {
        return false;
    };
    if meta.len() > SECRET_SCAN_MAX_BYTES {
        return false;
    }
    let Ok(bytes) = fs::read(path) else {
        return false;
    };
    let Ok(text) = String::from_utf8(bytes) else {
        return false; // binary — not an env/PEM/JWT file
    };
    text_looks_secret(&text)
}

/// True if the text contains recognizable credential material.
fn text_looks_secret(text: &str) -> bool {
    if text.contains("-----BEGIN") && text.contains("PRIVATE KEY-----") {
        return true;
    }
    text.lines().any(|line| {
        let line = line.trim();
        known_token_in_line(line) || secret_assignment_in_line(line)
    })
}

/// True if the line embeds a well-known token format (AWS key IDs, GitHub /
/// Slack / Google tokens, JWTs) anywhere in its text.
fn known_token_in_line(line: &str) -> bool {
    // AWS access key IDs: AKIA/ASIA + 16 A-Z0-9 = 20 chars total.
    for prefix in ["AKIA", "ASIA"] {
        if has_prefixed_token(line, prefix, 20, |c| {
            c.is_ascii_uppercase() || c.is_ascii_digit()
        }) {
            return true;
        }
    }
    // GitHub tokens: ghp_/gho_/ghu_/ghs_/ghr_ + 36+ alphanumerics.
    for prefix in ["ghp_", "gho_", "ghu_", "ghs_", "ghr_"] {
        if has_prefixed_token(line, prefix, 41, |c| c.is_ascii_alphanumeric() || c == '_') {
            return true;
        }
    }
    // GitHub fine-grained PATs.
    if has_prefixed_token(line, "github_pat_", 60, |c| {
        c.is_ascii_alphanumeric() || c == '_'
    }) {
        return true;
    }
    // Slack tokens: xox[bapors]-...
    for prefix in ["xoxb-", "xoxp-", "xoxa-", "xoxr-", "xoxo-", "xoxs-"] {
        if has_prefixed_token(line, prefix, 25, |c| c.is_ascii_alphanumeric() || c == '-') {
            return true;
        }
    }
    // Google API keys: AIza + 35 base64ish chars.
    if has_prefixed_token(line, "AIza", 39, |c| {
        c.is_ascii_alphanumeric() || c == '_' || c == '-'
    }) {
        return true;
    }
    jwt_in_line(line)
}

/// Does `line` contain `prefix` followed by at least `min_total_len - prefix.len()`
/// consecutive chars accepted by `charset`?
fn has_prefixed_token(
    line: &str,
    prefix: &str,
    min_total_len: usize,
    charset: impl Fn(char) -> bool,
) -> bool {
    let Some(idx) = line.find(prefix) else {
        return false;
    };
    let rest = &line[idx + prefix.len()..];
    let tok_len = rest.chars().take_while(|&c| charset(c)).count();
    prefix.len() + tok_len >= min_total_len
}

/// True if the line contains a JWT: three base64url segments joined by dots,
/// header and payload both starting with the "eyJ" marker.
fn jwt_in_line(line: &str) -> bool {
    let Some(idx) = line.find("eyJ") else {
        return false;
    };
    let bytes = line.as_bytes();
    let mut i = idx;
    let mut segment_starts: Vec<usize> = Vec::new();
    for seg in 0..3 {
        let start = i;
        segment_starts.push(start);
        while i < bytes.len()
            && (bytes[i].is_ascii_alphanumeric() || matches!(bytes[i], b'-' | b'_'))
        {
            i += 1;
        }
        let seg_len = i - start;
        // Header/payload segments of a real JWT are >= 10 chars; the signature
        // segment may be shorter but must be non-empty.
        if seg_len < if seg < 2 { 10 } else { 1 } {
            return false;
        }
        if seg < 2 {
            if i >= bytes.len() || bytes[i] != b'.' {
                return false;
            }
            i += 1; // skip the dot
        }
    }
    // Both the header and the payload must start with the base64-of-{" marker.
    segment_starts.len() == 3 && line[segment_starts[1]..].starts_with("eyJ")
}

/// True if the line is an assignment (`NAME=VALUE`, `NAME: VALUE`) whose name
/// is secret-adjacent and whose value is long, restricted-charset, and
/// high-entropy.
fn secret_assignment_in_line(line: &str) -> bool {
    let Some((name, value)) = split_assignment(line) else {
        return false;
    };
    let name_lower = name.to_lowercase();
    if !SECRET_NAME_MARKERS.iter().any(|m| name_lower.contains(m)) {
        return false;
    }
    let value = value.trim().trim_matches('"').trim_matches('\'');
    value.len() >= SECRET_MIN_VALUE_LEN
        && value
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, '+' | '/' | '=' | '_' | '-'))
        && shannon_entropy(value) >= SECRET_ENTROPY_THRESHOLD
}

/// Split `NAME=VALUE` / `NAME: VALUE` into halves; None if no separator.
fn split_assignment(line: &str) -> Option<(&str, &str)> {
    let idx = line.find('=').or_else(|| line.find(':'))?;
    let (name, value) = line.split_at(idx);
    let value = value.get(1..).unwrap_or("");
    let name = name.trim();
    if name.is_empty() {
        return None;
    }
    Some((name, value))
}

/// Shannon entropy (bits per character) of a string.
fn shannon_entropy(s: &str) -> f64 {
    if s.is_empty() {
        return 0.0;
    }
    let mut counts = std::collections::HashMap::new();
    for c in s.chars() {
        *counts.entry(c).or_insert(0u32) += 1;
    }
    let len = s.chars().count() as f64;
    counts
        .values()
        .map(|&n| {
            let p = n as f64 / len;
            -p * p.log2()
        })
        .sum()
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
        entry.read_to_end(&mut buf).map_err(|e| {
            CascadeError::Other(format!("read entry {}: {e}", entry_path.display()))
        })?;

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
        fs::write(
            dir.join("memory").join("decisions.md"),
            "# Decisions\n## D-01\nUse BLAKE3.",
        )
        .unwrap();
        fs::write(
            dir.join("memory").join("lessons.md"),
            "# Lessons\nLesson 1.",
        )
        .unwrap();
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
        assert!(
            !has_vault,
            "vault.env should be excluded without --include-secrets"
        );
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
        assert!(
            has_vault,
            "vault.env should be included with --include-secrets"
        );
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
        assert!(
            restored_decisions.exists(),
            "decisions.md should be restored"
        );
        let content = fs::read_to_string(&restored_decisions).unwrap();
        assert!(
            content.contains("BLAKE3"),
            "content should round-trip intact"
        );

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
        fs::write(
            &sidecar,
            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        )
        .unwrap();

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

    // ── Content-aware secret detection (T-P7-E07-05) ─────────────────────────

    #[test]
    fn text_looks_secret_detects_known_formats() {
        // AWS access key pair.
        assert!(text_looks_secret("AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE"));
        // PEM private key.
        assert!(text_looks_secret(
            "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7...\n-----END RSA PRIVATE KEY-----"
        ));
        // JWT.
        assert!(text_looks_secret(
            "authorization: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
        ));
        // GitHub token.
        assert!(text_looks_secret(
            "github_token: ghp_16C7e42F292c6912E7710c838347Ae178B4a"
        ));
        // Slack token.
        assert!(text_looks_secret(
            "xoxb-123456789012-1234567890123-abcDEFabcDEFabcDEFabcDEF"
        ));
        // High-entropy assignment with a secret-adjacent name.
        assert!(text_looks_secret(
            "api_secret=wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
        ));
    }

    #[test]
    fn text_looks_secret_ignores_benign_text() {
        // Ordinary markdown notes.
        assert!(!text_looks_secret(
            "# Decisions\n\nUse BLAKE3 for hashing because it is fast and audited.\n"
        ));
        // Assignment with a secret-ish name but a low-entropy short value.
        assert!(!text_looks_secret("password=hunter2"));
        // Assignment with a long value that is natural language (spaces break
        // the restricted charset).
        assert!(!text_looks_secret(
            "auth_note=remember to rotate the keys every quarter please"
        ));
        // Prose mentioning token formats without embedding one.
        assert!(!text_looks_secret(
            "Tokens starting with AKIA are AWS access key IDs; never commit them."
        ));
        // Base64-looking but low-entropy repeated runs.
        assert!(!text_looks_secret("secret=aaaaaaaaaaaaaaaaaaaaaaaaaaaa"));
    }

    #[test]
    fn shannon_entropy_bounds() {
        assert_eq!(shannon_entropy(""), 0.0);
        // Single repeated char: zero entropy.
        assert_eq!(shannon_entropy("aaaaaaaa"), 0.0);
        // Two evenly-mixed chars: exactly 1 bit.
        assert!((shannon_entropy("abababab") - 1.0).abs() < 1e-9);
    }

    /// A credential file under an UNLISTED name must be excluded by the
    /// content scan; benign files alongside it must still be included.
    #[test]
    fn export_excludes_unlisted_name_secret_file_by_content() {
        let td = TempDir::new().unwrap();
        let source = td.path().join(".cascade");
        make_fake_cascade(&source);
        // Unlisted filename, no secret extension — only content can catch it.
        fs::write(
            source.join("deploy-settings.conf"),
            "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\nregion=us-east-1\n",
        )
        .unwrap();

        let archive_bytes = build_archive(&source, false).unwrap();
        let manifest = extract_manifest(&archive_bytes).unwrap();
        assert!(
            !manifest
                .files
                .iter()
                .any(|f| f.contains("deploy-settings.conf")),
            "unlisted-name credential file must be excluded by content scan"
        );
        // Benign files are unaffected.
        assert!(manifest.files.iter().any(|f| f.contains("decisions.md")));

        // --include-secrets still lets it through.
        let archive_bytes = build_archive(&source, true).unwrap();
        let manifest = extract_manifest(&archive_bytes).unwrap();
        assert!(manifest
            .files
            .iter()
            .any(|f| f.contains("deploy-settings.conf")));
    }
}
