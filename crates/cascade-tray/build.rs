/// build.rs — cascade-tray build script
///
/// Purpose: Detect libappindicator-3 at build time on Linux so the crate can
///   compile with AppIndicator3 support when the library is present, or fall
///   back to a no-op stub + KsystemTray D-Bus path when it is absent.
/// Inputs: pkg-config tool, CARGO_CFG_TARGET_OS env var (set by cargo).
/// Outputs: `cargo:rustc-cfg=cascade_tray_appindicator` (library found) OR
///          `cargo:rustc-cfg=cascade_tray_no_appindicator` (library absent).
/// Constraints:
///   - Only runs the probe when CARGO_CFG_TARGET_OS == "linux" so cross-
///     compilation from macOS to Linux host still works correctly.
///   - build.rs runs on the BUILD HOST; cfg(target_os) here refers to the
///     TARGET, accessed via CARGO_CFG_TARGET_OS, not cfg() attributes.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
fn main() {
    // CARGO_CFG_TARGET_OS is set by cargo and reflects the compilation TARGET
    // (not the build host).  Use it instead of cfg(target_os) in build.rs.
    let target_os = std::env::var("CARGO_CFG_TARGET_OS").unwrap_or_default();
    if target_os != "linux" {
        return;
    }

    // probe_library emits cargo:rustc-link-lib and cargo:rustc-link-search
    // automatically when it succeeds.  On failure it does NOT panic — we
    // catch the Err and emit the fallback cfg flag instead.
    match pkg_config::probe_library("appindicator3-0.1") {
        Ok(_) => {
            println!("cargo:rustc-cfg=cascade_tray_appindicator");
        }
        Err(_) => {
            println!("cargo:rustc-cfg=cascade_tray_no_appindicator");
        }
    }
}
