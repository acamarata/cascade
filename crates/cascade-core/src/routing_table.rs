//! Routing table — per-provider state, round-robin cursor, 429 cooldown.
//!
//! Purpose: pure data-structure and algorithm layer for multi-provider round-robin
//! routing. Holds enabled/disabled state per provider, tracks a cooldown timestamp
//! for providers that returned HTTP 429, and exposes a `pick_next()` cursor that
//! advances atomically across enabled slots.
//!
//! Inputs:  `&[ProviderEntry]` (from providers_store — callers build the table at
//!          daemon start and rebuild on providers.json change).
//! Outputs: `Option<RouteResult>` from `pick_next()`; None when all slots are
//!          rate-limited.
//! Constraints: no I/O, no network, no async. All timing uses `std::time::Instant`.
//!              The caller must wrap `RoutingTable` in `Arc<Mutex<RoutingTable>>` for
//!              use across async tasks — see SPORT note below.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — RoutingTable, ProviderSlot, RouteResult
//!        (T-P2-E02-35)

use std::collections::VecDeque;
use std::time::{Duration, Instant};

use crate::providers_store::ProviderEntry;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single routing slot, derived from a [`ProviderEntry`].
///
/// Tracks mutable per-provider state: enabled flag, optional re-enable timestamp
/// after a 429 cooldown, and per-provider metrics (request count, error count,
/// rate-limit count, and latency distribution).
#[derive(Debug, Clone)]
pub struct ProviderSlot {
    /// Stable identifier matching [`ProviderEntry::id`].
    pub id: String,
    /// Human-readable display name for logging.
    pub display_name: String,
    /// Whether this slot is currently eligible for `pick_next`.
    /// Set to `false` by `mark_rate_limited`; restored by `tick_cooldowns`.
    pub enabled: bool,
    /// When this slot should be re-enabled after a 429 cooldown.
    /// `None` means either never rate-limited or the cooldown has elapsed.
    pub re_enable_at: Option<Instant>,
    /// Total successful `pick_next` selections for this slot.
    pub request_count: u64,
    /// Total number of non-429 errors encountered.
    pub error_count: u64,
    /// Total number of rate-limit (429) errors encountered.
    pub rate_limit_count: u64,
    /// Ring buffer of recent request latencies (in milliseconds).
    /// Bounded to 100 entries using a VecDeque.
    pub latency_ring: VecDeque<u64>,
}

/// The result returned by a successful `pick_next` call.
///
/// Carries the slot id and display name so the caller can attach the correct
/// credentials and log the selected provider without holding the table lock.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RouteResult {
    /// The [`ProviderSlot::id`] of the selected provider.
    pub slot_id: String,
    /// The [`ProviderSlot::display_name`] of the selected provider.
    pub display_name: String,
}

/// Snapshot of provider metrics for reporting via IPC.
///
/// Computed on-demand from the current state of a [`ProviderSlot`].
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct ProviderMetricsSnapshot {
    /// The slot ID.
    pub slot_id: String,
    /// The display name.
    pub display_name: String,
    /// Total requests routed to this provider.
    pub total_requests: u64,
    /// Number of non-429 errors.
    pub error_count: u64,
    /// Number of 429 rate-limit errors.
    pub rate_limit_count: u64,
    /// P50 latency in milliseconds, or None if no latencies recorded.
    pub p50_ms: Option<u64>,
    /// P95 latency in milliseconds, or None if no latencies recorded.
    pub p95_ms: Option<u64>,
    /// Whether the slot is currently enabled for routing.
    pub enabled: bool,
}

/// Round-robin routing table for multi-provider dispatch.
///
/// Built from a slice of [`ProviderEntry`] values (gemini harness only; callers
/// may filter before construction or rely on [`RoutingTable::new`] filtering).
/// The table is not `Send + Sync` on its own — wrap in `Arc<Mutex<RoutingTable>>`
/// or `Arc<RwLock<RoutingTable>>` before sharing across async tasks.
///
/// # Thread-safety note
///
/// `pick_next` takes `&mut self` to advance the cursor and update `request_count`.
/// The caller must hold the mutex for the duration of the call:
///
/// ```rust,ignore
/// let result = {
///     let mut table = routing_table.lock().unwrap();
///     table.pick_next()
/// };
/// ```
pub struct RoutingTable {
    slots: Vec<ProviderSlot>,
    /// Next candidate index for round-robin. Wraps modulo `slots.len()`.
    cursor: usize,
}

// ── impl ─────────────────────────────────────────────────────────────────────

