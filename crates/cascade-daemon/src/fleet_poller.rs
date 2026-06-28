//! Fleet poller — periodic multi-source quota aggregation loop (E-P6-03 v1.1).
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
//!   - Stub sources (`ClaudeMaxSource`, `CodexSource`, `AgySource`) always
//!     return `None`; they exist as object-safe placeholders until real API
//!     polling lands in a future epic.
//!   - `GfpSource` reads the existing `quota-state.json` written by the
//!     `gfp`-feature quota poller. If the file is absent or stale (>120 s),
//!     it returns `None`.
//!   - No `unwrap()` outside `#[cfg(test)]` blocks.
//!   - File ≤ 300 lines.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — fleet_poller (E-P6-03 v1.1)

use std::path::PathBuf;

use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use cascade_core::accounts_store::{
    accounts_dir, count_gfp_keys, detect_cli, read_accounts_registry, write_accounts_registry,
    write_quota_json,
};
use cascade_core::quota_aggregator::aggregate_quota;
use cascade_core::quota_store::{write_quota_store, QUOTA_STORE_SCHEMA_VERSION};
use cascade_types::accounts::{AccessMethod, AccountFamily};
use cascade_types::quota_store::{
    QuotaState, QuotaStore, PROVIDER_CLAUDE_MAX, PROVIDER_GFP, PROVIDER_GOOGLE_AGY,
    PROVIDER_OPENAI_CODEX,
};

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

/// Stub source for Anthropic Claude Max subscription accounts.
///
/// Purpose: placeholder until real Claude Max API polling lands. Always
/// returns `None`; registers the provider in the fleet source list.
/// Constraints: must never return fake usage numbers.
pub struct ClaudeMaxSource;

impl FleetSource for ClaudeMaxSource {
    fn provider_id(&self) -> &str {
        PROVIDER_CLAUDE_MAX
    }

    /// No real polling configured yet; returns `None`.
    fn poll(&self) -> Option<QuotaState> {
        None
    }
}

/// Stub source for OpenAI Codex accounts.
///
/// Purpose: placeholder until real Codex API polling lands. Always
/// returns `None`; registers the provider in the fleet source list.
pub struct CodexSource;

impl FleetSource for CodexSource {
    fn provider_id(&self) -> &str {
        PROVIDER_OPENAI_CODEX
    }

    /// No real polling configured yet; returns `None`.
    fn poll(&self) -> Option<QuotaState> {
        None
    }
}

/// Stub source for Google AI (agy) accounts.
///
/// Purpose: placeholder until real Google Agy API polling lands. Always
/// returns `None`; registers the provider in the fleet source list.
pub struct AgySource;

impl FleetSource for AgySource {
    fn provider_id(&self) -> &str {
        PROVIDER_GOOGLE_AGY
    }

    /// No real polling configured yet; returns `None`.
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

            Self::tick(&sources, &store_path);
        }

        info!("fleet poller stopped");
    }

    /// Execute one polling tick across all sources and write the result.
    ///
    /// Inputs: `sources` — registered fleet sources; `store_path` — destination.
    /// Outputs: `quota-store.json` and (if it already exists) `accounts.json` written.
    /// Constraints: synchronous — must not be called from an async context
    ///              without `spawn_blocking` if sources perform blocking I/O.
    fn tick(sources: &[Box<dyn FleetSource>], store_path: &std::path::Path) {
        // Refresh accounts/ CLI availability and GFP key count, and regenerate the
        // native widget's quota.json — on EVERY tick, independent of whether the
        // live quota sources have data yet. This must run before the early-return
        // below: in the common case where no source has live quota data, the widget
        // would otherwise never see a refreshed quota.json. Only runs when
        // accounts/accounts.json already exists (creation is `cascade accounts detect`).
        let accts_path = accounts_dir().join("accounts.json");
        if accts_path.exists() {
            Self::refresh_accounts(&accts_path);
        }

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
    /// then atomically regenerate `accounts/quota.json` for the native widget.
    ///
    /// Purpose: keeps the registry and widget data in sync with the current PATH state
    /// on each daemon tick without requiring the user to run `cascade accounts detect`.
    /// Inputs:  `path` — existing `accounts/accounts.json` file.
    /// Outputs: updated `accounts.json` + `quota.json` on success; warn log on error.
    /// Constraints: only called when the file exists; never creates it.
    fn refresh_accounts(path: &std::path::Path) {
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

        // Refresh quota.json (same directory as accounts.json).
        if let Some(dir) = path.parent() {
            let quota_path = dir.join("quota.json");
            match write_quota_json(&quota_path, &registry) {
                Ok(()) => info!(path = %quota_path.display(), "fleet: quota.json refreshed"),
                Err(e) => warn!(%e, "fleet: failed to write quota.json"),
            }
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
