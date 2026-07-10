//! `cascade plugin` — manage installed WASM plugins.
//!
//! Purpose: list, enable, disable, scaffold, and test plugins.
//!   - list/enable/disable/info operate on `~/.cascade/plugins/` (no daemon IPC required).
//!   - new: scaffolds a new plugin project from `templates/plugin-template/` using
//!     cargo-generate (vendored template, no network). (T-P4-E03-11)
//!   - test: runs `cargo build --target wasm32-wasip1` + `cargo test` in a plugin project
//!     and formats the pass/fail output. (T-P4-E03-12)
//!
//! Inputs:  subcommand + optional plugin ID arg.
//!
//! Outputs: formatted table (list) or confirmation message (enable/disable/new/test).
//!
//! Constraints:
//!   - Operates on `~/.cascade/plugins/` by default; `--plugins-dir` overrides for testing.
//!   - Enable/disable writes or removes a `.disabled` marker file in the plugin directory.
//!   - Enable/disable require the plugin directory to exist (exact `id` match).
//!   - `cascade plugin new` uses cargo-generate with the vendored template directory.
//!   - `cascade plugin test` builds with wasm32-wasip1 first; if the toolchain is absent,
//!     it prints a graceful message and exits 0 rather than erroring.
//!
//! SPORT: cascade-cli / plugin subcommand (T-P4-E03-09, T-P4-E03-11, T-P4-E03-12)

use std::path::{Path, PathBuf};
use std::process::Command as StdCommand;

use async_trait::async_trait;
use cascade_plugins::capability::Capability;
use cascade_plugins::grants::GrantStore;
use cascade_plugins::loader::{PluginLoadError, PluginLoader};
use cascade_plugins::manifest::PluginKind;
use cascade_plugins::signing::TrustedPublishers;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};

use super::Command;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade plugin`.
#[derive(Debug, Args)]
pub struct PluginArgs {
    /// Override the plugins directory (default: `~/.cascade/plugins/`).
    ///
    /// Useful for testing with a custom directory without affecting the real
    /// plugin installation.
    #[arg(long, global = true)]
    pub plugins_dir: Option<PathBuf>,

    #[command(subcommand)]
    pub command: PluginCommands,
}

/// Subcommands under `cascade plugin`.
#[derive(Debug, Subcommand)]
pub enum PluginCommands {
    /// List installed plugins with id, name, version, kind, and status.
    List(ListArgs),
    /// Enable a disabled plugin (removes `.disabled` marker).
    Enable(EnableArgs),
    /// Disable a plugin (writes `.disabled` marker; does not uninstall).
    Disable(DisableArgs),
    /// Show detailed information about a plugin.
    Info(InfoArgs),
    /// Scaffold a new plugin project using the vendored cargo-generate template.
    New(NewArgs),
    /// Build and test a plugin project; outputs PASS/FAIL per test.
    Test(TestArgs),
    /// Revoke a granted capability from a plugin.
    Revoke(RevokeArgs),
    /// Trust a plugin publisher by adding their Ed25519 public key.
    Trust(TrustArgs),
    /// Grant a capability to a plugin (records user consent).
    Grant(GrantArgs),
}

/// Arguments for `cascade plugin list`.
#[derive(Debug, Args)]
pub struct ListArgs {
    /// Output as JSON instead of a human-readable table.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade plugin enable`.
#[derive(Debug, Args)]
pub struct EnableArgs {
    /// Plugin ID to enable (e.g. `com.example.my-plugin`).
    pub id: String,
}

/// Arguments for `cascade plugin disable`.
#[derive(Debug, Args)]
pub struct DisableArgs {
    /// Plugin ID to disable (e.g. `com.example.my-plugin`).
    pub id: String,
}

/// Arguments for `cascade plugin info`.
#[derive(Debug, Args)]
pub struct InfoArgs {
    /// Plugin ID to show info for.
    pub id: String,
}

/// Arguments for `cascade plugin new`.
#[derive(Debug, Args)]
pub struct NewArgs {
    /// Name for the new plugin project (kebab-case, e.g. `my-tool`).
    pub name: String,

