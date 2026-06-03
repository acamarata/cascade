# Daemon Architecture

This page documents the internal architecture of `cascaded`, the cascade daemon process.

---

## Overview

`cascaded` is a Tokio-based Rust daemon that runs as a user-session process (no admin
elevation). It owns the event bus, IPC socket, and health state. The CLI (`cascade`)
connects to `cascaded` over a Unix socket to query status, inject inbox alerts, and
fetch quota summaries.

---

## IPC HealthSnapshot Contract

**FROZEN after T-P2-E02-04. Do not rename or remove fields — E3 IPC handler and E4
native widgets depend on this exact JSON shape.**

The `health` IPC method returns a `HealthSnapshot` serialized to JSON:

```json
{
  "status": "ok",
  "pid": 12345,
  "uptime_secs": 3600,
  "queue_depth": 0,
  "ram_kb": 4096,
  "cpu_pct": 0.5,
  "index_freshness_secs": -1
}
```

### Field reference

| Field | Type | Description |
|---|---|---|
| `status` | `string` | Always `"ok"` while the daemon is running. Reserved for future `"degraded"`/`"unhealthy"` states. |
| `pid` | `number` | OS process ID of the daemon. |
| `uptime_secs` | `number` | Seconds elapsed since the daemon started. |
| `queue_depth` | `number` | Count of unconsumed events in the event bus. |
| `ram_kb` | `number` | Resident set size of the daemon process in kibibytes (0 on Windows). |
| `cpu_pct` | `number` | CPU utilization percentage at the last sample. 0.0 on Windows or when a two-sample delta is not available. |
| `index_freshness_secs` | `number` | Seconds since the RAG index was last rebuilt. **-1** = not yet indexed (default on startup). Updated by the RAG indexer (E6) via `HealthState::update_index_freshness()`. |

### Consumers

- `cascade status` (E1/E3 CLI) — renders the snapshot as terminal output.
- E4 native OS widgets — poll the IPC socket and display a subset of these fields.
- Integration tests — assert exact field names and default values.

---

## Component Map

| Component | File | Notes |
|---|---|---|
| `HealthState` | `crates/cascade-daemon/src/healthcheck.rs` | Arc-wrapped; shared into IPC handler and sampler task. |
| `HealthSnapshot` | `crates/cascade-daemon/src/healthcheck.rs` | Frozen IPC contract (see above). |
| `EventBus` | `crates/cascade-daemon/src/event_bus.rs` | SQLite WAL event queue; provides `queue_depth()`. |
| `IpcServer` | `crates/cascade-daemon/src/ipc.rs` | Unix socket server; serves `health`, `inbox_summary`, etc. |
| `supervisor::run` | `crates/cascade-daemon/src/supervisor.rs` | Daemon main loop; wires health + bus + IPC together. |

---

## Health Sampling

The background sampler (`healthcheck::sampler`) runs every `config.daemon.health_sample_interval_secs`
seconds (default 30, range 5–3600). Each tick it:

1. Calls `sample_resources()` to read RAM and CPU from the OS (macOS: `ps`; Linux: `/proc/self/status`; Windows: returns zeros).
2. Calls `EventBus::queue_depth()` for the live unconsumed event count.
3. Calls `HealthState::update(queue_depth, ram_kb, cpu_pct, -1)` — passing `-1` for `index_freshness_secs` so the value set by the RAG indexer is preserved across ticks.

The RAG indexer (E6) calls `HealthState::update_index_freshness(secs)` independently after
each index rebuild.

---

## Quota History Persistence

The daemon maintains a persistent SQLite table `quota_history` to enable P3 analytics and quota 
state trending. This table is managed by the `EventBus` and populated by the quota poller.

### quota_history table schema

```sql
CREATE TABLE IF NOT EXISTS quota_history (
  id      INTEGER PRIMARY KEY,
  ts      INTEGER NOT NULL DEFAULT (unixepoch()),
  payload TEXT    NOT NULL
);
```

