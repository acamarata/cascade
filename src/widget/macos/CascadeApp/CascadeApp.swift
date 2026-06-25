import AppKit
import SwiftUI

// MARK: - CascadeApp entry point
//
// Purpose: Classic AppKit bootstrap for the Cascade Fleet menu-bar + desktop widget.
// Why NOT SwiftUI @main App: a SwiftUI App with only a Settings scene under LSUIElement
// does NOT fire applicationDidFinishLaunching reliably — the status item and desktop panel
// are never created. The NSApplication.run() path fires the delegate deterministically.
// This mirrors the pattern documented in CascadeAppApp.swift (prior from-scratch version)
// and the lesson captured in ~/.claude/projects/.../memory/lesson_cc_hijack_release_builds.md.
//
// Architecture (ported from ClawFleet ClawFleetApp.swift / DesktopManager):
//   CascadeStore     — ObservableObject owning the 60s poll loop + file watchers
//   CascadeDesktopManager — wraps DesktopWindowController; drives toggle + visibility state
//   NSStatusItem     — menu-bar icon; left-click toggles desktop panel, right-click opens popover
//   NSPopover        — hosts CascadeMenuBarContentView (the full fleet table)
//   DesktopWindowController — borderless NSPanel at desktopIconWindow level, always-on-desktop

// MARK: - CascadeDesktopManager

/// Observable wrapper around DesktopWindowController.
/// Kept @MainActor so SwiftUI views can read `isVisible` reactively.
@MainActor
final class CascadeDesktopManager: ObservableObject {
    @Published private(set) var isVisible: Bool = CascadeDesktopVisibilityStore.load()
    private var controller: DesktopWindowController?

    func launch(store: CascadeStore) {
        guard controller == nil else { return }
        let wc = DesktopWindowController(store: store)
        controller = wc
        if isVisible { wc.show() }
    }

    func toggle() {
        guard let wc = controller else { return }
        if wc.isVisible {
            wc.hide()
            isVisible = false
        } else {
            wc.show()
            isVisible = true
        }
        CascadeDesktopVisibilityStore.save(isVisible)
    }
}

// MARK: - Visibility persistence

enum CascadeDesktopVisibilityStore {
    private static var fileURL: URL {
        let dir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/cascade", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("desktop-window-visible.json")
    }

    static func load() -> Bool {
        guard let data = try? Data(contentsOf: fileURL),
              let dict = try? JSONDecoder().decode([String: Bool].self, from: data),
              let v = dict["visible"] else {
            return true   // visible by default on first launch
        }
        return v
    }

    static func save(_ visible: Bool) {
        let dict = ["visible": visible]
        if let data = try? JSONEncoder().encode(dict) {
            try? data.write(to: fileURL)
        }
    }
}

// MARK: - AppDelegate

@MainActor
class CascadeAppDelegate: NSObject, NSApplicationDelegate {
    // Single shared store — both menu bar and desktop window use this instance.
    private let store = CascadeStore()
    private let desktopManager = CascadeDesktopManager()

    private var statusItem: NSStatusItem?
    private var popover = NSPopover()

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Launch the always-on-desktop panel immediately (default visible).
        desktopManager.launch(store: store)

        // Build the menu-bar status item.
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        self.statusItem = item

        // Embed the reactive label view in the status button.
        if let button = item.button {
            let labelHost = NSHostingView(rootView: CascadeMenuBarLabelView(store: store))
            labelHost.translatesAutoresizingMaskIntoConstraints = false
            button.addSubview(labelHost)
            NSLayoutConstraint.activate([
                labelHost.leadingAnchor.constraint(equalTo: button.leadingAnchor, constant: 4),
                labelHost.trailingAnchor.constraint(equalTo: button.trailingAnchor, constant: -4),
                labelHost.centerYAnchor.constraint(equalTo: button.centerYAnchor),
            ])
            button.action = #selector(statusClicked(_:))
            button.target = self
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }

        // Popover is transient (closes on outside click).
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(
            rootView: CascadeMenuBarContentView(store: store)
                .environmentObject(desktopManager)
        )
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        return false
    }

    /// Any click → open popover (status + table + widget toggle + Open Cascade).
    /// Widget toggle lives inside the popover; no separate right-click path needed.
    @objc private func statusClicked(_ sender: Any?) {
        togglePopover()
    }

    private func togglePopover() {
        guard let button = statusItem?.button else { return }
        if popover.isShown {
            popover.performClose(nil)
        } else {
            // Refresh popover content before showing.
            popover.contentViewController = NSHostingController(
                rootView: CascadeMenuBarContentView(store: store)
                    .environmentObject(desktopManager)
            )
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        }
    }
}

// MARK: - Entry point

@main
enum CascadeMain {
    static func main() {
        let app = NSApplication.shared
        let delegate = CascadeAppDelegate()
        app.delegate = delegate
        app.setActivationPolicy(.accessory)
        app.run()
    }
}