    /// Plugin capability kind.
    ///
    /// One of: ChatTool, Chunker, Retriever, Provider, Agent.
    /// Defaults to ChatTool when `--no-interactive` is passed.
    #[arg(long, default_value = "ChatTool")]
    pub kind: String,

    /// Author name for the plugin manifest.
    #[arg(long, default_value = "Cascade Plugin Author")]
    pub author: String,

    /// Short description for the plugin manifest.
    #[arg(long, default_value = "A Cascade WASM plugin")]
    pub description: String,

    /// Skip interactive prompts and use all defaults / provided flags.
    #[arg(long)]
    pub no_interactive: bool,

    /// Directory to create the plugin project in.
    /// Defaults to `./<name>` relative to the current directory.
    #[arg(long)]
    pub output_dir: Option<PathBuf>,
}

/// Arguments for `cascade plugin test`.
#[derive(Debug, Args)]
pub struct TestArgs {
    /// Path to the plugin project root.
    /// Defaults to the current directory.
    #[arg(long)]
    pub project_dir: Option<PathBuf>,

    /// Build in release mode before testing.
    #[arg(long)]
    pub release: bool,

    /// Skip the `cargo build --target wasm32-wasip1` step (use existing WASM).
    #[arg(long)]
    pub skip_build: bool,
}

/// Arguments for `cascade plugin revoke`.
#[derive(Debug, Args)]
pub struct RevokeArgs {
    /// Plugin ID (e.g. `com.example.my-plugin`).
    pub id: String,
    /// Capability to revoke (e.g. `personal_data`, `net_outbound`).
    pub cap: String,
}

/// Arguments for `cascade plugin trust`.
#[derive(Debug, Args)]
pub struct TrustArgs {
    /// Publisher name (display label).
    pub name: String,
    /// Base64-encoded Ed25519 public key.
    pub public_key: String,
}

