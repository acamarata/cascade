//! `cascade update check | apply | auto | models | --full` — update commands.
//!
//! Purpose: User-facing interface for the update pipeline (T-P4-E04-14/16).
//!
//! Subcommands:
//!   `cascade update check`              — query GitHub for a newer version; print result
//!   `cascade update apply [--yes]`      — trigger full download+verify+apply via IPC
//!   `cascade update auto [--enable|--disable]` — toggle auto_update in config.toml
//!   `cascade update models`             — refresh ~/.cascade/models.yaml from GitHub
//!
//! Unified flag:
//!   `cascade update --full`             — one-command full-stack redeploy
//!     (daemon + CLI + app + widget, codesign + launchd kickstart). Composes
//!     the existing per-component subcommands into a single orchestrated pass.
//!     Cannot be combined with a subcommand.
//!
//! Exit code `0` on success; `1` on daemon error.
//!
//! SPORT: MASTER-CLI.md — cascade update check/apply/auto (T-P4-E04-16)

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use cascade_types::ipc::{
    UpdateApplyParams, UpdateApplyResult, UpdateAutoParams, UpdateAutoResult, UpdateCheckParams,
    UpdateCheckResult,
};
use clap::{Args, Subcommand};
use std::io::Write;
use std::path::{Path, PathBuf};

use super::Command;
use crate::ipc_client::IpcClient;

const MODELS_YAML_URL: &str =
    "https://raw.githubusercontent.com/acamarata/cascade/main/models/models.yaml";
