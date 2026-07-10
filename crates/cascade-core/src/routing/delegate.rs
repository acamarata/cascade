//! # delegate — local delegate-out lane abstraction + concrete lanes
//!
//! ## Purpose
//!
//! Defines the [`DelegateLane`] trait and concrete implementations for every
//! v1.2 delegate target. Each lane shells out to a CLI binary (or returns
//! "unavailable") and captures the output as a typed result. No `sh -c`
//! interpolation is used — all subprocess args are discrete tokens.
//!
//! ## Lanes
//!
//! | Lane | CLI | Provider ID | Notes |
//! |------|-----|-------------|-------|
//! | `CodexLane` | `codex exec -p` | `openai-codex` | OpenAI Codex headless |
//! | `AgyLane`   | `agy -p` | `google-agy` | Google Pro / Gemini AGY headless |
//! | `OcGoLane`  | `opencode run` | `oc-go` | OpenCode-Go headless, dollar-metered |
//! | `ZaiLane`   | `claude -p` + GLM env | `zai` | z.ai GLM Coding Plan via Claude Code |
//! | `GfpLane`   | HTTP → :3762 GP proxy | `gfp` | Gemini Free Pool, free Flash (E1-S6) |
//! | `CcAccLane` | `claude -p` | `acc{N}` | Extra Claude accounts (see § Claude-PTY lane) |
//! | `MainClaudeLane` | `claude -p` | `claude` | Main T0 Claude (interactive / final-gate) |
//! | `LocalLlmLane`  | (in-process) | `local` | Local LLM fallback |
//!
//! ## Claude-PTY lane (CcAccLane / MainClaudeLane) — ESCALATION NOTICE
//!
//! The Claude-PTY lane (`CcAccLane`, `MainClaudeLane`) uses `claude -p` in
//! non-interactive (print-once) mode, which is publicly documented and does NOT
//! require PTY driving — it is a standard subprocess spawn identical to how
//! `cascade dispatch --harness cc` works today.
//!
//! The deeper PTY interactive-session driving (smithers/claude-p style) required
//! by the `cascade-ccapi` `LiveCcDriver` is separately escalated: that surface
//! is a documented stub (`crates/cascade-ccapi/src/driver.rs § LiveCcDriver`)
//! and is NOT attempted here. The `CcAccLane` here uses the safe `-p` flag.
//!
//! ## Inputs
//!
//! - `payload` — the prompt string to send to the lane.
//!
//! ## Outputs
//!
//! - `LaneResult::Output(String)` — captured stdout.
//! - `LaneResult::Unavailable(String)` — lane binary not found on PATH.
//! - Error via `CascadeError`.
//!
//! ## Constraints
//!
//! - No `sh -c` string interpolation — all args are discrete tokens.
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - Tests MUST pass without the real CLIs installed (use `check_availability`
//!   and skip / return Unavailable as appropriate).
//! - File ≤ 300 lines.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing::delegate

use std::{
    path::{Path, PathBuf},
    process::{Command as StdCommand, Stdio},
    time::Duration,
};

use cascade_types::error::{CascadeError, Result};

// ── LaneAvailability ──────────────────────────────────────────────────────────

/// Whether a lane's underlying CLI binary is present on PATH.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LaneAvailability {
    /// The binary was found and is executable.
    Available,
    /// The binary was not found. Callers should skip this lane.
    Unavailable { reason: String },
}

impl LaneAvailability {
    /// Returns `true` when the lane is available.
    pub fn is_available(&self) -> bool {
        matches!(self, LaneAvailability::Available)
    }
}

// ── LaneResult ────────────────────────────────────────────────────────────────

/// The result of executing a delegate lane.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum LaneResult {
    /// The lane ran and returned captured stdout.
    Output(String),
    /// The lane binary was not present — caller should fall through to next lane.
    Unavailable(String),
}

// ── DelegateLane trait ────────────────────────────────────────────────────────

/// Abstraction over a single delegate-out lane.
///
/// Each lane wraps one CLI tool (or one in-process capability) and exposes a
/// uniform interface for availability checking and prompt execution.
pub trait DelegateLane: Send + Sync {
    /// The provider ID string (matches `PROVIDER_*` constants and
    /// `sensitivity::provider_is_trusted_for_sensitive`).
    fn provider_id(&self) -> &str;