/// Arguments for `cascade plugin grant`.
#[derive(Debug, Args)]
pub struct GrantArgs {
    /// Plugin ID.
    pub id: String,
    /// Capability to grant.
    pub cap: String,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for PluginArgs {
    async fn run(&self) -> Result<()> {
        let plugins_dir = resolve_plugins_dir(self.plugins_dir.as_deref())?;

        match &self.command {
            PluginCommands::List(args) => run_list(&plugins_dir, args),
            PluginCommands::Enable(args) => run_enable(&plugins_dir, &args.id),
            PluginCommands::Disable(args) => run_disable(&plugins_dir, &args.id),
            PluginCommands::Info(args) => run_info(&plugins_dir, &args.id),
            PluginCommands::New(args) => run_new(args),
            PluginCommands::Test(args) => run_test(args),
            PluginCommands::Revoke(args) => run_revoke(&args.id, &args.cap),
            PluginCommands::Trust(args) => run_trust(&args.name, &args.public_key),
            PluginCommands::Grant(args) => run_grant(&args.id, &args.cap),
        }
    }
}

// ── grant / revoke / trust ────────────────────────────────────────────────────

fn str_to_cap(s: &str) -> Result<Capability> {
    match s {
        "fs_read"       => Ok(Capability::FsRead),
        "fs_write"      => Ok(Capability::FsWrite),
        "fs_exec"       => Ok(Capability::FsExec),
        "net_outbound"  => Ok(Capability::NetOutbound),
        "net_listen"    => Ok(Capability::NetListen),
        "personal_data" => Ok(Capability::PersonalData),
        _ => Err(CascadeError::Other(format!(
            "unknown capability '{s}'. Valid: fs_read, fs_write, fs_exec, net_outbound, net_listen, personal_data"
        ))),
    }
}

fn run_revoke(id: &str, cap_str: &str) -> Result<()> {
    let cap = str_to_cap(cap_str)?;
    let mut store = GrantStore::load()
        .map_err(|e| CascadeError::Other(format!("failed to load grants: {e}")))?;
    store
        .revoke(id, &cap)
        .map_err(|e| CascadeError::Other(format!("failed to revoke: {e}")))?;
    println!("Revoked '{cap_str}' from plugin '{id}'.");
    Ok(())
}

fn run_grant(id: &str, cap_str: &str) -> Result<()> {
    let cap = str_to_cap(cap_str)?;
    let mut store = GrantStore::load()
        .map_err(|e| CascadeError::Other(format!("failed to load grants: {e}")))?;
    store
        .grant(id, &cap)
        .map_err(|e| CascadeError::Other(format!("failed to grant: {e}")))?;
    println!("Granted '{cap_str}' to plugin '{id}'.");
    Ok(())
}

fn run_trust(name: &str, public_key: &str) -> Result<()> {
    let mut publishers = TrustedPublishers::load()
        .map_err(|e| CascadeError::Other(format!("failed to load publishers: {e}")))?;
    publishers
        .trust(name.to_owned(), public_key.to_owned())
        .map_err(|e| CascadeError::Other(format!("failed to add publisher: {e}")))?;
    println!("Added trusted publisher '{name}'.");
    Ok(())
}

// ── list ──────────────────────────────────────────────────────────────────────

fn run_list(plugins_dir: &Path, args: &ListArgs) -> Result<()> {
    let (loaded, errors) = PluginLoader::scan(plugins_dir);

    if args.json {
        // JSON output: include both loaded and error entries.
        let loaded_json: Vec<serde_json::Value> = loaded
            .iter()
            .map(|d| {
                serde_json::json!({
                    "id":      d.manifest.id,
                    "name":    d.manifest.name,
                    "version": d.manifest.version,
                    "kind":    kind_str(d.manifest.kind),
                    "status":  "loaded",
                    "dir":     d.plugin_dir.display().to_string(),
                })
            })
            .collect();

        let error_json: Vec<serde_json::Value> = errors
            .iter()
            .map(|e| {
                serde_json::json!({
                    "status": "error",
                    "reason": e.to_string(),
                })
            })
            .collect();

        let out = serde_json::json!({
            "plugins": loaded_json,
            "errors":  error_json,
        });
        println!("{}", serde_json::to_string_pretty(&out).unwrap_or_default());
        return Ok(());
    }

    // Human-readable table.
    if loaded.is_empty() && errors.is_empty() {
        println!("No plugins installed in {}", plugins_dir.display());
        println!(
            "Install a plugin by copying its directory to {}",
            plugins_dir.display()
        );
        return Ok(());
    }

    // Header
    println!(
        "{:<40}  {:<24}  {:<8}  {:<16}  STATUS",
        "ID", "NAME", "VERSION", "KIND"
    );
    println!("{}", "-".repeat(110));

    for d in &loaded {
        println!(
            "{:<40}  {:<24}  {:<8}  {:<16}  loaded",
            truncate(&d.manifest.id, 40),
            truncate(&d.manifest.name, 24),
            truncate(&d.manifest.version, 8),
            kind_str(d.manifest.kind)
        );
    }

    // Show failed / disabled plugins at the bottom.
    for e in &errors {
        let (id_col, reason) = error_display(e);
        println!(
            "{:<40}  {:<24}  {:<8}  {:<16}  error ({})",
            truncate(&id_col, 40),
            "-",
            "-",
            "-",
            reason
        );
    }

    if !errors.is_empty() {
        println!();
        println!(
            "{} plugin(s) could not load. Run `cascade plugin info <id>` for details.",
            errors.len()
        );
    }

    Ok(())
}

// ── enable ────────────────────────────────────────────────────────────────────

fn run_enable(plugins_dir: &Path, id: &str) -> Result<()> {
    let plugin_dir = find_plugin_dir(plugins_dir, id)?;
    let marker = plugin_dir.join(".disabled");

    if marker.exists() {
        std::fs::remove_file(&marker).map_err(|e| {
            CascadeError::Other(format!("failed to remove .disabled marker for {id}: {e}"))
        })?;
        println!("Plugin '{id}' enabled.");
    } else {
        println!("Plugin '{id}' is already enabled.");
    }

    Ok(())
}

// ── disable ───────────────────────────────────────────────────────────────────

fn run_disable(plugins_dir: &Path, id: &str) -> Result<()> {
    let plugin_dir = find_plugin_dir(plugins_dir, id)?;
    let marker = plugin_dir.join(".disabled");

    if marker.exists() {
        println!("Plugin '{id}' is already disabled.");
    } else {
        std::fs::write(&marker, "").map_err(|e| {
            CascadeError::Other(format!("failed to write .disabled marker for {id}: {e}"))
        })?;
        println!("Plugin '{id}' disabled. It will not be loaded on next daemon start.");
    }

    Ok(())
}

// ── info ──────────────────────────────────────────────────────────────────────

fn run_info(plugins_dir: &Path, id: &str) -> Result<()> {
    let plugin_dir = find_plugin_dir(plugins_dir, id)?;
    let manifest_path = plugin_dir.join("plugin.json");

    let manifest = cascade_plugins::manifest::PluginJsonManifest::load(&manifest_path)
        .map_err(|e| CascadeError::Other(format!("failed to load manifest for {id}: {e}")))?;

    let disabled = plugin_dir.join(".disabled").exists();

    println!("Plugin: {}", manifest.name);
    println!("  ID:          {}", manifest.id);
    println!("  Version:     {}", manifest.version);
    println!("  Kind:        {}", kind_str(manifest.kind));
    println!("  Description: {}", manifest.description);
    println!("  Author:      {}", manifest.author);
    println!("  License:     {}", manifest.license);
    println!("  WASM entry:  {}", manifest.entry_wasm);
    println!("  Directory:   {}", plugin_dir.display());
    println!(
        "  Status:      {}",
        if disabled { "disabled" } else { "enabled" }
    );
    println!("  Min Cascade: {}", manifest.min_cascade_version);

    if !manifest.capabilities.is_empty() {
        println!("  Capabilities: {}", manifest.capabilities.join(", "));
    }

    Ok(())
}

// ── new ───────────────────────────────────────────────────────────────────────

/// Scaffold a new plugin project from the vendored `templates/plugin-template/`.
///
/// Uses `cargo-generate` with the template directory. The template path is
/// resolved relative to the cascade workspace root (or the `CASCADE_WORKSPACE`
/// env var when set in tests).
fn run_new(args: &NewArgs) -> Result<()> {
    // Validate kind.
    let valid_kinds = ["ChatTool", "Chunker", "Retriever", "Provider", "Agent"];
    if !valid_kinds.contains(&args.kind.as_str()) {
        return Err(CascadeError::Other(format!(
            "invalid plugin kind '{}'. Must be one of: {}",
            args.kind,
            valid_kinds.join(", ")
        )));
    }

    // Resolve template path.
    let template_path = resolve_template_path()?;

    // Destination directory.
    let dest = args
        .output_dir
        .clone()
        .unwrap_or_else(|| PathBuf::from(&args.name));

    println!("Scaffolding plugin '{}' (kind: {})…", args.name, args.kind);
    println!("  Template: {}", template_path.display());
    println!("  Output:   {}", dest.display());

    // Build the cargo-generate command.
    // cargo-generate must be installed (`cargo install cargo-generate`).
    let mut cmd = StdCommand::new("cargo");
    cmd.arg("generate")
        .arg("--path")
        .arg(&template_path)
        .arg("--name")
        .arg(&args.name)
        .arg("--define")
        .arg(format!("plugin_name={}", args.name))
        .arg("--define")
        .arg(format!("plugin_type={}", args.kind))
        .arg("--define")
        .arg(format!("author={}", args.author))
        .arg("--define")
        .arg(format!("description={}", args.description))
        .arg("--destination")
        .arg(dest.parent().unwrap_or_else(|| Path::new(".")));

    if args.no_interactive {
        cmd.arg("--silent");
    }

    let status = cmd.status().map_err(|e| {
        if e.kind() == std::io::ErrorKind::NotFound {
            CascadeError::Other(
                "cargo-generate not found. Install with: cargo install cargo-generate".to_owned(),
            )
        } else {
            CascadeError::Other(format!("failed to run cargo-generate: {e}"))
        }
    })?;

    if status.success() {
        println!();
        println!("Plugin scaffolded successfully!");
        println!();
        println!("Next steps:");
        println!("  cd {}", args.name);
        println!("  rustup target add wasm32-wasip1   # if not already added");
        println!("  cascade plugin test               # build + run harness tests");
        println!("  ./build.sh                        # compile and install");
        Ok(())
    } else {
        Err(CascadeError::Other(format!(
            "cargo-generate exited with status {status}"
        )))
    }
}

/// Resolve the path to `templates/plugin-template/`.
///
/// Search order:
/// 1. `CASCADE_WORKSPACE` env var (set in tests).
/// 2. Current executable's ancestor directories (up to 6 levels).
/// 3. Current working directory ancestors.
fn resolve_template_path() -> Result<PathBuf> {
    // Test override.
    if let Ok(ws) = std::env::var("CASCADE_WORKSPACE") {
        let p = PathBuf::from(ws).join("templates").join("plugin-template");
        if p.is_dir() {
            return Ok(p);
        }
    }

    // Walk up from the executable.
    if let Ok(exe) = std::env::current_exe() {
        let mut dir = exe.parent().map(|p| p.to_owned());
        for _ in 0..6 {
            if let Some(d) = dir {
                let candidate = d.join("templates").join("plugin-template");
                if candidate.is_dir() {
                    return Ok(candidate);
                }
                dir = d.parent().map(|p| p.to_owned());
            } else {
                break;
            }
        }
    }

    // Walk up from CWD.
    if let Ok(cwd) = std::env::current_dir() {
        let mut dir = Some(cwd.as_path().to_owned());
        for _ in 0..6 {
            if let Some(d) = dir {
                let candidate = d.join("templates").join("plugin-template");
                if candidate.is_dir() {
                    return Ok(candidate);
                }
                dir = d.parent().map(|p| p.to_owned());
            } else {
                break;
            }
        }
    }

    Err(CascadeError::Other(
        "Could not find templates/plugin-template/. \
         Run from within the Cascade workspace or set CASCADE_WORKSPACE env var."
            .to_owned(),
    ))
}

// ── test ──────────────────────────────────────────────────────────────────────

/// Build + test a plugin project.
///
/// Steps:
/// 1. Detect the plugin project root (via Cargo.toml).
/// 2. `cargo build --target wasm32-wasip1` — gracefully skip if toolchain absent.
/// 3. `cargo test` — stream stdout, parse `test ... ok/FAILED` lines.
/// 4. Print summary table.
fn run_test(args: &TestArgs) -> Result<()> {
    let project_dir = args
        .project_dir
        .clone()
        .unwrap_or_else(|| std::env::current_dir().unwrap_or_default());

    // Verify it looks like a Rust project.
    let cargo_toml = project_dir.join("Cargo.toml");
    if !cargo_toml.exists() {
        return Err(CascadeError::Other(format!(
            "no Cargo.toml found in {}. Run from a plugin project root.",
            project_dir.display()
        )));
    }

    // Step 1: build WASM (if not skipped).
    if !args.skip_build {
        let wasm_status = build_wasm(&project_dir, args.release)?;
        if !wasm_status {
            // Build failed — already printed error; return non-zero.
            return Err(CascadeError::Other(
                "WASM build failed. Fix errors above before running tests.".to_owned(),
            ));
        }
    }

    // Step 2: run cargo test.
    println!();
    println!("Running plugin tests…");
    println!("{}", "─".repeat(60));

    let test_output = StdCommand::new("cargo")
        .arg("test")
        .current_dir(&project_dir)
        .output()
        .map_err(|e| CascadeError::Other(format!("failed to run cargo test: {e}")))?;

    let stdout = String::from_utf8_lossy(&test_output.stdout);
    let stderr = String::from_utf8_lossy(&test_output.stderr);

    // Print raw output for context.
    if !stderr.is_empty() {
        eprint!("{stderr}");
    }

    // Parse test lines and build summary.
    let (passed, failed, results) = parse_test_output(&stdout);

    // Print formatted result table.
    for (name, status, duration_ms) in &results {
        let status_str = if *status { "PASS" } else { "FAIL" };
        let duration_str = format!("{duration_ms}ms");
        println!("  {:<8}  {:<50}  {}", status_str, name, duration_str);
    }

    println!("{}", "─".repeat(60));
    println!("Results: {} passed, {} failed", passed, failed);

    if test_output.status.success() && failed == 0 {
        println!("All tests passed.");
        Ok(())
    } else {
        Err(CascadeError::Other(format!("{failed} test(s) failed.")))
    }
}

/// Run `cargo build --target wasm32-wasip1` in `project_dir`.
///
/// Returns `true` if the build succeeded, `false` if the toolchain is absent
/// (graceful degradation) or the build failed.
fn build_wasm(project_dir: &Path, release: bool) -> Result<bool> {
    println!("Building plugin (target: wasm32-wasip1)…");

    let mut cmd = StdCommand::new("cargo");
    cmd.arg("build").arg("--target").arg("wasm32-wasip1");
    if release {
        cmd.arg("--release");
    }
    cmd.current_dir(project_dir);

    let result = cmd.status();

    match result {
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            println!("  cargo not found — skipping WASM build.");
            Ok(true) // Graceful: allow tests to proceed without WASM.
        }
        Err(e) => Err(CascadeError::Other(format!("failed to spawn cargo: {e}"))),
        Ok(status) => {
            if status.success() {
                println!("  WASM build succeeded.");
                return Ok(true);
            }

            // Check whether the failure was a missing target.
            // cargo exits non-zero and prints to stderr; we detect the target
            // error by running `rustup target list` as a probe.
            let has_target = StdCommand::new("rustup")
                .args(["target", "list", "--installed"])
                .output()
                .map(|o| String::from_utf8_lossy(&o.stdout).contains("wasm32-wasip1"))
                .unwrap_or(false);

            if !has_target {
                println!();
                println!("  wasm32-wasip1 toolchain not installed.");
                println!("  To build for WASM, run:");
                println!("    rustup target add wasm32-wasip1");
                println!();
                println!("  Continuing with host-only tests (no WASM step).");
                return Ok(true); // Graceful skip.
            }

            // Real build failure.
            Ok(false)
        }
    }
}

