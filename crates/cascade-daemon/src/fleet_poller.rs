//! Fleet poller — periodic multi-source quota aggregation loop (E-P6-03 v1.2).
//!
//! Purpose: Runs every `interval_secs` (default 60 s). Each registered
//! [`FleetSource`] is polled for a [`QuotaState`] snapshot. Non-`None`
//! results are aggregated via [`aggregate_quota`] and written atomically
//! to `~/.cascade/quota-store.json` via [`write_quota_store`].
//!
//! Inputs:
//!   - `config` — `[fleet]` section from `config.toml` (interval, enabled).
//!   - `config_dir` — daemon config directory (e.g. `~/.cascade/`).
//!   - `shutdown` — cancellation token; the loop exits cleanly on cancel.
//!
//! Outputs: `~/.cascade/quota-store.json` refreshed on each tick.
//!
//! Constraints:
//!   - `ClaudeMaxSource::poll()` always returns `None` by design; it is
//!     superseded by the async live-usage path in
//!     `FleetPoller::fetch_and_cache_claude_usage` (see that fn + the
//!     `ClaudeMaxSource` doc comment below), which writes
//!     `~/.claude/usage-cache.json` before `write_quota_json` merges it.
//!   - `CodexSource` and `AgySource` remain honestly inert (`poll()` →
//!     `None`) — see each struct's doc comment for exactly what real flow
//!     is (Codex: not implemented anywhere yet) or is not (Agy: no
//!     credential bridge exists in this codebase) available to wire up.
//!   - `GfpSource` reads the existing `quota-state.json` written by the
//!     `gfp`-feature quota poller.  If the file is absent or stale (>120 s),
//!     it returns `None`.
//!   - No `unwrap()` outside `#[cfg(test)]` blocks.
//!   - File ≤ 300 lines.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — fleet_poller (E-P6-03 v1.2)

use std::path::PathBuf;

use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use cascade_core::accounts_store::{
    accounts_dir, count_gfp_keys, detect_cli, read_accounts_registry, write_accounts_registry,
    write_quota_json,
};
use cascade_core::external_accounts::{discover as discover_external, ExternalAgent};
use cascade_core::read_claude_access_token;
use cascade_core::quota_aggregator::aggregate_quota;
use cascade_core::quota_store::{write_quota_store, QUOTA_STORE_SCHEMA_VERSION};
use cascade_types::accounts::{AccessMethod, AccountFamily};
use cascade_types::quota_store::{
    QuotaState, QuotaStore, PROVIDER_CLAUDE_MAX, PROVIDER_GFP, PROVIDER_GOOGLE_AGY,
    PROVIDER_OPENAI_CODEX,
};

use crate::claude_usage::fetch_claude_usage;
use crate::config::FleetConfig;

// ── Trait ─────────────────────────────────────────────────────────────────────

/// A single quota data source in the fleet.
///
/// Purpose: object-safe abstraction over heterogeneous quota backends
/// (Claude Max, Codex, Google Agy, GFP). Implementations are polled by
/// [`FleetPoller`] on each tick and must complete synchronously.
///
/// Inputs: the source's own configuration and credential state.
/// Outputs: `Some(QuotaState)` when a fresh reading is available;
///          `None` when the source is unconfigured or its data is stale.
/// Constraints: must be `Send + Sync`; must not panic; must complete quickly
///              (no blocking network I/O in the default implementations).
pub trait FleetSource: Send + Sync {
    /// Stable identifier for this source (matches a `PROVIDER_*` constant).
    ///
    /// Used in log messages and as the `provider` field of emitted snapshots.
    // Trait method — implemented by all FleetSource structs; called by the poller loop.
    #[allow(dead_code)]
    fn provider_id(&self) -> &str;

    /// Poll for a current quota snapshot.
    ///
    /// Returns `None` when no data is available (unconfigured, stale, or
    /// provider not yet implemented).
    fn poll(&self) -> Option<QuotaState>;
}

// ── Stub implementations ──────────────────────────────────────────────────────

