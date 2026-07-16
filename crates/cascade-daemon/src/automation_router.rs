//! RegistryRouter — real `ProviderRouter` impl backed by `ProviderRegistry`.
//!
//! Purpose: wire the `cascade-providers` `ProviderRegistry` into the agent
//!   executor so `AutomationRunner` steps produce real LLM completions instead
//!   of the "nop" placeholder strings used during scaffolding.
//!
//! Inputs:
//!   - `Arc<ProviderRegistry>` — the shared provider registry, identical to
//!     what the HTTP chat handler and health-check task use.
//!   - `AgentSpec` + `AgentRunContext` — the agent identity and assembled
//!     conversation, forwarded by the executor on each step.
//!
//! Outputs:
//!   - `StepOutcome` — the provider's response mapped into the executor's type.
//!
//! Constraints:
//!   - **No-provider path is graceful**: if `pick_for_chat` returns `None`
//!     (registry empty or all providers unhealthy), the router returns
//!     `ExecutorError::ProviderFailed` with an explicit "no provider available"
//!     message rather than silently succeeding with fake output.
//!   - **Lazy**: the registry is `Arc`-shared; no new work happens at daemon
//!     startup. The first actual automation step triggers the first pick.
//!   - **Thread-safe**: `Arc<ProviderRegistry>` is `Send + Sync`; the router
//!     can be used concurrently from many executor workers.
//!   - **Tool invoker**: `SafeToolInvoker` handles the `ToolInvoker` side.
//!     All tool calls are returned as an explicit "not yet implemented" result
//!     so the model can gracefully handle the missing capability without the
//!     executor seeing a false success.
//!   - **Safety gate**: `SafetyGate` wraps any `ToolInvoker` (e.g.
//!     `SafeToolInvoker` or `LocalToolInvoker`) with three layers enforced
//!     BEFORE the wrapped invoker ever runs: (1) deny-list pattern matching
//!     mirroring the top-chat destructive-deny-list categories via
//!     `cascade_harness::policy::simple::SimplePolicyEvaluator`; (2) path
//!     sandboxing — every path-bearing argument must canonicalize (resolving
//!     symlinks) to a descendant of the resolved project-tier root; (3) a
//!     real, tamper-evident audit-log entry for every call via
//!     `crate::audit::record` (the genuine `cascade-audit` SHA-256 hash
//!     chain — never the fake `cascade_core::security::audit` module).
//!
//! SPORT: cascade-daemon / automation_router — auto-02, T-P7-E04-03

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::Arc;

use async_trait::async_trait;
use tracing::warn;

use cascade_agents::context::{AgentRunContext, ContextRole, StepOutcome, TokenUsage, ToolCall};
use cascade_agents::executor::{ExecutorError, ProviderRouter, ToolInvoker};
use cascade_agents::spec::AgentSpec;
use cascade_audit::AuditOp;
use cascade_core::sensitivity::path_is_personal_scope;
use cascade_harness::policy::engine::PolicyEvaluator;
use cascade_harness::policy::simple::SimplePolicyEvaluator;
use cascade_providers::{CompletionRequest, Message, MessageRole, ProviderRegistry};
use cascade_types::policy::PolicyAction;

// ── RegistryRouter ────────────────────────────────────────────────────────────

/// `ProviderRouter` implementation backed by the shared `ProviderRegistry`.
///
/// Converts each `(AgentSpec, AgentRunContext)` pair into a `CompletionRequest`
/// and dispatches it through `ProviderRegistry::pick_for_chat`.
///
/// # No-provider degradation
///
/// When the registry has no healthy provider, `step()` returns
/// `Err(ExecutorError::ProviderFailed("no provider available"))` so the
/// executor transitions the task to `Failed` and the user sees a clear
/// status rather than a silently wrong "nop" success.
pub struct RegistryRouter {
    registry: Arc<ProviderRegistry>,
}

impl RegistryRouter {
    /// Construct a `RegistryRouter` around the shared provider registry.
    pub fn new(registry: Arc<ProviderRegistry>) -> Self {
        Self { registry }
    }
}

#[async_trait]
impl ProviderRouter for RegistryRouter {
    /// Run one ReAct step by forwarding the assembled context to the best
    /// available provider.
    ///
    /// # Provider selection
    ///
    /// Uses `spec.model_pref` as the preferred provider ID when set, then
    /// falls back to `ProviderRegistry::pick_for_chat(None)` (routing-table
    /// Chat default → health-aware scan → local fallback).
    ///
    /// # Context mapping
    ///
    /// `AgentRunContext::messages` are mapped to `cascade_providers::Message`
    /// values.  `ContextRole::Tool` results are injected as `User` turns
    /// (OpenAI / Anthropic both accept tool results as user-role messages
    /// when there is no structured tool-call wire format in play).
    async fn step(
        &self,
        spec: &AgentSpec,
        ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError> {
        // ── Pick provider ─────────────────────────────────────────────────────
        let preferred = spec.model_pref.as_deref();
        let (adapter, provider_id) =
            self.registry
                .pick_for_chat(preferred)
                .await
                .ok_or_else(|| {
                    warn!(
                        agent_id = %spec.id,
                        "RegistryRouter: no provider available — registry empty or all unhealthy"
                    );
                    ExecutorError::ProviderFailed(
                        "no provider available: registry is empty or all providers are unhealthy"
                            .to_string(),
                    )
                })?;

        // ── Build CompletionRequest ───────────────────────────────────────────
        // Map ContextMessage → cascade_providers::Message.
        // ContextRole::Tool results become User-role messages so the model sees
        // the tool output as part of the conversation history.
        let messages: Vec<Message> = ctx
            .messages
            .iter()
            .map(|m| {
                let role = match m.role {
                    ContextRole::System => MessageRole::System,
                    ContextRole::User | ContextRole::Tool => MessageRole::User,
                    ContextRole::Assistant => MessageRole::Assistant,
                };
                Message {
                    role,
                    content: m.content.clone(),
                }
            })
            .collect();

        // Derive the model name: prefer the explicit model_pref (which may be a
        // full model string like "claude-sonnet-4-6"), then fall back to the
        // provider_id string. We do not call available_models() here (async,
        // potentially slow) — the provider will use its default model if the
        // model field is set to the provider_id.
        let model = spec
            .model_pref
            .clone()
            .unwrap_or_else(|| provider_id.clone());

        let req = CompletionRequest {
            model,
            messages,
            max_tokens: None,
            temperature: None,
            stream: false,
            system: None,
        };

        // ── Dispatch ──────────────────────────────────────────────────────────
        let resp = adapter
            .complete(req)
            .await
            .map_err(|e| ExecutorError::ProviderFailed(e.to_string()))?;

        Ok(StepOutcome {
            assistant_text: resp.content,
            tool_calls: vec![],
            done: true,
            usage: TokenUsage {
                prompt_tokens: resp.usage.prompt_tokens,
                completion_tokens: resp.usage.completion_tokens,
            },
        })
    }
}

// ── SafeToolInvoker ───────────────────────────────────────────────────────────

/// `ToolInvoker` that returns a clear "not implemented" result for all calls.
///
/// This is the correct placeholder for the tool dispatch layer until concrete
/// tool implementations land (P4+ scope). Unlike the old `NopInvoker` which
/// returned `"nop:<tool_id>"` (a silent false-success), `SafeToolInvoker`
/// returns an explicit error string so the model knows the tool is unavailable
/// and can adapt its reasoning accordingly.
///
/// The executor feeds tool results back to the model as user-role messages, so
/// an error string here is handled gracefully — the model sees "tool X returned
/// an error: not yet implemented" and can try a different approach.
pub struct SafeToolInvoker;

#[async_trait]
impl ToolInvoker for SafeToolInvoker {
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        // WHY Ok(error_string) not Err(): the executor feeds tool results back
        // into the next prompt turn. Returning Err here would park the task
        // with ToolFailed. Returning the error as a string lets the model see
        // the failure and decide how to proceed (try another tool, give up, etc.).
        Ok(format!(
            "[cascade] tool '{}' is not yet implemented in this build. \
             The tool call has been noted but no action was taken.",
            call.tool_id
        ))
    }
}

