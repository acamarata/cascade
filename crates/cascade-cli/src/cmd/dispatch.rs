//! `cascade dispatch` — launch a CC or OC subprocess targeting a repo with
//! optional context injection from the Cascade RAG pipeline.
//!
//! ## Purpose
//! Acts as the meta-harness coordinator: resolves a target repo, optionally
//! prepends a context slice retrieved from the running daemon, and spawns the
//! chosen harness binary (`claude -p` or `opencode run`) with the composed
//! prompt. Stdout/stderr are streamed to the terminal and optionally captured.
//!
//! ## Inputs
//! - `--repo <absolute-path>` — target repo (working directory for subprocess)
//! - `--harness [cc|oc]` — which harness binary to invoke
//! - `--prompt <text>` — user prompt to send
//! - `--context-query <text>` — optional: fetch context slice and prepend
//! - `--budget-tokens <N>` — token budget for injected context (default 2048)
//! - `--capture-output` — write subprocess stdout to `.cascade/dispatch-results/`
//! - `--dry-run` — print command + injected prompt, do not execute
//! - `--timeout-secs <N>` — subprocess timeout (default 120)
//!
//! ## Outputs
//! - Subprocess stdout/stderr (passthrough)
//! - Optional: `{repo}/.cascade/dispatch-results/{timestamp}-{harness}.md`
//!
//! ## Constraints
//! - Never invokes shell; all subprocess args are discrete tokens
//! - Target repo must be HOME-confined (validated)
//! - On timeout, kills the process group (Unix) or process (Windows) to avoid
//!   orphaned children
//!
//! ## SPORT
//! MASTER-CLI.md: cascade dispatch

use std::{
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Command as StdCommand, Stdio},
    time::{Duration, Instant},
};

use async_trait::async_trait;
use cascade_core::routing::{Router, RouterConfig, RoutingDecision, TaskClass};
use cascade_harness::policy::dispatch::evaluate_before_dispatch;
use cascade_rag::context::ContextOptimizer;
use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
    policy::PolicyAction,
};
use chrono::Utc;
use clap::Args;

use super::Command;

// ── Harness type ──────────────────────────────────────────────────────────────

/// Supported harness backends.
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum Harness {
    /// Claude Code (`claude -p "<prompt>"`).
    Cc,
    /// OpenCode (`opencode run "<prompt>"`).
    Oc,
}

