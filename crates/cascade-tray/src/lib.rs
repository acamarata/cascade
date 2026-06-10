//! # cascade-tray
//!
//! Platform-agnostic system-tray abstraction for the Cascade daemon.
//!
//! ## Purpose
//!
//! Provides a single trait ([`TrayHandle`]) plus supporting types so the
//! cascade daemon can drive the OS tray icon without depending on any
//! platform-specific libraries in its core. Platform backends implement
//! `TrayHandle` and live in feature-gated sibling crates (T-P2-E06-03/04/05).
//!
//! ## Public surface
//!
//! | Item | Description |
//! |------|-------------|
//! | [`TrayHandle`] | Trait for all platform tray backends |
//! | [`TrayState`] | Telemetry snapshot pushed by the daemon |
//! | [`DaemonStatus`] | Operational state enum for the daemon |
//! | [`TrayError`] | Typed error returned by `TrayHandle` methods |
//!
//! ## Design invariants
//!
//! - `TrayHandle` is **object-safe**: no generic methods, no `Self`-returning
//!   methods. Callers store backends as `Box<dyn TrayHandle>`.
//! - Platform dependencies are gated with `cfg(target_os = "…")` in
//!   `Cargo.toml` so this crate always compiles on all three OS targets.
//! - `TrayState` is fully serializable to allow daemon → tray IPC over JSON.
//!
//! ## SPORT
//! `.claude/docs/MASTER-CRATES.md` — cascade-tray entry.

mod action;
mod error;
mod state;

// Platform-specific backend modules — compiled only on their target OS so
// the crate remains cross-compilable without native GUI deps on every platform.
#[cfg(target_os = "macos")]
pub mod macos;

#[cfg(target_os = "windows")]
mod windows;

// Linux backend — AppIndicator3 with ksystemtray fallback (T-P2-E06-04).
#[cfg(target_os = "linux")]
pub mod linux;

pub use action::{TrayAction, TrayMenuItem, TrayMenuSpec};
pub use error::TrayError;
pub use state::{DaemonStatus, TrayState};

// Re-export platform backends so callers can access them without knowing the
// internal module path.
#[cfg(target_os = "macos")]
pub use macos::MacosTrayImpl;

#[cfg(target_os = "windows")]
pub use windows::WindowsTrayImpl;

#[cfg(target_os = "linux")]
pub use linux::LinuxTrayImpl;

/// Construct the platform-appropriate tray backend.
///
/// Purpose: single factory function so the cascade daemon can obtain a
///   `Box<dyn TrayHandle>` without knowing which concrete type is used on
///   the current OS at compile time.
/// Inputs: `label` — initial display name / icon title for the tray icon.
/// Outputs: `Ok(Box<dyn TrayHandle>)` on success; `Err(TrayError)` if the OS
///   fails to create the native tray resource.
/// Constraints: compiled only on platforms that have a backend implementation.
///   Linux always succeeds (NoOp fallback).
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
#[cfg(target_os = "macos")]
pub fn new_tray(label: &str) -> Result<Box<dyn TrayHandle>, TrayError> {
    Ok(Box::new(MacosTrayImpl::new(label)?))
}

#[cfg(target_os = "windows")]
pub fn new_tray(label: &str) -> Result<Box<dyn TrayHandle>, TrayError> {
    Ok(Box::new(WindowsTrayImpl::new(label)?))
}

/// Linux factory — always returns Ok (NoOp is the guaranteed fallback).
#[cfg(target_os = "linux")]
pub fn new_tray(label: &str) -> Result<Box<dyn TrayHandle>, TrayError> {
    Ok(Box::new(LinuxTrayImpl::new(label)?))
}

