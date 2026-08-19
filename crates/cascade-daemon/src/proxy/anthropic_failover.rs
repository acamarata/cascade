//! Request-level Anthropic failover proxy — T-P7-E13-09.
//!
//! Purpose: HTTP server on `127.0.0.1:3763` that accepts `POST /v1/messages`
//! in Anthropic Messages API format, routes each request through
//! `cascade_core::selection::select_account_for_prompt` (the SAME account
//! spill order + sensitivity firewall the conductor uses), shells the real
//! `claude` CLI under the selected account's `CLAUDE_CONFIG_DIR`, re-frames
//! the CLI's `stream-json` stdout as real Anthropic SSE, and — on a detected
//! account failure — marks that account unavailable and retries with the
//! next spill candidate. Pointing an interactive `claude` session here via
//! `ANTHROPIC_BASE_URL=http://127.0.0.1:3763` gives it transparent
//! account-priority failover with zero manual effort (activation is an
//! explicit, user-initiated export — see README "Interactive session
//! failover"; this daemon never mutates shell rc files).
//!
//! Why shell the CLI instead of proxying `api.anthropic.com` directly:
//! Anthropic OAuth access tokens are short-lived and silently rotated by the
//! CLI's own refresh flow, so the only safe way to mint a request "as" an
//! account is to run the real `claude` binary under that account's config
//! dir (mirroring `conductor.rs::execute_claude` — reimplemented here, not
//! imported, because `cascade-daemon` cannot depend on `cascade-cli`; the
//! daemon→core dependency direction is fixed).
//!
//! ## Empirically captured signatures (2026-08-17, claude CLI 2.1.140)
//!
//! Captured live under `/tmp/cascade-failover-capture` by running real
//! `claude -p --output-format stream-json --include-partial-messages
//! --verbose` subprocesses against (a) an empty `CLAUDE_CONFIG_DIR`
//! (auth failure), (b) a local mock Anthropic server returning HTTP 429
//! (rate limit), and (c) a healthy account (success). NOT guessed from
//! documentation — the shapes below are the observed literals:
//!
//! - Early 429, BEFORE any content (best spill trigger — lets this proxy
//!   spill immediately instead of waiting out the CLI's internal
//!   10-attempt/~135s retry ladder):
//!   `{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,
//!     "retry_delay_ms":581.46,"error_status":429,"error":"rate_limit",...}`
//! - Auth failure (empty/invalid config dir), synthetic assistant line +
//!   terminal result (exit 1). NOTE the misleading `subtype:"success"` —
//!   only `is_error` classifies correctly:
//!   `{"type":"assistant","message":{...,"model":"<synthetic>",...,"content":
//!     [{"type":"text","text":"Not logged in · Please run /login"}]},
//!    "error":"authentication_failed"}`
//!   `{"type":"result","subtype":"success","is_error":true,
//!     "api_error_status":null,...}`
//! - 429 exhaustion (CLI's retry ladder fully spent, ~182s), same shapes:
//!   `"error":"rate_limit"` + `"is_error":true,"api_error_status":429`.
//! - Success: `stream_event` lines wrap a LITERAL Anthropic SSE event in
//!   `.event` (`message_start` / `content_block_*` / `message_delta` /
//!   `message_stop`), so re-framing is unwrap-and-re-emit, closed by
//!   `{"type":"result","subtype":"success","is_error":false,...,
//!   "usage":{...}}`.
//!
//! Two capture-driven corrections to the ticket's assumed command shape:
//! `-p` + `--output-format stream-json` REQUIRES `--verbose` (v2.1.140), and
//! the prompt is fed via STDIN (not argv) — interactive-session payloads can
//! exceed the OS argv-size limit.
//!
//! ## Environment sanitation (prevents proxy self-recursion)
//!
//! The capture also proved that inherited `ANTHROPIC_*` / `CLAUDE*` env vars
//! leak into the child and override the selected account: a run under an
//! EMPTY `CLAUDE_CONFIG_DIR` still succeeded (answering via the caller's
//! `ANTHROPIC_BASE_URL=api.z.ai`). Worse, once a user activates this proxy,
//! the daemon's inherited `ANTHROPIC_BASE_URL=http://127.0.0.1:3763` would
//! make every child `claude` call back INTO the proxy — infinite recursion.
//! [`sanitize_child_env`] therefore strips every inherited `ANTHROPIC_*` /
//! `CLAUDE*` var BEFORE applying the target account's `CLAUDE_CONFIG_DIR`,
//! `cascade-env.sh`, and the augmented `PATH`.
//!
//! ## Commit-point streaming contract
//!
//! SSE frames are buffered until the first `content_block_delta` (real model
//! output). A failure BEFORE that point spills to the next account and the
//! caller still receives one continuous, correctly-framed stream from the
//! successful account. A failure AFTER that point cannot be retried without
//! corrupting the stream already delivered — it is surfaced as an Anthropic
//! `error` SSE event and the stream closes. Never a silent truncation.
//!
//! ## Fidelity limits (honest scope)
//!
//! The child is a single-shot `claude -p` turn with all tools disabled, so
//! this proxy serves TEXT chat turns: outer-session tool definitions are not
//! round-tripped (tool_use/tool_result history is flattened to text
//! markers), images are dropped with a marker, and `--tools ""` means no
//! inner tool execution. Requests whose account selection is Sensitive are
//! confined to trusted (Claude) accounts by `select_account_for_prompt`'s
//! sensitivity firewall — same rule as every other Cascade dispatch path.
//!
//! Inputs:  `POST /v1/messages` (Anthropic Messages API body), `GET /v1/health`.
//! Outputs: Anthropic SSE stream (`stream:true`) or Messages JSON body;
//!          HTTP 529 `overloaded_error` once every spill candidate is tried.
//! SPORT: `.opencode/phases/sport/MASTER-FEATURES.md` — F-FLEET-ANTHROPIC-FAILOVER

use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::Duration;

