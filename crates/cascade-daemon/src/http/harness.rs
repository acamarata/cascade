//! Harness-augmentation API handlers for the /api/gci/ router group.
//!
//! Purpose: Exposes two endpoints that surface Cascade's role as an augmenting
//!   meta-harness for CC (Claude Code) and OC (OpenCode):
//!     GET  /harness-status      → detect CC/OC presence; report instruction-file links.
//!     POST /harness-regenerate  → (re)create CLAUDE.md / AGENTS.md sibling symlinks at
//!                                 all resolved cascade tier directories for one harness.
//!
//! Inputs:
//!   - GET  /harness-status:      none (reads $HOME filesystem).
//!   - POST /harness-regenerate:  JSON body `{ "harness": "cc" | "oc" }`.
//!
//! Outputs:
//!   - GET  → `cascade_types::harness::HarnessStatus` JSON.
//!   - POST → `cascade_types::harness::RegenerateResponse` JSON, or HTTP 400 on
//!     bad harness value.
//!
//! Constraints:
//!   - Path resolution uses $HOME env var only (repo convention, not dirs crate).
//!   - HOME-confinement: the regenerator ONLY writes inside $HOME; it refuses to
//!     create symlinks at any path outside of it.
//!   - Symlink creation is idempotent: an existing symlink at the target path is
//!     removed and recreated atomically (remove → symlink); if the target is a
//!     regular file it is left untouched and the slot is skipped.
//!   - Permission errors on individual tier dirs are logged and skipped; the
//!     handler still returns regenerated_count for successfully written slots.
//!   - The harness argument is validated against the set {"cc", "oc"}; any other
//!     value returns HTTP 400.
//!
//! SPORT: T-P3-E02-28 — MASTER-ROUTES.md: GET /api/gci/harness-status,
//!   POST /api/gci/harness-regenerate

use std::path::{Path, PathBuf};

use axum::{
    http::StatusCode,
    response::IntoResponse,
    routing::{get, post},
    Json, Router,
};
use serde_json::json;
use tracing::warn;

use cascade_types::harness::{HarnessEntry, HarnessStatus, RegenerateRequest, RegenerateResponse};

use crate::dashboard::DashboardState;

// ── Path helpers ──────────────────────────────────────────────────────────────

/// Return $HOME as a PathBuf, or None if unset.
///
/// WHY: repo convention requires std::env var lookup (not dirs::home_dir).
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

// ── CC detection ──────────────────────────────────────────────────────────────

/// Check whether Claude Code is installed/present on this machine.
///
/// Purpose: CC is considered "detected" when either `~/.claude/settings.json`
///   or `~/.claude/CLAUDE.md` exists, which indicates the user has the Claude
///   Code harness configured.
/// Inputs:  `home` — the resolved home directory.
/// Outputs: `true` if CC presence markers are found.
fn detect_cc(home: &Path) -> bool {
    let claude_dir = home.join(".claude");
    claude_dir.join("settings.json").exists() || claude_dir.join("CLAUDE.md").exists()
}

/// Check whether the CC instruction file (CLAUDE.md) exists in `~/.claude/`
/// and is a symlink pointing to CASCADE.md.
///
/// Outputs: `(Option<abs_path_string>, is_linked)`.
fn cc_instruction_file_status(home: &Path) -> (Option<String>, bool) {
    let claude_md = home.join(".claude").join("CLAUDE.md");
    if !claude_md.exists() && claude_md.symlink_metadata().is_err() {
        return (None, false);
    }
    let path_str = claude_md.to_string_lossy().to_string();
    // Check if it's a symlink pointing to a CASCADE.md target.
    let linked = std::fs::read_link(&claude_md)
        .ok()
        .and_then(|target| target.file_name().map(|n| n == "CASCADE.md"))
        .unwrap_or(false);
    (Some(path_str), linked)
}

// ── OC detection ──────────────────────────────────────────────────────────────

/// Check whether OpenCode is installed/present on this machine.
///
/// Purpose: OC is detected when `~/.opencode/` or `~/.config/opencode/` exists
///   (either directory indicates an OpenCode installation).
/// Inputs:  `home` — the resolved home directory.
/// Outputs: `true` if OC presence markers are found.
fn detect_oc(home: &Path) -> bool {
    home.join(".opencode").exists()
        || home.join(".config").join("opencode").exists()
        || home
            .join(".config")
            .join("opencode")
            .join("opencode.json")
            .exists()
}

