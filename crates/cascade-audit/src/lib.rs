//! cascade-audit — append-only, SHA-256 hash-chained audit log.
//!
//! Each entry is a JSONL line appended to a file (default: `~/.cascade/audit.log`).
//! The chain hash prevents undetected tampering: mutating any field of any record
//! breaks all subsequent hashes, which `verify_chain` detects.
//!
//! # Hash preimage (canonical — must never change once entries exist)
//!
//! ```text
//! preimage = prev_hash || ts || op || actor || target || detail
//! hash     = hex(SHA-256(preimage.as_bytes()))
//! ```
//!
//! Fields are concatenated **without separators** in the order shown.
//! `prev_hash` of the first record is the 64-char genesis zero string.
//!
//! # Crash safety and multi-process concurrency
//!
//! `append()` uses a **tmp+rename** pattern for crash atomicity:
//! 1. Write the complete serialised record to `<log>.tmp`.
//! 2. `flush()` + `sync_data()` — bytes reach the OS before rename.
//! 3. Acquire an **advisory exclusive flock** on the `.tmp` file (via `fs2`).
//! 4. Re-read `last_hash()` from the canonical log path inside the flock.
//!    If it changed (another writer committed between our read and our lock),
//!    clean up `.tmp` and return `Err(AuditError::ChainFork)`.
//! 5. `fs::rename(<log>.tmp, <log>)` — atomic on POSIX same-filesystem.
//! 6. Flock releases implicitly when the `.tmp` file handle is dropped.
//! 7. Update the `.count` sidecar **after** the rename.
//!
//! On process startup, `open()` removes any stale `.tmp` left by a prior crash
//! (file older than 30 seconds), logging a `tracing::warn!` for observability.

use std::{
    fmt,
    fs::{self, File, OpenOptions},
    io::{self, BufRead, BufReader, Write},
    path::{Path, PathBuf},
    time::SystemTime,
};

use chrono::Utc;
use fs2::FileExt;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use thiserror::Error;

// ──────────────────────────────────────────────────────────────────
// Genesis constant
// ──────────────────────────────────────────────────────────────────

/// 64-char all-zero hex string used as `prev_hash` for the very first record.
///
/// Purpose: provides a deterministic starting point so the chain begins with a
/// well-known sentinel that is easy to verify by external tooling.
pub const GENESIS_HASH: &str = "0000000000000000000000000000000000000000000000000000000000000000";

// ──────────────────────────────────────────────────────────────────
// Operation enum
// ──────────────────────────────────────────────────────────────────

/// Defined operation types for audit records.
///
/// Purpose: restrict `op` to a closed set so downstream tooling can pattern-match
///          without free-form string parsing.
/// Inputs:  n/a (unit variants)
/// Outputs: serialises as snake_case string
/// Constraints: adding a variant is non-breaking; removing one is.
/// SPORT: MASTER-COMPONENTS.md § cascade-audit / AuditOp
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuditOp {
    GciWrite,
    CascadeResolve,
    KeyRotation,
    SymlinkCreate,
    SymlinkDelete,
    DaemonStart,
    DaemonStop,
    /// Escape hatch for callers that need a one-off operation not yet in the enum.
    #[serde(rename = "other")]
    Other(String),
}

impl fmt::Display for AuditOp {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            AuditOp::GciWrite => write!(f, "gci_write"),
            AuditOp::CascadeResolve => write!(f, "cascade_resolve"),
            AuditOp::KeyRotation => write!(f, "key_rotation"),
            AuditOp::SymlinkCreate => write!(f, "symlink_create"),
            AuditOp::SymlinkDelete => write!(f, "symlink_delete"),
            AuditOp::DaemonStart => write!(f, "daemon_start"),
            AuditOp::DaemonStop => write!(f, "daemon_stop"),
            AuditOp::Other(s) => write!(f, "{s}"),
        }
    }
}

// ──────────────────────────────────────────────────────────────────
// AuditEntry (caller-supplied input — no hash fields)
// ──────────────────────────────────────────────────────────────────