// ── LocalCapability ───────────────────────────────────────────────────────────

/// Daemon-side tool capability, gating what `LocalToolInvoker` may execute.
///
/// Mirrors the shape of `cascade_plugins::capability::Capability` (granted /
/// denied sets) without pulling in the WASM-plugin manifest machinery — the
/// daemon's `LocalToolInvoker` is a native, in-process invoker, not a
/// sandboxed plugin, so it needs its own lightweight parallel gate (per
/// T-P7-E04-01 guidance: "reuse CapabilitySet pattern or a parallel
/// daemon-side gate").
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum LocalCapability {
    /// Read files / directory listings (also required for grep/glob).
    FsRead,
    /// Write or create files.
    FsWrite,
    /// Execute a bash command.
    BashExec,
}

/// Grant set for `LocalToolInvoker`: which `LocalCapability` values are held.
///
/// **Deny-by-default**: a capability absent from `granted` resolves to
/// `Denied` — the same posture as `cascade_agents::grants::ToolGrant` and
/// `cascade_plugins::capability::CapabilitySet`.
#[derive(Debug, Clone, Default)]
pub struct LocalCapabilitySet {
    granted: HashSet<LocalCapability>,
}

impl LocalCapabilitySet {
    /// Construct an empty grant set (all capabilities denied).
    // Public API for granular grant construction (`::new().with(FsRead)`); the
    // daemon today only ever instantiates `::all()` (ipc_ceo.rs) or `::new()`
    // from tests, so the bin target sees no non-test caller — the allow keeps
    // strict clippy honest without weakening the API surface.
    #[allow(dead_code)]
    pub fn new() -> Self {
        Self::default()
    }

    /// Construct a grant set holding every `LocalCapability` — used for
    /// fully-trusted local automation contexts (e.g. CLI-driven runs where
    /// the human is already at the keyboard).
    pub fn all() -> Self {
        let mut granted = HashSet::new();
        granted.insert(LocalCapability::FsRead);
        granted.insert(LocalCapability::FsWrite);
        granted.insert(LocalCapability::BashExec);
        Self { granted }
    }

    /// Grant `cap`, returning `self` for chained construction.
    // Public API for granular grant construction; no caller in the daemon bin
    // today (only `::all()` is used in production), retained for future
    // capability-narrowing wiring.
    #[allow(dead_code)]
    pub fn with(mut self, cap: LocalCapability) -> Self {
        self.granted.insert(cap);
        self
    }

    /// Returns `true` if `cap` is held.
    pub fn has(&self, cap: LocalCapability) -> bool {
        self.granted.contains(&cap)
    }
}

// ── LocalToolInvoker ──────────────────────────────────────────────────────────

/// `ToolInvoker` that executes core tools natively: file read/write, bash
/// exec, and grep/glob search.
///
/// # Sensitivity firewall
///
/// Every path-bearing call is checked against
/// `cascade_core::sensitivity::path_is_personal_scope` (the same firewall
/// primitive the dispatch-guard uses, see `cascade-harness::policy::
/// sensitivity_guard`). Personal-scope paths (`~/Downloads` and equivalents)
/// are refused unless the invoker's capability set explicitly grants access
/// — `LocalToolInvoker` never silently reads/writes/execs into personal
/// scope on a bare grant.
///
/// # Capability gating
///
/// Each tool id maps to a `LocalCapability`; the call is denied before any
/// I/O happens if the capability is not held. This is independent of (and
/// runs after) the executor's own `ToolRegistry` grant check — defense in
/// depth, not a replacement.
///
/// # Supported tool ids
///
/// | tool_id | capability | args |
/// |---|---|---|
/// | `cascade.fs.read` | `FsRead` | `{ "path": string }` |
/// | `cascade.fs.write` | `FsWrite` | `{ "path": string, "content": string }` |
/// | `cascade.bash.exec` | `BashExec` | `{ "command": string }` |
/// | `cascade.search.grep` | `FsRead` | `{ "pattern": string, "path": string }` |
/// | `cascade.search.glob` | `FsRead` | `{ "pattern": string, "path": string }` |
pub struct LocalToolInvoker {
    capabilities: LocalCapabilitySet,
}

impl LocalToolInvoker {
    /// Construct a `LocalToolInvoker` with the given capability grants.
    pub fn new(capabilities: LocalCapabilitySet) -> Self {
        Self { capabilities }
    }

    /// Deny reason if `path` is in personal scope and `PersonalData`-equivalent
    /// access was not explicitly granted. `LocalCapabilitySet` has no
    /// dedicated personal-data capability (v1 scope is core tools only), so
    /// personal-scope paths are unconditionally refused — callers that need
    /// personal-scope access use the dedicated personal-data pipeline
    /// (`cascade-personal`), not the generic tool invoker.
    fn check_personal_scope(path: &Path, tool_id: &str) -> Result<(), ExecutorError> {
        if path_is_personal_scope(path) {
            return Err(ExecutorError::ToolFailed {
                tool_id: tool_id.to_string(),
                message: format!(
                    "path '{}' is in personal scope (~/Downloads); LocalToolInvoker refuses \
                     personal-scope access — use the cascade-personal pipeline instead",
                    path.display()
                ),
            });
        }
        Ok(())
    }

    fn require(&self, cap: LocalCapability, tool_id: &str) -> Result<(), ExecutorError> {
        if !self.capabilities.has(cap) {
            return Err(ExecutorError::ToolDenied {
                reason: format!(
                    "tool '{tool_id}' requires capability {cap:?}, which is not granted"
                ),
            });
        }
        Ok(())
    }