/// Check whether the OC instruction file (AGENTS.md) exists at the primary OC
/// config dir and is a symlink pointing to CASCADE.md.
///
/// Outputs: `(Option<abs_path_string>, is_linked)`.
fn oc_instruction_file_status(home: &Path) -> (Option<String>, bool) {
    // Primary location: ~/.opencode/AGENTS.md; fallback: ~/.config/opencode/AGENTS.md
    let primary = home.join(".opencode").join("AGENTS.md");
    let fallback = home.join(".config").join("opencode").join("AGENTS.md");

    let agents_md = if primary.exists() || primary.symlink_metadata().is_ok() {
        primary
    } else if fallback.exists() || fallback.symlink_metadata().is_ok() {
        fallback
    } else {
        return (None, false);
    };

    let path_str = agents_md.to_string_lossy().to_string();
    let linked = std::fs::read_link(&agents_md)
        .ok()
        .and_then(|target| target.file_name().map(|n| n == "CASCADE.md"))
        .unwrap_or(false);
    (Some(path_str), linked)
}

// ── Tier-path resolution ──────────────────────────────────────────────────────

/// Resolve the cascade instruction-file slot paths for a given harness.
///
/// Purpose: Returns a list of `(dir, instruction_filename)` pairs — one per
///   cascade tier that should carry a harness-specific instruction-file symlink.
///   The tiers covered are: GCI (~/.claude), PPI (~/.cascade at HOME level),
///   and a representative per-project level under ~/Sites/ if it exists.
///
/// Inputs:  `harness` — "cc" | "oc"; `home` — resolved $HOME.
/// Outputs: Vec of `(target_dir, symlink_filename)` tuples. Only dirs that
///   exist AND are inside $HOME are returned (HOME-confinement rule).
/// Constraints:
///   - ONLY paths under `home` are returned; no system-wide paths.
///   - Directories that do not yet exist are excluded (we do not create dirs).
fn resolve_tier_paths(harness: &str, home: &Path) -> Vec<(PathBuf, &'static str)> {
    let symlink_name: &'static str = if harness == "cc" {
        "CLAUDE.md"
    } else {
        "AGENTS.md"
    };

    let mut candidates: Vec<PathBuf> = Vec::new();

    // GCI tier: ~/.claude/ (CC) or ~/.opencode/ or ~/.config/opencode/ (OC).
    if harness == "cc" {
        candidates.push(home.join(".claude"));
    } else {
        let oc_primary = home.join(".opencode");
        let oc_alt = home.join(".config").join("opencode");
        if oc_primary.is_dir() {
            candidates.push(oc_primary);
        } else if oc_alt.is_dir() {
            candidates.push(oc_alt);
        }
    }

    // Global cascade dir: ~/.cascade/ — always a candidate.
    candidates.push(home.join(".cascade"));

    // Sites-level cascade dirs: ~/Sites/.claude/ (CC) or ~/Sites/.opencode/ (OC).
    let sites_cascade = if harness == "cc" {
        home.join("Sites").join(".claude")
    } else {
        home.join("Sites").join(".opencode")
    };
    candidates.push(sites_cascade);

    // Filter: must be inside home, must be an existing directory.
    candidates
        .into_iter()
        .filter(|p| p.is_dir() && p.starts_with(home))
        .map(|p| (p, symlink_name))
        .collect()
}

// ── Regenerator ───────────────────────────────────────────────────────────────

