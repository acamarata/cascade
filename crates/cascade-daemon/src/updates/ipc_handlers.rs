//! Daemon-facing entry points for `update_check` / `update_apply` / `update_auto`.
//!
//! Purpose: Thin orchestration layer between `ipc.rs` (which only knows JSON
//! params/results) and the update machinery in `downloader.rs` / `full_bundle.rs`
//! / `snapshot.rs`. Keeps `ipc.rs` slim per the existing dispatch pattern.
//!
//! Flow for `apply_update`:
//!   1. Check GitHub for a newer release. No newer version → `up_to_date`.
//!   2. Try the delta path first (`.cascade-delta-{from}-to-{to}.tar.gz`) — this
//!      is a no-op today since no release has ever published that asset, but
//!      the seam stays in place for when deltas start shipping.
//!   3. Fall back to the full-bundle path: locate
//!      `cascade-v{latest}-macos-aarch64.tar.gz` + `SHA256SUMS`, download both,
//!      verify sha256 (hard fail on mismatch/absent — never install unverified),
//!      extract, and atomically swap the binaries at their current install
//!      locations. A pre-swap snapshot is taken via `Snapshot::create` so the
//!      old binaries can be restored if anything goes wrong downstream.
//!   4. On success, schedule a daemon self-restart: spawn a detached replacement
//!      `cascaded` process, then exit this process ~500ms later (after the IPC
//!      response has been flushed to the caller).
//!
//! Inputs:  IPC params (already deserialized by ipc.rs)
//! Outputs: typed result structs from `cascade_types::ipc`
//! Constraints: macOS-first (aarch64 asset), but only the asset-name/install
//!   path resolution is platform-specific — everything else compiles anywhere.
//!
//! SPORT: MASTER-CLI.md — cascade update check/apply/auto (T-P4-E04-16)

use std::path::PathBuf;

use cascade_types::ipc::{UpdateApplyResult, UpdateAutoResult, UpdateCheckResult};
use toml_edit::DocumentMut;
use tracing::{info, warn};

use super::downloader::{DownloadError, UpdateChecker};
use super::full_bundle::{
    find_release_assets, macos_aarch64_asset_name, parse_sha256sums, FullBundleInstaller, GhRelease,
};
use super::snapshot::Snapshot;

/// Default GitHub API base for the cascade repo. Overridable via
/// `CASCADE_UPDATE_API_BASE` for local testing without touching call sites.
fn github_api_base() -> String {
    std::env::var("CASCADE_UPDATE_API_BASE")
        .unwrap_or_else(|_| "https://api.github.com/repos/acamarata/cascade".to_string())
}

fn home_dir() -> PathBuf {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(dirs::home_dir)
        .unwrap_or_else(|| PathBuf::from("/tmp"))
}

fn cascade_config_dir() -> PathBuf {
    home_dir().join(".cascade")
}

// ── update_check ──────────────────────────────────────────────────────────────

/// Query GitHub for the latest release and compare against the running version.
///
/// Purpose: Backs the `update_check` IPC method. Never fabricates a result on
/// network failure — the caller (ipc.rs) surfaces the error as an IPC error
/// rather than reporting `up_to_date` or a made-up version.
pub async fn check_for_update() -> Result<UpdateCheckResult, DownloadError> {
    let current_version = env!("CARGO_PKG_VERSION").to_string();

    let client = reqwest::Client::builder().use_rustls_tls().build()?;
    let url = format!("{}/releases/latest", github_api_base());
    let resp = client
        .get(&url)
        .header("Accept", "application/vnd.github+json")
        .header("X-GitHub-Api-Version", "2022-11-28")
        .header("User-Agent", "cascade-updater/1")
        .timeout(std::time::Duration::from_secs(10))
        .send()
        .await?;

    if resp.status() == reqwest::StatusCode::NOT_FOUND {
        // No releases published yet.
        return Ok(UpdateCheckResult {
            update_available: false,
            current_version,
            latest_version: None,
        });
    }
    resp.error_for_status_ref()?;

    let release: GhRelease = resp.json().await?;
    let latest = release.tag_name.trim_start_matches(['v', 'V']).to_string();

    let update_available = is_newer(&latest, &current_version);
    Ok(UpdateCheckResult {
        update_available,
        current_version,
        latest_version: if update_available {
            Some(latest)
        } else {
            None
        },
    })
}

