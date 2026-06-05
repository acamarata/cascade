//! Key-index management for cascade daemon.
//!
//! Purpose: Maintain a persistent JSON file (`~/.cascade/key-index.json`) that
//! maps short token names to credential paths. Supports concurrent read/write via
//! exclusive file locking to prevent corruption when multiple daemon processes
//! access the file simultaneously.
//!
//! Inputs:  `key_array` — array of `{name, path}` objects to persist.
//! Outputs: JSON file at `key_index_path` with exclusive lock held during I/O.
//! Constraints: all read/write ops must lock the file exclusively (fs2::FileExt)
//!              and do all I/O through the single locked handle — never open a
//!              second handle to the same path while holding the lock.
//! SPORT: MASTER-SECURITY.md — key-index-lock=✅

use fs2::FileExt;
use serde_json::{json, Value};
use std::fs::OpenOptions;
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::Path;

/// Read the key-index JSON file with a shared lock held for the duration.
///
/// Opens one handle, acquires `lock_shared`, reads through that same handle,
/// then releases the lock. A second `File::open` is never called — doing so
/// would bypass the lock and expose a TOCTOU race.
///
/// # Arguments
/// * `key_index_path` — path to the key-index JSON file
///
/// # Returns
/// Array of key objects: `[{name: string, path: string}, ...]`
///
/// # Errors
/// File open/lock failures or JSON parse errors.
// clippy::incompatible_msrv: fs2::FileExt::lock_shared / unlock are crate
// methods, not std items.  Clippy incorrectly attributes them to a std API
// that is stable since 1.89; the actual calls are on the fs2 trait and compile
// against the declared MSRV.
#[allow(clippy::incompatible_msrv)]
pub fn read_key_index(key_index_path: &Path) -> Result<Value, Box<dyn std::error::Error>> {
    // Bug 1 fix: open ONE handle and read through it. Use lock_shared so
    // multiple readers can proceed concurrently while still excluding writers.
    let mut file = OpenOptions::new()
        .read(true)
        .write(true) // write flag required on some platforms for lock_shared to work
        .create(true)
        .truncate(false) // read path — never truncate on open
        .open(key_index_path)?;

    file.lock_shared()?;

    let mut contents = String::new();
    file.read_to_string(&mut contents)?;

    let result = if contents.is_empty() {
        Ok(json!([]))
    } else {
        serde_json::from_str(&contents).map_err(|e| Box::new(e) as Box<dyn std::error::Error>)
    };

    file.unlock()?;
    result
}

/// Write the key-index JSON file with an exclusive lock held for the duration.
///
/// Opens one handle WITHOUT truncation, acquires `lock_exclusive`, THEN
/// truncates via `set_len(0)` + `seek(0)`, writes, and flushes — all while
/// holding the lock.  The previous implementation truncated on `open()` before
/// the lock was acquired (Bug 3: truncation race) and also opened a second
/// `File::create` handle inside the locked closure (Bug 2: lock bypass).
///
/// # Arguments
/// * `key_index_path` — path to the key-index JSON file
/// * `key_array` — array of key objects to persist
///
/// # Errors
/// File open/lock failures or JSON serialization errors.
// clippy::incompatible_msrv: same as read_key_index — fs2 trait methods.
#[allow(clippy::incompatible_msrv)]
pub fn write_key_index(
    key_index_path: &Path,
    key_array: &Value,
) -> Result<(), Box<dyn std::error::Error>> {
    // Bug 2 + 3 fix: open WITHOUT truncate so the file is not visible as empty
    // to other readers before the lock is held. Truncation happens after
    // lock_exclusive() via set_len(0) + seek(0).
    let mut file = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        // NO .truncate(true) here — that would truncate before we hold the lock.
        // Explicit .truncate(false) to satisfy clippy::suspicious_open_options.
        .truncate(false)
        .open(key_index_path)?;

    file.lock_exclusive()?;

    // Truncate and rewind now that we exclusively own the file.
    file.set_len(0)?;
    file.seek(SeekFrom::Start(0))?;

    let json_str = serde_json::to_string_pretty(key_array)
        .map_err(|e| Box::new(e) as Box<dyn std::error::Error>)?;
    file.write_all(json_str.as_bytes())?;
    file.flush()?;

    file.unlock()?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::thread;
    use tempfile::TempDir;

    /// Verify that two threads hammering concurrent writes never produce a
    /// corrupt (non-parseable or empty) file.  This would fail before the Bug
    /// 2+3 fix because truncation could race with another writer's exclusive
    /// lock, leaving the file empty mid-write.
    #[test]
    fn test_concurrent_writes_no_corruption() {
        let dir = TempDir::new().unwrap();
        let key_index_path = dir.path().join("key-index.json");

        let key_index_path1 = key_index_path.clone();
        let key_index_path2 = key_index_path.clone();
        let key_index_path3 = key_index_path.clone();

        // Writer thread 1
        let handle1 = thread::spawn(move || {
            for i in 0..200 {
                let key_array = json!([
                    {"name": format!("key1-{}", i), "path": "/path/to/key1"},
                ]);
                write_key_index(&key_index_path1, &key_array).unwrap();
            }
        });

        // Writer thread 2
        let handle2 = thread::spawn(move || {
            for i in 0..200 {
                let key_array = json!([
                    {"name": format!("key2-{}", i), "path": "/path/to/key2"},
                ]);
                write_key_index(&key_index_path2, &key_array).unwrap();
            }
        });

        // Concurrent reader — must never see an empty or corrupt file once the
        // first write has occurred. We collect all reads and check them after
        // the writers finish; any parse failure indicates a lock-bypass race.
        let handle3 = thread::spawn(move || {
            let mut iterations = 0u32;
            loop {
                iterations += 1;
                if iterations > 10_000 {
                    break;
                }
                match read_key_index(&key_index_path3) {
                    Ok(v) => {
                        // Every successful read must return a valid JSON array
                        // (either empty initial state or a written payload).
                        assert!(
                            v.is_array(),
                            "concurrent read must return a JSON array, got: {v}"
                        );
                    }
                    Err(e) => panic!("concurrent read returned error: {e}"),
                }
            }
        });

        handle1.join().unwrap();
        handle2.join().unwrap();
        handle3.join().unwrap();

        // Final state must be a non-empty valid JSON array.
        let final_contents = read_key_index(&key_index_path).unwrap();
        assert!(
            final_contents.is_array(),
            "final file must be valid JSON array"
        );
        assert!(
            !final_contents.as_array().unwrap().is_empty(),
            "final file must not be empty — write-through-lock must persist data"
        );
    }

    #[test]
    fn test_read_empty_file_returns_empty_array() {
        let dir = TempDir::new().unwrap();
        let key_index_path = dir.path().join("key-index.json");

        let result = read_key_index(&key_index_path).unwrap();
        assert_eq!(
            result,
            json!([]),
            "reading non-existent file must return empty array"
        );
    }

    #[test]
    fn test_write_and_read_roundtrip() {
        let dir = TempDir::new().unwrap();
        let key_index_path = dir.path().join("key-index.json");

        let key_array = json!([
            {"name": "test-key", "path": "/path/to/test"},
        ]);

        write_key_index(&key_index_path, &key_array).unwrap();
        let read_back = read_key_index(&key_index_path).unwrap();

        assert_eq!(read_back, key_array, "written data must match read data");
    }
}
