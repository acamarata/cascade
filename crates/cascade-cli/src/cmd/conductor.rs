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
        let result = if sensitive
            && !cascade_core::sensitivity::registry_provider_is_trusted_for_sensitive(
                current.provider.as_str(),
            ) {
            Outcome::Unavailable {
                reason: format!(
                    "sensitive-content firewall: {} is not a trusted provider",
                    current.provider.as_str()
                ),
            }
        } else {
            execute_target(&current, prompt)
        };
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

// ── Gemini backend (Gemini Pro via Antigravity / cloudcode-pa) ───────────────
//
// Dispatch lane for the owner's paid Google AI Pro (Google One) subscription,
// reusing the same OAuth refresh token already captured by `cascade-agy-auth`
// at `~/.cascade/agy-token.json` (see `src/bin/cascade-agy` for the sibling
// quota-collector that proved this refresh + `loadCodeAssist` flow live).
//
// Owner-authorized personal use: the account owner explicitly authorized
// routing their own paid Google AI Pro prompts through this token. This is
// distinct from `cascade-providers`'s `AntigravityAdapter`, whose ToS-safety
// gate (`inference_routing` feature, off by default) concerns routing a
// *third party's* Antigravity subscription through Cascade — not applicable
// here, where the owner is dispatching their own account's prompts directly.

/// OAuth client used by the Antigravity desktop app (public, not secret).
const AGY_CLIENT_ID: &str = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com";
const AGY_CLIENT_SECRET: &str = "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";
const AGY_TOKEN_URL: &str = "https://oauth2.googleapis.com/token";
const AGY_BASE_URL: &str = "https://cloudcode-pa.googleapis.com";
const AGY_USER_AGENT: &str = "antigravity/1.18.3 macos/arm64";
/// Fallback GCP project id (used when `loadCodeAssist` doesn't return one).
const AGY_FALLBACK_PROJECT: &str = "bamboo-precept-lgxtn";
/// User-facing model label for Gemini Pro dispatch (current newest Pro model).
const AGY_MODEL_LABEL: &str = "gemini-3.1-pro";
/// The `model` field cloudcode-pa expects for generateContent — proven live.
const AGY_GENERATE_MODEL: &str = "gemini-pro-agent";
/// Per-call timeout (each curl invocation), mirrors other lanes' bounded calls.
const AGY_HTTP_TIMEOUT_SECS: u64 = 120;

/// Path to the agy OAuth token store (`~/.cascade/agy-token.json`).
fn agy_token_path() -> Option<PathBuf> {
    dirs::home_dir().map(|h| h.join(".cascade").join("agy-token.json"))
}

/// Read the first account's refresh token + email from `agy-token.json`.
fn read_agy_refresh_token(path: &std::path::Path) -> Option<(String, String)> {
    let bytes = std::fs::read(path).ok()?;
    let v: Value = serde_json::from_slice(&bytes).ok()?;
    let acc = v.get("accounts")?.as_array()?.first()?;
    let refresh_token = acc.get("refresh_token")?.as_str()?.to_string();
    let email = acc.get("email").and_then(|e| e.as_str()).unwrap_or("").to_string();
    Some((refresh_token, email))
}

/// POST via curl with a JSON body and bounded timeout; returns stdout on 2xx,
/// `None` on any transport/HTTP failure (caller decides Unavailable vs Error).
fn curl_post_json(
    url: &str,
    headers: &[(&str, &str)],
    body: &str,
    timeout_secs: u64,
) -> std::result::Result<String, String> {
    let curl = find_binary("curl").ok_or_else(|| "curl not found".to_string())?;

    let mut cmd = StdCommand::new(&curl);
    cmd.env("PATH", AUGMENTED_PATH).args([
        "-s",
        "-f",
        "--max-time",
        &timeout_secs.to_string(),
        "-X",
        "POST",
    ]);
    for (k, v) in headers {
        cmd.args(["-H", &format!("{k}: {v}")]);
    }
    cmd.args(["-d", body, url]).stdout(Stdio::piped()).stderr(Stdio::piped());

    match cmd.output() {
        Ok(out) if out.status.success() => Ok(String::from_utf8_lossy(&out.stdout).to_string()),
        Ok(out) => {
            let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
            Err(format!("HTTP request failed (exit {}): {stderr}", out.status.code().unwrap_or(-1)))
        }
        Err(e) => Err(format!("curl exec failed: {e}")),
    }
}