/// Parse `cargo test` stdout into (passed_count, failed_count, per-test results).
///
/// Matches lines like:
/// - `test my_test ... ok` (with optional timing suffix)
/// - `test my_test ... FAILED`
fn parse_test_output(stdout: &str) -> (usize, usize, Vec<(String, bool, u64)>) {
    let mut results = Vec::new();
    let mut passed = 0usize;
    let mut failed = 0usize;

    for line in stdout.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with("test ") {
            continue;
        }
        // Split: "test <name> ... <status>"
        let parts: Vec<&str> = trimmed.splitn(3, " ... ").collect();
        if parts.len() < 2 {
            continue;
        }
        let name_part = parts[0].trim_start_matches("test ").trim();
        let status_part = if parts.len() == 2 { parts[1] } else { parts[2] };

        // Extract optional timing from "(Xms)" suffix.
        let duration_ms = status_part
            .split_once('(')
            .and_then(|(_, rest)| {
                rest.split_once("ms)")
                    .map(|(ms, _)| ms.trim().parse::<u64>().unwrap_or(0))
            })
            .unwrap_or(0);

        let ok = status_part.trim_start().starts_with("ok");
        if ok {
            passed += 1;
        } else if status_part.trim_start().starts_with("FAILED") {
            failed += 1;
        } else {
            continue; // "ignored" — skip.
        }

        results.push((name_part.to_owned(), ok, duration_ms));
    }

    (passed, failed, results)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Resolve the plugins directory: use override if provided, else `~/.cascade/plugins/`.