/// Input record supplied by the caller to `AuditLog::append`.
///
/// Purpose: decouple what callers supply (operation intent) from what is persisted
///          (operation + chain hash).
/// Inputs:  actor, op, target, detail
/// Outputs: converted to AuditRecord inside append()
/// Constraints: timestamp is assigned by append() using Utc::now()
/// SPORT: MASTER-COMPONENTS.md § cascade-audit / AuditEntry
#[derive(Debug, Clone)]
pub struct AuditEntry {
    /// Identity that performed the operation (e.g. "cascade-daemon", "cascade-cli").
    pub actor: String,
    /// Typed operation being logged.
    pub op: AuditOp,
    /// Resource targeted by the operation (file path, key name, …).
    pub target: String,
    /// Human-readable detail / payload summary.
    pub detail: String,
}

// ──────────────────────────────────────────────────────────────────
// AuditRecord (on-disk JSONL line)
// ──────────────────────────────────────────────────────────────────

/// One line in the JSONL audit log, including chain hash fields.
///
/// Purpose: tamper-evident, self-describing audit event.
/// Inputs:  derived from AuditEntry + chain state inside append()
/// Outputs: serialised to one JSON line (no embedded newlines)
/// Constraints: field order in the JSON output is not load-bearing; only the hash
///              preimage order (prev_hash || ts || op || actor || target || detail)
///              is canonical.
/// SPORT: MASTER-COMPONENTS.md § cascade-audit / AuditRecord
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuditRecord {
    /// RFC-3339 UTC timestamp assigned at append time.
    pub ts: String,
    /// Operation type.
    pub op: AuditOp,
    /// Actor identity.
    pub actor: String,
    /// Targeted resource.
    pub target: String,
    /// Human-readable detail.
    pub detail: String,
    /// SHA-256 hex hash of the immediately preceding record (GENESIS_HASH for record 0).
    pub prev_hash: String,
    /// SHA-256 hex of: `prev_hash || ts || op || actor || target || detail`
    pub hash: String,
}

// ──────────────────────────────────────────────────────────────────
// ChainViolation
// ──────────────────────────────────────────────────────────────────

/// Describes a detected tamper at a specific line in the audit log.
///
/// Purpose: provide enough context for forensic analysis when verify_chain finds
///          a break.
/// Constraints: `line` is 0-based (first record = 0).
/// SPORT: MASTER-COMPONENTS.md § cascade-audit / ChainViolation
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChainViolation {
    /// 0-based index of the record where the violation was detected.
    pub line: usize,
    /// Hash stored in the record.
    pub stored_hash: String,
    /// Hash recomputed from the record's fields.
    pub recomputed_hash: String,
    /// Human-readable reason string.
    pub reason: String,
}

// ──────────────────────────────────────────────────────────────────
// AuditError
// ──────────────────────────────────────────────────────────────────