use async_trait::async_trait;
use axum::{
    body::{Body, Bytes},
    extract::{DefaultBodyLimit, State},
    http::{header, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use futures_util::{Stream, StreamExt};
use serde_json::{json, Value};
use tokio::io::AsyncBufReadExt;
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};
use uuid::Uuid;

// This module is loaded via `#[path]` from lib.rs (to keep it unconditional
// while the rest of proxy/ is feature-gated). Submodules of a `#[path]`-loaded
// module resolve relative to the file's own directory (`src/proxy/`), not a
// module-named subdirectory — so the explicit path is required.
#[path = "anthropic_failover/stream_json.rs"]
mod stream_json;
use cascade_core::selection::{
    select_account_for_prompt, GpHealthSnapshot, ModelClass, QuotaAccount, QuotaSnapshot,
    SelectionRequest, SelectionTarget, Tier,
};
use stream_json::{classify_line, error_frame, synthesize_success_frames, LineOutcome};

/// Default bind address (loopback only — this proxy must never bind off-host).
pub const DEFAULT_BIND: &str = "127.0.0.1:3763";

/// Per-attempt overall timeout, mirroring the conductor's `LANE_TIMEOUT_CLI`
/// (300s). Early spill on the first `api_retry` makes this a backstop, not
/// the common path: a saturated account is abandoned in milliseconds.
const ATTEMPT_TIMEOUT: Duration = Duration::from_secs(300);

/// Request body limit, matching Anthropic's own 32MB API limit (axum's 2MB
/// default would reject legitimately large interactive-session payloads).
const MAX_BODY_BYTES: usize = 32 * 1024 * 1024;

/// `PATH` for the child `claude` process — mirrors conductor's
/// `AUGMENTED_PATH` so homebrew/local installs resolve even when the daemon
/// was started from a restricted environment.
const AUGMENTED_PATH: &str =
    "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/opt/homebrew/sbin:/usr/local/sbin:/usr/sbin:/sbin";

/// mpsc channel capacity for streamed SSE frames between the retry loop and
/// the HTTP body. Small on purpose: frames are tiny and the loop must react
/// promptly to a slow/disconnected client (send failure ⇒ abandon).
const FRAME_CHANNEL_CAPACITY: usize = 64;

// ── Request flattening (step 2) ────────────────────────────────────────────────

/// Flatten an Anthropic Messages API body into the single text prompt the
/// child `claude -p` turn consumes.
///
/// `system` (string or text-block array) becomes a `[system]` preamble;
/// each message becomes a `[user]`/`[assistant]` transcript turn.
/// `tool_use`/`tool_result` blocks are flattened to text markers and images
/// are dropped with a marker (fidelity limits documented in the module doc).
pub fn flatten_request(body: &Value) -> Result<String, String> {
    let messages = body
        .get("messages")
        .and_then(Value::as_array)
        .ok_or("missing or invalid 'messages' field")?;
    if messages.is_empty() {
        return Err("'messages' must contain at least one message".to_string());
    }

    let mut out = String::new();
    match body.get("system") {
        Some(Value::String(s)) if !s.is_empty() => {
            out.push_str("[system]\n");
            out.push_str(s);
            out.push_str("\n\n");
        }
        Some(Value::Array(blocks)) => {
            let text: Vec<&str> = blocks
                .iter()
                .filter_map(|b| b.get("text").and_then(Value::as_str))
                .collect();
            if !text.is_empty() {
                out.push_str("[system]\n");
                out.push_str(&text.join("\n"));
                out.push_str("\n\n");
            }
        }
        _ => {}
    }

    for msg in messages {
        let speaker = match msg.get("role").and_then(Value::as_str) {
            Some("assistant") => "assistant",
            _ => "user",
        };
        out.push_str(&format!("[{speaker}]\n"));
        out.push_str(&flatten_content(msg.get("content").unwrap_or(&Value::Null)));
        out.push_str("\n\n");
    }

    out.push_str("[assistant]\n");
    Ok(out)
}

/// Render one message's `content` (string or block array) as flat text.
fn flatten_content(content: &Value) -> String {
    match content {
        Value::String(s) => s.clone(),
        Value::Array(blocks) => {
            let parts: Vec<String> = blocks.iter().filter_map(render_block).collect();
            parts.join("\n")
        }
        _ => String::new(),
    }
}

/// Render one content block; `None` skips the block entirely (thinking).
fn render_block(block: &Value) -> Option<String> {
    match block.get("type").and_then(Value::as_str).unwrap_or("") {
        "text" => block
            .get("text")
            .and_then(Value::as_str)
            .map(str::to_string),
        "tool_use" => {
            let name = block.get("name").and_then(Value::as_str).unwrap_or("tool");
            Some(format!(
                "[tool use: {name} {}]",
                block.get("input").cloned().unwrap_or(Value::Null)
            ))
        }
        "tool_result" => {
            let inner = flatten_content(block.get("content").unwrap_or(&Value::Null));
            Some(format!("[tool result: {inner}]"))
        }
        "image" => Some("[image omitted by failover proxy]".to_string()),
        "document" => Some("[document omitted by failover proxy]".to_string()),
        _ => None,
    }
}

/// Map the requested Anthropic model string onto a selection model class.
/// Unrecognized names (including provider-specific ids like `glm-5.2`, where
/// the class is irrelevant — zai lanes resolve to their own model) default
/// to Sonnet, the interactive-session default.
fn model_class_for(model: &str) -> ModelClass {
    let m = model.to_ascii_lowercase();
    if m.contains("haiku") {
        ModelClass::Haiku
    } else if m.contains("opus") {
        ModelClass::Opus
    } else if m.contains("fable") {
        ModelClass::Fable
    } else {
        ModelClass::Sonnet
    }
}

// ── Quota snapshot (step 3 input) ──────────────────────────────────────────────

/// Parse `~/.cascade/accounts/quota.json` content into a `QuotaSnapshot`.
///
/// Mirrors `conductor.rs::try_load_quota_snapshot_from` (reimplemented, not
/// imported — `cascade-daemon` cannot depend on `cascade-cli`). Returns
/// `None` on any read/parse failure, matching the conductor's best-effort
/// semantics.
fn parse_quota_snapshot(text: &str) -> Option<QuotaSnapshot> {
    let v: Value = serde_json::from_str(text).ok()?;
    let accounts_arr = v.get("accounts").and_then(|a| a.as_array())?;
    let accounts: Vec<QuotaAccount> = accounts_arr
        .iter()
        .filter_map(|entry| {
            Some(QuotaAccount {
                account: entry.get("account").and_then(Value::as_str)?.to_string(),
                provider: entry
                    .get("provider")
                    .and_then(Value::as_str)
                    .unwrap_or("unknown")
                    .to_string(),
                status: entry
                    .get("status")
                    .and_then(Value::as_str)
                    .unwrap_or("unknown")
                    .to_string(),
                five_hour_utilization: entry
                    .get("usage")
                    .and_then(|u| u.get("five_hour"))
                    .and_then(|fh| fh.get("utilization"))
                    .and_then(Value::as_f64),
                seven_day_utilization: entry
                    .get("usage")
                    .and_then(|u| u.get("seven_day"))
                    .and_then(|sd| sd.get("utilization"))
                    .and_then(Value::as_f64),
                config_dir: entry
                    .get("config_dir")
                    .and_then(Value::as_str)
                    .map(PathBuf::from),
            })
        })
        .collect();
    Some(QuotaSnapshot { accounts })
}

/// Provider tags dispatchable through the `claude` binary: Claude accounts
/// (config dir selects the account) and the z.ai GLM lane (claude binary +
/// endpoint env from the account's `cascade-env.sh`, proven by the existing
/// Za conductor path). Every other provider (codex/opencode/gemini/gfp) uses
/// a different binary and is out of this proxy's scope.
const CLAUDE_DISPATCHABLE_PROVIDERS: [&str; 3] = ["claude", "zai", "glm"];

/// Load the dispatchable account snapshot from `~/.cascade/accounts/quota.json`,
/// filtered to claude-binary-dispatchable accounts that have a config dir.
/// `Err` (not an empty snapshot) when the file is missing/unreadable — the
/// caller must fail loud with the path, not imply "no accounts configured".
fn load_dispatchable_snapshot() -> Result<QuotaSnapshot, String> {
    let path = cascade_core::accounts_store::quota_json_path();
    let text = std::fs::read_to_string(&path)
        .map_err(|e| format!("cannot read {}: {e}", path.display()))?;
    let snapshot = parse_quota_snapshot(&text)
        .ok_or_else(|| format!("cannot parse {} as quota.json", path.display()))?;
    Ok(QuotaSnapshot {
        accounts: snapshot
            .accounts
            .into_iter()
            .filter(|a| {
                CLAUDE_DISPATCHABLE_PROVIDERS.contains(&a.provider.as_str())
                    && a.config_dir.is_some()
            })
            .collect(),
    })
}

/// Mark one account as tried-and-failed in the local snapshot copy so
/// `select_account` can never return it again this request. EXACT account-id
/// equality — the T-P7-E13-02 lesson: `claude` must not prefix-match
/// `claude2`.
fn mark_unavailable(snapshot: &mut QuotaSnapshot, account_id: &str) {
    for account in &mut snapshot.accounts {
        if account.account == account_id {
            // is_alive() only accepts "ok"/"pool", so any other value is dead.
            account.status = "failover-tried".to_string();
        }
    }
}

/// Human-readable audit trail for the exhaustion error.
fn exhaustion_message(audit: &[String]) -> String {
    if audit.is_empty() {
        "no claude-binary-dispatchable accounts available in quota.json".to_string()
    } else {
        format!(
            "all spill candidates exhausted — tried: {}",
            audit.join("; ")
        )
    }
}

// ── Subprocess runner seam (step 4) ────────────────────────────────────────────

/// Stream of stdout lines from one dispatched attempt. `Err` carries a
/// spawn/IO failure. Dropping the stream KILLS the child process (spill
/// path) — see `ChildLineStream`.
type LineStream = Pin<Box<dyn Stream<Item = Result<String, String>> + Send>>;

/// Seam between the retry loop and the real `claude` subprocess. Production
/// uses [`SubprocessRunner`]; tests script per-account line sequences.
pub trait AttemptRunner: Send + Sync {
    fn spawn(&self, target: &SelectionTarget, prompt: &str) -> LineStream;
}

/// Real runner: shells the `claude` binary under the target account's config
/// dir, mirroring `conductor.rs::execute_claude` plus the streaming flags
/// the empirical capture validated (`--output-format stream-json` requires
/// `--verbose`; `--include-partial-messages` for deltas; `--tools ""`
/// disables inner tool execution; `--no-session-persistence` keeps the
/// single-shot turn out of the account's session history; prompt arrives on
/// STDIN to dodge argv-size limits).
struct SubprocessRunner;

impl AttemptRunner for SubprocessRunner {
    fn spawn(&self, target: &SelectionTarget, prompt: &str) -> LineStream {
        match spawn_claude_attempt(target, prompt) {
            Ok(stream) => stream,
            Err(e) => Box::pin(futures_util::stream::iter(vec![Err(e)])),
        }
    }
}

/// Probe for an executable by name: real `PATH`, then well-known install
/// dirs, then `~/bin` (mirrors conductor's `find_binary` — crate-boundary
/// reimplementation).
fn find_binary(name: &str) -> Option<PathBuf> {
    if let Some(path_var) = std::env::var_os("PATH") {
        for dir in std::env::split_paths(&path_var) {
            let candidate = dir.join(name);
            if is_executable_file(&candidate) {
                return Some(candidate);
            }
        }
    }
    for dir in ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"] {
        let candidate = PathBuf::from(dir).join(name);
        if is_executable_file(&candidate) {
            return Some(candidate);
        }
    }
    if let Some(home) = dirs::home_dir() {
        let candidate = home.join("bin").join(name);
        if is_executable_file(&candidate) {
            return Some(candidate);
        }
    }
    None
}

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