    /// Check whether the underlying CLI is available on PATH.
    /// Must not spawn a real process — only checks PATH for the binary.
    fn check_availability(&self) -> LaneAvailability;

    /// Execute the lane with the given prompt.
    ///
    /// Returns `LaneResult::Unavailable` if the binary is not on PATH,
    /// or `LaneResult::Output` with captured stdout on success.
    ///
    /// # Errors
    ///
    /// Returns `CascadeError` on subprocess spawn failure or I/O error.
    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult>;
}

// ── Binary helpers ────────────────────────────────────────────────────────────

/// Find a binary on PATH. Returns None when not found.
pub(crate) fn find_binary_opt(name: &str) -> Option<PathBuf> {
    let path_var = std::env::var_os("PATH").unwrap_or_default();
    for dir in std::env::split_paths(&path_var) {
        let candidate = dir.join(name);
        if is_exec(&candidate) {
            return Some(candidate);
        }
    }
    None
}

fn is_exec(path: &Path) -> bool {
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

/// Run a subprocess with discrete args, capture stdout, enforce timeout.
/// NO `sh -c` — binary and args are passed as separate tokens.
pub(crate) fn run_lane_subprocess(
    binary: &Path,
    args: &[&str],
    payload: &str,
    timeout: Duration,
) -> Result<String> {
    let mut child = StdCommand::new(binary)
        .args(args)
        .arg(payload)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| CascadeError::Io {
            path: binary.to_path_buf(),
            operation: "spawn",
            source: e,
        })?;

    let deadline = std::time::Instant::now() + timeout;
    loop {
        if std::time::Instant::now() >= deadline {
            let _ = child.kill();
            return Err(CascadeError::Other(format!(
                "lane subprocess timed out after {}s",
                timeout.as_secs()
            )));
        }
        match child.try_wait().map_err(|e| CascadeError::Io {
            path: binary.to_path_buf(),
            operation: "wait",
            source: e,
        })? {
            Some(_status) => break,
            None => std::thread::sleep(Duration::from_millis(100)),
        }
    }

    let output = child
        .wait_with_output()
        .unwrap_or_else(|_| std::process::Output {
            status: std::process::ExitStatus::default(),
            stdout: Vec::new(),
            stderr: Vec::new(),
        });
    Ok(String::from_utf8_lossy(&output.stdout).into_owned())
}

// ── Concrete lane: CodexLane ─────────────────────────────────────────────────

/// OpenAI Codex headless lane (`codex exec -p <payload>`).
///
/// Provider ID: `openai-codex`. External — blocked for Sensitive content.
pub struct CodexLane;

impl DelegateLane for CodexLane {
    fn provider_id(&self) -> &str {
        cascade_types::quota_store::PROVIDER_OPENAI_CODEX
    }

    fn check_availability(&self) -> LaneAvailability {
        match find_binary_opt("codex") {
            Some(_) => LaneAvailability::Available,
            None => LaneAvailability::Unavailable {
                reason: "`codex` binary not found on PATH; install OpenAI Codex CLI".into(),
            },
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        let Some(bin) = find_binary_opt("codex") else {
            return Ok(LaneResult::Unavailable("`codex` not on PATH".into()));
        };
        let out = run_lane_subprocess(&bin, &["exec", "-p"], payload, timeout)?;
        Ok(LaneResult::Output(out))
    }
}

// ── Concrete lane: AgyLane ────────────────────────────────────────────────────

/// Google Pro / Gemini AGY headless lane (`agy -p <payload>`).
///
/// Provider ID: `google-agy`. External — blocked for Sensitive content.
pub struct AgyLane;

impl DelegateLane for AgyLane {
    fn provider_id(&self) -> &str {
        cascade_types::quota_store::PROVIDER_GOOGLE_AGY
    }

    fn check_availability(&self) -> LaneAvailability {
        match find_binary_opt("agy") {
            Some(_) => LaneAvailability::Available,
            None => LaneAvailability::Unavailable {
                reason: "`agy` binary not found on PATH; install Google AGY CLI".into(),
            },
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        let Some(bin) = find_binary_opt("agy") else {
            return Ok(LaneResult::Unavailable("`agy` not on PATH".into()));
        };
        let out = run_lane_subprocess(&bin, &["-p"], payload, timeout)?;
        Ok(LaneResult::Output(out))
    }
}

// ── Concrete lane: OcGoLane ───────────────────────────────────────────────────

/// OpenCode-Go headless lane (`opencode run <payload>`).
///
/// Provider ID: `oc-go`. External, dollar-metered — blocked for Sensitive.
pub struct OcGoLane;

impl DelegateLane for OcGoLane {
    fn provider_id(&self) -> &str {
        cascade_types::quota_store::PROVIDER_OC_GO
    }