/// Error type for cascade-audit operations.
///
/// Purpose: typed errors so callers can distinguish I/O from serialisation from
///          tamper detection.
/// SPORT: MASTER-COMPONENTS.md § cascade-audit / AuditError
#[derive(Debug, Error)]
pub enum AuditError {
    #[error("I/O error: {0}")]
    Io(#[from] io::Error),

    #[error("Serialisation error: {0}")]
    Serialization(#[source] serde_json::Error),

    #[error("Deserialisation error on line {line}: {source}")]
    Deserialization {
        line: usize,
        #[source]
        source: serde_json::Error,
    },

    /// Returned by verify_chain when ≥1 hash mismatch is detected.
    #[error("Tamper detected: {count} violation(s)")]
    TamperDetected { count: usize },
    /// A concurrent writer committed a new record between this writer's
    /// `last_hash()` read and its exclusive flock acquisition, causing a
    /// chain fork.  The caller should retry.
    #[error("chain fork detected: concurrent writer committed between hash read and rename")]
    ChainFork,
}

// ──────────────────────────────────────────────────────────────────
// Hash computation
// ──────────────────────────────────────────────────────────────────

/// Compute the SHA-256 chain hash for one record.
///
/// Purpose: canonical single source of truth for the hash preimage so both
///          append() and verify_chain() use the exact same formula.
/// Inputs:  prev_hash (64-char hex), ts (RFC-3339 string), op (display string),
///          actor, target, detail
/// Outputs: 64-char lowercase hex string
/// Constraints: fields are concatenated WITHOUT separators in the documented order.
///              Changing this formula invalidates all existing logs.
fn compute_hash(
    prev_hash: &str,
    ts: &str,
    op: &str,
    actor: &str,
    target: &str,
    detail: &str,
) -> String {
    // Fields are joined with the ASCII Unit Separator (0x1F) so that variable
    // field boundaries cannot collide: without a delimiter, (actor="ab",
    // target="c") and (actor="a", target="bc") would hash identically. 0x1F
    // never appears in RFC-3339 timestamps, hashes, or normal text fields.
    let input = format!("{prev_hash}\u{1f}{ts}\u{1f}{op}\u{1f}{actor}\u{1f}{target}\u{1f}{detail}");
    let digest = Sha256::digest(input.as_bytes());
    // Format each byte as two lowercase hex digits without an external hex crate.
    digest.iter().fold(String::with_capacity(64), |mut s, b| {
        use std::fmt::Write;
        write!(s, "{b:02x}").unwrap();
        s
    })
}

// ──────────────────────────────────────────────────────────────────
// AuditLog
// ──────────────────────────────────────────────────────────────────

/// Append-only, SHA-256 hash-chained audit log backed by a JSONL file.
///
/// Purpose: provide tamper-evident logging of privileged Cascade operations.
///          Each record embeds the hash of the previous record; any mutation of
///          any field breaks all subsequent hashes.
/// Inputs:  path to the JSONL log file (parent directory must exist)
/// Outputs: `append` returns the written AuditRecord; `verify_chain` returns
///          all detected violations.
/// Constraints:
///   - File permissions are set to 0600 on creation (macOS/Linux).
///   - `append` uses a crash-atomic tmp+rename strategy:
///       1. Write the new entry to `<log>.tmp` and call `sync_data()`.
///       2. Acquire an exclusive advisory flock on `<log>.tmp` (fs2).
///       3. Re-read `last_hash()` inside the flock; return `Err(ChainFork)` if
///          a concurrent writer committed between the pre-lock read and now.
///       4. `fs::rename(<log>.tmp, <log>)` — POSIX-atomic on the same filesystem.
///       5. Release flock (implicit on file drop).
///          The in-process Mutex in cascade-daemon serialises all daemon writes; the
///          advisory flock guards against concurrent CLI processes (e.g. `cascade
///          audit tail`) racing the daemon on the same file.
///   - On `open`, any `<log>.tmp` file older than 30 seconds is removed with a
///     `tracing::warn!` (crash-recovery cleanup).
///   - Tail truncation (deleting recent records) is detected via a `<log>.count`
///     sidecar; the sidecar is updated **after** the rename.  A local admin who
///     can edit BOTH the log and its sidecar can still erase tracks; the chain
///     does not substitute for a remote append-only store in high-security
///     deployments.
///
/// SPORT: MASTER-COMPONENTS.md § cascade-audit / AuditLog
pub struct AuditLog {
    path: PathBuf,
}

/// Restrict `path` to owner-only access (0600 on unix).
///
/// The audit log is a security artifact, so its permissions are not
/// best-effort on unix — a failure to tighten them is returned to the caller.
///
/// On Windows there is no mode bitmask; `PermissionsExt` and
/// `Permissions::from_mode` do not exist there, and calling them
/// unconditionally is why cascade-audit did not compile for Windows at all.
/// The Windows equivalent is an ACL, which is tracked separately with the
/// named-pipe DACL work (T-P7-E25-10); until then this is a no-op there
/// rather than a silent claim of protection.
#[cfg(unix)]
fn restrict_to_owner(path: &Path) -> Result<(), AuditError> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))?;
    Ok(())
}

/// Windows has no mode bitmask; see the unix variant for why this is a no-op.
#[cfg(not(unix))]
fn restrict_to_owner(_path: &Path) -> Result<(), AuditError> {
    Ok(())
}

impl AuditLog {
    /// Open (or create) the audit log at `path`.
    ///
    /// Inputs:  `path` — absolute or relative path to the JSONL file.
    /// Outputs: `Ok(AuditLog)` if the file can be created/opened with 0600 perms.
    /// Constraints: parent directory must already exist.
    pub fn open(path: &Path) -> Result<Self, AuditError> {
        // Create or open the file so we can set permissions.
        let file = OpenOptions::new().create(true).append(true).open(path)?;
        // Set 0600 on creation (no-op if the file already had these perms).
        drop(file);
        restrict_to_owner(path)?;
        let log = Self {
            path: path.to_path_buf(),
        };
        // Remove any stale .tmp left by a prior crashed append.
        log.cleanup_tmp();
        Ok(log)
    }

