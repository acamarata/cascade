import AppKit

// MARK: - AppDelegate
//
// Purpose: NSApplicationDelegate that owns the MenubarController + DesktopPanelController
//          lifecycle for the Cascade Fleet accessory app.
// Why a classic AppKit entry point (not SwiftUI `App`): a SwiftUI `App` whose only
//          scene is `Settings` and which runs as an accessory (LSUIElement) does NOT
//          reliably invoke applicationDidFinishLaunching — so the status item and the
//          desktop panel were never created and nothing appeared on screen. The classic
//          NSApplication.run() path below fires the delegate deterministically.
// Constraints:
//   menubar/desktopPanel are retained for the app lifetime (AppDelegate is held by the
//   local `delegate` in CascadeMain.main() while app.run() blocks).
//   setActivationPolicy(.accessory) = no Dock icon (replaces LSUIElement reliance).
//   applicationShouldTerminateAfterLastWindowClosed = false: there are no standard windows.
// SPORT: .opencode/phases/sport/ — CascadeApp entry-point (Fleet menu-bar + desktop widget)

class AppDelegate: NSObject, NSApplicationDelegate {
    let menubar = MenubarController()
    let desktopPanel = DesktopPanelController()

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Always-on-desktop fleet panel (native replacement for the Claw-Fleet widget).
        desktopPanel.setup()
        // Menu-bar icon: left-click toggles the desktop panel, right-click opens the popover.
        menubar.desktopPanel = desktopPanel
        menubar.setup()
    }

    /// Keep alive when no windows are open (accessory app has no standard windows).
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        return false
    }
}

// MARK: - Entry point
//
// Classic AppKit bootstrap. NSApplication.run() drives the event loop and guarantees
// applicationDidFinishLaunching fires. The delegate is retained for the process lifetime
// because run() never returns until the app quits.

@main
enum CascadeMain {
    static func main() {
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        app.run()
    }
}