    fn arg_str<'a>(
        args: &'a serde_json::Value,
        key: &str,
        tool_id: &str,
    ) -> Result<&'a str, ExecutorError> {
        args.get(key)
            .and_then(|v| v.as_str())
            .ok_or_else(|| ExecutorError::ToolFailed {
                tool_id: tool_id.to_string(),
                message: format!("missing or non-string '{key}' argument"),
            })
    }

    async fn fs_read(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        self.require(LocalCapability::FsRead, &call.tool_id)?;
        let path_str = Self::arg_str(&call.args, "path", &call.tool_id)?;
        let path = PathBuf::from(path_str);
        Self::check_personal_scope(&path, &call.tool_id)?;
        tokio::fs::read_to_string(&path)
            .await
            .map_err(|e| ExecutorError::ToolFailed {
                tool_id: call.tool_id.clone(),
                message: format!("read '{}' failed: {e}", path.display()),
            })
    }

    async fn fs_write(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        self.require(LocalCapability::FsWrite, &call.tool_id)?;
        let path_str = Self::arg_str(&call.args, "path", &call.tool_id)?;
        let content = Self::arg_str(&call.args, "content", &call.tool_id)?;
        let path = PathBuf::from(path_str);
        Self::check_personal_scope(&path, &call.tool_id)?;
        tokio::fs::write(&path, content)
            .await
            .map_err(|e| ExecutorError::ToolFailed {
                tool_id: call.tool_id.clone(),
                message: format!("write '{}' failed: {e}", path.display()),
            })?;
        Ok(format!(
            "wrote {} bytes to {}",
            content.len(),
            path.display()
        ))
    }

    async fn bash_exec(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        self.require(LocalCapability::BashExec, &call.tool_id)?;
        let command = Self::arg_str(&call.args, "command", &call.tool_id)?.to_string();
        // SAFETY-GATE-AUDITED-SHELL-EXEC: this is the one deliberate shell
        // launcher in cascade-daemon. `bash_exec` exists to run an
        // AI-supplied shell command string (pipes/redirects/&&) on the
        // caller's behalf, which requires a real shell — an arg-array exec
        // cannot express that. The injection surface this opens is closed by
        // `SafetyGate`, which every `ToolInvoker` wrapping this router runs
        // through: deny-list pattern matching (`SimplePolicyEvaluator`) on
        // `command` happens BEFORE this function is ever reached (see the
        // `SafetyGate` doc comment below). Do not add a second shell
        // launcher anywhere else in this crate — route it through here so it
        // stays behind the same gate. See `no_shell_injection.rs` for the
        // static audit that enforces this file stays the sole exception.
        let output = tokio::process::Command::new("bash")
            .arg("-c")
            .arg(&command)
            .stdin(Stdio::null())
            .output()
            .await
            .map_err(|e| ExecutorError::ToolFailed {
                tool_id: call.tool_id.clone(),
                message: format!("spawn failed: {e}"),
            })?;

        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);
        if output.status.success() {
            Ok(stdout.into_owned())
        } else {
            Ok(format!(
                "[exit {}] stdout:\n{stdout}\nstderr:\n{stderr}",
                output.status.code().unwrap_or(-1)
            ))
        }
    }

    async fn search_grep(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        self.require(LocalCapability::FsRead, &call.tool_id)?;
        let pattern = Self::arg_str(&call.args, "pattern", &call.tool_id)?.to_string();
        let root = PathBuf::from(Self::arg_str(&call.args, "path", &call.tool_id)?);
        Self::check_personal_scope(&root, &call.tool_id)?;

        let re = regex::Regex::new(&pattern).map_err(|e| ExecutorError::ToolFailed {
            tool_id: call.tool_id.clone(),
            message: format!("invalid regex '{pattern}': {e}"),
        })?;

        let mut matches = Vec::new();
        for entry in walkdir::WalkDir::new(&root)
            .into_iter()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_type().is_file())
        {
            let Ok(text) = std::fs::read_to_string(entry.path()) else {
                continue; // binary or unreadable — skip, not an error
            };
            for (lineno, line) in text.lines().enumerate() {
                if re.is_match(line) {
                    matches.push(format!(
                        "{}:{}:{}",
                        entry.path().display(),
                        lineno + 1,
                        line
                    ));
                }
            }
        }
        Ok(matches.join("\n"))
    }

    async fn search_glob(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        self.require(LocalCapability::FsRead, &call.tool_id)?;
        let pattern = Self::arg_str(&call.args, "pattern", &call.tool_id)?.to_string();
        let root = PathBuf::from(Self::arg_str(&call.args, "path", &call.tool_id)?);
        Self::check_personal_scope(&root, &call.tool_id)?;

        let re =
            regex::Regex::new(&glob_to_regex(&pattern)).map_err(|e| ExecutorError::ToolFailed {
                tool_id: call.tool_id.clone(),
                message: format!("invalid glob pattern '{pattern}': {e}"),
            })?;

        let mut hits = Vec::new();
        for entry in walkdir::WalkDir::new(&root)
            .into_iter()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_type().is_file())
        {
            let name = entry.file_name().to_string_lossy();
            if re.is_match(&name) {
                hits.push(entry.path().display().to_string());
            }
        }
        Ok(hits.join("\n"))
    }
}

/// Convert a simple glob pattern (`*` / `?`) to an anchored regex.
///
/// Only the two common glob wildcards are supported — this is deliberately
/// minimal (no `**`, no brace expansion) since it only matches file *names*,
/// not full paths (directory traversal is handled by `WalkDir`).
fn glob_to_regex(glob: &str) -> String {
    let mut out = String::from("^");
    for c in glob.chars() {
        match c {
            '*' => out.push_str(".*"),
            '?' => out.push('.'),
            '.' | '+' | '(' | ')' | '[' | ']' | '{' | '}' | '^' | '$' | '|' | '\\' => {
                out.push('\\');
                out.push(c);
            }
            _ => out.push(c),
        }
    }
    out.push('$');
    out
}

#[async_trait]
impl ToolInvoker for LocalToolInvoker {
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        match call.tool_id.as_str() {
            "cascade.fs.read" => self.fs_read(call).await,
            "cascade.fs.write" => self.fs_write(call).await,
            "cascade.bash.exec" => self.bash_exec(call).await,
            "cascade.search.grep" => self.search_grep(call).await,
            "cascade.search.glob" => self.search_glob(call).await,
            other => Ok(format!(
                "[cascade] tool '{other}' is not a core LocalToolInvoker tool. \
                 The tool call has been noted but no action was taken."
            )),
        }
    }
}

// ── PathSandbox ────────────────────────────────────────────────────────────────

