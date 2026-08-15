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

use std::path::{Path, PathBuf};
use std::process::{Command as StdCommand, Stdio};
use std::time::{Duration, Instant};

use async_trait::async_trait;
use cascade_core::accounts_store::quota_json_path;
use cascade_core::conductor_router::{
    select_target_with_gp, ConductorRequest, ConductorTarget, GpHealthSnapshot, ModelClass,
    Provider, QuotaAccount, QuotaSnapshot, Tier,
};
use cascade_core::routing::gfp_http::{probe_gp_health, GFP_HEALTH_URL};
use cascade_types::error::{CascadeError, Result};
use cascade_types::model_ids::{MODEL_GEMINI_FLASH, MODEL_GEMINI_PRO};
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

        let req = ConductorRequest {
            tier,
            model_class,
            account_override,
        };

        // Load live quota snapshot.
        let snapshot = load_quota_snapshot();

        // Live GP pool health (E1-S6): probed from the proxy's /health
        // endpoint for T3 work so the T3-GP preference can actually fire.
        // Any probe failure yields the default (unhealthy) snapshot — the
        // spill order is then identical to the pre-unification behavior.
        let gp = gp_health_for_tier(tier);

        // Select target.
        let Some(target) = select_target_with_gp(&req, &snapshot, &gp) else {
            let msg =
                "cascade conductor: no available backend (all accounts saturated or unavailable)";
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
            let provider = entry
                .get("provider")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown")
                .to_string();
            let status = entry
                .get("status")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown")
                .to_string();
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

/// Typed classification of a spillable failure (CFC-04, T-P7-E13-02).
///
/// Each lane that the spill chain skips is recorded with one of these kinds,
/// assigned at the failure's point of origin. This replaces the old
/// `Vec<String>` of formatted `"account (reason)"` entries whose only
/// machine-readable consumer compared account ids by string PREFIX —
/// fragile because a shorter id (`claude`) prefix-matched a longer
/// previously-tried id (`claude2`) and broke the spill loop early.
#[derive(Debug, Clone)]
pub enum LaneFailure {
    /// Backend CLI absent, unreachable, or the process failed to exec/spill.
    Unavailable { reason: String },
    /// Sensitivity firewall: Sensitive prompt on an untrusted provider.
    SensitiveFirewall { provider: Provider },
}

impl LaneFailure {
    /// Human/JSON rendering of the failure reason.
    fn reason_text(&self) -> String {
        match self {
            LaneFailure::Unavailable { reason } => reason.clone(),
            LaneFailure::SensitiveFirewall { provider } => format!(
                "sensitive-content firewall: {} is not a trusted provider",
                provider.as_str()
            ),
        }
    }
}

/// One lane the spill chain has already tried and skipped past.
#[derive(Debug, Clone)]
pub struct TriedLane {
    pub account_id: String,
    pub failure: LaneFailure,
}

impl TriedLane {
    /// Display form: `"account_id (reason)"` — byte-identical to the
    /// pre-E13-02 `tried` string format, so exhausted-messages and JSON
    /// envelopes are unchanged.
    fn describe(&self) -> String {
        format!("{} ({})", self.account_id, self.failure.reason_text())
    }
}

/// Cycle guard over the typed tried-lane records (T-P7-E13-02).
///
/// Exact account-id equality only — never prefix matching. See
/// [`LaneFailure`] for why the old `starts_with` form was fragile.
fn lane_already_tried(tried: &[TriedLane], account_id: &str) -> bool {
    tried.iter().any(|lane| lane.account_id == account_id)
}

/// `fallbacks_tried` entries for messages/JSON envelopes.
fn tried_display(tried: &[TriedLane]) -> Vec<String> {
    tried.iter().map(TriedLane::describe).collect()
}

/// One dispatch attempt against the current lane, classified at its point
/// of origin (T-P7-E13-02): either the executor actually ran, or the
/// sensitivity firewall blocked the lane before any dispatch.
enum LaneAttempt {
    Ran(Outcome),
    BlockedByFirewall { provider: Provider },
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
    let mut tried: Vec<TriedLane> = Vec::new();
    let mut current = initial_target;

    // Sensitivity firewall — parity with the chat path (routing/router.rs).
    // Classify the prompt CONTENT once; a Sensitive prompt (PII / VA / health
    // / personal) must never be dispatched to an untrusted provider
    // (gemini / gfp / codex / opencode). We treat an untrusted target as
    // Unavailable so the existing spill chain continues to a trusted
    // (Claude / local) lane, and fails closed ("all backends exhausted") if
    // none remain — never leaking sensitive content to an external pool.
    let sensitive = cascade_core::sensitivity::classify_sensitivity(prompt)
        == cascade_core::sensitivity::ContentSensitivity::Sensitive;

    loop {
        // Classify-then-execute (T-P7-E13-02): each spillable failure gets a
        // typed LaneFailure at its point of origin. The spill/fallback
        // DECISION logic is unchanged — firewall blocks and executor
        // Unavailables both still spill; executor Errors are still fatal.
        let attempt = if sensitive
            && !cascade_core::sensitivity::registry_provider_is_trusted_for_sensitive(
                current.provider.as_str(),
            ) {
            LaneAttempt::BlockedByFirewall {
                provider: current.provider,
            }
        } else {
            LaneAttempt::Ran(execute_target(&current, prompt))
        };
        let failure = match attempt {
            LaneAttempt::Ran(Outcome::Success { output }) => {
                if json_output {
                    println!(
                        "{}",
                        serde_json::json!({
                            "provider": current.provider.as_str(),
                            "account_id": current.account_id,
                            "model": current.model,
                            "fallbacks_tried": tried_display(&tried),
                            "output": output,
                        })
                    );
                } else {
                    print!("{output}");
                }
                // Za local usage tracking: record dispatched prompt in the JSONL
                // log so the daemon can synthesise a rolling five-hour window.
                // Best-effort — errors silently ignored (never block the caller).
                if current.provider == Provider::Zai {
                    if let Some(home) = dirs::home_dir() {
                        let za_path = home.join(".cascade").join("za-usage.jsonl");
                        let ts = std::time::SystemTime::now()
                            .duration_since(std::time::UNIX_EPOCH)
                            .map(|d| d.as_secs())
                            .unwrap_or(0);
                        let rec = cascade_core::ZaRecord {
                            ts,
                            account: current.account_id.clone(),
                            prompts: 1,
                            est_input_tokens: (prompt.len() as u64) / 4,
                            est_output_tokens: (output.len() as u64) / 4,
                        };
                        let _ = std::panic::catch_unwind(|| {
                            cascade_core::za_append_record(&za_path, &rec);
                        });
                    }
                }
                // D9: fire-and-forget POST to daemon fleet-routing ring.
                // Non-blocking: spawned thread; failure is silently ignored
                // (the daemon may not be running; routing ring is best-effort).
                let task_class = tier_to_task_class(req.tier).to_string();
                let account_id = current.account_id.clone();
                let model = current.model.clone();
                let reason = current.reason.clone();
                std::thread::spawn(move || {
                    post_routing_event(&task_class, &account_id, &model, &reason);
                });
                return Ok(());
            }
            LaneAttempt::Ran(Outcome::Unavailable { reason }) => {
                eprintln!(
                    "cascade conductor: {} unavailable ({}), trying next...",
                    current.account_id, reason
                );
                LaneFailure::Unavailable { reason }
            }
            LaneAttempt::BlockedByFirewall { provider } => {
                eprintln!(
                    "cascade conductor: {} unavailable ({}), trying next...",
                    current.account_id,
                    LaneFailure::SensitiveFirewall { provider }.reason_text()
                );
                LaneFailure::SensitiveFirewall { provider }
            }
            LaneAttempt::Ran(Outcome::Error { message }) => {
                let msg = format!(
                    "cascade conductor: backend `{}` error: {}",
                    current.account_id, message
                );
                if json_output {
                    println!(
                        "{}",
                        serde_json::json!({"error": msg, "fallbacks_tried": tried_display(&tried)})
                    );
                } else {
                    eprintln!("{msg}");
                }
                std::process::exit(1);
            }
        };

        tried.push(TriedLane {
            account_id: current.account_id.clone(),
            failure,
        });

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
                tried_display(&tried).join(", ")
            );
            if json_output {
                println!(
                    "{}",
                    serde_json::json!({"error": msg, "fallbacks_tried": tried_display(&tried)})
                );
            } else {
                eprintln!("{msg}");
            }
            std::process::exit(1);
        };

        // Skip if we already tried this one (cycle guard, T-P7-E13-02:
        // exact account-id equality over typed records — never the old
        // `starts_with` prefix compare, which let a short id like
        // `claude` false-match a tried `claude2` and end the spill early).
        if lane_already_tried(&tried, &next.account_id) {
            break;
        }
        current = next;
    }

    let msg = format!(
        "cascade conductor: all backends exhausted (tried: {})",
        tried_display(&tried).join(", ")
    );
    if json_output {
        println!(
            "{}",
            serde_json::json!({"error": msg, "fallbacks_tried": tried_display(&tried)})
        );
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
        // z.ai GLM Coding Plan compliance: the plan permits usage only via supported
        // products (Claude Code with ANTHROPIC_BASE_URL set to the z.ai endpoint).
        // We dispatch through the same `claude -p` path as Provider::Claude, with
        // GLM env vars loaded from ~/.claude-glm/cascade-env.sh by apply_cascade_env().
        // Do NOT change this to a direct API call — that would violate the GLM plan.
        Provider::Zai => execute_claude(target, prompt),
        Provider::Gemini => execute_gemini(target, prompt),
        Provider::Gfp => execute_gfp(target, prompt),
    }
}