impl RoutingTable {
    /// Build a new [`RoutingTable`] from a slice of provider entries.
    ///
    /// Inputs:  `providers` — all configured providers (any harness).
    /// Outputs: table populated with slots for entries where `enabled == true`
    ///          AND `harness == "gemini"`. Cursor starts at 0.
    /// Constraints: order of slots matches order in `providers` slice after
    ///              filtering — callers should sort before construction if they
    ///              want a stable order.
    pub fn new(providers: &[ProviderEntry]) -> Self {
        let slots = providers
            .iter()
            .filter(|p| p.enabled && p.harness == "gemini")
            .map(|p| ProviderSlot {
                id: p.id.clone(),
                display_name: p.display_name.clone(),
                enabled: true,
                re_enable_at: None,
                request_count: 0,
                error_count: 0,
                rate_limit_count: 0,
                latency_ring: VecDeque::new(),
            })
            .collect();

        Self { slots, cursor: 0 }
    }

    /// Pick the next available provider using round-robin selection.
    ///
    /// Inputs:  none (reads `self.slots` and `self.cursor`).
    /// Outputs: `Some(RouteResult)` for the next enabled slot, or `None` if all
    ///          slots are currently rate-limited.
    /// Constraints:
    ///   - `tick_cooldowns` is called first to re-enable any slots whose
    ///     cooldown has elapsed (caller does NOT need to call it separately).
    ///   - The cursor advances past the chosen slot (wraps modulo `slots.len()`).
    ///   - `request_count` on the chosen slot increments by 1.
    ///   - If the slots Vec is empty, returns `None` immediately.
    pub fn pick_next(&mut self) -> Option<RouteResult> {
        if self.slots.is_empty() {
            return None;
        }

        // Re-enable any slots whose cooldown has elapsed before scanning.
        self.tick_cooldowns();

        let len = self.slots.len();

        // Scan at most `len` slots starting from `cursor`.
        for offset in 0..len {
            let idx = (self.cursor + offset) % len;
            if self.slots[idx].enabled {
                // Advance cursor past this slot for the next call.
                self.cursor = (idx + 1) % len;
                self.slots[idx].request_count += 1;
                return Some(RouteResult {
                    slot_id: self.slots[idx].id.clone(),
                    display_name: self.slots[idx].display_name.clone(),
                });
            }
        }

        // No enabled slot found.
        None
    }

    /// Mark a provider as rate-limited, disabling it for `cooldown_secs` seconds.
    ///
    /// Inputs:  `slot_id` — must match a [`ProviderSlot::id`]; no-op if not found.
    ///          `cooldown_secs` — duration until automatic re-enable. `0` means
    ///          the slot is disabled until the next `tick_cooldowns` call clears it
    ///          (i.e. immediately eligible on the next call to `pick_next`).
    /// Outputs: mutates the matching slot's `enabled` and `re_enable_at`.
    pub fn mark_rate_limited(&mut self, slot_id: &str, cooldown_secs: u64) {
        if let Some(slot) = self.slots.iter_mut().find(|s| s.id == slot_id) {
            slot.enabled = false;
            slot.re_enable_at = Some(Instant::now() + Duration::from_secs(cooldown_secs));
        }
    }

    /// Scan all slots and re-enable any whose `re_enable_at` timestamp has passed.
    ///
    /// Inputs:  none (reads `self.slots`).
    /// Outputs: mutates `enabled = true` and `re_enable_at = None` for each slot
    ///          where `re_enable_at <= Instant::now()`.
    /// Constraints: called automatically at the start of every `pick_next()` call;
    ///              callers may also invoke it directly (e.g. in a background task).
    pub fn tick_cooldowns(&mut self) {
        let now = Instant::now();
        for slot in self.slots.iter_mut() {
            if let Some(re_enable_at) = slot.re_enable_at {
                if re_enable_at <= now {
                    slot.enabled = true;
                    slot.re_enable_at = None;
                }
            }
        }
    }

    /// Return the number of slots in the table (enabled or not).
    ///
    /// Primarily useful for testing assertions.
    pub fn slot_count(&self) -> usize {
        self.slots.len()
    }

    /// Return a mutable reference to all slots (for direct metric updates).
    pub fn slots_mut(&mut self) -> &mut [ProviderSlot] {
        &mut self.slots
    }

    /// Return a reference to all slots (for reading metrics).
    pub fn slots(&self) -> &[ProviderSlot] {
        &self.slots
    }
}

// ── Metric helpers ────────────────────────────────────────────────────────────

/// Record a request latency (in milliseconds) in the latency ring buffer.
///
/// Maintains a bounded ring buffer of the last 100 latencies.
/// If the buffer is full, pops the oldest entry before pushing the new one.
pub fn record_latency(slot: &mut ProviderSlot, ms: u64) {
    if slot.latency_ring.len() >= 100 {
        slot.latency_ring.pop_front();
    }
    slot.latency_ring.push_back(ms);
}

