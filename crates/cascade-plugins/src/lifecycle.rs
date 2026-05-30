//! Plugin loading lifecycle — state machine with crash and quarantine handling.
//!
//! Purpose: track each plugin through its load/active/crash/quarantine states.
//!   Crashed plugins require manual reload. Quarantined plugins (crashed >=3 times
//!   in any 5-minute window) are refused on startup until the user re-enables them.
//! Inputs: `PluginRegistry` (mutation-safe) + lifecycle events (load, crash, unload).
//! Outputs: updated `PluginState` per plugin; circuit-breaker enforcement.
//! Constraints: state transitions are mutex-guarded per plugin slot; crash counter
//!   persists to `~/.cascade/plugins/state.json`; counter resets after 24 hours.
//! SPORT: cascade-plugins / lifecycle layer

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use thiserror::Error;

/// All valid states in the plugin lifecycle.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PluginState {
    /// Plugin directory found; manifest not yet parsed.
    Discovered,
    /// Manifest is being parsed and WASM imports validated.
    Validating,
    /// Module is being compiled and instantiated in the sandbox.
    Loading,
    /// Plugin is loaded and callable.
    Active,
    /// Plugin is being unloaded (daemon shutdown or hot reload).
    Unloading,
    /// Plugin has been cleanly unloaded.
    Unloaded,
    /// Manifest parse failed or WASM import validation failed.
    ValidationFailed,
    /// WASM module failed to compile or instantiate.
    LoadFailed,
    /// Plugin WASM trapped during an invocation; requires `cascade plugin reload`.
    Crashed,
    /// Crashed >=3 times in 5 minutes; refused on startup until `cascade plugin enable`.
    Quarantined,
}

/// Per-plugin crash tracking (persisted between daemon restarts).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct CrashRecord {
    /// Recent crash timestamps (kept only for the rolling 5-minute window).
    pub recent_crashes: Vec<std::time::SystemTime>,
    /// Total crash count since first installation.
    pub total_crashes: u64,
    /// When the last crash occurred, if any.
    pub last_crash_at: Option<std::time::SystemTime>,
}

impl CrashRecord {
    const WINDOW: Duration = Duration::from_secs(5 * 60);
    const QUARANTINE_THRESHOLD: usize = 3;
    const RESET_AFTER: Duration = Duration::from_secs(24 * 60 * 60);

    /// Record a crash and return whether the plugin should be quarantined.
    pub fn record_crash(&mut self) -> bool {
        let now = std::time::SystemTime::now();
        self.recent_crashes.push(now);
        self.total_crashes += 1;
        self.last_crash_at = Some(now);
        self.prune_old_crashes(now);
        self.recent_crashes.len() >= Self::QUARANTINE_THRESHOLD
    }

    fn prune_old_crashes(&mut self, now: std::time::SystemTime) {
        self.recent_crashes.retain(|t| {
            now.duration_since(*t).unwrap_or(Duration::MAX) < Self::WINDOW
        });
    }

    /// Returns true if enough time has passed to auto-clear the crash counter.
    pub fn should_reset(&self) -> bool {
        if let Some(last) = self.last_crash_at {
            std::time::SystemTime::now()
                .duration_since(last)
                .unwrap_or_default()
                >= Self::RESET_AFTER
        } else {
            true
        }
    }
}

/// Per-plugin slot in the registry (mutex-guarded).
#[derive(Debug)]
pub struct PluginSlot {
    pub name: String,
    pub state: PluginState,
    pub crashes: CrashRecord,
    pub plugin_dir: PathBuf,
}

/// Lifecycle errors.
#[derive(Debug, Error)]
pub enum LifecycleError {
    #[error("plugin '{name}' is quarantined after {crashes} crashes in 5 minutes. Run `cascade plugin enable {name}` to re-enable.")]
    Quarantined { name: String, crashes: usize },

    #[error("invalid state transition for plugin '{name}': {from:?} -> {to:?}")]
    InvalidTransition {
        name: String,
        from: PluginState,
        to: PluginState,
    },

    #[error("plugin '{name}' not found in registry")]
    NotFound { name: String },

    #[error("failed to persist crash state: {0}")]
    PersistError(String),
}

/// Registry of all known plugin slots, keyed by plugin name.
#[derive(Debug, Default)]
pub struct PluginRegistry {
    slots: Mutex<HashMap<String, Arc<Mutex<PluginSlot>>>>,
}

impl PluginRegistry {
    pub fn new() -> Self {
        Self::default()
    }

    /// Register a newly discovered plugin (initial state: `Discovered`).
    pub fn register(&self, name: &str, plugin_dir: PathBuf, crashes: CrashRecord) {
        let slot = Arc::new(Mutex::new(PluginSlot {
            name: name.to_owned(),
            state: PluginState::Discovered,
            crashes,
            plugin_dir,
        }));
        self.slots.lock().unwrap().insert(name.to_owned(), slot);
    }