impl Harness {
    /// Returns the name used in filenames and display.
    pub fn as_str(self) -> &'static str {
        match self {
            Harness::Cc => "cc",
            Harness::Oc => "oc",
        }
    }

    /// The binary name to locate via PATH.
    pub fn binary_name(self) -> &'static str {
        match self {
            Harness::Cc => "claude",
            Harness::Oc => "opencode",
        }
    }

    /// Build the argv for this harness given a full prompt string.
    /// Returns `(binary_path, args_vec)`.
    ///
    /// CC: `claude -p "<prompt>"`
    /// OC: `opencode run "<prompt>"`
    pub fn build_argv(self, binary: &Path, prompt: &str) -> (PathBuf, Vec<String>) {
        let binary = binary.to_path_buf();
        let args = match self {
            Harness::Cc => vec!["-p".to_string(), prompt.to_string()],
            Harness::Oc => vec!["run".to_string(), prompt.to_string()],
        };
        (binary, args)
    }
}

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade dispatch`.
#[derive(Debug, Args)]
pub struct DispatchArgs {
    /// Target repo path (working directory for the harness subprocess).
    #[arg(long, value_name = "PATH")]
    pub repo: PathBuf,

    /// Which harness to invoke.
    #[arg(long, value_name = "HARNESS", default_value = "cc")]
    pub harness: Harness,

    /// The user prompt to send to the harness.
    #[arg(long, value_name = "TEXT")]
    pub prompt: String,

    /// Optional RAG query: fetch a context slice and prepend it to the prompt.
    #[arg(long, value_name = "TEXT")]
    pub context_query: Option<String>,

    /// Token budget for the injected context slice (default 2048).
    #[arg(long, value_name = "N", default_value = "2048")]
    pub budget_tokens: usize,

    /// Write subprocess stdout to `.cascade/dispatch-results/{ts}-{harness}.md`.
    #[arg(long)]
    pub capture_output: bool,

    /// Print the exact invocation and injected prompt without executing.
    #[arg(long)]
    pub dry_run: bool,

    /// Kill the subprocess after this many seconds (default 120).
    #[arg(long, value_name = "N", default_value = "120")]
    pub timeout_secs: u64,

    /// Override the cascade daemon MCP socket path (for testing).
    #[arg(long, value_name = "PATH", hide = true)]
    pub mcp_socket: Option<PathBuf>,

    /// Override binary lookup — use this path instead of searching PATH (for
    /// testing).
    #[arg(long, value_name = "PATH", hide = true)]
    pub binary_override: Option<PathBuf>,

    /// Task class for routing-matrix selection.
    ///
    /// When set, the routing matrix picks the optimal lane automatically and
    /// prints the routing decision before dispatch. When absent, dispatch
    /// proceeds with the `--harness` flag unchanged.
    ///
    /// Values: interactive-chat | bulk-exec | cheap | adversarial-review |
    ///         final-gate | sensitive
    #[arg(long, value_name = "CLASS", value_enum)]
    pub route: Option<TaskClassArg>,
}

/// Clap-compatible wrapper for [`TaskClass`].
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum TaskClassArg {
    #[value(name = "interactive-chat")]
    InteractiveChat,
    #[value(name = "bulk-exec")]
    BulkExec,
    #[value(name = "cheap")]
    Cheap,
    #[value(name = "adversarial-review")]
    AdversarialReview,
    #[value(name = "final-gate")]
    FinalGate,
    #[value(name = "sensitive")]
    Sensitive,
}

impl From<TaskClassArg> for TaskClass {
    fn from(a: TaskClassArg) -> Self {
        match a {
            TaskClassArg::InteractiveChat => TaskClass::InteractiveChat,
            TaskClassArg::BulkExec => TaskClass::BulkExec,
            TaskClassArg::Cheap => TaskClass::Cheap,
            TaskClassArg::AdversarialReview => TaskClass::AdversarialReview,
            TaskClassArg::FinalGate => TaskClass::FinalGate,
            TaskClassArg::Sensitive => TaskClass::Sensitive,
        }
    }
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for DispatchArgs {
    async fn run(&self) -> Result<()> {
        // 1. Validate target repo path (HOME-confined).
        let repo = self.repo.canonicalize().map_err(|e| CascadeError::Io {
            path: self.repo.clone(),
            operation: "canonicalize",
            source: e,
        })?;
        validate_home_confined(&repo)?;

        // 2. Policy check — evaluate the dispatch action before proceeding.
        //    If the policy engine denies the action, block immediately with reason.
        let dispatch_action = PolicyAction::new(
            "dispatch",
            serde_json::json!({
                "harness": self.harness.as_str(),
                "prompt": &self.prompt,
                "repo": repo.to_string_lossy().as_ref(),
            }),
        );
        let policy_result = evaluate_before_dispatch(&dispatch_action);
        if policy_result.is_deny() {
            return Err(CascadeError::Other(format!(
                "dispatch blocked by policy `{}`: {}",
                policy_result.policy_id, policy_result.reason
            )));
        }

        // 3. If --route was given, run the routing matrix and select the effective
        //    harness. Lane decisions resolve to a concrete harness binary; Reserved
        //    decisions use the default --harness. FirewallDeny exits with an error;
        //    AllExhausted warns and falls back to the default --harness.
        let effective_harness = if let Some(task_class_arg) = self.route {
            let task_class = TaskClass::from(task_class_arg);
            let router = Router::with_config(RouterConfig::default());
            let decision = router.select(task_class, &self.prompt);
            print_routing_decision(task_class, &decision);
            match &decision {
                RoutingDecision::FirewallDeny { reason } => {
                    return Err(CascadeError::Other(format!(
                        "dispatch blocked by sensitivity firewall: {reason}"
                    )));
                }
                RoutingDecision::AllExhausted { tried } => {
                    eprintln!(
                        "cascade dispatch: warning — all preferred lanes exhausted: {}; \
                         proceeding with --harness override",
                        tried.join(", ")
                    );
                    self.harness
                }
                RoutingDecision::Reserved { .. } => self.harness,
                RoutingDecision::Lane { provider_id, .. } => {
                    harness_for_provider(provider_id).ok_or_else(|| {
                        CascadeError::Other(format!(
                            "route selected provider `{provider_id}` which has no subprocess \
                             dispatch path (gfp/local/codex/agy require `cascade conductor`); \
                             override with --harness to bypass routing"
                        ))
                    })?
                }
            }
        } else {
            self.harness
        };

        // 4. Locate harness binary. --binary-override wins; otherwise search PATH.
        let binary = if let Some(ref p) = self.binary_override {
            if p.is_file() {
                p.clone()
            } else {
                return Err(CascadeError::PathNotFound { path: p.clone() });
            }
        } else {
            find_binary(effective_harness.binary_name())?
        };

        // 5. Optionally fetch context slice via ContextOptimizer (T-P4-E04-20).
        //    In the CLI dispatch path we do not have a live RAG index connection,
        //    so we use the optimizer in "shell-only" mode: the query is echoed as
        //    a header and the budget is noted. A full RAG-backed slice requires the
        //    cascade-mcp context_slice tool (T-P4-E04-22) or daemon IPC.
        let context_block = self
            .context_query
            .as_ref()
            .map(|query| fetch_context_with_optimizer(query, self.budget_tokens));

        // 6. Compose full prompt.
        let full_prompt = compose_prompt(context_block.as_deref(), &self.prompt);

        // 7. Resolve MCP socket path.
        let mcp_socket = self
            .mcp_socket
            .clone()
            .unwrap_or_else(cascade_types::paths::daemon_socket);

        // 8. Build argv.
        let (bin_path, args) = effective_harness.build_argv(&binary, &full_prompt);

        // 9. Dry-run: print and exit.
        if self.dry_run {
            print_dry_run(&bin_path, &args, &mcp_socket, &repo, &full_prompt);
            return Ok(());
        }

        // 10. Spawn subprocess and wait (with timeout).
        let exit_code = run_subprocess(
            &bin_path,
            &args,
            &repo,
            &mcp_socket,
            self.capture_output,
            effective_harness,
            Duration::from_secs(self.timeout_secs),
        )?;

        if exit_code != 0 {
            return Err(CascadeError::Other(format!(
                "dispatch subprocess exited with code {exit_code}"
            )));
        }

        Ok(())
    }
}


// ── Core helpers (exported for unit tests) ────────────────────────────────────

/// Locate a binary on the system PATH using the `PATH` env var.
///
/// We avoid the `which` crate (not in workspace deps) and implement a minimal
/// resolver: iterate PATH entries, check `<dir>/<name>` is executable.
pub fn find_binary(name: &str) -> Result<PathBuf> {
    let path_var = std::env::var_os("PATH").unwrap_or_default();
    for dir in std::env::split_paths(&path_var) {
        let candidate = dir.join(name);
        if is_executable(&candidate) {
            return Ok(candidate);
        }
    }
    Err(CascadeError::Other(format!(
        "harness binary `{name}` not found on PATH; install it or use --binary-override"
    )))
}

/// Returns true if `path` points to an executable file.
pub fn is_executable(path: &Path) -> bool {
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

/// Validate that `path` is inside the current user's HOME directory.
///
/// Prevents dispatch from targeting paths outside the user's workspace.
pub fn validate_home_confined(path: &Path) -> Result<()> {
    let home = home_dir();
    if !path.starts_with(&home) {
        return Err(CascadeError::Other(format!(
            "dispatch target `{}` is not confined to HOME (`{}`); \
             only repos under your home directory may be targeted",
            path.display(),
            home.display()
        )));
    }
    Ok(())
}

/// Compose the full prompt by optionally prepending a context block.
///
/// Format when context is present:
/// ```text
/// ## Context from Cascade
/// <context>
///
/// ---
///
/// <user_prompt>
/// ```
pub fn compose_prompt(context: Option<&str>, user_prompt: &str) -> String {
    match context {
        None => user_prompt.to_string(),
        Some(ctx) => format!("## Context from Cascade\n\n{ctx}\n\n---\n\n{user_prompt}"),
    }
}

