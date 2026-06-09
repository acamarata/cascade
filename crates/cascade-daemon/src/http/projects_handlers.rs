//! Purpose: HTTP handlers for the Projects API panel (T-P3-E02-14, T-P3-E02-15).
//!   Exposes four read-only GET routes mounted at `/api/projects`:
//!     GET /                    → project list + ASI overview
//!     GET /:id/repos           → sub-repo cards (git info) for a project
//!     GET /:id/phase           → PEWS phase status from .claude/phases/status.yaml
//!     GET /:id/scaffold        → recursive PEWS YAML tree (60s in-memory cache)
//! Inputs:  filesystem reads under $HOME/Sites/; serde_yaml for YAML parsing.
//! Outputs: typed JSON, always 200 (missing dirs → empty arrays / null fields).
//! Constraints: $HOME via std::env::var_os only (never dirs::home_dir).
//!   Git calls via std::process::Command — no extra crate.
//!   Scaffold cache: Arc<Mutex<HashMap>> with std::time::Instant (no framework cache).
//!   Max PEWS recursion depth: 5 levels (phase/epic/wave/sprint/ticket).
//!   serde_yaml parse errors are non-fatal: returns partial/empty data, never 500.
//! SPORT: MASTER-ENDPOINTS.md § /api/projects; T-P3-E02-14; T-P3-E02-15

use axum::{
    extract::Path,
    http::StatusCode,
    response::IntoResponse,
    routing::get,
    Json, Router,
};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::json;
use std::{
    collections::HashMap,
    path::PathBuf,
    sync::Arc,
    time::{Duration, Instant},
};
use tokio::sync::Mutex;

use crate::dashboard::DashboardState;

// ── helpers ───────────────────────────────────────────────────────────────────

/// Resolve $HOME as a PathBuf.
/// WHY not dirs::home_dir: that crate pulls in unnecessary deps and the env var
/// is always set in the contexts where this daemon runs.
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

/// Return ISO 8601 mtime for a path, or "unknown" on failure.
fn mtime_str(p: &std::path::Path) -> String {
    std::fs::metadata(p)
        .ok()
        .and_then(|m| m.modified().ok())
        .map(|t| DateTime::<Utc>::from(t).to_rfc3339())
        .unwrap_or_else(|| "unknown".to_string())
}

// ── T-P3-E02-14 types ─────────────────────────────────────────────────────────

/// One entry in the GET /api/projects response array.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
pub struct ProjectEntry {
    /// Short directory name, e.g. "nself".
    pub id: String,
    /// Same as `id` (human label placeholder until display names exist).
    pub name: String,
    /// Absolute path on disk.
    pub path: String,
    /// Always empty string for now; populated from PPI CLAUDE.md in a future ticket.
    pub description: String,
    /// Count of sub-directories that are git repos.
    pub repo_count: u32,
    /// The `phase_id` field from .claude/phases/status.yaml, if present.
    pub active_phase_id: Option<String>,
}

/// Overview of the ASI layer (~Sites/.claude/).
#[derive(Debug, Serialize, Deserialize)]
pub struct AsiOverview {
    /// Whether ~/Sites/.claude/CLAUDE.md exists.
    pub exists: bool,
    /// Number of .md files directly under ~/Sites/.claude/ (doctrine files).
    pub doctrine_file_count: u32,
}

/// Response for GET /api/projects.
#[derive(Debug, Serialize, Deserialize)]
pub struct ProjectsResponse {
    pub projects: Vec<ProjectEntry>,
    pub asi_overview: AsiOverview,
}

/// One sub-repo card for GET /api/projects/:id/repos.
#[derive(Debug, Serialize, Deserialize)]
pub struct RepoEntry {
    pub name: String,
    pub path: String,
    pub branch: Option<String>,
    pub last_commit_at: Option<String>,
    pub has_claude_md: bool,
}

// ── T-P3-E02-15 types ─────────────────────────────────────────────────────────

/// Minimal fields parsed from .claude/phases/status.yaml.
/// Matches the schema used by cascade itself.
#[derive(Debug, Default, Serialize, Deserialize, Clone)]
pub struct PhaseStatus {
    pub phase_id: Option<String>,
    pub phase_name: Option<String>,
    pub phase_status: Option<String>,
    pub pct_done: Option<f64>,
    pub tickets_total: Option<u32>,
    pub tickets_done: Option<u32>,
    pub tickets_blocked: Option<u32>,
    /// Whether the phase plan.md file exists.
    pub plan_md_exists: bool,
    /// Absolute path of the phase directory that was read.
    pub phase_dir: Option<String>,
}

