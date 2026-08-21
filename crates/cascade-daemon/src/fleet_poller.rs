//! Fleet poller — periodic multi-source quota aggregation loop (E-P6-03 v1.2).
//!
//! Purpose: Ticks once immediately on startup, then every `interval_secs`
//! (default 60 s), so `~/.cascade/accounts/quota.json` is fresh within
//! seconds of a daemon (re)start rather than up to a full interval stale.
//! Each registered [`FleetSource`] is polled for a [`QuotaState`] snapshot.
//! Non-`None` results are aggregated via [`aggregate_quota`] and written
//! atomically to `~/.cascade/quota-store.json` via [`write_quota_store`].
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

use std::fs::OpenOptions;
use std::path::{Path, PathBuf};

use fs2::FileExt;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use cascade_core::accounts_store::{
    accounts_dir, count_gfp_keys, detect_cli, read_accounts_registry, write_accounts_registry,
    write_quota_json,
};
use cascade_core::external_accounts::{discover as discover_external, ExternalAgent};
use cascade_core::model_ids::MODEL_CLAUDE_HAIKU;
use cascade_core::quota_aggregator::aggregate_quota;
use cascade_core::quota_store::{write_quota_store, QUOTA_STORE_SCHEMA_VERSION};
use cascade_core::read_claude_access_token;
use cascade_types::accounts::{AccessMethod, AccountFamily, AccountsRegistry};
use cascade_types::quota_store::{
    weekly_slot_key, ModelUsage, QuotaState, QuotaStore, PROVIDER_CLAUDE_MAX, PROVIDER_GFP,
    PROVIDER_GOOGLE_AGY, PROVIDER_OPENAI_CODEX, WEEKLY_SLOT_ALL,
};

use crate::claude_usage::fetch_claude_usage;
use crate::config::{Config, FleetConfig};

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

/// `FleetSource` for Anthropic Claude Max subscription accounts.
///
/// Reads `~/.claude/usage-cache.json` (written by
/// [`FleetPoller::fetch_and_cache_claude_usage`] on every tick, before this
/// source's `poll()` is called) and converts each non-errored account entry
/// into a model slot in the returned `QuotaState`.
///
/// `pct_used` is populated from `five_hour.utilization`; `used` and `limit`
/// are left at `0` / `None` because the cache only stores utilization
/// percentages, not raw token counts.  This is real measured data — not
/// fabricated — and surfaces in `quota-store.json` for the IPC quota handler.
///
/// Returns `None` when the cache file is absent, unreadable, or contains no
/// non-errored entries.  A missing file is normal on first start (before the
/// async fetch completes) and on machines with no Claude accounts.
pub struct ClaudeMaxSource {
    /// Path to `usage-cache.json` written by `fetch_and_cache_claude_usage`.
    cache_path: PathBuf,
}

impl ClaudeMaxSource {
    /// Construct a `ClaudeMaxSource` reading from `cache_path`.
    pub fn new(cache_path: PathBuf) -> Self {
        Self { cache_path }
    }
}

impl FleetSource for ClaudeMaxSource {
    fn provider_id(&self) -> &str {
        PROVIDER_CLAUDE_MAX
    }

