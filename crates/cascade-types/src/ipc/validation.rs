// FROZEN — schema version 1. See parent module for protocol contract notes.

//! Schema-validating deserialization and field-value bounds validation.
//! T-P2-E07-11 (deserialize_request) and T-P2-E07-12 (field bounds).

use super::envelope::Request;
use super::methods::{MemoryWriteParams, ResolveParams, SearchParams};

// ── Schema-validating deserialization (T-P2-E07-11) ────────────────────────────────

/// Deserialize a length-prefixed JSON IPC `Request<P>` body, mapping serde
/// schema violations onto structured [`crate::error::IpcError`] variants.
///
/// Purpose: every IPC message type carries `#[serde(deny_unknown_fields)]`, so an
///   unexpected key or a missing required field fails deserialization. This wrapper
///   translates the opaque serde error text into an actionable [`crate::error::IpcError`]
///   the daemon can return to the client as a structured JSON-RPC error.
/// Inputs: raw JSON bytes of a single request frame.
/// Outputs: `Ok(Request<P>)`, or `IpcError::UnknownField` / `IpcError::MissingField`
///   on schema violation, or a generic `IpcError::MalformedFrame` otherwise.
/// Constraints: pure; does no I/O. The `unknown field` / `missing field` substrings
///   are part of serde's stable human-readable error format.
pub fn deserialize_request<P>(bytes: &[u8]) -> Result<Request<P>, crate::error::IpcError>
where
    P: serde::de::DeserializeOwned,
{
    serde_json::from_slice::<Request<P>>(bytes).map_err(|e| {
        let msg = e.to_string();
        if msg.contains("unknown field") {
            crate::error::IpcError::UnknownField(msg)
        } else if msg.contains("missing field") {
            crate::error::IpcError::MissingField(msg)
        } else {
            crate::error::IpcError::MalformedFrame(msg)
        }
    })
}

// ── Field-value bounds validation (T-P2-E07-12) ─────────────────────────────────

/// Maximum accepted length of a search query string, in characters.
pub const MAX_QUERY_LEN: usize = 2048;

/// Maximum accepted length of a memory-write content payload, in bytes.
pub const MAX_CONTENT_LEN: usize = 524_288; // 512 KiB

/// The canonical cascade tier names, mirroring [`crate::cascade_tier::CascadeTier`]'s
/// `#[serde(rename_all = "lowercase")]` serialization. A `ResolveParams.tier` value
/// outside this set is rejected.
pub const VALID_TIERS: &[&str] = &["gci", "pci", "apc", "ppc", "prc", "pac"];

/// Reject a relative path that contains a `..` traversal component or a null byte.
///
/// cascade-types cannot depend on cascade-core (that would form a dependency cycle:
/// cascade-core already depends on cascade-types), so the traversal guard is
/// inlined here rather than calling `cascade_core::security::validate_cascade_path`.
/// The check is intentionally identical in spirit: reject `..` and `\0`.
fn path_field_is_safe(value: &str) -> bool {
    if value.contains('\0') {
        return false;
    }
    !std::path::Path::new(value)
        .components()
        .any(|c| c == std::path::Component::ParentDir)
}

/// Validate a [`SearchParams`]: the query must not exceed [`MAX_QUERY_LEN`].
pub fn validate_search_params(p: &SearchParams) -> Result<(), crate::error::IpcError> {
    if p.query.chars().count() > MAX_QUERY_LEN {
        return Err(crate::error::IpcError::InvalidFieldValue {
            field: "query".to_string(),
            reason: format!("exceeds {MAX_QUERY_LEN} character cap"),
        });
    }
    Ok(())
}

/// Validate a [`MemoryWriteParams`]: `content` must not exceed [`MAX_CONTENT_LEN`]
/// bytes, and `project` must not contain a path traversal.
pub fn validate_memory_write_params(p: &MemoryWriteParams) -> Result<(), crate::error::IpcError> {
    if p.content.len() > MAX_CONTENT_LEN {
        return Err(crate::error::IpcError::InvalidFieldValue {
            field: "content".to_string(),
            reason: format!("exceeds {MAX_CONTENT_LEN} byte cap"),
        });
    }
    if !path_field_is_safe(&p.project) {
        return Err(crate::error::IpcError::InvalidFieldValue {
            field: "project_path".to_string(),
            reason: "path traversal".to_string(),
        });
    }
    Ok(())
}

/// Validate a [`ResolveParams`]: when `tier` is present it must be one of
/// [`VALID_TIERS`].
pub fn validate_resolve_params(p: &ResolveParams) -> Result<(), crate::error::IpcError> {
    if let Some(tier) = &p.tier {
        if !VALID_TIERS.contains(&tier.as_str()) {
            return Err(crate::error::IpcError::InvalidFieldValue {
                field: "tier".to_string(),
                reason: format!("`{tier}` is not one of {VALID_TIERS:?}"),
            });
        }
    }
    Ok(())
}