| Column | Type | Description |
|---|---|---|
| `id` | INTEGER PRIMARY KEY | Auto-incrementing row ID. |
| `ts` | INTEGER | Unix timestamp (seconds) when the snapshot was recorded. Default: current time via `unixepoch()`. |
| `payload` | TEXT | JSON blob containing the full quota state (e.g., `QuotaState` serialized). |

### Methods

| Method | Signature | Purpose |
|---|---|---|
| `record_quota_snapshot` | `async fn(&self, payload: &serde_json::Value) -> Result<i64, DaemonError>` | Insert a quota snapshot and return the inserted rowid. Uses `spawn_blocking` to execute the synchronous SQLite query. Called by the quota poller after publishing a `quota.polled` event. |
| `prune_history` | `async fn(&self, retention_days: u32) -> Result<u64, DaemonError>` | Delete all quota history rows older than `retention_days * 86400` seconds. Returns the count of deleted rows. Special case: if `retention_days == 0`, returns `Ok(0)` immediately without deleting (keep forever). Called at daemon startup from `supervisor::run()` with the configured retention policy. |

### Lifecycle

1. **Startup** — `supervisor::run()` initializes the `EventBus`, which creates the `quota_history` table via DDL in `EventBus::new()`.
2. **Prune at startup** — `supervisor::run()` calls `bus.prune_history(config.history.retention_days)` to remove old entries per the configured retention policy.
3. **Record on each poll** — Every time `quota_poller` successfully polls quota state, it calls `bus.record_quota_snapshot(&quota_state_json)` to persist the state.
4. **P3 analytics** — P3 adds dashboard endpoints to query quota history for trending and visualization.

### Configuration

The retention policy is controlled via the config file:

```toml
[history]
retention_days = 90  # Keep 90 days of quota history; 0 = keep forever
```

---

---

## quota-store.json Schema (T-P2-E02-29)

`~/.cascade/quota-store.json` is the canonical file-based single source of truth for all
quota and usage data. It is written atomically by the daemon aggregator (T-P2-E02-30) and
read by the fleet widget, round-robin scheduler, and P3 analytics — without requiring the
daemon to be running.

### File location

`~/.cascade/quota-store.json`

### Top-level shape

```json
{
  "schema_version": 1,
  "updated_at": "2026-06-02T12:00:00Z",
  "accounts": [...],
  "week_totals": { "claude-sonnet-4-6": 1000 },
  "month_totals": { "claude-sonnet-4-6": 4000 },
  "rolling_history": [...]
}
```

| Field | Type | Description |
|---|---|---|
| `schema_version` | `u32` | Must equal `QUOTA_STORE_SCHEMA_VERSION` (currently `1`). Validated on every read. |
| `updated_at` | `string` (ISO-8601) | Timestamp of the last write. |
| `accounts` | `AccountEntry[]` | One entry per AI account across all harnesses. |
| `week_totals` | `map<string, u64>` | Cross-account tokens/requests used this week, keyed by model ID. |
| `month_totals` | `map<string, u64>` | Same but for the current calendar month. |
| `rolling_history` | `HistoryEntry[]` | Snapshots trimmed to `retention_days`. |

### AccountEntry

```json
{
  "account_id": "cc-acct1",
  "harness": "cc",
  "models": {
    "claude-sonnet-4-6": {
      "used": 1000,
      "limit": 10000,
      "reset_at": "2026-07-01T00:00:00Z",
      "pct_used": 10.0
    }
  },
  "week_total_used": 1000,
  "month_total_used": 4000,
  "last_polled": "2026-06-02T12:00:00Z"
}
```

| Field | Type | Description |
|---|---|---|
| `account_id` | `string` | Stable identifier (e.g. `"cc-acct1"`). Key used by the scheduler. |
| `harness` | `string` | `"cc"`, `"oc"`, `"codex"`, or `"cursor"`. |
| `models` | `map<string, ModelUsage>` | Per-model usage snapshot. |
| `week_total_used` | `u64` | Total usage this week. |
| `month_total_used` | `u64` | Total usage this month. |
| `last_polled` | `string` (ISO-8601) | Most recent successful poll time. |

### ModelUsage