    /// Return the path of the underlying log file (useful for tests / diagnostics).
    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Path of the tamper-evidence count sidecar (`<log>.count`).
    fn count_path(&self) -> PathBuf {
        let mut p = self.path.clone().into_os_string();
        p.push(".count");
        PathBuf::from(p)
    }

    /// Path of the crash-recovery tmp file (`<log>.tmp`).
    fn tmp_path(&self) -> PathBuf {
        let mut p = self.path.clone().into_os_string();
        p.push(".tmp");
        PathBuf::from(p)
    }

    /// Remove a stale `<log>.tmp` file if it is older than 30 seconds.
    ///
    /// Purpose: crash-recovery cleanup called on every `open()`.
    /// Constraints: 30-second threshold avoids racing a concurrent in-flight
    ///   append; a fresh `.tmp` (< 30 s old) is left untouched.
    fn cleanup_tmp(&self) {
        let tmp = self.tmp_path();
        if !tmp.exists() {
            return;
        }
        let stale = tmp
            .metadata()
            .and_then(|m| m.modified())
            .map(|modified| {
                SystemTime::now()
                    .duration_since(modified)
                    .map(|d| d.as_secs() > 30)
                    .unwrap_or(false)
            })
            .unwrap_or(false);
        if stale {
            if let Err(e) = fs::remove_file(&tmp) {
                tracing::warn!(
                    error = %e,
                    path = %tmp.display(),
                    "failed to remove stale audit tmp file"
                );
            } else {
                tracing::warn!(
                    path = %tmp.display(),
                    "stale audit tmp file removed"
                );
            }
        }
    }

    /// Count the non-empty records currently in the log file.
    fn count_records(&self) -> Result<usize, AuditError> {
        let file = File::open(&self.path)?;
        let reader = BufReader::new(file);
        let mut n = 0usize;
        for line in reader.lines() {
            if !line?.trim().is_empty() {
                n += 1;
            }
        }
        Ok(n)
    }

    /// Persist the current record count to the `<log>.count` sidecar (0600).
    ///
    /// The hash chain detects mutation of existing records but NOT truncation of
    /// the tail — deleting recent records leaves a still-internally-consistent
    /// chain. Persisting the expected count lets [`verify_chain`](Self::verify_chain)
    /// detect such truncation (anti-rollback).
    fn write_count_sidecar(&self) -> Result<(), AuditError> {
        let n = self.count_records()?;
        let cp = self.count_path();
        fs::write(&cp, n.to_string())?;
        let _ = restrict_to_owner(&cp);
        Ok(())
    }

    /// Read the expected record count from the sidecar, if present and parseable.
    fn expected_count(&self) -> Option<usize> {
        fs::read_to_string(self.count_path())
            .ok()
            .and_then(|s| s.trim().parse::<usize>().ok())
    }

    /// Read the hash of the last record, or GENESIS_HASH if the file is empty.
    ///
    /// Purpose: chain the next record onto the current tail.
    /// Constraints: reads the whole file to find the last line; for very large logs
    ///              this is O(n). Acceptable for audit log sizes in typical use.
    fn last_hash(&self) -> Result<String, AuditError> {
        let file = File::open(&self.path)?;
        let reader = BufReader::new(file);
        let mut last = None;
        for (idx, line_result) in reader.lines().enumerate() {
            let line = line_result?;
            if line.trim().is_empty() {
                continue;
            }
            let record: AuditRecord =
                serde_json::from_str(&line).map_err(|e| AuditError::Deserialization {
                    line: idx,
                    source: e,
                })?;
            last = Some(record.hash);
        }
        Ok(last.unwrap_or_else(|| GENESIS_HASH.to_string()))
    }