    fn check_availability(&self) -> LaneAvailability {
        match find_binary_opt("opencode") {
            Some(_) => LaneAvailability::Available,
            None => LaneAvailability::Unavailable {
                reason: "`opencode` binary not found on PATH; install OpenCode CLI".into(),
            },
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        let Some(bin) = find_binary_opt("opencode") else {
            return Ok(LaneResult::Unavailable("`opencode` not on PATH".into()));
        };
        let out = run_lane_subprocess(&bin, &["run"], payload, timeout)?;
        Ok(LaneResult::Output(out))
    }
}

// ── Concrete lane: ZaiLane ───────────────────────────────────────────────────

/// z.ai GLM Coding Plan lane — `claude -p` with GLM env loaded from
/// `~/.claude-glm/cascade-env.sh`.
///
/// Provider ID: `zai`. External — blocked for Sensitive content.
///
/// z.ai GLM Coding Plan compliance: the plan permits usage only via supported
/// products (Claude Code with ANTHROPIC_BASE_URL set to the z.ai endpoint).
/// We do NOT call the z.ai API directly; cascade-env.sh carries the env vars
/// that redirect `claude` to the GLM endpoint transparently.
pub struct ZaiLane;

impl DelegateLane for ZaiLane {
    fn provider_id(&self) -> &str {
        cascade_types::quota_store::PROVIDER_ZAI
    }

    fn check_availability(&self) -> LaneAvailability {
        let has_claude = find_binary_opt("claude").is_some();
        let has_env = dirs::home_dir()
            .map(|h| h.join(".claude-glm").join("cascade-env.sh").exists())
            .unwrap_or(false);
        if has_claude && has_env {
            LaneAvailability::Available
        } else if !has_claude {
            LaneAvailability::Unavailable {
                reason: "`claude` binary not found on PATH; install Claude Code CLI".into(),
            }
        } else {
            LaneAvailability::Unavailable {
                reason: "`~/.claude-glm/cascade-env.sh` not found; Zai GLM env not installed"
                    .into(),
            }
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        let Some(bin) = find_binary_opt("claude") else {
            return Ok(LaneResult::Unavailable("`claude` not on PATH".into()));
        };
        let env_path = match dirs::home_dir() {
            Some(h) => h.join(".claude-glm").join("cascade-env.sh"),
            None => {
                return Ok(LaneResult::Unavailable(
                    "could not determine HOME for Zai env lookup".into(),
                ))
            }
        };
        if !env_path.exists() {
            return Ok(LaneResult::Unavailable(
                "`~/.claude-glm/cascade-env.sh` not found; Zai GLM env not installed".into(),
            ));
        }
        // Load GLM env vars from cascade-env.sh and inject into the subprocess.
        let env_content = std::fs::read_to_string(&env_path).unwrap_or_default();
        let env_vars: Vec<(String, String)> = env_content
            .lines()
            .filter_map(|raw| {
                let line = raw.trim().strip_prefix("export ").unwrap_or(raw.trim());
                let (k, v) = line.split_once('=')?;
                let k = k.trim().to_string();
                let v = v.trim().trim_matches('"').trim_matches('\'').to_string();
                if k.chars().all(|c| c.is_ascii_alphanumeric() || c == '_') {
                    Some((k, v))
                } else {
                    None
                }
            })
            .collect();

        let mut child = std::process::Command::new(&bin)
            .args(["-p", payload])
            .envs(env_vars)
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::null())
            .spawn()
            .map_err(|e| cascade_types::error::CascadeError::Io {
                path: bin.clone(),
                operation: "spawn",
                source: e,
            })?;

        let deadline = std::time::Instant::now() + timeout;
        loop {
            if std::time::Instant::now() >= deadline {
                let _ = child.kill();
                return Err(cascade_types::error::CascadeError::Other(format!(
                    "ZaiLane subprocess timed out after {}s",
                    timeout.as_secs()
                )));
            }
            match child
                .try_wait()
                .map_err(|e| cascade_types::error::CascadeError::Io {
                    path: bin.clone(),
                    operation: "wait",
                    source: e,
                })? {
                Some(_) => break,
                None => std::thread::sleep(Duration::from_millis(100)),
            }
        }
        let output = child
            .wait_with_output()
            .unwrap_or_else(|_| std::process::Output {
                status: std::process::ExitStatus::default(),
                stdout: Vec::new(),
                stderr: Vec::new(),
            });
        Ok(LaneResult::Output(
            String::from_utf8_lossy(&output.stdout).into_owned(),
        ))
    }
}

// ── Concrete lane: GfpLane ────────────────────────────────────────────────────

/// Gemini Free Pool lane — real HTTP path to the local GP proxy (E1-S6).
///
/// Provider ID: `gfp`. External — blocked for Sensitive content.
///
/// Executes by POSTing the prompt to the daemon's Anthropic-compat adapter at
/// `http://127.0.0.1:3762/v1/messages` (see [`super::gfp_http`]), which fronts
/// the :3761 key-pool with rotation and 429 cooldown. When the proxy is not
/// running, the pool is exhausted, or the response carries an error envelope,
/// the lane returns `Unavailable` so the router spills onward — output is
/// never fabricated.
///
/// Kill-switch: setting `CASCADE_GFP_LANE_OFF` (any value) forces
/// `Unavailable` — used by hermetic tests and as an operational off-switch.
pub struct GfpLane;

impl DelegateLane for GfpLane {
    fn provider_id(&self) -> &str {
        cascade_types::quota_store::PROVIDER_GFP
    }