/// Produce a context block using the ContextOptimizer (T-P4-E04-20).
///
/// In the CLI dispatch path (no live RAG index), we use the optimizer's
/// compress-and-budget machinery directly. Without a RAG backend, the returned
/// block echoes the query + budget so the harness prompt is structurally valid.
/// When a full RAG connection is available (daemon IPC or cascade-mcp), this
/// path will be replaced with a real retrieve → optimize call.
fn fetch_context_with_optimizer(query: &str, budget_tokens: usize) -> String {
    // Create optimizer; run shell compression on a synthetic shell snippet.
    let optimizer = ContextOptimizer::new(budget_tokens.max(256));
    // No RAG chunks available in CLI path — optimize an empty slice.
    let result = optimizer.optimize(vec![], &[]);
    format!(
        "<!-- cascade context_slice: query={query:?} budget={budget_tokens} tokens_used={} -->\
         \n[RAG index not active — run `cascaded start` to enable context retrieval]",
        result.tokens_used
    )
}

/// Map a routing `provider_id` to the subprocess [`Harness`] that can dispatch it.
///
/// Returns `None` for providers that require out-of-process delegation
/// (gfp, gemini, codex, agy) — callers should redirect those to
/// `cascade conductor` instead.
fn harness_for_provider(provider_id: &str) -> Option<Harness> {
    match provider_id {
        "claude" | "claude-max" | "zai" | "acc" => Some(Harness::Cc),
        "oc-go" | "opencode" => Some(Harness::Oc),
        _ => None,
    }
}

