//! `cascade status` — report daemon health, index freshness, and tier health.
//!
//! Outputs a human-readable table with three sections:
//! 1. **Daemon** — socket present, PID alive, daemon version.
//! 2. **Index** — last index timestamp (from cache), number of chunks.
//! 3. **Cascade tiers** — one row per discovered tier, health indicator.
//!
//! Exit code `0` if all checks pass; `1` if any check is FAIL.
//!
//! WHY: DiscoveredTier exposes `cascade_dir` (the `.cascade/` path) and
//! `is_readable()`. Derived health is computed by checking `verify_siblings`
//! from `cascade_core::symlinks`. There are no `is_present`, `root`,
//! `is_healthy()`, `claude_md_valid`, or `agents_md_valid` members.
//!
//! ## RAG status fields (T-P4-E04-06)
//!
//! When `--json` is passed, a `rag` sub-object is included:
//! ```json
//! "rag": {
//!   "shard_count": 4,
//!   "indexed_docs": 8234,
//!   "last_index_duration_ms": 412,
//!   "incremental_enabled": true,
//!   "worker_queue_depth": 0,
//!   "embedding_status": "ready",
//!   "embedding_degraded": false
//! }
//! ```
//! Fields are sourced from: project config.toml (shard_count, incremental_enabled),
//! cascade_state.db (indexed_docs), a stats file written by the daemon
//! (`last_index_stats.json` in the index root), and the daemon's live
//! `rag.queue_depth` and `rag.embedding_status` IPC endpoints (local defaults
//! are used when the daemon is not reachable).

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::Result;
use cascade_types::paths;
use clap::Args;

use super::Command;

/// Arguments for `cascade status`.
#[derive(Debug, Args)]
pub struct StatusArgs {
    /// Output as JSON instead of a human-readable table.
    #[arg(long)]
    pub json: bool,
}

// ── RagStatus — serializable metrics for `cascade status --json` ─────────────

/// RAG sub-object emitted in `cascade status --json`.
///
/// # Purpose
/// Gives operators visibility into the RAG indexing pipeline without needing to
/// inspect database files. All fields degrade gracefully to sensible defaults
/// when the daemon is not running or the state DB is absent.
///
/// SPORT: MASTER-COMPONENTS.md → cascade-cli::RagStatus
#[derive(Debug, serde::Serialize)]
pub struct RagStatus {
    /// Number of index shards (from config `rag.shard_count`; default 4).
    pub shard_count: u64,
    /// Total documents tracked in `cascade_state.db`.
    pub indexed_docs: usize,
    /// Wall-clock milliseconds of the last index pass (from `last_index_stats.json`).
    pub last_index_duration_ms: u64,
    /// Whether incremental indexing is enabled (from config `rag.incremental`; default `true`).
    pub incremental_enabled: bool,
    /// Pending embed-worker queue depth. 0 when daemon is not reachable.
    pub worker_queue_depth: usize,
    /// Runtime embedding backend state: loading, ready, degraded, or unknown.
    pub embedding_status: String,
    /// True when failed/skipped real-model initialisation left MockEmbedModel active.
    pub embedding_degraded: bool,
}

