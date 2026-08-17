//! Gemini proxy subsystem — RoutingTable-backed round-robin HTTP proxy.
//!
//! Purpose: provides the [`GeminiProxy`] HTTP server that listens on
//! `localhost:3761`, routes each incoming request through the
//! [`cascade_core::routing_table::RoutingTable`], and handles HTTP 429
//! responses from upstream Gemini by marking the provider rate-limited and
//! retrying with the next available slot.
//!
//! Also exposes [`AnthropicCompatServer`] on `localhost:3762` — an Anthropic
//! Messages API adapter that translates requests to Gemini format and forwards
//! them to the GP proxy at port 3761.
//!
//! Inputs:  configured `ProxyConfig` (cooldown_secs, max_retries), providers
//!          path (`~/.cascade/providers.json`), `SharedBus` (for
//!          `providers.updated` event subscription).
//! Outputs: HTTP proxy server on `localhost:3761`; rebuilt routing table on
//!          each `providers.updated` event.
//!          Anthropic-compat adapter on `localhost:3762`.
//! Constraints: RoutingTable is wrapped in `Arc<Mutex<RoutingTable>>`; the
//!              Mutex is NEVER held across an `.await` point.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy module (T-P2-E02-36)

pub mod anthropic_compat;
pub mod gemini_proxy;

// anthropic_failover (T-P7-E13-09) now lives at `crate::anthropic_failover`,
// declared unconditionally from lib.rs — it is loopback-only (no
// external-network surface) and must be compiled + started even when the
// `gemini-proxy` feature is off.

pub use anthropic_compat::AnthropicCompatServer;
pub use gemini_proxy::GeminiProxy;
