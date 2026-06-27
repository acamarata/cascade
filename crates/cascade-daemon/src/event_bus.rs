//! Internal event bus for cascaded.
//!
//! Purpose: SQLite-backed persistent event queue. Events survive daemon
//! restarts. All IPC subsystems (inbox alert, hotword, reminders) publish
//! into the bus and consumers subscribe via `subscribe()`.
//!
//! Schema (`~/.cascade/events.db`):
//!   events(id INTEGER PK, kind TEXT, payload TEXT, ts INTEGER, consumed INTEGER)
//!   hotwords(word TEXT PK, context_block TEXT, updated INTEGER)
//!
//! Queue depth is written to HealthState every `SAMPLE_INTERVAL_SECS`.
//!
//! Constraints:
//!   - SQLite in WAL mode for concurrent reads from widgets / IPC.
//!   - `consumed` flag instead of DELETE for auditability.
//!   - All writes are serialized through a single tokio Mutex to prevent
//!     SQLITE_BUSY without enabling multi-thread mode.

use std::path::PathBuf;
use std::sync::Arc;

use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;
use tracing::debug;

use crate::supervisor::DaemonError;

const DB_NAME: &str = "events.db";
const SAMPLE_INTERVAL_SECS: u64 = 30;

/// Cloneable handle to the event bus.
pub type SharedBus = Arc<EventBus>;

pub struct EventBus {
    db: Mutex<Connection>,
    config_dir: PathBuf,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Event {
    pub id: i64,
    pub kind: String,
    pub payload: serde_json::Value,
    pub ts: i64,
}

impl EventBus {
    /// Open (or create) the events database.
    pub async fn new(config_dir: PathBuf) -> Result<Arc<Self>, DaemonError> {
        let db_path = config_dir.join(DB_NAME);
        let conn = cascade_db::open_configured(&db_path)
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        // Create tables if they don't exist — idempotent DDL.
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS events (
                id       INTEGER PRIMARY KEY AUTOINCREMENT,
                kind     TEXT    NOT NULL,
                payload  TEXT    NOT NULL DEFAULT '{}',
                ts       INTEGER NOT NULL,
                consumed INTEGER NOT NULL DEFAULT 0
             );
             CREATE TABLE IF NOT EXISTS hotwords (
                word          TEXT    PRIMARY KEY,
                context_block TEXT    NOT NULL,
                updated       INTEGER NOT NULL
             );
             CREATE TABLE IF NOT EXISTS inbox_items (
                id       INTEGER PRIMARY KEY AUTOINCREMENT,
                project  TEXT    NOT NULL,
                path     TEXT    NOT NULL,
                summary  TEXT    NOT NULL DEFAULT '',
                ts       INTEGER NOT NULL
             );",
        )
        .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        Ok(Arc::new(Self {
            db: Mutex::new(conn),
            config_dir,
        }))
    }

    /// Publish an event into the bus.
    ///
    /// Inputs: `kind` — event type string, `payload` — arbitrary JSON value.
    /// Outputs: assigned event id.
    pub async fn publish(
        &self,
        kind: &str,
        payload: serde_json::Value,
    ) -> Result<i64, DaemonError> {
        let conn = self.db.lock().await;
        let ts = now_secs();
        let payload_str = serde_json::to_string(&payload).unwrap_or_default();
        conn.execute(
            "INSERT INTO events (kind, payload, ts) VALUES (?1, ?2, ?3)",
            params![kind, payload_str, ts],
        )
        .map_err(|e| DaemonError::EventBus(e.to_string()))?;
        let id = conn.last_insert_rowid();
        debug!(kind, id, "event published");
        Ok(id)
    }

    /// Fetch unconsumed events of a given kind and mark them consumed atomically.
    ///
    /// Inputs:  `kind`  — event type string.
    ///          `limit` — maximum number of events to consume.
    /// Outputs: consumed events (empty when none pending).
    /// Constraints: fetches and marks consumed inside a single Mutex guard to
    ///   prevent double-delivery across concurrent callers.
    pub async fn consume(&self, kind: &str, limit: usize) -> Result<Vec<Event>, DaemonError> {
        let conn = self.db.lock().await;
        let mut stmt = conn
            .prepare(
                "SELECT id, kind, payload, ts FROM events
                 WHERE kind = ?1 AND consumed = 0
                 ORDER BY ts ASC LIMIT ?2",
            )
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        let rows = stmt
            .query_map(params![kind, limit as i64], |row| {
                let payload_str: String = row.get(2)?;
                Ok(Event {
                    id: row.get(0)?,
                    kind: row.get(1)?,
                    payload: serde_json::from_str(&payload_str).unwrap_or(serde_json::Value::Null),
                    ts: row.get(3)?,
                })
            })
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        let events: Vec<Event> = rows
            .collect::<Result<Vec<_>, _>>()
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        if !events.is_empty() {
            // Mark all fetched events consumed in a single UPDATE.
            let ids: Vec<String> = events.iter().map(|e| e.id.to_string()).collect();
            let placeholders = ids.join(",");
            conn.execute_batch(&format!(
                "UPDATE events SET consumed = 1 WHERE id IN ({placeholders})"
            ))
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;
            debug!(kind, count = events.len(), "events consumed");
        }

        Ok(events)
    }