/// Create or update instruction-file symlinks for the named harness.
///
/// Purpose: For each resolved tier directory, create a symlink named CLAUDE.md
///   (cc) or AGENTS.md (oc) that points to CASCADE.md in the same directory.
///   The operation is idempotent: an existing symlink is removed and recreated;
///   a regular file at the same path is skipped with a warning.
///
/// Inputs:  `harness` — "cc" | "oc" (pre-validated); `home` — $HOME path.
/// Outputs: Count of symlinks successfully created/updated.
/// Constraints:
///   - Only writes within `home` (enforced by `resolve_tier_paths`).
///   - Uses `std::os::unix::fs::symlink` for atomic-ish symlink creation.
///   - Permission errors are logged and skipped; partial success is valid.
fn regenerate_for_harness(harness: &str, home: &Path) -> u32 {
    #[cfg(unix)]
    use std::os::unix::fs::symlink;

    let slots = resolve_tier_paths(harness, home);
    let mut count: u32 = 0;

    for (dir, link_name) in &slots {
        // HOME-confinement double-check (belt + suspenders).
        if !dir.starts_with(home) {
            warn!(
                "harness-regenerate: refusing to write outside HOME: {}",
                dir.display()
            );
            continue;
        }

        let link_path = dir.join(link_name);

        // If a regular (non-symlink) file already exists, skip it.
        if link_path.exists()
            && !link_path
                .symlink_metadata()
                .map(|m| m.file_type().is_symlink())
                .unwrap_or(false)
        {
            warn!(
                "harness-regenerate: {} is a regular file, skipping",
                link_path.display()
            );
            continue;
        }

        // Remove existing symlink if present (idempotent).
        if link_path.symlink_metadata().is_ok() {
            if let Err(e) = std::fs::remove_file(&link_path) {
                warn!(
                    "harness-regenerate: failed to remove existing symlink {}: {e}",
                    link_path.display()
                );
                continue;
            }
        }

        // Create the symlink: CLAUDE.md → CASCADE.md (relative target in same dir).
        #[cfg(unix)]
        {
            match symlink("CASCADE.md", &link_path) {
                Ok(()) => {
                    count += 1;
                }
                Err(e) => {
                    warn!(
                        "harness-regenerate: failed to create symlink {}: {e}",
                        link_path.display()
                    );
                }
            }
        }

        // On non-Unix platforms, write a stub file instead of a symlink.
        #[cfg(not(unix))]
        {
            match std::fs::write(&link_path, "# Cascade instruction file\n@CASCADE.md\n") {
                Ok(()) => {
                    count += 1;
                }
                Err(e) => {
                    warn!(
                        "harness-regenerate: failed to write instruction file {}: {e}",
                        link_path.display()
                    );
                }
            }
        }
    }

    count
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// GET /harness-status
///
/// Purpose: Detect CC and OC harnesses and report instruction-file linkage.
/// Outputs: JSON `HarnessStatus` with two entries (cc, oc).
/// Constraints: Read-only; no filesystem writes.
async fn handler_harness_status() -> impl IntoResponse {
    let home = match home_dir() {
        Some(h) => h,
        None => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({"error": "HOME not set"})),
            )
                .into_response();
        }
    };

    let (cc_path, cc_linked) = cc_instruction_file_status(&home);
    let (oc_path, oc_linked) = oc_instruction_file_status(&home);

    let status = HarnessStatus {
        harnesses: vec![
            HarnessEntry {
                name: "cc".to_string(),
                detected: detect_cc(&home),
                instruction_file_path: cc_path,
                instruction_file_linked: cc_linked,
            },
            HarnessEntry {
                name: "oc".to_string(),
                detected: detect_oc(&home),
                instruction_file_path: oc_path,
                instruction_file_linked: oc_linked,
            },
        ],
    };

    Json(status).into_response()
}

/// POST /harness-regenerate
///
/// Purpose: (Re)create instruction-file symlinks for the requested harness.
/// Inputs:  JSON body `{ "harness": "cc" | "oc" }`.
/// Outputs: JSON `RegenerateResponse { regenerated_count }`.
/// Constraints:
///   - Returns HTTP 400 if `harness` is not "cc" or "oc".
///   - Returns HTTP 500 if $HOME is unset.
///   - Only writes within $HOME (enforced by `regenerate_for_harness`).
async fn handler_harness_regenerate(Json(req): Json<RegenerateRequest>) -> impl IntoResponse {
    // Validate harness argument.
    if req.harness != "cc" && req.harness != "oc" {
        return (
            StatusCode::BAD_REQUEST,
            Json(json!({
                "error": "invalid harness",
                "detail": "harness must be \"cc\" or \"oc\""
            })),
        )
            .into_response();
    }

    let home = match home_dir() {
        Some(h) => h,
        None => {
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({"error": "HOME not set"})),
            )
                .into_response();
        }
    };

    let regenerated_count = regenerate_for_harness(&req.harness, &home);

    Json(RegenerateResponse { regenerated_count }).into_response()
}

// ── Router ────────────────────────────────────────────────────────────────────

