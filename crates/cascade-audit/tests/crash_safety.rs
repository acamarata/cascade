//! Integration tests for crash-safe append and concurrent-writer fork detection.
//!
//! Purpose: Verify that AuditLog::append() uses the tmp+rename pattern correctly,
//!          that stale .tmp files are cleaned up on open(), and that concurrent writers
//!          never corrupt the hash chain.
//! SPORT: cascade-audit crash-safe append (T-P3-E00-04)

use cascade_audit::{AuditEntry, AuditError, AuditLog, AuditOp};
use std::sync::{Arc, Barrier};
use tempfile::TempDir;

fn sample_entry(n: u32) -> AuditEntry {
    AuditEntry {
        actor: format!("test-actor-{n}"),
        op: AuditOp::DaemonStart,
        target: format!("target-{n}"),
        detail: format!("detail-{n}"),
    }
}

/// Test 1: Simulated crash — stale .tmp file is cleaned up on re-open;
/// canonical log remains intact and verify_chain() passes.
#[test]
fn stale_tmp_cleaned_up_on_open() {
    let dir = TempDir::new().unwrap();
    let log_path = dir.path().join("audit.log");

    // Create initial log with one record
    {
        let log = AuditLog::open(&log_path).unwrap();
        log.append(sample_entry(0)).unwrap();
    }

    // Simulate a crashed writer: create a stale .tmp file
    let tmp_path = dir.path().join("audit.log.tmp");
    std::fs::write(&tmp_path, b"garbage stale content\n").unwrap();

    // Backdate the mtime to 60 seconds ago (exceeds the 30-second threshold)
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap();
    let past_secs = now.as_secs() as i64 - 60;
    let past_time = filetime::FileTime::from_unix_time(past_secs, 0);
    filetime::set_file_mtime(&tmp_path, past_time).unwrap();

    // Re-open the log — cleanup_tmp() should remove the stale .tmp
    let log2 = AuditLog::open(&log_path).unwrap();
    assert!(
        !tmp_path.exists(),
        ".tmp should be removed on open after 30s threshold"
    );

    // Canonical log must still be intact
    let violations = log2.verify_chain().unwrap();
    assert!(
        violations.is_empty(),
        "chain should be intact after stale-tmp cleanup: {violations:?}"
    );
}

/// Test 2: Concurrent writers — after both threads complete (with either success or
/// ChainFork), the final chain must be valid with no violations.
#[test]
fn concurrent_writer_chain_remains_valid() {
    let dir = TempDir::new().unwrap();
    let log_path = dir.path().join("audit.log");

    // Pre-seed one record so there is a valid chain to fork from
    {
        let log = AuditLog::open(&log_path).unwrap();
        log.append(sample_entry(0)).unwrap();
    }

    let path = Arc::new(log_path.clone());
    let barrier = Arc::new(Barrier::new(2));

    let path1 = Arc::clone(&path);
    let barrier1 = Arc::clone(&barrier);
    let h1 = std::thread::spawn(move || {
        let log = AuditLog::open(&path1).unwrap();
        barrier1.wait(); // synchronize both threads to race the same append window
        log.append(sample_entry(1))
    });

    let path2 = Arc::clone(&path);
    let barrier2 = Arc::clone(&barrier);
    let h2 = std::thread::spawn(move || {
        let log = AuditLog::open(&path2).unwrap();
        barrier2.wait();
        log.append(sample_entry(2))
    });

    let r1 = h1.join().unwrap();
    let r2 = h2.join().unwrap();

    // Tally outcomes: each must be either Ok or ChainFork — never another error
    let mut successes = 0usize;
    let mut forks = 0usize;
    for r in [&r1, &r2] {
        match r {
            Ok(_) => successes += 1,
            Err(AuditError::ChainFork) => forks += 1,
            Err(e) => panic!("unexpected error from concurrent append: {e}"),
        }
    }
    assert_eq!(
        successes + forks,
        2,
        "total outcomes must account for both threads"
    );
    assert!(successes >= 1, "at least one append must succeed");

    // Regardless of whether a fork was detected, the chain must be valid
    let log = AuditLog::open(&log_path).unwrap();
    let violations = log.verify_chain().unwrap();
    assert!(
        violations.is_empty(),
        "chain must have no violations after concurrent appends: {violations:?}"
    );
}

/// Test 3: Normal sequential appends produce a valid chain.
#[test]
fn normal_append_verify_chain_passes() {
    let dir = TempDir::new().unwrap();
    let log_path = dir.path().join("audit.log");
    let log = AuditLog::open(&log_path).unwrap();

    log.append(sample_entry(0)).unwrap();
    log.append(sample_entry(1)).unwrap();
    log.append(sample_entry(2)).unwrap();

    let violations = log.verify_chain().unwrap();
    assert!(
        violations.is_empty(),
        "clean chain of 3 records should have no violations: {violations:?}"
    );
}
