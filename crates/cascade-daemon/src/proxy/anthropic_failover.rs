//! Request-level Anthropic failover proxy — Phase C ticket C4.
//!
//! **STATUS: DEFERRED SKELETON — NOT WIRED, NOT IMPLEMENTED.** This module
//! defines the shape (struct + dispatch trait) that
//! `.claude/planning/10-session-failover-spec.md` §1.2 ("what IS proxyable")
//! calls for, but the actual dispatch body is intentionally left
//! unimplemented. It is declared in `proxy/mod.rs` (so the shape is visible
//! and reviewable) but is deliberately NEVER started from `supervisor.rs` —
//! that omission is the fail-closed default: `:3763` binds to nothing and
//! accepts no connections unless and until this ticket is finished.
//!
//! ## Why this is a skeleton, not a full implementation
//!
//! Per the design spec §0/§1.3: this is NOT a raw HTTP passthrough to
//! `api.anthropic.com` — Anthropic OAuth access tokens are short-lived and
//! silently rotated by the `claude` CLI's own (undocumented) refresh flow,
//! so the only safe way to mint a request "as" an account is to shell out to
//! the real `claude` binary under that account's `CLAUDE_CONFIG_DIR`
//! (mirroring `cascade-cli/src/cmd/conductor.rs`'s `execute_claude`), not to
//! extract-and-replay its bearer token. A correct implementation needs, in
//! order (spec §1.2 steps 1-5):
//!   1. Parse the incoming Anthropic `/v1/messages` request (reuse
//!      `anthropic_compat.rs`'s request-shape parsing, skip its Gemini
//!      translation step).
//!   2. Call `cascade_core::selection::select_account` to pick a target
//!      account + config dir.
//!   3. Shell `CLAUDE_CONFIG_DIR=<dir> claude -p "<prompt>" --output-format
//!      stream-json --include-partial-messages ...` (mirrors
//!      `conductor.rs::execute_claude`, plus streaming flags).
//!   4. Re-frame Claude Code's own `stream-json` stdout as real Anthropic SSE
//!      (analogous to `anthropic_compat/sse.rs::StreamTranslator`, but a new
//!      parser — the source shape is different from Gemini's SSE). This step
//!      depends on empirically confirming Claude Code's exact 429/rate-limit
//!      signal shape (spec §5 Q1, ticket F1) so a failed attempt can be
//!      distinguished from real content and trigger the retry-with-next-
//!      account loop in step 5 — that signature has not been captured in
//!      this pass and is the main reason this ticket stays a skeleton.
//!   5. On a detected failed attempt, mark that account unavailable in a
//!      local snapshot copy and retry via `select_account` again, bounded to
//!      the spill-list length; on exhaustion return an Anthropic-shaped
//!      `overloaded_error` (spec §4).
//!
//! Building steps 1-5 correctly (streaming re-framing + a real retry loop
//! against live subprocess output) is Phase B of the design spec (tickets
//! B1-B5, sized S/M each, "3-7 days" total) — materially larger than the
//! C2/C3/C5 mechanism work done alongside this skeleton in the same pass.
//! Rather than ship a half-working HTTP proxy that silently mishandles
//! streaming or misses the 429 signature, this ticket ships the reviewable
//! shape only. Follow-up: capture the F1 signature, then implement
//! [`AnthropicFailoverDispatch`] for a real dispatcher and wire
//! `AnthropicFailoverServer::run` + a `supervisor.rs` startup block
//! (mirroring the existing `:3761`/`:3762` wiring).
//!
//! Design spec: `.claude/planning/10-session-failover-spec.md` §1.2-§1.3.
//! Master plan: `.claude/planning/11-vNEXT-master-plan.md` Phase C / C4.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy::anthropic_failover (Phase C / C4, deferred)

use std::net::SocketAddr;

use tokio_util::sync::CancellationToken;

/// Default bind address for the (not-yet-running) failover proxy, per spec §1.2.
pub const DEFAULT_BIND: &str = "127.0.0.1:3763";

/// Contract a real dispatcher must satisfy once this ticket is implemented:
/// given a parsed Anthropic-format request body, pick an account and return
/// its re-framed Anthropic-shaped response (or an `overloaded_error` body
/// once every spill candidate has been tried). Intentionally has no
/// implementors yet — this is the shape reviewers can comment on before any
/// code depends on it.
pub trait AnthropicFailoverDispatch: Send + Sync {
    /// Dispatch one already-parsed Anthropic Messages API request body and
    /// return the (eventually re-framed) response body, or an error string
    /// describing why every spill candidate failed.
    fn dispatch(&self, request_body: &serde_json::Value) -> Result<serde_json::Value, String>;
}

/// Request-level Anthropic failover proxy server shape (spec §1.2).
///
/// `run` deliberately returns `Err` immediately — see module docs. This
/// keeps the type constructible and reviewable (e.g. from tests asserting
/// the fail-closed default) without ever opening a socket.
pub struct AnthropicFailoverServer {
    bind_addr: SocketAddr,
    shutdown: CancellationToken,
}

impl AnthropicFailoverServer {
    /// Construct a new (non-running) `AnthropicFailoverServer`.
    pub fn new(bind_addr: SocketAddr, shutdown: CancellationToken) -> Self {
        Self {
            bind_addr,
            shutdown,
        }
    }

    /// Bind address this instance was constructed with (exposed for tests /
    /// future `supervisor.rs` wiring — currently always `DEFAULT_BIND`).
    pub fn bind_addr(&self) -> SocketAddr {
        self.bind_addr
    }

    /// Fail-closed default: always returns `Err` without binding a socket or
    /// spawning anything. Never call this expecting a running server — see
    /// module docs for what remains before it can run.
    pub async fn run(self) -> Result<(), std::io::Error> {
        // Consume `shutdown` to keep the field "used" without giving the
        // skeleton any real cancellation behavior to get subtly wrong.
        let _ = &self.shutdown;
        Err(std::io::Error::other(
            "AnthropicFailoverServer (proxy/anthropic_failover.rs, Phase C ticket C4) is a \
             documented skeleton, not an implementation — see its module doc comment and \
             .claude/planning/10-session-failover-spec.md §1.2 for what remains (F1 429-signature \
             capture, stream-json→SSE re-framing, retry-with-next-account loop). It is not \
             started from supervisor.rs and must not be until that work lands.",
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn run_fails_closed_rather_than_binding_a_socket() {
        let addr: SocketAddr = DEFAULT_BIND.parse().unwrap();
        let shutdown = CancellationToken::new();
        let server = AnthropicFailoverServer::new(addr, shutdown);

        let err = server.run().await.expect_err("skeleton must fail closed");
        let msg = err.to_string();
        assert!(msg.contains("documented skeleton"), "msg: {msg}");

        // No listener should have been left bound on the default port.
        assert!(
            std::net::TcpListener::bind(DEFAULT_BIND).is_ok(),
            "port {DEFAULT_BIND} must remain free — the skeleton must never bind it"
        );
    }

    #[test]
    fn bind_addr_reports_what_it_was_constructed_with() {
        let addr: SocketAddr = "127.0.0.1:3763".parse().unwrap();
        let server = AnthropicFailoverServer::new(addr, CancellationToken::new());
        assert_eq!(server.bind_addr(), addr);
    }
}