// ── Augmented PATH ────────────────────────────────────────────────────────────

/// Augmented PATH that ensures common install locations are always searched.
const AUGMENTED_PATH: &str =
    "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/opt/homebrew/sbin:/usr/local/sbin:/usr/sbin:/sbin";

/// Probe for a CLI binary name. Checks the real PATH first, then falls back to
/// well-known install dirs and ~/bin. Uses an executable-bit check rather than
/// just `.exists()` so we don't return directories or non-executable files.
fn find_binary(name: &str) -> Option<PathBuf> {
    // 1. Real PATH env var — what the shell actually uses.
    if let Some(path_var) = std::env::var_os("PATH") {
        for dir in std::env::split_paths(&path_var) {
            let candidate = dir.join(name);
            if is_executable_file(&candidate) {
                return Some(candidate);
            }
        }
    }
    // 2. Well-known install locations not always in PATH (e.g. restricted shells).
    let extra_dirs = ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"];
    for dir in &extra_dirs {
        let candidate = PathBuf::from(dir).join(name);
        if is_executable_file(&candidate) {
            return Some(candidate);
        }
    }
    // 3. ~/bin/<name> (user-local installs).
    if let Some(home) = dirs::home_dir() {
        let candidate = home.join("bin").join(name);
        if is_executable_file(&candidate) {
            return Some(candidate);
        }
    }
    None
}