    /// Append one entry to the audit log.
    ///
    /// Purpose: crash-atomic, concurrency-safe tamper-evident append.
    /// Inputs:  `entry` — caller-supplied AuditEntry (actor, op, target, detail).
    /// Outputs: the written AuditRecord (including the computed hash).
    /// Constraints:
    ///   - Timestamp is assigned as Utc::now() inside this call.
    ///   - Uses tmp+rename: writes to `<log>.tmp`, syncs, acquires exclusive
    ///     advisory flock (fs2), re-reads `last_hash()` inside the flock to detect
    ///     chain forks, then renames atomically to the canonical path.
    ///   - Returns `Err(AuditError::ChainFork)` if a concurrent writer committed
    ///     between the pre-lock hash read and flock acquisition; the caller should
    ///     retry (the in-process Mutex in cascade-daemon prevents this in the
    ///     single-daemon case; the flock guards the multi-process CLI case).
    ///   - Count sidecar is updated AFTER the rename.
    pub fn append(&self, entry: AuditEntry) -> Result<AuditRecord, AuditError> {
        // Pre-lock snapshot of the chain tip.
        let prev_hash = self.last_hash()?;
        let ts = Utc::now().to_rfc3339();
        let op_str = entry.op.to_string();
        let hash = compute_hash(
            &prev_hash,
            &ts,
            &op_str,
            &entry.actor,
            &entry.target,
            &entry.detail,
        );

        let record = AuditRecord {
            ts,
            op: entry.op,
            actor: entry.actor,
            target: entry.target,
            detail: entry.detail,
            prev_hash: prev_hash.clone(),
            hash,
        };

        let mut line = serde_json::to_string(&record).map_err(AuditError::Serialization)?;
        line.push('\n');

        let tmp = self.tmp_path();

        {
            // 1. Write entry to <log>.tmp and sync to OS.
            //    Must copy existing canonical log content first so that rename
            //    atomically replaces the log with (all prior records + new record),
            //    rather than replacing the entire log with only the new record.
            let mut tmp_file = OpenOptions::new()
                .create(true)
                .write(true)
                .truncate(true)
                .open(&tmp)?;
            if self.path.exists() {
                let existing = fs::read(&self.path)?;
                tmp_file.write_all(&existing)?;
            }
            tmp_file.write_all(line.as_bytes())?;
            tmp_file.flush()?;
            tmp_file.sync_data()?;

            // 2. Acquire exclusive advisory flock.
            tmp_file.lock_exclusive()?;

            // 3. Re-read last_hash inside flock — detect chain fork.
            let current_hash = self.last_hash()?;
            if current_hash != prev_hash {
                // A concurrent writer committed; clean up and signal the caller.
                let _ = fs::remove_file(&tmp);
                return Err(AuditError::ChainFork);
            }

            // 4. Atomic rename: <log>.tmp -> canonical log path.
            //    POSIX guarantees this is atomic when src and dst are on the same
            //    filesystem — always true here because tmp is derived from log path.
            fs::rename(&tmp, &self.path)?;

            // 5. Flock releases implicitly when tmp_file is dropped here.
        }

        // 6. Update the anti-rollback count sidecar AFTER the rename.
        if let Err(e) = self.write_count_sidecar() {
            tracing::warn!(%e, "failed to update audit count sidecar");
        }

        tracing::debug!(
            op = %record.op,
            actor = %record.actor,
            hash = %record.hash,
            "audit record appended"
        );

        Ok(record)
    }

