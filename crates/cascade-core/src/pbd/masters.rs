//! SPORT master list generation — `cascade index <repo-path>`.
//!
//! # Purpose
//! Static analysis of a source repository to produce YAML master lists:
//! - `masters/{repo}.yaml`   — SPORT entity registry (exports, routes, components, endpoints)
//! - `masters/MASTER-PHASES.yaml`  — flattened phase list from the PBD tree
//! - `masters/MASTER-TICKETS.yaml` — flattened ticket list from the PBD tree
//!
//! # Language support
//! - **TypeScript/JavaScript** — scans `src/` for `export` declarations
//!   (`export function`, `export class`, `export const`, `export default`),
//!   Express/Next-style route decorators (GET/POST/PUT/DELETE strings near handler).
//! - **Rust** — scans `src/` for `pub fn`, `pub struct`, `pub enum`, `pub trait`,
//!   `pub type`, `pub use`.
//! - **Fallback** — any other repo: structural scan of `src/` dir listing.
//!
//! # Output format (masters/{repo}.yaml)
//! ```yaml
//! repo: my-repo
//! generated_at: 2026-06-13T00:00:00Z
//! entities:
//!   - kind: export
//!     name: myFunction
//!     file: src/utils.ts
//!   - kind: route
//!     name: GET /api/users
//!     file: src/routes/users.ts
//! ```
//!
//! # Constraints
//! - Idempotent: re-running produces the same output for the same source.
//! - Non-destructive: never deletes or modifies source files.
//! - Written atomically via a tmp file + rename.
//! - Language detection is heuristic — entities may be over/under-reported.
//!   Accuracy is "good enough for SPORT context"; not a replacement for a full AST.

use std::{
    collections::HashSet,
    fs,
    io::{BufRead, BufReader},
    path::{Path, PathBuf},
};

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

use cascade_types::error::{CascadeError, Result};

use super::store::PbdStore;

// ── Public types ───────────────────────────────────────────────────────────────

/// A single tracked entity in a master list.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct MasterEntity {
    /// Kind of entity: `export`, `route`, `component`, `pub_item`, `file`.
    pub kind: String,
    /// Name of the entity (function name, route path, file path, etc.).
    pub name: String,
    /// Source file path relative to repo root.
    pub file: String,
}

/// Root of `masters/{repo}.yaml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MasterList {
    pub repo: String,
    pub generated_at: DateTime<Utc>,
    pub entities: Vec<MasterEntity>,
}

/// Entry in `MASTER-PHASES.yaml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MasterPhaseEntry {
    pub id: String,
    pub title: String,
    pub status: String,
    pub epic_count: usize,
}

/// Root of `MASTER-PHASES.yaml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MasterPhases {
    pub generated_at: DateTime<Utc>,
    pub phases: Vec<MasterPhaseEntry>,
}

/// Entry in `MASTER-TICKETS.yaml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MasterTicketEntry {
    pub id: String,
    pub sprint_id: String,
    pub title: String,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub repo: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub weight: Option<String>,
}

/// Root of `MASTER-TICKETS.yaml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MasterTickets {
    pub generated_at: DateTime<Utc>,
    pub tickets: Vec<MasterTicketEntry>,
}

// ── Entry points ───────────────────────────────────────────────────────────────

/// Generate `masters/{repo-name}.yaml` from static analysis of `repo_path`.
/// `masters_dir` is the directory where the file will be written.
pub fn index_repo(repo_path: &Path, masters_dir: &Path) -> Result<MasterList> {
    let repo_name = repo_path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| "unknown".into());

    let lang = detect_language(repo_path);
    let entities = match lang {
        RepoLanguage::TypeScript => scan_typescript(repo_path),
        RepoLanguage::Rust => scan_rust(repo_path),
        RepoLanguage::Other => scan_structural(repo_path),
    };

    let list = MasterList {
        repo: repo_name.clone(),
        generated_at: Utc::now(),
        entities,
    };

    fs::create_dir_all(masters_dir).map_err(|e| CascadeError::Io {
        path: masters_dir.to_path_buf(),
        operation: "create masters dir",
        source: e,
    })?;

    let out_path = masters_dir.join(format!("{repo_name}.yaml"));
    write_yaml_atomic(&out_path, &list)?;

    Ok(list)
}