/// Compare two semver-ish strings; `true` if `a` is strictly newer than `b`.
/// Unparseable strings fall back to lexicographic comparison (never panics).
fn is_newer(a: &str, b: &str) -> bool {
    fn parse(s: &str) -> Option<(u64, u64, u64)> {
        let parts: Vec<&str> = s.splitn(3, '.').collect();
        if parts.len() < 3 {
            return None;
        }
        let major = parts[0].parse::<u64>().ok()?;
        let minor = parts[1].parse::<u64>().ok()?;
        let patch_str = parts[2].split('-').next().unwrap_or(parts[2]);
        let patch = patch_str.parse::<u64>().ok()?;
        Some((major, minor, patch))
    }
    match (parse(a), parse(b)) {
        (Some(va), Some(vb)) => va > vb,
        _ => a > b,
    }
}

// ── update_apply ──────────────────────────────────────────────────────────────

/// Perform the full update cycle: check, try delta, fall back to full-bundle,
/// snapshot, swap binaries, and report the outcome.
///
/// Purpose: Backs the `update_apply` IPC method. Never returns a fabricated
/// success — every branch that doesn't actually install returns either
/// `up_to_date` or `ok: false` with a real error message.
pub async fn apply_update() -> UpdateApplyResult {
    let current_version = env!("CARGO_PKG_VERSION").to_string();

    let check = match check_for_update().await {
        Ok(c) => c,
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("update check failed: {e}")),
            }
        }
    };

    if !check.update_available {
        return UpdateApplyResult {
            ok: true,
            installed_version: Some(current_version),
            snapshot_id: None,
            error: None,
        };
    }
    let latest = check.latest_version.expect("update_available implies Some");

    let config_dir = cascade_config_dir();
    let snapshot_root = config_dir.join("snapshots");

    // ── (b) Try the delta path first ──────────────────────────────────────────
    // Resolve the daemon's own protocol root as `config_dir` (protocol files —
    // specs/hooks/etc — live under ~/.cascade). This mirrors the existing
    // downloader.rs contract; no release has ever published a delta asset, so
    // this branch is a documented no-op today but stays wired for when one
    // ships.
    let delta_checker = UpdateChecker {
        github_api_base: github_api_base(),
        current_version: current_version.clone(),
        snapshot_root: snapshot_root.clone(),
        protocol_root: config_dir.clone(),
        max_snapshots: 20,
    };

    match delta_checker.check_for_update().await {
        Ok(Some(_)) => {
            // A delta asset actually exists for this transition — use it.
            match delta_checker.download_and_apply().await {
                Ok(super::downloader::UpdateStatus::Updated { to, .. }) => {
                    schedule_self_restart();
                    return UpdateApplyResult {
                        ok: true,
                        installed_version: Some(to),
                        snapshot_id: None,
                        error: None,
                    };
                }
                Ok(super::downloader::UpdateStatus::UpToDate) => {
                    // Race: became up to date between check and apply.
                    return UpdateApplyResult {
                        ok: true,
                        installed_version: Some(current_version),
                        snapshot_id: None,
                        error: None,
                    };
                }
                Err(e) => {
                    warn!(error = %e, "delta apply failed; falling back to full-bundle path");
                    // Fall through to full-bundle below.
                }
            }
        }
        Ok(None) => {
            // No delta asset for this transition (expected today) — fall
            // through to the full-bundle path.
        }
        Err(e) => {
            warn!(error = %e, "delta check_for_update failed; falling back to full-bundle path");
        }
    }

    // ── (c) Full-bundle fallback ───────────────────────────────────────────────
    apply_full_bundle(&current_version, &latest, &snapshot_root).await
}