    fn check_availability(&self) -> LaneAvailability {
        // The lane's transport is a curl subprocess (crate-standard sync HTTP
        // mechanism — cascade-core has no HTTP client dependency). Proxy
        // reachability and pool health are determined at execute() time.
        match find_binary_opt("curl") {
            Some(_) => LaneAvailability::Available,
            None => LaneAvailability::Unavailable {
                reason: "`curl` not found on PATH; cannot reach the GP proxy".into(),
            },
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        if std::env::var_os("CASCADE_GFP_LANE_OFF").is_some() {
            return Ok(LaneResult::Unavailable(
                "GFP lane disabled via CASCADE_GFP_LANE_OFF".into(),
            ));
        }
        super::gfp_http::post_messages(super::gfp_http::GFP_MESSAGES_URL, payload, timeout)
    }
}

// ── Concrete lane: CcAccLane ──────────────────────────────────────────────────

/// Extra Claude account lane (`claude -p <payload>`).
///
/// Provider ID: `acc{N}` (e.g. `acc2`, `acc3`). Trusted for Sensitive content.
///
/// Uses `claude -p` (non-interactive, print-once mode) — a publicly documented,
/// stable interface. The deeper PTY interactive-session driving (LiveCcDriver)
/// is a separately escalated stub in `cascade-ccapi`.
///
/// `account_suffix` is the suffix appended to `"acc"` (e.g. `2` → `"acc2"`).
pub struct CcAccLane {
    /// Suffix for the account label (e.g. 2 for "acc2").
    pub account_suffix: u8,
    /// Optional explicit path to the `claude` binary (for testing).
    pub binary_override: Option<PathBuf>,
}

impl CcAccLane {
    /// Construct a lane for extra account `N` (N ≥ 2).
    pub fn new(account_suffix: u8) -> Self {
        Self {
            account_suffix,
            binary_override: None,
        }
    }
}

impl DelegateLane for CcAccLane {
    fn provider_id(&self) -> &str {
        // Note: we cannot return a dynamic String from &str without a field.
        // We always use the generic "acc" prefix — specific routing by suffix
        // is tracked in the quota store under account_id (acc2, acc3, etc.).
        // The router uses account_suffix separately when scheduling.
        "acc"
    }