    /// Verify the integrity of the entire chain.
    ///
    /// Purpose: detect any mutation of stored records by recomputing each hash from
    ///          scratch and comparing to the stored value.
    /// Inputs:  none (reads the log file at self.path)
    /// Outputs: `Ok(Vec<ChainViolation>)` — empty means no tampering detected.
    ///          A non-empty Vec lists every line where a mismatch was found.
    /// Constraints:
    ///   - prev_hash of record N is expected to equal hash of record N-1.
    ///   - Both violations (wrong prev_hash and wrong self-hash) are reported
    ///     separately when both are present on the same line.
    ///   - An empty file returns Ok(vec![]).
    pub fn verify_chain(&self) -> Result<Vec<ChainViolation>, AuditError> {
        let file = File::open(&self.path)?;
        let reader = BufReader::new(file);
        let mut violations = Vec::new();
        let mut prev_computed_hash = GENESIS_HASH.to_string();
        let mut record_count = 0usize;

        for (idx, line_result) in reader.lines().enumerate() {
            let line = line_result?;
            if line.trim().is_empty() {
                continue;
            }
            record_count += 1;
            let record: AuditRecord =
                serde_json::from_str(&line).map_err(|e| AuditError::Deserialization {
                    line: idx,
                    source: e,
                })?;

            // Check prev_hash linkage.
            if record.prev_hash != prev_computed_hash {
                violations.push(ChainViolation {
                    line: idx,
                    stored_hash: record.prev_hash.clone(),
                    recomputed_hash: prev_computed_hash.clone(),
                    reason: format!(
                        "record[{idx}].prev_hash does not match hash of record[{}]",
                        idx.saturating_sub(1)
                    ),
                });
            }

            // Recompute this record's self-hash.
            let op_str = record.op.to_string();
            let recomputed = compute_hash(
                &record.prev_hash,
                &record.ts,
                &op_str,
                &record.actor,
                &record.target,
                &record.detail,
            );
            if record.hash != recomputed {
                violations.push(ChainViolation {
                    line: idx,
                    stored_hash: record.hash.clone(),
                    recomputed_hash: recomputed.clone(),
                    reason: format!("record[{idx}].hash does not match recomputed value"),
                });
                // Use the recomputed hash so subsequent prev_hash checks use
                // the "what it should have been" value rather than the corrupted one.
                prev_computed_hash = recomputed;
            } else {
                prev_computed_hash = record.hash.clone();
            }
        }

        // Anti-rollback / truncation detection. The hash chain alone cannot
        // detect removal of the tail (the remaining prefix is still internally
        // consistent). If the count sidecar records MORE entries than are present,
        // records were deleted — report it. (An empty/missing log against a
        // sidecar count > 0 surfaces here as `found 0`.)
        if let Some(expected) = self.expected_count() {
            if record_count < expected {
                violations.push(ChainViolation {
                    line: record_count,
                    stored_hash: String::new(),
                    recomputed_hash: String::new(),
                    reason: format!(
                        "audit log truncated: expected {expected} records, found {record_count}"
                    ),
                });
            }
        }

        Ok(violations)
    }
}