/// Returns true only if `path` is a regular file with at least one executable bit set.
fn is_executable_file(path: &Path) -> bool {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        path.metadata()
            .map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
            .unwrap_or(false)
    }
    #[cfg(not(unix))]
    {
        path.is_file()
    }
}

fn apply_cascade_env(cmd: &mut StdCommand, config_dir: &Path) {
    let env_path = config_dir.join("cascade-env.sh");
    let Ok(content) = std::fs::read_to_string(&env_path) else {
        return;
    };
    for (key, value) in parse_cascade_env(&content) {
        cmd.env(key, value);
    }
}

fn parse_cascade_env(content: &str) -> Vec<(String, String)> {
    let mut vars = Vec::new();
    for raw in content.lines() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let line = line.strip_prefix("export ").unwrap_or(line).trim();
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let key = key.trim();
        if !is_env_key(key) {
            continue;
        }
        vars.push((key.to_string(), parse_env_value(value.trim())));
    }
    vars
}

fn is_env_key(key: &str) -> bool {
    let mut chars = key.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    (first == '_' || first.is_ascii_alphabetic())
        && chars.all(|c| c == '_' || c.is_ascii_alphanumeric())
}

fn parse_env_value(value: &str) -> String {
    if value.len() >= 2 {
        if let Some(inner) = value.strip_prefix('"').and_then(|v| v.strip_suffix('"')) {
            return inner.replace("\\\"", "\"").replace("\\\\", "\\");
        }
        if let Some(inner) = value.strip_prefix('\'').and_then(|v| v.strip_suffix('\'')) {
            return inner.to_string();
        }
    }
    value.to_string()
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
    apply_cascade_env(&mut cmd, &config_dir);
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

// ── Gemini backend (via agy CLI) ─────────────────────────────────────────────
//
// Dispatch lane for the owner's paid Google AI Pro subscription via the `agy`
// CLI. Model display name is mapped from the canonical model id by
// map_model_to_agy_display. The lane returns Unavailable if `agy` is not
// found in PATH, allowing the conductor spill chain to move on.

/// Extract the completion text from a `v1internal:generateContent` response.
///
/// Walks `candidates[0].content.parts[]` and concatenates every part's `text`
/// field, skipping parts that are thought-signature-only (no `text` field, or
/// a `thought: true` marker with no visible text).
///
/// cloudcode-pa's `v1internal:generateContent` wraps the Gemini payload under a
/// top-level `response` key (alongside `traceId`/`metadata`), so unwrap that
/// first; fall back to the bare shape for forward-compat.
#[allow(dead_code)]
fn extract_generate_content_text(resp: &Value) -> Option<String> {
    let root = resp.get("response").unwrap_or(resp);
    let candidates = root.get("candidates")?.as_array()?;
    let first = candidates.first()?;
    let parts = first.get("content")?.get("parts")?.as_array()?;

    let mut out = String::new();
    for part in parts {
        // Skip thought-signature-only parts (no visible text, or explicitly
        // marked as a thought rather than user-facing content).
        if part.get("thoughtSignature").is_some() && part.get("text").is_none() {
            continue;
        }
        if part.get("thought").and_then(|t| t.as_bool()) == Some(true) && part.get("text").is_none()
        {
            continue;
        }
        if let Some(text) = part.get("text").and_then(|t| t.as_str()) {
            out.push_str(text);
        }
    }

    if out.is_empty() {
        None
    } else {
        Some(out)
    }
}

/// Dispatch `prompt` to Gemini via the `agy` CLI.
///
/// Command: `agy -p "<prompt>" --model "<display name>" --dangerously-skip-permissions
///            --add-dir <cwd>`
///
/// Model display name is derived from the target model id via
/// `map_model_to_agy_display`. Any failure returns `Unavailable` so the
/// conductor spill chain moves to the next candidate — this lane never blocks
/// the fallback path.
fn execute_gemini(target: &ConductorTarget, prompt: &str) -> Outcome {
    execute_gemini_with_binary(target, prompt, "agy")
}

fn execute_gemini_with_binary(target: &ConductorTarget, prompt: &str, bin_name: &str) -> Outcome {
    let Some(agy) = find_binary(bin_name) else {
        return Outcome::Unavailable {
            reason: "agy not found in PATH (run: cargo install cascade-agy or place ~/bin/agy)"
                .to_string(),
        };
    };

    let model_display = map_model_to_agy_display(&target.model);

    let cwd = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("/tmp"));

    let mut cmd = StdCommand::new(&agy);
    cmd.env("PATH", AUGMENTED_PATH)
        .args(["-p", prompt])
        .args(["--model", model_display])
        .arg("--dangerously-skip-permissions")
        .args(["--add-dir", cwd.to_str().unwrap_or("/tmp")])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    run_command(cmd, "agy")
}

