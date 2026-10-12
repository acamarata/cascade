import AppKit

// Purpose: launch the non-sandboxed menu-bar application. AppDelegate owns all runtime work.
// Inputs: process environment. Outputs: AppKit event loop; XCTest owns its own lifecycle.
@main
enum CascadeMenuBarApp {
    static func main() {
        let app = NSApplication.shared
        let delegate = AppDelegate()
        if ProcessInfo.processInfo.environment["XCTestConfigurationFilePath"] == nil {
            app.delegate = delegate
        }
        app.setActivationPolicy(.accessory)
        withExtendedLifetime(delegate) { app.run() }
    }
}