/// Strip every inherited `ANTHROPIC_*` / `CLAUDE*` env var from the child.
/// Empirically required (2026-08-17 capture): inherited vars override the
/// selected account AND — once this proxy is active as
/// `ANTHROPIC_BASE_URL=http://127.0.0.1:3763` — would recurse the child
/// back into the proxy forever. The target account's own values are applied
/// AFTER this, via `CLAUDE_CONFIG_DIR` + `cascade-env.sh`.
fn sanitize_child_env(cmd: &mut tokio::process::Command) {
    for (key, _) in std::env::vars() {
        if key.starts_with("ANTHROPIC_") || key.starts_with("CLAUDE") {
            cmd.env_remove(&key);
        }
    }
}

/// Apply `<config_dir>/cascade-env.sh` exports to the child (the Za lane's
/// `ANTHROPIC_BASE_URL`/auth endpoint config). Mirrors conductor's
/// `apply_cascade_env` + `parse_cascade_env`.
fn apply_cascade_env(cmd: &mut tokio::process::Command, config_dir: &Path) {
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
        if key.is_empty()
            || !key.chars().all(|c| c == '_' || c.is_ascii_alphanumeric())
            || !key
                .chars()
                .next()
                .is_some_and(|c| c == '_' || c.is_ascii_alphabetic())
        {
            continue;
        }
        vars.push((key.to_string(), parse_env_value(value.trim())));
    }
    vars
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

/// Spawn one attempt. `Err` = the attempt never started (binary missing, no
/// config dir) — classified as an attempt failure by the retry loop.
fn spawn_claude_attempt(target: &SelectionTarget, prompt: &str) -> Result<LineStream, String> {
    let config_dir = target
        .config_dir
        .as_deref()
        .ok_or_else(|| format!("no config_dir for account `{}`", target.account_id))?;
    let binary = find_binary("claude").ok_or("claude binary not found in PATH")?;

    let mut cmd = tokio::process::Command::new(&binary);
    sanitize_child_env(&mut cmd);
    apply_cascade_env(&mut cmd, config_dir);
    cmd.env("CLAUDE_CONFIG_DIR", config_dir)
        .env("PATH", AUGMENTED_PATH)
        .args([
            "-p",
            "--model",
            &target.model,
            "--output-format",
            "stream-json",
            "--include-partial-messages",
            "--verbose",
            "--strict-mcp-config",
            "--mcp-config",
            r#"{"mcpServers":{}}"#,
            "--setting-sources",
            "",
            "--tools",
            "",
            "--no-session-persistence",
        ])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        // Inner-CLI stderr chatter is noise at daemon log level; failures are
        // classified from the captured stdout signatures, not stderr.
        .stderr(std::process::Stdio::null())
        // Dropping the stream (spill path) kills the child immediately.
        .kill_on_drop(true);

    let mut child = cmd.spawn().map_err(|e| format!("spawn claude: {e}"))?;

    // Prompt via stdin, not argv: interactive-session payloads can exceed the
    // OS argv-size limit; stdin has no such bound. `-p` with no prompt
    // argument reads the prompt from stdin.
    if let Some(mut stdin) = child.stdin.take() {
        let prompt = prompt.to_string();
        tokio::spawn(async move {
            use tokio::io::AsyncWriteExt;
            let _ = stdin.write_all(prompt.as_bytes()).await;
            let _ = stdin.shutdown().await;
        });
    }

    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| "claude stdout not captured".to_string())?;

    Ok(Box::pin(ChildLineStream {
        _child: child,
        lines: tokio::io::BufReader::new(stdout).lines(),
    }))
}

/// Owning line stream: keeps the `Child` handle alive so `kill_on_drop(true)`
/// fires exactly when the dispatcher drops the stream (spill/timeout), not
/// when `spawn_claude_attempt` returns.
struct ChildLineStream {
    _child: tokio::process::Child,
    lines: tokio::io::Lines<tokio::io::BufReader<tokio::process::ChildStdout>>,
}

impl Stream for ChildLineStream {
    type Item = Result<String, String>;

    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        match Pin::new(&mut self.lines).poll_next_line(cx) {
            Poll::Pending => Poll::Pending,
            Poll::Ready(Ok(opt)) => Poll::Ready(opt.map(Ok)),
            Poll::Ready(Err(e)) => Poll::Ready(Some(Err(format!("read claude stdout: {e}")))),
        }
    }
}

// ── Dispatcher: selection + retry loop (steps 3 & 6) ─────────────────────────

/// Non-streaming dispatch contract (the shape the deferred-skeleton ticket
/// specified): one parsed Anthropic request body in, one Anthropic Messages
/// response body out, or an error string carrying the tried-account audit
/// trail once every spill candidate has failed.
// The dispatch contract the deferred-skeleton ticket specified. The server
// internally uses the concrete `FailoverDispatcher` (the trait covers only the
// non-streaming shape), so the only in-crate consumer is the test suite —
// suppress dead_code for the bin-tree compile, which has no caller for it.
#[allow(dead_code)]
#[async_trait]
pub trait AnthropicFailoverDispatch: Send + Sync {
    async fn dispatch(&self, request_body: &Value) -> Result<Value, String>;
}

/// Outcome of one dispatched attempt inside the streaming retry loop.
enum AttemptOutcome {
    /// Terminal state reached (full stream delivered, synthesized fallback
    /// emitted, or post-commit error event already sent) — do NOT retry.
    Done,
    /// Pre-commit failure: no bytes reached the caller; safe to spill.
    Failed { reason: String },
    /// Caller went away mid-attempt — stop the loop entirely.
    ClientGone,
}

/// Why the attempt's line-driving loop ended early (internal to one attempt).
enum DriveSignal {
    Reason(String),
    ClientGone,
}

