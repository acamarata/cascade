//! Session-copy — Phase C (session-failover) ticket C2.
//!
//! Purpose: idempotently copy a Claude Code session transcript (one JSONL
//! file under a project's `projects/<slug>/` directory) from one
//! `CLAUDE_CONFIG_DIR` to another, so a continuity resume
//! (`crate::continuity::fire_resume`) can target a different account's
//! config dir and still find the session history.
//!
//! Design spec: `.claude/planning/10-session-failover-spec.md` §1.4, §5 Q6.
//! Master plan: `.claude/planning/11-vNEXT-master-plan.md` Phase C / C2.
//!
//! Verified fact (master plan Phase C spike, 2026-07-07): on the reference
//! box today, `~/.claude2/projects` is a symlink to `~/.claude/projects` (A1
//! and A2 currently resolve to the same Anthropic account). Canonical root
//! comparison detects that case and treats it as already-synced (no-op copy),
//! so this module is safe to call unconditionally even before a genuinely
//! separate second account is wired into `~/.claude2`.
//!
//! Inputs:  source/destination `projects/` roots, a project slug (the
//!          directory name Claude Code derives from the escaped cwd path),
//!          and a session id (the JSONL filename stem, no extension).
//! Outputs: [`CopyOutcome`] describing what happened; never panics.
//! Constraints: pure `std::fs` — no network, no async — fully unit-testable
//!              with `tempfile::TempDir`.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — failover::session_copy (Phase C / C2)