/// Generate `MASTER-PHASES.yaml` and `MASTER-TICKETS.yaml` from the PBD tree.
pub fn index_pbd(store: &PbdStore, masters_dir: &Path) -> Result<(MasterPhases, MasterTickets)> {
    let phases = store.list_phases()?;

    let mut phase_entries = Vec::new();
    let mut ticket_entries = Vec::new();

    for phase in &phases {
        let epics = store.list_epics(&phase.id).unwrap_or_default();
        phase_entries.push(MasterPhaseEntry {
            id: phase.id.clone(),
            title: phase.title.clone(),
            status: phase.status.as_str().into(),
            epic_count: epics.len(),
        });

        for epic in &epics {
            let waves = store.list_waves(&phase.id, &epic.id).unwrap_or_default();
            for wave in &waves {
                let sprints = store
                    .list_sprints(&phase.id, &epic.id, &wave.id)
                    .unwrap_or_default();
                for sprint in &sprints {
                    let tickets = store
                        .list_tickets(&phase.id, &epic.id, &wave.id, &sprint.id)
                        .unwrap_or_default();
                    for ticket in tickets {
                        ticket_entries.push(MasterTicketEntry {
                            id: ticket.id.clone(),
                            sprint_id: ticket.sprint_id.clone(),
                            title: ticket.title.clone(),
                            status: ticket.status.as_str().into(),
                            repo: ticket.repo.clone(),
                            weight: ticket.weight.clone(),
                        });
                    }
                }
            }
        }
    }

    let master_phases = MasterPhases {
        generated_at: Utc::now(),
        phases: phase_entries,
    };

    let master_tickets = MasterTickets {
        generated_at: Utc::now(),
        tickets: ticket_entries,
    };

    fs::create_dir_all(masters_dir).map_err(|e| CascadeError::Io {
        path: masters_dir.to_path_buf(),
        operation: "create masters dir",
        source: e,
    })?;

    write_yaml_atomic(&masters_dir.join("MASTER-PHASES.yaml"), &master_phases)?;
    write_yaml_atomic(&masters_dir.join("MASTER-TICKETS.yaml"), &master_tickets)?;

    Ok((master_phases, master_tickets))
}

// ── Language detection ────────────────────────────────────────────────────────

#[derive(Debug, Clone, PartialEq, Eq)]
enum RepoLanguage {
    TypeScript,
    Rust,
    Other,
}

fn detect_language(repo_path: &Path) -> RepoLanguage {
    // Rust: Cargo.toml at root
    if repo_path.join("Cargo.toml").exists() {
        return RepoLanguage::Rust;
    }
    // TypeScript/JS: package.json or tsconfig.json
    if repo_path.join("package.json").exists() || repo_path.join("tsconfig.json").exists() {
        return RepoLanguage::TypeScript;
    }
    RepoLanguage::Other
}

// ── TypeScript scanner ────────────────────────────────────────────────────────

static TS_EXTENSIONS: &[&str] = &["ts", "tsx", "js", "jsx", "mts", "cts", "mjs", "cjs"];

fn scan_typescript(repo_path: &Path) -> Vec<MasterEntity> {
    let src_root = find_src_root(repo_path);
    let mut entities = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();

    walk_files(&src_root, TS_EXTENSIONS, &mut |file_path, rel_path| {
        let file = match fs::File::open(file_path) {
            Ok(f) => f,
            Err(_) => return,
        };
        let reader = BufReader::new(file);
        for line in reader.lines().map_while(|l| l.ok()) {
            let trimmed = line.trim();

            // Export patterns
            for (kind, prefix) in &[
                ("export", "export function "),
                ("export", "export async function "),
                ("component", "export default function "),
                ("export", "export class "),
                ("export", "export const "),
                ("export", "export type "),
                ("export", "export interface "),
                ("export", "export enum "),
            ] {
                if let Some(stripped) = trimmed.strip_prefix(prefix) {
                    if let Some(name) = extract_identifier(stripped) {
                        let key = format!("{kind}:{name}:{rel_path}");
                        if seen.insert(key) {
                            entities.push(MasterEntity {
                                kind: kind.to_string(),
                                name,
                                file: rel_path.clone(),
                            });
                        }
                    }
                    break;
                }
            }

            // Route patterns: app.get('/path', ...) or router.post(...)
            // Also Next.js: export async function GET(
            for method in &["GET", "POST", "PUT", "DELETE", "PATCH"] {
                if trimmed.contains(&format!(".{}(", method.to_lowercase()))
                    || trimmed.starts_with(&format!("export async function {method}("))
                    || trimmed.starts_with(&format!("export function {method}("))
                {
                    // Extract path if present as string arg
                    let route_name = extract_route_path(trimmed, method)
                        .unwrap_or_else(|| format!("{method} (in {})", rel_path));
                    let key = format!("route:{route_name}:{rel_path}");
                    if seen.insert(key) {
                        entities.push(MasterEntity {
                            kind: "route".into(),
                            name: route_name,
                            file: rel_path.clone(),
                        });
                    }
                    break;
                }
            }
        }
    });

    entities
}

