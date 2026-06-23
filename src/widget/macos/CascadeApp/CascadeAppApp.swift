import SwiftUI
import AppKit

// MARK: - AppDelegate
//
// Purpose: NSApplicationDelegate that owns MenubarController lifecycle.
//          Menu-bar-only app: LSUIElement = true in Info.plist suppresses Dock icon.
//          No WindowGroup — the only UI surface is the NSPopover fleet table.
// Constraints:
//   MenubarController must be retained for the app lifetime (stored as let on AppDelegate).
//   applicationShouldTerminateAfterLastWindowClosed must return false — there are no
//   windows, so without this the app would quit immediately on macOS 13+.
// SPORT: .opencode/phases/sport/ — CascadeApp entry-point (Fleet menu-bar app)

class AppDelegate: NSObject, NSApplicationDelegate {
    let menubar = MenubarController()

    func applicationDidFinishLaunching(_ notification: Notification) {
        menubar.setup()
    }

    /// Keep alive when no windows are open (menu-bar-only app has no windows).
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        return false
    }
}

// MARK: - CascadeAppApp
//
// SwiftUI App entry-point. No scenes declared — AppDelegate.menubar owns all UI.
// The @NSApplicationDelegateAdaptor wires AppKit lifecycle into the SwiftUI app.

@main
struct CascadeAppApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        // No WindowGroup — menu-bar-only. Settings scene kept minimal so the
        // @main struct satisfies the `App` protocol (body cannot be empty).
        Settings { EmptyView() }
    }
}
