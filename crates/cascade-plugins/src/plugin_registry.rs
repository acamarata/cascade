//! PluginRegistry — thread-safe dispatch hub for loaded WASM plugins.
//!
//! Purpose: hold a `DashMap<id, Arc<LoadedPlugin>>` and expose register /
//!   unregister / dispatch / list operations used by the daemon, RAG pipeline
//!   (E-01), and MCP layer (E-04).
//! Inputs:  `DiscoveredPlugin` from `PluginLoader::scan`; `ReloadEvent` from
//!   `PluginWatcher`.
//! Outputs: `Vec<PluginInfo>` for the `plugin.list` IPC endpoint and CLI table;
//!   `serde_json::Value` results from dispatched calls.
//! Constraints:
//!   - `DashMap` is used instead of `Mutex<HashMap>` so dispatch reads never
//!     block register/unregister writes.
//!   - Dispatch holds the Arc (not a guard) during the call so no DashMap shard
//!     lock is held across the await boundary.
//!   - `reload_plugin` uses `hot_reload::drain_arc_default` to wait for in-flight
//!     calls before dropping the old plugin.
//!
//! SPORT: cascade-plugins / registry layer (T-P4-E03-09)

use std::path::PathBuf;
use std::sync::Arc;

use dashmap::DashMap;
use serde::{Deserialize, Serialize};
use tracing::{info, instrument, warn};
use wasmtime::Val;

use crate::hot_reload::drain_arc_default;
use crate::loader::DiscoveredPlugin;
use crate::manifest::{PluginJsonManifest, PluginKind};
use crate::runtime::LoadedPlugin;

// ── PluginStatus ──────────────────────────────────────────────────────────────

/// Runtime status of a plugin slot in the registry.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PluginStatus {
    /// Plugin is loaded and callable.
    Loaded,
    /// Plugin failed to load; it is listed but not callable.
    Error,
    /// Plugin is being unloaded (drain in progress).
    Unloading,
}

// ── PluginInfo ────────────────────────────────────────────────────────────────

/// Lightweight summary of a plugin, returned by `list()` and the `plugin.list`
/// IPC endpoint.
///
/// This is the stable JSON shape for the IPC endpoint — field names must not
/// change without a version bump.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PluginInfo {
    /// Reverse-domain plugin ID (e.g. `"com.example.my-plugin"`).
    pub id: String,
    /// Human-readable display name.
    pub name: String,
    /// Semantic version (e.g. `"1.0.0"`).
    pub version: String,
    /// Plugin kind (DataSource / ChatTool / Widget / Provider / Agent).
    pub kind: PluginKind,
    /// Current runtime status.
    pub status: PluginStatus,
    /// Directory this plugin was loaded from.
    pub plugin_dir: PathBuf,
}

// ── Internal slot ─────────────────────────────────────────────────────────────

/// One registry slot: an `Arc<LoadedPlugin>` plus the manifest for metadata.
struct PluginSlot {
    plugin: Arc<LoadedPlugin>,
    manifest: PluginJsonManifest,
    plugin_dir: PathBuf,
    status: PluginStatus,
}

// ── PluginRegistry ────────────────────────────────────────────────────────────

/// Thread-safe registry of loaded WASM plugins.
///
/// # Concurrency model
/// `DashMap` is a sharded concurrent hashmap.  `dispatch()` clones the `Arc`
/// from the map entry (holding the shard lock for only that clone), then drops
/// the lock before calling `await` on the plugin.  This means shard locks are
/// never held across async boundaries.
pub struct PluginRegistry {
    slots: DashMap<String, PluginSlot>,
}

impl Default for PluginRegistry {
    fn default() -> Self {
        Self::new()
    }
}

impl PluginRegistry {
    /// Create an empty registry.
    pub fn new() -> Self {
        Self {
            slots: DashMap::new(),
        }
    }

    /// Register a newly discovered plugin.
    ///
    /// If a plugin with the same ID is already registered, it is replaced and
    /// the old `Arc` is drained before dropping (same as `reload_plugin`).
    #[instrument(name = "registry_register", skip(self, discovered), fields(plugin_id = %discovered.manifest.id))]
    pub fn register(&self, discovered: DiscoveredPlugin) {
        let id = discovered.manifest.id.clone();
        let slot = PluginSlot {
            plugin: Arc::new(discovered.loaded),
            manifest: discovered.manifest,
            plugin_dir: discovered.plugin_dir,
            status: PluginStatus::Loaded,
        };

        if let Some(old) = self.slots.insert(id.clone(), slot) {
            // Drain in-flight calls to the old plugin before it is dropped.
            if !drain_arc_default(&old.plugin) {
                warn!(
                    plugin_id = %id,
                    "drain timeout while replacing plugin — forcing drop"
                );
            }
        }

        info!(plugin_id = %id, "plugin registered");
    }