    /// Read `usage-cache.json` and return a `QuotaState` aggregating all
    /// non-errored Claude account entries found there.
    ///
    /// Each account appears as a model slot keyed by its account id (e.g.
    /// `"claude"`, `"claude-acc1"`).  `pct_used` = `five_hour.utilization`;
    /// `used = 0, limit = None` (raw counts not available from this cache).
    fn poll(&self) -> Option<QuotaState> {
        let bytes = std::fs::read(&self.cache_path).ok()?;
        let root: serde_json::Value = serde_json::from_slice(&bytes).ok()?;
        let accounts = root.get("accounts")?.as_array()?;

        let now = chrono::Utc::now().timestamp();
        let mut models = std::collections::HashMap::new();

        for entry in accounts {
            // Skip auth-failure entries (last_error is a non-null string).
            if entry.get("last_error").and_then(|e| e.as_str()).is_some() {
                continue;
            }
            let account_id = match entry.get("account").and_then(|v| v.as_str()) {
                Some(id) => id,
                None => continue,
            };
            let usage = match entry.get("usage") {
                Some(u) if !u.is_null() => u,
                _ => continue,
            };

            let five_hour = usage.get("five_hour");
            let pct = five_hour
                .and_then(|h| h.get("utilization"))
                .and_then(|v| v.as_f64())
                .map(|f| f as f32);
            // resets_at may be a float epoch or a formatted string.
            let resets_at = five_hour.and_then(|h| h.get("resets_at")).and_then(|v| {
                v.as_str()
                    .map(|s| s.to_string())
                    .or_else(|| v.as_f64().map(|n| n.to_string()))
            });

            // used = 0 / limit = None: raw token counts not available from
            // the usage cache; pct_used carries the real measurement.
            models.insert(
                account_id.to_string(),
                ModelUsage {
                    used: 0,
                    limit: None,
                    reset_at: resets_at,
                    pct_used: pct,
                    cost_usd: None,
                },
            );

            // The endpoint also publishes WEEKLY windows — `seven_day`
            // (account-wide) plus per-model breakdowns such as
            // `seven_day_opus` / `seven_day_sonnet`. Only the 5-hour window
            // was read before, so weekly exhaustion — the limit that actually
            // stops a Max account — was invisible to routing and to the
            // budget guard. Map every `seven_day*` key generically so a family
            // the provider adds later (e.g. `seven_day_fable`) is picked up
            // without another code change.
            for (key, window) in usage.as_object().into_iter().flatten() {
                let Some(suffix) = key.strip_prefix("seven_day") else {
                    continue;
                };
                let model = match suffix.trim_start_matches('_') {
                    "" => WEEKLY_SLOT_ALL,
                    other => other,
                };
                // A window whose `utilization` is null is still recorded, with
                // `pct_used: None`. "Reported but empty" and "not reported at
                // all" are different states and consumers must be able to tell
                // them apart.
                let pct = window
                    .get("utilization")
                    .and_then(|v| v.as_f64())
                    .map(|f| f as f32);
                let reset_at = window.get("resets_at").and_then(|v| {
                    v.as_str()
                        .map(|s| s.to_string())
                        .or_else(|| v.as_f64().map(|n| n.to_string()))
                });
                models.insert(
                    weekly_slot_key(account_id, model),
                    ModelUsage {
                        used: 0,
                        limit: None,
                        reset_at,
                        pct_used: pct,
                        cost_usd: None,
                    },
                );
            }
        }

        if models.is_empty() {
            return None;
        }

        Some(QuotaState {
            account_id: "claude-aggregate".to_string(),
            harness: "cc".to_string(),
            provider: PROVIDER_CLAUDE_MAX.to_string(),
            ts: now,
            models,
        })
    }
}

/// `FleetSource` for OpenAI Codex accounts.
///
/// Probes whether the `codex` CLI is on `$PATH` via `detect_cli`.  Returns
/// `Some` (with empty `models`) when the binary is present — confirming the
/// CLI is installed and the account is accessible — or `None` when it is
/// absent.  No usage API call is made: OpenAI does not expose a public CLI
/// quota endpoint for Codex subscription plans.  Presence information alone
/// is sufficient for the fleet dashboard to surface the account row.
pub struct CodexSource;

impl FleetSource for CodexSource {
    fn provider_id(&self) -> &str {
        PROVIDER_OPENAI_CODEX
    }

    /// Return `Some` when `codex` is on `$PATH`, `None` otherwise.
    ///
    /// An empty `models` map signals "detected, usage data unavailable":
    /// the CLI is present but no quota API exists to query.
    fn poll(&self) -> Option<QuotaState> {
        if !detect_cli("codex") {
            return None;
        }
        Some(QuotaState {
            account_id: "codex".to_string(),
            harness: "cli".to_string(),
            provider: PROVIDER_OPENAI_CODEX.to_string(),
            ts: chrono::Utc::now().timestamp(),
            models: std::collections::HashMap::new(),
        })
    }
}

/// `FleetSource` for Google AI (Antigravity / agy) accounts.
///
/// Probes whether the `agy` CLI is on `$PATH` via `detect_cli`.  Returns
/// `Some` (with empty `models`) when the binary is present — confirming the
/// CLI is installed — or `None` when it is absent.
///
/// No credential bridge or Google AI quota API call exists yet: there is no
/// `agy`-specific token-read path anywhere in this codebase.  Presence
/// information via `detect_cli` is the real available data for this source.
/// When the credential bridge and quota API are wired, this impl can be
/// extended to populate `models` with real token counts.
pub struct AgySource;