use std::{
    fmt,
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    path::{Component, Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

#[cfg(unix)]
use std::os::unix::fs::{MetadataExt, OpenOptionsExt, PermissionsExt};

const MAX_TRANSCRIPT_BYTES: u64 = 64 * 1024 * 1024;

/// Result of one [`copy_session`] call.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CopyOutcome {
    /// Source and destination `projects/` roots resolve to the same
    /// canonical filesystem path (e.g. a symlink) — nothing to copy, the
    /// session is already visible from both config dirs.
    AlreadySharedStorage,
    /// Destination did not exist yet — copied fresh bytes.
    Copied,
    /// Destination already held byte-identical content — no write performed.
    UnchangedSkipped,
    /// Destination existed with different content — overwritten with the
    /// source's current bytes (idempotent convergence toward the source,
    /// not a merge).
    Overwritten,
    /// Destination already exists with different content. Public copy calls
    /// preserve the destination and report this conflict instead of
    /// overwriting it.
    ConflictDifferentContent,
}

/// Copy `<src_root>/<project_slug>/<session_id>.jsonl` to the equivalent
/// path under `dst_root`, creating the destination project directory if it
/// does not exist.
///
/// Returns `Err` when validation, root containment, source inspection, or
/// destination I/O fails. The public signature remains `String`-based because
/// this module has no dependency on any daemon error type.
pub fn copy_session(
    src_root: &Path,
    dst_root: &Path,
    project_slug: &str,
    session_id: &str,
) -> Result<CopyOutcome, String> {
    copy_session_inner(src_root, dst_root, project_slug, session_id, false)
        .map_err(|e| e.to_string())
}

fn copy_session_inner(
    src_root: &Path,
    dst_root: &Path,
    project_slug: &str,
    session_id: &str,
    force_overwrite: bool,
) -> Result<CopyOutcome, SessionCopyError> {
    validate_component(ComponentKind::ProjectSlug, project_slug)?;
    validate_component(ComponentKind::SessionId, session_id)?;

    let canonical_dst_root = canonicalize_dst_root(dst_root)?;
    let canonical_src_root = canonicalize_existing_dir(src_root, "SourceRootNotDirectory")?;

    if canonical_src_root == canonical_dst_root {
        return Ok(CopyOutcome::AlreadySharedStorage);
    }

    let filename = format!("{session_id}.jsonl");
    let src_dir = canonical_src_root.join(project_slug);
    let src_file = src_dir.join(&filename);
    let canonical_src_dir = canonicalize_source_dir(&src_dir, &src_file)?;
    require_under("SourceEscapesRoot", &canonical_src_dir, &canonical_src_root)?;

    let src_bytes = read_source_file(&canonical_src_dir.join(&filename))?;
    let canonical_dst_dir = ensure_destination_project_dir(
        &canonical_dst_root.join(project_slug),
        &canonical_dst_root,
    )?;
    let dst_file = canonical_dst_dir.join(filename);

    match read_existing_destination(&dst_file)? {
        Some(existing) if existing == src_bytes => {
            set_file_private(&dst_file)?;
            Ok(CopyOutcome::UnchangedSkipped)
        }
        Some(_) if force_overwrite => {
            write_atomic_replace(&dst_file, &src_bytes)?;
            Ok(CopyOutcome::Overwritten)
        }
        Some(_) => {
            set_file_private(&dst_file)?;
            Ok(CopyOutcome::ConflictDifferentContent)
        }
        None => match write_atomic_create_new(&dst_file, &src_bytes) {
            Ok(()) => Ok(CopyOutcome::Copied),
            Err(SessionCopyError::DestinationAlreadyExists { .. }) => {
                match read_existing_destination(&dst_file)? {
                    Some(existing) if existing == src_bytes => {
                        set_file_private(&dst_file)?;
                        Ok(CopyOutcome::UnchangedSkipped)
                    }
                    Some(_) => {
                        set_file_private(&dst_file)?;
                        Ok(CopyOutcome::ConflictDifferentContent)
                    }
                    None => Err(SessionCopyError::DestinationChanged {
                        path: dst_file.to_path_buf(),
                    }),
                }
            }
            Err(e) => Err(e),
        },
    }
}

#[derive(Debug, Clone, Copy)]
enum ComponentKind {
    ProjectSlug,
    SessionId,
}

impl ComponentKind {
    fn error(self, value: &str, reason: &'static str) -> SessionCopyError {
        match self {
            Self::ProjectSlug => SessionCopyError::InvalidProjectSlug {
                value: value.to_owned(),
                reason,
            },
            Self::SessionId => SessionCopyError::InvalidSessionId {
                value: value.to_owned(),
                reason,
            },
        }
    }
}

#[derive(Debug)]
enum SessionCopyError {
    InvalidProjectSlug {
        value: String,
        reason: &'static str,
    },
    InvalidSessionId {
        value: String,
        reason: &'static str,
    },
    DestinationRootMissing {
        path: PathBuf,
    },
    SourceMissing {
        path: PathBuf,
    },
    SourceChanged {
        path: PathBuf,
    },
    SourceIncomplete {
        path: PathBuf,
    },
    DestinationAlreadyExists {
        path: PathBuf,
    },
    DestinationChanged {
        path: PathBuf,
    },
    NotDirectory {
        kind: &'static str,
        path: PathBuf,
    },
    SymlinkPath {
        kind: &'static str,
        path: PathBuf,
    },
    EscapesRoot {
        kind: &'static str,
        path: PathBuf,
        root: PathBuf,
    },
    NotRegularFile {
        kind: &'static str,
        path: PathBuf,
    },
    FileTooLarge {
        kind: &'static str,
        path: PathBuf,
        len: u64,
        max: u64,
    },
    Io {
        kind: &'static str,
        path: PathBuf,
        source: io::Error,
    },
}

impl fmt::Display for SessionCopyError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidProjectSlug { value, reason } => write!(
                f,
                "session_copy: InvalidProjectSlug: rejected {value:?}: {reason}"
            ),
            Self::InvalidSessionId { value, reason } => write!(
                f,
                "session_copy: InvalidSessionId: rejected {value:?}: {reason}"
            ),
            Self::DestinationRootMissing { path } => write!(
                f,
                "session_copy: DestinationRootMissing: dst_root must already exist: {}",
                path.display()
            ),
            Self::SourceMissing { path } => write!(
                f,
                "session_copy: SourceMissing: source session not found at {}",
                path.display()
            ),
            Self::SourceChanged { path } => write!(
                f,
                "session_copy: SourceChanged: source changed while being copied: {}",
                path.display()
            ),
            Self::SourceIncomplete { path } => write!(
                f,
                "session_copy: SourceIncomplete: source JSONL does not end with newline: {}",
                path.display()
            ),
            Self::DestinationAlreadyExists { path } => write!(
                f,
                "session_copy: DestinationAlreadyExists: destination appeared during copy: {}",
                path.display()
            ),
            Self::DestinationChanged { path } => write!(
                f,
                "session_copy: DestinationChanged: destination changed during copy: {}",
                path.display()
            ),
            Self::NotDirectory { kind, path } => {
                write!(
                    f,
                    "session_copy: {kind}: not a directory: {}",
                    path.display()
                )
            }
            Self::SymlinkPath { kind, path } => {
                write!(
                    f,
                    "session_copy: {kind}: symlink rejected: {}",
                    path.display()
                )
            }
            Self::EscapesRoot { kind, path, root } => write!(
                f,
                "session_copy: {kind}: {} escapes root {}",
                path.display(),
                root.display()
            ),
            Self::NotRegularFile { kind, path } => write!(
                f,
                "session_copy: {kind}: not a regular file: {}",
                path.display()
            ),
            Self::FileTooLarge {
                kind,
                path,
                len,
                max,
            } => write!(
                f,
                "session_copy: {kind}: {} is {len} bytes, max {max}",
                path.display()
            ),
            Self::Io { kind, path, source } => {
                write!(f, "session_copy: {kind}: {}: {source}", path.display())
            }
        }
    }
}