const COMPILED_MODELS_YAML: &str = include_str!("../../../../models/models.yaml");

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade update`.
#[derive(Debug, Args)]
pub struct UpdateArgs {
    /// One-command full-stack redeploy: daemon + CLI + app + widget, with
    /// codesign and launchd kickstart on macOS. Composes the existing
    /// per-component subcommands into a single orchestrated pass. Cannot be
    /// combined with a subcommand.
    #[arg(long)]
    pub full: bool,

    #[command(subcommand)]
    pub subcommand: Option<UpdateSubcommand>,
}

/// Subcommands under `cascade update`.
#[derive(Debug, Subcommand)]
pub enum UpdateSubcommand {
    /// Check for an available update without installing.
    Check,
    /// Download, verify, and apply the latest update.
    Apply {
        /// Skip the confirmation prompt.
        #[arg(long, short = 'y')]
        yes: bool,
    },
    /// Toggle auto-update in config.toml.
    Auto {
        /// Enable auto-update.
        #[arg(long, conflicts_with = "disable")]
        enable: bool,
        /// Disable auto-update.
        #[arg(long, conflicts_with = "enable")]
        disable: bool,
    },
    /// Refresh the cached fleet model roster from GitHub.
    Models,
}

#[async_trait]
impl Command for UpdateArgs {
    async fn run(&self) -> Result<()> {
        if self.full {
            if let Some(sub) = &self.subcommand {
                eprintln!(
                    "cascade update --full cannot be combined with the `{}` subcommand",
                    update_subcommand_name(sub)
                );
                std::process::exit(1);
            }
            return run_full().await;
        }
        match &self.subcommand {
            None | Some(UpdateSubcommand::Check) => run_check().await,
            Some(UpdateSubcommand::Apply { yes }) => run_apply(*yes).await,
            Some(UpdateSubcommand::Auto { enable, disable }) => {
                if *enable {
                    run_auto(true).await
                } else if *disable {
                    run_auto(false).await
                } else {
                    eprintln!("cascade update auto requires --enable or --disable");
                    std::process::exit(1);
                }
            }
            Some(UpdateSubcommand::Models) => run_models().await,
        }
    }
}

// ── check ─────────────────────────────────────────────────────────────────────

async fn run_check() -> Result<()> {
    let client = ipc_client()?;
    let result = client
        .send::<UpdateCheckParams, UpdateCheckResult>("update_check", UpdateCheckParams {})
        .await;

    match result {
        Ok(res) => {
            if res.update_available {
                let latest = res.latest_version.as_deref().unwrap_or("unknown");
                println!(
                    "Update available: {} — run `cascade update apply` to install.",
                    latest
                );
            } else {
                println!("Up to date ({})", res.current_version);
            }
            Ok(())
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => daemon_not_running(),
        Err(e) => ipc_error(e),
    }
}

// ── apply ─────────────────────────────────────────────────────────────────────

/// Core update-apply IPC call — no prompt, no `process::exit`.
///
/// Composed by both [`run_apply`] (which adds the confirmation prompt and
/// exits on error) and [`run_full`] (which handles errors inline so a
/// mid-sequence failure can be reported without leaving the daemon down).
///
/// Returns `Ok(())` when the daemon reports success (including "already up to
/// date"), or `Err` with a human-readable message on any failure.
async fn apply_step() -> Result<()> {
    println!("Checking for update…");
    let client = ipc_client()?;

    let result = client
        .send::<UpdateApplyParams, UpdateApplyResult>("update_apply", UpdateApplyParams {})
        .await;

    match result {
        Ok(res) if res.ok => {
            let version = res.installed_version.as_deref().unwrap_or("unknown");
            match res.snapshot_id.as_deref() {
                // A real install always snapshots the old binaries first; no
                // snapshot means nothing was swapped — we were already current.
                None => println!("Already up to date ({version})."),
                Some(id) => {
                    println!("Updated to {version} (snapshot: {id}). Daemon reloading.")
                }
            }
            Ok(())
        }
        Ok(res) => {
            let err = res.error.as_deref().unwrap_or("unknown error");
            Err(CascadeError::Other(format!("Update failed: {err}")))
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => Err(CascadeError::Other(
            "cascade daemon is not running. Start it with: cascade daemon start".into(),
        )),
        Err(e) => Err(CascadeError::Other(format!("Error: {e}"))),
    }
}

async fn run_apply(yes: bool) -> Result<()> {
    if !yes {
        eprint!("Download and apply the latest update? [y/N] ");
        let mut input = String::new();
        std::io::stdin()
            .read_line(&mut input)
            .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;
        if !input.trim().eq_ignore_ascii_case("y") {
            println!("Aborted.");
            return Ok(());
        }
    }

    if let Err(e) = apply_step().await {
        eprintln!("{e}");
        std::process::exit(1);
    }
    Ok(())
}

// ── auto ──────────────────────────────────────────────────────────────────────

async fn run_auto(enable: bool) -> Result<()> {
    let client = ipc_client()?;
    let params = UpdateAutoParams { enable };

    let result = client
        .send::<UpdateAutoParams, UpdateAutoResult>("update_auto", params)
        .await;

    match result {
        Ok(res) => {
            let state = if res.auto_update {
                "enabled"
            } else {
                "disabled"
            };
            println!("Auto-update {state}.");
            Ok(())
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => daemon_not_running(),
        Err(e) => ipc_error(e),
    }
}

// ── models ────────────────────────────────────────────────────────────────────

#[derive(Debug, serde::Deserialize)]
struct ModelsYaml {
    providers: Vec<ProviderBlock>,
}

#[derive(Debug, serde::Deserialize)]
struct ProviderBlock {
    name: String,
}

#[derive(Debug)]
struct ValidatedModelsYaml {
    provider_count: usize,
}

#[derive(Debug)]
struct ModelsUpdateReport {
    cache_path: PathBuf,
    changed: bool,
    compared_to: &'static str,
    provider_count: usize,
}

async fn run_models() -> Result<()> {
    match update_models_cache().await {
        Ok(report) => {
            let change = if report.changed {
                "changed"
            } else {
                "unchanged"
            };
            println!(
                "Models roster refreshed ({change} vs {}; {} providers).",
                report.compared_to, report.provider_count
            );
            println!("Cache: {}", report.cache_path.display());
            Ok(())
        }
        Err(e) => {
            eprintln!("Failed to update models.yaml: {e}");
            std::process::exit(1);
        }
    }
}

async fn update_models_cache() -> Result<ModelsUpdateReport> {
    let fetched = fetch_models_yaml().await?;
    let validated = validate_models_yaml(&fetched)?;
    let cache_path = models_cache_path();

    let (baseline, compared_to) = match std::fs::read_to_string(&cache_path) {
        Ok(cached) => (cached, "cached models.yaml"),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            (COMPILED_MODELS_YAML.to_string(), "compiled-in default")
        }
        Err(_) => (
            COMPILED_MODELS_YAML.to_string(),
            "compiled-in default (cache unreadable)",
        ),
    };
    let changed = fetched != baseline;

    write_models_cache_atomically(&cache_path, &fetched)?;

    Ok(ModelsUpdateReport {
        cache_path,
        changed,
        compared_to,
        provider_count: validated.provider_count,
    })
}

async fn fetch_models_yaml() -> Result<String> {
    let client = reqwest::Client::builder()
        .use_rustls_tls()
        .build()
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;
    let response = client
        .get(MODELS_YAML_URL)
        .header("User-Agent", "cascade-updater/1")
        .send()
        .await
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?
        .error_for_status()
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;

    response
        .text()
        .await
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))
}

fn validate_models_yaml(contents: &str) -> Result<ValidatedModelsYaml> {
    let parsed: ModelsYaml = serde_yaml::from_str(contents).map_err(|e| {
        cascade_types::error::CascadeError::Other(format!(
            "fetched models.yaml is not valid YAML with providers[].name: {e}"
        ))
    })?;

    if parsed.providers.is_empty() {
        return Err(cascade_types::error::CascadeError::Other(
            "fetched models.yaml has no providers".to_string(),
        ));
    }

    for provider in &parsed.providers {
        if provider.name.trim().is_empty() {
            return Err(cascade_types::error::CascadeError::Other(
                "fetched models.yaml contains a provider with an empty name".to_string(),
            ));
        }
    }

    Ok(ValidatedModelsYaml {
        provider_count: parsed.providers.len(),
    })
}

fn models_cache_path() -> PathBuf {
    cascade_types::paths::global_cascade_dir().join("models.yaml")
}

fn write_models_cache_atomically(cache_path: &Path, contents: &str) -> Result<()> {
    let parent = cache_path
        .parent()
        .expect("models.yaml cache path has parent");
    std::fs::create_dir_all(parent).map_err(|e| cascade_types::error::CascadeError::Io {
        path: parent.to_path_buf(),
        operation: "create models cache directory",
        source: e,
    })?;

    let tmp_path = cache_path.with_file_name("models.yaml.tmp");
    let mut tmp =
        std::fs::File::create(&tmp_path).map_err(|e| cascade_types::error::CascadeError::Io {
            path: tmp_path.clone(),
            operation: "create temporary models cache",
            source: e,
        })?;
    tmp.write_all(contents.as_bytes())
        .map_err(|e| cascade_types::error::CascadeError::Io {
            path: tmp_path.clone(),
            operation: "write temporary models cache",
            source: e,
        })?;
    tmp.sync_all()
        .map_err(|e| cascade_types::error::CascadeError::Io {
            path: tmp_path.clone(),
            operation: "sync temporary models cache",
            source: e,
        })?;
    std::fs::rename(&tmp_path, cache_path).map_err(|e| cascade_types::error::CascadeError::Io {
        path: cache_path.to_path_buf(),
        operation: "replace models cache",
        source: e,
    })?;
    Ok(())
}

// ── full (one-command full-stack redeploy) ────────────────────────────────────
//
// T-P7-E19-04: `cascade update --full` composes the existing per-component
// update logic into a single orchestrated pass:
//   1. Daemon + CLI binary update  — reuses `apply_step` (the `update apply` IPC).
//   2. Codesign swapped binaries   — macOS-only; degrades cleanly elsewhere.
//   3. launchd kickstart -k         — macOS-only; falls back to `cascade daemon
//      restart` when not launchd-managed or on non-macOS. Preferred over
//      unload/load (or bootout/bootstrap) because it atomically kills and
//      relaunches the job in one call (lesson_daemon_deploy_gotchas).
//   4. Models roster refresh        — reuses `update_models_cache`.
//   5. Widget LaunchAgent re-install — reuses `cascade widget install`.
//   6. App signature verification   — macOS-only health check (codesign --verify).
//      The app bundle is signed in CI and distributed as a DMG; `--full` verifies
//      the existing signature but does not re-download or re-sign the bundle
//      (no existing per-component CLI logic for that — re-signing locally would
//      break CI notarization).
//
// Safety:
// - Idempotent: every step is safe to re-run.
// - If `apply` fails, the daemon rolls back internally (existing IPC behaviour)
//   and the pipeline aborts before touching codesign or kickstart.
// - If `codesign` fails, the pipeline still proceeds to `kickstart` so the
//   daemon is not left down; the codesign failure is surfaced as an error.
// - `models` and `widget` failures are non-fatal warnings — the daemon is
//   already running by then.