/// Platform-agnostic handle to the OS system-tray icon.
///
/// Purpose: decouple the cascade daemon from native tray libraries.
///   Each OS target (macOS, Linux, Windows) provides its own impl.
/// Inputs: `TrayState` snapshots from the daemon, user interactions via
///   menu callbacks.
/// Outputs: Updated native menu/icon; or `TrayError` on failure.
/// Constraints:
///   - Object-safe — no generic parameters, no `Self`-returning methods.
///   - `destroy` consumes `self` to enforce single teardown and prevent use
///     after the native resource has been freed.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
pub trait TrayHandle {
    /// Push a new state snapshot to the tray icon and menu.
    ///
    /// Implementations should refresh the tooltip text, icon badge, and any
    /// dynamic menu items (e.g. "Active agents: N") using the supplied state.
    ///
    /// # Errors
    /// Returns `TrayError::Platform` if the OS tray API rejects the update.
    fn update(&mut self, state: &TrayState) -> Result<(), TrayError>;

    /// Make the tray icon visible in the OS status bar.
    ///
    /// A no-op if the icon is already visible.
    ///
    /// # Errors
    /// Returns `TrayError::Platform` on OS API failure.
    fn show(&mut self) -> Result<(), TrayError>;

    /// Hide the tray icon from the OS status bar.
    ///
    /// A no-op if the icon is already hidden.
    fn hide(&mut self);

    /// Release all native resources held by this tray handle.
    ///
    /// Consumes `self` — after calling `destroy` the handle is gone.
    /// Implementations MUST remove the icon from the status bar and
    /// deregister any OS callbacks before returning.
    fn destroy(self);

    /// Set the menu specification for this tray icon.
    ///
    /// Default implementation: no-op (returns Ok(())).
    /// Implementations that support dynamic menus override this method.
    ///
    /// # Errors
    /// Returns `TrayError::Platform` if the OS API rejects the menu update.
    fn set_menu(&mut self, _spec: &TrayMenuSpec) -> Result<(), TrayError> {
        Ok(())
    }

    /// Get the last action that was triggered by a user clicking a menu item.
    ///
    /// Default implementation: always returns `None`.
    /// Implementations that track recent actions override this method.
    fn last_action(&self) -> Option<TrayAction> {
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Verify TrayHandle is object-safe: storing a boxed dyn reference must
    /// compile. This test catches accidental introduction of non-object-safe
    /// methods (e.g. generic params, `Self` returns) before they land in prod.
    ///
    /// Acceptance criterion: let _: Box<dyn TrayHandle> compiles.
    #[test]
    fn tray_handle_is_object_safe() {
        // A minimal no-op implementation to satisfy the trait bounds.
        struct NoopTray;
        impl TrayHandle for NoopTray {
            fn update(&mut self, _state: &TrayState) -> Result<(), TrayError> {
                Ok(())
            }
            fn show(&mut self) -> Result<(), TrayError> {
                Ok(())
            }
            fn hide(&mut self) {}
            fn destroy(self) {}
        }

        // Object-safety check: the compiler accepts `Box<dyn TrayHandle>`.
        // The new set_menu and last_action default methods do not break object-safety.
        let _: Box<dyn TrayHandle> = Box::new(NoopTray);
    }

    /// Verify TrayState serializes to JSON and deserializes back to the same
    /// value (serde round-trip). This guarantees the daemon can send state
    /// updates over IPC without data loss.
    ///
    /// Acceptance criterion:
    ///   serde_json::from_str(&serde_json::to_string(&TrayState::default())
    ///     .unwrap()).unwrap() == TrayState::default()
    #[test]
    fn tray_state_serde_round_trip() {
        let original = TrayState::default();
        let json = serde_json::to_string(&original).expect("TrayState must serialize");
        let restored: TrayState = serde_json::from_str(&json).expect("TrayState must deserialize");
        assert_eq!(original, restored, "serde round-trip must be lossless");
    }

    /// Verify a non-default TrayState (with populated fields) also survives
    /// a serde round-trip. Catches HashMap serialization edge cases.
    #[test]
    fn tray_state_serde_round_trip_populated() {
        let mut state = TrayState {
            rag_freshness_secs: 120,
            active_agents: 8,
            inbox_unread: 3,
            daemon_status: DaemonStatus::Running,
            ..Default::default()
        };
        state.tier_counts.insert("T0".to_string(), 1);
        state.tier_counts.insert("T2".to_string(), 7);

        let json = serde_json::to_string(&state).expect("must serialize");
        let restored: TrayState = serde_json::from_str(&json).expect("must deserialize");
        assert_eq!(state, restored);
    }
}