    /// Advance a plugin to the next lifecycle state.
    ///
    /// # Why explicit transitions
    /// State-machine transitions prevent a plugin from jumping from `Crashed`
    /// directly to `Active` without going through `Loading` (which re-validates
    /// the module). All allowed transitions are enumerated here.
    pub fn transition(&self, name: &str, to: PluginState) -> Result<(), LifecycleError> {
        let slots = self.slots.lock().unwrap();
        let slot_arc = slots.get(name).ok_or_else(|| LifecycleError::NotFound {
            name: name.to_owned(),
        })?;
        let mut slot = slot_arc.lock().unwrap();

        // Block startup if quarantined.
        if slot.state == PluginState::Quarantined && to == PluginState::Loading {
            return Err(LifecycleError::Quarantined {
                name: name.to_owned(),
                crashes: slot.crashes.recent_crashes.len(),
            });
        }

        let allowed = Self::allowed_transition(&slot.state, &to);
        if !allowed {
            return Err(LifecycleError::InvalidTransition {
                name: name.to_owned(),
                from: slot.state.clone(),
                to,
            });
        }

        slot.state = to;
        Ok(())
    }

    /// Record a WASM trap for a plugin, potentially quarantining it.
    pub fn record_crash(&self, name: &str) -> Result<PluginState, LifecycleError> {
        let slots = self.slots.lock().unwrap();
        let slot_arc = slots.get(name).ok_or_else(|| LifecycleError::NotFound {
            name: name.to_owned(),
        })?;
        let mut slot = slot_arc.lock().unwrap();

        let should_quarantine = slot.crashes.record_crash();
        slot.state = if should_quarantine {
            PluginState::Quarantined
        } else {
            PluginState::Crashed
        };
        Ok(slot.state.clone())
    }

    /// Re-enable a quarantined plugin (clears crash counter).
    pub fn enable(&self, name: &str) -> Result<(), LifecycleError> {
        let slots = self.slots.lock().unwrap();
        let slot_arc = slots.get(name).ok_or_else(|| LifecycleError::NotFound {
            name: name.to_owned(),
        })?;
        let mut slot = slot_arc.lock().unwrap();
        slot.crashes = CrashRecord::default();
        slot.state = PluginState::Discovered;
        Ok(())
    }

    /// Current state of a named plugin.
    pub fn state(&self, name: &str) -> Option<PluginState> {
        self.slots
            .lock()
            .unwrap()
            .get(name)
            .map(|s| s.lock().unwrap().state.clone())
    }

    /// List all plugins with their current states.
    pub fn list(&self) -> Vec<(String, PluginState)> {
        self.slots
            .lock()
            .unwrap()
            .values()
            .map(|s| {
                let s = s.lock().unwrap();
                (s.name.clone(), s.state.clone())
            })
            .collect()
    }

    fn allowed_transition(from: &PluginState, to: &PluginState) -> bool {
        use PluginState::*;
        matches!(
            (from, to),
            (Discovered, Validating)
                | (Validating, Loading)
                | (Validating, ValidationFailed)
                | (Loading, Active)
                | (Loading, LoadFailed)
                | (Active, Unloading)
                | (Unloading, Unloaded)
                // Hot reload: Unloaded -> Discovered to re-enter the pipeline.
                | (Unloaded, Discovered)
                | (Crashed, Loading) // Manual reload.
                | (Quarantined, Discovered) // After `cascade plugin enable`.
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normal_lifecycle() {
        let reg = PluginRegistry::new();
        reg.register("my-plugin", PathBuf::from("/tmp/my-plugin"), Default::default());

        reg.transition("my-plugin", PluginState::Validating).unwrap();
        reg.transition("my-plugin", PluginState::Loading).unwrap();
        reg.transition("my-plugin", PluginState::Active).unwrap();
        assert_eq!(reg.state("my-plugin"), Some(PluginState::Active));
    }

    #[test]
    fn quarantine_after_three_crashes() {
        let reg = PluginRegistry::new();
        reg.register("crashy", PathBuf::from("/tmp/crashy"), Default::default());
        reg.transition("crashy", PluginState::Validating).unwrap();
        reg.transition("crashy", PluginState::Loading).unwrap();
        reg.transition("crashy", PluginState::Active).unwrap();

        // Two crashes -> Crashed state.
        assert_eq!(reg.record_crash("crashy").unwrap(), PluginState::Crashed);
        reg.transition("crashy", PluginState::Loading).unwrap();
        reg.transition("crashy", PluginState::Active).unwrap();
        assert_eq!(reg.record_crash("crashy").unwrap(), PluginState::Crashed);
        reg.transition("crashy", PluginState::Loading).unwrap();
        reg.transition("crashy", PluginState::Active).unwrap();

        // Third crash -> Quarantined.
        assert_eq!(reg.record_crash("crashy").unwrap(), PluginState::Quarantined);

        // Loading while quarantined is blocked.
        let err = reg.transition("crashy", PluginState::Loading).unwrap_err();
        assert!(matches!(err, LifecycleError::Quarantined { .. }));
    }

    #[test]
    fn enable_clears_quarantine() {
        let reg = PluginRegistry::new();
        reg.register("q", PathBuf::from("/tmp/q"), Default::default());

        // Reach Active, then crash 3 times to reach Quarantined.
        reg.transition("q", PluginState::Validating).unwrap();
        reg.transition("q", PluginState::Loading).unwrap();
        reg.transition("q", PluginState::Active).unwrap();
        reg.record_crash("q").unwrap();
        reg.transition("q", PluginState::Loading).unwrap();
        reg.transition("q", PluginState::Active).unwrap();
        reg.record_crash("q").unwrap();
        reg.transition("q", PluginState::Loading).unwrap();
        reg.transition("q", PluginState::Active).unwrap();
        let state = reg.record_crash("q").unwrap();
        assert_eq!(state, PluginState::Quarantined);

        reg.enable("q").unwrap();
        assert_eq!(reg.state("q"), Some(PluginState::Discovered));
    }
}
