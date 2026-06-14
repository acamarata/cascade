/// build.rs — cascade-tray build script
///
/// Purpose: No-op.  The previous build.rs probed libappindicator3-0.1 via
///   pkg-config and emitted cfg flags to gate the AppIndicator3 FFI backend.
///   That backend has been replaced by ksni (pure-Rust StatusNotifierItem over
///   zbus/D-Bus, MIT license, no GTK, no LGPL transitive deps).  ksni has no
///   system-library requirements so no pkg-config probe is needed.
///
/// Inputs: none.
/// Outputs: none.
/// Constraints: keep this file so cargo does not strip it from the manifest.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
fn main() {}