/// Return the CLI name for an [`UpdateSubcommand`] (used in error messages).
fn update_subcommand_name(sub: &UpdateSubcommand) -> &'static str {
    match sub {
        UpdateSubcommand::Check => "check",
        UpdateSubcommand::Apply { .. } => "apply",
        UpdateSubcommand::Auto { .. } => "auto",
        UpdateSubcommand::Models => "models",
    }
}

/// Orchestrate the full-stack redeploy pipeline.
async fn run_full() -> Result<()> {
    println!("cascade update --full: full-stack redeploy\n");

    // ── 1. Daemon + CLI binary update (hard fail — daemon rolls back) ──────
    println!("[1/6] daemon + CLI update…");
    apply_step().await?;

    // ── 2. Codesign swapped binaries (macOS-only) ──────────────────────────
    println!("\n[2/6] codesign…");
    let mut errors: Vec<String> = Vec::new();
    codesign_step(&mut errors);

    // ── 3. Daemon service restart (must succeed — daemon must come back) ───
    println!("\n[3/6] daemon service restart…");
    if let Err(e) = kickstart_daemon().await {
        eprintln!("daemon restart failed: {e}");
        errors.push(format!("daemon restart: {e}"));
    }

    // ── 4. Models refresh (non-fatal) ──────────────────────────────────────
    println!("\n[4/6] models refresh…");
    let mut warnings: Vec<String> = Vec::new();
    match update_models_cache().await {
        Ok(report) => {
            let change = if report.changed {
                "changed"
            } else {
                "unchanged"
            };
            println!(
                "  Models roster refreshed ({change} vs {}; {} providers).",
                report.compared_to, report.provider_count
            );
        }
        Err(e) => {
            warnings.push(format!("models refresh: {e}"));
        }
    }

    // ── 5. Widget re-install (non-fatal) ───────────────────────────────────
    println!("\n[5/6] widget re-install…");
    if let Err(e) = crate::cmd::widget::WidgetInstallArgs.run().await {
        warnings.push(format!("widget re-install: {e}"));
    }

    // ── 6. App signature verification (macOS-only, non-fatal) ──────────────
    println!("\n[6/6] app signature check…");
    app_verify_step(&mut warnings);

    // ── Report ─────────────────────────────────────────────────────────────
    full_redeploy_result(errors, warnings)
}