fn resolve_plugins_dir(override_dir: Option<&Path>) -> Result<PathBuf> {
    if let Some(d) = override_dir {
        return Ok(d.to_owned());
    }
    let home = dirs::home_dir()
        .ok_or_else(|| CascadeError::Other("cannot determine home directory".to_owned()))?;
    Ok(home.join(".cascade").join("plugins"))
}

/// Find the plugin directory for the given `id` under `plugins_dir`.
///
/// Looks for a subdirectory whose name exactly matches `id`.
fn find_plugin_dir(plugins_dir: &Path, id: &str) -> Result<PathBuf> {
    let dir = plugins_dir.join(id);
    if dir.is_dir() {
        return Ok(dir);
    }

    // Scan for a directory containing a plugin.json with a matching id.
    if let Ok(rd) = std::fs::read_dir(plugins_dir) {
        for entry in rd.flatten() {
            if !entry.file_type().map(|t| t.is_dir()).unwrap_or(false) {
                continue;
            }
            let manifest_path = entry.path().join("plugin.json");
            if let Ok(m) = cascade_plugins::manifest::PluginJsonManifest::load(&manifest_path) {
                if m.id == id {
                    return Ok(entry.path());
                }
            }
        }
    }

    Err(CascadeError::Other(format!(
        "plugin '{id}' not found in {plugins_dir}",
        plugins_dir = plugins_dir.display()
    )))
}