/// Inert `FleetSource` for Anthropic Claude Max subscription accounts.
///
/// **This trait impl is intentionally always `None` — it is NOT where real
/// Claude Max quota comes from.** The real per-account flow already exists
/// and runs on every poll tick, but it bypasses the `FleetSource` trait
/// entirely: see [`FleetPoller::fetch_and_cache_claude_usage`], which
/// (1) discovers every Claude config dir via
/// `cascade_core::external_accounts::discover()`, (2) reads/refreshes each
/// account's access token via `read_claude_access_token` (keychain on macOS,
/// `.credentials.json` elsewhere), (3) calls `fetch_claude_usage` against the
/// live Anthropic usage API, and (4) writes the results to
/// `~/.claude/usage-cache.json`, which `write_quota_json` then merges into
/// `quota.json` independently of this trait's `poll()`.
///
/// `ClaudeMaxSource` itself stays a no-op `FleetSource` so the provider is
/// still registered/enumerable in the generic source list (e.g. for any
/// future code that iterates `sources` expecting one entry per provider)
/// without duplicating or racing the real fetch path above. Fabricating a
/// number here would either duplicate that live fetch or drift from it.
pub struct ClaudeMaxSource;

impl FleetSource for ClaudeMaxSource {
    fn provider_id(&self) -> &str {
        PROVIDER_CLAUDE_MAX
    }

    /// Always `None` by design — see the struct-level doc comment for the
    /// real Claude Max quota flow (`fetch_and_cache_claude_usage`).
    fn poll(&self) -> Option<QuotaState> {
        None
    }
}

/// Inert `FleetSource` for OpenAI Codex accounts.
///
/// Purpose: registers the Codex provider slot in the fleet source list.
/// Returns `None` on every poll — no fabricated numbers.
///
/// Real quota flow: unlike Claude Max, Codex has **no live usage-fetch path
/// implemented yet** anywhere in this crate. `cascade_core::external_accounts`
/// already discovers Codex's `auth.json` credential dir (`ExternalAgent::Codex`)
/// for auth/re-auth status purposes, but nothing currently calls an OpenAI
/// usage/quota endpoint with that token. Wiring a real `CodexSource` requires:
/// (1) an OpenAI usage API call analogous to `fetch_claude_usage`, and
/// (2) a decision on whether Codex quota is even exposed by an API OpenAI
/// publishes for CLI/subscription accounts (unconfirmed as of this fix).
/// Until that lands, this source stays honestly inert rather than guessing.
pub struct CodexSource;

impl FleetSource for CodexSource {
    fn provider_id(&self) -> &str {
        PROVIDER_OPENAI_CODEX
    }

    /// Always `None` — no Codex usage-fetch path exists yet (see doc comment).
    fn poll(&self) -> Option<QuotaState> {
        None
    }
}

/// Inert `FleetSource` for Google AI (agy) accounts.
///
/// Purpose: registers the Agy provider slot in the fleet source list.
/// Returns `None` on every poll — no fabricated numbers.
///
/// Real quota flow: **does not exist yet.** Unlike Claude and Codex, Agy has
/// no credential-bridge counterpart in `cascade_core::external_accounts`
/// (that module's `ExternalAgent` enum only has `Claude` and `Codex` variants
/// — confirmed by inspection, not merely undocumented). The only Agy-related
/// code elsewhere in the daemon (`AccessMethod::AgyCli` in
/// `cascade_types::accounts` / `fleet_poller::refresh_accounts`) only checks
/// **whether the `agy` CLI binary is present on `$PATH`** via `detect_cli`
/// — it does not read a token or call any quota endpoint. There is no
/// existing "agy-token" read path anywhere in this codebase to reuse, so
/// implementing a real `AgySource` is not trivially doable from existing
/// code; it would require net-new credential discovery (locate agy's auth
/// storage) and a net-new Google AI quota API call. Left inert + documented
/// rather than fabricated.
pub struct AgySource;

impl FleetSource for AgySource {
    fn provider_id(&self) -> &str {
        PROVIDER_GOOGLE_AGY
    }

    /// Always `None` — no Agy credential bridge or quota API call exists yet
    /// anywhere in this codebase (see doc comment).
    fn poll(&self) -> Option<QuotaState> {
        None
    }
}

// ── GFP real implementation ───────────────────────────────────────────────────

/// Source that reads the existing GFP `quota-state.json` written by the
/// `gfp`-feature quota poller.
///
/// Purpose: bridge the existing single-source GFP poller output into the
/// multi-source fleet aggregator without requiring a separate network call.
/// Inputs: path to `~/.cascade/quota-state.json`.
/// Outputs: `Some(QuotaState)` when the file exists and is fresh (<120 s old);
///          `None` otherwise.
/// Constraints: synchronous file read — must not block the async runtime
///              for more than a few milliseconds.
pub struct GfpSource {
    /// Path to `quota-state.json` produced by the GFP quota poller.
    state_path: PathBuf,
}