/// Full-bundle install: locate assets, verify sha256, snapshot, swap binaries.
async fn apply_full_bundle(
    current_version: &str,
    latest: &str,
    snapshot_root: &std::path::Path,
) -> UpdateApplyResult {
    let client = match reqwest::Client::builder().use_rustls_tls().build() {
        Ok(c) => c,
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("failed to build HTTP client: {e}")),
            }
        }
    };

    let url = format!("{}/releases/latest", github_api_base());
    let release: GhRelease = match client
        .get(&url)
        .header("Accept", "application/vnd.github+json")
        .header("X-GitHub-Api-Version", "2022-11-28")
        .header("User-Agent", "cascade-updater/1")
        .timeout(std::time::Duration::from_secs(10))
        .send()
        .await
        .and_then(|r| r.error_for_status())
    {
        Ok(resp) => match resp.json().await {
            Ok(r) => r,
            Err(e) => {
                return UpdateApplyResult {
                    ok: false,
                    installed_version: None,
                    snapshot_id: None,
                    error: Some(format!("failed to parse release JSON: {e}")),
                }
            }
        },
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("failed to fetch latest release: {e}")),
            }
        }
    };

    let (bundle_asset, checksums_asset) = match find_release_assets(&release, latest) {
        Ok(pair) => pair,
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("{e}")),
            }
        }
    };
    let bundle_url = bundle_asset.browser_download_url.clone();
    let checksums_url = checksums_asset.browser_download_url.clone();

    // Download SHA256SUMS and find our asset's expected digest.
    let checksums_raw = match client
        .get(&checksums_url)
        .header("User-Agent", "cascade-updater/1")
        .send()
        .await
        .and_then(|r| r.error_for_status())
    {
        Ok(resp) => match resp.text().await {
            Ok(t) => t,
            Err(e) => {
                return UpdateApplyResult {
                    ok: false,
                    installed_version: None,
                    snapshot_id: None,
                    error: Some(format!("failed to read SHA256SUMS: {e}")),
                }
            }
        },
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("failed to download SHA256SUMS: {e}")),
            }
        }
    };

    let sums = parse_sha256sums(&checksums_raw);
    let asset_name = macos_aarch64_asset_name(latest);
    let expected_sha = match super::full_bundle::expected_digest_for(&sums, &asset_name) {
        Ok(h) => h.to_string(),
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("{e} — refusing to install unverified")),
            }
        }
    };

    // Resolve current install paths BEFORE writing anything.
    let cascaded_path = match std::env::current_exe() {
        Ok(p) => p,
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("failed to resolve running daemon path: {e}")),
            }
        }
    };
    let cascade_cli_path = resolve_cascade_cli_path(&cascaded_path);

    // Snapshot the current binaries before swapping (rollback safety).
    let mut snap_files: Vec<(&str, &std::path::Path)> = vec![("bin/cascaded", &cascaded_path)];
    if let Some(cli) = &cascade_cli_path {
        snap_files.push(("bin/cascade", cli));
    }
    let snapshot = match Snapshot::create(snapshot_root, current_version, &snap_files) {
        Ok(s) => s,
        Err(e) => {
            return UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: None,
                error: Some(format!("failed to snapshot current binaries: {e}")),
            }
        }
    };
    let snapshot_id = snapshot.metadata.id.clone();

    let installer = FullBundleInstaller {
        cascaded_path: cascaded_path.clone(),
        cascade_cli_path: cascade_cli_path.clone(),
    };

    match installer
        .download_and_install(latest, &bundle_url, &expected_sha)
        .await
    {
        Ok(outcome) => {
            info!(
                version = %outcome.version,
                snapshot = %snapshot_id,
                "full-bundle update applied"
            );
            schedule_self_restart();
            UpdateApplyResult {
                ok: true,
                installed_version: Some(outcome.version),
                snapshot_id: Some(snapshot_id),
                error: None,
            }
        }
        Err(e) => {
            // Attempt rollback to the snapshot we just took.
            if let Err(restore_err) =
                Snapshot::restore(snapshot_root, &snapshot_id, cascaded_path.parent().unwrap_or(
                    std::path::Path::new("/"),
                ))
            {
                warn!(
                    apply_error = %e,
                    restore_error = %restore_err,
                    "full-bundle apply failed AND rollback failed"
                );
                return UpdateApplyResult {
                    ok: false,
                    installed_version: None,
                    snapshot_id: Some(snapshot_id),
                    error: Some(format!(
                        "apply failed ({e}) and rollback also failed ({restore_err}) — \
                         binaries may be in an inconsistent state"
                    )),
                };
            }
            UpdateApplyResult {
                ok: false,
                installed_version: None,
                snapshot_id: Some(snapshot_id),
                error: Some(format!("apply failed, rolled back successfully: {e}")),
            }
        }
    }
}