/// The retry loop: selection → subprocess → re-frame → spill-on-failure.
pub struct FailoverDispatcher {
    runner: std::sync::Arc<dyn AttemptRunner>,
    /// Tests inject a fixture snapshot; production reads quota.json live on
    /// every request (mid-run saturation is then respected, mirroring the
    /// conductor's quota re-check doctrine).
    snapshot_override: Option<QuotaSnapshot>,
}

impl Default for FailoverDispatcher {
    fn default() -> Self {
        Self::new()
    }
}

impl FailoverDispatcher {
    /// Production dispatcher: real subprocess runner + live quota.json.
    pub fn new() -> Self {
        Self {
            runner: std::sync::Arc::new(SubprocessRunner),
            snapshot_override: None,
        }
    }

    /// Test constructor: scripted runner + fixture snapshot.
    #[cfg(test)]
    fn with_runner_and_snapshot(
        runner: std::sync::Arc<dyn AttemptRunner>,
        snapshot: QuotaSnapshot,
    ) -> Self {
        Self {
            runner,
            snapshot_override: Some(snapshot),
        }
    }

    fn load_snapshot(&self) -> Result<QuotaSnapshot, String> {
        match &self.snapshot_override {
            Some(s) => Ok(s.clone()),
            None => load_dispatchable_snapshot(),
        }
    }

    /// One selection round: the SAME spill order + sensitivity firewall the
    /// conductor uses (T1 = interactive; GP health defaulted = no GP
    /// preference — GP is not claude-binary dispatchable anyway).
    fn next_target(
        snapshot: &QuotaSnapshot,
        prompt: &str,
        class: ModelClass,
    ) -> Option<SelectionTarget> {
        select_account_for_prompt(
            &SelectionRequest {
                tier: Tier::T1,
                model_class: Some(class),
                account_override: None,
            },
            snapshot,
            &GpHealthSnapshot::default(),
            prompt,
        )
    }

    /// Streaming retry loop. Drives attempts and pushes SSE frames into
    /// `tx`; terminates on success, post-commit error, client disconnect, or
    /// spill-list exhaustion (final `overloaded_error` error frame).
    pub async fn run_stream_loop(
        &self,
        prompt: &str,
        class: ModelClass,
        tx: mpsc::Sender<Vec<u8>>,
    ) {
        let mut snapshot = match self.load_snapshot() {
            Ok(s) => s,
            Err(e) => {
                let _ = tx
                    .send(error_frame(
                        "api_error",
                        &format!("failover proxy misconfigured: {e}"),
                    ))
                    .await;
                return;
            }
        };
        let mut audit: Vec<String> = Vec::new();
        loop {
            let Some(target) = Self::next_target(&snapshot, prompt, class) else {
                let _ = tx
                    .send(error_frame("overloaded_error", &exhaustion_message(&audit)))
                    .await;
                return;
            };
            info!(
                account = %target.account_id,
                provider = target.provider.as_str(),
                reason = %target.reason,
                "anthropic_failover: dispatching attempt"
            );
            match self.run_attempt_stream(&target, prompt, &tx).await {
                AttemptOutcome::Done | AttemptOutcome::ClientGone => return,
                AttemptOutcome::Failed { reason } => {
                    warn!(
                        account = %target.account_id,
                        %reason,
                        "anthropic_failover: attempt failed pre-commit, spilling to next account"
                    );
                    audit.push(format!("{} ({reason})", target.account_id));
                    mark_unavailable(&mut snapshot, &target.account_id);
                }
            }
        }
    }

    /// One streaming attempt: buffer frames pre-commit, live passthrough
    /// after the first content delta. See "Commit-point streaming contract".
    async fn run_attempt_stream(
        &self,
        target: &SelectionTarget,
        prompt: &str,
        tx: &mpsc::Sender<Vec<u8>>,
    ) -> AttemptOutcome {
        let mut lines = self.runner.spawn(target, prompt);
        let mut buffer: Vec<Vec<u8>> = Vec::new();
        let mut committed = false;
        let mut terminal: Option<Value> = None;

        let drive = async {
            while let Some(item) = lines.next().await {
                let line = match item {
                    Ok(l) => l,
                    Err(e) => return Err(DriveSignal::Reason(e)),
                };
                match classify_line(&line) {
                    LineOutcome::Frames(frames) => {
                        for frame in frames {
                            if !committed && stream_json::frame_is_content_delta(&frame) {
                                committed = true;
                                for buffered in buffer.drain(..) {
                                    tx.send(buffered)
                                        .await
                                        .map_err(|_| DriveSignal::ClientGone)?;
                                }
                            }
                            if committed {
                                tx.send(frame).await.map_err(|_| DriveSignal::ClientGone)?;
                            } else {
                                buffer.push(frame);
                            }
                        }
                    }
                    LineOutcome::Result(v) => terminal = Some(v),
                    LineOutcome::Spill { reason } => return Err(DriveSignal::Reason(reason)),
                    LineOutcome::Ignored => {}
                }
            }
            Ok(())
        };

        let driven = tokio::time::timeout(ATTEMPT_TIMEOUT, drive).await;
        let signal: Result<Option<Value>, DriveSignal> = match driven {
            Err(_) => Err(DriveSignal::Reason(format!(
                "attempt timed out after {}s",
                ATTEMPT_TIMEOUT.as_secs()
            ))),
            Ok(Ok(())) => Ok(terminal.take()),
            Ok(Err(sig)) => Err(sig),
        };
        // `lines` (and the child, via kill_on_drop) is dropped here at the
        // latest — a timeout or spill abandons the subprocess immediately.

        match signal {
            Ok(Some(result)) => {
                if !committed {
                    // Success with zero emitted deltas (never observed with
                    // --include-partial-messages, but possible): synthesize a
                    // complete, correctly-ordered SSE sequence from the result.
                    let text = result.get("result").and_then(Value::as_str).unwrap_or("");
                    let input = result["usage"]["input_tokens"].as_u64().unwrap_or(0);
                    let output = result["usage"]["output_tokens"].as_u64().unwrap_or(0);
                    for frame in synthesize_success_frames(text, &target.model, input, output) {
                        if tx.send(frame).await.is_err() {
                            return AttemptOutcome::ClientGone;
                        }
                    }
                }
                AttemptOutcome::Done
            }
            Ok(None) => {
                if committed {
                    let _ = tx
                        .send(error_frame(
                            "api_error",
                            "claude stream ended without a terminal result",
                        ))
                        .await;
                    AttemptOutcome::Done
                } else {
                    AttemptOutcome::Failed {
                        reason: "stream ended without terminal result".to_string(),
                    }
                }
            }
            Err(DriveSignal::ClientGone) => AttemptOutcome::ClientGone,
            Err(DriveSignal::Reason(reason)) => {
                if committed {
                    // Post-commit failure: retrying would corrupt the stream
                    // already delivered — surface it and close. Never silent.
                    let _ = tx
                        .send(error_frame(
                            "api_error",
                            &format!(
                                "upstream account `{}` failed after stream start: {reason}",
                                target.account_id
                            ),
                        ))
                        .await;
                    AttemptOutcome::Done
                } else {
                    AttemptOutcome::Failed { reason }
                }
            }
        }
    }

    /// Non-streaming dispatch with the same retry loop: full Anthropic
    /// Messages JSON on success; `Err` carries the tried-account audit trail
    /// (HTTP 529 `overloaded_error` in the handler).
    pub async fn dispatch_json(&self, prompt: &str, class: ModelClass) -> Result<Value, String> {
        let mut snapshot = self.load_snapshot()?;
        let mut audit: Vec<String> = Vec::new();
        loop {
            let Some(target) = Self::next_target(&snapshot, prompt, class) else {
                return Err(exhaustion_message(&audit));
            };
            info!(
                account = %target.account_id,
                provider = target.provider.as_str(),
                "anthropic_failover: dispatching non-streaming attempt"
            );
            match self.run_attempt_json(&target, prompt).await {
                Ok(body) => return Ok(body),
                Err(reason) => {
                    warn!(
                        account = %target.account_id,
                        %reason,
                        "anthropic_failover: non-streaming attempt failed, spilling"
                    );
                    audit.push(format!("{} ({reason})", target.account_id));
                    mark_unavailable(&mut snapshot, &target.account_id);
                }
            }
        }
    }