/// Leaf ticket summary inside a scaffold tree.
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct TicketSummary {
    pub id: String,
    pub title: Option<String>,
    pub weight: Option<String>,
    pub status: Option<String>,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct SprintNode {
    pub id: String,
    pub tickets: Vec<TicketSummary>,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct WaveNode {
    pub id: String,
    pub sprints: Vec<SprintNode>,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct EpicNode {
    pub id: String,
    pub waves: Vec<WaveNode>,
}

/// Root scaffold response for GET /api/projects/:id/scaffold.
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct ScaffoldResponse {
    pub phase_id: Option<String>,
    pub phase_dir: Option<String>,
    pub epics: Vec<EpicNode>,
    /// ISO 8601 timestamp when this tree was built.
    pub built_at: String,
}

// ── scaffold cache ────────────────────────────────────────────────────────────

/// 60-second TTL in-memory scaffold cache.
/// Key: project path string. Value: (instant_built, cached_response).
type ScaffoldCache = Arc<Mutex<HashMap<String, (Instant, ScaffoldResponse)>>>;

const SCAFFOLD_TTL: Duration = Duration::from_secs(60);

/// Module-level shared cache, initialised once.
static CACHE: std::sync::OnceLock<ScaffoldCache> = std::sync::OnceLock::new();

fn scaffold_cache() -> &'static ScaffoldCache {
    CACHE.get_or_init(|| Arc::new(Mutex::new(HashMap::new())))
}

// ── git helpers ───────────────────────────────────────────────────────────────

/// Check whether `dir` is a git repository (non-blocking, process spawn).
fn is_git_repo(dir: &std::path::Path) -> bool {
    std::process::Command::new("git")
        .args(["-C", &dir.to_string_lossy(), "rev-parse", "--is-inside-work-tree"])
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// Current branch name for a git repo.
fn git_branch(dir: &std::path::Path) -> Option<String> {
    let out = std::process::Command::new("git")
        .args(["-C", &dir.to_string_lossy(), "rev-parse", "--abbrev-ref", "HEAD"])
        .output()
        .ok()?;
    if out.status.success() {
        Some(String::from_utf8_lossy(&out.stdout).trim().to_string())
    } else {
        None
    }
}

/// ISO 8601 timestamp of the last commit in a git repo.
fn git_last_commit_at(dir: &std::path::Path) -> Option<String> {
    let out = std::process::Command::new("git")
        .args([
            "-C",
            &dir.to_string_lossy(),
            "log",
            "-1",
            "--format=%cI",
        ])
        .output()
        .ok()?;
    if out.status.success() {
        let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
        if s.is_empty() { None } else { Some(s) }
    } else {
        None
    }
}

// ── data helpers ──────────────────────────────────────────────────────────────

/// Read the top-level .claude/phases/status.yaml for a project path.
/// Returns default struct (all None) on any I/O or parse failure.
fn read_phase_status(project_path: &std::path::Path) -> PhaseStatus {
    let status_path = project_path.join(".claude").join("phases").join("status.yaml");
    let phase_dir_base = project_path.join(".claude").join("phases").join("current");

    let content = match std::fs::read_to_string(&status_path) {
        Ok(c) => c,
        Err(_) => return PhaseStatus::default(),
    };

    // Parse as serde_yaml::Value first to be tolerant of schema drift.
    let val: serde_yaml::Value = match serde_yaml::from_str(&content) {
        Ok(v) => v,
        Err(_) => return PhaseStatus::default(),
    };

    let get_str = |key: &str| -> Option<String> {
        val.get(key)?.as_str().map(|s| s.to_string())
    };
    let get_f64 = |key: &str| -> Option<f64> {
        val.get(key)?.as_f64()
    };
    let get_u32 = |key: &str| -> Option<u32> {
        val.get(key)
            .and_then(|v| v.as_u64())
            .map(|n| n as u32)
            // Some status.yaml files write ticket counts under a "tickets" sub-map.
            .or_else(|| {
                val.get("tickets")
                    .and_then(|t| t.get(key))
                    .and_then(|v| v.as_u64())
                    .map(|n| n as u32)
            })
    };

    let phase_id = get_str("phase_id");
    // Find the phase dir if phase_id is known.
    let phase_dir = phase_id.as_deref().map(|pid| {
        // Normalise: status.yaml uses "P1", dir is "p1".
        let dir_name = pid.to_lowercase();
        phase_dir_base.join(dir_name).to_string_lossy().into_owned()
    });
    let plan_md_exists = phase_dir.as_deref().map(|d| {
        std::path::Path::new(d).join("plan.md").exists()
    }).unwrap_or(false);

    PhaseStatus {
        phase_id,
        phase_name: get_str("phase_name"),
        phase_status: get_str("phase_status"),
        pct_done: get_f64("pct_done"),
        tickets_total: get_u32("total"),
        tickets_done: get_u32("done"),
        tickets_blocked: get_u32("blocked"),
        plan_md_exists,
        phase_dir,
    }
}

/// Read a single YAML ticket file and extract summary fields.
fn read_ticket_summary(path: &std::path::Path) -> TicketSummary {
    let id = path
        .file_stem()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_else(|| "unknown".to_string());
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(_) => {
            return TicketSummary { id, title: None, weight: None, status: None }
        }
    };
    let val: serde_yaml::Value = match serde_yaml::from_str(&content) {
        Ok(v) => v,
        Err(_) => return TicketSummary { id, title: None, weight: None, status: None },
    };
    let get = |key: &str| val.get(key)?.as_str().map(|s| s.to_string());
    TicketSummary {
        id,
        title: get("title"),
        weight: get("weight"),
        status: get("status"),
    }
}

/// Walk the PEWS tree for one project up to 5 levels deep.
/// Silently skips any non-existent directory at any level.
fn build_scaffold(project_path: &std::path::Path) -> ScaffoldResponse {
    let status = read_phase_status(project_path);
    let phase_id = status.phase_id.clone();

    let current_dir = project_path.join(".claude").join("phases").join("current");
    // Find the active phase dir: first subdir of current/ that starts with "p".
    let phase_dir = match phase_id.as_deref() {
        Some(pid) => {
            let d = current_dir.join(pid.to_lowercase());
            if d.is_dir() { Some(d) } else { None }
        }
        None => {
            // Fallback: find any p{N} dir.
            std::fs::read_dir(&current_dir)
                .ok()
                .and_then(|rd| {
                    rd.flatten()
                        .filter(|e| {
                            e.file_name()
                                .to_string_lossy()
                                .starts_with('p')
                        })
                        .min_by_key(|e| e.file_name())
                        .map(|e| e.path())
                })
        }
    };

    let phase_dir_str = phase_dir.as_ref().map(|d| d.to_string_lossy().into_owned());

    let epics = phase_dir.as_deref()
        .and_then(|pd| {
            let epics_dir = pd.join("epics");
            let rd = std::fs::read_dir(&epics_dir).ok()?;
            let mut epics: Vec<EpicNode> = rd
                .flatten()
                .filter(|e| e.path().is_dir())
                .map(|e| {
                    let epic_id = e.file_name().to_string_lossy().into_owned();
                    let waves_dir = e.path().join("waves");
                    let waves = std::fs::read_dir(&waves_dir)
                        .ok()
                        .map(|wd| {
                            let mut wv: Vec<WaveNode> = wd
                                .flatten()
                                .filter(|we| we.path().is_dir())
                                .map(|we| {
                                    let wave_id = we.file_name().to_string_lossy().into_owned();
                                    let sprints_dir = we.path().join("sprints");
                                    let sprints = std::fs::read_dir(&sprints_dir)
                                        .ok()
                                        .map(|sd| {
                                            let mut sv: Vec<SprintNode> = sd
                                                .flatten()
                                                .filter(|se| se.path().is_dir())
                                                .map(|se| {
                                                    let sprint_id = se.file_name().to_string_lossy().into_owned();
                                                    let tickets_dir = se.path().join("tickets");
                                                    let tickets = std::fs::read_dir(&tickets_dir)
                                                        .ok()
                                                        .map(|td| {
                                                            let mut tv: Vec<TicketSummary> = td
                                                                .flatten()
                                                                .filter(|te| {
                                                                    te.path().extension()
                                                                        .map(|x| x == "yaml")
                                                                        .unwrap_or(false)
                                                                    && !te.file_name()
                                                                        .to_string_lossy()
                                                                        .ends_with(".status.yaml")
                                                                })
                                                                .map(|te| read_ticket_summary(&te.path()))
                                                                .collect();
                                                            tv.sort_by(|a, b| a.id.cmp(&b.id));
                                                            tv
                                                        })
                                                        .unwrap_or_default();
                                                    SprintNode { id: sprint_id, tickets }
                                                })
                                                .collect();
                                            sv.sort_by(|a, b| a.id.cmp(&b.id));
                                            sv
                                        })
                                        .unwrap_or_default();
                                    WaveNode { id: wave_id, sprints }
                                })
                                .collect();
                            wv.sort_by(|a, b| a.id.cmp(&b.id));
                            wv
                        })
                        .unwrap_or_default();
                    EpicNode { id: epic_id, waves }
                })
                .collect();
            epics.sort_by(|a, b| a.id.cmp(&b.id));
            Some(epics)
        })
        .unwrap_or_default();

    ScaffoldResponse {
        phase_id,
        phase_dir: phase_dir_str,
        epics,
        built_at: Utc::now().to_rfc3339(),
    }
}

// ── route handlers ────────────────────────────────────────────────────────────

/// GET /api/projects
///
/// Purpose: list all projects under ~/Sites/ that have a .claude/ directory,
/// and return an ASI overview (~/Sites/.claude/ presence + doctrine file count).
/// Returns 200 [] when ~/Sites/ is absent or empty.
pub async fn handler_projects() -> impl IntoResponse {
    let home = match home_dir() {
        Some(h) => h,
        None => {
            return Json(json!({
                "projects": [],
                "asi_overview": { "exists": false, "doctrine_file_count": 0 }
            }))
            .into_response()
        }
    };

    let sites_dir = home.join("Sites");

    // ASI overview.
    let asi_dir = sites_dir.join(".claude");
    let asi_exists = asi_dir.join("CLAUDE.md").exists();
    let doctrine_file_count = std::fs::read_dir(&asi_dir)
        .ok()
        .map(|rd| {
            rd.flatten()
                .filter(|e| {
                    e.path().extension().map(|x| x == "md").unwrap_or(false)
                })
                .count() as u32
        })
        .unwrap_or(0);

    // Project enumeration.
    let mut projects: Vec<ProjectEntry> = std::fs::read_dir(&sites_dir)
        .ok()
        .map(|rd| {
            rd.flatten()
                .filter(|e| {
                    let p = e.path();
                    p.is_dir() && p.join(".claude").join("CLAUDE.md").exists()
                })
                .map(|e| {
                    let path = e.path();
                    let id = path.file_name().unwrap().to_string_lossy().into_owned();
                    // Count sub-repos.
                    let repo_count = std::fs::read_dir(&path)
                        .ok()
                        .map(|rd2| {
                            rd2.flatten()
                                .filter(|e2| e2.path().is_dir() && is_git_repo(&e2.path()))
                                .count() as u32
                        })
                        .unwrap_or(0);
                    // Active phase id.
                    let active_phase_id = {
                        let s = read_phase_status(&path);
                        s.phase_id
                    };
                    ProjectEntry {
                        name: id.clone(),
                        id,
                        path: path.to_string_lossy().into_owned(),
                        description: String::new(),
                        repo_count,
                        active_phase_id,
                    }
                })
                .collect()
        })
        .unwrap_or_default();

    projects.sort_by(|a, b| a.id.cmp(&b.id));

    let resp = ProjectsResponse {
        projects,
        asi_overview: AsiOverview {
            exists: asi_exists,
            doctrine_file_count,
        },
    };
    Json(json!(resp)).into_response()
}

/// GET /api/projects/:id/repos
///
/// Purpose: list sub-repositories for a project identified by its directory name
/// (e.g. "nself" → ~/Sites/nself/).  Each entry carries git branch + last commit.
/// Returns 200 [] when the project directory does not exist.
pub async fn handler_project_repos(Path(id): Path<String>) -> impl IntoResponse {
    let home = match home_dir() {
        Some(h) => h,
        None => return (StatusCode::OK, Json(json!([]))).into_response(),
    };

    let project_dir = home.join("Sites").join(&id);
    if !project_dir.is_dir() {
        return (StatusCode::OK, Json(json!([]))).into_response();
    }

    let mut repos: Vec<RepoEntry> = std::fs::read_dir(&project_dir)
        .ok()
        .map(|rd| {
            rd.flatten()
                .filter(|e| e.path().is_dir() && is_git_repo(&e.path()))
                .map(|e| {
                    let path = e.path();
                    let name = path.file_name().unwrap().to_string_lossy().into_owned();
                    let branch = git_branch(&path);
                    let last_commit_at = git_last_commit_at(&path);
                    let has_claude_md = path.join(".claude").join("CLAUDE.md").exists();
                    RepoEntry {
                        name,
                        path: path.to_string_lossy().into_owned(),
                        branch,
                        last_commit_at,
                        has_claude_md,
                    }
                })
                .collect()
        })
        .unwrap_or_default();

    repos.sort_by(|a, b| a.name.cmp(&b.name));
    Json(json!(repos)).into_response()
}

/// GET /api/projects/:id/phase
///
/// Purpose: return the PEWS phase status for a project by reading
/// ~/Sites/{id}/.claude/phases/status.yaml.
/// Returns 200 with all-null fields when the file is absent or unparseable.
pub async fn handler_project_phase(Path(id): Path<String>) -> impl IntoResponse {
    let home = match home_dir() {
        Some(h) => h,
        None => return Json(json!(PhaseStatus::default())).into_response(),
    };

    let project_dir = home.join("Sites").join(&id);
    let status = read_phase_status(&project_dir);
    Json(json!(status)).into_response()
}

/// GET /api/projects/:id/scaffold
///
/// Purpose: walk the PEWS tree for a project and return a nested JSON tree
/// (phase → epics → waves → sprints → tickets).  Results are cached for 60 s.
/// Returns 200 with empty epics when no phase dir exists.
pub async fn handler_project_scaffold(Path(id): Path<String>) -> impl IntoResponse {
    let home = match home_dir() {
        Some(h) => h,
        None => {
            return Json(json!(ScaffoldResponse {
                phase_id: None,
                phase_dir: None,
                epics: vec![],
                built_at: Utc::now().to_rfc3339(),
            }))
            .into_response()
        }
    };

    let project_path = home.join("Sites").join(&id);
    let cache_key = project_path.to_string_lossy().into_owned();

    // Check cache.
    {
        let cache = scaffold_cache().lock().await;
        if let Some((built_at, cached)) = cache.get(&cache_key) {
            if built_at.elapsed() < SCAFFOLD_TTL {
                return Json(json!(cached.clone())).into_response();
            }
        }
    }

    // Build fresh (blocking FS work on Tokio thread pool).
    let project_path_clone = project_path.clone();
    let scaffold = tokio::task::spawn_blocking(move || build_scaffold(&project_path_clone))
        .await
        .unwrap_or_else(|_| ScaffoldResponse {
            phase_id: None,
            phase_dir: None,
            epics: vec![],
            built_at: Utc::now().to_rfc3339(),
        });

    // Store in cache.
    {
        let mut cache = scaffold_cache().lock().await;
        cache.insert(cache_key, (Instant::now(), scaffold.clone()));
    }

    Json(json!(scaffold)).into_response()
}

// ── router ────────────────────────────────────────────────────────────────────

/// Build the projects API router, mounted at `/api/projects` in dashboard.rs.
pub fn router() -> Router<DashboardState> {
    Router::new()
        .route("/", get(handler_projects))
        .route("/:id/repos", get(handler_project_repos))
        .route("/:id/phase", get(handler_project_phase))
        .route("/:id/scaffold", get(handler_project_scaffold))
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{body::Body, http::Request};
    use serial_test::serial;
    use serde_json::Value;
    use tempfile::TempDir;
    use tower::ServiceExt;

    /// Build a stateless test router (no DashboardState needed for unit tests).
    fn test_router() -> axum::Router {
        axum::Router::new()
            .route("/", get(handler_projects))
            .route("/:id/repos", get(handler_project_repos))
            .route("/:id/phase", get(handler_project_phase))
            .route("/:id/scaffold", get(handler_project_scaffold))
    }

    /// Set HOME to a temp dir for the duration of the closure.
    fn with_fake_home<F: FnOnce(&TempDir)>(f: F) {
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        f(&tmp);
    }

    // ── GET / ─────────────────────────────────────────────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_projects_empty_sites() {
        with_fake_home(|_tmp| {
            // ~/Sites/ does not exist → should return 200 with empty array.
        });
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder().uri("/").body(Body::empty()).unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_object());
                    assert!(val["projects"].is_array());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_projects_with_project() {
        with_fake_home(|tmp| {
            // Create ~/Sites/myproject/.claude/CLAUDE.md
            let proj = tmp.path().join("Sites").join("myproject").join(".claude");
            std::fs::create_dir_all(&proj).unwrap();
            std::fs::write(proj.join("CLAUDE.md"), "# test").unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder().uri("/").body(Body::empty()).unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    let projects = val["projects"].as_array().unwrap();
                    assert_eq!(projects.len(), 1);
                    assert_eq!(projects[0]["id"], "myproject");
                })
            });
        });
    }

    // ── GET /:id/repos ────────────────────────────────────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_repos_missing_project() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/nonexistent/repos")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_array());
                    assert_eq!(val.as_array().unwrap().len(), 0);
                })
            });
        });
    }

    // ── GET /:id/phase ────────────────────────────────────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_phase_missing_yaml() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/someproject/phase")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_object());
                    // All fields are null when yaml missing.
                    assert!(val["phase_id"].is_null());
                    assert_eq!(val["plan_md_exists"], false);
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_phase_parses_status_yaml() {
        with_fake_home(|tmp| {
            // Create ~/Sites/cascade/.claude/phases/status.yaml
            let phases_dir = tmp
                .path()
                .join("Sites")
                .join("cascade")
                .join(".claude")
                .join("phases");
            std::fs::create_dir_all(&phases_dir).unwrap();
            std::fs::write(
                phases_dir.join("status.yaml"),
                "phase_id: P3\nphase_name: Desktop GUI\nphase_status: building\npct_done: 42.0\n",
            )
            .unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/cascade/phase")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert_eq!(val["phase_id"], "P3");
                    assert_eq!(val["phase_status"], "building");
                    assert_eq!(val["pct_done"], 42.0);
                })
            });
        });
    }

    // ── GET /:id/scaffold ─────────────────────────────────────────────────────

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_scaffold_no_phase_dir() {
        with_fake_home(|_tmp| {
            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/noproject/scaffold")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert!(val.is_object());
                    assert!(val["epics"].as_array().unwrap().is_empty());
                })
            });
        });
    }

    #[tokio::test(flavor = "multi_thread")]
    #[serial(global_env)]
    async fn test_scaffold_with_fixture_tree() {
        with_fake_home(|tmp| {
            // Build a minimal PEWS tree:
            //   ~/Sites/myproj/.claude/phases/current/p1/epics/E-01/waves/W-01/sprints/S-01/tickets/T-01.yaml
            let ticket_dir = tmp
                .path()
                .join("Sites/myproj/.claude/phases/current/p1/epics/E-01/waves/W-01/sprints/S-01/tickets");
            std::fs::create_dir_all(&ticket_dir).unwrap();
            std::fs::write(
                ticket_dir.join("T-01.yaml"),
                "id: T-01\ntitle: First ticket\nweight: S\nstatus: pending\n",
            )
            .unwrap();
            // Also write status.yaml so phase_id resolves.
            let phases_dir = tmp.path().join("Sites/myproj/.claude/phases");
            std::fs::write(
                phases_dir.join("status.yaml"),
                "phase_id: P1\nphase_status: building\n",
            )
            .unwrap();

            let app = test_router();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async {
                    let req = Request::builder()
                        .uri("/myproj/scaffold")
                        .body(Body::empty())
                        .unwrap();
                    let resp = app.oneshot(req).await.unwrap();
                    assert_eq!(resp.status(), StatusCode::OK);
                    let body = axum::body::to_bytes(resp.into_body(), usize::MAX)
                        .await
                        .unwrap();
                    let val: Value = serde_json::from_slice(&body).unwrap();
                    assert_eq!(val["phase_id"], "P1");
                    let epics = val["epics"].as_array().unwrap();
                    assert_eq!(epics.len(), 1);
                    assert_eq!(epics[0]["id"], "E-01");
                    let tickets = &epics[0]["waves"][0]["sprints"][0]["tickets"];
                    assert_eq!(tickets[0]["id"], "T-01");
                    assert_eq!(tickets[0]["weight"], "S");
                })
            });
        });
    }
}
