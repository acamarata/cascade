// model_ids.rs — Re-export of the canonical fleet model-ID constants.
//
// The single source of truth for these constants now lives in
// `cascade_types::model_ids` (cascade-types is the workspace's DAG root, so
// leaf crates that do not depend on cascade-core — e.g. cascade-providers —
// can still import the doctrine constants). This module re-exports them so
// every existing `cascade_core::model_ids::X` call site keeps working
// unchanged.
//
// See `cascade_types::model_ids` for full documentation of each constant.

pub use cascade_types::model_ids::*;