/// Build the harness sub-router, nested at `/api/gci` by the parent router.
///
/// Purpose: Groups the harness-status and harness-regenerate routes; nested at
///   `/api/gci` so the final paths are:
///     GET  /api/gci/harness-status
///     POST /api/gci/harness-regenerate
/// Inputs:  None (routes are stateless; no DashboardState fields required here).
/// Outputs: `Router<DashboardState>` ready for nesting.
pub fn router() -> Router<DashboardState> {
    Router::new()
        .route("/harness-status", get(handler_harness_status))
        .route("/harness-regenerate", post(handler_harness_regenerate))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{body::Body, http::Request, Router};
    use serial_test::serial;
    use tempfile::TempDir;
    use tower::ServiceExt;

    use crate::dashboard::DashboardState;
    use std::sync::Arc;

    /// Build a test app wrapping the harness router.
    fn test_app() -> Router {
        router().with_state(DashboardState {
            token: Arc::new("test-token".to_string()),
            provider_registry: None,
        })
    }

    /// Set $HOME to a fresh tempdir and return it.
    /// SAFETY: callers must carry #[serial(global_env)] to serialise env mutations.
    fn set_fake_home() -> TempDir {
        let tmp = TempDir::new().unwrap();
        // SAFETY: serialised by #[serial(global_env)] on every calling test.
        std::env::set_var("HOME", tmp.path());
        tmp
    }

    // ── Status endpoint ──────────────────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn test_harness_status_returns_cc_and_oc_entries() {
        let tmp = set_fake_home();
        // Minimal setup: create ~/.claude so CC is detectable.
        std::fs::create_dir_all(tmp.path().join(".claude")).unwrap();
        std::fs::write(tmp.path().join(".claude").join("settings.json"), "{}").unwrap();

        let app = test_app();
        let resp = app
            .oneshot(
                Request::builder()
                    .uri("/harness-status")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(resp.status(), StatusCode::OK);

        let body = axum::body::to_bytes(resp.into_body(), 65536).await.unwrap();
        let status: HarnessStatus = serde_json::from_slice(&body).unwrap();

        assert_eq!(status.harnesses.len(), 2);

        let cc = status.harnesses.iter().find(|e| e.name == "cc").unwrap();
        let oc = status.harnesses.iter().find(|e| e.name == "oc").unwrap();

        // CC should be detected (settings.json exists).
        assert!(cc.detected, "CC should be detected");
        // OC not set up → not detected.
        assert!(!oc.detected, "OC should not be detected in clean fake HOME");
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn test_harness_status_both_entries_present() {
        let _tmp = set_fake_home();

        let app = test_app();
        let resp = app
            .oneshot(
                Request::builder()
                    .uri("/harness-status")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(resp.status(), StatusCode::OK);
        let body = axum::body::to_bytes(resp.into_body(), 65536).await.unwrap();
        let status: HarnessStatus = serde_json::from_slice(&body).unwrap();
        // Always exactly 2 entries: cc + oc.
        assert_eq!(status.harnesses.len(), 2);
        let names: Vec<&str> = status.harnesses.iter().map(|e| e.name.as_str()).collect();
        assert!(names.contains(&"cc"));
        assert!(names.contains(&"oc"));
    }

    // ── Regenerate endpoint ──────────────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn test_harness_regenerate_invalid_harness_returns_400() {
        let _tmp = set_fake_home();

        let app = test_app();
        let body = serde_json::to_vec(&RegenerateRequest {
            harness: "cursor".to_string(),
        })
        .unwrap();

        let resp = app
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/harness-regenerate")
                    .header("content-type", "application/json")
                    .body(Body::from(body))
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn test_harness_regenerate_cc_creates_symlinks_count_gt_zero() {
        let tmp = set_fake_home();
        // Set up ~/.claude/ so there is a slot to write into.
        std::fs::create_dir_all(tmp.path().join(".claude")).unwrap();
        // Also create ~/.cascade/ as a second tier slot.
        std::fs::create_dir_all(tmp.path().join(".cascade")).unwrap();

        let app = test_app();
        let body = serde_json::to_vec(&RegenerateRequest {
            harness: "cc".to_string(),
        })
        .unwrap();

        let resp = app
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/harness-regenerate")
                    .header("content-type", "application/json")
                    .body(Body::from(body))
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(resp.status(), StatusCode::OK);
        let bytes = axum::body::to_bytes(resp.into_body(), 65536).await.unwrap();
        let regen: RegenerateResponse = serde_json::from_slice(&bytes).unwrap();
        assert!(
            regen.regenerated_count > 0,
            "expected regenerated_count > 0, got {}",
            regen.regenerated_count
        );
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn test_harness_regenerate_idempotent() {
        let tmp = set_fake_home();
        std::fs::create_dir_all(tmp.path().join(".claude")).unwrap();

        // First call.
        let body1 = serde_json::to_vec(&RegenerateRequest {
            harness: "cc".to_string(),
        })
        .unwrap();
        let resp1 = test_app()
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/harness-regenerate")
                    .header("content-type", "application/json")
                    .body(Body::from(body1))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(resp1.status(), StatusCode::OK);
        let b1 = axum::body::to_bytes(resp1.into_body(), 65536)
            .await
            .unwrap();
        let r1: RegenerateResponse = serde_json::from_slice(&b1).unwrap();

        // Second call (idempotent — should not error).
        let body2 = serde_json::to_vec(&RegenerateRequest {
            harness: "cc".to_string(),
        })
        .unwrap();
        let resp2 = test_app()
            .oneshot(
                Request::builder()
                    .method("POST")
                    .uri("/harness-regenerate")
                    .header("content-type", "application/json")
                    .body(Body::from(body2))
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(resp2.status(), StatusCode::OK);
        let b2 = axum::body::to_bytes(resp2.into_body(), 65536)
            .await
            .unwrap();
        let r2: RegenerateResponse = serde_json::from_slice(&b2).unwrap();

        // Both calls must succeed and produce equal counts.
        assert_eq!(r1.regenerated_count, r2.regenerated_count);
    }
}
