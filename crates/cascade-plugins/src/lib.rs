//! # cascade-plugins
//!
//! WASM plugin host for Cascade. Provides discovery, sandboxing, lifecycle
//! management, capability enforcement, and cascade-types trait dispatchers for
//! community-authored plugins compiled to WASM.
//!
//! ## Architecture
//!
//! ```text
//! Discovery (discovery.rs)
//!   -> Manifest parsing + schema version check (manifest.rs)
//!   -> Lifecycle state machine (lifecycle.rs)
//!   -> Capability resolution (capability.rs)
//!   -> Sandbox load in wasmtime (sandbox.rs)
//!   -> IPC gate + dispatch (ipc.rs)
//!   -> cascade-types trait dispatchers (types.rs)
//! ```
//!
//! ## Vendor-neutral guarantee
//!
//! No LLM provider names, no coding-agent names, and no tool-brand names appear
//! in this crate's public API. Plugins operate on Cascade abstractions only.
//!
//! ## Security boundary
//!
//! Every plugin runs inside a wasmtime `Store` with:
//! - Fuel limit (CPU, default 1e9 instructions per call)
//! - Memory cap (64 MB for lightweight types; 256 MB for Retriever/Provider/Agent)
//! - FD cap (default 32)
//! - Wall-clock timeout (default 30 s)
//! - Capability-based permission set (declared in `cascade-plugin.toml`)
//! - Input validation gate wired at the IPC entry point

pub mod audit;
pub mod capability;
pub mod consent;
pub mod discovery;
pub mod grants;
pub mod signing;
pub mod hot_reload;
pub mod ipc;
pub mod lifecycle;
pub mod loader;
pub mod manifest;
pub mod plugin_registry;
pub mod runtime;
pub mod sandbox;
pub mod traits;
pub mod types;
pub mod wit_bindings;

pub use audit::{log_capability_event, AuditEntry, AuditOutcome};
pub use capability::{CapabilityError, CapabilitySet, DeclaredCapabilities};
pub use consent::{check_capability_granted, check_fs_read_allowed, is_personal_path, ConsentError};
pub use grants::{GrantError, GrantStore};
pub use signing::{verify_plugin, SigningError, TrustedPublisher, TrustedPublishers, VerifyResult};
pub use discovery::{discover_all, DiscoveredPlugin, DiscoveryError, PluginOrigin};
pub use hot_reload::{drain_arc, drain_arc_default, PluginWatcher, ReloadEvent};
pub use ipc::{
    dispatch, InputValidator, NoopValidator, PluginError, PluginRequest, PluginResponse,
};
pub use lifecycle::{LifecycleError, PluginRegistry, PluginState};
pub use loader::{DiscoveredPlugin as LoaderDiscoveredPlugin, PluginLoadError, PluginLoader};
pub use manifest::{ManifestError, PluginManifest, PluginType};
pub use plugin_registry::PluginRegistry as PluginDispatchRegistry;
pub use plugin_registry::{PluginDispatchError, PluginInfo, PluginStatus};
pub use sandbox::{ResourceLimits, SandboxError, WasmPlugin};
pub use types::{
    WasmAgent, WasmChunker, WasmEmbeddingProvider, WasmParser, WasmQueryStrategy, WasmReranker,
    WasmRetriever,
};