/// Map a canonical model id to the agy CLI display name.
fn map_model_to_agy_display(model: &str) -> &'static str {
    if model.contains(MODEL_GEMINI_FLASH) {
        return "Gemini 3.5 Flash (High)";
    }
    if model.contains(MODEL_GEMINI_PRO) {
        return "Gemini 3.1 Pro (High)";
    }
    if model.contains("opus") {
        return "Claude Opus 4.6 (Thinking)";
    }
    if model.contains("sonnet") {
        return "Claude Sonnet 4.6 (Thinking)";
    }
    if model.contains("gpt-oss-120b") {
        return "GPT-OSS 120B (Medium)";
    }
    "Gemini 3.1 Pro (High)"
}

// ── GFP backend ───────────────────────────────────────────────────────────────

/// Attempt to POST to the GFP proxy at localhost:3762 (Anthropic-compat adapter).
///
/// Returns `Unavailable` when the proxy is not running (connection refused).
fn execute_gfp(_target: &ConductorTarget, prompt: &str) -> Outcome {
    // Use a simple TCP probe + HTTP POST via std (no async here).
    // The :3762 anthropic-compat proxy remaps by claude-* prefix → Gemini
    // (claude-sonnet* → gemini-2.0-flash), so a current sonnet id keeps the
    // GFP lane on the free flash tier without a stale literal.
    let url = "http://localhost:3762/v1/messages";
    let body = serde_json::json!({
        "model": cascade_core::model_ids::MODEL_CLAUDE_SONNET,
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

// ── Fleet routing event (D9) ──────────────────────────────────────────────────

/// Map a conductor tier to the canonical task_class string expected by the
/// daemon's fleet-routing ring (matches RoutingEvent.task_class conventions).
fn tier_to_task_class(tier: Tier) -> &'static str {
    match tier {
        Tier::T1 => "final-gate",
        Tier::T2 => "bulk-execution",
        Tier::T3 => "grunt",
    }
}

/// Read `~/.cascade/dashboard.token` (the daemon bearer token).
/// Returns `None` when the file is absent or unreadable (daemon not running).
fn read_dashboard_token() -> Option<String> {
    let path = dirs::home_dir()?.join(".cascade").join("dashboard.token");
    std::fs::read_to_string(&path)
        .ok()
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
}

/// Fire-and-forget: POST one RoutingEvent to the daemon's fleet routing ring.
///
/// Called from a background thread after a successful conductor dispatch so
/// the Fleet UI ring buffer reflects real conductor routing decisions.
///
/// Silently exits on any failure — the daemon ring is best-effort telemetry.
fn post_routing_event(task_class: &str, account_id: &str, model: &str, reason: &str) {
    let Some(token) = read_dashboard_token() else {
        return;
    };
    let Some(curl) = find_binary("curl") else {
        return;
    };

    let body = serde_json::json!({
        "task_class": task_class,
        "account_id": account_id,
        "model": model,
        "reason": reason,
    });
    let body_str = match serde_json::to_string(&body) {
        Ok(s) => s,
        Err(_) => return,
    };

    let mut cmd = StdCommand::new(&curl);
    cmd.env("PATH", AUGMENTED_PATH)
        .args([
            "-s",
            "-o",
            "/dev/null",
            "--max-time",
            "2",
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "-H",
            &format!("Authorization: Bearer {token}"),
            "-d",
            &body_str,
            "http://127.0.0.1:9761/api/fleet/routing",
        ])
        .stdout(Stdio::null())
        .stderr(Stdio::null());

    // We don't care about the exit status — best-effort telemetry.
    let _ = cmd.output();
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
            probe: || {
                (
                    find_binary("claude").is_some(),
                    find_binary("claude").map(|p| p.display().to_string()),
                )
            },
            ping: || ping_claude_config(dirs::home_dir().map(|h| h.join(".claude"))),
        },
        SelftestProvider {
            name: "claude2 (A2 ~/.claude2)",
            probe: || {
                (
                    find_binary("claude").is_some(),
                    find_binary("claude").map(|p| p.display().to_string()),
                )
            },
            ping: || ping_claude_config(dirs::home_dir().map(|h| h.join(".claude2"))),
        },
        SelftestProvider {
            name: "codex",
            probe: || {
                (
                    find_binary("codex").is_some(),
                    find_binary("codex").map(|p| p.display().to_string()),
                )
            },
            ping: || ping_codex(),
        },
        SelftestProvider {
            name: "opencode",
            probe: || {
                (
                    find_binary("opencode").is_some(),
                    find_binary("opencode").map(|p| p.display().to_string()),
                )
            },
            ping: || ping_opencode(),
        },
        SelftestProvider {
            name: "gemini (cascade-agy)",
            probe: || {
                (
                    find_binary("cascade-agy").is_some(),
                    find_binary("cascade-agy").map(|p| p.display().to_string()),
                )
            },
            ping: || None, // cascade-agy has no prompt path
        },
        SelftestProvider {
            name: "zai (GLM Coding Plan / ~/.claude-glm)",
            probe: || {
                let has_claude = find_binary("claude").is_some();
                let has_env = dirs::home_dir()
                    .map(|h| h.join(".claude-glm").join("cascade-env.sh").exists())
                    .unwrap_or(false);
                (
                    has_claude && has_env,
                    find_binary("claude").map(|p| p.display().to_string()),
                )
            },
            ping: || {
                // Skipped when ~/.claude-glm/cascade-env.sh is absent (Zai not installed).
                if !dirs::home_dir()
                    .map(|h| h.join(".claude-glm").join("cascade-env.sh").exists())
                    .unwrap_or(false)
                {
                    return None;
                }
                ping_claude_config(dirs::home_dir().map(|h| h.join(".claude-glm")))
            },
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
        let status = if !found { "unavailable" } else { "available" };

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
        println!(
            "{}",
            serde_json::to_string_pretty(&results).unwrap_or_default()
        );
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
    apply_cascade_env(&mut cmd, &config_dir);
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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod agy_tests {
    use super::*;

    // ── Loop-guard / structured failure classification (T-P7-E13-02) ─────────

    /// Regression for the fragile prefix guard: a previously-tried `claude2`
    /// must NOT count as `claude` already tried. The old
    /// `t.starts_with(&next.account_id)` over `"claude2 (reason)"` returned
    /// TRUE for `claude`, ending the spill loop one lane early.
    #[test]
    fn cycle_guard_no_prefix_false_match_between_account_ids() {
        let tried = vec![TriedLane {
            account_id: "claude2".to_string(),
            failure: LaneFailure::Unavailable {
                reason: "claude binary not found in PATH".to_string(),
            },
        }];
        // `claude` was never tried — the guard must NOT fire.
        assert!(!lane_already_tried(&tried, "claude"));
        // Exact id does fire.
        assert!(lane_already_tried(&tried, "claude2"));
    }

    /// The display form must stay byte-identical to the pre-E13-02
    /// `"account (reason)"` format (consumed by exhausted-messages and the
    /// JSON `fallbacks_tried` envelope).
    #[test]
    fn tried_lane_describe_keeps_legacy_string_format() {
        let lane = TriedLane {
            account_id: "codex-acc1".to_string(),
            failure: LaneFailure::Unavailable {
                reason: "codex binary not found in PATH".to_string(),
            },
        };
        assert_eq!(
            lane.describe(),
            "codex-acc1 (codex binary not found in PATH)"
        );
    }

    /// The firewall failure renders the same reason string the pre-E13-02
    /// inline format! produced.
    #[test]
    fn lane_failure_sensitive_firewall_reason_matches_legacy_text() {
        let f = LaneFailure::SensitiveFirewall {
            provider: Provider::Gemini,
        };
        assert_eq!(
            f.reason_text(),
            "sensitive-content firewall: gemini is not a trusted provider"
        );
    }

    /// Captured (shape-accurate, values synthesized) `v1internal:generateContent`
    /// response fixture: a leading thought-signature-only part (no visible
    /// text) followed by the real completion text, matching what cloudcode-pa
    /// returns for `gemini-pro-agent` reasoning-model calls.
    const FIXTURE_WITH_THOUGHT: &str = r#"{
        "candidates": [
            {
                "content": {
                    "role": "model",
                    "parts": [
                        {"thoughtSignature": "opaque-base64-signature=="},
                        {"text": "Hello from Gemini Pro."}
                    ]
                },
                "finishReason": "STOP"
            }
        ]
    }"#;

    /// Fixture with a `thought: true` marker (alternate shape some responses
    /// use) and no `thoughtSignature` field, still with no visible text.
    const FIXTURE_WITH_THOUGHT_BOOL: &str = r#"{
        "candidates": [
            {
                "content": {
                    "role": "model",
                    "parts": [
                        {"thought": true},
                        {"text": "Part one. "},
                        {"text": "Part two."}
                    ]
                }
            }
        ]
    }"#;

    /// Fixture with no candidates at all (e.g. safety block / empty response).
    const FIXTURE_EMPTY: &str = r#"{"candidates": []}"#;

    /// Fixture where the only part is thought-signature-only (no usable text).
    const FIXTURE_THOUGHT_ONLY: &str = r#"{
        "candidates": [
            {"content": {"parts": [{"thoughtSignature": "sig=="}]}}
        ]
    }"#;

    #[test]
    fn extract_text_skips_thought_signature_part() {
        let v: Value = serde_json::from_str(FIXTURE_WITH_THOUGHT).unwrap();
        let text = extract_generate_content_text(&v).expect("should extract text");
        assert_eq!(text, "Hello from Gemini Pro.");
    }

    #[test]
    fn extract_text_concatenates_multiple_text_parts_and_skips_thought_bool() {
        let v: Value = serde_json::from_str(FIXTURE_WITH_THOUGHT_BOOL).unwrap();
        let text = extract_generate_content_text(&v).expect("should extract text");
        assert_eq!(text, "Part one. Part two.");
    }

    /// The REAL cloudcode-pa shape: candidates wrapped under a top-level
    /// `response` key alongside `traceId`/`metadata`. Verified live 2026-07-06.
    const FIXTURE_WRAPPED: &str = r#"{
        "response": {
            "candidates": [
                {"content": {"role": "model", "parts": [
                    {"thoughtSignature": "sig=="},
                    {"text": "81"}
                ]}}
            ]
        },
        "traceId": "abc123",
        "metadata": {}
    }"#;

    /// The sensitivity firewall's block condition: a Sensitive prompt routed
    /// to an untrusted provider must be blocked; a public prompt or a trusted
    /// provider must not. Mirrors the guard in the dispatch loop.
    #[test]
    fn sensitivity_firewall_blocks_untrusted_for_sensitive_content() {
        use cascade_core::sensitivity::{
            classify_sensitivity, registry_provider_is_trusted_for_sensitive, ContentSensitivity,
        };
        let sensitive_prompt = "my SSN is 123-45-6789 and my VA file number is 12345678";
        let public_prompt = "refactor this loop to use iterators";
        let is_sensitive = |p: &str| classify_sensitivity(p) == ContentSensitivity::Sensitive;
        let blocked = |p: &str, prov: &str| {
            is_sensitive(p) && !registry_provider_is_trusted_for_sensitive(prov)
        };
        // Sensitive + untrusted → blocked (spills / fails closed).
        assert!(blocked(sensitive_prompt, "gemini"));
        assert!(blocked(sensitive_prompt, "gfp"));
        assert!(blocked(sensitive_prompt, "codex"));
        // Sensitive + trusted → allowed.
        assert!(!blocked(sensitive_prompt, "claude"));
        // Public content → never blocked, even to untrusted.
        assert!(!blocked(public_prompt, "gemini"));
    }

    #[test]
    fn extract_text_unwraps_cloudcode_response_envelope() {
        let v: Value = serde_json::from_str(FIXTURE_WRAPPED).unwrap();
        let text = extract_generate_content_text(&v).expect("should unwrap response envelope");
        assert_eq!(text, "81");
    }

    #[test]
    fn extract_text_none_when_no_candidates() {
        let v: Value = serde_json::from_str(FIXTURE_EMPTY).unwrap();
        assert!(extract_generate_content_text(&v).is_none());
    }

    #[test]
    fn extract_text_none_when_only_thought_signature() {
        let v: Value = serde_json::from_str(FIXTURE_THOUGHT_ONLY).unwrap();
        assert!(extract_generate_content_text(&v).is_none());
    }

    #[test]
    fn execute_gemini_unavailable_when_agy_not_in_path() {
        // When `agy` is not found in PATH the lane returns Unavailable without
        // blocking. Use a non-existent binary so find_binary returns None.
        let target = ConductorTarget {
            provider: Provider::Gemini,
            account_id: "gemini-acc1".to_string(),
            model: MODEL_GEMINI_PRO.to_string(),
            config_dir: None,
            reason: "test".to_string(),
        };
        let result = execute_gemini_with_binary(&target, "hello", "__no_such_binary_agy__");
        match result {
            Outcome::Unavailable { .. } => {}
            other => panic!("expected Unavailable, got {other:?}"),
        }
    }

    #[test]
    fn map_model_to_agy_display_covers_all_branches() {
        assert_eq!(
            map_model_to_agy_display("gemini-3.5-flash-001"),
            "Gemini 3.5 Flash (High)"
        );
        assert_eq!(
            map_model_to_agy_display(MODEL_GEMINI_PRO),
            "Gemini 3.1 Pro (High)"
        );
        assert_eq!(
            map_model_to_agy_display("claude-opus-4-6"),
            "Claude Opus 4.6 (Thinking)"
        );
        assert_eq!(
            map_model_to_agy_display("claude-sonnet-4-6"),
            "Claude Sonnet 4.6 (Thinking)"
        );
        assert_eq!(
            map_model_to_agy_display("gpt-oss-120b"),
            "GPT-OSS 120B (Medium)"
        );
        assert_eq!(
            map_model_to_agy_display("unknown-model"),
            "Gemini 3.1 Pro (High)"
        );
    }
}