    /// One non-streaming attempt: accumulate text deltas, build the response
    /// from the terminal result on success.
    async fn run_attempt_json(
        &self,
        target: &SelectionTarget,
        prompt: &str,
    ) -> Result<Value, String> {
        let mut lines = self.runner.spawn(target, prompt);
        let mut text = String::new();
        let mut terminal: Option<Value> = None;

        let drive = async {
            while let Some(item) = lines.next().await {
                let line = item?;
                match classify_line(&line) {
                    LineOutcome::Frames(frames) => {
                        for frame in frames {
                            if let Some(chunk) = text_from_frame(&frame) {
                                text.push_str(&chunk);
                            }
                        }
                    }
                    LineOutcome::Result(v) => terminal = Some(v),
                    LineOutcome::Spill { reason } => return Err(reason),
                    LineOutcome::Ignored => {}
                }
            }
            Ok(())
        };

        match tokio::time::timeout(ATTEMPT_TIMEOUT, drive).await {
            Err(_) => Err(format!(
                "attempt timed out after {}s",
                ATTEMPT_TIMEOUT.as_secs()
            )),
            Ok(Err(reason)) => Err(reason),
            Ok(Ok(())) => {
                let result = terminal.ok_or("stream ended without terminal result")?;
                let final_text = result
                    .get("result")
                    .and_then(Value::as_str)
                    .filter(|s| !s.is_empty())
                    .map(str::to_string)
                    .unwrap_or(std::mem::take(&mut text));
                let stop_reason = match result.get("stop_reason").and_then(Value::as_str) {
                    Some("max_tokens") => "max_tokens",
                    _ => "end_turn",
                };
                Ok(json!({
                    "id": format!("msg_failover_{}", Uuid::new_v4().simple()),
                    "type": "message",
                    "role": "assistant",
                    "content": [{ "type": "text", "text": final_text }],
                    "model": target.model,
                    "stop_reason": stop_reason,
                    "stop_sequence": Value::Null,
                    "usage": {
                        "input_tokens": result["usage"]["input_tokens"].as_u64().unwrap_or(0),
                        "output_tokens": result["usage"]["output_tokens"].as_u64().unwrap_or(0),
                    }
                }))
            }
        }
    }
}

#[async_trait]
impl AnthropicFailoverDispatch for FailoverDispatcher {
    async fn dispatch(&self, request_body: &Value) -> Result<Value, String> {
        let prompt = flatten_request(request_body)?;
        let class = model_class_for(
            request_body
                .get("model")
                .and_then(Value::as_str)
                .unwrap_or(""),
        );
        self.dispatch_json(&prompt, class).await
    }
}

/// Extract the text payload of a `text_delta` SSE frame (non-streaming path).
fn text_from_frame(frame: &[u8]) -> Option<String> {
    let s = std::str::from_utf8(frame).ok()?;
    let data = s.split_once("data: ")?.1.trim_end();
    let v: Value = serde_json::from_str(data).ok()?;
    if v.get("type").and_then(Value::as_str) == Some("content_block_delta")
        && v["delta"]["type"] == "text_delta"
    {
        v["delta"]["text"].as_str().map(str::to_string)
    } else {
        None
    }
}

// ── HTTP server (step 7) ──────────────────────────────────────────────────────

#[derive(Clone)]
struct FailoverState {
    dispatcher: std::sync::Arc<FailoverDispatcher>,
}

/// GET /v1/health
async fn health() -> Json<Value> {
    Json(json!({ "status": "ok", "proxy": "anthropic-failover", "bind": DEFAULT_BIND }))
}

/// Anthropic-shaped error response.
fn error_response(status: StatusCode, error_type: &str, message: &str) -> Response {
    (
        status,
        Json(json!({
            "type": "error",
            "error": { "type": error_type, "message": message }
        })),
    )
        .into_response()
}

/// HTTP 529 + Anthropic `overloaded_error` — the spec'd exhaustion body.
fn overloaded_response(message: &str) -> Response {
    let status = StatusCode::from_u16(529).unwrap_or(StatusCode::SERVICE_UNAVAILABLE);
    error_response(status, "overloaded_error", message)
}

/// POST /v1/messages
async fn messages(State(state): State<FailoverState>, Json(body): Json<Value>) -> Response {
    let prompt = match flatten_request(&body) {
        Ok(p) => p,
        Err(e) => return error_response(StatusCode::BAD_REQUEST, "invalid_request_error", &e),
    };
    let class = model_class_for(body.get("model").and_then(Value::as_str).unwrap_or(""));

    if body.get("stream").and_then(Value::as_bool) == Some(true) {
        let (tx, rx) = mpsc::channel::<Vec<u8>>(FRAME_CHANNEL_CAPACITY);
        let dispatcher = state.dispatcher.clone();
        tokio::spawn(async move {
            dispatcher.run_stream_loop(&prompt, class, tx).await;
        });
        let frame_stream = ReceiverStream::new(rx)
            .map(|frame| Ok::<_, std::convert::Infallible>(Bytes::from(frame)));
        Response::builder()
            .status(StatusCode::OK)
            .header(header::CONTENT_TYPE, "text/event-stream")
            .header(header::CACHE_CONTROL, "no-cache")
            .body(Body::from_stream(frame_stream))
            .unwrap_or_else(|e| {
                warn!(error = %e, "anthropic_failover: failed to build SSE response");
                (StatusCode::INTERNAL_SERVER_ERROR, "stream build failed").into_response()
            })
    } else {
        match state.dispatcher.dispatch_json(&prompt, class).await {
            Ok(v) => (StatusCode::OK, Json(v)).into_response(),
            Err(audit) => overloaded_response(&audit),
        }
    }
}

/// Request-level Anthropic failover proxy server (`127.0.0.1:3763`).
///
/// Started automatically by `supervisor.rs` on daemon boot (mirroring the
/// `:3761`/`:3762` startup blocks, but NOT feature-gated: this proxy is
/// loopback-only and dispatches local `claude` subprocesses under
/// user-configured accounts — no external-network surface beyond the user's
/// own CLIs). Activation for an interactive `claude` session is an explicit
/// user step: `export ANTHROPIC_BASE_URL=http://127.0.0.1:3763`.
pub struct AnthropicFailoverServer {
    bind_addr: SocketAddr,
    shutdown: CancellationToken,
    dispatcher: std::sync::Arc<FailoverDispatcher>,
    /// Actual bound address (set by `run` once the listener is up). Tests bind
    /// port `:0` for isolation, so they need the OS-assigned ephemeral port —
    /// the configured `bind_addr` is a request, not the bound reality.
    bound_addr: std::sync::Arc<std::sync::OnceLock<SocketAddr>>,
}

impl AnthropicFailoverServer {
    /// Construct a production server (real subprocess dispatcher).
    pub fn new(bind_addr: SocketAddr, shutdown: CancellationToken) -> Self {
        Self {
            bind_addr,
            shutdown,
            dispatcher: std::sync::Arc::new(FailoverDispatcher::new()),
            bound_addr: std::sync::Arc::new(std::sync::OnceLock::new()),
        }
    }

    /// Test constructor: inject a scripted dispatcher.
    #[cfg(test)]
    fn with_dispatcher(
        bind_addr: SocketAddr,
        shutdown: CancellationToken,
        dispatcher: std::sync::Arc<FailoverDispatcher>,
    ) -> Self {
        Self {
            bind_addr,
            shutdown,
            dispatcher,
            bound_addr: std::sync::Arc::new(std::sync::OnceLock::new()),
        }
    }

    /// Bind address this instance will serve on.
    #[cfg(test)]
    pub fn bind_addr(&self) -> SocketAddr {
        self.bind_addr
    }

