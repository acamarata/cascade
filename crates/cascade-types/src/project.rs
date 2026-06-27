//! Project discovery types — taxonomy, records, and index status.
//!
//! Purpose: canonical shared types for the project registry (rag-13). Consumed
//! by `cascade-daemon::discovery` (scanner + registry) and any future CLI or
//! MCP surface that queries the project index.
//!
//! Inputs:  directory paths, marker-file presence (determined by the classifier)
//! Outputs: `ProjectRecord` values stored in the SQLite registry
//! Constraints:
//!   - Zero vendor coupling — no model names, no provider SDKs.
//!   - All types must be `serde`-serialisable so they round-trip through JSON
//!     and SQLite TEXT columns without loss.
//!
//! SPORT: cascade-types / project module (rag-13)

use serde::{Deserialize, Serialize};
use std::path::PathBuf;

// ── ProjectType ───────────────────────────────────────────────────────────────

/// High-level taxonomy for a discovered project directory.
///
/// Determined by marker-file presence (see `cascade-daemon::discovery::classifier`).
/// `Unknown` is the safe fallback when no marker matches.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum ProjectType {
    /// `Cargo.toml` present at the directory root.
    RustCrate,
    /// `package.json` + a `next.config.*` file present.
    NextApp,
    /// `package.json` + a `astro.config.*` file present.
    AstroSite,
    /// `package.json` + a `vite.config.*` file present (and no next/astro config).
    ViteApp,
    /// `package.json` present without a recognised framework config.
    NodeApp,
    /// `pyproject.toml` (or `setup.py` / `setup.cfg`) present.
    PythonPkg,
    /// `pubspec.yaml` present (Dart / Flutter project).
    DartFlutter,
    /// Multiple sub-projects detected under `apps/` or `packages/` subdirectories.
    Monorepo,
    /// No recognised marker found.
    #[default]
    Unknown,
}

impl std::fmt::Display for ProjectType {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            Self::RustCrate => "rust_crate",
            Self::NextApp => "next_app",
            Self::AstroSite => "astro_site",
            Self::ViteApp => "vite_app",
            Self::NodeApp => "node_app",
            Self::PythonPkg => "python_pkg",
            Self::DartFlutter => "dart_flutter",
            Self::Monorepo => "monorepo",
            Self::Unknown => "unknown",
        };
        f.write_str(s)
    }
}

impl std::str::FromStr for ProjectType {
    type Err = ();

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Ok(match s {
            "rust_crate" => Self::RustCrate,
            "next_app" => Self::NextApp,
            "astro_site" => Self::AstroSite,
            "vite_app" => Self::ViteApp,
            "node_app" => Self::NodeApp,
            "python_pkg" => Self::PythonPkg,
            "dart_flutter" => Self::DartFlutter,
            "monorepo" => Self::Monorepo,
            _ => Self::Unknown,
        })
    }
}

// ── IndexStatus ───────────────────────────────────────────────────────────────

/// Lifecycle state of a project's RAG index within the registry.
///
/// Transitions: `Pending → Indexing → Ready` (happy path) or `→ Error(msg)`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case", tag = "status", content = "detail")]
pub enum IndexStatus {
    /// Registered but indexing has not started.
    #[default]
    Pending,
    /// Currently being indexed by the daemon's indexing pipeline.
    Indexing,
    /// Index is up-to-date and queryable.
    Ready,
    /// Indexing failed; `String` contains the error message.
    Error(String),
}

impl std::fmt::Display for IndexStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Pending => f.write_str("pending"),
            Self::Indexing => f.write_str("indexing"),
            Self::Ready => f.write_str("ready"),
            Self::Error(msg) => write!(f, "error:{msg}"),
        }
    }
}

impl IndexStatus {
    /// Reconstruct from the stored string representation.
    ///
    /// Format: `"pending"`, `"indexing"`, `"ready"`, or `"error:<message>"`.
    pub fn from_stored(s: &str) -> Self {
        match s {
            "pending" => Self::Pending,
            "indexing" => Self::Indexing,
            "ready" => Self::Ready,
            other => {
                let msg = other.strip_prefix("error:").unwrap_or(other).to_owned();
                Self::Error(msg)
            }
        }
    }
}