impl GfpSource {
    /// Construct a `GfpSource` that reads `state_path`.
    ///
    /// Inputs: `state_path` — absolute path to `~/.cascade/quota-state.json`.
    pub fn new(state_path: PathBuf) -> Self {
        Self { state_path }
    }
}

impl FleetSource for GfpSource {
    fn provider_id(&self) -> &str {
        PROVIDER_GFP
    }

    /// Read and deserialise `quota-state.json` if present and fresh.
    ///
    /// Staleness threshold: 120 seconds. Returns `None` on I/O or parse error.
    fn poll(&self) -> Option<QuotaState> {
        let meta = std::fs::metadata(&self.state_path).ok()?;
        let modified = meta.modified().ok()?;
        let age = std::time::SystemTime::now()
            .duration_since(modified)
            .unwrap_or(std::time::Duration::MAX);
        if age.as_secs() > 120 {
            return None;
        }
        let bytes = std::fs::read(&self.state_path).ok()?;
        let state: QuotaState = serde_json::from_slice(&bytes).ok()?;
        Some(state)
    }
}

// ── Poller ────────────────────────────────────────────────────────────────────

/// Periodic fleet polling loop.
///
/// Purpose: tick every `config.interval_secs`, poll all registered
/// [`FleetSource`]s, aggregate the non-`None` snapshots, and write the
/// result to `~/.cascade/quota-store.json`.
/// Inputs: `FleetConfig`, `config_dir`, and a `CancellationToken`.
/// Outputs: side-effect — `quota-store.json` updated on each successful tick.
/// Constraints: never panics; logs info on success, warn on errors; exits
///              cleanly when `shutdown` fires.
pub struct FleetPoller;

impl FleetPoller {
    /// Run the fleet polling loop.
    ///
    /// Constructs the default source list (one stub per provider + GFP reader),
    /// then loops at `config.interval_secs` until `shutdown` fires.
    ///
    /// Inputs:
    ///   - `config` — `[fleet]` section (interval_secs).
    ///   - `config_dir` — daemon config directory; `quota-state.json` is
    ///     expected here and `quota-store.json` is written here.
    ///   - `shutdown` — cancellation token; the loop exits cleanly on cancel.
    ///
    /// Outputs: logs; `quota-store.json` updated on each tick.
    ///
    /// Constraints: this function is `async` and must be spawned via `tokio::spawn`.
    pub async fn run(config: FleetConfig, config_dir: PathBuf, shutdown: CancellationToken) {
        let state_path = config_dir.join("quota-state.json");
        let store_path = config_dir.join("quota-store.json");

        let sources: Vec<Box<dyn FleetSource>> = vec![
            Box::new(ClaudeMaxSource),
            Box::new(CodexSource),
            Box::new(AgySource),
            Box::new(GfpSource::new(state_path)),
        ];

        let interval =
            tokio::time::Duration::from_secs(config.interval_secs.max(1));

        info!(interval_secs = config.interval_secs, "fleet poller started");

        loop {
            tokio::select! {
                _ = tokio::time::sleep(interval) => {}
                _ = shutdown.cancelled() => {
                    info!("fleet poller: shutdown signal received — exiting");
                    break;
                }
            }

            Self::tick_async(&sources, &store_path).await;
        }

        info!("fleet poller stopped");
    }

    /// Async tick: fetch live Claude usage, then run the synchronous quota
    /// source aggregation pass.
    ///
    /// Inputs: `sources` — registered fleet sources; `store_path` — destination.
    /// Outputs: `quota.json` + `quota-store.json` updated.
    async fn tick_async(sources: &[Box<dyn FleetSource>], store_path: &std::path::Path) {
        // Refresh accounts/ CLI availability, fetch live Claude usage, and write
        // quota.json.  This must run before the quota-store aggregation so the
        // widget has fresh data even when no synchronous source has returned data.
        let accts_path = accounts_dir().join("accounts.json");
        if accts_path.exists() {
            Self::refresh_accounts(&accts_path).await;
        }
        Self::tick(sources, store_path);
    }