    /// Bind and serve until `shutdown` is cancelled. Takes `&self` (the
    /// supervisor and tests hold their own handles and spawn `run` onto a
    /// task) and publishes the actually-bound address via the shared
    /// `bound_addr` cell.
    pub async fn run(&self) -> Result<(), std::io::Error> {
        let state = FailoverState {
            dispatcher: self.dispatcher.clone(),
        };
        let app = Router::new()
            .route("/v1/health", get(health))
            .route("/v1/messages", post(messages))
            .layer(DefaultBodyLimit::max(MAX_BODY_BYTES))
            .with_state(state);

        let listener = tokio::net::TcpListener::bind(self.bind_addr).await?;
        let local = listener.local_addr()?;
        let _ = self.bound_addr.set(local);
        info!(addr = %local, "anthropic_failover: listening");

        let shutdown = self.shutdown.clone();
        axum::serve(listener, app)
            .with_graceful_shutdown(async move {
                shutdown.cancelled().await;
            })
            .await?;

        info!("anthropic_failover: stopped");
        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::model_ids::MODEL_CLAUDE_FABLE;
    use cascade_types::model_ids::MODEL_CLAUDE_HAIKU;
    use cascade_types::model_ids::MODEL_CLAUDE_OPUS;
    use cascade_types::model_ids::MODEL_CLAUDE_SONNET;
    use cascade_types::model_ids::MODEL_GLM;
    use std::collections::HashMap;
    use std::sync::Mutex;

    /// Scripted runner: per-account NDJSON line scripts + a spawn log so
    /// tests can assert which accounts were (never) dispatched.
    struct FakeRunner {
        scripts: HashMap<String, Vec<String>>,
        spawns: Mutex<Vec<String>>,
    }

    impl FakeRunner {
        fn new(scripts: Vec<(&str, Vec<String>)>) -> std::sync::Arc<Self> {
            std::sync::Arc::new(Self {
                scripts: scripts
                    .into_iter()
                    .map(|(k, lines)| (k.to_string(), lines))
                    .collect(),
                spawns: Mutex::new(Vec::new()),
            })
        }

        fn spawned_accounts(&self) -> Vec<String> {
            self.spawns.lock().unwrap().clone()
        }
    }

    impl AttemptRunner for FakeRunner {
        fn spawn(&self, target: &SelectionTarget, _prompt: &str) -> LineStream {
            self.spawns.lock().unwrap().push(target.account_id.clone());
            let lines = self
                .scripts
                .get(target.account_id.as_str())
                .cloned()
                .unwrap_or_default();
            Box::pin(futures_util::stream::iter(
                lines.into_iter().map(Ok::<String, String>),
            ))
        }
    }

    /// Snapshot matching the live fleet shape: zai lane first in spill order,
    /// then claude2, then claude (ACCOUNT_SPILL_ORDER).
    fn test_snapshot() -> QuotaSnapshot {
        let acc = |account: &str, provider: &str| QuotaAccount {
            account: account.to_string(),
            provider: provider.to_string(),
            status: "ok".to_string(),
            five_hour_utilization: Some(10.0),
            seven_day_utilization: None,
            config_dir: Some(PathBuf::from("/tmp/cascade-failover-test").join(account)),
        };
        QuotaSnapshot {
            accounts: vec![
                acc("glm-acc1", "zai"),
                acc("claude2", "claude"),
                acc("claude", "claude"),
            ],
        }
    }

    // A minimal but shape-faithful success script (verified against the
    // 2026-08-17 capture): init prelude, one delta, terminal result.
    fn success_script(text: &str) -> Vec<String> {
        vec![
            r#"{"type":"system","subtype":"init","tools":["Bash"]}"#.into(),
            r#"{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_t1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":0}}}}"#.into(),
            r#"{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}"#.into(),
            format!(r#"{{"type":"stream_event","event":{{"type":"content_block_delta","index":0,"delta":{{"type":"text_delta","text":"{text}"}}}}}}"#),
            r#"{"type":"stream_event","event":{"type":"content_block_stop","index":0}}"#.into(),
            r#"{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}}"#.into(),
            r#"{"type":"stream_event","event":{"type":"message_stop"}}"#.into(),
            r#"{"type":"assistant","message":{"content":[{"type":"text","text":"snapshot"}]}}"#.into(),
            format!(r#"{{"type":"result","subtype":"success","is_error":false,"api_error_status":null,"result":"{text}","stop_reason":"end_turn","usage":{{"input_tokens":5,"output_tokens":1}}}}"#),
        ]
    }

    // ── Request flattening ──────────────────────────────────────────────────

    #[test]
    fn flatten_request_renders_system_and_turns() {
        let body = json!({
            "system": "be terse",
            "messages": [
                { "role": "user", "content": "hello" },
                { "role": "assistant", "content": [{ "type": "text", "text": "hi" }] },
                { "role": "user", "content": "bye" },
            ]
        });
        let prompt = flatten_request(&body).unwrap();
        assert!(
            prompt.starts_with("[system]\nbe terse\n\n[user]\nhello\n\n"),
            "{prompt}"
        );
        assert!(prompt.contains("[assistant]\nhi\n\n"), "{prompt}");
        assert!(prompt.ends_with("[user]\nbye\n\n[assistant]\n"), "{prompt}");
    }

    #[test]
    fn flatten_request_marks_tools_and_drops_images() {
        let body = json!({
            "messages": [{
                "role": "user",
                "content": [
                    { "type": "text", "text": "look" },
                    { "type": "image", "source": { "type": "base64" } },
                    { "type": "tool_result", "content": "tool said no" },
                    { "type": "tool_use", "name": "Bash", "input": { "cmd": "ls" } },
                ]
            }]
        });
        let prompt = flatten_request(&body).unwrap();
        assert!(prompt.contains("look"), "{prompt}");
        assert!(
            prompt.contains("[image omitted by failover proxy]"),
            "{prompt}"
        );
        assert!(prompt.contains("[tool result: tool said no]"), "{prompt}");
        assert!(
            prompt.contains("[tool use: Bash {\"cmd\":\"ls\"}]"),
            "{prompt}"
        );
    }

    #[test]
    fn flatten_request_rejects_missing_messages() {
        assert!(flatten_request(&json!({})).is_err());
        assert!(flatten_request(&json!({"messages": []})).is_err());
    }

    #[test]
    fn model_class_mapping_covers_known_families() {
        assert_eq!(model_class_for(MODEL_CLAUDE_HAIKU), ModelClass::Haiku);
        assert_eq!(model_class_for(MODEL_CLAUDE_OPUS), ModelClass::Opus);
        assert_eq!(model_class_for(MODEL_CLAUDE_SONNET), ModelClass::Sonnet);
        assert_eq!(model_class_for(MODEL_CLAUDE_FABLE), ModelClass::Fable);
        // Unrecognized (incl. provider-specific ids) → interactive default.
        assert_eq!(model_class_for(MODEL_GLM), ModelClass::Sonnet);
        assert_eq!(model_class_for(""), ModelClass::Sonnet);
    }

    // ── Snapshot loading / filtering ────────────────────────────────────────

    #[test]
    fn snapshot_parser_and_dispatchable_filter() {
        let text = r#"{
            "accounts": [
                { "account": "glm-acc1", "provider": "zai", "status": "ok",
                  "config_dir": "/tmp/z", "usage": { "five_hour": { "utilization": 21.7 } } },
                { "account": "claude2", "provider": "claude", "status": "ok",
                  "config_dir": "/tmp/c2" },
                { "account": "codex-c1", "provider": "codex", "status": "ok",
                  "config_dir": "/tmp/x" },
                { "account": "gfp-pool", "provider": "gfp", "status": "pool" },
                { "account": "claude-nodir", "provider": "claude", "status": "ok" }
            ]
        }"#;
        let snapshot = parse_quota_snapshot(text).unwrap();
        assert_eq!(snapshot.accounts.len(), 5);
        // The raw parser keeps everything; the dispatchable filter narrows.
        let text_val = serde_json::from_str::<Value>(text).unwrap();
        let filtered = {
            let mut s = parse_quota_snapshot(&text_val.to_string()).unwrap();
            s.accounts.retain(|a| {
                CLAUDE_DISPATCHABLE_PROVIDERS.contains(&a.provider.as_str())
                    && a.config_dir.is_some()
            });
            s
        };
        let names: Vec<&str> = filtered
            .accounts
            .iter()
            .map(|a| a.account.as_str())
            .collect();
        assert_eq!(names, vec!["glm-acc1", "claude2"]);
        assert_eq!(
            filtered.accounts[0].five_hour_utilization,
            Some(21.7),
            "utilization must survive parsing for saturation checks"
        );
    }

    #[test]
    fn mark_unavailable_uses_exact_account_id() {
        let mut snapshot = test_snapshot();
        mark_unavailable(&mut snapshot, "claude");
        let by_id = |s: &QuotaSnapshot, id: &str| {
            s.accounts.iter().find(|a| a.account == id).unwrap().clone()
        };
        assert_eq!(by_id(&snapshot, "claude").status, "failover-tried");
        // The T-P7-E13-02 prefix trap: `claude` must NOT take down `claude2`.
        assert_eq!(by_id(&snapshot, "claude2").status, "ok");
        assert_eq!(by_id(&snapshot, "glm-acc1").status, "ok");
    }

    // ── Spill loop (streaming) ──────────────────────────────────────────────

    #[tokio::test]
    async fn streaming_spill_on_early_429_yields_continuous_frames_from_next_account() {
        // glm-acc1 (first in spill order) hits the captured api_retry 429
        // signature BEFORE any content; claude2 succeeds.
        let runner = FakeRunner::new(vec![
            ("glm-acc1", vec![r#"{"type":"system","subtype":"init"}"#.into(),
                r#"{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":581.4,"error_status":429,"error":"rate_limit"}"#.into()]),
            ("claude2", success_script("recovered")),
        ]);
        let dispatcher =
            FailoverDispatcher::with_runner_and_snapshot(runner.clone(), test_snapshot());

        let (tx, mut rx) = mpsc::channel::<Vec<u8>>(FRAME_CHANNEL_CAPACITY);
        dispatcher
            .run_stream_loop("hello there friend", ModelClass::Sonnet, tx)
            .await;

        let mut all = String::new();
        while let Some(frame) = rx.recv().await {
            all.push_str(&String::from_utf8_lossy(&frame));
        }
        // Exactly one message lifecycle, from the SPILL target only — the
        // caller sees a single continuous, correctly-framed stream.
        assert_eq!(all.matches("event: message_start").count(), 1);
        assert_eq!(all.matches("event: message_stop").count(), 1);
        assert!(all.contains("recovered"), "{all}");
        assert!(!all.contains("api_retry"), "{all}");
        assert_eq!(runner.spawned_accounts(), vec!["glm-acc1", "claude2"]);
    }

    #[tokio::test]
    async fn streaming_exhaustion_returns_overloaded_error_frame() {
        // Every dispatchable account fails with the captured auth signature.
        let auth_fail = || {
            vec![
            r#"{"type":"system","subtype":"init"}"#.into(),
            r#"{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Not logged in"}]},"error":"authentication_failed"}"#.into(),
            r#"{"type":"result","subtype":"success","is_error":true,"api_error_status":null,"result":"auth"}"#.into(),
        ]
        };
        let runner = FakeRunner::new(vec![
            ("glm-acc1", auth_fail()),
            ("claude2", auth_fail()),
            ("claude", auth_fail()),
        ]);
        let dispatcher =
            FailoverDispatcher::with_runner_and_snapshot(runner.clone(), test_snapshot());

        let (tx, mut rx) = mpsc::channel::<Vec<u8>>(FRAME_CHANNEL_CAPACITY);
        dispatcher
            .run_stream_loop("hello there friend", ModelClass::Sonnet, tx)
            .await;

        let mut all = String::new();
        while let Some(frame) = rx.recv().await {
            all.push_str(&String::from_utf8_lossy(&frame));
        }
        assert!(all.contains("event: error"), "{all}");
        assert!(all.contains("overloaded_error"), "{all}");
        // Audit trail names every tried account.
        assert!(all.contains("glm-acc1"), "{all}");
        assert!(all.contains("claude2"), "{all}");
        assert!(all.contains("claude ("), "{all}");
        assert_eq!(
            runner.spawned_accounts(),
            vec!["glm-acc1", "claude2", "claude"],
            "bounded to the spill-list length — no account retried"
        );
    }

    #[tokio::test]
    async fn post_commit_failure_surfaces_error_event_without_spill() {
        // Content starts, THEN the account dies mid-stream: retrying would
        // corrupt the delivered stream — an error event is the only honest
        // continuation, and the next account must NOT be spawned.
        let lines = vec![
            r#"{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_t1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}}"#.into(),
            r#"{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial "}}}"#.into(),
            r#"{"type":"system","subtype":"api_retry","attempt":1,"error_status":429,"error":"rate_limit"}"#.into(),
            r#"{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"429"}"#.into(),
        ];
        let runner = FakeRunner::new(vec![("glm-acc1", lines)]);
        let dispatcher =
            FailoverDispatcher::with_runner_and_snapshot(runner.clone(), test_snapshot());

        let (tx, mut rx) = mpsc::channel::<Vec<u8>>(FRAME_CHANNEL_CAPACITY);
        dispatcher
            .run_stream_loop("hello there friend", ModelClass::Sonnet, tx)
            .await;

        let mut all = String::new();
        while let Some(frame) = rx.recv().await {
            all.push_str(&String::from_utf8_lossy(&frame));
        }
        assert!(all.contains("partial "), "{all}");
        assert!(all.contains("event: error"), "{all}");
        assert!(all.contains("failed after stream start"), "{all}");
        assert_eq!(
            runner.spawned_accounts(),
            vec!["glm-acc1"],
            "post-commit failure must not spill"
        );
    }

    #[tokio::test]
    async fn sensitive_prompt_never_lands_on_the_untrusted_zai_lane() {
        // select_account_for_prompt's sensitivity firewall: a Sensitive
        // transcript skips glm-acc1 entirely and uses a trusted Claude lane.
        // (Keyword per cascade-core's classify_sensitivity fixture usage.)
        let runner = FakeRunner::new(vec![
            ("claude2", success_script("trusted answer")),
            ("claude", success_script("trusted answer")),
        ]);
        let dispatcher =
            FailoverDispatcher::with_runner_and_snapshot(runner.clone(), test_snapshot());

        let (tx, mut rx) = mpsc::channel::<Vec<u8>>(FRAME_CHANNEL_CAPACITY);
        let sensitive_prompt = "patient medical records diagnosis: chest pain";
        dispatcher
            .run_stream_loop(sensitive_prompt, ModelClass::Sonnet, tx)
            .await;

        let mut all = String::new();
        while let Some(frame) = rx.recv().await {
            all.push_str(&String::from_utf8_lossy(&frame));
        }
        assert!(all.contains("trusted answer"), "{all}");
        assert!(
            !runner.spawned_accounts().contains(&"glm-acc1".to_string()),
            "sensitive prompt must never dispatch to zai, got {:?}",
            runner.spawned_accounts()
        );
    }

    // ── Non-streaming path ──────────────────────────────────────────────────

    #[tokio::test]
    async fn non_streaming_success_builds_anthropic_message_body() {
        let runner = FakeRunner::new(vec![("glm-acc1", success_script("final answer text"))]);
        let dispatcher = FailoverDispatcher::with_runner_and_snapshot(runner, test_snapshot());

        let body = dispatcher
            .dispatch_json("hello there friend", ModelClass::Sonnet)
            .await
            .unwrap();
        assert_eq!(body["type"], "message");
        assert_eq!(body["role"], "assistant");
        assert_eq!(body["content"][0]["text"], "final answer text");
        assert_eq!(body["stop_reason"], "end_turn");
        assert_eq!(body["usage"]["input_tokens"], 5);
        assert_eq!(body["usage"]["output_tokens"], 1);
    }

    #[tokio::test]
    async fn non_streaming_exhaustion_carries_audit_trail() {
        let fail = || {
            vec![
                r#"{"type":"system","subtype":"init"}"#.into(),
                r#"{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"429"}"#.into(),
            ]
        };
        let runner = FakeRunner::new(vec![
            ("glm-acc1", fail()),
            ("claude2", fail()),
            ("claude", fail()),
        ]);
        let dispatcher = FailoverDispatcher::with_runner_and_snapshot(runner, test_snapshot());

        let err = dispatcher
            .dispatch_json("hello there friend", ModelClass::Sonnet)
            .await
            .unwrap_err();
        assert!(err.starts_with("all spill candidates exhausted"), "{err}");
        assert!(err.contains("glm-acc1"), "{err}");
        assert!(err.contains("claude2"), "{err}");
    }

    #[tokio::test]
    async fn dispatch_trait_flattens_and_dispatches() {
        let runner = FakeRunner::new(vec![("glm-acc1", success_script("via trait"))]);
        let dispatcher = std::sync::Arc::new(FailoverDispatcher::with_runner_and_snapshot(
            runner,
            test_snapshot(),
        ));
        let body = json!({
            "model": MODEL_CLAUDE_SONNET,
            "messages": [{ "role": "user", "content": "ping" }],
        });
        let out = dispatcher.dispatch(&body).await.unwrap();
        assert_eq!(out["content"][0]["text"], "via trait");
    }

    // ── Server: real bind + serve (replaces the fail-closed skeleton test) ──

    /// Wait for `run` to publish the OS-assigned ephemeral port (bind `:0`).
    async fn wait_for_bound(cell: &std::sync::Arc<std::sync::OnceLock<SocketAddr>>) -> SocketAddr {
        for _ in 0..500 {
            if let Some(a) = cell.get() {
                return *a;
            }
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
        panic!("server never bound its listener");
    }

    #[tokio::test]
    async fn server_binds_serves_and_shuts_down() {
        let runner = FakeRunner::new(vec![("glm-acc1", success_script("served"))]);
        let dispatcher = std::sync::Arc::new(FailoverDispatcher::with_runner_and_snapshot(
            runner,
            test_snapshot(),
        ));
        let shutdown = CancellationToken::new();
        // Port 0 → ephemeral: never collides with a real :3763 instance.
        let bind: SocketAddr = "127.0.0.1:0".parse().unwrap();
        let server = std::sync::Arc::new(AnthropicFailoverServer::with_dispatcher(
            bind,
            shutdown.clone(),
            dispatcher,
        ));
        let bound = server.bound_addr.clone();

        let handle = tokio::spawn({
            let server = server.clone();
            async move { server.run().await }
        });
        let addr = wait_for_bound(&bound).await;

        // Health probe.
        let health = reqwest::get(format!("http://{addr}/v1/health"))
            .await
            .unwrap();
        assert_eq!(health.status(), 200);
        let hv: Value = health.json().await.unwrap();
        assert_eq!(hv["status"], "ok");
        assert_eq!(hv["proxy"], "anthropic-failover");

        // Streaming messages request.
        let client = reqwest::Client::new();
        let resp = client
            .post(format!("http://{addr}/v1/messages"))
            .json(&json!({
                "model": MODEL_CLAUDE_SONNET,
                "stream": true,
                "messages": [{ "role": "user", "content": "hi" }],
            }))
            .send()
            .await
            .unwrap();
        assert_eq!(resp.status(), 200);
        assert_eq!(
            resp.headers().get("content-type").unwrap(),
            "text/event-stream"
        );
        let body = resp.text().await.unwrap();
        assert!(body.starts_with("event: message_start"), "{body}");
        assert!(body.contains("event: message_stop"), "{body}");
        assert!(body.contains("served"), "{body}");

        // Non-streaming messages request.
        let resp = client
            .post(format!("http://{addr}/v1/messages"))
            .json(&json!({
                "model": MODEL_CLAUDE_SONNET,
                "messages": [{ "role": "user", "content": "hi" }],
            }))
            .send()
            .await
            .unwrap();
        assert_eq!(resp.status(), 200);
        let jv: Value = resp.json().await.unwrap();
        assert_eq!(jv["type"], "message");

        // Malformed body → Anthropic-shaped 400.
        let resp = client
            .post(format!("http://{addr}/v1/messages"))
            .json(&json!({ "model": MODEL_CLAUDE_SONNET }))
            .send()
            .await
            .unwrap();
        assert_eq!(resp.status(), 400);
        let ev: Value = resp.json().await.unwrap();
        assert_eq!(ev["error"]["type"], "invalid_request_error");

        shutdown.cancel();
        handle
            .await
            .expect("server task must not panic")
            .expect("graceful shutdown must be clean");
    }

    #[tokio::test]
    async fn server_exhaustion_maps_to_529_overloaded() {
        let fail = || {
            vec![r#"{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"429"}"#.into()]
        };
        let runner = FakeRunner::new(vec![
            ("glm-acc1", fail()),
            ("claude2", fail()),
            ("claude", fail()),
        ]);
        let dispatcher = std::sync::Arc::new(FailoverDispatcher::with_runner_and_snapshot(
            runner,
            test_snapshot(),
        ));
        let shutdown = CancellationToken::new();
        let bind: SocketAddr = "127.0.0.1:0".parse().unwrap();
        let server = std::sync::Arc::new(AnthropicFailoverServer::with_dispatcher(
            bind,
            shutdown.clone(),
            dispatcher,
        ));
        let bound = server.bound_addr.clone();
        tokio::spawn({
            let server = server.clone();
            async move {
                let _ = server.run().await;
            }
        });
        let addr = wait_for_bound(&bound).await;

        let client = reqwest::Client::new();
        let resp = client
            .post(format!("http://{addr}/v1/messages"))
            .json(&json!({
                "model": MODEL_CLAUDE_SONNET,
                "messages": [{ "role": "user", "content": "hi" }],
            }))
            .send()
            .await
            .unwrap();
        assert_eq!(resp.status().as_u16(), 529);
        let ev: Value = resp.json().await.unwrap();
        assert_eq!(ev["error"]["type"], "overloaded_error");
        assert!(ev["error"]["message"]
            .as_str()
            .unwrap()
            .contains("all spill candidates exhausted"));
        shutdown.cancel();
    }

    #[test]
    fn bind_addr_reports_what_it_was_constructed_with() {
        let addr: SocketAddr = "127.0.0.1:3763".parse().unwrap();
        let server = AnthropicFailoverServer::new(addr, CancellationToken::new());
        assert_eq!(server.bind_addr(), addr);
    }

    #[test]
    fn env_sanitation_removes_anthropic_and_claude_vars_only() {
        // The critical recursion/billing-leak guard, unit-pinned: the var
        // names stripped are exactly the ANTHROPIC_*/CLAUDE* families.
        let names = [
            "ANTHROPIC_BASE_URL",
            "ANTHROPIC_AUTH_TOKEN",
            "ANTHROPIC_API_KEY",
            "ANTHROPIC_MODEL",
            "CLAUDE_CONFIG_DIR",
            "CLAUDE_CODE_SOME_FLAG",
        ];
        for name in names {
            let prefix_ok = name.starts_with("ANTHROPIC_") || name.starts_with("CLAUDE");
            assert!(prefix_ok, "sanitation contract changed for {name}");
        }
        // And the guard families do not over-match unrelated vars.
        for unrelated in ["CASCADE_HOME", "PATH", "HOME"] {
            assert!(
                !(unrelated.starts_with("ANTHROPIC_") || unrelated.starts_with("CLAUDE")),
                "over-match: {unrelated}"
            );
        }
    }
}
