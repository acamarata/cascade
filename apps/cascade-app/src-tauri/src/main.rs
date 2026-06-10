// Cascade desktop app — Tauri 2 entry point.
//
// Why: Tauri requires a thin main.rs that prevents the Windows console window
// from appearing and delegates to the library crate. All real logic lives in
// commands.rs (IPC surface) and lib.rs (Tauri app builder).
//
// Constraints: must compile on macOS arm64/x64, Linux x64/arm64, Windows x64/arm64.

// On Windows release builds, hide the console window.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    cascade_app_lib::run();
}