fn validate_component(kind: ComponentKind, value: &str) -> Result<(), SessionCopyError> {
    if value.is_empty() {
        return Err(kind.error(value, "component is empty"));
    }
    if value == "." || value == ".." {
        return Err(kind.error(value, "dot components are not allowed"));
    }
    if value.contains('/') || value.contains('\\') {
        return Err(kind.error(value, "path separators are not allowed"));
    }
    if value.contains('\0') {
        return Err(kind.error(value, "NUL bytes are not allowed"));
    }
    if Path::new(value).is_absolute() {
        return Err(kind.error(value, "absolute paths are not allowed"));
    }

    let mut components = Path::new(value).components();
    match (components.next(), components.next()) {
        (Some(Component::Normal(_)), None) => Ok(()),
        _ => Err(kind.error(value, "must be one normal path component")),
    }
}

fn canonicalize_dst_root(path: &Path) -> Result<PathBuf, SessionCopyError> {
    let canonical = match fs::canonicalize(path) {
        Ok(path) => path,
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            return Err(SessionCopyError::DestinationRootMissing {
                path: path.to_path_buf(),
            })
        }
        Err(source) => {
            return Err(SessionCopyError::Io {
                kind: "DestinationRootCanonicalize",
                path: path.to_path_buf(),
                source,
            })
        }
    };
    ensure_directory(&canonical, "DestinationRootNotDirectory")?;
    Ok(canonical)
}

fn canonicalize_existing_dir(path: &Path, kind: &'static str) -> Result<PathBuf, SessionCopyError> {
    let canonical = fs::canonicalize(path).map_err(|source| SessionCopyError::Io {
        kind: "CanonicalizeDirectory",
        path: path.to_path_buf(),
        source,
    })?;
    ensure_directory(&canonical, kind)?;
    Ok(canonical)
}

fn canonicalize_source_dir(src_dir: &Path, src_file: &Path) -> Result<PathBuf, SessionCopyError> {
    let canonical = match fs::canonicalize(src_dir) {
        Ok(path) => path,
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            return Err(SessionCopyError::SourceMissing {
                path: src_file.to_path_buf(),
            })
        }
        Err(source) => {
            return Err(SessionCopyError::Io {
                kind: "SourceProjectCanonicalize",
                path: src_dir.to_path_buf(),
                source,
            })
        }
    };
    ensure_directory(&canonical, "SourceProjectNotDirectory")?;
    Ok(canonical)
}