// ──────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_entry(op: AuditOp) -> AuditEntry {
        AuditEntry {
            actor: "test-agent".to_string(),
            op,
            target: "/tmp/test".to_string(),
            detail: "test detail".to_string(),
        }
    }

    /// Helper: open a fresh log in a temp dir.
    fn open_log(dir: &TempDir) -> AuditLog {
        let path = dir.path().join("audit.log");
        AuditLog::open(&path).expect("open should succeed")
    }

    #[test]
    fn empty_log_verifies_ok() {
        let dir = TempDir::new().unwrap();
        let log = open_log(&dir);
        let violations = log.verify_chain().expect("verify_chain on empty log");
        assert!(violations.is_empty(), "empty log should have no violations");
    }

    #[test]
    fn tail_truncation_is_detected() {
        let dir = TempDir::new().unwrap();
        let log = open_log(&dir);
        log.append(make_entry(AuditOp::DaemonStart)).unwrap();
        log.append(make_entry(AuditOp::GciWrite)).unwrap();
        log.append(make_entry(AuditOp::DaemonStop)).unwrap();
        // Intact chain (3 records, sidecar=3) verifies clean.
        assert!(log.verify_chain().unwrap().is_empty());

        // Attacker erases the last record from the log but cannot fix the count
        // sidecar (=3). verify_chain must flag the rollback.
        let content = std::fs::read_to_string(log.path()).unwrap();
        let kept: Vec<&str> = content
            .lines()
            .filter(|l| !l.trim().is_empty())
            .take(2)
            .collect();
        std::fs::write(log.path(), kept.join("\n") + "\n").unwrap();

        let violations = log.verify_chain().expect("verify_chain should not error");
        assert!(
            violations.iter().any(|v| v.reason.contains("truncated")),
            "tail truncation must be detected; got {violations:?}"
        );
    }

    #[test]
    fn append_three_records_chain_passes() {
        let dir = TempDir::new().unwrap();
        let log = open_log(&dir);

        log.append(make_entry(AuditOp::DaemonStart)).unwrap();
        log.append(make_entry(AuditOp::GciWrite)).unwrap();
        log.append(make_entry(AuditOp::DaemonStop)).unwrap();

        let violations = log.verify_chain().expect("verify_chain should not error");
        assert!(
            violations.is_empty(),
            "clean chain of 3 should have no violations, got: {violations:?}"
        );
    }

    #[test]
    fn prev_hash_of_record_n_equals_hash_of_record_n_minus_1() {
        let dir = TempDir::new().unwrap();
        let log = open_log(&dir);

        let r0 = log.append(make_entry(AuditOp::DaemonStart)).unwrap();
        let r1 = log.append(make_entry(AuditOp::GciWrite)).unwrap();
        let r2 = log.append(make_entry(AuditOp::DaemonStop)).unwrap();

        assert_eq!(
            r1.prev_hash, r0.hash,
            "record[1].prev_hash must equal record[0].hash"
        );
        assert_eq!(
            r2.prev_hash, r1.hash,
            "record[2].prev_hash must equal record[1].hash"
        );
        assert_eq!(
            r0.prev_hash, GENESIS_HASH,
            "first record must use genesis hash"
        );
    }

    #[test]
    fn tamper_middle_entry_detail_causes_violations() {
        let dir = TempDir::new().unwrap();
        let log = open_log(&dir);

        log.append(make_entry(AuditOp::DaemonStart)).unwrap();
        log.append(make_entry(AuditOp::GciWrite)).unwrap();
        log.append(make_entry(AuditOp::DaemonStop)).unwrap();

        // Read all bytes, mutate record[1]'s detail field.
        let path = log.path().to_path_buf();
        let content = fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines.len(), 3, "should have 3 lines before tamper");

        // Parse, mutate, re-serialise line 1.
        let mut record1: AuditRecord = serde_json::from_str(lines[1]).unwrap();
        record1.detail = "TAMPERED".to_string();
        // Leave record1.hash unchanged — this is what a real tamper looks like.
        let tampered_line1 = serde_json::to_string(&record1).unwrap();

        let new_content = format!("{}\n{}\n{}\n", lines[0], tampered_line1, lines[2]);
        fs::write(&path, new_content).unwrap();

        let violations = log.verify_chain().expect("verify_chain should not error");
        // record[1].hash is now wrong (detail changed), and record[2].prev_hash
        // is also wrong (it stores the pre-tamper hash of record[1]).
        assert!(
            !violations.is_empty(),
            "tampered log must have ≥1 violation"
        );
        // At least one violation must be on line 1.
        assert!(
            violations.iter().any(|v| v.line == 1),
            "violation must be detected at line 1 (the tampered record), got: {violations:?}"
        );
    }

    /// Unix-only: Windows has no mode bitmask to assert against. The
    /// equivalent ACL check belongs with the Windows DACL work
    /// (T-P7-E25-10); asserting nothing there is honest, asserting 0600
    /// would be meaningless.
    #[cfg(unix)]
    #[test]
    fn file_permissions_are_0600() {
        use std::os::unix::fs::PermissionsExt;
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("audit.log");
        let _log = AuditLog::open(&path).unwrap();
        let meta = fs::metadata(&path).unwrap();
        let mode = meta.permissions().mode() & 0o777;
        assert_eq!(mode, 0o600, "log file must have 0600 permissions");
    }

    #[test]
    fn each_line_is_valid_json_audit_record() {
        let dir = TempDir::new().unwrap();
        let log = open_log(&dir);
        log.append(make_entry(AuditOp::KeyRotation)).unwrap();
        log.append(make_entry(AuditOp::CascadeResolve)).unwrap();

        let content = fs::read_to_string(log.path()).unwrap();
        for (i, line) in content.lines().enumerate() {
            let _: AuditRecord = serde_json::from_str(line)
                .unwrap_or_else(|e| panic!("line {i} is not valid JSON AuditRecord: {e}"));
        }
    }

    #[test]
    fn genesis_hash_is_64_zeros() {
        assert_eq!(GENESIS_HASH.len(), 64);
        assert!(
            GENESIS_HASH.chars().all(|c| c == '0'),
            "genesis hash must be 64 zero chars"
        );
    }

    #[test]
    fn compute_hash_is_deterministic() {
        let h1 = compute_hash("prev", "ts", "op", "actor", "target", "detail");
        let h2 = compute_hash("prev", "ts", "op", "actor", "target", "detail");
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64);
    }
}