    fn check_availability(&self) -> LaneAvailability {
        let bin = self
            .binary_override
            .as_deref()
            .and_then(|p| {
                if is_exec(p) {
                    Some(p.to_path_buf())
                } else {
                    None
                }
            })
            .or_else(|| find_binary_opt("claude"));
        match bin {
            Some(_) => LaneAvailability::Available,
            None => LaneAvailability::Unavailable {
                reason: "`claude` binary not found on PATH; install Claude Code CLI".into(),
            },
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        let bin = self
            .binary_override
            .as_deref()
            .filter(|p| is_exec(p))
            .map(|p| p.to_path_buf())
            .or_else(|| find_binary_opt("claude"));
        let Some(bin) = bin else {
            return Ok(LaneResult::Unavailable("`claude` not on PATH".into()));
        };
        let out = run_lane_subprocess(&bin, &["-p"], payload, timeout)?;
        Ok(LaneResult::Output(out))
    }
}

// ── Concrete lane: MainClaudeLane ─────────────────────────────────────────────

/// Main T0 Claude lane — reserved for InteractiveChat and FinalGate.
///
/// Provider ID: `claude`. Trusted for Sensitive content.
/// Uses `claude -p` (non-interactive print-once) just like `cascade dispatch`.
pub struct MainClaudeLane {
    /// Optional explicit path to the `claude` binary (for testing).
    pub binary_override: Option<PathBuf>,
}

#[allow(clippy::derivable_impls)] // explicit Default is intentional for clarity
impl Default for MainClaudeLane {
    fn default() -> Self {
        Self {
            binary_override: None,
        }
    }
}

impl DelegateLane for MainClaudeLane {
    fn provider_id(&self) -> &str {
        "claude"
    }

    fn check_availability(&self) -> LaneAvailability {
        let bin = self
            .binary_override
            .as_deref()
            .and_then(|p| {
                if is_exec(p) {
                    Some(p.to_path_buf())
                } else {
                    None
                }
            })
            .or_else(|| find_binary_opt("claude"));
        match bin {
            Some(_) => LaneAvailability::Available,
            None => LaneAvailability::Unavailable {
                reason: "`claude` binary not found on PATH".into(),
            },
        }
    }

    fn execute(&self, payload: &str, timeout: Duration) -> Result<LaneResult> {
        let bin = self
            .binary_override
            .as_deref()
            .filter(|p| is_exec(p))
            .map(|p| p.to_path_buf())
            .or_else(|| find_binary_opt("claude"));
        let Some(bin) = bin else {
            return Ok(LaneResult::Unavailable("`claude` not on PATH".into()));
        };
        let out = run_lane_subprocess(&bin, &["-p"], payload, timeout)?;
        Ok(LaneResult::Output(out))
    }
}

// ── Concrete lane: LocalLlmLane ───────────────────────────────────────────────

/// Local LLM fallback lane.
///
/// Provider ID: `local`. Trusted for Sensitive content.
///
/// In v1.2 this lane signals Unavailable — the in-process `cascade-local-llm`
/// adapter is the real path, which is wired separately by the daemon. This stub
/// ensures the lane appears in the preference list so the router can report it.
pub struct LocalLlmLane;

impl DelegateLane for LocalLlmLane {
    fn provider_id(&self) -> &str {
        "local"
    }

    fn check_availability(&self) -> LaneAvailability {
        // Local LLM availability depends on downloaded model weights.
        // The cascade-local-llm crate owns that check; this stub returns
        // Available so it shows in the preference list.
        LaneAvailability::Available
    }

    fn execute(&self, _payload: &str, _timeout: Duration) -> Result<LaneResult> {
        Ok(LaneResult::Unavailable(
            "LocalLlm lane: routed via cascade-local-llm adapter, not inline CLI".into(),
        ))
    }
}

/// Type alias for a boxed lane (heap-allocated, Send+Sync).
pub type DelegateTarget = Box<dyn DelegateLane>;

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn unavail_reason(av: &LaneAvailability) -> &str {
        match av {
            LaneAvailability::Unavailable { reason } => reason.as_str(),
            LaneAvailability::Available => panic!("expected Unavailable"),
        }
    }

    // ── provider_id correctness ────────────────────────────────────────────

    #[test]
    fn codex_provider_id() {
        assert_eq!(CodexLane.provider_id(), "openai-codex");
    }

    #[test]
    fn agy_provider_id() {
        assert_eq!(AgyLane.provider_id(), "google-agy");
    }

    #[test]
    fn ocgo_provider_id() {
        assert_eq!(OcGoLane.provider_id(), "oc-go");
    }