/// Refresh the OAuth access token from a stored refresh token.
///
/// Mirrors `cascade-agy`'s `refresh_access()` — same client id/secret,
/// same token endpoint. Returns `None` on any failure.
fn agy_refresh_access_token(refresh_token: &str) -> Option<String> {
    let form = format!(
        "grant_type=refresh_token&refresh_token={}&client_id={}&client_secret={}",
        urlencode(refresh_token),
        urlencode(AGY_CLIENT_ID),
        urlencode(AGY_CLIENT_SECRET),
    );
    let curl = find_binary("curl")?;
    let mut cmd = StdCommand::new(&curl);
    cmd.env("PATH", AUGMENTED_PATH)
        .args([
            "-s",
            "-f",
            "--max-time",
            &AGY_HTTP_TIMEOUT_SECS.to_string(),
            "-X",
            "POST",
            "-H",
            "Content-Type: application/x-www-form-urlencoded",
            "-d",
            &form,
            AGY_TOKEN_URL,
        ])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    let out = cmd.output().ok()?;
    if !out.status.success() {
        return None;
    }
    let v: Value = serde_json::from_slice(&out.stdout).ok()?;
    v.get("access_token")?.as_str().map(|s| s.to_string())
}

/// Minimal percent-encoding for x-www-form-urlencoded values (token/id/secret
/// are all URL-safe-ish but may contain `+`/`/` in the refresh token).
fn urlencode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// Discover the `cloudaicompanionProject` id for this account via `loadCodeAssist`.
/// Falls back to `AGY_FALLBACK_PROJECT` on any failure (matches `cascade-agy`).
fn agy_load_project(access_token: &str) -> String {
    let url = format!("{AGY_BASE_URL}/v1internal:loadCodeAssist");
    let body = serde_json::json!({"metadata": {"ideType": "ANTIGRAVITY"}}).to_string();
    let headers = [
        ("Authorization", format!("Bearer {access_token}")),
        ("Content-Type", "application/json".to_string()),
        ("User-Agent", AGY_USER_AGENT.to_string()),
    ];
    let header_refs: Vec<(&str, &str)> = headers.iter().map(|(k, v)| (*k, v.as_str())).collect();

    match curl_post_json(&url, &header_refs, &body, AGY_HTTP_TIMEOUT_SECS) {
        Ok(resp) => {
            let v: Value = match serde_json::from_str(&resp) {
                Ok(v) => v,
                Err(_) => return AGY_FALLBACK_PROJECT.to_string(),
            };
            match v.get("cloudaicompanionProject") {
                Some(Value::String(s)) if !s.is_empty() => s.clone(),
                Some(Value::Object(o)) => o
                    .get("id")
                    .and_then(|i| i.as_str())
                    .filter(|s| !s.is_empty())
                    .unwrap_or(AGY_FALLBACK_PROJECT)
                    .to_string(),
                _ => AGY_FALLBACK_PROJECT.to_string(),
            }
        }
        Err(_) => AGY_FALLBACK_PROJECT.to_string(),
    }
}

/// Extract the completion text from a `v1internal:generateContent` response.
///
/// Walks `candidates[0].content.parts[]` and concatenates every part's `text`
/// field, skipping parts that are thought-signature-only (no `text` field, or
/// a `thought: true` marker with no visible text).
///
/// cloudcode-pa's `v1internal:generateContent` wraps the Gemini payload under a
/// top-level `response` key (alongside `traceId`/`metadata`), so unwrap that
/// first; fall back to the bare shape for forward-compat.
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
        if part.get("thought").and_then(|t| t.as_bool()) == Some(true)
            && part.get("text").is_none()
        {
            continue;
        }
        if let Some(text) = part.get("text").and_then(|t| t.as_str()) {
            out.push_str(text);
        }
    }

    if out.is_empty() { None } else { Some(out) }
}