/// Enforces that every path-bearing tool argument resolves inside a project
/// tier root.
///
/// # Why canonicalize, not string-prefix match
///
/// A naive `starts_with(root)` check is defeated by a symlink inside the
/// sandbox that points outside it (`ln -s /etc/passwd ./escape`). This
/// sandbox instead calls [`std::fs::canonicalize`] on the joined path, which
/// resolves every symlink in the chain, then checks the *resolved* path is a
/// descendant of the (also canonicalized) root. A `..`-only traversal attempt
/// (e.g. `../../etc/passwd`) collapses under canonicalization the same way.
///
/// # Non-existent paths
///
/// `canonicalize` requires the path to exist. For write targets that do not
/// exist yet (creating a new file), the sandbox canonicalizes the deepest
/// existing ancestor directory and checks *that* is contained in the root —
/// this still catches `../` escapes and symlinked ancestor directories while
/// allowing legitimate new-file creation.
#[derive(Debug, Clone)]
pub struct PathSandbox {
    /// Canonicalized root of the resolved project tier. All checked paths
    /// must resolve to a descendant of this directory.
    root: PathBuf,
}

impl PathSandbox {
    /// Construct a sandbox rooted at `root`. `root` is canonicalized eagerly
    /// so every subsequent check compares against a symlink-resolved base.
    /// Falls back to the raw (non-canonicalized) path if `root` does not yet
    /// exist on disk — this only happens in tests/misconfiguration, and the
    /// containment check below still rejects the vast majority of escapes.
    pub fn new(root: impl Into<PathBuf>) -> Self {
        let root = root.into();
        let canonical = std::fs::canonicalize(&root).unwrap_or(root);
        Self { root: canonical }
    }

    /// Return `Ok(())` if `candidate` resolves inside the sandbox root, or
    /// `Err(reason)` describing why it was rejected.
    ///
    /// `candidate` may be relative (resolved against `root`) or absolute.
    pub fn check(&self, candidate: &Path) -> Result<(), String> {
        // Reject embedded NUL bytes outright — never a legitimate path.
        if candidate.to_string_lossy().contains('\0') {
            return Err("path contains a null byte".to_string());
        }

        let joined = if candidate.is_absolute() {
            candidate.to_path_buf()
        } else {
            self.root.join(candidate)
        };

        // Find the deepest existing ancestor (inclusive) to canonicalize —
        // this lets the sandbox validate write targets that don't exist yet.
        let mut probe = joined.clone();
        let resolved = loop {
            match std::fs::canonicalize(&probe) {
                Ok(resolved) => break resolved,
                Err(_) => {
                    let Some(parent) = probe.parent() else {
                        // Ran out of ancestors without finding one that
                        // exists (e.g. a bare relative path with no root) —
                        // fail closed.
                        return Err(format!(
                            "path '{}' has no resolvable ancestor",
                            joined.display()
                        ));
                    };
                    if parent == probe {
                        return Err(format!(
                            "path '{}' has no resolvable ancestor",
                            joined.display()
                        ));
                    }
                    probe = parent.to_path_buf();
                }
            }
        };

        // The resolved ancestor must itself be inside root, AND the
        // unresolved remainder (the part of `joined` beyond `probe`) must not
        // reintroduce `..` segments that could still escape once the missing
        // path components are created.
        if !resolved.starts_with(&self.root) {
            return Err(format!(
                "path '{}' resolves to '{}', which escapes the sandboxed \
                 project root '{}'",
                joined.display(),
                resolved.display(),
                self.root.display()
            ));
        }

        let remainder = joined.strip_prefix(&probe).unwrap_or(Path::new(""));
        if remainder
            .components()
            .any(|c| matches!(c, std::path::Component::ParentDir))
        {
            return Err(format!(
                "path '{}' contains a `..` segment past its resolved ancestor \
                 — rejected as a potential traversal escape",
                joined.display()
            ));
        }

        Ok(())
    }
}

// ── SafetyGate ───────────────────────────────────────────────────────────────

/// `ToolInvoker` decorator enforcing deny-list + path-sandbox + real audit
/// logging around any inner `ToolInvoker`.
///
/// # Layer order (fail-closed, cheapest first)
///
/// 1. **Deny-list**: the call is serialized to a `PolicyAction` and run
///    through `SimplePolicyEvaluator` (injection-resilient literal matching —
///    base64/URL-encoding expansion, chained-command splitting, the same
///    categories as the top-chat destructive deny-list: filesystem, git,
///    database, infra, publish, secrets). A `Decision::Deny` short-circuits
///    before the inner invoker runs.
/// 2. **Path sandbox**: every string-valued argument under a path-shaped key
///    (`path`, `file`, `dir`, `cwd`) is checked against `PathSandbox`. Any
///    escape short-circuits before the inner invoker runs.
/// 3. **Audit**: a real `cascade-audit` entry (`AuditOp::Other("tool_invoke")`)
///    is recorded via `crate::audit::record` for every call — allowed AND
///    denied — with `actor` set to the agent id and `target` set to the
///    tool id, so denials are forensically visible too.
///
/// Only after all three layers pass does the wrapped `inner` invoker run.
pub struct SafetyGate<I: ToolInvoker> {
    inner: Arc<I>,
    sandbox: PathSandbox,
    evaluator: SimplePolicyEvaluator,
    /// Identity recorded as `actor` in every audit entry (e.g. `"cascade-daemon"`).
    actor: String,
}

/// Argument keys treated as path-bearing for sandbox enforcement. Deliberately
/// broad (over-inclusive) — a false-positive sandbox check on a non-path
/// string argument merely rejects a tool call that shouldn't have used one of
/// these key names; a false-negative would be a real escape.
const PATH_ARG_KEYS: &[&str] = &["path", "file", "dir", "cwd", "target", "root"];

impl<I: ToolInvoker> SafetyGate<I> {
    /// Wrap `inner` with deny-list + sandbox + audit enforcement.
    ///
    /// `project_root` anchors the `PathSandbox`; `actor` is the identity
    /// recorded in every audit entry.
    pub fn new(inner: Arc<I>, project_root: impl Into<PathBuf>, actor: impl Into<String>) -> Self {
        Self {
            inner,
            sandbox: PathSandbox::new(project_root),
            evaluator: SimplePolicyEvaluator,
            actor: actor.into(),
        }
    }

    /// Deny-list check: serialize `call` into a `PolicyAction` and run it
    /// through `SimplePolicyEvaluator`. Returns the deny reason on Deny.
    fn check_deny_list(&self, call: &ToolCall) -> Result<(), String> {
        let action = PolicyAction::new(
            call.tool_id.clone(),
            serde_json::json!({ "tool_id": call.tool_id, "args": call.args }),
        );
        let result = self.evaluator.evaluate(&action);
        if result.decision == cascade_types::policy::Decision::Deny {
            return Err(result.reason);
        }
        Ok(())
    }