// ── ProjectRecord ─────────────────────────────────────────────────────────────

/// A single discovered project entry in the registry.
///
/// The `id` is a stable opaque string (typically a hash of `root`). The
/// `parent_id` field links monorepo sub-projects back to their root.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProjectRecord {
    /// Stable unique identifier for this project.
    ///
    /// WHY: a hash of the canonical root path keeps IDs stable across restarts
    /// while avoiding path-length issues in foreign key references.
    pub id: String,

    /// Absolute path to the project root directory.
    pub root: PathBuf,

    /// Human-readable project name (typically the directory's base name).
    pub name: String,

    /// Coarse project taxonomy determined by marker-file classification.
    pub project_type: ProjectType,

    /// Primary programming language (e.g. `"rust"`, `"typescript"`, `"python"`).
    /// `None` when not determinable from markers alone.
    pub language: Option<String>,

    /// Framework or runtime (e.g. `"next"`, `"vite"`, `"astro"`, `"flutter"`).
    /// `None` for language-only projects.
    pub framework: Option<String>,

    /// Package manager detected from lock-file presence (`"pnpm"`, `"npm"`, `"yarn"`, etc.).
    /// `None` for non-JS projects or when no lock file is found.
    pub package_manager: Option<String>,

    /// ID of the parent project when this record is a monorepo sub-project.
    /// `None` for top-level projects.
    pub parent_id: Option<String>,

    /// Current indexing state for this project's RAG index.
    pub index_status: IndexStatus,
}

impl ProjectRecord {
    /// Derive a stable `id` from a canonical root path.
    ///
    /// WHY: Using a short hex hash of the path string keeps IDs URL-safe and
    /// avoids embedding raw paths (which can contain spaces or non-ASCII chars)
    /// in foreign keys.
    pub fn id_for(root: &std::path::Path) -> String {
        use std::collections::hash_map::DefaultHasher;
        use std::hash::{Hash, Hasher};
        let mut h = DefaultHasher::new();
        root.hash(&mut h);
        format!("{:016x}", h.finish())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    #[test]
    fn project_type_display_roundtrip() {
        let cases = [
            ProjectType::RustCrate,
            ProjectType::NextApp,
            ProjectType::AstroSite,
            ProjectType::ViteApp,
            ProjectType::NodeApp,
            ProjectType::PythonPkg,
            ProjectType::DartFlutter,
            ProjectType::Monorepo,
            ProjectType::Unknown,
        ];
        for pt in &cases {
            let s = pt.to_string();
            let back: ProjectType = s.parse().unwrap();
            assert_eq!(pt, &back, "roundtrip failed for {s}");
        }
    }

    #[test]
    fn index_status_stored_roundtrip() {
        let cases = [
            IndexStatus::Pending,
            IndexStatus::Indexing,
            IndexStatus::Ready,
            IndexStatus::Error("some failure".into()),
        ];
        for status in &cases {
            let stored = status.to_string();
            let back = IndexStatus::from_stored(&stored);
            assert_eq!(status, &back, "roundtrip failed for {stored}");
        }
    }

    #[test]
    fn id_for_is_deterministic() {
        let path = Path::new("/Users/test/Sites/myproject");
        let id1 = ProjectRecord::id_for(path);
        let id2 = ProjectRecord::id_for(path);
        assert_eq!(id1, id2);
        assert_eq!(id1.len(), 16, "id must be 16 hex chars");
    }

    #[test]
    fn project_record_serde_roundtrip() {
        let rec = ProjectRecord {
            id: "abc123".into(),
            root: PathBuf::from("/home/user/myapp"),
            name: "myapp".into(),
            project_type: ProjectType::ViteApp,
            language: Some("typescript".into()),
            framework: Some("vite".into()),
            package_manager: Some("pnpm".into()),
            parent_id: None,
            index_status: IndexStatus::Ready,
        };
        let json = serde_json::to_string(&rec).expect("serialize");
        let back: ProjectRecord = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(back.name, rec.name);
        assert_eq!(back.project_type, rec.project_type);
        assert_eq!(back.index_status, rec.index_status);
    }
}