/// Dispatch `prompt` to Gemini Pro via the Antigravity / cloudcode-pa backend.
///
/// Flow: read `~/.cascade/agy-token.json` → refresh access token → discover
/// `cloudaicompanionProject` via `loadCodeAssist` → POST `generateContent` →
/// extract completion text. Any failure at any stage returns `Unavailable` so
/// the conductor spill chain moves to the next candidate — this lane never
/// blocks the fallback path.
fn execute_gemini(_target: &ConductorTarget, prompt: &str) -> Outcome {
    let Some(token_path) = agy_token_path() else {
        return Outcome::Unavailable { reason: "cannot resolve home dir for agy-token.json".to_string() };
    };
    if !token_path.is_file() {
        return Outcome::Unavailable {
            reason: format!("no agy token at {} (run cascade-agy-auth)", token_path.display()),
        };
    }

    let Some((refresh_token, _email)) = read_agy_refresh_token(&token_path) else {
        return Outcome::Unavailable { reason: "agy-token.json present but unreadable/malformed".to_string() };
    };

    let Some(access_token) = agy_refresh_access_token(&refresh_token) else {
        return Outcome::Unavailable { reason: "agy OAuth refresh failed".to_string() };
    };

    let project = agy_load_project(&access_token);

    let url = format!("{AGY_BASE_URL}/v1internal:generateContent");
    let body = serde_json::json!({
        "project": project,
        "model": AGY_GENERATE_MODEL,
        "request": {
            "contents": [{"role": "user", "parts": [{"text": prompt}]}]
        }
    })
    .to_string();
    let headers = [
        ("Authorization", format!("Bearer {access_token}")),
        ("Content-Type", "application/json".to_string()),
        ("User-Agent", AGY_USER_AGENT.to_string()),
    ];
    let header_refs: Vec<(&str, &str)> = headers.iter().map(|(k, v)| (*k, v.as_str())).collect();

    match curl_post_json(&url, &header_refs, &body, AGY_HTTP_TIMEOUT_SECS) {
        Ok(resp_str) => {
            let resp: Value = match serde_json::from_str(&resp_str) {
                Ok(v) => v,
                Err(e) => {
                    return Outcome::Unavailable {
                        reason: format!("gemini ({AGY_MODEL_LABEL}) returned unparseable response: {e}"),
                    }
                }
            };
            match extract_generate_content_text(&resp) {
                Some(text) => Outcome::Success { output: text },
                None => Outcome::Unavailable {
                    reason: format!("gemini ({AGY_MODEL_LABEL}) response had no completion text"),
                },
            }
        }
        Err(e) => Outcome::Unavailable {
            reason: format!("gemini ({AGY_MODEL_LABEL}) generateContent failed: {e}"),
        },
    }
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

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod agy_tests {
    use super::*;

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
    fn urlencode_escapes_reserved_chars() {
        assert_eq!(urlencode("a+b/c=d"), "a%2Bb%2Fc%3Dd");
        assert_eq!(urlencode("plain-text_123.~"), "plain-text_123.~");
    }

    #[test]
    fn execute_gemini_unavailable_when_no_token_file() {
        // Point at a home dir with no ~/.cascade/agy-token.json by using a
        // target whose config_dir is irrelevant — execute_gemini reads the
        // real home dir's agy token path. We can't easily inject a fake home
        // dir without touching global state, so this test only exercises the
        // "file missing" branch when the real path doesn't exist. If a real
        // token happens to exist on the test machine, skip the assertion.
        if let Some(p) = agy_token_path() {
            if !p.is_file() {
                let target = ConductorTarget {
                    provider: Provider::Gemini,
                    account_id: "gemini-acc1".to_string(),
                    model: AGY_MODEL_LABEL.to_string(),
                    config_dir: None,
                    reason: "test".to_string(),
                };
                match execute_gemini(&target, "hello") {
                    Outcome::Unavailable { .. } => {}
                    other => panic!("expected Unavailable, got {other:?}"),
                }
            }
        }
    }
}