/// Convert a `PluginKind` to a display string.
fn kind_str(kind: PluginKind) -> &'static str {
    match kind {
        PluginKind::DataSource => "DataSource",
        PluginKind::ChatTool => "ChatTool",
        PluginKind::Widget => "Widget",
        PluginKind::Provider => "Provider",
        PluginKind::Agent => "Agent",
    }
}

/// Truncate a string to `max_len` for table display.
fn truncate(s: &str, max_len: usize) -> String {
    if s.len() <= max_len {
        s.to_owned()
    } else {
        format!("{}…", &s[..max_len.saturating_sub(1)])
    }
}

/// Extract a short display ID and reason from a `PluginLoadError`.
fn error_display(e: &PluginLoadError) -> (String, String) {
    match e {
        PluginLoadError::ManifestNotFound { dir } => (
            dir.file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned()),
            "plugin.json missing".to_owned(),
        ),
        PluginLoadError::ManifestInvalid { dir, reason } => (
            dir.file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned()),
            format!("manifest invalid: {reason}"),
        ),
        PluginLoadError::WasmNotFound { path } => (
            path.parent()
                .and_then(|p| p.file_name())
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned()),
            "WASM file missing".to_owned(),
        ),
        PluginLoadError::WasmReadError { path, reason } => (
            path.parent()
                .and_then(|p| p.file_name())
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned()),
            format!("WASM read error: {reason}"),
        ),
        PluginLoadError::SandboxError { id, reason } => (id.clone(), format!("sandbox: {reason}")),
        PluginLoadError::Disabled { id } => (id.clone(), "disabled".to_owned()),
        PluginLoadError::NotADirectory { path } => (
            path.file_name()
                .map(|n| n.to_string_lossy().into_owned())
                .unwrap_or_else(|| "<unknown>".to_owned()),
            "not a directory".to_owned(),
        ),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn valid_manifest_json(id: &str) -> String {
        format!(
            r#"{{
                "id": "{id}",
                "name": "Test Plugin",
                "version": "1.0.0",
                "description": "A test",
                "author": "test",
                "license": "MIT",
                "entry_wasm": "{id}.wasm",
                "kind": "ChatTool",
                "min_cascade_version": ">=0.1.0"
            }}"#
        )
    }

    fn write_minimal_wasm(path: &Path) {
        let wasm = wat::parse_str("(module)").unwrap();
        fs::write(path, wasm).unwrap();
    }

    fn make_plugin(plugins_dir: &Path, id: &str) -> PathBuf {
        let dir = plugins_dir.join(id);
        fs::create_dir_all(&dir).unwrap();
        fs::write(dir.join("plugin.json"), valid_manifest_json(id)).unwrap();
        write_minimal_wasm(&dir.join(format!("{id}.wasm")));
        dir
    }

    // Serial attribute ensures HOME env var manipulation does not interfere with
    // parallel tests in the same binary.
    #[cfg(test)]
    fn run_list_cmd(plugins_dir: &Path, json: bool) -> Result<()> {
        run_list(plugins_dir, &ListArgs { json })
    }

    #[test]
    fn list_empty_dir_prints_no_plugins_message() {
        let tmp = TempDir::new().unwrap();
        // Create the plugins dir but leave it empty.
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        // Should not panic or error.
        assert!(run_list_cmd(&plugins_dir, false).is_ok());
    }

    #[test]
    fn list_json_includes_loaded_plugin() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        make_plugin(&plugins_dir, "com.example.foo");

        // Capture stdout is hard in unit tests; just assert no error.
        assert!(run_list_cmd(&plugins_dir, true).is_ok());
    }

    #[test]
    fn enable_removes_disabled_marker() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        let dir = make_plugin(&plugins_dir, "com.example.bar");

        // Write the marker.
        fs::write(dir.join(".disabled"), "").unwrap();
        assert!(dir.join(".disabled").exists());

        run_enable(&plugins_dir, "com.example.bar").unwrap();
        assert!(
            !dir.join(".disabled").exists(),
            ".disabled should be removed after enable"
        );
    }

    #[test]
    fn disable_writes_disabled_marker() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        let dir = make_plugin(&plugins_dir, "com.example.baz");

        assert!(!dir.join(".disabled").exists());
        run_disable(&plugins_dir, "com.example.baz").unwrap();
        assert!(
            dir.join(".disabled").exists(),
            ".disabled should be created after disable"
        );
    }

    #[test]
    fn enable_unknown_plugin_returns_error() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        let result = run_enable(&plugins_dir, "com.example.nonexistent");
        assert!(result.is_err(), "enable on nonexistent plugin should error");
    }

    #[test]
    fn find_plugin_dir_exact_match() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();
        make_plugin(&plugins_dir, "com.example.exact");

        let found = find_plugin_dir(&plugins_dir, "com.example.exact");
        assert!(found.is_ok(), "should find exact match: {found:?}");
    }

    #[test]
    fn find_plugin_dir_not_found_returns_error() {
        let tmp = TempDir::new().unwrap();
        let plugins_dir = tmp.path().join("plugins");
        fs::create_dir_all(&plugins_dir).unwrap();

        let result = find_plugin_dir(&plugins_dir, "com.example.ghost");
        assert!(result.is_err());
    }

    // ── new / scaffold tests ──────────────────────────────────────────────────

    #[test]
    fn new_args_invalid_kind_returns_error() {
        let args = NewArgs {
            name: "my-plugin".into(),
            kind: "InvalidKind".into(),
            author: "Test".into(),
            description: "desc".into(),
            no_interactive: true,
            output_dir: None,
        };
        let result = run_new(&args);
        assert!(result.is_err(), "invalid kind should return error");
        let msg = format!("{result:?}");
        assert!(
            msg.contains("invalid plugin kind"),
            "error message should mention kind: {msg}"
        );
    }

    #[test]
    fn resolve_template_path_finds_vendored_template() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        // Set CASCADE_WORKSPACE to the repo root so the resolver can find the template.
        let workspace = "/home/user/projects/acamarata/cascade";
        std::env::set_var("CASCADE_WORKSPACE", workspace);
        let result = resolve_template_path();
        // Unset before asserting so test isolation is maintained.
        std::env::remove_var("CASCADE_WORKSPACE");
        assert!(
            result.is_ok(),
            "should find template via CASCADE_WORKSPACE: {result:?}"
        );
    }

    // ── test command helper tests ─────────────────────────────────────────────

    #[test]
    fn parse_test_output_empty() {
        let (p, f, r) = parse_test_output("");
        assert_eq!(p, 0);
        assert_eq!(f, 0);
        assert!(r.is_empty());
    }

    #[test]
    fn parse_test_output_ok_lines() {
        let output = r"
running 2 tests
test dispatch_tool_call_success ... ok
test dispatch_unknown_function_returns_error ... ok

test result: ok. 2 passed; 0 failed; 0 ignored";
        let (p, f, r) = parse_test_output(output);
        assert_eq!(p, 2, "expected 2 passed");
        assert_eq!(f, 0, "expected 0 failed");
        assert_eq!(r.len(), 2);
        assert!(r[0].1, "first test should be ok");
    }

    #[test]
    fn parse_test_output_failed_lines() {
        let output = r"
running 2 tests
test passing_test ... ok
test failing_test ... FAILED

failures:
    failing_test

test result: FAILED. 1 passed; 1 failed; 0 ignored";
        let (p, f, r) = parse_test_output(output);
        assert_eq!(p, 1);
        assert_eq!(f, 1);
        assert_eq!(r.len(), 2);
        assert!(!r[1].1, "second test should be FAILED");
    }

    #[test]
    fn test_args_project_dir_not_cargo_project_returns_error() {
        let tmp = TempDir::new().unwrap();
        let args = TestArgs {
            project_dir: Some(tmp.path().to_owned()),
            release: false,
            skip_build: true,
        };
        let result = run_test(&args);
        assert!(result.is_err(), "missing Cargo.toml should error");
    }
}