    /// Execute one polling tick across all synchronous sources and write the result.
    ///
    /// Inputs: `sources` — registered fleet sources; `store_path` — destination.
    /// Outputs: `quota-store.json` written.
    /// Constraints: synchronous — must not be called from an async context
    ///              without `spawn_blocking` if sources perform blocking I/O.
    ///              `refresh_accounts` (async) has already run before this call.
    fn tick(sources: &[Box<dyn FleetSource>], store_path: &std::path::Path) {
        let snapshots: Vec<QuotaState> = sources
            .iter()
            .filter_map(|src| {
                let result = src.poll();
                if result.is_none() {
                    // Trace-level: absence of data from a stub is expected.
                }
                result
            })
            .collect();

        if snapshots.is_empty() {
            // No sources returned data; write an empty store so the file always
            // exists and downstream readers do not need to handle a missing file.
            let empty = QuotaStore {
                schema_version: QUOTA_STORE_SCHEMA_VERSION,
                updated_at: chrono::Utc::now().to_rfc3339(),
                accounts: vec![],
                week_totals: std::collections::HashMap::new(),
                month_totals: std::collections::HashMap::new(),
                rolling_history: vec![],
            };
            match write_quota_store(store_path, &empty) {
                Ok(()) => info!(path = %store_path.display(), "fleet: wrote empty quota-store.json (no sources ready)"),
                Err(e) => warn!(%e, "fleet: failed to write quota-store.json"),
            }
            return;
        }

        let store = aggregate_quota(&snapshots, 168); // 168 = 7 days × 24 hourly ticks
        match write_quota_store(store_path, &store) {
            Ok(()) => info!(
                accounts = store.accounts.len(),
                path = %store_path.display(),
                "fleet: quota-store.json updated"
            ),
            Err(e) => warn!(%e, "fleet: failed to write quota-store.json"),
        }
    }

    /// Refresh `cli_available` and `key_count` in an existing `accounts/accounts.json`,
    /// fetch live Claude usage for each discovered Claude account, and atomically
    /// regenerate `accounts/quota.json` for the native widget.
    ///
    /// Purpose: keeps the registry and widget data in sync with the current PATH state
    /// on each daemon tick without requiring the user to run `cascade accounts detect`.
    /// Live Claude usage is fetched via `api.anthropic.com/api/oauth/usage` for each
    /// account whose token is valid (or after a `claude -p` refresh attempt if expired).
    /// Fetched usage is written to `~/.claude/usage-cache.json` so `write_quota_json`
    /// picks it up via its existing merge path.
    ///
    /// Inputs:  `path` — existing `accounts/accounts.json` file.
    /// Outputs: updated `accounts.json` + `quota.json` on success; warn log on error.
    /// Constraints: async; bounded per-account (10s network timeout + 45s refresh);
    ///              never overwrites existing usage with zeros on failure.
    async fn refresh_accounts(path: &std::path::Path) {
        let mut registry = match read_accounts_registry(path) {
            Ok(r) => r,
            Err(e) => {
                warn!(%e, "fleet: failed to read accounts.json for refresh");
                return;
            }
        };

        let gfp_count = count_gfp_keys();

        for acc in &mut registry.accounts {
            if acc.family == AccountFamily::Gfp {
                acc.key_count = gfp_count;
            } else {
                // Re-detect CLI availability from the access methods.
                acc.cli_available = acc.access_methods.iter().any(|m| match m {
                    AccessMethod::NativeCc => detect_cli("claude"),
                    AccessMethod::SmithersClaudeP => {
                        detect_cli("smithers") || detect_cli("claude-p")
                    }
                    AccessMethod::CodexCli => detect_cli("codex"),
                    AccessMethod::AgyCli => detect_cli("agy"),
                    AccessMethod::OpencodeRun => detect_cli("opencode"),
                    AccessMethod::GfpKeypool => true,
                });
            }
        }

        registry.updated_at = chrono::Utc::now().to_rfc3339();

        match write_accounts_registry(path, &registry) {
            Ok(()) => info!(path = %path.display(), "fleet: accounts.json refreshed"),
            Err(e) => warn!(%e, "fleet: failed to write accounts.json"),
        }

        // ── Live Claude usage fetch ────────────────────────────────────────────
        // Discover all Claude config dirs, fetch live usage for each valid token,
        // and write results to ~/.claude/usage-cache.json so write_quota_json
        // picks them up via its existing merge path.
        Self::fetch_and_cache_claude_usage().await;

        // Refresh quota.json (same directory as accounts.json).
        if let Some(dir) = path.parent() {
            let quota_path = dir.join("quota.json");
            match write_quota_json(&quota_path, &registry) {
                Ok(()) => info!(path = %quota_path.display(), "fleet: quota.json refreshed"),
                Err(e) => warn!(%e, "fleet: failed to write quota.json"),
            }
        }
    }