/// Calculate the percentile value from the latency ring.
///
/// Returns `None` if the ring is empty. Percentiles are computed by sorting
/// the values and selecting the element at the computed index (using floor division).
fn percentile_helper(latencies: &VecDeque<u64>, percentile: u32) -> Option<u64> {
    if latencies.is_empty() {
        return None;
    }

    let mut sorted: Vec<u64> = latencies.iter().copied().collect();
    sorted.sort_unstable();

    // Compute the index for the given percentile.
    // For p50: (99 * 50) / 100 = 49 (index into the 0-99 array)
    // For p95: (99 * 95) / 100 = 94
    let index = ((sorted.len() as u32 - 1) * percentile) / 100;
    Some(sorted[index as usize])
}

/// Get the P50 (50th percentile) latency from the ring.
///
/// Returns `None` if no latencies have been recorded.
pub fn p50(slot: &ProviderSlot) -> Option<u64> {
    percentile_helper(&slot.latency_ring, 50)
}

/// Get the P95 (95th percentile) latency from the ring.
///
/// Returns `None` if no latencies have been recorded.
pub fn p95(slot: &ProviderSlot) -> Option<u64> {
    percentile_helper(&slot.latency_ring, 95)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::providers_store::ProviderEntry;

    fn gemini_entry(id: &str, display_name: &str) -> ProviderEntry {
        ProviderEntry {
            id: id.to_string(),
            harness: "gemini".to_string(),
            account_id: format!("{}-account", id),
            display_name: display_name.to_string(),
            auth_kind: "ApiKey".to_string(),
            enabled: true,
            source: "auto-detected".to_string(),
        }
    }

    /// 3 slots: pick_next 6 times must cycle [0, 1, 2, 0, 1, 2].
    #[test]
    fn round_robin_order() {
        let providers = vec![
            gemini_entry("slot-0", "Slot Zero"),
            gemini_entry("slot-1", "Slot One"),
            gemini_entry("slot-2", "Slot Two"),
        ];
        let mut table = RoutingTable::new(&providers);

        let expected_ids = ["slot-0", "slot-1", "slot-2", "slot-0", "slot-1", "slot-2"];
        for expected in &expected_ids {
            let result = table.pick_next().expect("should return a slot");
            assert_eq!(result.slot_id, *expected);
        }
    }

    /// Mark slot-1 rate-limited; subsequent picks must skip it and return
    /// slot-0 and slot-2 in round-robin order.
    #[test]
    fn skip_rate_limited() {
        let providers = vec![
            gemini_entry("slot-0", "Slot Zero"),
            gemini_entry("slot-1", "Slot One"),
            gemini_entry("slot-2", "Slot Two"),
        ];
        let mut table = RoutingTable::new(&providers);

        // Mark slot-1 with a long cooldown so it stays disabled.
        table.mark_rate_limited("slot-1", 3600);

        // With slot-1 disabled, the order should be: slot-0, slot-2, slot-0.
        let r0 = table.pick_next().expect("should return slot");
        assert_eq!(r0.slot_id, "slot-0");

        let r1 = table.pick_next().expect("should return slot");
        assert_eq!(r1.slot_id, "slot-2");

        let r2 = table.pick_next().expect("should return slot");
        assert_eq!(r2.slot_id, "slot-0");
    }

    /// Mark slot-0 with a 0-second cooldown; tick_cooldowns must re-enable it
    /// immediately (re_enable_at is in the past or present the instant it is set).
    #[test]
    fn cooldown_reactivation() {
        let providers = vec![gemini_entry("slot-0", "Slot Zero")];
        let mut table = RoutingTable::new(&providers);

        // 0-second cooldown: re_enable_at = Instant::now() + 0s = now.
        // Because the Instant comparison uses <=, this must re-enable on tick.
        table.mark_rate_limited("slot-0", 0);

        // Slot should be disabled after mark_rate_limited.
        assert!(!table.slots[0].enabled);

        // tick_cooldowns must re-enable it (0s cooldown has already elapsed).
        table.tick_cooldowns();

        assert!(table.slots[0].enabled);
        assert!(table.slots[0].re_enable_at.is_none());
    }

    /// Mark all slots rate-limited; pick_next must return None.
    #[test]
    fn all_disabled() {
        let providers = vec![
            gemini_entry("slot-0", "Slot Zero"),
            gemini_entry("slot-1", "Slot One"),
        ];
        let mut table = RoutingTable::new(&providers);

        table.mark_rate_limited("slot-0", 3600);
        table.mark_rate_limited("slot-1", 3600);

        let result = table.pick_next();
        assert!(
            result.is_none(),
            "expected None when all slots are rate-limited"
        );
    }

    /// Non-gemini providers and disabled providers are excluded from the table.
    #[test]
    fn filters_non_gemini_and_disabled() {
        let providers = vec![
            gemini_entry("slot-0", "Gemini"),
            ProviderEntry {
                id: "cc-default".to_string(),
                harness: "cc".to_string(),
                account_id: "cc-default".to_string(),
                display_name: "Claude Code".to_string(),
                auth_kind: "OAuthToken".to_string(),
                enabled: true,
                source: "auto-detected".to_string(),
            },
            ProviderEntry {
                id: "gemini-disabled".to_string(),
                harness: "gemini".to_string(),
                account_id: "disabled".to_string(),
                display_name: "Disabled Gemini".to_string(),
                auth_kind: "ApiKey".to_string(),
                enabled: false, // should be excluded
                source: "manual".to_string(),
            },
        ];
        let table = RoutingTable::new(&providers);
        assert_eq!(table.slot_count(), 1);
        assert_eq!(table.slots[0].id, "slot-0");
    }

    /// request_count increments by 1 per successful pick_next for that slot.
    #[test]
    fn request_count_increments() {
        let providers = vec![gemini_entry("slot-0", "Slot Zero")];
        let mut table = RoutingTable::new(&providers);

        assert_eq!(table.slots[0].request_count, 0);
        table.pick_next();
        assert_eq!(table.slots[0].request_count, 1);
        table.pick_next();
        assert_eq!(table.slots[0].request_count, 2);
    }

    /// Empty provider list: pick_next returns None without panic.
    #[test]
    fn empty_table_returns_none() {
        let mut table = RoutingTable::new(&[]);
        assert!(table.pick_next().is_none());
    }

    /// Single slot: cursor wraps correctly back to index 0.
    #[test]
    fn single_slot_cursor_wraps() {
        let providers = vec![gemini_entry("slot-0", "Only Slot")];
        let mut table = RoutingTable::new(&providers);

        for _ in 0..5 {
            let result = table.pick_next().expect("should return slot");
            assert_eq!(result.slot_id, "slot-0");
        }
        // After 5 picks on a single-slot table, cursor should be back at 0.
        assert_eq!(table.cursor, 0);
    }

    /// p50 calculation: [10, 20, 30, 40, 50] → p50 = 30.
    #[test]
    fn p50_calculation() {
        let slot = ProviderSlot {
            id: "test".to_string(),
            display_name: "Test".to_string(),
            enabled: true,
            re_enable_at: None,
            request_count: 0,
            error_count: 0,
            rate_limit_count: 0,
            latency_ring: VecDeque::from(vec![10, 20, 30, 40, 50]),
        };

        // For 5 values (indices 0-4), p50 index = (4 * 50) / 100 = 2 → value 30
        let result = p50(&slot);
        assert_eq!(result, Some(30), "p50 of [10,20,30,40,50] should be 30");

        // Also test p95: (4 * 95) / 100 = 3 → value 40
        let result = p95(&slot);
        assert_eq!(result, Some(40), "p95 of [10,20,30,40,50] should be 40");
    }

    /// Ring buffer bounded: after pushing 101 latencies, buffer stays at 100.
    #[test]
    fn ring_bounded() {
        let mut slot = ProviderSlot {
            id: "test".to_string(),
            display_name: "Test".to_string(),
            enabled: true,
            re_enable_at: None,
            request_count: 0,
            error_count: 0,
            rate_limit_count: 0,
            latency_ring: VecDeque::new(),
        };

        for i in 0..101 {
            record_latency(&mut slot, i as u64);
        }

        // Buffer should be at exactly 100 entries (the oldest, 0, is dropped).
        assert_eq!(
            slot.latency_ring.len(),
            100,
            "ring buffer should be bounded to 100"
        );

        // The first value should be 1 (since 0 was popped off).
        assert_eq!(
            slot.latency_ring.front(),
            Some(&1),
            "oldest value should be 1"
        );
        // The last value should be 100.
        assert_eq!(
            slot.latency_ring.back(),
            Some(&100),
            "newest value should be 100"
        );
    }

    /// Empty latency ring returns None for p50 and p95.
    #[test]
    fn empty_ring_returns_none() {
        let slot = ProviderSlot {
            id: "test".to_string(),
            display_name: "Test".to_string(),
            enabled: true,
            re_enable_at: None,
            request_count: 0,
            error_count: 0,
            rate_limit_count: 0,
            latency_ring: VecDeque::new(),
        };

        assert_eq!(p50(&slot), None, "p50 on empty ring should be None");
        assert_eq!(p95(&slot), None, "p95 on empty ring should be None");
    }
}
