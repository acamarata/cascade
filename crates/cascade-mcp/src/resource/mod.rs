//! MCP Resources — `cascade://` URI scheme for tier files, memory, inbox, and master-lists.
//!
//! Implements the four MCP resource methods:
//! - `resources/list` — paginated catalog of all available `cascade://` resources.
//! - `resources/read` — fetch content for a specific URI as `TextResourceContents`.
//! - `resources/subscribe` — add a URI to the per-connection subscription set.
//! - `resources/unsubscribe` — remove a URI from the subscription set.
//!
//! ## SPORT
//! MASTER-MCP-PRIMITIVES.md: resources row → Done

pub mod backend;
pub mod registry;
pub mod subscription;
pub mod types;

#[cfg(test)]
mod tests;

// Re-export the full public API at the `resource` module level so all existing
// `use crate::resource::*` call-sites continue to compile without change.

pub use types::{ContentBackend, McpResource, TextResourceContents, PAGE_SIZE};
pub use backend::FsContentBackend;
pub use subscription::SubscriptionStore;
pub use registry::{
    register_resource_handlers, ResourceRegistry, ResourcesListHandler, ResourcesReadHandler,
    ResourcesSubscribeHandler, ResourcesUnsubscribeHandler,
};