    /// Discover all Claude config dirs, fetch live usage for each, and write
    /// the results to `~/.claude/usage-cache.json` in the shape that
    /// `write_quota_json` already merges.
    ///
    /// For accounts with an expired token: attempt a `claude -p "ok"` refresh
    /// (bounded to 45 s), then re-read the token.
    ///
    /// On per-account failure: log a warning, do NOT write zeros — the prior
    /// cache entry is preserved (we only write when we have real data).
    ///
    /// Outputs: atomically overwrites `~/.claude/usage-cache.json` with an
    ///          `{"accounts":[...]}` array containing all successfully-fetched
    ///          usage entries.
    async fn fetch_and_cache_claude_usage() {
        let home = match dirs::home_dir() {
            Some(h) => h,
            None => {
                warn!("fleet: cannot determine home dir for usage cache write");
                return;
            }
        };

        let cache_path = home.join(".claude").join("usage-cache.json");

        // Load the existing cache so we can preserve entries for accounts that
        // fail this round (never overwrite with zeros).
        let mut existing: Vec<serde_json::Value> = (|| -> Option<Vec<serde_json::Value>> {
            let bytes = std::fs::read(&cache_path).ok()?;
            let v: serde_json::Value = serde_json::from_slice(&bytes).ok()?;
            v.get("accounts")?.as_array().cloned()
        })()
        .unwrap_or_default();

        let external_accounts = discover_external();
        let now_epoch = chrono::Utc::now().timestamp() as f64;

        for ext in &external_accounts {
            if ext.agent != ExternalAgent::Claude {
                continue;
            }

            // Derive the account_id label (e.g. "claude", "claude-acc1", "claude-acc2").
            let account_id = label_to_account_id(&ext.label);

            // Try to get a valid access token; attempt refresh if expired.
            //
            // `Some(token)`  → usable token, proceed to fetch.
            // `None` with `definitive_auth_failure = true`  → the refresh ran and
            //   produced a definitive auth failure (token still dead): write an
            //   http_401 marker so write_quota_json flags the account.
            // `None` with `definitive_auth_failure = false` → transient (refresh
            //   timed out / spawn error, or no token entry at all): skip and
            //   preserve any prior-good cache entry.
            let mut definitive_auth_failure = false;
            let access_token: Option<String> = match read_claude_access_token(&ext.config_dir) {
                Some((tok, true)) => Some(tok),
                Some((_, false)) => {
                    // Token expired — attempt claude -p refresh (bounded).
                    info!(account = %account_id, "fleet: token expired, attempting refresh via claude -p");
                    if Self::refresh_token_via_claude_p(&ext.config_dir).await {
                        // Refresh process ran to completion (definitive result).
                        match read_claude_access_token(&ext.config_dir) {
                            Some((tok, true)) => Some(tok),
                            _ => {
                                // claude -p ran but the token is STILL invalid →
                                // the refresh token is revoked: a definitive 401.
                                warn!(account = %account_id, "fleet: token still invalid after refresh — definitive auth failure");
                                definitive_auth_failure = true;
                                None
                            }
                        }
                    } else {
                        // Refresh timed out / spawn error — transient, not auth-dead.
                        warn!(account = %account_id, "fleet: claude -p refresh inconclusive (transient) — preserving prior cache");
                        None
                    }
                }
                None => {
                    // No keychain entry at all — cannot conclude auth-dead; skip.
                    None
                }
            };

            let access_token = match access_token {
                Some(tok) => tok,
                None => {
                    if definitive_auth_failure {
                        // Write a definitive auth-failure marker (usage null) so the
                        // widget renders the re-auth prompt instead of stale data.
                        let entry = serde_json::json!({
                            "account":      account_id,
                            "provider":     "claude",
                            "usage":        serde_json::Value::Null,
                            "last_pull_at": now_epoch,
                            "quota_opaque": false,
                            "last_error":   "http_401"
                        });
                        upsert_cache_entry(&mut existing, account_id, entry);
                    }
                    // Transient → preserve prior entry untouched.
                    continue;
                }
            };

            // Fetch live usage.
            match fetch_claude_usage(&access_token).await {
                Some(usage) => {
                    info!(account = %account_id, "fleet: live Claude usage fetched successfully");
                    // Upsert into existing cache: replace matching entry or append.
                    let entry = serde_json::json!({
                        "account":      account_id,
                        "provider":     "claude",
                        "usage":        usage,
                        "last_pull_at": now_epoch,
                        "quota_opaque": false,
                        "last_error":   serde_json::Value::Null
                    });
                    upsert_cache_entry(&mut existing, account_id, entry);
                }
                None => {
                    // Transport/HTTP error or error envelope — transient. Do NOT
                    // overwrite a prior-good entry with zeros; preserve it.
                    warn!(account = %account_id, "fleet: live usage fetch returned None — preserving prior cache");
                }
            }
        }

        // Write the merged cache atomically.
        let payload = serde_json::json!({ "accounts": existing });
        match serde_json::to_vec_pretty(&payload) {
            Ok(bytes) => {
                let tmp = cache_path.with_extension("tmp");
                if let Err(e) = std::fs::write(&tmp, &bytes) {
                    warn!(%e, "fleet: failed to write usage-cache.json.tmp");
                    return;
                }
                if let Err(e) = std::fs::rename(&tmp, &cache_path) {
                    warn!(%e, "fleet: failed to rename usage-cache.json");
                }
            }
            Err(e) => warn!(%e, "fleet: failed to serialize usage cache"),
        }
    }

