//! Project registry — SQLite-backed persistence for discovered `ProjectRecord`s.
//!
//! Purpose: maintain a `projects` table in `registry.db` so that project
//! discovery results survive daemon restarts and can be queried without
//! re-scanning the filesystem.
//!
//! Inputs:  `ProjectRecord` values produced by the scanner / classifier
//! Outputs: `Vec<ProjectRecord>` via `list()`, `Option<ProjectRecord>` via `get()`
//! Constraints:
//!   - Uses `cascade_db::open_configured` for consistent PRAGMA settings.
//!   - Schema is created with `CREATE TABLE IF NOT EXISTS` on `ProjectRegistry::open`.
//!   - All optional string fields are stored as nullable TEXT; enums stored as
//!     their `Display` string for readability.
//!   - `upsert` is an `INSERT OR REPLACE` keyed on `id`.
//!
//! SPORT: cascade-daemon / discovery / registry (rag-13)

use cascade_db::{db_files, open_configured, CascadeDb};
use cascade_types::project::{IndexStatus, ProjectRecord, ProjectType};
use rusqlite::{params, Connection};
use std::path::{Path, PathBuf};
use std::str::FromStr;

// ── ProjectRegistry ───────────────────────────────────────────────────────────

/// SQLite-backed store for `ProjectRecord`s.
///
/// Opens (or creates) `registry.db` in the given data root and ensures the
/// `projects` schema is present. All methods take `&mut self` because the
/// underlying `rusqlite::Connection` is not `Sync`.
pub struct ProjectRegistry {
    conn: Connection,
}

impl ProjectRegistry {
    /// Open the registry at the standard path inside `data_root`.
    ///
    /// `data_root` is typically `~/.cascade/` (or `$CASCADE_INDEX_ROOT`).
    /// Creates `data_root` if it does not exist.
    pub fn open(data_root: &Path) -> Result<Self, RegistryError> {
        std::fs::create_dir_all(data_root).map_err(|e| RegistryError::Io(e.to_string()))?;
        let db_path = data_root.join(db_files::REGISTRY);
        let conn = open_configured(&db_path).map_err(|e| RegistryError::Db(e.to_string()))?;
        let reg = Self { conn };
        reg.ensure_schema()?;
        Ok(reg)
    }

    /// Open via a `CascadeDb` handle (convenience wrapper).
    // Convenience constructor — called by daemon components that hold a CascadeDb handle.
    #[allow(dead_code)]
    pub fn open_via(db: &CascadeDb) -> Result<Self, RegistryError> {
        Self::open(db.root())
    }

    /// Open directly from a full path to the database file (used in tests).
    // Test-facing constructor — allows opening at an arbitrary path without a full CascadeDb.
    #[allow(dead_code)]
    pub fn open_file(db_path: &Path) -> Result<Self, RegistryError> {
        let conn = open_configured(db_path).map_err(|e| RegistryError::Db(e.to_string()))?;
        let reg = Self { conn };
        reg.ensure_schema()?;
        Ok(reg)
    }

    // ── Schema ────────────────────────────────────────────────────────────────