fn ensure_directory(path: &Path, kind: &'static str) -> Result<(), SessionCopyError> {
    let meta = fs::metadata(path).map_err(|source| SessionCopyError::Io {
        kind: "InspectDirectory",
        path: path.to_path_buf(),
        source,
    })?;
    if meta.is_dir() {
        Ok(())
    } else {
        Err(SessionCopyError::NotDirectory {
            kind,
            path: path.to_path_buf(),
        })
    }
}

fn require_under(kind: &'static str, path: &Path, root: &Path) -> Result<(), SessionCopyError> {
    if path.starts_with(root) {
        Ok(())
    } else {
        Err(SessionCopyError::EscapesRoot {
            kind,
            path: path.to_path_buf(),
            root: root.to_path_buf(),
        })
    }
}

fn ensure_destination_project_dir(
    dst_dir: &Path,
    canonical_dst_root: &Path,
) -> Result<PathBuf, SessionCopyError> {
    match fs::symlink_metadata(dst_dir) {
        Ok(meta) => validate_destination_dir_metadata(dst_dir, &meta)?,
        Err(e) if e.kind() == io::ErrorKind::NotFound => match fs::create_dir(dst_dir) {
            Ok(()) => set_dir_private(dst_dir)?,
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {}
            Err(source) => {
                return Err(SessionCopyError::Io {
                    kind: "DestinationProjectCreate",
                    path: dst_dir.to_path_buf(),
                    source,
                })
            }
        },
        Err(source) => {
            return Err(SessionCopyError::Io {
                kind: "DestinationProjectInspect",
                path: dst_dir.to_path_buf(),
                source,
            })
        }
    }

    let meta = fs::symlink_metadata(dst_dir).map_err(|source| SessionCopyError::Io {
        kind: "DestinationProjectInspect",
        path: dst_dir.to_path_buf(),
        source,
    })?;
    validate_destination_dir_metadata(dst_dir, &meta)?;

    let canonical = canonicalize_existing_dir(dst_dir, "DestinationProjectNotDirectory")?;
    require_under("DestinationEscapesRoot", &canonical, canonical_dst_root)?;
    set_dir_private(&canonical)?;
    Ok(canonical)
}

fn validate_destination_dir_metadata(
    path: &Path,
    meta: &fs::Metadata,
) -> Result<(), SessionCopyError> {
    if meta.file_type().is_symlink() {
        return Err(SessionCopyError::SymlinkPath {
            kind: "DestinationProjectSymlink",
            path: path.to_path_buf(),
        });
    }
    if meta.is_dir() {
        Ok(())
    } else {
        Err(SessionCopyError::NotDirectory {
            kind: "DestinationProjectNotDirectory",
            path: path.to_path_buf(),
        })
    }
}

fn read_source_file(path: &Path) -> Result<Vec<u8>, SessionCopyError> {
    let before = source_file_metadata(path)?;
    validate_regular_file("SourceNotRegular", path, &before)?;
    validate_size("SourceTooLarge", path, before.len())?;

    let file = File::open(path).map_err(|source| match source.kind() {
        io::ErrorKind::NotFound => SessionCopyError::SourceMissing {
            path: path.to_path_buf(),
        },
        _ => SessionCopyError::Io {
            kind: "SourceOpen",
            path: path.to_path_buf(),
            source,
        },
    })?;
    let opened = file.metadata().map_err(|source| SessionCopyError::Io {
        kind: "SourceInspectOpened",
        path: path.to_path_buf(),
        source,
    })?;
    validate_regular_file("SourceNotRegular", path, &opened)?;
    validate_size("SourceTooLarge", path, opened.len())?;
    if metadata_changed(&before, &opened) {
        return Err(SessionCopyError::SourceChanged {
            path: path.to_path_buf(),
        });
    }

    let mut bytes = Vec::with_capacity(opened.len() as usize);
    file.take(MAX_TRANSCRIPT_BYTES + 1)
        .read_to_end(&mut bytes)
        .map_err(|source| SessionCopyError::Io {
            kind: "SourceRead",
            path: path.to_path_buf(),
            source,
        })?;
    validate_size("SourceTooLarge", path, bytes.len() as u64)?;

    let after = source_file_metadata(path)?;
    validate_regular_file("SourceNotRegular", path, &after)?;
    if metadata_changed(&before, &after) || after.len() != bytes.len() as u64 {
        return Err(SessionCopyError::SourceChanged {
            path: path.to_path_buf(),
        });
    }
    if !bytes.is_empty() && !bytes.ends_with(b"\n") {
        return Err(SessionCopyError::SourceIncomplete {
            path: path.to_path_buf(),
        });
    }

    Ok(bytes)
}

