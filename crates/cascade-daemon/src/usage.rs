//! Token usage accumulator for the Cascade daemon.
//!
//! # Purpose
//! Accumulates token usage and estimated USD cost per provider per day.  Every
//! completion that flows through the daemon records its [`TokenUsage`] and
//! `cost_usd` here.  Persistence is SQLite — the same `events.db` database
//! path pattern is followed but using a dedicated `provider_usage` table.
//!
//! # Inputs / Outputs
//! - `UsageAccumulator::new(db_path)` → opens/creates the SQLite DB and runs
//!   the `provider_usage` migration.
//! - `record(provider_id, usage, cost_usd)` → increments today's row atomically.
//! - `today(provider_id)` → `Option<ProviderUsageItem>` for one provider.
//! - `all_today()` → `Vec<ProviderUsageItem>` for all providers with today usage.
//! - `weekly_totals()` → `Vec<ProviderUsageItem>` — summed over the last 7 days.
//!
//! # Constraints
//! - Concurrent `record()` calls are serialized via a `tokio::sync::Mutex` on the
//!   `rusqlite::Connection` — no SQLITE_BUSY races.
//! - `cost_usd = None` is stored as `0.0` (never NULL).
//! - The migration is idempotent (`CREATE TABLE IF NOT EXISTS`).
//! - `date('now')` in SQLite uses UTC; daemon dates are therefore UTC.
//!
//! # SPORT
//! MASTER-RUST-CRATES.md — cascade-daemon: UsageAccumulator + provider_usage table
//!   (T-P3-E04-27)

use std::path::{Path, PathBuf};
use std::sync::Arc;

use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;
use tracing::{debug, error, info};

use cascade_providers::TokenUsage;

// ── ProviderUsageItem ─────────────────────────────────────────────────────────

/// A single provider's usage snapshot for one day.
///
/// Returned by `today()` and `all_today()`.  The `date` field uses ISO-8601
/// format (`YYYY-MM-DD`, UTC).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ProviderUsageItem {
    /// ISO-8601 date (UTC) for this usage row.
    pub date: String,
    /// Stable provider ID (matches `ProviderEntry.id`).
    pub provider_id: String,
    /// Total prompt/input tokens consumed on this date.
    pub prompt_tokens: u64,
    /// Total completion/output tokens consumed on this date.
    pub completion_tokens: u64,
    /// Total tokens consumed (`prompt_tokens + completion_tokens`).
    pub total_tokens: u64,
    /// Estimated USD cost for this date (0.0 when unknown / free-tier).
    pub cost_usd: f64,
}

// ── UsageAccumulator ──────────────────────────────────────────────────────────

/// Daemon-side token usage accumulator.
///
/// Wraps a `rusqlite::Connection` behind an async Mutex so concurrent tokio
/// tasks can call `record()` safely without holding the lock across `.await`
/// points.
pub struct UsageAccumulator {
    conn: Arc<Mutex<Connection>>,
    db_path: PathBuf,
}