fn extract_identifier(s: &str) -> Option<String> {
    let s = s.trim();
    let end = s
        .find(|c: char| !c.is_alphanumeric() && c != '_')
        .unwrap_or(s.len());
    if end == 0 {
        None
    } else {
        Some(s[..end].to_string())
    }
}

fn extract_route_path(line: &str, method: &str) -> Option<String> {
    // Try to find a quoted path string like '/api/users'
    let quote_start = line.find('\'')?;
    let rest = &line[quote_start + 1..];
    let quote_end = rest.find('\'')?;
    let path = &rest[..quote_end];
    if path.starts_with('/') {
        Some(format!("{method} {path}"))
    } else {
        None
    }
}

// ── Rust scanner ──────────────────────────────────────────────────────────────

static RUST_EXTENSIONS: &[&str] = &["rs"];

fn scan_rust(repo_path: &Path) -> Vec<MasterEntity> {
    let src_root = find_src_root(repo_path);
    let mut entities = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();

    walk_files(&src_root, RUST_EXTENSIONS, &mut |file_path, rel_path| {
        let file = match fs::File::open(file_path) {
            Ok(f) => f,
            Err(_) => return,
        };
        let reader = BufReader::new(file);
        let mut in_block_comment = false;

        for line in reader.lines().map_while(|l| l.ok()) {
            let trimmed = line.trim();

            // Track block comments (very naive)
            if trimmed.starts_with("/*") {
                in_block_comment = true;
            }
            if trimmed.ends_with("*/") {
                in_block_comment = false;
                continue;
            }
            if in_block_comment || trimmed.starts_with("//") {
                continue;
            }

            for (kind, prefix) in &[
                ("pub_fn", "pub fn "),
                ("pub_fn", "pub async fn "),
                ("pub_struct", "pub struct "),
                ("pub_enum", "pub enum "),
                ("pub_trait", "pub trait "),
                ("pub_type", "pub type "),
                ("pub_use", "pub use "),
            ] {
                if let Some(stripped) = trimmed.strip_prefix(prefix) {
                    if let Some(name) = extract_identifier(stripped) {
                        let key = format!("{kind}:{name}:{rel_path}");
                        if seen.insert(key) {
                            entities.push(MasterEntity {
                                kind: kind.to_string(),
                                name,
                                file: rel_path.clone(),
                            });
                        }
                    }
                    break;
                }
            }
        }
    });

    entities
}

// ── Structural fallback ───────────────────────────────────────────────────────

fn scan_structural(repo_path: &Path) -> Vec<MasterEntity> {
    let src_root = find_src_root(repo_path);
    let mut entities = Vec::new();

    walk_files(
        &src_root,
        &["ts", "js", "rs", "py", "go", "rb", "java", "kt"],
        &mut |_, rel_path| {
            entities.push(MasterEntity {
                kind: "file".into(),
                name: rel_path.clone(),
                file: rel_path.clone(),
            });
        },
    );

    entities
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Find the best src root: `src/` or the repo root itself.
fn find_src_root(repo_path: &Path) -> PathBuf {
    let src = repo_path.join("src");
    if src.is_dir() {
        src
    } else {
        repo_path.to_path_buf()
    }
}

/// Walk all files under `root` with the given extensions, calling `cb` for each.
fn walk_files(root: &Path, extensions: &[&str], cb: &mut impl FnMut(&Path, String)) {
    let root = root.to_path_buf();
    walk_recursive(&root, &root, extensions, cb);
}

fn walk_recursive(
    base: &Path,
    dir: &Path,
    extensions: &[&str],
    cb: &mut impl FnMut(&Path, String),
) {
    let entries = match fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            // Skip common non-source dirs
            let name = path
                .file_name()
                .map(|n| n.to_string_lossy())
                .unwrap_or_default();
            if matches!(
                name.as_ref(),
                "node_modules"
                    | "target"
                    | ".git"
                    | "dist"
                    | "build"
                    | ".cache"
                    | "coverage"
                    | "__pycache__"
            ) {
                continue;
            }
            walk_recursive(base, &path, extensions, cb);
        } else if let Some(ext) = path.extension() {
            if extensions.contains(&ext.to_string_lossy().as_ref()) {
                let rel = path
                    .strip_prefix(base)
                    .map(|p| p.to_string_lossy().into_owned())
                    .unwrap_or_else(|_| path.to_string_lossy().into_owned());
                cb(&path, rel);
            }
        }
    }
}