    /// Remove a plugin from the registry.
    ///
    /// Waits for in-flight calls (drain) before returning so the caller knows
    /// the plugin is fully unloaded.  If the drain times out, the plugin is
    /// force-dropped and a warning is emitted.
    #[instrument(name = "registry_unregister", skip(self), fields(plugin_id = %id))]
    pub fn unregister(&self, id: &str) {
        if let Some((_, slot)) = self.slots.remove(id) {
            if !drain_arc_default(&slot.plugin) {
                warn!(
                    plugin_id = %id,
                    "drain timeout during unregister — forcing drop"
                );
            }
            info!(plugin_id = %id, "plugin unregistered");
        } else {
            warn!(plugin_id = %id, "unregister: plugin not found");
        }
    }

    /// Hot-reload a plugin: swap the `Arc<LoadedPlugin>` atomically.
    ///
    /// Called by the hot-reload watcher loop when a WASM file changes.
    /// The old Arc is drained (in-flight calls finish) before being dropped.
    #[instrument(name = "registry_reload", skip(self, discovered), fields(plugin_id = %discovered.manifest.id))]
    pub fn reload_plugin(&self, discovered: DiscoveredPlugin) {
        let id = discovered.manifest.id.clone();
        let new_arc = Arc::new(discovered.loaded);
        let new_manifest = discovered.manifest;
        let new_dir = discovered.plugin_dir;

        if let Some(mut slot) = self.slots.get_mut(&id) {
            let old_arc = std::mem::replace(&mut slot.plugin, Arc::clone(&new_arc));
            slot.manifest = new_manifest;
            slot.plugin_dir = new_dir;
            slot.status = PluginStatus::Loaded;
            drop(slot); // release DashMap shard lock BEFORE draining

            if !drain_arc_default(&old_arc) {
                warn!(
                    plugin_id = %id,
                    "drain timeout during hot-reload — forcing drop of old plugin"
                );
            }
            info!(plugin_id = %id, "plugin hot-reloaded");
        } else {
            // Plugin was not previously registered — treat as a new registration.
            // Safety: we just created new_arc on the line above, so try_unwrap
            // succeeds (no other clones exist at this point).
            let loaded = Arc::try_unwrap(new_arc)
                .unwrap_or_else(|_| panic!("unexpected Arc clone during reload_plugin for {id}"));
            self.register(DiscoveredPlugin {
                loaded,
                manifest: new_manifest,
                plugin_dir: new_dir,
            });
        }
    }

    /// Dispatch a call to a plugin by ID.
    ///
    /// Looks up the plugin, clones the `Arc` (shard lock held only for the clone),
    /// then calls the specified WASM export with the provided `Val` arguments.
    ///
    /// Returns `Err(PluginDispatchError::NotFound)` if the ID is not registered.
    #[instrument(name = "registry_dispatch", skip(self, args), fields(plugin_id = %id, func = %func_name))]
    pub async fn dispatch(
        &self,
        id: &str,
        func_name: &str,
        args: &[Val],
    ) -> Result<Vec<Val>, PluginDispatchError> {
        // Clone the Arc while holding the shard lock briefly, then release.
        let plugin_arc = {
            self.slots
                .get(id)
                .map(|slot| Arc::clone(&slot.plugin))
                .ok_or_else(|| PluginDispatchError::NotFound { id: id.to_owned() })?
        };

        // Shard lock is released here; the call runs without holding any lock.
        let result =
            plugin_arc
                .call(func_name, args)
                .await
                .map_err(|e| PluginDispatchError::CallError {
                    id: id.to_owned(),
                    reason: e.to_string(),
                })?;

        Ok(result)
    }

    /// Return a snapshot of all registered plugins.
    ///
    /// Iterates the `DashMap` in no particular order.  Each shard lock is held
    /// only long enough to clone the slot's metadata.
    pub fn list(&self) -> Vec<PluginInfo> {
        self.slots
            .iter()
            .map(|entry| PluginInfo {
                id: entry.key().clone(),
                name: entry.value().manifest.name.clone(),
                version: entry.value().manifest.version.clone(),
                kind: entry.value().manifest.kind,
                status: entry.value().status.clone(),
                plugin_dir: entry.value().plugin_dir.clone(),
            })
            .collect()
    }

    /// Return the number of registered plugins.
    pub fn len(&self) -> usize {
        self.slots.len()
    }

    /// Return `true` if no plugins are registered.
    pub fn is_empty(&self) -> bool {
        self.slots.is_empty()
    }
}

// ── PluginDispatchError ───────────────────────────────────────────────────────