fn source_file_metadata(path: &Path) -> Result<fs::Metadata, SessionCopyError> {
    fs::symlink_metadata(path).map_err(|source| match source.kind() {
        io::ErrorKind::NotFound => SessionCopyError::SourceMissing {
            path: path.to_path_buf(),
        },
        _ => SessionCopyError::Io {
            kind: "SourceInspect",
            path: path.to_path_buf(),
            source,
        },
    })
}

fn read_existing_destination(path: &Path) -> Result<Option<Vec<u8>>, SessionCopyError> {
    let before = match fs::symlink_metadata(path) {
        Ok(meta) => meta,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(source) => {
            return Err(SessionCopyError::Io {
                kind: "DestinationInspect",
                path: path.to_path_buf(),
                source,
            })
        }
    };
    validate_regular_file("DestinationNotRegular", path, &before)?;
    validate_size("DestinationTooLarge", path, before.len())?;

    let file = match File::open(path) {
        Ok(file) => file,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(source) => {
            return Err(SessionCopyError::Io {
                kind: "DestinationOpen",
                path: path.to_path_buf(),
                source,
            })
        }
    };
    let opened = file.metadata().map_err(|source| SessionCopyError::Io {
        kind: "DestinationInspectOpened",
        path: path.to_path_buf(),
        source,
    })?;
    validate_regular_file("DestinationNotRegular", path, &opened)?;
    validate_size("DestinationTooLarge", path, opened.len())?;
    if metadata_changed(&before, &opened) {
        return Err(SessionCopyError::DestinationChanged {
            path: path.to_path_buf(),
        });
    }

    let mut bytes = Vec::with_capacity(opened.len() as usize);
    file.take(MAX_TRANSCRIPT_BYTES + 1)
        .read_to_end(&mut bytes)
        .map_err(|source| SessionCopyError::Io {
            kind: "DestinationRead",
            path: path.to_path_buf(),
            source,
        })?;
    validate_size("DestinationTooLarge", path, bytes.len() as u64)?;
    Ok(Some(bytes))
}

fn validate_regular_file(
    kind: &'static str,
    path: &Path,
    meta: &fs::Metadata,
) -> Result<(), SessionCopyError> {
    if meta.file_type().is_symlink() {
        return Err(SessionCopyError::SymlinkPath {
            kind,
            path: path.to_path_buf(),
        });
    }
    if meta.is_file() {
        Ok(())
    } else {
        Err(SessionCopyError::NotRegularFile {
            kind,
            path: path.to_path_buf(),
        })
    }
}

fn validate_size(kind: &'static str, path: &Path, len: u64) -> Result<(), SessionCopyError> {
    if len <= MAX_TRANSCRIPT_BYTES {
        Ok(())
    } else {
        Err(SessionCopyError::FileTooLarge {
            kind,
            path: path.to_path_buf(),
            len,
            max: MAX_TRANSCRIPT_BYTES,
        })
    }
}

fn metadata_changed(before: &fs::Metadata, after: &fs::Metadata) -> bool {
    if before.len() != after.len() {
        return true;
    }
    #[cfg(unix)]
    if before.dev() != after.dev() || before.ino() != after.ino() {
        return true;
    }
    match (before.modified(), after.modified()) {
        (Ok(a), Ok(b)) => a != b,
        _ => false,
    }
}