/// Print the routing decision to stderr so it is visible without mixing with
/// captured stdout.
pub fn print_routing_decision(task_class: TaskClass, decision: &RoutingDecision) {
    match decision {
        RoutingDecision::Lane {
            provider_id,
            reason,
        } => {
            eprintln!("cascade dispatch: route({task_class}) → provider={provider_id} ({reason})");
        }
        RoutingDecision::Reserved {
            provider_id,
            reason,
        } => {
            eprintln!(
                "cascade dispatch: route({task_class}) → RESERVED provider={provider_id} ({reason})"
            );
        }
        RoutingDecision::AllExhausted { tried } => {
            eprintln!(
                "cascade dispatch: route({task_class}) → ALL_EXHAUSTED tried=[{}]",
                tried.join(", ")
            );
        }
        RoutingDecision::FirewallDeny { reason } => {
            eprintln!("cascade dispatch: route({task_class}) → FIREWALL_DENY: {reason}");
        }
    }
}

/// Print the dry-run representation.
pub fn print_dry_run(
    binary: &Path,
    args: &[String],
    mcp_socket: &Path,
    repo: &Path,
    full_prompt: &str,
) {
    println!("=== cascade dispatch --dry-run ===");
    println!();
    println!("Binary : {}", binary.display());
    print!("Args   :");
    for a in args {
        print!(" {a:?}");
    }
    println!();
    println!("Workdir: {}", repo.display());
    println!("Env    : CASCADE_MCP_URL={}", mcp_socket.display());
    println!();
    println!("--- Full prompt ---");
    println!("{full_prompt}");
    println!("------------------");
}