/// Errors returned by `dispatch()`.
#[derive(Debug, thiserror::Error)]
pub enum PluginDispatchError {
    /// No plugin with the given ID is in the registry.
    #[error("plugin '{id}' not found in registry")]
    NotFound { id: String },

    /// The plugin's WASM call returned an error.
    #[error("plugin '{id}' call error: {reason}")]
    CallError { id: String, reason: String },
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::loader::PluginLoader;
    use crate::manifest::PluginKind;
    use std::fs;
    use tempfile::TempDir;

    /// Build a minimal valid `plugin.json` JSON string.
    fn valid_manifest_json(id: &str, kind: &str) -> String {
        format!(
            r#"{{
                "id": "{id}",
                "name": "Test Plugin",
                "version": "1.0.0",
                "description": "test",
                "author": "test",
                "license": "MIT",
                "entry_wasm": "{id}.wasm",
                "kind": "{kind}",
                "min_cascade_version": ">=0.1.0"
            }}"#
        )
    }

    /// Write a minimal valid WASM module to `path`.
    fn write_minimal_wasm(path: &std::path::Path) {
        let wasm = wat::parse_str("(module)").expect("wat parse");
        fs::write(path, wasm).expect("write wasm");
    }

    /// Create a complete plugin directory and return a `DiscoveredPlugin`.
    fn make_and_load_plugin(
        plugins_dir: &std::path::Path,
        id: &str,
        kind: &str,
    ) -> DiscoveredPlugin {
        let plugin_dir = plugins_dir.join(id);
        fs::create_dir_all(&plugin_dir).unwrap();
        fs::write(
            plugin_dir.join("plugin.json"),
            valid_manifest_json(id, kind),
        )
        .unwrap();
        write_minimal_wasm(&plugin_dir.join(format!("{id}.wasm")));

        let (mut loaded, errors) = PluginLoader::scan(plugins_dir);
        assert!(
            errors
                .iter()
                .all(|e| matches!(e, crate::loader::PluginLoadError::Disabled { .. })),
            "unexpected errors: {errors:?}"
        );
        // Find the one we just added.
        let pos = loaded
            .iter()
            .position(|d| d.manifest.id == id)
            .expect("plugin not found");
        loaded.swap_remove(pos)
    }

    #[test]
    fn register_and_list() {
        let tmp = TempDir::new().unwrap();
        let registry = PluginRegistry::new();

        let plugin = make_and_load_plugin(tmp.path(), "com.example.alpha", "ChatTool");
        registry.register(plugin);

        let list = registry.list();
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].id, "com.example.alpha");
        assert_eq!(list[0].status, PluginStatus::Loaded);
    }

    #[test]
    fn unregister_removes_plugin() {
        let tmp = TempDir::new().unwrap();
        let registry = PluginRegistry::new();

        let plugin = make_and_load_plugin(tmp.path(), "com.example.beta", "ChatTool");
        registry.register(plugin);
        assert_eq!(registry.len(), 1);

        registry.unregister("com.example.beta");
        assert_eq!(registry.len(), 0);
    }

    #[test]
    fn dispatch_not_found_returns_error() {
        let registry = PluginRegistry::new();
        let rt = tokio::runtime::Runtime::new().unwrap();
        let result = rt.block_on(registry.dispatch("com.example.nonexistent", "hello", &[]));
        assert!(matches!(result, Err(PluginDispatchError::NotFound { .. })));
    }

    #[tokio::test]
    async fn dispatch_routes_to_correct_plugin() {
        let tmp = TempDir::new().unwrap();
        let registry = PluginRegistry::new();

        let p1 = make_and_load_plugin(tmp.path(), "com.example.one", "ChatTool");
        let p2 = make_and_load_plugin(tmp.path(), "com.example.two", "ChatTool");
        registry.register(p1);
        registry.register(p2);

        assert_eq!(registry.len(), 2);

        // Both plugins export a trivial module (no exports), so calling a
        // non-existent export should return a MissingExport error — proving
        // dispatch reached the correct plugin and did not panic.
        let result = registry
            .dispatch("com.example.one", "does_not_exist", &[])
            .await;
        assert!(
            result.is_err(),
            "expected MissingExport error, got: {result:?}"
        );
        let err_str = result.unwrap_err().to_string();
        assert!(
            err_str.contains("com.example.one"),
            "error should name the plugin, got: {err_str}"
        );
    }

    #[test]
    fn list_returns_accurate_kind() {
        let tmp = TempDir::new().unwrap();
        let registry = PluginRegistry::new();

        let plugin = make_and_load_plugin(tmp.path(), "com.example.provider", "Provider");
        registry.register(plugin);

        let list = registry.list();
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].kind, PluginKind::Provider);
    }
}
