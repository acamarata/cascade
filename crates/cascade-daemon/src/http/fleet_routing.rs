//! Purpose: HTTP handler for GET /api/fleet/routing — returns the last N routing
//!   decisions from the in-memory ring buffer populated by the cascade-core Router
//!   observer.
//!
//! ## Route (nested at /api/fleet by the parent router)
//!
//!   GET /routing  →  200 JSON array of RoutingEvent (most-recent first)
//!
//! ## Inputs
//!   No request body or query parameters.
//!
//! ## Outputs
//!   200: JSON array of RoutingEvent:
//!     `[{ "task_class", "account_id", "model", "reason" }, ...]`
//!   Empty array when the ring is empty (daemon just started, no routing calls yet).
//!
//! ## Constraints
//!   - Always returns 200; empty array on error (graceful degradation).
//!   - Ring is capped at ROUTING_RING_CAP (64) entries — oldest is evicted.
//!   - Read-only; no auth required (quota/routing data is not sensitive).
//!   - Zero SQLite I/O — ring lives entirely in RAM.
//!
//! SPORT: MASTER-ENDPOINTS.md — GET /api/fleet/routing (fleet-01)

use std::collections::VecDeque;
use std::sync::{Arc, RwLock};

use axum::{extract::State, routing::get, Json, Router};
use cascade_core::routing::RoutingEvent;

use crate::dashboard::DashboardState;

/// Maximum number of routing events retained in the ring buffer.
///
/// WHY 64: enough to show the last few minutes of routing activity in the Fleet
/// UI without unbounded memory growth. Evicts oldest on overflow.
pub const ROUTING_RING_CAP: usize = 64;

/// Shared handle to the routing-event ring buffer.
///
/// `Arc<RwLock<VecDeque<RoutingEvent>>>` so it can be:
/// - Cloned cheaply into `DashboardState` (shared with the HTTP handler).
/// - Wrapped in a `RoutingObserver` closure installed on `cascade_core::Router`.
///   The closure captures a clone of the Arc and pushes events on each `select()`.
pub type RoutingRing = Arc<RwLock<VecDeque<RoutingEvent>>>;

/// Construct an empty routing ring.
pub fn new_routing_ring() -> RoutingRing {
    Arc::new(RwLock::new(VecDeque::with_capacity(ROUTING_RING_CAP)))
}

/// Build a `RoutingObserver` closure that pushes events into `ring`.
///
/// Install this on a `cascade_core::Router` via `router.with_observer(make_observer(ring))`.
/// The observer is called synchronously by `Router::select()` after each decision.
pub fn make_observer(ring: RoutingRing) -> cascade_core::routing::RoutingObserver {
    Arc::new(move |event: RoutingEvent| {
        if let Ok(mut guard) = ring.write() {
            if guard.len() >= ROUTING_RING_CAP {
                guard.pop_front(); // evict oldest
            }
            guard.push_back(event);
        }
    })
}

// ── Axum router ───────────────────────────────────────────────────────────────

/// Builds the fleet-routing sub-router, nested at `/api/fleet` by the parent.
pub fn router() -> Router<DashboardState> {
    Router::new().route("/routing", get(fleet_routing_handler))
}

// ── Handler ───────────────────────────────────────────────────────────────────

/// GET /api/fleet/routing
///
/// Returns the last N routing events from the in-memory ring, most-recent first.
/// Returns an empty array when the ring is empty (no routing decisions yet) or
/// when the daemon's routing ring is unavailable. Always 200.
async fn fleet_routing_handler(State(state): State<DashboardState>) -> Json<Vec<RoutingEvent>> {
    let Some(ring) = &state.routing_ring else {
        return Json(vec![]);
    };
    match ring.read() {
        Ok(guard) => {
            // Return most-recent first.
            let events: Vec<RoutingEvent> = guard.iter().rev().cloned().collect();
            Json(events)
        }
        Err(_) => Json(vec![]),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::routing::RoutingEvent;

    fn make_event(task_class: &str, account_id: &str) -> RoutingEvent {
        RoutingEvent {
            task_class: task_class.to_string(),
            account_id: account_id.to_string(),
            model: String::new(),
            reason: format!("{task_class} → {account_id}"),
        }
    }

    #[test]
    fn ring_caps_at_n_events() {
        let ring = new_routing_ring();
        let observer = make_observer(Arc::clone(&ring));

        // Push CAP + 10 events.
        for i in 0..(ROUTING_RING_CAP + 10) {
            observer(make_event("BulkExec", &format!("acct-{i}")));
        }

        let guard = ring.read().unwrap();
        assert_eq!(guard.len(), ROUTING_RING_CAP, "ring must cap at {ROUTING_RING_CAP}");

        // Oldest should be evicted; first entry should be acct-10.
        assert_eq!(guard.front().unwrap().account_id, "acct-10");
    }

    #[test]
    fn ring_preserves_insertion_order() {
        let ring = new_routing_ring();
        let observer = make_observer(Arc::clone(&ring));

        observer(make_event("Cheap", "gfp-pool"));
        observer(make_event("BulkExec", "claude-pooled-1"));

        let guard = ring.read().unwrap();
        assert_eq!(guard[0].task_class, "Cheap");
        assert_eq!(guard[1].task_class, "BulkExec");
    }

    #[test]
    fn observer_receives_event_fields() {
        let ring = new_routing_ring();
        let observer = make_observer(Arc::clone(&ring));

        observer(make_event("FinalGate", "claude-primary"));

        let guard = ring.read().unwrap();
        let ev = guard.front().unwrap();
        assert_eq!(ev.task_class, "FinalGate");
        assert_eq!(ev.account_id, "claude-primary");
        assert!(ev.reason.contains("FinalGate"));
    }

    #[tokio::test]
    async fn endpoint_returns_most_recent_first() {
        use axum::body::to_bytes;
        use axum::http::Request;
        use tower::ServiceExt;

        let ring = new_routing_ring();
        let observer = make_observer(Arc::clone(&ring));
        observer(make_event("Cheap", "gfp-pool"));
        observer(make_event("BulkExec", "claude-pooled-1"));

        let state = DashboardState {
            token: std::sync::Arc::new("test-token".to_string()),
            provider_registry: None,
            routing_ring: Some(Arc::clone(&ring)),
            middleware: Default::default(),
        };

        let app = router().with_state(state);
        let req = Request::builder()
            .uri("/routing")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), axum::http::StatusCode::OK);

        let body = to_bytes(resp.into_body(), usize::MAX).await.unwrap();
        let events: Vec<RoutingEvent> = serde_json::from_slice(&body).unwrap();
        assert_eq!(events.len(), 2);
        // Most-recent first: BulkExec was pushed last.
        assert_eq!(events[0].task_class, "BulkExec");
        assert_eq!(events[1].task_class, "Cheap");
    }

    #[tokio::test]
    async fn endpoint_returns_empty_when_ring_is_none() {
        use axum::body::to_bytes;
        use axum::http::Request;
        use tower::ServiceExt;

        let state = DashboardState {
            token: std::sync::Arc::new("test-token".to_string()),
            provider_registry: None,
            routing_ring: None,
            middleware: Default::default(),
        };

        let app = router().with_state(state);
        let req = Request::builder()
            .uri("/routing")
            .body(axum::body::Body::empty())
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        let body = to_bytes(resp.into_body(), usize::MAX).await.unwrap();
        let events: Vec<RoutingEvent> = serde_json::from_slice(&body).unwrap();
        assert!(events.is_empty());
    }
}