/// Build the final `Result` from collected errors and warnings.
///
/// Errors (critical steps that failed) cause the overall result to be `Err` —
/// a mid-sequence failure surfaces an error rather than reporting success.
/// Warnings (non-fatal steps) are printed but do not change the result.
fn full_redeploy_result(errors: Vec<String>, warnings: Vec<String>) -> Result<()> {
    for w in &warnings {
        eprintln!("warning: {w}");
    }
    if errors.is_empty() {
        if warnings.is_empty() {
            println!("\ncascade update --full: complete.");
        } else {
            println!(
                "\ncascade update --full: complete with {} warning(s).",
                warnings.len()
            );
        }
        Ok(())
    } else {
        for e in &errors {
            eprintln!("error: {e}");
        }
        Err(CascadeError::Other(format!(
            "full-stack redeploy completed with {} error(s)",
            errors.len()
        )))
    }
}

// ── codesign helpers ──────────────────────────────────────────────────────────

/// Run the codesign step, pushing any failure into `errors`.
///
/// On macOS: ad-hoc signs the swapped `cascaded` and `cascade` binaries.
/// On non-macOS: prints a notice and skips (degrades cleanly).
#[cfg(target_os = "macos")]
fn codesign_step(errors: &mut Vec<String>) {
    match codesign_swapped_binaries() {
        Ok(()) => {}
        Err(e) => {
            eprintln!("codesign failed: {e}");
            eprintln!("proceeding to daemon restart to avoid leaving the daemon down…");
            errors.push(format!("codesign: {e}"));
        }
    }
}

#[cfg(not(target_os = "macos"))]
fn codesign_step(_errors: &mut Vec<String>) {
    println!("  macOS-only, skipped.");
}