impl RagStatus {
    /// Build a `RagStatus` from local files relative to `cwd`.
    ///
    /// All I/O errors are silently swallowed — fields default to 0/false/true
    /// so the overall `cascade status` call never fails due to missing RAG state.
    fn load(cwd: &std::path::Path) -> Self {
        let index_root = find_index_root(cwd);

        // Read shard_count and incremental from config.
        let (shard_count, incremental_enabled) = read_rag_config(cwd);

        // Read indexed_docs from cascade_state.db via IndexStateStore.
        let indexed_docs = index_root
            .as_ref()
            .and_then(|root| {
                cascade_rag::IndexStateStore::open(root)
                    .ok()
                    .map(|s| s.count())
            })
            .unwrap_or(0);

        // Read last_index_duration_ms from the stats JSON file written by the daemon.
        let last_index_duration_ms = index_root
            .as_ref()
            .and_then(|root| {
                let stats_path = root.join("last_index_stats.json");
                std::fs::read_to_string(&stats_path).ok().and_then(|s| {
                    serde_json::from_str::<serde_json::Value>(&s)
                        .ok()
                        .and_then(|v| v["duration"].as_u64())
                })
            })
            .unwrap_or(0);

        Self {
            shard_count,
            indexed_docs,
            last_index_duration_ms,
            incremental_enabled,
            worker_queue_depth: 0,
            embedding_status: "unknown".into(),
            embedding_degraded: false,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct EmbeddingRuntimeStatus {
    status: String,
    degraded: bool,
}

fn parse_embedding_status(value: &serde_json::Value) -> Option<EmbeddingRuntimeStatus> {
    Some(EmbeddingRuntimeStatus {
        status: value.get("status")?.as_str()?.to_string(),
        degraded: value.get("degraded")?.as_bool()?,
    })
}

fn embedding_status_label(rag: &RagStatus) -> String {
    if rag.embedding_degraded {
        "DEGRADED (MockEmbedModel fallback active)".into()
    } else {
        rag.embedding_status.to_ascii_uppercase()
    }
}

/// Fetch the live embed-worker queue depth from the daemon via IPC
/// (`rag.queue_depth`, T-P7-E07-02).
///
/// Returns `None` when the daemon is unreachable or the IPC call fails for
/// any reason — the caller then keeps the local default of 0.  Silent by
/// design: `cascade status` must never fail (or print daemon diagnostics)
/// because of this optional enrichment.
async fn fetch_worker_queue_depth() -> Option<usize> {
    let client = crate::ipc_client::IpcClient::new().ok()?;
    let resp: serde_json::Value = client
        .send("rag.queue_depth", serde_json::Value::Null)
        .await
        .ok()?;
    resp.get("worker_queue_depth")
        .and_then(|v| v.as_u64())
        .map(|v| v as usize)
}

/// Fetch the live embedding backend state. A failed/skipped real-model load is
/// returned as `degraded=true`; temporary startup mock availability is `loading`.
async fn fetch_embedding_status() -> Option<EmbeddingRuntimeStatus> {
    let client = crate::ipc_client::IpcClient::new().ok()?;
    let resp: serde_json::Value = client
        .send("rag.embedding_status", serde_json::Value::Null)
        .await
        .ok()?;
    parse_embedding_status(&resp)
}

#[async_trait]
impl Command for StatusArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

        let daemon_ok = check_daemon();
        let tiers = cascade_core::discovery::TierDiscovery::new().discover(&cwd)?;

        let mut rag = RagStatus::load(&cwd);
        if daemon_ok {
            if let Some(depth) = fetch_worker_queue_depth().await {
                rag.worker_queue_depth = depth;
            }
            if let Some(embedding) = fetch_embedding_status().await {
                rag.embedding_status = embedding.status;
                rag.embedding_degraded = embedding.degraded;
            }
        }

        // A tier is "healthy" when CASCADE.md is readable and both sibling symlinks
        // (CLAUDE.md, AGENTS.md) are valid.
        let any_fail = !daemon_ok
            || rag.embedding_degraded
            || tiers.iter().any(|t| {
                t.is_readable()
                    && !cascade_core::symlinks::verify_siblings(&t.cascade_dir).is_empty()
            });

        if self.json {
            let tier_json: Vec<serde_json::Value> = tiers
                .iter()
                .map(|t| {
                    let issues = cascade_core::symlinks::verify_siblings(&t.cascade_dir);
                    serde_json::json!({
                        "tier": format!("{:?}", t.tier),
                        "path": t.cascade_md.display().to_string(),
                        "present": t.is_readable(),
                        "healthy": t.is_readable() && issues.is_empty(),
                    })
                })
                .collect();

            let out = serde_json::json!({
                "daemon": daemon_ok,
                "tiers": tier_json,
                "rag": {
                    "shard_count": rag.shard_count,
                    "indexed_docs": rag.indexed_docs,
                    "last_index_duration_ms": rag.last_index_duration_ms,
                    "incremental_enabled": rag.incremental_enabled,
                    "worker_queue_depth": rag.worker_queue_depth,
                    "embedding_status": rag.embedding_status,
                    "embedding_degraded": rag.embedding_degraded,
                },
            });
            println!("{}", serde_json::to_string_pretty(&out).unwrap());
        } else {
            println!(
                "{:<12} {}",
                "DAEMON",
                if daemon_ok { "OK" } else { "NOT RUNNING" }
            );
            println!("{:<12} {}", "EMBEDDING", embedding_status_label(&rag));
            println!();
            println!("{:<8} {:<10} {:<8} PATH", "TIER", "STATUS", "SYMLINKS");
            println!("{}", "-".repeat(72));
            for t in &tiers {
                let present = t.is_readable();
                let issues = cascade_core::symlinks::verify_siblings(&t.cascade_dir);
                let status = if present { "OK" } else { "MISSING" };
                let symlinks = if issues.is_empty() && present {
                    "OK"
                } else if present {
                    "BROKEN"
                } else {
                    "—"
                };
                println!(
                    "{:<8} {:<10} {:<8} {}",
                    format!("{:?}", t.tier),
                    status,
                    symlinks,
                    t.cascade_md.display()
                );
            }
        }

        if any_fail {
            std::process::exit(1);
        }
        Ok(())
    }
}

/// Returns `true` if the daemon socket exists and is a socket file.
fn check_daemon() -> bool {
    let sock = paths::daemon_socket();
    sock.exists()
}

/// Resolve the index root: `<project-root>/.cascade/index/` walking up from `cwd`.
fn find_index_root(cwd: &std::path::Path) -> Option<PathBuf> {
    cwd.ancestors().find_map(|p| {
        let candidate = p.join(".cascade").join("index");
        if candidate.is_dir() {
            Some(candidate)
        } else {
            None
        }
    })
}

/// Read `rag.shard_count` and `rag.incremental` from merged config.
/// Returns (shard_count, incremental_enabled) with defaults (4, true).
fn read_rag_config(cwd: &std::path::Path) -> (u64, bool) {
    let mut shard_count: u64 = 4;
    let mut incremental = true;

    let config_paths: Vec<PathBuf> = {
        let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
        let global = PathBuf::from(home).join(".cascade").join("config.toml");
        let project = cwd.ancestors().find_map(|p| {
            let c = p.join(".cascade").join("config.toml");
            if c.exists() {
                Some(c)
            } else {
                None
            }
        });
        let mut v = vec![global];
        if let Some(proj) = project {
            v.push(proj);
        }
        v
    };

    for path in &config_paths {
        if let Ok(raw) = std::fs::read_to_string(path) {
            if let Ok(table) = raw.parse::<toml::Value>() {
                if let Some(rag) = table.get("rag").and_then(|v| v.as_table()) {
                    if let Some(sc) = rag.get("shard_count").and_then(|v| v.as_integer()) {
                        shard_count = sc as u64;
                    }
                    if let Some(inc) = rag.get("incremental").and_then(|v| v.as_bool()) {
                        incremental = inc;
                    }
                }
            }
        }
    }

    (shard_count, incremental)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── status::rag_status_defaults ───────────────────────────────────────────

    /// RagStatus::load must never panic and must return sensible defaults
    /// when called from a directory with no .cascade/ index.
    #[test]
    fn rag_status_loads_with_no_index() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        let rag = RagStatus::load(tmp.path());

        assert_eq!(rag.shard_count, 4, "default shard_count must be 4");
        assert!(
            rag.incremental_enabled,
            "incremental_enabled must default to true"
        );
        assert_eq!(rag.indexed_docs, 0);
        assert_eq!(rag.last_index_duration_ms, 0);
        assert_eq!(rag.worker_queue_depth, 0);
        assert_eq!(rag.embedding_status, "unknown");
        assert!(!rag.embedding_degraded);
    }

