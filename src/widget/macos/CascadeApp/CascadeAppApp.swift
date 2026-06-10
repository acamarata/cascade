import SwiftUI
import AppKit

// MARK: - AppDelegate
//
// Purpose: NSApplicationDelegate that owns MenubarController lifecycle.
//          CascadeApp runs as a regular NSApplication so AppDelegate is the right
//          hook for menubar setup — applicationDidFinishLaunching fires after the
//          run loop is ready, which is required before NSStatusBar.system is used.
// Constraints: MenubarController must be retained for the app lifetime; storing it
//              as a let on AppDelegate achieves that without a global variable.
// SPORT: .claude/docs/MASTER-APPS.md — CascadeApp entry-point row (P2, T-P2-E04-09)

class AppDelegate: NSObject, NSApplicationDelegate {
    let menubar = MenubarController()

    func applicationDidFinishLaunching(_ notification: Notification) {
        menubar.setup()
    }
}

// MARK: - CascadeAppApp

@main
struct CascadeAppApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}