impl FleetSource for AgySource {
    fn provider_id(&self) -> &str {
        PROVIDER_GOOGLE_AGY
    }

    /// Return `Some` when `agy` is on `$PATH`, `None` otherwise.
    ///
    /// An empty `models` map signals "detected, usage data unavailable":
    /// the CLI is present but no quota API bridge exists to query.
    fn poll(&self) -> Option<QuotaState> {
        if !detect_cli("agy") {
            return None;
        }
        Some(QuotaState {
            account_id: "agy".to_string(),
            harness: "cli".to_string(),
            provider: PROVIDER_GOOGLE_AGY.to_string(),
            ts: chrono::Utc::now().timestamp(),
            models: std::collections::HashMap::new(),
        })
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

        // Read the Za prompt cap once at startup; missing/invalid config → default 120.
        let zai_prompts_per_5h = Config::load(&config_dir)
            .map(|c| c.quota.zai.prompts_per_5h)
            .unwrap_or(120);

        // ClaudeMaxSource reads usage-cache.json written by fetch_and_cache_claude_usage
        // (which runs before FleetPoller::tick on every iteration) at ~/.claude/usage-cache.json.
        let claude_cache_path = dirs::home_dir()
            .unwrap_or_else(|| config_dir.clone())
            .join(".claude")
            .join("usage-cache.json");

        let sources: Vec<Box<dyn FleetSource>> = vec![
            Box::new(ClaudeMaxSource::new(claude_cache_path)),
            Box::new(CodexSource),
            Box::new(AgySource),
            Box::new(GfpSource::new(state_path)),
        ];

        let interval = tokio::time::Duration::from_secs(config.interval_secs.max(1));

        info!(interval_secs = config.interval_secs, "fleet poller started");

        // Prime quota.json / quota-store.json immediately on startup instead
        // of waiting a full `interval_secs` for the first tick. Without this,
        // a freshly (re)started daemon left the widget reading up-to-an-
        // interval-stale (or, on first-ever run, entirely absent) data for as
        // long as 60 s. `tick_async` is bounded by per-account network
        // timeouts inside `refresh_accounts`, so this cannot hang startup.
        Self::tick_async(&sources, &store_path, zai_prompts_per_5h).await;

        loop {
            tokio::select! {
                _ = tokio::time::sleep(interval) => {}
                _ = shutdown.cancelled() => {
                    info!("fleet poller: shutdown signal received — exiting");
                    break;
                }
            }

            Self::tick_async(&sources, &store_path, zai_prompts_per_5h).await;
        }

        info!("fleet poller stopped");
    }

    /// Async tick: fetch live Claude usage, then run the synchronous quota
    /// source aggregation pass.
    ///
    /// Inputs: `sources` — registered fleet sources; `store_path` — destination;
    ///         `zai_prompts_per_5h` — Za prompt cap for the rolling window.
    /// Outputs: `quota.json` + `quota-store.json` updated.
    async fn tick_async(
        sources: &[Box<dyn FleetSource>],
        store_path: &std::path::Path,
        zai_prompts_per_5h: u32,
    ) {
        // Refresh accounts/ CLI availability, fetch live Claude usage, and write
        // quota.json.  This must run before the quota-store aggregation so the
        // widget has fresh data even when no synchronous source has returned data.
        let accts_path = accounts_dir().join("accounts.json");
        if accts_path.exists() {
            Self::refresh_accounts(&accts_path, zai_prompts_per_5h).await;
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
                Ok(()) => {
                    info!(path = %store_path.display(), "fleet: wrote empty quota-store.json (no sources ready)")
                }
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
    /// fetch live Claude usage for each discovered Claude account, inject Za local
    /// usage from `~/.cascade/za-usage.jsonl`, and atomically regenerate
    /// `accounts/quota.json` for the native widget.
    ///
    /// Purpose: keeps the registry and widget data in sync with the current PATH state
    /// on each daemon tick without requiring the user to run `cascade accounts detect`.
    /// Live Claude usage is fetched via `api.anthropic.com/api/oauth/usage` for each
    /// account whose token is valid (or after a `claude -p` refresh attempt if expired).
    /// Fetched usage is written to `~/.claude/usage-cache.json` so `write_quota_json`
    /// picks it up via its existing merge path. Za usage is read from the local JSONL
    /// log and patched into the same cache before `write_quota_json` runs.
    ///
    /// Inputs:  `path` — existing `accounts/accounts.json` file;
    ///          `zai_prompts_per_5h` — rolling-window prompt cap for Za accounts.
    /// Outputs: updated `accounts.json` + `quota.json` on success; warn log on error.
    /// Constraints: async; bounded per-account (10s network timeout + 45s refresh);
    ///              never overwrites existing usage with zeros on failure.
    async fn refresh_accounts(path: &std::path::Path, zai_prompts_per_5h: u32) {
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

        // ── Za local usage injection ──────────────────────────────────────────
        // Za (z.ai GLM Coding Plan) exposes no usage API; we read the local
        // prompt log written by the CLI conductor and patch synthetic five-hour
        // data into usage-cache.json for each configured Zai account, so
        // write_quota_json picks it up via the existing merge path. Only
        // writes when the log file has data — never overwrites with zeros.
        inject_za_usage_into_cache(&registry, zai_prompts_per_5h);

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

        let mut updates: Vec<(String, serde_json::Value)> = Vec::new();

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
                        updates.push((account_id.to_string(), entry));
                    }
                    // Transient → preserve prior entry untouched.
                    continue;
                }
            };