    fn ensure_schema(&self) -> Result<(), RegistryError> {
        self.conn
            .execute_batch(
                "CREATE TABLE IF NOT EXISTS projects (
                    id              TEXT PRIMARY KEY,
                    root            TEXT NOT NULL,
                    name            TEXT NOT NULL,
                    project_type    TEXT NOT NULL,
                    language        TEXT,
                    framework       TEXT,
                    package_manager TEXT,
                    parent_id       TEXT,
                    index_status    TEXT NOT NULL DEFAULT 'pending'
                );",
            )
            .map_err(|e| RegistryError::Db(e.to_string()))
    }

    // ── Write operations ──────────────────────────────────────────────────────

    /// Insert or replace a `ProjectRecord`.
    ///
    /// WHY `INSERT OR REPLACE`: keeps the registry idempotent — re-scanning a
    /// directory updates the record rather than erroring on duplicate `id`.
    pub fn upsert(&mut self, rec: &ProjectRecord) -> Result<(), RegistryError> {
        self.conn
            .execute(
                "INSERT OR REPLACE INTO projects
                    (id, root, name, project_type, language, framework,
                     package_manager, parent_id, index_status)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)",
                params![
                    rec.id,
                    rec.root.to_string_lossy().as_ref(),
                    rec.name,
                    rec.project_type.to_string(),
                    rec.language,
                    rec.framework,
                    rec.package_manager,
                    rec.parent_id,
                    rec.index_status.to_string(),
                ],
            )
            .map_err(|e| RegistryError::Db(e.to_string()))?;
        Ok(())
    }

    /// Remove a record by `id`. No-op if the record does not exist.
    // CRUD surface — called by project scanner when a project root disappears.
    #[allow(dead_code)]
    pub fn remove(&mut self, id: &str) -> Result<(), RegistryError> {
        self.conn
            .execute("DELETE FROM projects WHERE id = ?1", params![id])
            .map_err(|e| RegistryError::Db(e.to_string()))?;
        Ok(())
    }

    // ── Read operations ───────────────────────────────────────────────────────

    /// Retrieve a single record by `id`.
    // CRUD surface — called by IPC handlers and the project scanner.
    #[allow(dead_code)]
    pub fn get(&self, id: &str) -> Result<Option<ProjectRecord>, RegistryError> {
        let mut stmt = self
            .conn
            .prepare(
                "SELECT id, root, name, project_type, language, framework,
                        package_manager, parent_id, index_status
                 FROM projects WHERE id = ?1",
            )
            .map_err(|e| RegistryError::Db(e.to_string()))?;

        let mut rows = stmt
            .query(params![id])
            .map_err(|e| RegistryError::Db(e.to_string()))?;

        if let Some(row) = rows.next().map_err(|e| RegistryError::Db(e.to_string()))? {
            Ok(Some(row_to_record(row)?))
        } else {
            Ok(None)
        }
    }

    /// Return all records in insertion order (stable across SQLite restarts).
    // CRUD surface — called by `cascade status` and the project discovery IPC handler.
    #[allow(dead_code)]
    pub fn list(&self) -> Result<Vec<ProjectRecord>, RegistryError> {
        let mut stmt = self
            .conn
            .prepare(
                "SELECT id, root, name, project_type, language, framework,
                        package_manager, parent_id, index_status
                 FROM projects ORDER BY rowid",
            )
            .map_err(|e| RegistryError::Db(e.to_string()))?;

        let rows = stmt
            .query_map([], |row| {
                Ok(row_to_record(row)
                    .map_err(|e| rusqlite::Error::InvalidParameterName(e.to_string())))
            })
            .map_err(|e| RegistryError::Db(e.to_string()))?;

        let mut records = Vec::new();
        for row in rows {
            let inner = row.map_err(|e| RegistryError::Db(e.to_string()))?;
            records.push(inner.map_err(|e| RegistryError::Db(e.to_string()))?);
        }
        Ok(records)
    }
}

// ── Row deserialisation helper ────────────────────────────────────────────────