    /// Path-sandbox check: scan `call.args` for any string value under a
    /// path-shaped key and verify it resolves inside the sandbox root.
    fn check_sandbox(&self, call: &ToolCall) -> Result<(), String> {
        let serde_json::Value::Object(map) = &call.args else {
            return Ok(());
        };
        for key in PATH_ARG_KEYS {
            if let Some(serde_json::Value::String(raw)) = map.get(*key) {
                self.sandbox.check(Path::new(raw))?;
            }
        }
        Ok(())
    }

    /// Record a real, tamper-evident audit entry for this call via the
    /// process-global `cascade-audit` log. Non-fatal by the contract of
    /// `crate::audit::record` — an audit-log failure never blocks or masks
    /// the safety decision itself.
    fn audit(&self, call: &ToolCall, decision: &str, reason: &str) {
        crate::audit::record(
            AuditOp::Other("tool_invoke".to_string()),
            &self.actor,
            &call.tool_id,
            &format!(
                "decision={decision} call_id={} reason={reason}",
                call.call_id
            ),
        );
    }
}

#[async_trait]
impl<I: ToolInvoker> ToolInvoker for SafetyGate<I> {
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        // ── Layer 1: deny-list ───────────────────────────────────────────────
        if let Err(reason) = self.check_deny_list(call) {
            self.audit(call, "deny_pattern", &reason);
            return Err(ExecutorError::ToolDenied { reason });
        }

        // ── Layer 2: path sandbox ────────────────────────────────────────────
        if let Err(reason) = self.check_sandbox(call) {
            self.audit(call, "deny_sandbox", &reason);
            return Err(ExecutorError::ToolDenied { reason });
        }

        // ── Layer 3: audit the allowed call, then dispatch ───────────────────
        self.audit(call, "allow", "");
        self.inner.invoke(call).await
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    use std::pin::Pin;
    use std::sync::Arc;

    use async_trait::async_trait;
    use futures_util::Stream;

    use cascade_agents::context::{
        AgentRunContextBuilder, ContextMessage, ContextRole, NoopContextProvider,
    };
    use cascade_agents::spec::{AgentRole, AgentSpec, Capability, Runtime};
    use cascade_agents::tool_registry::ToolRegistry;
    use cascade_providers::{
        CompletionResponse, ModelInfo, ProviderAdapter, ProviderCapabilities, ProviderError,
        ProviderInfo, StreamChunk, TokenUsage as PTokenUsage,
    };
    use cascade_types::agent::Tier;

    // ── MockProvider ─────────────────────────────────────────────────────────

    /// A mock `ProviderAdapter` that returns a fixed string from `complete()`.
    struct MockProvider {
        response: String,
    }

    impl MockProvider {
        fn new(response: impl Into<String>) -> Self {
            Self {
                response: response.into(),
            }
        }
    }