    #[test]
    fn gfp_provider_id() {
        assert_eq!(GfpLane.provider_id(), "gfp");
    }

    #[test]
    fn cc_acc_provider_id() {
        assert_eq!(CcAccLane::new(2).provider_id(), "acc");
    }

    #[test]
    fn main_claude_provider_id() {
        assert_eq!(MainClaudeLane::default().provider_id(), "claude");
    }

    #[test]
    fn local_llm_provider_id() {
        assert_eq!(LocalLlmLane.provider_id(), "local");
    }

    // ── availability when binary absent ───────────────────────────────────

    #[test]
    fn codex_unavailable_when_not_installed() {
        // `codex` is very unlikely to be on PATH in this repo's CI.
        if find_binary_opt("codex").is_none() {
            let av = CodexLane.check_availability();
            assert!(!av.is_available());
            let r = unavail_reason(&av);
            assert!(r.contains("codex"), "reason should mention binary: {r}");
        }
    }

    #[test]
    fn agy_unavailable_when_not_installed() {
        if find_binary_opt("agy").is_none() {
            let av = AgyLane.check_availability();
            assert!(!av.is_available());
        }
    }

    #[test]
    fn ocgo_unavailable_when_not_installed() {
        if find_binary_opt("opencode").is_none() {
            let av = OcGoLane.check_availability();
            assert!(!av.is_available());
        }
    }

    // ── execute returns Unavailable when binary absent ─────────────────────

    #[test]
    fn codex_execute_returns_unavailable_when_absent() {
        if find_binary_opt("codex").is_none() {
            let result = CodexLane.execute("test", Duration::from_secs(5)).unwrap();
            assert!(matches!(result, LaneResult::Unavailable(_)));
        }
    }

    #[test]
    fn agy_execute_returns_unavailable_when_absent() {
        if find_binary_opt("agy").is_none() {
            let result = AgyLane.execute("test", Duration::from_secs(5)).unwrap();
            assert!(matches!(result, LaneResult::Unavailable(_)));
        }
    }

    #[test]
    fn ocgo_execute_returns_unavailable_when_absent() {
        if find_binary_opt("opencode").is_none() {
            let result = OcGoLane.execute("test", Duration::from_secs(5)).unwrap();
            assert!(matches!(result, LaneResult::Unavailable(_)));
        }
    }

    // ── GFP kill-switch and local stub behavior ────────────────────────────

    #[test]
    fn gfp_execute_respects_kill_switch() {
        // Hermetic: the env kill-switch must short-circuit before any I/O.
        // (Transport + parsing behavior is tested in `routing::gfp_http`.)
        std::env::set_var("CASCADE_GFP_LANE_OFF", "1");
        let result = GfpLane.execute("test", Duration::from_secs(5)).unwrap();
        assert!(matches!(result, LaneResult::Unavailable(_)));
    }

    #[test]
    fn local_llm_execute_returns_provider_unavailable() {
        let result = LocalLlmLane
            .execute("test", Duration::from_secs(5))
            .unwrap();
        assert!(matches!(result, LaneResult::Unavailable(_)));
    }

    // ── LaneAvailability helpers ───────────────────────────────────────────

    #[test]
    fn lane_availability_is_available() {
        assert!(LaneAvailability::Available.is_available());
        assert!(!LaneAvailability::Unavailable { reason: "x".into() }.is_available());
    }

    // ── CcAccLane with binary override ────────────────────────────────────

    #[test]
    fn cc_acc_lane_unavailable_with_missing_override() {
        let lane = CcAccLane {
            account_suffix: 2,
            binary_override: Some(PathBuf::from("/no/such/binary/claude")),
        };
        // Override path doesn't exist, falls back to PATH lookup.
        // If `claude` is on PATH the lane is available; if not, unavailable.
        // Either way must not panic.
        let _ = lane.check_availability();
    }

    // ── find_binary_opt ────────────────────────────────────────────────────

    #[test]
    fn find_binary_opt_finds_echo() {
        #[cfg(unix)]
        {
            let r = find_binary_opt("echo");
            assert!(r.is_some(), "expected to find `echo` on PATH");
        }
    }

    #[test]
    fn find_binary_opt_returns_none_for_missing() {
        let r = find_binary_opt("__cascade_no_such_binary_xyz__");
        assert!(r.is_none());
    }
}