| Field | Type | Description |
|---|---|---|
| `used` | `u64` | Tokens/requests consumed in the current window. |
| `limit` | `u64?` | Hard limit (omitted if unknown). |
| `reset_at` | `string?` | ISO-8601 reset timestamp (omitted if unknown). |
| `pct_used` | `f32?` | Percentage of `limit` consumed; omitted when `limit` is unknown. Invariant: `0.0..=100.0`. |

### HistoryEntry

| Field | Type | Description |
|---|---|---|
| `ts` | `i64` | Unix epoch seconds of snapshot capture. |
| `accounts_snapshot` | `AccountEntry[]` | Full account state at that moment. |

### Atomic write guarantee

The writer (`write_quota_store`) serializes to `quota-store.json.tmp` then renames it to
`quota-store.json` atomically (`std::fs::rename`). No reader ever observes a partial write.

### Schema version migration

When `schema_version` in the file does not match `QUOTA_STORE_SCHEMA_VERSION`, `read_quota_store`
returns `CascadeError::SchemaMismatch`. The daemon (T-P2-E02-31) is responsible for
regenerating the store on mismatch; readers should treat it as "no data yet".

### Implementation

- **Types:** `crates/cascade-types/src/quota_store.rs`
- **I/O:** `crates/cascade-core/src/quota_store.rs`
- **Re-exports:** `cascade_types::QuotaStore` · `cascade_core::{read_quota_store, write_quota_store}`

*Last updated: T-P2-E02-29*

---

## RoutingTable — per-provider round-robin state (T-P2-E02-35)

`RoutingTable` is the pure data-structure and algorithm layer for multi-provider Gemini routing.
It holds per-provider slot state (enabled/disabled, 429 cooldown timestamp, request count) and
a round-robin cursor. No I/O, no async — the caller wraps it in `Arc<Mutex<RoutingTable>>` for
use across async tasks.

### Location

`crates/cascade-core/src/routing_table.rs`
Re-exported at crate root: `cascade_core::{RoutingTable, ProviderSlot, RouteResult}`

### Types

| Type | Description |
|---|---|
| `ProviderSlot` | Per-slot state: `id`, `display_name`, `enabled`, `re_enable_at: Option<Instant>`, `request_count: u64` |
| `RouteResult` | Result of a successful `pick_next` call: `slot_id`, `display_name` |
| `RoutingTable` | Table holding `Vec<ProviderSlot>` + `cursor: usize` for round-robin |

### Construction

```rust
let table = RoutingTable::new(&providers);
// Filters to entries where enabled == true AND harness == "gemini".
// cursor starts at 0.
```

### Methods

| Method | Signature | Behaviour |
|---|---|---|
| `new` | `(&[ProviderEntry]) -> Self` | Build table; filter to enabled gemini entries; cursor = 0 |
| `pick_next` | `(&mut self) -> Option<RouteResult>` | `tick_cooldowns` first; scan for first enabled slot from cursor; advance cursor; increment `request_count`; return `None` if all disabled |
| `mark_rate_limited` | `(&mut self, slot_id: &str, cooldown_secs: u64)` | Disable slot and set `re_enable_at = now + duration` |
| `tick_cooldowns` | `(&mut self)` | Re-enable all slots whose `re_enable_at <= now`; called automatically inside `pick_next` |
| `slot_count` | `(&self) -> usize` | Number of slots (enabled or not); useful for tests |

### Thread-safety

`RoutingTable` is `!Sync` on its own. Wrap in `Arc<Mutex<RoutingTable>>` before sharing across
async tasks. `pick_next` takes `&mut self` — the caller holds the mutex for the duration:

```rust
let result = {
    let mut table = routing_table.lock().unwrap();
    table.pick_next()
};
```

### Unit tests

All four required tests pass (`cargo test -p cascade-core -- routing_table::tests`):

| Test | Verifies |
|---|---|
| `round_robin_order` | 3 slots cycle `[0, 1, 2, 0, 1, 2]` across 6 calls |
| `skip_rate_limited` | Disabled slot-1 is skipped; order becomes `slot-0, slot-2, slot-0` |
| `cooldown_reactivation` | 0-second cooldown + `tick_cooldowns` immediately re-enables the slot |
| `all_disabled` | All slots rate-limited → `pick_next` returns `None` |