impl UsageAccumulator {
    /// Open (or create) the usage database at `db_path` and apply the
    /// `provider_usage` migration.
    ///
    /// # Errors
    /// Returns a `String` describing the SQLite error if the connection or
    /// migration fails.
    pub fn new(db_path: impl AsRef<Path>) -> Result<Self, String> {
        let db_path = db_path.as_ref().to_path_buf();

        // Ensure parent directory exists.
        if let Some(parent) = db_path.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|e| format!("create_dir_all {}: {e}", parent.display()))?;
        }

        let conn = Connection::open(&db_path)
            .map_err(|e| format!("open usage db {}: {e}", db_path.display()))?;

        // WAL mode: allows concurrent reads from widgets / IPC.
        conn.execute_batch("PRAGMA journal_mode=WAL;")
            .map_err(|e| format!("enable WAL: {e}"))?;

        // Migration (idempotent).
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS provider_usage (
                date                TEXT NOT NULL,
                provider_id         TEXT NOT NULL,
                prompt_tokens       INTEGER DEFAULT 0,
                completion_tokens   INTEGER DEFAULT 0,
                total_tokens        INTEGER DEFAULT 0,
                cost_usd            REAL    DEFAULT 0,
                PRIMARY KEY (date, provider_id)
            );",
        )
        .map_err(|e| format!("migration provider_usage: {e}"))?;

        info!(path = %db_path.display(), "UsageAccumulator opened");

        Ok(Self {
            conn: Arc::new(Mutex::new(conn)),
            db_path,
        })
    }

    /// Record a single completion's token usage for a provider.
    ///
    /// Atomically increments today's row (UTC date).  If no row exists for
    /// today + provider_id, one is inserted with the initial values.
    ///
    /// `cost_usd = None` → stored as `0.0`.
    pub async fn record(&self, provider_id: &str, usage: &TokenUsage, cost_usd: Option<f64>) {
        let cost = cost_usd.unwrap_or(0.0);
        let conn = self.conn.lock().await;

        // Upsert: insert a zero row then increment in two steps inside a
        // transaction to avoid a TOCTOU gap.  SQLite's atomic `UPDATE ... WHERE`
        // is sufficient when the Mutex serializes all writers.
        let result = conn.execute(
            "INSERT INTO provider_usage (date, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd)
             VALUES (date('now'), ?1, ?2, ?3, ?4, ?5)
             ON CONFLICT(date, provider_id) DO UPDATE SET
               prompt_tokens     = prompt_tokens     + excluded.prompt_tokens,
               completion_tokens = completion_tokens + excluded.completion_tokens,
               total_tokens      = total_tokens      + excluded.total_tokens,
               cost_usd          = cost_usd          + excluded.cost_usd",
            params![
                provider_id,
                usage.prompt_tokens as i64,
                usage.completion_tokens as i64,
                usage.total_tokens as i64,
                cost,
            ],
        );

        match result {
            Ok(_) => debug!(
                provider_id,
                prompt = usage.prompt_tokens,
                completion = usage.completion_tokens,
                cost,
                "usage recorded"
            ),
            Err(e) => error!(provider_id, %e, "failed to record usage"),
        }
    }

    /// Return today's usage for a single provider (UTC date).
    ///
    /// Returns `None` if the provider has no recorded usage today.
    pub async fn today(&self, provider_id: &str) -> Option<ProviderUsageItem> {
        let conn = self.conn.lock().await;
        let result = conn.query_row(
            "SELECT date, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd
             FROM provider_usage
             WHERE date = date('now') AND provider_id = ?1",
            params![provider_id],
            |row| {
                Ok(ProviderUsageItem {
                    date: row.get(0)?,
                    provider_id: row.get(1)?,
                    prompt_tokens: row.get::<_, i64>(2)? as u64,
                    completion_tokens: row.get::<_, i64>(3)? as u64,
                    total_tokens: row.get::<_, i64>(4)? as u64,
                    cost_usd: row.get(5)?,
                })
            },
        );

        match result {
            Ok(item) => Some(item),
            Err(rusqlite::Error::QueryReturnedNoRows) => None,
            Err(e) => {
                error!(provider_id, %e, "usage today() query failed");
                None
            }
        }
    }

    /// Return today's usage for all providers that have at least one recorded
    /// completion today (UTC date).
    pub async fn all_today(&self) -> Vec<ProviderUsageItem> {
        let conn = self.conn.lock().await;
        let mut stmt = match conn.prepare(
            "SELECT date, provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd
             FROM provider_usage
             WHERE date = date('now')",
        ) {
            Ok(s) => s,
            Err(e) => {
                error!(%e, "all_today() prepare failed");
                return vec![];
            }
        };

        let rows = stmt.query_map([], |row| {
            Ok(ProviderUsageItem {
                date: row.get(0)?,
                provider_id: row.get(1)?,
                prompt_tokens: row.get::<_, i64>(2)? as u64,
                completion_tokens: row.get::<_, i64>(3)? as u64,
                total_tokens: row.get::<_, i64>(4)? as u64,
                cost_usd: row.get(5)?,
            })
        });

        match rows {
            Ok(mapped) => mapped
                .filter_map(|r| r.map_err(|e| error!(%e, "all_today() row error")).ok())
                .collect(),
            Err(e) => {
                error!(%e, "all_today() query_map failed");
                vec![]
            }
        }
    }

    /// Return per-provider totals summed over the last 7 days (UTC).
    ///
    /// Used by the settings page weekly summary.
    pub async fn weekly_totals(&self) -> Vec<ProviderUsageItem> {
        let conn = self.conn.lock().await;
        let mut stmt = match conn.prepare(
            "SELECT date('now') AS date,
                    provider_id,
                    SUM(prompt_tokens)     AS prompt_tokens,
                    SUM(completion_tokens) AS completion_tokens,
                    SUM(total_tokens)      AS total_tokens,
                    SUM(cost_usd)          AS cost_usd
             FROM provider_usage
             WHERE date >= date('now', '-6 days')
             GROUP BY provider_id",
        ) {
            Ok(s) => s,
            Err(e) => {
                error!(%e, "weekly_totals() prepare failed");
                return vec![];
            }
        };

        let rows = stmt.query_map([], |row| {
            Ok(ProviderUsageItem {
                date: row.get(0)?,
                provider_id: row.get(1)?,
                prompt_tokens: row.get::<_, i64>(2)? as u64,
                completion_tokens: row.get::<_, i64>(3)? as u64,
                total_tokens: row.get::<_, i64>(4)? as u64,
                cost_usd: row.get(5)?,
            })
        });

        match rows {
            Ok(mapped) => mapped
                .filter_map(|r| r.map_err(|e| error!(%e, "weekly_totals() row error")).ok())
                .collect(),
            Err(e) => {
                error!(%e, "weekly_totals() query_map failed");
                vec![]
            }
        }
    }

    /// Return the path to the SQLite database file.
    pub fn db_path(&self) -> &Path {
        &self.db_path
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    fn make_acc(tmp: &TempDir) -> UsageAccumulator {
        let path = tmp.path().join("usage.db");
        UsageAccumulator::new(&path).expect("open accumulator")
    }

    fn usage(prompt: u32, completion: u32) -> TokenUsage {
        TokenUsage {
            prompt_tokens: prompt,
            completion_tokens: completion,
            total_tokens: prompt + completion,
        }
    }

    #[tokio::test]
    #[serial(usage_accumulator)]
    async fn migration_runs_without_error() {
        let tmp = TempDir::new().unwrap();
        make_acc(&tmp);
        // Second open (idempotent migration).
        let path = tmp.path().join("usage.db");
        UsageAccumulator::new(&path).expect("second open should not fail");
    }

    #[tokio::test]
    #[serial(usage_accumulator)]
    async fn record_twice_same_provider_sums_correctly() {
        let tmp = TempDir::new().unwrap();
        let acc = make_acc(&tmp);

        acc.record("anthropic", &usage(100, 200), Some(0.01)).await;
        acc.record("anthropic", &usage(50, 100), Some(0.005)).await;

        let item = acc.today("anthropic").await.expect("should have row");
        assert_eq!(item.provider_id, "anthropic");
        assert_eq!(item.prompt_tokens, 150);
        assert_eq!(item.completion_tokens, 300);
        assert_eq!(item.total_tokens, 450);
        // Float comparison with small epsilon.
        assert!((item.cost_usd - 0.015).abs() < 1e-9);
    }

    #[tokio::test]
    #[serial(usage_accumulator)]
    async fn all_today_returns_one_row_per_provider() {
        let tmp = TempDir::new().unwrap();
        let acc = make_acc(&tmp);

        acc.record("openai", &usage(10, 20), None).await;
        acc.record("anthropic", &usage(30, 40), Some(0.002)).await;

        let rows = acc.all_today().await;
        assert_eq!(rows.len(), 2, "expected 2 providers");
        assert!(rows.iter().any(|r| r.provider_id == "openai"));
        assert!(rows.iter().any(|r| r.provider_id == "anthropic"));

        let openai = rows.iter().find(|r| r.provider_id == "openai").unwrap();
        assert_eq!(openai.cost_usd, 0.0, "None cost_usd stored as 0.0");
    }

    #[tokio::test]
    #[serial(usage_accumulator)]
    async fn today_returns_none_for_unknown_provider() {
        let tmp = TempDir::new().unwrap();
        let acc = make_acc(&tmp);
        assert!(acc.today("no-such-provider").await.is_none());
    }

    #[tokio::test]
    #[serial(usage_accumulator)]
    async fn weekly_totals_groups_by_provider() {
        let tmp = TempDir::new().unwrap();
        let acc = make_acc(&tmp);

        acc.record("gemini", &usage(100, 200), Some(0.005)).await;
        acc.record("gemini", &usage(50, 100), Some(0.002)).await;

        let weekly = acc.weekly_totals().await;
        let gemini = weekly.iter().find(|r| r.provider_id == "gemini");
        assert!(gemini.is_some(), "gemini should appear in weekly totals");
        let g = gemini.unwrap();
        assert_eq!(g.prompt_tokens, 150);
        assert_eq!(g.completion_tokens, 300);
    }
}
