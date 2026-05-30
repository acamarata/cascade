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

pub mod capability;
pub mod discovery;
pub mod ipc;
pub mod lifecycle;
pub mod manifest;
pub mod sandbox;
pub mod types;

pub use capability::{CapabilityError, CapabilitySet, DeclaredCapabilities};
pub use discovery::{DiscoveredPlugin, DiscoveryError, PluginOrigin, discover_all};
pub use ipc::{InputValidator, NoopValidator, PluginError, PluginRequest, PluginResponse, dispatch};
pub use lifecycle::{LifecycleError, PluginRegistry, PluginState};
pub use manifest::{ManifestError, PluginManifest, PluginType};
pub use sandbox::{ResourceLimits, SandboxError, WasmPlugin};
pub use types::{
    WasmAgent, WasmChunker, WasmEmbeddingProvider, WasmParser, WasmQueryStrategy,
    WasmReranker, WasmRetriever,
};