/// Ad-hoc codesign the swapped `cascaded` and `cascade` binaries (macOS).
///
/// Uses `codesign --force --sign -` (ad-hoc). Release binaries downloaded by
/// `update apply` are already signed by CI — re-signing ad-hoc is harmless and
/// idempotent. Locally-built binaries require this to run without Gatekeeper
/// prompts.
///
/// Fails loudly if the `codesign` tool is not found — never silently skips.
#[cfg(target_os = "macos")]
fn codesign_swapped_binaries() -> Result<()> {
    let daemon_bin = crate::daemon_install::resolve_binary()?;
    let cli_bin = resolve_cascade_cli_binary(&daemon_bin)?;
    for (label, bin) in [("cascaded", &daemon_bin), ("cascade", &cli_bin)] {
        print!("  codesign {label}… ");
        let out = std::process::Command::new("codesign")
            .args(["--force", "--sign", "-"])
            .arg(bin)
            .output()
            .map_err(|e| {
                CascadeError::Other(format!(
                    "codesign tool not available: {e}. Install Xcode Command Line Tools."
                ))
            })?;
        if !out.status.success() {
            return Err(CascadeError::Other(format!(
                "codesign failed for {label}: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            )));
        }
        println!("ok");
    }
    Ok(())
}

/// Resolve the `cascade` CLI binary next to `cascaded`, or via `which` (macOS).
#[cfg(target_os = "macos")]
fn resolve_cascade_cli_binary(daemon_bin: &Path) -> Result<PathBuf> {
    if let Some(parent) = daemon_bin.parent() {
        let sibling = parent.join("cascade");
        if sibling.is_file() {
            return Ok(sibling);
        }
    }
    let out = std::process::Command::new("which")
        .arg("cascade")
        .output()
        .map_err(|e| CascadeError::Other(format!("which cascade failed: {e}")))?;
    if out.status.success() {
        let s = String::from_utf8_lossy(&out.stdout)
            .lines()
            .next()
            .unwrap_or("")
            .trim()
            .to_string();
        if !s.is_empty() {
            return Ok(PathBuf::from(s));
        }
    }
    Err(CascadeError::Other(
        "cascade CLI binary not found; cannot codesign".into(),
    ))
}

// ── launchd kickstart helpers ─────────────────────────────────────────────────

/// Restart the daemon service.
///
/// macOS: `launchctl kickstart -k gui/<uid>/<label>` — atomically kills and
/// relaunches the job. Falls back to `cascade daemon restart` (bare process
/// stop+start) if kickstart fails (e.g. daemon not installed as a LaunchAgent).
///
/// Non-macOS: uses `cascade daemon restart` directly.
#[cfg(target_os = "macos")]
async fn kickstart_daemon() -> Result<()> {
    let label = crate::daemon_install::macos_service_label();
    let uid = user_uid()?;
    let target = format!("gui/{uid}/{label}");

    print!("  launchctl kickstart -k {target}… ");
    let out = std::process::Command::new("launchctl")
        .args(["kickstart", "-k"])
        .arg(&target)
        .output()
        .map_err(|e| CascadeError::Other(format!("launchctl not available: {e}")))?;

    if out.status.success() {
        println!("ok");
        return Ok(());
    }

    let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
    println!("failed ({stderr}); falling back to cascade daemon restart…");
    crate::cmd::daemon::DaemonRestartArgs.run().await
}

#[cfg(not(target_os = "macos"))]
async fn kickstart_daemon() -> Result<()> {
    println!("  using cascade daemon restart…");
    crate::cmd::daemon::DaemonRestartArgs.run().await
}

/// Get the current user's UID via `id -u` (macOS).
#[cfg(target_os = "macos")]
fn user_uid() -> Result<String> {
    let out = std::process::Command::new("id")
        .arg("-u")
        .output()
        .map_err(|e| CascadeError::Other(format!("id -u failed: {e}")))?;
    if !out.status.success() {
        return Err(CascadeError::Other(format!(
            "id -u failed: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )));
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_string())
}

// ── app signature verification ────────────────────────────────────────────────

/// Run the app signature verification step, pushing any failure into `warnings`.
#[cfg(target_os = "macos")]
fn app_verify_step(warnings: &mut Vec<String>) {
    match verify_app_signature() {
        Ok(()) => {}
        Err(e) => warnings.push(format!("app signature: {e}")),
    }
}

#[cfg(not(target_os = "macos"))]
fn app_verify_step(_warnings: &mut Vec<String>) {
    println!("  macOS-only, skipped.");
}

/// Verify the signature of `~/Applications/Cascade.app` if present (macOS).
///
/// Health check only — does not re-sign or re-deploy the bundle. The app is
/// signed in CI and distributed as a DMG; re-signing locally would break
/// notarization.
#[cfg(target_os = "macos")]
fn verify_app_signature() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let app = home.join("Applications").join("Cascade.app");
    if !app.exists() {
        println!(
            "  Cascade.app not installed at {}; skipping.",
            app.display()
        );
        return Ok(());
    }
    print!("  codesign --verify --deep --strict {}… ", app.display());
    let out = std::process::Command::new("codesign")
        .args(["--verify", "--deep", "--strict"])
        .arg(&app)
        .output()
        .map_err(|e| CascadeError::Other(format!("codesign tool not available: {e}")))?;
    if out.status.success() {
        println!("ok");
        Ok(())
    } else {
        Err(CascadeError::Other(format!(
            "signature verification failed: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )))
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

fn ipc_client() -> Result<IpcClient> {
    IpcClient::new().map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))
}

fn daemon_not_running() -> Result<()> {
    eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
    std::process::exit(1);
}

fn ipc_error(e: crate::ipc_client::IpcClientError) -> Result<()> {
    eprintln!("Error: {e}");
    std::process::exit(1);
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use clap::Parser;

    #[derive(Parser)]
    struct Cli {
        #[command(subcommand)]
        cmd: crate::cmd::Commands,
    }

    #[test]
    fn update_check_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "check"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Check)
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_apply_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "apply"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Apply { yes: false })
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_apply_yes_flag_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "apply", "--yes"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Apply { yes: true })
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_enable_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "auto", "--enable"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => match args.subcommand {
                Some(super::UpdateSubcommand::Auto { enable, disable }) => {
                    assert!(enable);
                    assert!(!disable);
                }
                _ => panic!("expected Auto"),
            },
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_disable_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "auto", "--disable"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => match args.subcommand {
                Some(super::UpdateSubcommand::Auto { enable, disable }) => {
                    assert!(!enable);
                    assert!(disable);
                }
                _ => panic!("expected Auto"),
            },
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_models_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "models"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Models)
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_enable_disable_conflict_fails() {
        let result = Cli::try_parse_from(["cascade", "update", "auto", "--enable", "--disable"]);
        assert!(result.is_err(), "enable and disable must conflict");
    }

    #[test]
    fn update_no_subcommand_defaults_to_check() {
        let cli = Cli::try_parse_from(["cascade", "update"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(args.subcommand.is_none());
            }
            _ => panic!("expected Update"),
        }
    }

    // ── --full flag tests (T-P7-E19-04) ──────────────────────────────────────

    #[test]
    fn update_full_flag_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "--full"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(args.full, "--full flag must be true");
                assert!(args.subcommand.is_none(), "no subcommand expected");
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_full_flag_false_by_default() {
        let cli = Cli::try_parse_from(["cascade", "update", "check"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(!args.full, "--full must default to false");
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_full_with_subcommand_parses_both() {
        // clap allows --full + subcommand simultaneously; the runtime dispatch
        // in `run()` rejects this combination. Here we only verify parsing.
        let cli = Cli::try_parse_from(["cascade", "update", "--full", "apply"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(args.full, "--full flag must be true");
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Apply { .. })
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_subcommand_name_returns_correct_labels() {
        use super::UpdateSubcommand;
        assert_eq!(
            super::update_subcommand_name(&UpdateSubcommand::Check),
            "check"
        );
        assert_eq!(
            super::update_subcommand_name(&UpdateSubcommand::Apply { yes: false }),
            "apply"
        );
        assert_eq!(
            super::update_subcommand_name(&UpdateSubcommand::Auto {
                enable: false,
                disable: false
            }),
            "auto"
        );
        assert_eq!(
            super::update_subcommand_name(&UpdateSubcommand::Models),
            "models"
        );
    }

    // ── full_redeploy_result: mid-sequence failure surfaces error ────────────

    #[test]
    fn full_redeploy_result_ok_when_no_errors_no_warnings() {
        let result = super::full_redeploy_result(vec![], vec![]);
        assert!(result.is_ok(), "no errors + no warnings => Ok");
    }

    #[test]
    fn full_redeploy_result_ok_with_only_warnings() {
        let result =
            super::full_redeploy_result(vec![], vec!["models refresh: network".to_string()]);
        assert!(
            result.is_ok(),
            "warnings only (no errors) => Ok, not a hard failure"
        );
    }

    #[test]
    fn full_redeploy_result_err_when_errors_present() {
        // A mid-sequence failure (e.g. codesign) pushes into `errors`.
        // The overall result must be Err — never report success.
        let result = super::full_redeploy_result(
            vec!["codesign: no identity found".to_string()],
            vec!["models refresh: network".to_string()],
        );
        assert!(result.is_err(), "errors present => Err, not success");
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("1 error"),
            "error count must appear in message: {msg}"
        );
    }

    #[test]
    fn full_redeploy_result_err_message_counts_multiple_errors() {
        let result = super::full_redeploy_result(
            vec![
                "codesign: boom".to_string(),
                "daemon restart: failed".to_string(),
            ],
            vec![],
        );
        assert!(result.is_err());
        let msg = result.unwrap_err().to_string();
        assert!(
            msg.contains("2 error"),
            "error count must reflect 2 errors: {msg}"
        );
    }
}
