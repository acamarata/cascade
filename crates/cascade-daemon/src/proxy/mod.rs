//! Gemini proxy subsystem — RoutingTable-backed round-robin HTTP proxy.
//!
//! Purpose: provides the [`GeminiProxy`] HTTP server that listens on
//! `localhost:3761`, routes each incoming request through the
//! [`cascade_core::routing_table::RoutingTable`], and handles HTTP 429
//! responses from upstream Gemini by marking the provider rate-limited and
//! retrying with the next available slot.
//!
//! Inputs:  configured `ProxyConfig` (cooldown_secs, max_retries), providers
//!          path (`~/.cascade/providers.json`), `SharedBus` (for
//!          `providers.updated` event subscription).
//! Outputs: HTTP proxy server on `localhost:3761`; rebuilt routing table on
//!          each `providers.updated` event.
//! Constraints: RoutingTable is wrapped in `Arc<Mutex<RoutingTable>>`; the
//!              Mutex is NEVER held across an `.await` point.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy module (T-P2-E02-36)

pub mod gemini_proxy;

pub use gemini_proxy::{GeminiProxy, ProxyError};
