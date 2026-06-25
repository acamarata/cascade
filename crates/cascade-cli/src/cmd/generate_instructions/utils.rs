//! Shared file-system utilities for generate-instructions sub-modules.

use std::fs;
use std::path::{Path, PathBuf};

use cascade_types::error::{CascadeError, Result};

pub(super) fn ensure_dir(dir: &Path) -> Result<()> {
    fs::create_dir_all(dir).map_err(|e| CascadeError::Io {
        path: dir.to_path_buf(),
        operation: "create_dir_all",
        source: e,
    })
}

pub(super) fn atomic_write(path: &Path, content: &str) -> Result<()> {
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".generate-instr.tmp");
    let tmp_path = PathBuf::from(tmp);

    fs::write(&tmp_path, content).map_err(|e| CascadeError::Io {
        path: tmp_path.clone(),
        operation: "write_tmp",
        source: e,
    })?;

    fs::rename(&tmp_path, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename",
        source: e,
    })
}

/// Print a simple unified-style diff to stdout (no external `similar` crate needed).
pub(super) fn print_diff(path: &Path, before: &str, after: &str) {
    println!("--- {}", path.display());
    println!("+++ {}", path.display());
    for line in before.lines() {
        println!("- {line}");
    }
    for line in after.lines() {
        println!("+ {line}");
    }
}

pub(super) fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME").ok().map(PathBuf::from)
}