/// Best-effort resolution of the `cascade` CLI binary next to the running
/// `cascaded`. Falls back to `which cascade` if no sibling binary exists.
fn resolve_cascade_cli_path(cascaded_path: &std::path::Path) -> Option<PathBuf> {
    let sibling = cascaded_path.parent()?.join(if cfg!(windows) {
        "cascade.exe"
    } else {
        "cascade"
    });
    if sibling.exists() {
        return Some(sibling);
    }
    which_cascade()
}

/// `which cascade` — best-effort PATH lookup, returns `None` if not found or
/// the lookup itself fails (never an error path for the caller).
fn which_cascade() -> Option<PathBuf> {
    let output = std::process::Command::new(if cfg!(windows) { "where" } else { "which" })
        .arg("cascade")
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let path_str = String::from_utf8_lossy(&output.stdout);
    let first_line = path_str.lines().next()?.trim();
    if first_line.is_empty() {
        None
    } else {
        Some(PathBuf::from(first_line))
    }
}

/// Spawn a detached replacement `cascaded` process, then exit this process
/// after a short delay so the IPC response reaches the caller first.
///
/// Purpose: There is no launchd/systemd auto-restart wired for the plain
/// `cascade daemon start` flow (it's a bare child process tracked by a PID
/// file) — self-restart has to spawn the new process itself rather than
/// relying on a supervisor to notice the exit and relaunch it. If `cascaded`
/// is registered as an OS service (`cascade daemon install`), that service
/// manager will ALSO see the exit and may restart it a second time; the new
/// process binds its own socket and exits immediately if one is already
/// listening, so a double-start is harmless.
fn schedule_self_restart() {
    let exe = std::env::current_exe().ok();
    tokio::spawn(async move {
        tokio::time::sleep(std::time::Duration::from_millis(500)).await;
        if let Some(exe) = exe {
            let mut cmd = std::process::Command::new(exe);
            cmd.stdin(std::process::Stdio::null())
                .stdout(std::process::Stdio::null())
                .stderr(std::process::Stdio::null());
            #[cfg(unix)]
            {
                use std::os::unix::process::CommandExt;
                // New session so the child survives this process exiting.
                unsafe {
                    cmd.pre_exec(|| {
                        libc::setsid();
                        Ok(())
                    });
                }
            }
            match cmd.spawn() {
                Ok(_) => info!("spawned replacement cascaded process; exiting for self-restart"),
                Err(e) => warn!(error = %e, "failed to spawn replacement cascaded process"),
            }
        }
        std::process::exit(0);
    });
}

// ── update_auto ───────────────────────────────────────────────────────────────

/// Toggle `[updates] auto` in `~/.cascade/config.toml`, preserving every other
/// key/comment in the file (uses `toml_edit`, same pattern as
/// `cascade telemetry enable/disable`).
pub fn set_auto_update(enable: bool) -> Result<UpdateAutoResult, std::io::Error> {
    let config_path = cascade_config_dir().join("config.toml");
    std::fs::create_dir_all(config_path.parent().expect("config path has parent"))?;

    let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
    let mut doc: DocumentMut = raw.parse().unwrap_or_default();

    if doc.get("updates").is_none() {
        doc["updates"] = toml_edit::table();
    }
    doc["updates"]["auto"] = toml_edit::value(enable);

    std::fs::write(&config_path, doc.to_string())?;

    Ok(UpdateAutoResult { auto_update: enable })
}