*Last updated: T-P2-E02-35*

---

## Gemini Proxy — RoutingTable-backed HTTP proxy (T-P2-E02-36)

The `proxy` module (`crates/cascade-daemon/src/proxy/`) provides an HTTP proxy server that
routes incoming requests to Gemini upstream using `RoutingTable`-based round-robin dispatch.
On HTTP 429 from upstream, the slot is marked rate-limited and the request retries with the
next available provider. When all retries are exhausted the proxy returns HTTP 503.

### Location

`crates/cascade-daemon/src/proxy/gemini_proxy.rs`

### Configuration (`[proxy]` section in `config.toml`)

| Field | Default | Description |
|---|---|---|
| `cooldown_secs` | `60` | Seconds a slot stays rate-limited after receiving HTTP 429 |
| `max_retries` | `3` | Max retry attempts before returning 503 to client |

### Types

| Type | Description |
|---|---|
| `GeminiProxy` | Main struct; construct with `new()`, run with `run()` |
| `ProxyState` | Shared mutable state: `Arc<Mutex<RoutingTable>>` + credentials map + `ProxyConfig` + upstream base URL |
| `ProxyError` | Error variants: `NoProvidersAvailable`, `AllProvidersExhausted{attempts}`, `Upstream(reqwest::Error)`, `Listener(io::Error)` |

### Key functions

| Function | Description |
|---|---|
| `GeminiProxy::new` | Load routing state from `providers.json`; build `ProxyState`; bind address |
| `GeminiProxy::run` | Spawn rebuild task + HTTP server; block until shutdown |
| `dispatch_request` | Pick slot via `pick_next()`; forward to upstream; 429 → `mark_rate_limited` + retry |
| `build_routing_state` | Load `providers.json`; build `RoutingTable` + credentials map |
| `build_upstream_url` | Append `?key={api_key}` (or `&key=`) to upstream path |

### Routing-table rebuild

A background task (`rebuild_task`) polls the event bus for `providers.updated` events every
5 seconds. When an event arrives, `providers.json` is re-read and both `state.table` and
`state.credentials` are replaced under their Mutex guards — without restarting the proxy.

### 429 retry loop

The `dispatch_request` function follows this protocol:

1. `pick_next()` — acquires Mutex, picks next slot, releases Mutex BEFORE await.
2. Forward request to upstream using `slot.account_id` as the API key.
3. On HTTP 429: `mark_rate_limited(slot_id, cooldown_secs)` — mark BEFORE retry so the
   exhausted slot is skipped immediately on the next `pick_next()` call.
4. Increment `attempts`. If `attempts >= max_retries` → return `AllProvidersExhausted`.
5. If `pick_next()` returns `None` after some attempts → return `AllProvidersExhausted`.
6. If `pick_next()` returns `None` with zero attempts → return `NoProvidersAvailable`.

### Thread-safety contract

The `Mutex<RoutingTable>` is NEVER held across an `.await` point. The pattern everywhere:

```rust
let slot = {
    let mut guard = state.table.lock().unwrap();
    guard.pick_next()
};
// await happens HERE, outside the lock
```

### Unit tests (8 passing)

| Test | Verifies |
|---|---|
| `test_429_fallback_to_200` | Provider 0 returns 429; provider 1 returns 200; client receives 200 |
| `test_all_providers_exhausted_returns_error` | All providers 429; AllProvidersExhausted returned |
| `test_providers_updated_triggers_rebuild` | providers.updated event triggers routing table rebuild |
| `test_proxy_config_defaults` | cooldown_secs=60, max_retries=3 from Config::default() |
| `test_proxy_config_from_toml` | Custom values parsed from TOML |
| `test_build_upstream_url_no_query` | `?key=` appended when no existing query |
| `test_build_upstream_url_with_existing_query` | `&key=` appended when query already present |
| `test_build_routing_state_missing_file` | Missing providers.json returns empty table + map |

*Last updated: T-P2-E02-36*