    /// Attempt a token refresh by running `CLAUDE_CONFIG_DIR=<dir> claude -p "ok"`.
    ///
    /// Bounded to 45 seconds.  Returns `true` if the process exits successfully
    /// (exit code 0 or 1 — both mean Claude ran and potentially refreshed the
    /// token; only a timeout or spawn failure returns `false`).
    ///
    /// This mirrors the proven refresh path used by the external bash poller and
    /// `cascade-reauth`: Claude Code itself refreshes the OAuth token on startup.
    async fn refresh_token_via_claude_p(config_dir: &std::path::Path) -> bool {
        let dir_str = config_dir.to_string_lossy().into_owned();
        let claude_bin = resolve_claude_bin();
        let result = tokio::time::timeout(
            std::time::Duration::from_secs(45),
            tokio::task::spawn_blocking(move || {
                // Skip MCP-server / plugin / settings startup so a heavy config
                // (e.g. ~/.claude with many MCP servers) does NOT hang for 45 s —
                // these flags make a dead token return its 401 in seconds.
                std::process::Command::new(&claude_bin)
                    .args([
                        "-p",
                        "ok",
                        "--model",
                        "claude-haiku-4-5",
                        "--strict-mcp-config",
                        "--mcp-config",
                        r#"{"mcpServers":{}}"#,
                        "--setting-sources",
                        "",
                    ])
                    .env("CLAUDE_CONFIG_DIR", &dir_str)
                    .stdout(std::process::Stdio::null())
                    .stderr(std::process::Stdio::null())
                    .status()
            }),
        )
        .await;

        match result {
            Ok(Ok(Ok(_status))) => true, // process ran (status 0 or non-zero both OK for our purposes)
            Ok(Ok(Err(e))) => {
                warn!(%e, "fleet: claude -p refresh: spawn error");
                false
            }
            Ok(Err(e)) => {
                warn!(%e, "fleet: claude -p refresh: join error");
                false
            }
            Err(_) => {
                warn!("fleet: claude -p refresh: 45 s timeout exceeded");
                false
            }
        }
    }
}

// ── Free helpers ──────────────────────────────────────────────────────────────

/// Convert an external account `label` (e.g. `"claude"`, `"claude-acc1"`) to
/// the account_id string used in `usage-cache.json` and `quota.json`.
///
/// The external account label is the directory name without its leading dot
/// (e.g. `.claude` → `"claude"`, `.claude-acc1` → `"claude-acc1"`).
/// This matches the `acc.id` strings in `accounts.json`.
fn label_to_account_id(label: &str) -> &str {
    label
}