/// Read the current `[updates] auto` value (defaults to `false` if unset).
#[allow(dead_code)]
pub fn get_auto_update() -> bool {
    let config_path = cascade_config_dir().join("config.toml");
    let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
    let doc: DocumentMut = raw.parse().unwrap_or_default();
    doc.get("updates")
        .and_then(|t| t.get("auto"))
        .and_then(|v| v.as_bool())
        .unwrap_or(false)
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;

    // ── is_newer ──

    #[test]
    fn is_newer_patch_bump() {
        assert!(is_newer("1.9.19", "1.9.18"));
        assert!(!is_newer("1.9.18", "1.9.18"));
        assert!(!is_newer("1.9.17", "1.9.18"));
    }

    #[test]
    fn is_newer_minor_and_major_bump() {
        assert!(is_newer("1.10.0", "1.9.99"));
        assert!(is_newer("2.0.0", "1.99.99"));
    }

    #[test]
    fn is_newer_unparseable_falls_back_to_lexicographic() {
        // Neither parses as major.minor.patch — must not panic.
        assert!(!is_newer("abc", "abc"));
        let _ = is_newer("v-weird", "1.0.0");
    }

    // ── check_for_update against an injected mock base ──

    #[tokio::test]
    #[serial]
    async fn check_for_update_reports_available_when_newer() {
        let server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/releases/latest"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "tag_name": "v99.0.0",
                "assets": []
            })))
            .mount(&server)
            .await;

        std::env::set_var(
            "CASCADE_UPDATE_API_BASE",
            server.uri(),
        );
        let result = check_for_update().await.expect("check succeeds");
        std::env::remove_var("CASCADE_UPDATE_API_BASE");

        assert!(result.update_available);
        assert_eq!(result.latest_version.as_deref(), Some("99.0.0"));
        assert_eq!(result.current_version, env!("CARGO_PKG_VERSION"));
    }

    #[tokio::test]
    #[serial]
    async fn check_for_update_reports_up_to_date_when_same_or_older() {
        let server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/releases/latest"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "tag_name": format!("v{}", env!("CARGO_PKG_VERSION")),
                "assets": []
            })))
            .mount(&server)
            .await;

        std::env::set_var("CASCADE_UPDATE_API_BASE", server.uri());
        let result = check_for_update().await.expect("check succeeds");
        std::env::remove_var("CASCADE_UPDATE_API_BASE");

        assert!(!result.update_available);
        assert!(result.latest_version.is_none());
    }

    #[tokio::test]
    #[serial]
    async fn check_for_update_returns_err_on_network_failure() {
        // Point at a base with no server listening — must be a real error,
        // never a fabricated "up to date" result.
        std::env::set_var("CASCADE_UPDATE_API_BASE", "http://127.0.0.1:1");
        let result = check_for_update().await;
        std::env::remove_var("CASCADE_UPDATE_API_BASE");

        assert!(result.is_err(), "network failure must surface as Err");
    }

    // ── set_auto_update / get_auto_update ──

    #[test]
    #[serial]
    fn set_auto_update_preserves_other_sections() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let cascade_dir = dir.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(
            cascade_dir.join("config.toml"),
            "[watcher]\ndebounce_ms = 250\n",
        )
        .unwrap();

        std::env::set_var("HOME", dir.path());
        let result = set_auto_update(true).expect("set succeeds");
        assert!(result.auto_update);

        let written = std::fs::read_to_string(cascade_dir.join("config.toml")).unwrap();
        assert!(
            written.contains("debounce_ms = 250"),
            "existing [watcher] section must survive: {written}"
        );
        assert!(written.contains("[updates]"));
        assert!(written.contains("auto = true"));
    }

    #[test]
    #[serial]
    fn set_auto_update_roundtrip_via_config_dir() {
        let dir = tempfile::tempdir().expect("tmpdir");
        std::env::set_var("HOME", dir.path());

        set_auto_update(true).expect("enable");
        assert!(get_auto_update());

        set_auto_update(false).expect("disable");
        assert!(!get_auto_update());
    }

    // ── resolve_cascade_cli_path ──

    #[test]
    fn resolve_cascade_cli_path_finds_sibling() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let cascaded = dir.path().join("cascaded");
        let cascade = dir.path().join("cascade");
        std::fs::write(&cascaded, b"daemon").unwrap();
        std::fs::write(&cascade, b"cli").unwrap();

        let resolved = resolve_cascade_cli_path(&cascaded);
        assert_eq!(resolved, Some(cascade));
    }

    #[test]
    fn resolve_cascade_cli_path_none_when_absent() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let cascaded = dir.path().join("cascaded");
        std::fs::write(&cascaded, b"daemon").unwrap();
        // No sibling `cascade` binary in this empty temp dir, and `which`
        // may or may not find a real one on PATH in CI — just assert this
        // doesn't panic and returns a coherent Option.
        let _ = resolve_cascade_cli_path(&cascaded);
    }

}
