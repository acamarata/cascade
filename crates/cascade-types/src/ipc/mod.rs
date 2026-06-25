// FROZEN — schema version 1. Add new methods by appending only; never remove or rename.
// This file is the canonical JSON-RPC 2.0 protocol contract for all Cascade IPC clients
// (cascade-cli, OS widgets, MCP server in E7, and future external integrations).
// Any schema change requires a versioning ticket before any dependent crate is updated.

//! # cascade IPC protocol — JSON-RPC 2.0 types
//!
//! Defines the frozen wire-format types used by every Cascade IPC client.
//!
//! ## Architecture
//!
//! ```text
//! cascade-cli / widgets / MCP  ──[JSON-RPC 2.0]──► cascaded daemon
//!                                                        │
//!                                               dispatch to handlers
//! ```
//!
//! ## Protocol summary
//!
//! | Field | Type | Notes |
//! |-------|------|-------|
//! | `jsonrpc` | `"2.0"` | Always `"2.0"` per spec |
//! | `id` | `RequestId` | Number, string, or null per JSON-RPC 2.0 |
//! | `method` | `String` | e.g. `"ping"`, `"cascade.status"` |
//! | `params` | `Option<P>` | Method-specific params struct |
//! | `result` | `Option<R>` | Present on success |
//! | `error` | `Option<RpcError>` | Present on failure; mutually exclusive with `result` |
//!
//! ## Error codes
//!
//! | Constant | Value | Meaning |
//! |----------|-------|---------|
//! | `METHOD_NOT_FOUND` | -32601 | No handler for the requested method |
//! | `INVALID_PARAMS` | -32602 | Params failed validation |
//! | `INTERNAL_ERROR` | -32603 | Unhandled daemon-side error |
//! | `DAEMON_NOT_RUNNING` | -32001 | Client tried to connect but daemon is not up |
//! | `AUTH_FAILED` | -32002 | Auth token missing or invalid |
//! | `RESOURCE_NOT_FOUND` | -32003 | Requested resource does not exist |

mod envelope;
mod methods;
mod validation;

#[cfg(test)]
mod tests;

// Re-export everything at the `ipc` module level to preserve all existing public paths.

pub use envelope::{
    JsonRpcVersion, Request, RequestId, Response, RpcError, AUTH_FAILED, DAEMON_NOT_RUNNING,
    INTERNAL_ERROR, INVALID_PARAMS, METHOD_NOT_FOUND, PROTOCOL_VERSION, RESOURCE_NOT_FOUND,
};

pub use methods::{
    ConfigGetParams, ConfigGetResult, ConfigSetParams, ConfigSetResult, DaemonStopParams,
    DaemonStopResult, HealthCheck, HealthParams, HealthResult, HotwordLookupParams,
    HotwordLookupResult, InboxItem, InboxSummaryParams, InboxSummaryResult, MemoryReadParams,
    MemoryReadResult, MemoryWriteParams, MemoryWriteResult, PingParams, PingResult, ProviderEntry,
    ProviderQuotaParams, ProviderQuotaResult, ResolveParams, ResolveResult, RollbackApplyParams,
    RollbackApplyResult, RollbackListParams, RollbackListResult, SearchHit, SearchParams,
    SearchResult, SnapshotEntry, StatusParams, StatusResult, UpdateApplyParams, UpdateApplyResult,
    UpdateAutoParams, UpdateAutoResult, UpdateCheckParams, UpdateCheckResult,
};

pub use validation::{
    deserialize_request, validate_memory_write_params, validate_resolve_params,
    validate_search_params, MAX_CONTENT_LEN, MAX_QUERY_LEN, VALID_TIERS,
};