/// Resolve the `claude` CLI binary path.
///
/// Under launchd the daemon inherits a minimal PATH that usually excludes
/// Homebrew, so a bare `claude` spawn fails with ENOENT and the refresh probe
/// is wrongly treated as a transient error (no re-auth flag). Probe the common
/// install locations and fall back to a bare `claude` for PATH-based lookup.
fn resolve_claude_bin() -> std::path::PathBuf {
    if let Some(home) = dirs::home_dir() {
        for c in [
            std::path::PathBuf::from("/opt/homebrew/bin/claude"),
            std::path::PathBuf::from("/usr/local/bin/claude"),
            home.join(".local/bin/claude"),
            home.join("bin/claude"),
        ] {
            if c.is_file() {
                return c;
            }
        }
    }
    std::path::PathBuf::from("claude")
}

/// Upsert a usage-cache entry: if an entry with the given `account_id` exists,
/// replace it; otherwise append it.
///
/// Inputs:  `entries` — mutable reference to the existing cache array;
///          `account_id` — the account's string id;
///          `entry` — the new JSON entry to insert.
fn upsert_cache_entry(
    entries: &mut Vec<serde_json::Value>,
    account_id: &str,
    entry: serde_json::Value,
) {
    if let Some(pos) = entries.iter().position(|e| {
        e.get("account").and_then(|v| v.as_str()) == Some(account_id)
    }) {
        entries[pos] = entry;
    } else {
        entries.push(entry);
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::quota_store::ModelUsage;
    use std::collections::HashMap;
    use tempfile::TempDir;

    fn make_snapshot(account_id: &str) -> QuotaState {
        let mut models = HashMap::new();
        models.insert(
            "test-model".to_string(),
            ModelUsage {
                used: 1_000,
                limit: Some(10_000),
                reset_at: None,
                pct_used: Some(10.0),
                cost_usd: None,
            },
        );
        QuotaState {
            account_id: account_id.to_string(),
            harness: "cc".to_string(),
            provider: PROVIDER_GFP.to_string(),
            ts: chrono::Utc::now().timestamp(),
            models,
        }
    }

    struct MockSource {
        id: String,
        snapshot: Option<QuotaState>,
    }

    impl FleetSource for MockSource {
        fn provider_id(&self) -> &str {
            &self.id
        }
        fn poll(&self) -> Option<QuotaState> {
            self.snapshot.clone()
        }
    }

    /// Inject a mock source that returns a snapshot, tick once, verify
    /// `quota-store.json` exists and is valid JSON with `schema_version`.
    #[test]
    fn fleet_poller_writes_store_on_tick() {
        let dir = TempDir::new().expect("tempdir");
        let store_path = dir.path().join("quota-store.json");

        let sources: Vec<Box<dyn FleetSource>> = vec![Box::new(MockSource {
            id: "mock-provider".to_string(),
            snapshot: Some(make_snapshot("mock-acct1")),
        })];

        FleetPoller::tick(&sources, &store_path);

        assert!(store_path.exists(), "quota-store.json must exist after tick");

        let bytes = std::fs::read(&store_path).expect("read store");
        let store: QuotaStore = serde_json::from_slice(&bytes).expect("valid JSON");
        assert_eq!(store.schema_version, QUOTA_STORE_SCHEMA_VERSION);
        assert_eq!(store.accounts.len(), 1);
        assert_eq!(store.accounts[0].account_id, "mock-acct1");
    }

    /// Stub sources (ClaudeMax, Codex, Agy) must all return None from poll().
    #[test]
    fn stub_sources_return_none() {
        let cm = ClaudeMaxSource;
        let cx = CodexSource;
        let ag = AgySource;

        assert!(cm.poll().is_none(), "ClaudeMaxSource must return None");
        assert!(cx.poll().is_none(), "CodexSource must return None");
        assert!(ag.poll().is_none(), "AgySource must return None");
    }

    /// Dynamic dispatch via `Box<dyn FleetSource>` must work correctly.
    #[test]
    fn fleet_source_trait_dispatch() {
        let sources: Vec<Box<dyn FleetSource>> = vec![
            Box::new(ClaudeMaxSource),
            Box::new(CodexSource),
            Box::new(AgySource),
            Box::new(MockSource {
                id: "mock".to_string(),
                snapshot: Some(make_snapshot("dyn-acct")),
            }),
        ];

        let results: Vec<Option<QuotaState>> = sources.iter().map(|s| s.poll()).collect();

        // First three are stubs → None; last one has data → Some.
        assert!(results[0].is_none());
        assert!(results[1].is_none());
        assert!(results[2].is_none());
        assert!(results[3].is_some());
        assert_eq!(
            results[3].as_ref().unwrap().account_id,
            "dyn-acct"
        );
    }
}