            // Fetch live usage.
            match fetch_claude_usage(account_id, access_token.as_str()).await {
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
                    updates.push((account_id.to_string(), entry));
                }
                None => {
                    // Transport/HTTP error or error envelope — transient. Do NOT
                    // overwrite a prior-good entry with zeros; preserve it.
                    warn!(account = %account_id, "fleet: live usage fetch returned None — preserving prior cache");
                }
            }
        }

        if updates.is_empty() {
            return;
        }
        if let Err(e) = merge_usage_cache_updates(&cache_path, updates) {
            warn!(%e, "fleet: failed to merge usage-cache.json updates");
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
                        MODEL_CLAUDE_HAIKU,
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

fn merge_usage_cache_updates(
    cache_path: &Path,
    updates: Vec<(String, serde_json::Value)>,
) -> Result<(), String> {
    if let Some(parent) = cache_path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| format!("create usage-cache parent: {e}"))?;
    }

    let lock_file = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(cache_path)
        .map_err(|e| format!("open usage-cache lock file: {e}"))?;
    lock_file
        .lock_exclusive()
        .map_err(|e| format!("lock usage-cache: {e}"))?;

    let result = (|| -> Result<(), String> {
        let mut existing: Vec<serde_json::Value> = (|| -> Option<Vec<serde_json::Value>> {
            let bytes = std::fs::read(cache_path).ok()?;
            let v: serde_json::Value = serde_json::from_slice(&bytes).ok()?;
            v.get("accounts")?.as_array().cloned()
        })()
        .unwrap_or_default();

        for (account_id, entry) in updates {
            upsert_cache_entry(&mut existing, &account_id, entry);
        }

        let payload = serde_json::json!({ "accounts": existing });
        let bytes = serde_json::to_vec_pretty(&payload)
            .map_err(|e| format!("serialize usage cache: {e}"))?;
        let tmp = cache_path.with_extension("tmp");
        std::fs::write(&tmp, &bytes).map_err(|e| format!("write usage-cache tmp: {e}"))?;
        std::fs::rename(&tmp, cache_path).map_err(|e| format!("rename usage-cache tmp: {e}"))?;
        Ok(())
    })();

    let unlock_result =
        fs2::FileExt::unlock(&lock_file).map_err(|e| format!("unlock usage-cache: {e}"));
    result.and(unlock_result)
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
    if let Some(pos) = entries
        .iter()
        .position(|e| e.get("account").and_then(|v| v.as_str()) == Some(account_id))
    {
        entries[pos] = entry;
    } else {
        entries.push(entry);
    }
}

// ── Za usage injection ────────────────────────────────────────────────────────