fn write_atomic_create_new(dst: &Path, bytes: &[u8]) -> Result<(), SessionCopyError> {
    let tmp = write_unique_temp(dst, bytes)?;
    match fs::hard_link(&tmp, dst) {
        Ok(()) => {
            cleanup_temp(&tmp);
            set_file_private(dst)
        }
        Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {
            cleanup_temp(&tmp);
            Err(SessionCopyError::DestinationAlreadyExists {
                path: dst.to_path_buf(),
            })
        }
        Err(source) => {
            cleanup_temp(&tmp);
            Err(SessionCopyError::Io {
                kind: "DestinationInstallNoReplace",
                path: dst.to_path_buf(),
                source,
            })
        }
    }
}

fn write_atomic_replace(dst: &Path, bytes: &[u8]) -> Result<(), SessionCopyError> {
    let tmp = write_unique_temp(dst, bytes)?;
    match fs::rename(&tmp, dst) {
        Ok(()) => set_file_private(dst),
        Err(source) => {
            cleanup_temp(&tmp);
            Err(SessionCopyError::Io {
                kind: "DestinationInstallReplace",
                path: dst.to_path_buf(),
                source,
            })
        }
    }
}

fn write_unique_temp(dst: &Path, bytes: &[u8]) -> Result<PathBuf, SessionCopyError> {
    let parent = dst.parent().ok_or_else(|| SessionCopyError::Io {
        kind: "DestinationParent",
        path: dst.to_path_buf(),
        source: io::Error::new(io::ErrorKind::InvalidInput, "destination has no parent"),
    })?;
    let name = dst
        .file_name()
        .ok_or_else(|| SessionCopyError::Io {
            kind: "DestinationFilename",
            path: dst.to_path_buf(),
            source: io::Error::new(io::ErrorKind::InvalidInput, "destination has no filename"),
        })?
        .to_string_lossy();

    for attempt in 0..100 {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let tmp = parent.join(format!(
            ".{name}.{}.{}.{}.tmp",
            std::process::id(),
            nanos,
            attempt
        ));
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        options.mode(0o600);

        let mut file = match options.open(&tmp) {
            Ok(file) => file,
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(source) => {
                return Err(SessionCopyError::Io {
                    kind: "TempCreate",
                    path: tmp,
                    source,
                })
            }
        };

        if let Err(source) = file.write_all(bytes) {
            cleanup_temp(&tmp);
            return Err(SessionCopyError::Io {
                kind: "TempWrite",
                path: tmp,
                source,
            });
        }
        if let Err(source) = file.sync_all() {
            cleanup_temp(&tmp);
            return Err(SessionCopyError::Io {
                kind: "TempSync",
                path: tmp,
                source,
            });
        }
        set_file_private(&tmp)?;
        return Ok(tmp);
    }

    Err(SessionCopyError::Io {
        kind: "TempCreate",
        path: dst.to_path_buf(),
        source: io::Error::new(io::ErrorKind::AlreadyExists, "unique temp name exhausted"),
    })
}

fn cleanup_temp(path: &Path) {
    let _ = fs::remove_file(path);
}

#[cfg(unix)]
fn set_dir_private(path: &Path) -> Result<(), SessionCopyError> {
    fs::set_permissions(path, fs::Permissions::from_mode(0o700)).map_err(|source| {
        SessionCopyError::Io {
            kind: "DestinationDirChmod",
            path: path.to_path_buf(),
            source,
        }
    })
}

#[cfg(not(unix))]
fn set_dir_private(_path: &Path) -> Result<(), SessionCopyError> {
    Ok(())
}

#[cfg(unix)]
fn set_file_private(path: &Path) -> Result<(), SessionCopyError> {
    fs::set_permissions(path, fs::Permissions::from_mode(0o600)).map_err(|source| {
        SessionCopyError::Io {
            kind: "DestinationFileChmod",
            path: path.to_path_buf(),
            source,
        }
    })
}