fn write_yaml_atomic<T: serde::Serialize>(path: &Path, value: &T) -> Result<()> {
    let yaml = serde_yaml::to_string(value)
        .map_err(|e| CascadeError::Other(format!("yaml serialize: {e}")))?;
    let tmp = path.with_extension("yaml.tmp");
    fs::write(&tmp, &yaml).map_err(|e| CascadeError::Io {
        path: tmp.clone(),
        operation: "write yaml tmp",
        source: e,
    })?;
    fs::rename(&tmp, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename yaml tmp",
        source: e,
    })?;
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use serial_test::serial;
    use tempfile::TempDir;

    use crate::pbd::{
        masters::{index_pbd, index_repo},
        schema::{
            Epic, EpicStatus, Phase, PhaseStatus, Sprint, SprintStatus, Ticket, TicketStatus, Wave,
            WaveStatus,
        },
        store::PbdStore,
    };

    fn mk_store(tmp: &TempDir) -> PbdStore {
        let root = tmp.path().join("phases");
        let store = PbdStore::new(&root);
        store.init().unwrap();
        store
    }

    // ── TS fixture ────────────────────────────────────────────────────────────

    fn create_ts_fixture(dir: &TempDir) -> std::path::PathBuf {
        let repo = dir.path().join("ts-repo");
        let src = repo.join("src");
        std::fs::create_dir_all(&src).unwrap();
        // package.json to trigger TS detection
        std::fs::write(repo.join("package.json"), r#"{"name":"ts-repo"}"#).unwrap();
        // A module with exports
        std::fs::write(
            src.join("utils.ts"),
            "export function add(a: number, b: number): number { return a + b; }\nexport const VERSION = '1.0';\n",
        )
        .unwrap();
        // A route file
        std::fs::write(
            src.join("routes.ts"),
            "export async function GET(req: Request) { return new Response('ok'); }\n",
        )
        .unwrap();
        repo
    }

    #[test]
    #[serial(global_env)]
    fn test_index_ts_repo_captures_exports_and_routes() {
        let tmp = TempDir::new().unwrap();
        let masters_dir = tmp.path().join("masters");
        let repo = create_ts_fixture(&tmp);

        let list = index_repo(&repo, &masters_dir).unwrap();
        assert_eq!(list.repo, "ts-repo");
        assert!(!list.entities.is_empty(), "Expected at least one entity");

        let export_names: Vec<&str> = list.entities.iter().map(|e| e.name.as_str()).collect();
        assert!(
            export_names.contains(&"add"),
            "Expected 'add' export; got: {:?}",
            export_names
        );
        assert!(
            export_names.contains(&"VERSION"),
            "Expected 'VERSION' export"
        );

        let has_route = list.entities.iter().any(|e| e.kind == "route");
        assert!(has_route, "Expected at least one route entity");

        // File written
        assert!(masters_dir.join("ts-repo.yaml").exists());
    }

    // ── Rust fixture ─────────────────────────────────────────────────────────

    fn create_rust_fixture(dir: &TempDir) -> std::path::PathBuf {
        let repo = dir.path().join("rust-repo");
        let src = repo.join("src");
        std::fs::create_dir_all(&src).unwrap();
        std::fs::write(
            repo.join("Cargo.toml"),
            "[package]\nname = \"rust-repo\"\nversion = \"0.1.0\"\n",
        )
        .unwrap();
        std::fs::write(
            src.join("lib.rs"),
            "pub fn hello() -> &'static str { \"hello\" }\npub struct Config { pub name: String }\npub enum Mode { Fast, Slow }\n",
        )
        .unwrap();
        repo
    }

    #[test]
    #[serial(global_env)]
    fn test_index_rust_repo_captures_pub_items() {
        let tmp = TempDir::new().unwrap();
        let masters_dir = tmp.path().join("masters");
        let repo = create_rust_fixture(&tmp);

        let list = index_repo(&repo, &masters_dir).unwrap();
        assert_eq!(list.repo, "rust-repo");

        let names: Vec<&str> = list.entities.iter().map(|e| e.name.as_str()).collect();
        assert!(
            names.contains(&"hello"),
            "Expected 'hello'; got: {:?}",
            names
        );
        assert!(names.contains(&"Config"), "Expected 'Config'");
        assert!(names.contains(&"Mode"), "Expected 'Mode'");

        let kinds: Vec<&str> = list.entities.iter().map(|e| e.kind.as_str()).collect();
        assert!(kinds.contains(&"pub_fn"));
        assert!(kinds.contains(&"pub_struct"));
        assert!(kinds.contains(&"pub_enum"));
    }

    // ── Idempotent ────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn test_index_repo_idempotent() {
        let tmp = TempDir::new().unwrap();
        let masters_dir = tmp.path().join("masters");
        let repo = create_rust_fixture(&tmp);

        let r1 = index_repo(&repo, &masters_dir).unwrap();
        let r2 = index_repo(&repo, &masters_dir).unwrap();

        // Entity names should be identical
        let names1: Vec<&str> = r1.entities.iter().map(|e| e.name.as_str()).collect();
        let names2: Vec<&str> = r2.entities.iter().map(|e| e.name.as_str()).collect();
        assert_eq!(
            names1, names2,
            "Idempotent index should produce same entities"
        );
    }

    // ── PBD tree indexing ─────────────────────────────────────────────────────

    fn build_pbd_fixture(store: &PbdStore) {
        store
            .create_phase(&Phase {
                id: "p1".into(),
                title: "Phase 1".into(),
                status: PhaseStatus::Building,
                epics: vec![],
                started_at: None,
                closed_at: None,
                note: None,
            })
            .unwrap();
        store
            .create_epic(&Epic {
                id: "e01".into(),
                phase_id: "p1".into(),
                title: "Epic 1".into(),
                status: EpicStatus::Active,
                waves: vec![],
                depends_on: vec![],
                note: None,
            })
            .unwrap();
        store
            .create_wave(
                "p1",
                &Wave {
                    id: "w01".into(),
                    epic_id: "e01".into(),
                    title: "Wave 1".into(),
                    status: WaveStatus::Active,
                    sprints: vec![],
                    note: None,
                },
            )
            .unwrap();
        store
            .create_sprint(
                "p1",
                "e01",
                &Sprint {
                    id: "s01".into(),
                    wave_id: "w01".into(),
                    title: "Sprint 1".into(),
                    status: SprintStatus::Active,
                    tickets: vec![],
                    note: None,
                },
            )
            .unwrap();
        store
            .create_ticket(
                "p1",
                "e01",
                "w01",
                &Ticket {
                    id: "T-P1-E01-W01-S01-01".into(),
                    sprint_id: "s01".into(),
                    title: "First ticket".into(),
                    status: TicketStatus::Active,
                    steps: vec![],
                    depends_on: vec![],
                    repo: Some("cascade".into()),
                    weight: Some("M".into()),
                    note: None,
                    blocked_reason: None,
                },
            )
            .unwrap();
    }

    #[test]
    #[serial(global_env)]
    fn test_index_pbd_generates_master_phases_and_tickets() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_pbd_fixture(&store);

        let masters_dir = tmp.path().join("masters");
        let (phases, tickets) = index_pbd(&store, &masters_dir).unwrap();

        assert_eq!(phases.phases.len(), 1);
        assert_eq!(phases.phases[0].id, "p1");
        assert_eq!(phases.phases[0].epic_count, 1);

        assert_eq!(tickets.tickets.len(), 1);
        assert_eq!(tickets.tickets[0].id, "T-P1-E01-W01-S01-01");
        assert_eq!(tickets.tickets[0].status, "active");
        assert_eq!(tickets.tickets[0].repo.as_deref(), Some("cascade"));

        assert!(masters_dir.join("MASTER-PHASES.yaml").exists());
        assert!(masters_dir.join("MASTER-TICKETS.yaml").exists());
    }

    #[test]
    #[serial(global_env)]
    fn test_index_pbd_idempotent() {
        let tmp = TempDir::new().unwrap();
        let store = mk_store(&tmp);
        build_pbd_fixture(&store);

        let masters_dir = tmp.path().join("masters");
        let (p1, t1) = index_pbd(&store, &masters_dir).unwrap();
        let (p2, t2) = index_pbd(&store, &masters_dir).unwrap();

        let ids1: Vec<&str> = p1.phases.iter().map(|p| p.id.as_str()).collect();
        let ids2: Vec<&str> = p2.phases.iter().map(|p| p.id.as_str()).collect();
        assert_eq!(ids1, ids2);

        let tids1: Vec<&str> = t1.tickets.iter().map(|t| t.id.as_str()).collect();
        let tids2: Vec<&str> = t2.tickets.iter().map(|t| t.id.as_str()).collect();
        assert_eq!(tids1, tids2);
    }
}
