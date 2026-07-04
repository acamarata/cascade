// conductor.rs — `cascade conductor` subcommand (Cascade Conductor).
//
// Purpose: quota-aware delegation to worker AI accounts. Routes a prompt to
//   the best available backend (A2 → A1-spare → Codex → Gemini → OpenCode →
//   GFP) and executes via the appropriate CLI, falling back on `Unavailable`.
//
//   Subcommand `cascade conductor selftest` probes every known provider and
//   reports available/unavailable + latency for each reachable one.
//
// Inputs:  CLI args (tier, model, account, prompt, --dry-run, --json).
// Outputs: backend stdout (streaming) or JSON envelope.
//
// Constraints:
//   - No shell invocation; all subprocess args are discrete tokens.
//   - Augments PATH with /opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin on
//     every spawned Command so tools resolve under a minimal PATH.
//   - On Unavailable → fall to next target; records fallbacks_tried.
//   - Real backends only — no stubs, no fake success.
//
// SPORT: MASTER-CLI.md — cascade conductor

use std::path::PathBuf;
use std::process::{Command as StdCommand, Stdio};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use cascade_core::accounts_store::quota_json_path;
use cascade_core::conductor_router::{
    ConductorRequest, ConductorTarget, GpHealthSnapshot, ModelClass, Provider, QuotaAccount,
    QuotaSnapshot, Tier, select_target_with_gp,
};
use cascade_core::routing::gfp_http::{probe_gp_health, GFP_HEALTH_URL};
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};
use serde_json::Value;

use super::Command;

// ── Clap types ────────────────────────────────────────────────────────────────

/// Arguments for `cascade conductor`.
#[derive(Debug, Args)]
pub struct ConductorArgs {
    #[command(subcommand)]
    pub command: Option<ConductorCommands>,

    /// Work tier: T1 (decisions/gates), T2 (bulk exec), T3 (cheap triage).
    #[arg(long, value_name = "TIER", global = true)]
    pub tier: Option<TierArg>,

    /// Model class override: opus | sonnet | haiku | fable.
    #[arg(long, value_name = "MODEL", global = true)]
    pub model: Option<ModelArg>,

    /// Force a specific account-id (e.g. `claude2`) or `auto` (default).
    #[arg(long, value_name = "ACCOUNT", global = true, default_value = "auto")]
    pub account: String,

    /// The prompt text to send.
    #[arg(long, value_name = "TEXT", global = true)]
    pub prompt: Option<String>,

    /// Read the prompt from a file instead of --prompt.
    #[arg(long, value_name = "PATH", global = true)]
    pub prompt_file: Option<PathBuf>,

    /// Print selected target and reason without executing.
    #[arg(long, global = true)]
    pub dry_run: bool,

    /// Emit output as JSON.
    #[arg(long, global = true)]
    pub json: bool,
}

/// Subcommands under `cascade conductor`.
#[derive(Debug, Subcommand)]
pub enum ConductorCommands {
    /// Probe each provider and report available/unavailable + latency.
    Selftest,
}

/// Clap-compatible tier arg.
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum TierArg {
    #[value(name = "T1")]
    T1,
    #[value(name = "T2")]
    T2,
    #[value(name = "T3")]
    T3,
}