    /// Fetch unconsumed events of a given kind. Does NOT mark them consumed.
    pub async fn peek(&self, kind: &str, limit: usize) -> Result<Vec<Event>, DaemonError> {
        let conn = self.db.lock().await;
        let mut stmt = conn
            .prepare(
                "SELECT id, kind, payload, ts FROM events
                 WHERE kind = ?1 AND consumed = 0
                 ORDER BY ts DESC LIMIT ?2",
            )
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        let rows = stmt
            .query_map(params![kind, limit as i64], |row| {
                let payload_str: String = row.get(2)?;
                Ok(Event {
                    id: row.get(0)?,
                    kind: row.get(1)?,
                    payload: serde_json::from_str(&payload_str).unwrap_or(serde_json::Value::Null),
                    ts: row.get(3)?,
                })
            })
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        rows.collect::<Result<Vec<_>, _>>()
            .map_err(|e| DaemonError::EventBus(e.to_string()))
    }

    /// Return current queue depth (unconsumed event count).
    pub async fn queue_depth(&self) -> u64 {
        let conn = self.db.lock().await;
        conn.query_row(
            "SELECT COUNT(*) FROM events WHERE consumed = 0",
            [],
            |row| row.get::<_, i64>(0),
        )
        .map(|n| n as u64)
        .unwrap_or(0)
    }

    /// Flush in-flight state before daemon exit.
    /// Currently a no-op (SQLite auto-flushes on drop) but exists as an
    /// explicit seam for future WAL checkpoint or state write.
    pub async fn flush(&self) -> Result<(), DaemonError> {
        let conn = self.db.lock().await;
        conn.execute_batch("PRAGMA wal_checkpoint(TRUNCATE);")
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;
        Ok(())
    }

    // ── IPC helpers ───────────────────────────────────────────────────────

    /// Return the last `limit` inbox items for the IPC `inbox_summary` method.
    pub async fn inbox_summary(&self, limit: usize) -> Result<Vec<serde_json::Value>, DaemonError> {
        let conn = self.db.lock().await;
        let mut stmt = conn
            .prepare(
                "SELECT project, path, summary, ts FROM inbox_items
                 ORDER BY ts DESC LIMIT ?1",
            )
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        let rows = stmt
            .query_map(params![limit as i64], |row| {
                Ok(serde_json::json!({
                    "project": row.get::<_, String>(0)?,
                    "path":    row.get::<_, String>(1)?,
                    "summary": row.get::<_, String>(2)?,
                    "ts":      row.get::<_, i64>(3)?,
                }))
            })
            .map_err(|e| DaemonError::EventBus(e.to_string()))?;

        rows.collect::<Result<Vec<_>, _>>()
            .map_err(|e| DaemonError::EventBus(e.to_string()))
    }

    /// Look up a hotword context block.
    pub async fn hotword_lookup(&self, word: &str) -> Result<Option<String>, DaemonError> {
        let conn = self.db.lock().await;
        let result = conn.query_row(
            "SELECT context_block FROM hotwords WHERE word = ?1",
            params![word],
            |row| row.get::<_, String>(0),
        );
        match result {
            Ok(block) => Ok(Some(block)),
            Err(rusqlite::Error::QueryReturnedNoRows) => Ok(None),
            Err(e) => Err(DaemonError::EventBus(e.to_string())),
        }
    }

    /// Record a quota snapshot in the persistent store (no-op stub — full
    /// implementation lives in P3 EventBus quota_snapshots table).
    ///
    /// The quota poller calls this after each successful poll so that quota
    /// history is durable across restarts. Returns the assigned row id (0 for
    /// this stub implementation).
    ///
    /// SPORT: .claude/docs/MASTER-DAEMON.md — quota_snapshots table (T-P2-E02-09)
    pub async fn record_quota_snapshot(
        &self,
        _snapshot: &serde_json::Value,
    ) -> Result<i64, DaemonError> {
        // Stub: returns 0 until the quota_snapshots table is added in P3.
        Ok(0)
    }

    /// Return provider quota summary from `~/.cascade/provider-quota.yaml`.
    /// The YAML is vendor-neutral — Tier labels only (T0/T1/T2/T3).
    pub async fn provider_quota(&self) -> Result<serde_json::Value, DaemonError> {
        let path = self.config_dir.join("provider-quota.yaml");
        let content = tokio::fs::read_to_string(&path).await.unwrap_or_default();
        // Parse with serde_yaml if available; for now return raw string so
        // the caller can display it without a heavy dep in daemon.
        Ok(serde_json::json!({ "raw": content }))
    }
}

fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[tokio::test]
    async fn publish_and_peek_roundtrip() {
        let dir = tempdir().unwrap();
        let bus = EventBus::new(dir.path().to_path_buf()).await.unwrap();

        let payload = serde_json::json!({ "msg": "hello" });
        let id = bus.publish("test", payload.clone()).await.unwrap();
        assert!(id > 0);

        let events = bus.peek("test", 10).await.unwrap();
        assert_eq!(events.len(), 1);
        assert_eq!(events[0].payload["msg"], "hello");
    }

    #[tokio::test]
    async fn queue_depth_counts_unconsumed() {
        let dir = tempdir().unwrap();
        let bus = EventBus::new(dir.path().to_path_buf()).await.unwrap();

        bus.publish("a", serde_json::Value::Null).await.unwrap();
        bus.publish("b", serde_json::Value::Null).await.unwrap();
        assert_eq!(bus.queue_depth().await, 2);
    }

    #[tokio::test]
    async fn hotword_lookup_missing_returns_none() {
        let dir = tempdir().unwrap();
        let bus = EventBus::new(dir.path().to_path_buf()).await.unwrap();
        let result = bus.hotword_lookup("nonexistent").await.unwrap();
        assert!(result.is_none());
    }
}