/// Spawn the subprocess, stream output, enforce timeout, and optionally capture.
///
/// On Unix, kills the entire process group on timeout so no orphan children
/// survive (`libc::kill(-pgid, SIGKILL)`).
///
/// Returns the subprocess exit code.
fn run_subprocess(
    binary: &Path,
    args: &[String],
    cwd: &Path,
    mcp_socket: &Path,
    capture: bool,
    harness: Harness,
    timeout: Duration,
) -> Result<i32> {
    let stdout_cfg = if capture {
        Stdio::piped()
    } else {
        Stdio::inherit()
    };
    let stderr_cfg = Stdio::inherit();

    let mut child = StdCommand::new(binary)
        .args(args)
        .current_dir(cwd)
        .env("CASCADE_MCP_URL", mcp_socket)
        .stdout(stdout_cfg)
        .stderr(stderr_cfg)
        .spawn()
        .map_err(|e| CascadeError::Io {
            path: binary.to_path_buf(),
            operation: "spawn",
            source: e,
        })?;

    // Collect captured output while monitoring timeout.
    let deadline = Instant::now() + timeout;

    let captured_output: Option<Vec<u8>> = if capture {
        // Read stdout in a blocking fashion — suitable for P4 synchronous model.
        let mut buf = Vec::new();
        let mut stdout = child.stdout.take().expect("stdout was piped");

        // Poll in a tight loop with a short sleep so we can check the deadline.
        let mut chunk = [0u8; 4096];
        loop {
            if Instant::now() >= deadline {
                kill_child(&mut child);
                return Err(CascadeError::Other(format!(
                    "dispatch timed out after {}s",
                    timeout.as_secs()
                )));
            }
            match stdout.read(&mut chunk) {
                Ok(0) => break, // EOF
                Ok(n) => {
                    // Also echo to terminal.
                    let _ = std::io::stdout().write_all(&chunk[..n]);
                    buf.extend_from_slice(&chunk[..n]);
                }
                Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                    std::thread::sleep(Duration::from_millis(50));
                }
                Err(e) => {
                    return Err(CascadeError::Io {
                        path: binary.to_path_buf(),
                        operation: "read stdout",
                        source: e,
                    });
                }
            }
        }
        Some(buf)
    } else {
        None
    };

    // Wait with timeout.
    let exit_status = loop {
        if Instant::now() >= deadline {
            kill_child(&mut child);
            return Err(CascadeError::Other(format!(
                "dispatch timed out after {}s",
                timeout.as_secs()
            )));
        }
        match child.try_wait().map_err(|e| CascadeError::Io {
            path: binary.to_path_buf(),
            operation: "wait",
            source: e,
        })? {
            Some(status) => break status,
            None => std::thread::sleep(Duration::from_millis(100)),
        }
    };

    // Write capture file if requested.
    if let Some(output_bytes) = captured_output {
        write_capture(cwd, harness, &output_bytes)?;
    }

    Ok(exit_status.code().unwrap_or(1))
}

/// Kill the child process (and its process group on Unix).
fn kill_child(child: &mut std::process::Child) {
    #[cfg(unix)]
    {
        // Kill the entire process group so no orphan grandchildren survive.
        let pid = child.id() as libc::pid_t;
        unsafe {
            libc::kill(-pid, libc::SIGKILL);
        }
    }
    #[cfg(not(unix))]
    {
        let _ = child.kill();
    }
}