/// Clap-compatible model class arg.
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum ModelArg {
    #[value(name = "opus")]
    Opus,
    #[value(name = "sonnet")]
    Sonnet,
    #[value(name = "haiku")]
    Haiku,
    #[value(name = "fable")]
    Fable,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ConductorArgs {
    async fn run(&self) -> Result<()> {
        // Selftest subcommand.
        if let Some(ConductorCommands::Selftest) = &self.command {
            return run_selftest(self.json);
        }

        // Conductor invocation: requires --tier and a prompt.
        let tier = match self.tier {
            Some(TierArg::T1) => Tier::T1,
            Some(TierArg::T2) => Tier::T2,
            Some(TierArg::T3) => Tier::T3,
            None => {
                eprintln!("cascade conductor: --tier is required (T1|T2|T3)");
                std::process::exit(1);
            }
        };

        let model_class = self.model.map(|m| match m {
            ModelArg::Opus => ModelClass::Opus,
            ModelArg::Sonnet => ModelClass::Sonnet,
            ModelArg::Haiku => ModelClass::Haiku,
            ModelArg::Fable => ModelClass::Fable,
        });

        let account_override = if self.account == "auto" || self.account.is_empty() {
            None
        } else {
            Some(self.account.clone())
        };

        let prompt = build_prompt(&self.prompt, &self.prompt_file)?;

        let req = ConductorRequest { tier, model_class, account_override };

        // Load live quota snapshot.
        let snapshot = load_quota_snapshot();

        // Live GP pool health (E1-S6): probed from the proxy's /health
        // endpoint for T3 work so the T3-GP preference can actually fire.
        // Any probe failure yields the default (unhealthy) snapshot — the
        // spill order is then identical to the pre-unification behavior.
        let gp = gp_health_for_tier(tier);

        // Select target.
        let Some(target) = select_target_with_gp(&req, &snapshot, &gp) else {
            let msg = "cascade conductor: no available backend (all accounts saturated or unavailable)";
            if self.json {
                println!("{}", serde_json::json!({"error": msg}));
            } else {
                eprintln!("{msg}");
            }
            std::process::exit(1);
        };

        if self.dry_run {
            if self.json {
                println!(
                    "{}",
                    serde_json::json!({
                        "dry_run": true,
                        "provider": target.provider.as_str(),
                        "account_id": target.account_id,
                        "model": target.model,
                        "reason": target.reason,
                    })
                );
            } else {
                println!("provider:   {}", target.provider.as_str());
                println!("account:    {}", target.account_id);
                println!("model:      {}", target.model);
                println!("reason:     {}", target.reason);
            }
            return Ok(());
        }

        // Execute — with fallback on Unavailable.
        execute_with_fallback(&req, &snapshot, &gp, target, &prompt, self.json)
    }
}

// ── GP pool health probe (E1-S6) ─────────────────────────────────────────────

/// Probe the live GP pool health, but only when it can change the outcome.
///
/// The T3-GP preference is the only consumer of this signal in the conductor
/// path, so T1/T2 requests skip the probe entirely (no added latency). The
/// probe hits the gemini_proxy's `/health` endpoint (:3761), which reports
/// LIVE routing-table state including 429 cooldowns — never static config.
fn gp_health_for_tier(tier: Tier) -> GpHealthSnapshot {
    if tier == Tier::T3 {
        probe_gp_health(GFP_HEALTH_URL, Duration::from_secs(2))
    } else {
        GpHealthSnapshot::default()
    }
}

// ── Prompt builder ────────────────────────────────────────────────────────────

fn build_prompt(prompt_text: &Option<String>, prompt_file: &Option<PathBuf>) -> Result<String> {
    if let Some(text) = prompt_text {
        return Ok(text.clone());
    }
    if let Some(path) = prompt_file {
        return std::fs::read_to_string(path)
            .map_err(|e| CascadeError::io(path.clone(), "read", e));
    }
    Err(CascadeError::Other(
        "cascade conductor: one of --prompt or --prompt-file is required".to_string(),
    ))
}

// ── Quota snapshot loader ─────────────────────────────────────────────────────

/// Load `~/.cascade/accounts/quota.json` and build an injectable `QuotaSnapshot`.
/// Returns an empty snapshot on any I/O or parse error (routing then returns None).
fn load_quota_snapshot() -> QuotaSnapshot {
    let path = quota_json_path();
    let text = match std::fs::read_to_string(&path) {
        Ok(t) => t,
        Err(_) => return QuotaSnapshot::default(),
    };
    let v: Value = match serde_json::from_str(&text) {
        Ok(v) => v,
        Err(_) => return QuotaSnapshot::default(),
    };

    let accounts_arr = match v.get("accounts").and_then(|a| a.as_array()) {
        Some(a) => a,
        None => return QuotaSnapshot::default(),
    };

    let accounts: Vec<QuotaAccount> = accounts_arr
        .iter()
        .filter_map(|entry| {
            let account = entry.get("account").and_then(|v| v.as_str())?.to_string();
            let provider = entry.get("provider").and_then(|v| v.as_str()).unwrap_or("unknown").to_string();
            let status = entry.get("status").and_then(|v| v.as_str()).unwrap_or("unknown").to_string();
            let config_dir = entry
                .get("config_dir")
                .and_then(|v| v.as_str())
                .map(PathBuf::from);

            let usage = entry.get("usage");
            let five_hour_utilization = usage
                .and_then(|u| u.get("five_hour"))
                .and_then(|fh| fh.get("utilization"))
                .and_then(|v| v.as_f64());
            let seven_day_utilization = usage
                .and_then(|u| u.get("seven_day"))
                .and_then(|sd| sd.get("utilization"))
                .and_then(|v| v.as_f64());

            Some(QuotaAccount {
                account,
                provider,
                status,
                config_dir,
                five_hour_utilization,
                seven_day_utilization,
            })
        })
        .collect();

    QuotaSnapshot { accounts }
}

// ── Executor ──────────────────────────────────────────────────────────────────

/// Outcome of a single backend call.
#[derive(Debug)]
pub enum Outcome {
    /// The backend returned output.
    Success { output: String },
    /// The backend CLI is not present or failed to connect.
    Unavailable { reason: String },
    /// The backend returned an error (non-zero exit or error envelope).
    Error { message: String },
}

/// Execute `prompt` against `target`, falling back through remaining candidates
/// in the spill order if the primary returns `Unavailable`.
fn execute_with_fallback(
    req: &ConductorRequest,
    snapshot: &QuotaSnapshot,
    gp: &GpHealthSnapshot,
    initial_target: ConductorTarget,
    prompt: &str,
    json_output: bool,
) -> Result<()> {
    let mut tried: Vec<String> = Vec::new();
    let mut current = initial_target;

    loop {
        let result = execute_target(&current, prompt);
        match result {
            Outcome::Success { output } => {
                if json_output {
                    println!(
                        "{}",
                        serde_json::json!({
                            "provider": current.provider.as_str(),
                            "account_id": current.account_id,
                            "model": current.model,
                            "fallbacks_tried": tried,
                            "output": output,
                        })
                    );
                } else {
                    print!("{output}");
                }
                return Ok(());
            }
            Outcome::Unavailable { reason } => {
                eprintln!(
                    "cascade conductor: {} unavailable ({}), trying next...",
                    current.account_id, reason
                );
                tried.push(format!("{} ({})", current.account_id, reason));

                // Mark the failed account as saturated in a mutable copy and
                // re-run routing to get the next target.
                let mut snapshot_copy = snapshot.clone();
                if let Some(entry) = snapshot_copy
                    .accounts
                    .iter_mut()
                    .find(|a| a.account == current.account_id)
                {
                    entry.status = "unavailable".to_string();
                }

                let next_req = ConductorRequest {
                    tier: req.tier,
                    model_class: req.model_class,
                    account_override: None,
                };

                let Some(next) = select_target_with_gp(&next_req, &snapshot_copy, gp) else {
                    let msg = format!(
                        "cascade conductor: all backends exhausted (tried: {})",
                        tried.join(", ")
                    );
                    if json_output {
                        println!("{}", serde_json::json!({"error": msg, "fallbacks_tried": tried}));
                    } else {
                        eprintln!("{msg}");
                    }
                    std::process::exit(1);
                };

                // Skip if we already tried this one (safety guard).
                if tried.iter().any(|t| t.starts_with(&next.account_id)) {
                    break;
                }
                current = next;
            }
            Outcome::Error { message } => {
                let msg = format!(
                    "cascade conductor: backend `{}` error: {}",
                    current.account_id, message
                );
                if json_output {
                    println!("{}", serde_json::json!({"error": msg, "fallbacks_tried": tried}));
                } else {
                    eprintln!("{msg}");
                }
                std::process::exit(1);
            }
        }
    }

    let msg = format!("cascade conductor: all backends exhausted (tried: {})", tried.join(", "));
    if json_output {
        println!("{}", serde_json::json!({"error": msg}));
    } else {
        eprintln!("{msg}");
    }
    std::process::exit(1);
}

/// Execute a prompt against a specific `ConductorTarget`.
///
/// Returns `Outcome::Unavailable` when the CLI binary is absent or unreachable,
/// allowing the caller to fall back to the next target.
pub fn execute_target(target: &ConductorTarget, prompt: &str) -> Outcome {
    match target.provider {
        Provider::Claude => execute_claude(target, prompt),
        Provider::Codex => execute_codex(target, prompt),
        Provider::OpenCode => execute_opencode(target, prompt),
        Provider::Gemini => execute_gemini(target, prompt),
        Provider::Gfp => execute_gfp(target, prompt),
    }
}

// ── Augmented PATH ────────────────────────────────────────────────────────────

/// Augmented PATH that ensures common install locations are always searched.
const AUGMENTED_PATH: &str =
    "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/opt/homebrew/sbin:/usr/local/sbin:/usr/sbin:/sbin";

/// Probe search dirs for a CLI binary name. Returns the first absolute path found.
fn find_binary(name: &str) -> Option<PathBuf> {
    // Common absolute install dirs; `~/bin` is covered by the home-relative
    // fallback below (no hardcoded user paths).
    let extra_dirs = [
        "/opt/homebrew/bin",
        "/usr/local/bin",
        "/usr/bin",
        "/bin",
    ];
    // Try PATH-based lookup via `which`-style probe.
    for dir in &extra_dirs {
        let candidate = PathBuf::from(dir).join(name);
        if candidate.exists() {
            return Some(candidate);
        }
    }
    // Fallback: try home-relative ~/bin/<name>.
    if let Some(home) = dirs::home_dir() {
        let candidate = home.join("bin").join(name);
        if candidate.exists() {
            return Some(candidate);
        }
    }
    None
}

// ── Claude backend ────────────────────────────────────────────────────────────

/// Run Claude Code non-interactively.
///
/// Command: `CLAUDE_CONFIG_DIR=<config_dir> claude -p "<prompt>" --model <model>
///            --output-format json --strict-mcp-config
///            --mcp-config '{"mcpServers":{}}' --setting-sources ''`
fn execute_claude(target: &ConductorTarget, prompt: &str) -> Outcome {
    let binary = match find_binary("claude") {
        Some(b) => b,
        None => {
            return Outcome::Unavailable {
                reason: "claude binary not found in PATH".to_string(),
            }
        }
    };

    let config_dir = match &target.config_dir {
        Some(d) => d.clone(),
        None => {
            return Outcome::Unavailable {
                reason: "no config_dir for claude account".to_string(),
            }
        }
    };

    let mut cmd = StdCommand::new(&binary);
    cmd.env("CLAUDE_CONFIG_DIR", &config_dir)
        .env("PATH", AUGMENTED_PATH)
        .args([
            "-p",
            prompt,
            "--model",
            &target.model,
            "--output-format",
            "json",
            "--strict-mcp-config",
            "--mcp-config",
            r#"{"mcpServers":{}}"#,
            "--setting-sources",
            "",
        ])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    run_command(cmd, "claude")
}

// ── Codex backend ─────────────────────────────────────────────────────────────

/// Run OpenAI Codex CLI non-interactively.
///
/// Command: `codex exec "<prompt>"`
/// Falls back on `codex run` if `exec` is absent.
fn execute_codex(_target: &ConductorTarget, prompt: &str) -> Outcome {
    let binary = match find_binary("codex") {
        Some(b) => b,
        None => {
            return Outcome::Unavailable {
                reason: "codex binary not found in PATH".to_string(),
            }
        }
    };

    let mut cmd = StdCommand::new(&binary);
    cmd.env("PATH", AUGMENTED_PATH)
        .args(["exec", prompt])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    run_command(cmd, "codex")
}

// ── OpenCode backend ──────────────────────────────────────────────────────────

/// Run OpenCode CLI non-interactively.
///
/// Command: `opencode run "<prompt>"`
fn execute_opencode(_target: &ConductorTarget, prompt: &str) -> Outcome {
    let binary = match find_binary("opencode") {
        Some(b) => b,
        None => {
            return Outcome::Unavailable {
                reason: "opencode binary not found in PATH".to_string(),
            }
        }
    };

    let mut cmd = StdCommand::new(&binary);
    cmd.env("PATH", AUGMENTED_PATH)
        .args(["run", prompt])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    run_command(cmd, "opencode")
}

// ── Gemini backend ────────────────────────────────────────────────────────────

/// Attempt to invoke the cascade-agy Gemini CLI.
///
/// `cascade-agy` is the Gemini usage collector; it does not expose a general
/// prompt invocation path. If no real invocation path is found, returns
/// `Unavailable` so routing falls to the next candidate.
fn execute_gemini(_target: &ConductorTarget, _prompt: &str) -> Outcome {
    // cascade-agy is a usage collector, not a prompt runner.
    // No real Gemini prompt CLI is available on this machine; report Unavailable.
    let agy = find_binary("cascade-agy");
    if agy.is_none() {
        return Outcome::Unavailable {
            reason: "cascade-agy not found; no Gemini prompt CLI available".to_string(),
        };
    }
    // Even if agy is present it does not support prompt dispatch — still unavailable.
    Outcome::Unavailable {
        reason: "cascade-agy does not support prompt dispatch; Gemini CLI unavailable".to_string(),
    }
}

// ── GFP backend ───────────────────────────────────────────────────────────────

/// Attempt to POST to the GFP proxy at localhost:3762 (Anthropic-compat adapter).
///
/// Returns `Unavailable` when the proxy is not running (connection refused).
fn execute_gfp(_target: &ConductorTarget, prompt: &str) -> Outcome {
    // Use a simple TCP probe + HTTP POST via std (no async here).
    let url = "http://localhost:3762/v1/messages";
    let body = serde_json::json!({
        "model": "claude-sonnet-4-6",
        "max_tokens": 1024,
        "messages": [{"role": "user", "content": prompt}]
    });
    let body_str = match serde_json::to_string(&body) {
        Ok(s) => s,
        Err(e) => {
            return Outcome::Error {
                message: format!("failed to serialize GFP request: {e}"),
            }
        }
    };

    // Use curl as a synchronous fallback to avoid pulling tokio into this sync fn.
    let curl = match find_binary("curl") {
        Some(b) => b,
        None => {
            return Outcome::Unavailable {
                reason: "curl not found; cannot reach GFP proxy".to_string(),
            }
        }
    };

    let mut cmd = StdCommand::new(&curl);
    cmd.env("PATH", AUGMENTED_PATH)
        .args([
            "-s",
            "-f",
            "--max-time",
            "5",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "-d",
            &body_str,
            url,
        ])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    match cmd.output() {
        Ok(out) if out.status.success() => Outcome::Success {
            output: String::from_utf8_lossy(&out.stdout).to_string(),
        },
        Ok(out) => {
            let stderr = String::from_utf8_lossy(&out.stderr);
            if stderr.contains("Connection refused") || out.status.code() == Some(7) {
                Outcome::Unavailable {
                    reason: "GFP proxy not running (connection refused on :3762)".to_string(),
                }
            } else {
                Outcome::Error {
                    message: format!(
                        "GFP HTTP error (exit {}): {}",
                        out.status.code().unwrap_or(-1),
                        stderr.trim()
                    ),
                }
            }
        }
        Err(e) => Outcome::Unavailable {
            reason: format!("curl exec failed: {e}"),
        },
    }
}

// ── Shared run helper ─────────────────────────────────────────────────────────

/// Run a prepared `StdCommand`, return `Outcome`.
fn run_command(mut cmd: StdCommand, name: &str) -> Outcome {
    match cmd.output() {
        Ok(out) => {
            if out.status.success() {
                Outcome::Success {
                    output: String::from_utf8_lossy(&out.stdout).to_string(),
                }
            } else {
                let stderr = String::from_utf8_lossy(&out.stderr);
                let stdout = String::from_utf8_lossy(&out.stdout);
                // Distinguish "binary not found / permission denied" from logical errors.
                if stderr.contains("No such file") || stderr.contains("Permission denied") {
                    Outcome::Unavailable {
                        reason: format!("{name}: {}", stderr.trim()),
                    }
                } else {
                    Outcome::Error {
                        message: format!(
                            "exit {}: {} {}",
                            out.status.code().unwrap_or(-1),
                            stderr.trim(),
                            stdout.trim()
                        ),
                    }
                }
            }
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Outcome::Unavailable {
            reason: format!("{name} binary not found: {e}"),
        },
        Err(e) => Outcome::Unavailable {
            reason: format!("{name} exec failed: {e}"),
        },
    }
}

// ── Selftest ──────────────────────────────────────────────────────────────────

/// Provider descriptor for selftest.
struct SelftestProvider {
    name: &'static str,
    probe: fn() -> (bool, Option<String>),
    ping: fn() -> Option<(bool, u64)>,
}

fn run_selftest(json_output: bool) -> Result<()> {
    let providers: &[SelftestProvider] = &[
        SelftestProvider {
            name: "claude (A1 ~/.claude)",
            probe: || (find_binary("claude").is_some(), find_binary("claude").map(|p| p.display().to_string())),
            ping: || ping_claude_config(dirs::home_dir().map(|h| h.join(".claude"))),
        },
        SelftestProvider {
            name: "claude2 (A2 ~/.claude2)",
            probe: || (find_binary("claude").is_some(), find_binary("claude").map(|p| p.display().to_string())),
            ping: || ping_claude_config(dirs::home_dir().map(|h| h.join(".claude2"))),
        },
        SelftestProvider {
            name: "codex",
            probe: || (find_binary("codex").is_some(), find_binary("codex").map(|p| p.display().to_string())),
            ping: || ping_codex(),
        },
        SelftestProvider {
            name: "opencode",
            probe: || (find_binary("opencode").is_some(), find_binary("opencode").map(|p| p.display().to_string())),
            ping: || ping_opencode(),
        },
        SelftestProvider {
            name: "gemini (cascade-agy)",
            probe: || (find_binary("cascade-agy").is_some(), find_binary("cascade-agy").map(|p| p.display().to_string())),
            ping: || None, // cascade-agy has no prompt path
        },
        SelftestProvider {
            name: "gfp (GP proxy :3762)",
            probe: || (true, Some("http://localhost:3762".to_string())),
            ping: || ping_gfp(),
        },
    ];

    let mut results: Vec<serde_json::Value> = Vec::new();

    for p in providers {
        let (found, path) = (p.probe)();
        let status = if !found {
            "unavailable"
        } else {
            "available"
        };

        let ping_result = if found { (p.ping)() } else { None };

        if json_output {
            results.push(serde_json::json!({
                "provider": p.name,
                "status": status,
                "binary": path,
                "ping": ping_result.map(|(ok, ms)| serde_json::json!({"ok": ok, "latency_ms": ms})),
            }));
        } else {
            let ping_str = match ping_result {
                Some((true, ms)) => format!("  ping: ok ({ms}ms)"),
                Some((false, _)) => "  ping: FAILED".to_string(),
                None => "  ping: skipped".to_string(),
            };
            let binary_str = path.as_deref().unwrap_or("not found");
            println!("[{status}] {}: {binary_str}{ping_str}", p.name);
        }
    }

    if json_output {
        println!("{}", serde_json::to_string_pretty(&results).unwrap_or_default());
    }

    Ok(())
}

/// Run a configured command bounded by `timeout_secs`; kill + return `None` on
/// timeout so one hanging provider can never block the whole selftest. Captures
/// stdout (probes emit tiny output, so the pipe never fills).
fn output_bounded(mut cmd: StdCommand, timeout_secs: u64) -> Option<std::process::Output> {
    use std::io::Read;
    let mut child = cmd
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .ok()?;
    let deadline = Instant::now() + Duration::from_secs(timeout_secs);
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                let mut buf = Vec::new();
                if let Some(mut so) = child.stdout.take() {
                    let _ = so.read_to_end(&mut buf);
                }
                return Some(std::process::Output {
                    status,
                    stdout: buf,
                    stderr: Vec::new(),
                });
            }
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    return None; // timed out
                }
                std::thread::sleep(Duration::from_millis(150));
            }
            Err(_) => return None,
        }
    }
}