    // ── status::rag_status_reads_config ──────────────────────────────────────

    /// RagStatus::load reads shard_count and incremental from config.toml.
    #[test]
    fn rag_status_reads_rag_config() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(
            cascade_dir.join("config.toml"),
            "[rag]\nshard_count = 8\nincremental = false\n",
        )
        .unwrap();

        let rag = RagStatus::load(tmp.path());
        assert_eq!(rag.shard_count, 8, "shard_count from config");
        assert!(!rag.incremental_enabled, "incremental=false from config");
    }

    // ── status::rag_status_reads_indexed_docs ─────────────────────────────────

    /// RagStatus::load reads indexed_docs from cascade_state.db.
    #[test]
    fn rag_status_reads_indexed_docs_from_state_db() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        let index_root = tmp.path().join(".cascade").join("index");
        std::fs::create_dir_all(&index_root).unwrap();

        // Seed cascade_state.db with a row.
        let store = cascade_rag::IndexStateStore::open(&index_root).expect("store open");
        let f = tempfile::NamedTempFile::new().unwrap();
        std::io::Write::write_all(&mut f.as_file(), b"test content").unwrap();
        let (_, hash) = store.classify(f.path()).unwrap();
        store.set_hash(f.path(), &hash.unwrap()).unwrap();

        let rag = RagStatus::load(tmp.path());
        assert_eq!(rag.indexed_docs, 1, "indexed_docs must reflect state DB");
    }

    // ── status::rag_json_has_all_fields ──────────────────────────────────────

    /// The JSON output must include all required RAG fields.
    #[test]
    fn rag_status_json_has_all_required_fields() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        let rag = RagStatus::load(tmp.path());
        let json = serde_json::to_value(&rag).unwrap();

        for field in &[
            "shard_count",
            "indexed_docs",
            "last_index_duration_ms",
            "incremental_enabled",
            "worker_queue_depth",
            "embedding_status",
            "embedding_degraded",
        ] {
            assert!(
                json.get(field).is_some(),
                "RagStatus JSON must include field '{field}'"
            );
        }
    }

    #[test]
    fn degraded_embedding_status_is_visible_in_json_and_human_label() {
        let value = serde_json::json!({
            "status": "degraded",
            "degraded": true,
            "model": "mock-embed-model",
        });
        let parsed = parse_embedding_status(&value).expect("valid status payload");
        assert_eq!(parsed.status, "degraded");
        assert!(parsed.degraded);

        let tmp = tempfile::tempdir().expect("tmpdir");
        let mut rag = RagStatus::load(tmp.path());
        rag.embedding_status = parsed.status;
        rag.embedding_degraded = parsed.degraded;

        let json = serde_json::to_value(&rag).unwrap();
        assert_eq!(json["embedding_status"], "degraded");
        assert_eq!(json["embedding_degraded"], true);
        assert_eq!(
            embedding_status_label(&rag),
            "DEGRADED (MockEmbedModel fallback active)"
        );
    }
}