#[cfg(not(unix))]
fn set_file_private(_path: &Path) -> Result<(), SessionCopyError> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn write_session(root: &Path, slug: &str, session_id: &str, content: &str) {
        let dir = root.join(slug);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join(format!("{session_id}.jsonl")), content).unwrap();
    }

    #[test]
    fn copies_fresh_when_destination_dir_missing() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-1", "{\"line\":1}\n");

        // Destination project dir does not exist yet.
        let dst_root = dst.path().join("projects");
        std::fs::create_dir(&dst_root).unwrap();
        let outcome = copy_session(src.path(), &dst_root, "proj", "sess-1").unwrap();
        assert_eq!(outcome, CopyOutcome::Copied);

        let copied = std::fs::read_to_string(dst_root.join("proj/sess-1.jsonl")).unwrap();
        assert_eq!(copied, "{\"line\":1}\n");

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;

            let dir_mode = std::fs::metadata(dst_root.join("proj"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777;
            let file_mode = std::fs::metadata(dst_root.join("proj/sess-1.jsonl"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777;
            assert_eq!(dir_mode, 0o700);
            assert_eq!(file_mode, 0o600);
        }
    }

    #[test]
    fn second_copy_of_identical_content_is_skipped() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-2", "{\"a\":1}\n");

        let first = copy_session(src.path(), dst.path(), "proj", "sess-2").unwrap();
        assert_eq!(first, CopyOutcome::Copied);

        let second = copy_session(src.path(), dst.path(), "proj", "sess-2").unwrap();
        assert_eq!(second, CopyOutcome::UnchangedSkipped);
    }

    #[test]
    fn conflicts_when_destination_content_differs() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-3", "{\"a\":1}\n");
        write_session(dst.path(), "proj", "sess-3", "{\"a\":0}\n");

        let outcome = copy_session(src.path(), dst.path(), "proj", "sess-3").unwrap();
        assert_eq!(outcome, CopyOutcome::ConflictDifferentContent);
        let now = std::fs::read_to_string(dst.path().join("proj/sess-3.jsonl")).unwrap();
        assert_eq!(now, "{\"a\":0}\n");
    }

    #[test]
    fn force_path_overwrites_when_destination_content_differs() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-force", "{\"a\":1}\n");
        write_session(dst.path(), "proj", "sess-force", "{\"a\":0}\n");

        let outcome =
            copy_session_inner(src.path(), dst.path(), "proj", "sess-force", true).unwrap();
        assert_eq!(outcome, CopyOutcome::Overwritten);
        let now = std::fs::read_to_string(dst.path().join("proj/sess-force.jsonl")).unwrap();
        assert_eq!(now, "{\"a\":1}\n");
    }

    #[test]
    fn errors_when_source_session_missing() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        let err = copy_session(src.path(), dst.path(), "proj", "does-not-exist").unwrap_err();
        assert!(err.contains("source session not found"), "err: {err}");
    }

    #[test]
    fn errors_when_destination_root_missing() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-missing-root", "{\"a\":1}\n");

        let err = copy_session(
            src.path(),
            &dst.path().join("missing-projects-root"),
            "proj",
            "sess-missing-root",
        )
        .unwrap_err();
        assert!(err.contains("DestinationRootMissing"), "err: {err}");
    }

    #[test]
    fn rejects_unsafe_project_slug_components() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();

        for slug in ["", ".", "..", "a/b", "a\\b", "/absolute", "nul\0byte"] {
            let err = copy_session(src.path(), dst.path(), slug, "sess-safe").unwrap_err();
            assert!(
                err.contains("InvalidProjectSlug"),
                "slug={slug:?} err={err}"
            );
        }
    }

    #[test]
    fn rejects_unsafe_session_id_components() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();

        for session_id in ["", ".", "..", "a/b", "a\\b", "/absolute", "nul\0byte"] {
            let err = copy_session(src.path(), dst.path(), "proj", session_id).unwrap_err();
            assert!(
                err.contains("InvalidSessionId"),
                "session_id={session_id:?} err={err}"
            );
        }
    }

    #[test]
    fn rejects_non_regular_source_file() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        std::fs::create_dir_all(src.path().join("proj/sess-dir.jsonl")).unwrap();

        let err = copy_session(src.path(), dst.path(), "proj", "sess-dir").unwrap_err();
        assert!(err.contains("SourceNotRegular"), "err: {err}");
    }

    #[test]
    fn rejects_non_regular_destination_file() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-dst-dir", "{\"a\":1}\n");
        std::fs::create_dir_all(dst.path().join("proj/sess-dst-dir.jsonl")).unwrap();

        let err = copy_session(src.path(), dst.path(), "proj", "sess-dst-dir").unwrap_err();
        assert!(err.contains("DestinationNotRegular"), "err: {err}");
    }

    #[test]
    fn rejects_incomplete_source_jsonl() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-open", "{\"a\":1}");

        let err = copy_session(src.path(), dst.path(), "proj", "sess-open").unwrap_err();
        assert!(err.contains("SourceIncomplete"), "err: {err}");
    }

    #[cfg(unix)]
    #[test]
    fn symlinked_projects_root_is_treated_as_already_shared() {
        use std::os::unix::fs::symlink;

        let src = TempDir::new().unwrap();
        let base = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-4", "{\"shared\":true}\n");

        // Reproduce the verified real-world case: dst's `projects` root is a
        // symlink pointing at src's `projects` root.
        let dst_link = base.path().join("projects-symlink");
        symlink(src.path(), &dst_link).unwrap();

        let outcome = copy_session(src.path(), &dst_link, "proj", "sess-4").unwrap();
        assert_eq!(outcome, CopyOutcome::AlreadySharedStorage);

        // No file should have been created/duplicated at the destination
        // beyond what already existed through the symlink itself.
        assert!(dst_link.join("proj/sess-4.jsonl").exists());
    }

    #[test]
    fn non_symlinked_distinct_roots_are_not_treated_as_shared() {
        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-5", "{\"x\":1}\n");
        write_session(dst.path(), "proj", "sess-5", "{\"x\":1}\n");

        // Two independent (non-symlinked) dirs that happen to hold identical
        // content must NOT be reported as shared storage — only a true
        // canonical-path match counts.
        let outcome = copy_session(src.path(), dst.path(), "proj", "sess-5").unwrap();
        assert_eq!(outcome, CopyOutcome::UnchangedSkipped);
    }

    #[cfg(unix)]
    #[test]
    fn rejects_source_file_symlink() {
        use std::os::unix::fs::symlink;

        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        let outside = TempDir::new().unwrap();
        std::fs::create_dir_all(src.path().join("proj")).unwrap();
        std::fs::write(outside.path().join("real.jsonl"), "{\"a\":1}\n").unwrap();
        symlink(
            outside.path().join("real.jsonl"),
            src.path().join("proj/sess-link.jsonl"),
        )
        .unwrap();

        let err = copy_session(src.path(), dst.path(), "proj", "sess-link").unwrap_err();
        assert!(err.contains("SourceNotRegular"), "err: {err}");
    }

    #[cfg(unix)]
    #[test]
    fn rejects_destination_project_symlink_escape() {
        use std::os::unix::fs::symlink;

        let src = TempDir::new().unwrap();
        let dst = TempDir::new().unwrap();
        let outside = TempDir::new().unwrap();
        write_session(src.path(), "proj", "sess-escape", "{\"a\":1}\n");
        symlink(outside.path(), dst.path().join("proj")).unwrap();

        let err = copy_session(src.path(), dst.path(), "proj", "sess-escape").unwrap_err();
        assert!(err.contains("DestinationProjectSymlink"), "err: {err}");
        assert!(!outside.path().join("sess-escape.jsonl").exists());
    }
}