// Private row deserialiser — called by get() and list().
#[allow(dead_code)]
fn row_to_record(row: &rusqlite::Row<'_>) -> Result<ProjectRecord, RegistryError> {
    let root_str: String = row.get(1).map_err(|e| RegistryError::Db(e.to_string()))?;
    let pt_str: String = row.get(3).map_err(|e| RegistryError::Db(e.to_string()))?;
    let is_str: String = row.get(8).map_err(|e| RegistryError::Db(e.to_string()))?;

    Ok(ProjectRecord {
        id: row.get(0).map_err(|e| RegistryError::Db(e.to_string()))?,
        root: PathBuf::from(root_str),
        name: row.get(2).map_err(|e| RegistryError::Db(e.to_string()))?,
        project_type: ProjectType::from_str(&pt_str).unwrap_or(ProjectType::Unknown),
        language: row.get(4).map_err(|e| RegistryError::Db(e.to_string()))?,
        framework: row.get(5).map_err(|e| RegistryError::Db(e.to_string()))?,
        package_manager: row.get(6).map_err(|e| RegistryError::Db(e.to_string()))?,
        parent_id: row.get(7).map_err(|e| RegistryError::Db(e.to_string()))?,
        index_status: IndexStatus::from_stored(&is_str),
    })
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Registry-specific error type.
#[derive(Debug, thiserror::Error)]
pub enum RegistryError {
    #[error("registry I/O error: {0}")]
    Io(String),
    #[error("registry database error: {0}")]
    Db(String),
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::project::{IndexStatus, ProjectRecord, ProjectType};
    use tempfile::TempDir;

    fn tempdir() -> TempDir {
        tempfile::tempdir().expect("create tempdir")
    }

    fn sample_record(suffix: &str) -> ProjectRecord {
        let root = PathBuf::from(format!("/home/user/project_{suffix}"));
        ProjectRecord {
            id: ProjectRecord::id_for(&root),
            root: root.clone(),
            name: format!("project_{suffix}"),
            project_type: ProjectType::ViteApp,
            language: Some("typescript".into()),
            framework: Some("vite".into()),
            package_manager: Some("pnpm".into()),
            parent_id: None,
            index_status: IndexStatus::Pending,
        }
    }

    #[test]
    fn upsert_and_get() {
        let d = tempdir();
        let mut reg = ProjectRegistry::open(d.path()).expect("open registry");

        let rec = sample_record("a");
        reg.upsert(&rec).expect("upsert");

        let got = reg.get(&rec.id).expect("get").expect("record exists");
        assert_eq!(got.name, rec.name);
        assert_eq!(got.project_type, ProjectType::ViteApp);
        assert_eq!(got.index_status, IndexStatus::Pending);
    }

    #[test]
    fn upsert_updates_existing_record() {
        let d = tempdir();
        let mut reg = ProjectRegistry::open(d.path()).expect("open registry");

        let mut rec = sample_record("b");
        reg.upsert(&rec).expect("first upsert");

        // Update the status and re-upsert.
        rec.index_status = IndexStatus::Ready;
        reg.upsert(&rec).expect("second upsert");

        let got = reg.get(&rec.id).expect("get").expect("record exists");
        assert_eq!(got.index_status, IndexStatus::Ready);
    }

    #[test]
    fn list_returns_all_records() {
        let d = tempdir();
        let mut reg = ProjectRegistry::open(d.path()).expect("open registry");

        reg.upsert(&sample_record("x")).expect("upsert x");
        reg.upsert(&sample_record("y")).expect("upsert y");
        reg.upsert(&sample_record("z")).expect("upsert z");

        let all = reg.list().expect("list");
        assert_eq!(all.len(), 3);
    }

    #[test]
    fn remove_deletes_record() {
        let d = tempdir();
        let mut reg = ProjectRegistry::open(d.path()).expect("open registry");

        let rec = sample_record("del");
        reg.upsert(&rec).expect("upsert");
        reg.remove(&rec.id).expect("remove");

        let got = reg.get(&rec.id).expect("get");
        assert!(got.is_none(), "record should be gone after remove");
    }

    #[test]
    fn survives_reopen() {
        let d = tempdir();

        // Write in first open.
        {
            let mut reg = ProjectRegistry::open(d.path()).expect("open");
            reg.upsert(&sample_record("persist")).expect("upsert");
        }

        // Read in second open.
        {
            let reg = ProjectRegistry::open(d.path()).expect("reopen");
            let all = reg.list().expect("list");
            assert_eq!(all.len(), 1, "record must survive registry reopen");
            assert_eq!(all[0].name, "project_persist");
        }
    }

    #[test]
    fn get_nonexistent_returns_none() {
        let d = tempdir();
        let reg = ProjectRegistry::open(d.path()).expect("open");
        let got = reg.get("does-not-exist").expect("get");
        assert!(got.is_none());
    }

    #[test]
    fn error_status_roundtrips() {
        let d = tempdir();
        let mut reg = ProjectRegistry::open(d.path()).expect("open");

        let root = PathBuf::from("/tmp/err_proj");
        let rec = ProjectRecord {
            id: ProjectRecord::id_for(&root),
            root,
            name: "err_proj".into(),
            project_type: ProjectType::RustCrate,
            language: Some("rust".into()),
            framework: None,
            package_manager: None,
            parent_id: None,
            index_status: IndexStatus::Error("disk full".into()),
        };
        reg.upsert(&rec).expect("upsert");

        let got = reg.get(&rec.id).expect("get").expect("exists");
        assert_eq!(got.index_status, IndexStatus::Error("disk full".into()));
    }
}