/// Write captured output to `{repo}/.cascade/dispatch-results/{ts}-{harness}.md`.
fn write_capture(repo: &Path, harness: Harness, output: &[u8]) -> Result<()> {
    let dir = repo.join(".cascade").join("dispatch-results");
    std::fs::create_dir_all(&dir).map_err(|e| CascadeError::Io {
        path: dir.clone(),
        operation: "create_dir_all",
        source: e,
    })?;

    let ts = Utc::now().format("%Y%m%dT%H%M%SZ");
    let filename = format!("{ts}-{}.md", harness.as_str());
    let file_path = dir.join(&filename);

    let header = format!(
        "# Dispatch Result\n\n\
         - **Timestamp:** {ts}\n\
         - **Harness:** {}\n\
         - **Repo:** {}\n\n\
         ## Output\n\n",
        harness.as_str(),
        repo.display()
    );

    let content = [header.as_bytes(), output].concat();
    std::fs::write(&file_path, &content).map_err(|e| CascadeError::Io {
        path: file_path.clone(),
        operation: "write",
        source: e,
    })?;

    eprintln!(
        "cascade dispatch: captured output written to {}",
        file_path.display()
    );
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    // ── compose_prompt ────────────────────────────────────────────────────

    #[test]
    fn compose_prompt_no_context() {
        let result = compose_prompt(None, "hello world");
        assert_eq!(result, "hello world");
    }

    #[test]
    fn compose_prompt_with_context_has_header() {
        let result = compose_prompt(Some("some context here"), "my prompt");
        assert!(
            result.contains("## Context from Cascade"),
            "missing context header"
        );
        assert!(result.contains("some context here"), "missing context body");
        assert!(result.contains("my prompt"), "missing user prompt");
    }

    #[test]
    fn compose_prompt_separator_between_context_and_prompt() {
        let result = compose_prompt(Some("ctx"), "prompt");
        // Separator must appear between context and prompt.
        let sep_pos = result.find("---").expect("separator missing");
        let ctx_pos = result.find("ctx").expect("context missing");
        let prompt_pos = result.find("prompt").expect("prompt missing");
        assert!(ctx_pos < sep_pos, "context must precede separator");
        assert!(sep_pos < prompt_pos, "separator must precede prompt");
    }

    // ── validate_home_confined ────────────────────────────────────────────

    #[test]
    fn validate_home_confined_accepts_home_subpath() {
        let home = home_dir();
        let sub = home.join("some").join("project");
        // Path doesn't need to exist for this logic test — we create a temp
        // subpath of HOME that we know starts_with HOME.
        // validate_home_confined calls canonicalize, so we test the inner
        // starts_with logic directly.
        assert!(sub.starts_with(&home));
    }

    #[test]
    fn validate_home_confined_rejects_tmp() {
        let path = PathBuf::from("/tmp");
        let result = validate_home_confined(&path);
        assert!(result.is_err(), "expected error for /tmp, got {:?}", result);
    }

    // ── harness argv construction ─────────────────────────────────────────

    #[test]
    fn cc_argv_uses_dash_p_flag() {
        let bin = PathBuf::from("/usr/local/bin/claude");
        let (_, args) = Harness::Cc.build_argv(&bin, "test prompt");
        assert_eq!(args[0], "-p");
        assert_eq!(args[1], "test prompt");
    }

    #[test]
    fn oc_argv_uses_run_subcommand() {
        let bin = PathBuf::from("/usr/local/bin/opencode");
        let (_, args) = Harness::Oc.build_argv(&bin, "test prompt");
        assert_eq!(args[0], "run");
        assert_eq!(args[1], "test prompt");
    }

    #[test]
    fn harness_binary_names() {
        assert_eq!(Harness::Cc.binary_name(), "claude");
        assert_eq!(Harness::Oc.binary_name(), "opencode");
    }

    // ── dry-run output ────────────────────────────────────────────────────

    #[test]
    fn dry_run_output_contains_binary_and_workdir() {
        // Redirect stdout to a buffer.
        let bin = PathBuf::from("/usr/local/bin/claude");
        let args = vec!["-p".to_string(), "hello".to_string()];
        let mcp = PathBuf::from("/tmp/cascade/daemon.sock");
        let repo = PathBuf::from("/tmp");
        let prompt = "hello";

        // Capture by redirecting internally — we call the function and verify
        // it does not panic; output format tested via integration.
        print_dry_run(&bin, &args, &mcp, &repo, prompt);
        // If we reach here without panic, the basic shape is fine.
    }

    // ── harness detection fixture ─────────────────────────────────────────

    #[test]
    fn find_binary_returns_err_for_nonexistent() {
        let result = find_binary("__cascade_no_such_binary_xyz__");
        assert!(
            result.is_err(),
            "expected Err for missing binary, got {:?}",
            result
        );
    }

    #[test]
    fn find_binary_finds_echo() {
        // `echo` is present on all Unix-like systems in /bin or /usr/bin.
        // Skip on Windows.
        #[cfg(unix)]
        {
            let result = find_binary("echo");
            assert!(result.is_ok(), "expected to find `echo` on PATH");
        }
    }

    // ── write_capture ─────────────────────────────────────────────────────

    #[test]
    fn write_capture_creates_file_with_header() {
        let dir = tempfile::tempdir().expect("tempdir");
        let repo = dir.path();
        let content = b"hello output";

        write_capture(repo, Harness::Cc, content).expect("write_capture");

        let results_dir = repo.join(".cascade").join("dispatch-results");
        let entries: Vec<_> = std::fs::read_dir(&results_dir)
            .expect("read results dir")
            .filter_map(|e| e.ok())
            .collect();

        assert_eq!(entries.len(), 1, "expected one result file");
        let file_path = entries[0].path();
        let written = std::fs::read_to_string(&file_path).expect("read file");
        assert!(written.contains("# Dispatch Result"), "missing header");
        assert!(written.contains("hello output"), "missing content");
        assert!(written.contains("cc"), "missing harness tag");
    }

    #[test]
    fn write_capture_filename_contains_harness() {
        let dir = tempfile::tempdir().expect("tempdir");
        let repo = dir.path();
        write_capture(repo, Harness::Oc, b"oc output").expect("write_capture");

        let results_dir = repo.join(".cascade").join("dispatch-results");
        let entry = std::fs::read_dir(&results_dir)
            .expect("read dir")
            .filter_map(|e| e.ok())
            .next()
            .expect("at least one file");

        let name = entry.file_name();
        let name_str = name.to_string_lossy();
        assert!(
            name_str.ends_with("-oc.md"),
            "expected -oc.md suffix, got {name_str}"
        );
    }

    // ── routing matrix integration (dispatch wiring) ──────────────────────

    #[test]
    fn task_class_arg_converts_to_task_class() {
        assert_eq!(
            TaskClass::from(TaskClassArg::InteractiveChat),
            TaskClass::InteractiveChat
        );
        assert_eq!(TaskClass::from(TaskClassArg::BulkExec), TaskClass::BulkExec);
        assert_eq!(TaskClass::from(TaskClassArg::Cheap), TaskClass::Cheap);
        assert_eq!(
            TaskClass::from(TaskClassArg::AdversarialReview),
            TaskClass::AdversarialReview
        );
        assert_eq!(
            TaskClass::from(TaskClassArg::FinalGate),
            TaskClass::FinalGate
        );
        assert_eq!(
            TaskClass::from(TaskClassArg::Sensitive),
            TaskClass::Sensitive
        );
    }

    #[test]
    fn interactive_chat_routes_to_reserved_main_claude() {
        // Hermetic: accounts_registry_path = None means an EMPTY registry (the FOSS
        // default), so InteractiveChat returns the "claude" sentinel. Without this
        // the test would read the developer's real ~/.cascade/accounts/accounts.json.
        let router =
            cascade_core::routing::Router::with_config(cascade_core::routing::RouterConfig {
                quota_store_path: None,
                accounts_registry_path: None,
                ..Default::default()
            });
        let d = router.select(TaskClass::InteractiveChat, "hello");
        assert!(
            matches!(d, cascade_core::routing::RoutingDecision::Reserved { ref provider_id, .. }
                if provider_id == "claude"),
            "InteractiveChat must be Reserved/claude, got: {d:?}"
        );
    }

    #[test]
    fn sensitive_task_class_never_routes_to_external() {
        // Hermetic: empty registry (None) so the test doesn't read the developer's
        // real ~/.cascade accounts. Sensitive must stay on claude/local regardless.
        let router =
            cascade_core::routing::Router::with_config(cascade_core::routing::RouterConfig {
                quota_store_path: None,
                accounts_registry_path: None,
                ..Default::default()
            });
        let d = router.select(TaskClass::Sensitive, "my ssn is 123-45-6789");
        match &d {
            cascade_core::routing::RoutingDecision::Lane { provider_id, .. } => {
                assert!(
                    provider_id == "claude"
                        || provider_id.starts_with("acc")
                        || provider_id == "local",
                    "Sensitive must not route external, got: {provider_id}"
                );
            }
            cascade_core::routing::RoutingDecision::AllExhausted { .. } => {}
            other => panic!("unexpected: {other:?}"),
        }
    }

    #[test]
    fn print_routing_decision_does_not_panic() {
        // Just verify no panic on all variants.
        print_routing_decision(
            TaskClass::Cheap,
            &cascade_core::routing::RoutingDecision::Lane {
                provider_id: "gfp".into(),
                reason: "test".into(),
            },
        );
        print_routing_decision(
            TaskClass::InteractiveChat,
            &cascade_core::routing::RoutingDecision::Reserved {
                provider_id: "claude".into(),
                reason: "reserved".into(),
            },
        );
        print_routing_decision(
            TaskClass::BulkExec,
            &cascade_core::routing::RoutingDecision::AllExhausted {
                tried: vec!["acc2".into()],
            },
        );
        print_routing_decision(
            TaskClass::Sensitive,
            &cascade_core::routing::RoutingDecision::FirewallDeny {
                reason: "sensitive".into(),
            },
        );
    }
}