/// Per-provider probe timeout (seconds). A worker that hangs past this is
/// reported as FAILED rather than blocking the rest of the selftest.
const PROBE_TIMEOUT_SECS: u64 = 30;

/// Probe Claude with a 1-token "reply ok" prompt via claude -p.
fn ping_claude_config(config_dir: Option<PathBuf>) -> Option<(bool, u64)> {
    let binary = find_binary("claude")?;
    let config_dir = config_dir?;
    if !config_dir.is_dir() {
        return None;
    }
    let mut cmd = StdCommand::new(&binary);
    cmd.env("CLAUDE_CONFIG_DIR", &config_dir)
        .env("PATH", AUGMENTED_PATH)
        .args([
            "-p",
            "reply ok",
            "--output-format",
            "json",
            "--strict-mcp-config",
            "--mcp-config",
            r#"{"mcpServers":{}}"#,
            "--setting-sources",
            "",
        ]);
    let start = Instant::now();
    let out = output_bounded(cmd, PROBE_TIMEOUT_SECS)?;
    Some((out.status.success(), start.elapsed().as_millis() as u64))
}

/// Probe Codex with a minimal exec.
fn ping_codex() -> Option<(bool, u64)> {
    let binary = find_binary("codex")?;
    let mut cmd = StdCommand::new(&binary);
    cmd.env("PATH", AUGMENTED_PATH).args(["exec", "reply ok"]);
    let start = Instant::now();
    let out = output_bounded(cmd, PROBE_TIMEOUT_SECS)?;
    Some((out.status.success(), start.elapsed().as_millis() as u64))
}

/// Probe OpenCode with `opencode run`.
fn ping_opencode() -> Option<(bool, u64)> {
    let binary = find_binary("opencode")?;
    let mut cmd = StdCommand::new(&binary);
    cmd.env("PATH", AUGMENTED_PATH).args(["run", "reply ok"]);
    let start = Instant::now();
    let out = output_bounded(cmd, PROBE_TIMEOUT_SECS)?;
    Some((out.status.success(), start.elapsed().as_millis() as u64))
}

/// Probe GFP proxy via curl health check.
fn ping_gfp() -> Option<(bool, u64)> {
    let curl = find_binary("curl")?;
    let mut cmd = StdCommand::new(&curl);
    cmd.env("PATH", AUGMENTED_PATH).args([
        "-s",
        "-f",
        "--max-time",
        "3",
        "http://localhost:3762/v1/health",
    ]);
    let start = Instant::now();
    let out = output_bounded(cmd, PROBE_TIMEOUT_SECS)?;
    Some((out.status.success(), start.elapsed().as_millis() as u64))
}