    #[async_trait]
    impl ProviderAdapter for MockProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Ok(CompletionResponse {
                content: self.response.clone(),
                model: "mock-model".into(),
                usage: PTokenUsage {
                    prompt_tokens: 10,
                    completion_tokens: 5,
                    total_tokens: 15,
                },
                cost_usd: None,
            })
        }

        async fn complete_stream(
            &self,
            _req: CompletionRequest,
        ) -> Result<
            Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
            ProviderError,
        > {
            Err(ProviderError::NotImplemented)
        }

        async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
            Ok(vec![ModelInfo {
                id: "mock-model".into(),
                name: "Mock Model".into(),
                context_window: 4096,
                supports_streaming: false,
            }])
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: "mock".into(),
                name: "Mock Provider".into(),
                auth_method: cascade_providers::AuthMethod::ApiKey,
                base_url: "http://mock.example.com".into(),
                capabilities: ProviderCapabilities {
                    supports_streaming: false,
                    supports_vision: false,
                    max_context_tokens: 4096,
                    supports_function_calling: false,
                },
            }
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            Ok(())
        }
    }

    // ── Helper: build a minimal AgentSpec ────────────────────────────────────

    fn mock_spec() -> AgentSpec {
        AgentSpec {
            id: "test-agent".into(),
            version: "0.1.0".into(),
            name: "Test Agent".into(),
            role: AgentRole::Generic,
            tier: Tier::T2,
            capabilities: vec![Capability::LlmCall],
            model_pref: None,
            system_prompt_ref: None,
            tool_grants_ref: None,
            runtime: Runtime::Native,
            soul_ref: None,
        }
    }

    // ── Helper: build a minimal AgentRunContext ───────────────────────────────

    async fn mock_ctx(spec: AgentSpec) -> AgentRunContext {
        AgentRunContextBuilder::new(spec.clone(), "say hello")
            .context_provider(Arc::new(NoopContextProvider))
            .tool_registry(Arc::new(ToolRegistry::new()), &spec.id)
            .session_id("test-sess-01")
            .build()
            .await
    }

    // ── Test: real provider output flows through (not "nop") ─────────────────

    /// Verifies that a one-step automation run via `RegistryRouter` returns
    /// the mock provider's text, NOT the old "nop" placeholder.
    ///
    /// This is the critical regression guard for auto-02: if the production
    /// runner ever regresses to NopRouter the assertion `!= "nop"` catches it.
    #[tokio::test]
    async fn registry_router_returns_provider_output_not_nop() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "mock".into(),
                Arc::new(MockProvider::new("hello from real provider")),
            )
            .expect("register mock provider");

        let router = RegistryRouter::new(Arc::clone(&registry));
        let spec = mock_spec();
        let ctx = mock_ctx(spec.clone()).await;

        let outcome = router.step(&spec, &ctx).await.expect("step should succeed");

        assert_eq!(
            outcome.assistant_text, "hello from real provider",
            "RegistryRouter must return the provider's actual output"
        );
        assert_ne!(
            outcome.assistant_text, "nop",
            "RegistryRouter must NOT return the old nop placeholder"
        );
        assert!(outcome.done, "single-step should be marked done");
        assert_eq!(outcome.usage.prompt_tokens, 10);
        assert_eq!(outcome.usage.completion_tokens, 5);
    }

    // ── Test: no-provider path returns clear error ────────────────────────────

    /// Verifies that an empty registry returns a clear `ProviderFailed` error
    /// rather than panicking or returning a fake success.
    #[tokio::test]
    async fn registry_router_no_provider_returns_clear_error() {
        let registry = Arc::new(ProviderRegistry::new()); // empty
        let router = RegistryRouter::new(registry);
        let spec = mock_spec();
        let ctx = mock_ctx(spec.clone()).await;

        let err = router
            .step(&spec, &ctx)
            .await
            .expect_err("should fail with no provider");

        match err {
            ExecutorError::ProviderFailed(msg) => {
                assert!(
                    msg.contains("no provider available"),
                    "error message should mention 'no provider available', got: {msg}"
                );
            }
            other => panic!("expected ProviderFailed, got {other:?}"),
        }
    }

    // ── Test: SafeToolInvoker returns explicit error string, not "nop:" ───────

    #[tokio::test]
    async fn safe_tool_invoker_returns_explicit_not_implemented_message() {
        let invoker = SafeToolInvoker;
        let call = ToolCall {
            tool_id: "cascade.read_file".into(),
            args: serde_json::json!({"path": "/tmp/foo"}),
            call_id: "call-001".into(),
        };

        let result = invoker.invoke(&call).await.expect("should return Ok");
        assert!(
            result.contains("not yet implemented"),
            "SafeToolInvoker must say 'not yet implemented', got: {result}"
        );
        assert!(
            !result.starts_with("nop:"),
            "SafeToolInvoker must NOT return 'nop:' prefixed output, got: {result}"
        );
    }

    // ── Test: context messages are forwarded to provider ─────────────────────

    /// Verifies that `ContextMessage` values in `AgentRunContext` are passed
    /// through to the completion request (not silently dropped).
    #[tokio::test]
    async fn registry_router_forwards_context_messages() {
        use std::sync::Mutex;

        struct CapturingProvider {
            captured: Mutex<Option<CompletionRequest>>,
        }

        #[async_trait]
        impl ProviderAdapter for CapturingProvider {
            async fn complete(
                &self,
                req: CompletionRequest,
            ) -> Result<CompletionResponse, ProviderError> {
                *self.captured.lock().unwrap() = Some(req);
                Ok(CompletionResponse {
                    content: "captured".into(),
                    model: "cap-model".into(),
                    usage: PTokenUsage {
                        prompt_tokens: 1,
                        completion_tokens: 1,
                        total_tokens: 2,
                    },
                    cost_usd: None,
                })
            }

            async fn complete_stream(
                &self,
                _req: CompletionRequest,
            ) -> Result<
                Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
                ProviderError,
            > {
                Err(ProviderError::NotImplemented)
            }

            async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
                Ok(vec![ModelInfo {
                    id: "cap-model".into(),
                    name: "Capturing Model".into(),
                    context_window: 4096,
                    supports_streaming: false,
                }])
            }

            fn provider_info(&self) -> ProviderInfo {
                ProviderInfo {
                    id: "capturing".into(),
                    name: "Capturing Provider".into(),
                    auth_method: cascade_providers::AuthMethod::ApiKey,
                    base_url: "http://capturing.example.com".into(),
                    capabilities: ProviderCapabilities {
                        supports_streaming: false,
                        supports_vision: false,
                        max_context_tokens: 4096,
                        supports_function_calling: false,
                    },
                }
            }

            async fn health_check(&self) -> Result<(), ProviderError> {
                Ok(())
            }
        }

        let provider = Arc::new(CapturingProvider {
            captured: Mutex::new(None),
        });
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "capturing".into(),
                Arc::clone(&provider) as Arc<dyn ProviderAdapter + Send + Sync>,
            )
            .expect("register");

        let router = RegistryRouter::new(Arc::clone(&registry));
        let spec = mock_spec();

        // Build a context that already has a user message pre-loaded.
        let mut ctx = mock_ctx(spec.clone()).await;
        ctx.messages.push(ContextMessage {
            role: ContextRole::User,
            content: "what is 2+2?".into(),
        });

        let _ = router.step(&spec, &ctx).await.expect("step ok");

        let captured = provider
            .captured
            .lock()
            .unwrap()
            .clone()
            .expect("should have captured a request");
        assert!(
            captured
                .messages
                .iter()
                .any(|m| m.content == "what is 2+2?"),
            "context messages must be forwarded to the provider"
        );
    }

    // ── LocalToolInvoker tests ────────────────────────────────────────────────

    fn call(tool_id: &str, args: serde_json::Value) -> ToolCall {
        ToolCall {
            tool_id: tool_id.into(),
            args,
            call_id: "call-local-001".into(),
        }
    }

    #[tokio::test]
    async fn local_invoker_fs_write_then_read_round_trips() {
        let dir = tempfile::tempdir().expect("tempdir");
        let path = dir.path().join("hello.txt");

        let invoker = LocalToolInvoker::new(LocalCapabilitySet::all());

        let write_call = call(
            "cascade.fs.write",
            serde_json::json!({"path": path.to_string_lossy(), "content": "hello world"}),
        );
        let write_result = invoker
            .invoke(&write_call)
            .await
            .expect("write should succeed");
        assert!(write_result.contains("wrote"), "got: {write_result}");

        let read_call = call(
            "cascade.fs.read",
            serde_json::json!({"path": path.to_string_lossy()}),
        );
        let read_result = invoker
            .invoke(&read_call)
            .await
            .expect("read should succeed");
        assert_eq!(read_result, "hello world");
    }

    #[tokio::test]
    async fn local_invoker_bash_exec_returns_stdout() {
        let invoker = LocalToolInvoker::new(LocalCapabilitySet::all());
        let c = call(
            "cascade.bash.exec",
            serde_json::json!({"command": "echo cascade-test"}),
        );
        let result = invoker.invoke(&c).await.expect("bash exec should succeed");
        assert!(result.contains("cascade-test"), "got: {result}");
    }

    #[tokio::test]
    async fn local_invoker_grep_finds_matching_line() {
        let dir = tempfile::tempdir().expect("tempdir");
        std::fs::write(dir.path().join("a.txt"), "alpha\nneedle here\nomega\n")
            .expect("write fixture");

        let invoker = LocalToolInvoker::new(LocalCapabilitySet::all());
        let c = call(
            "cascade.search.grep",
            serde_json::json!({"pattern": "needle", "path": dir.path().to_string_lossy()}),
        );
        let result = invoker.invoke(&c).await.expect("grep should succeed");
        assert!(result.contains("needle here"), "got: {result}");
    }

    #[tokio::test]
    async fn local_invoker_glob_finds_matching_filename() {
        let dir = tempfile::tempdir().expect("tempdir");
        std::fs::write(dir.path().join("target.rs"), "").expect("write fixture");
        std::fs::write(dir.path().join("other.txt"), "").expect("write fixture");

        let invoker = LocalToolInvoker::new(LocalCapabilitySet::all());
        let c = call(
            "cascade.search.glob",
            serde_json::json!({"pattern": "*.rs", "path": dir.path().to_string_lossy()}),
        );
        let result = invoker.invoke(&c).await.expect("glob should succeed");
        assert!(result.contains("target.rs"), "got: {result}");
        assert!(!result.contains("other.txt"), "got: {result}");
    }

    #[tokio::test]
    async fn local_invoker_denies_ungranted_capability() {
        // Empty capability set: every call must be denied before any I/O.
        let invoker = LocalToolInvoker::new(LocalCapabilitySet::new());
        let c = call(
            "cascade.fs.read",
            serde_json::json!({"path": "/tmp/should-not-be-touched"}),
        );

        let err = invoker.invoke(&c).await.expect_err("should be denied");
        match err {
            ExecutorError::ToolDenied { reason } => {
                assert!(
                    reason.contains("FsRead"),
                    "reason should name the capability, got: {reason}"
                );
            }
            other => panic!("expected ToolDenied, got {other:?}"),
        }
    }

    #[tokio::test]
    async fn local_invoker_refuses_personal_scope_path_even_with_capability() {
        let invoker = LocalToolInvoker::new(LocalCapabilitySet::all());
        let home = std::env::var("HOME").unwrap_or_else(|_| "/Users/test".into());
        let personal_path = format!("{home}/Downloads/secret.txt");

        let c = call(
            "cascade.fs.read",
            serde_json::json!({"path": personal_path}),
        );
        let err = invoker
            .invoke(&c)
            .await
            .expect_err("personal scope must be refused");
        match err {
            ExecutorError::ToolFailed { message, .. } => {
                assert!(
                    message.contains("personal scope"),
                    "message should explain the personal-scope refusal, got: {message}"
                );
            }
            other => panic!("expected ToolFailed, got {other:?}"),
        }
    }

    #[tokio::test]
    async fn local_invoker_unknown_tool_is_explicit_not_a_core_tool() {
        let invoker = LocalToolInvoker::new(LocalCapabilitySet::all());
        let c = call("cascade.unknown.tool", serde_json::json!({}));
        let result = invoker
            .invoke(&c)
            .await
            .expect("unknown tool returns Ok, not Err");
        assert!(
            result.contains("not a core LocalToolInvoker tool"),
            "got: {result}"
        );
    }

    // ── SafetyGate / PathSandbox tests (T-P7-E04-03) ──────────────────────────
    //
    // Adversarial coverage for the three-layer safety gate: deny-list,
    // path sandbox (symlink-resolving canonicalize), and audit-log recording.
    // Each SafetyGate test uses a unique `call_id` so it can grep its own audit
    // entry out of the process-global log even when tests run in parallel.

    /// Mock inner `ToolInvoker` that records every call so SafetyGate tests can
    /// assert "inner invoker never ran" on denials. Returns a fixed echo string
    /// so allow-path tests can also assert the call flowed through.
    #[derive(Default)]
    struct RecordingInvoker {
        calls: std::sync::Mutex<Vec<String>>,
    }

    #[async_trait]
    impl ToolInvoker for RecordingInvoker {
        async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
            self.calls.lock().unwrap().push(call.call_id.clone());
            Ok(format!("echo:{}", call.call_id))
        }
    }

    /// Process-global audit-log initialiser for the SafetyGate test block. First
    /// `init()` wins process-wide; later callers reuse the active path. The
    /// tempdir is intentionally leaked so the backing file outlives the call.
    static AUDIT_LOG_INIT: std::sync::Once = std::sync::Once::new();

    fn ensure_audit_log_initialised() {
        AUDIT_LOG_INIT.call_once(|| {
            let dir = tempfile::tempdir().expect("audit tempdir");
            let path = dir.path().join("audit.log");
            let _ = crate::audit::init(&path);
            std::mem::forget(dir);
        });
    }

    /// Returns true when the global audit log contains one line carrying both
    /// `decision=<decision>` and `call_id=<call_id>`. Reads the active log path
    /// from `crate::audit::active_log_path` so any test in the binary that beat
    /// us to `init()` is honoured.
    fn audit_log_records(call_id: &str, decision: &str) -> bool {
        ensure_audit_log_initialised();
        let Some(path) = crate::audit::active_log_path() else {
            return false;
        };
        let Ok(content) = std::fs::read_to_string(path) else {
            return false;
        };
        let needle_decision = format!("decision={decision}");
        let needle_call = format!("call_id={call_id}");
        content
            .lines()
            .any(|line| line.contains(&needle_decision) && line.contains(&needle_call))
    }

    // (a) deny-list rejection — `rm -rf /` must be blocked, audited as
    // "deny_pattern", and must never reach the inner invoker.
    #[tokio::test]
    async fn safety_gate_denies_dangerous_bash_and_records_deny_pattern() {
        // Must init the audit log BEFORE invoking the gate: record() is
        // non-fatal-when-uninitialised (silently drops the write), so
        // initialising lazily inside audit_log_records() after invoke()
        // already ran would check an empty log that never saw the write.
        ensure_audit_log_initialised();
        let dir = tempfile::tempdir().expect("tempdir");
        let inner = Arc::new(RecordingInvoker::default());
        let gate = SafetyGate::new(Arc::clone(&inner), dir.path(), "safety-gate-test");

        let c = ToolCall {
            tool_id: "cascade.bash.exec".into(),
            args: serde_json::json!({"command": "rm -rf /"}),
            call_id: "safety-deny-pattern-001".into(),
        };

        let err = gate
            .invoke(&c)
            .await
            .expect_err("deny-list must reject rm -rf /");
        match err {
            ExecutorError::ToolDenied { reason } => {
                assert!(!reason.is_empty(), "deny reason should be non-empty");
            }
            other => panic!("expected ToolDenied, got {other:?}"),
        }

        assert!(
            inner.calls.lock().unwrap().is_empty(),
            "inner invoker must NOT run on deny-list rejection"
        );
        assert!(
            audit_log_records("safety-deny-pattern-001", "deny_pattern"),
            "audit log should record decision=deny_pattern for safety-deny-pattern-001"
        );
    }

    // (b) path-traversal rejection — writing to an absolute path outside the
    // sandbox root must be rejected at layer 2 with "deny_sandbox" and never
    // reach the inner invoker.
    #[tokio::test]
    async fn safety_gate_rejects_absolute_path_outside_sandbox() {
        ensure_audit_log_initialised();
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let outside = tempfile::tempdir().expect("outside tempdir");
        let outside_file = outside.path().join("passwd");
        std::fs::write(&outside_file, "").expect("write outside fixture");

        let inner = Arc::new(RecordingInvoker::default());
        let gate = SafetyGate::new(Arc::clone(&inner), sandbox.path(), "safety-gate-test");

        let c = ToolCall {
            tool_id: "cascade.fs.write".into(),
            args: serde_json::json!({
                "path": outside_file.to_string_lossy(),
                "content": "pwned",
            }),
            call_id: "safety-traversal-abs-001".into(),
        };

        let err = gate
            .invoke(&c)
            .await
            .expect_err("absolute outside path must be denied");
        match err {
            ExecutorError::ToolDenied { reason } => {
                assert!(
                    reason.contains("escapes"),
                    "reason should mention sandbox escape, got: {reason}"
                );
            }
            other => panic!("expected ToolDenied, got {other:?}"),
        }

        assert!(
            inner.calls.lock().unwrap().is_empty(),
            "inner invoker must NOT run on path-traversal denial"
        );
        assert!(
            audit_log_records("safety-traversal-abs-001", "deny_sandbox"),
            "audit log should record decision=deny_sandbox for safety-traversal-abs-001"
        );
    }

    // (c) symlink escape — a symlink inside the sandbox pointing at an outside
    // file must be rejected because PathSandbox canonicalizes the symlink
    // target before checking containment.
    #[cfg(unix)]
    #[tokio::test]
    async fn safety_gate_rejects_symlink_escape_via_canonicalize() {
        ensure_audit_log_initialised();
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let outside_dir = tempfile::tempdir().expect("outside tempdir");
        let outside_file = outside_dir.path().join("secret.txt");
        std::fs::write(&outside_file, "secrets").expect("write outside fixture");

        let symlink_inside = sandbox.path().join("escape");
        std::os::unix::fs::symlink(&outside_file, &symlink_inside)
            .expect("create symlink inside sandbox pointing outside");

        let inner = Arc::new(RecordingInvoker::default());
        let gate = SafetyGate::new(Arc::clone(&inner), sandbox.path(), "safety-gate-test");

        let c = ToolCall {
            tool_id: "cascade.fs.write".into(),
            args: serde_json::json!({
                "path": symlink_inside.to_string_lossy(),
                "content": "pwned",
            }),
            call_id: "safety-symlink-001".into(),
        };

        let err = gate
            .invoke(&c)
            .await
            .expect_err("symlink escape must be denied");
        match err {
            ExecutorError::ToolDenied { reason } => {
                assert!(
                    reason.contains("escapes"),
                    "reason should mention escape through symlink, got: {reason}"
                );
            }
            other => panic!("expected ToolDenied, got {other:?}"),
        }

        assert!(
            inner.calls.lock().unwrap().is_empty(),
            "inner invoker must NOT run on symlink escape"
        );
        assert!(
            audit_log_records("safety-symlink-001", "deny_sandbox"),
            "audit log should record decision=deny_sandbox for safety-symlink-001"
        );
    }

    // (d) legitimate in-sandbox call — passes all three layers, reaches the
    // inner invoker, and is audited as "allow".
    #[tokio::test]
    async fn safety_gate_allows_in_sandbox_call_and_records_allow() {
        ensure_audit_log_initialised();
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let inside = sandbox.path().join("hello.txt");
        std::fs::write(&inside, "hi").expect("write in-sandbox fixture");

        let inner = Arc::new(RecordingInvoker::default());
        let gate = SafetyGate::new(Arc::clone(&inner), sandbox.path(), "safety-gate-test");

        let c = ToolCall {
            tool_id: "cascade.fs.read".into(),
            args: serde_json::json!({"path": inside.to_string_lossy()}),
            call_id: "safety-allow-001".into(),
        };

        let result = gate.invoke(&c).await.expect("legitimate call should pass");
        assert!(
            result.contains("echo:safety-allow-001"),
            "inner invoker should have run and produced its echo, got: {result}"
        );
        assert_eq!(
            inner.calls.lock().unwrap().len(),
            1,
            "inner invoker should be called exactly once"
        );
        assert!(
            audit_log_records("safety-allow-001", "allow"),
            "audit log should record decision=allow for safety-allow-001"
        );
    }

    // (e) null-byte path rejection — PathSandbox fails fast on embedded NUL
    // bytes before any canonicalize work, and SafetyGate surfaces that as
    // ToolDenied without reaching the inner invoker.
    #[tokio::test]
    async fn safety_gate_rejects_null_byte_path() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let inner = Arc::new(RecordingInvoker::default());
        let gate = SafetyGate::new(Arc::clone(&inner), sandbox.path(), "safety-gate-test");

        let c = ToolCall {
            tool_id: "cascade.fs.write".into(),
            args: serde_json::json!({
                "path": "safe\0/evil",
                "content": "x",
            }),
            call_id: "safety-nullbyte-001".into(),
        };

        let err = gate
            .invoke(&c)
            .await
            .expect_err("null-byte path must be rejected");
        match err {
            ExecutorError::ToolDenied { reason } => {
                assert!(
                    reason.contains("null byte"),
                    "reason should mention the null byte, got: {reason}"
                );
            }
            other => panic!("expected ToolDenied, got {other:?}"),
        }
        assert!(
            inner.calls.lock().unwrap().is_empty(),
            "inner invoker must NOT run on null-byte path"
        );
    }

    // ── PathSandbox unit tests ───────────────────────────────────────────────

    #[test]
    fn path_sandbox_rejects_absolute_path_outside_root() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let outside = tempfile::tempdir().expect("outside tempdir");
        let outside_file = outside.path().join("passwd");
        std::fs::write(&outside_file, "").expect("write fixture");

        let s = PathSandbox::new(sandbox.path());
        let err = s.check(&outside_file).unwrap_err();
        assert!(err.contains("escapes"), "got: {err}");
    }

    #[test]
    fn path_sandbox_rejects_relative_traversal_to_outside() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let s = PathSandbox::new(sandbox.path());
        // `../../etc/passwd` — `/etc/passwd` exists on Unix and lives well
        // outside any tempdir sandbox root.
        let err = s
            .check(Path::new("../../etc/passwd"))
            .expect_err("relative traversal must be rejected");
        assert!(
            err.contains("escapes") || err.contains(".."),
            "should flag traversal, got: {err}"
        );
    }

    #[test]
    fn path_sandbox_allows_existing_path_inside_root() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let sub = sandbox.path().join("sub");
        std::fs::create_dir_all(&sub).expect("mkdir");
        let inside = sub.join("child.txt");
        std::fs::write(&inside, "").expect("write fixture");

        let s = PathSandbox::new(sandbox.path());
        s.check(&inside).expect("in-sandbox path should pass");
    }

    #[test]
    fn path_sandbox_allows_nonexistent_write_target_inside_root() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let s = PathSandbox::new(sandbox.path());
        // New file that does not yet exist — PathSandbox canonicalizes the
        // deepest existing ancestor (the sandbox root) and allows the write.
        let new_file = sandbox.path().join("brand-new.txt");
        s.check(&new_file)
            .expect("non-existent in-sandbox target should pass");
    }

    #[cfg(unix)]
    #[test]
    fn path_sandbox_rejects_symlink_escape() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let outside_dir = tempfile::tempdir().expect("outside tempdir");
        let outside_file = outside_dir.path().join("secret.txt");
        std::fs::write(&outside_file, "x").expect("write outside fixture");

        let symlink = sandbox.path().join("escape");
        std::os::unix::fs::symlink(&outside_file, &symlink)
            .expect("create symlink inside sandbox pointing outside");

        let s = PathSandbox::new(sandbox.path());
        let err = s.check(&symlink).unwrap_err();
        assert!(err.contains("escapes"), "got: {err}");
    }

    #[test]
    fn path_sandbox_rejects_null_byte_immediately() {
        let sandbox = tempfile::tempdir().expect("sandbox tempdir");
        let s = PathSandbox::new(sandbox.path());
        let err = s.check(Path::new("foo\0bar")).unwrap_err();
        assert!(err.contains("null byte"), "got: {err}");
    }
}