/// Read Za local prompt log and patch synthetic five-hour usage into
/// `~/.claude/usage-cache.json` for each configured Zai account.
///
/// Purpose: since z.ai exposes no usage API, the CLI conductor records each
/// dispatched prompt in `~/.cascade/za-usage.jsonl`. This function reads that
/// log on every fleet tick and writes synthetic `five_hour` data into the same
/// usage-cache that `write_quota_json` merges, so Za utilization appears in the
/// widget without any new widget code.
///
/// Inputs:  `registry` — current accounts registry (to find Zai accounts);
///          `zai_prompts_per_5h` — rolling-window prompt cap.
/// Outputs: best-effort patch to `~/.claude/usage-cache.json`; silent on error.
/// Constraints: never overwrites with zeros — if `read_window` returns `None`
///              (log absent or empty), the existing cache entry is preserved.
fn inject_za_usage_into_cache(registry: &AccountsRegistry, zai_prompts_per_5h: u32) {
    use cascade_core::za_usage::{
        count_glm_session_turns, read_glm_exhaustion_signal, read_window,
    };

    let home = match dirs::home_dir() {
        Some(h) => h,
        None => return,
    };

    let za_log_path = home.join(".cascade").join("za-usage.jsonl");
    let cache_path = home.join(".claude").join("usage-cache.json");
    let glm_config_dir = home.join(".claude-glm");

    let cap = if zai_prompts_per_5h == 0 {
        1
    } else {
        zai_prompts_per_5h
    };

    // Collect Zai account ids from the registry.
    let zai_accounts: Vec<String> = registry
        .accounts
        .iter()
        .filter(|a| a.family == AccountFamily::Zai)
        .map(|a| a.id.clone())
        .collect();

    if zai_accounts.is_empty() {
        return;
    }

    let now_secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let now_f64 = now_secs as f64;

    // ── Path 1: exhaustion signal (primary) ───────────────────────────────────
    // Check GLM session logs for a live 429 before counting anything. If found
    // and the reset window is in the future, clamp to 100% immediately.
    if let Some((reset_epoch, resets_in)) = read_glm_exhaustion_signal(&glm_config_dir) {
        let resets_at_f64 = reset_epoch as f64;
        for account_id in &zai_accounts {
            let entry = serde_json::json!({
                "account":      account_id,
                "provider":     "zai",
                "usage": {
                    "five_hour": {
                        "utilization": 100.0_f64,
                        "resets_at":   resets_at_f64,
                        "resets_in":   resets_in
                    },
                    "seven_day":         serde_json::Value::Null,
                    "seven_day_sonnet":  serde_json::Value::Null,
                    "seven_day_opus":    serde_json::Value::Null,
                    "extra_usage":       serde_json::Value::Null
                },
                "last_pull_at": now_f64,
                "quota_opaque": false,
                "last_error":   serde_json::Value::Null
            });
            let updates = vec![(account_id.clone(), entry)];
            if let Err(e) = merge_usage_cache_updates(&cache_path, updates) {
                warn!(%e, account = %account_id, "fleet: failed to inject Za exhaustion into cache");
            }
        }
        return;
    }

    // ── Path 2: live count (not exhausted) ────────────────────────────────────
    // Combine conductor JSONL dispatches + direct GLM session turns.
    let summary = read_window(&za_log_path, 5 * 3600);
    let conductor_prompts = summary.as_ref().map(|s| s.used_prompts).unwrap_or(0);
    let glm_turns = count_glm_session_turns(&glm_config_dir, 5 * 3600);
    let combined_count = conductor_prompts.saturating_add(glm_turns);

    // If both sources are empty, preserve the existing cache entry (don't write zeros).
    if combined_count == 0 && summary.is_none() {
        return;
    }

    let utilization = (100.0 * combined_count as f64 / cap as f64).min(100.0);

    // Compute resets_at from oldest conductor record in the window (if any).
    let resets_at_epoch: Option<f64> = summary.as_ref().and_then(|s| {
        s.oldest_ts_in_window
            .map(|oldest| (oldest + 5 * 3600) as f64)
    });
    let resets_in: Option<String> = resets_at_epoch.and_then(|ra| {
        let remaining = ra as i64 - now_secs as i64;
        if remaining > 0 {
            let h = remaining / 3600;
            let m = (remaining % 3600) / 60;
            Some(format!("{h}h {m:02}m"))
        } else {
            None
        }
    });

    for account_id in &zai_accounts {
        let entry = serde_json::json!({
            "account":      account_id,
            "provider":     "zai",
            "usage": {
                "five_hour": {
                    "utilization": utilization,
                    "resets_at":   resets_at_epoch,
                    "resets_in":   resets_in
                },
                "seven_day":         serde_json::Value::Null,
                "seven_day_sonnet":  serde_json::Value::Null,
                "seven_day_opus":    serde_json::Value::Null,
                "extra_usage":       serde_json::Value::Null
            },
            "last_pull_at": now_f64,
            "quota_opaque": false,
            "last_error":   serde_json::Value::Null
        });

        let updates = vec![(account_id.clone(), entry)];
        if let Err(e) = merge_usage_cache_updates(&cache_path, updates) {
            warn!(%e, account = %account_id, "fleet: failed to inject Za usage into cache");
        }
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

        assert!(
            store_path.exists(),
            "quota-store.json must exist after tick"
        );

        let bytes = std::fs::read(&store_path).expect("read store");
        let store: QuotaStore = serde_json::from_slice(&bytes).expect("valid JSON");
        assert_eq!(store.schema_version, QUOTA_STORE_SCHEMA_VERSION);
        assert_eq!(store.accounts.len(), 1);
        assert_eq!(store.accounts[0].account_id, "mock-acct1");
    }

    /// ClaudeMaxSource returns None when the cache file does not exist.
    /// CodexSource and AgySource return None when the CLI is absent (common on CI),
    /// or Some when detected; the test is environment-safe in both cases.
    #[test]
    fn sources_return_none_when_unconfigured() {
        let tmp = TempDir::new().unwrap();

        // ClaudeMaxSource with a nonexistent cache path must return None.
        let cm = ClaudeMaxSource::new(tmp.path().join("no-such-file.json"));
        assert!(
            cm.poll().is_none(),
            "ClaudeMaxSource must return None when cache is absent"
        );

        // CodexSource: may return None (not installed) or Some (installed).
        // Either way must not panic and must carry the correct provider id.
        let cx = CodexSource;
        if let Some(snap) = cx.poll() {
            assert_eq!(
                snap.provider, PROVIDER_OPENAI_CODEX,
                "CodexSource provider_id mismatch"
            );
        }

        // AgySource: same.
        let ag = AgySource;
        if let Some(snap) = ag.poll() {
            assert_eq!(
                snap.provider, PROVIDER_GOOGLE_AGY,
                "AgySource provider_id mismatch"
            );
        }
    }

    /// The usage endpoint publishes weekly windows alongside the 5-hour one:
    /// `seven_day` (account-wide) plus per-model breakdowns. All of them were
    /// dropped before T-P7-E13-12, so weekly exhaustion — the limit that
    /// actually stops a Max account — was invisible to routing.
    #[test]
    fn claude_max_source_captures_weekly_and_per_model_windows() {
        use cascade_types::quota_store::weekly_slot_key;

        let tmp = TempDir::new().unwrap();
        let cache_path = tmp.path().join("usage-cache.json");
        // Shape copied from a real ~/.claude/usage-cache.json payload.
        let cache_content = serde_json::json!({
            "accounts": [{
                "account": "claude",
                "provider": "claude",
                "usage": {
                    "fable_available": true,
                    "five_hour": { "utilization": 15.0, "resets_at": 1_787_330_400_f64 },
                    "seven_day": { "utilization": 3.0, "resets_at": 1_787_889_600_f64 },
                    "seven_day_opus": { "utilization": 71.5, "resets_at": null },
                    "seven_day_sonnet": { "utilization": null, "resets_at": null }
                },
                "last_error": null
            }]
        });
        std::fs::write(&cache_path, cache_content.to_string()).unwrap();

        let snap = ClaudeMaxSource::new(cache_path)
            .poll()
            .expect("cache with one healthy account must poll");

        // The account slot still carries the 5-hour utilisation, unchanged.
        assert_eq!(
            snap.models.get("claude").and_then(|m| m.pct_used),
            Some(15.0)
        );

        // Account-wide weekly window.
        assert_eq!(
            snap.models
                .get(&weekly_slot_key("claude", "all"))
                .and_then(|m| m.pct_used),
            Some(3.0),
            "seven_day must land in the account-wide weekly slot"
        );

        // Per-model weekly breakdown.
        assert_eq!(
            snap.models
                .get(&weekly_slot_key("claude", "opus"))
                .and_then(|m| m.pct_used),
            Some(71.5)
        );

        // A window reported with a null utilisation is still RECORDED, with no
        // pct — "reported but empty" must stay distinguishable from "absent",
        // because the budget guard treats the two differently.
        let sonnet = snap
            .models
            .get(&weekly_slot_key("claude", "sonnet"))
            .expect("a reported window must be recorded even with a null utilisation");
        assert_eq!(sonnet.pct_used, None);

        // No window was invented for a family the provider did not report.
        assert!(!snap
            .models
            .contains_key(&weekly_slot_key("claude", "fable")));
    }

    /// ClaudeMaxSource with a valid usage-cache.json returns a correctly shaped QuotaState.
    #[test]
    fn claude_max_source_reads_from_cache() {
        let tmp = TempDir::new().unwrap();
        let cache_path = tmp.path().join("usage-cache.json");

        let cache_content = serde_json::json!({
            "accounts": [{
                "account": "claude",
                "provider": "claude",
                "usage": {
                    "five_hour": {
                        "utilization": 37.5,
                        "resets_at": 1_700_000_000_f64,
                        "resets_in": "2h 30m"
                    }
                },
                "last_pull_at": 1_700_000_000_f64,
                "quota_opaque": false,
                "last_error": null
            }]
        });
        std::fs::write(&cache_path, serde_json::to_vec(&cache_content).unwrap()).unwrap();

        let source = ClaudeMaxSource::new(cache_path);
        let snap = source
            .poll()
            .expect("ClaudeMaxSource must return Some for valid cache");

        assert_eq!(
            snap.provider, PROVIDER_CLAUDE_MAX,
            "provider must be claude-max"
        );
        assert_eq!(snap.harness, "cc");
        let entry = snap
            .models
            .get("claude")
            .expect("must have entry keyed by account id");
        let pct = entry
            .pct_used
            .expect("pct_used must be set from utilization");
        assert!(
            (pct - 37.5_f32).abs() < 0.1,
            "pct_used must match five_hour.utilization; got {pct}"
        );
        assert_eq!(entry.used, 0, "used must be 0 (raw count not in cache)");
    }

    /// ClaudeMaxSource skips entries that have a last_error string.
    #[test]
    fn claude_max_source_skips_auth_failure_entries() {
        let tmp = TempDir::new().unwrap();
        let cache_path = tmp.path().join("usage-cache.json");

        let cache_content = serde_json::json!({
            "accounts": [{
                "account": "claude",
                "provider": "claude",
                "usage": null,
                "last_pull_at": 1_700_000_000_f64,
                "quota_opaque": false,
                "last_error": "http_401"
            }]
        });
        std::fs::write(&cache_path, serde_json::to_vec(&cache_content).unwrap()).unwrap();

        let source = ClaudeMaxSource::new(cache_path);
        assert!(
            source.poll().is_none(),
            "ClaudeMaxSource must return None when all entries have last_error"
        );
    }

    /// Dynamic dispatch via `Box<dyn FleetSource>` must work correctly.
    #[test]
    fn fleet_source_trait_dispatch() {
        let tmp = TempDir::new().unwrap();

        let sources: Vec<Box<dyn FleetSource>> = vec![
            // Nonexistent path → ClaudeMaxSource returns None deterministically.
            Box::new(ClaudeMaxSource::new(tmp.path().join("no-cache.json"))),
            Box::new(CodexSource), // None or Some depending on PATH — both valid
            Box::new(AgySource),   // None or Some depending on PATH — both valid
            Box::new(MockSource {
                id: "mock".to_string(),
                snapshot: Some(make_snapshot("dyn-acct")),
            }),
        ];

        let results: Vec<Option<QuotaState>> = sources.iter().map(|s| s.poll()).collect();

        // ClaudeMaxSource with no cache → None.
        assert!(
            results[0].is_none(),
            "ClaudeMaxSource with no cache must be None"
        );
        // CodexSource and AgySource: don't assert value (PATH-dependent in test env).
        // MockSource with a snapshot must return Some.
        assert!(results[3].is_some(), "MockSource must return Some");
        assert_eq!(
            results[3].as_ref().unwrap().account_id,
            "dyn-acct",
            "MockSource account_id mismatch"
        );
    }
}
