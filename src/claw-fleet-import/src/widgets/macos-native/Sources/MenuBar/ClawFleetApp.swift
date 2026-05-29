import SwiftUI
import AppKit

// MARK: - App entry point

@main
struct ClawFleetApp: App {
    // Single shared store — both the menu bar and the desktop window read from
    // this one instance so they stay in sync on every 60-second tick.
    @StateObject private var store = CacheStore()

    // The controller is held in an EnvironmentObject wrapper so MenuBarContentView
    // can call show/hide without needing a Binding back up to the App struct.
    @StateObject private var desktopManager = DesktopManager()

    var body: some Scene {
        MenuBarExtra {
            MenuBarContentView(store: store)
                .environmentObject(desktopManager)
        } label: {
            // The label view is rendered immediately at app launch — using its
            // onAppear is the earliest reliable hook to spawn the desktop window
            // without an AppDelegate, since MenuBarContentView.onAppear only
            // fires when the user opens the panel.
            MenuBarLabelView(store: store)
                .onAppear {
                    desktopManager.launch(store: store)
                }
        }
        .menuBarExtraStyle(.window)
    }
}

// MARK: - DesktopManager

/// Observable wrapper around DesktopWindowController.
///
/// Kept as an @MainActor ObservableObject so SwiftUI views can read
/// `isVisible` reactively and the App struct can pass it as an EnvironmentObject.
@MainActor
final class DesktopManager: ObservableObject {
    @Published private(set) var isVisible: Bool = DesktopVisibilityStore.load()

    private var controller: DesktopWindowController?

    func launch(store: CacheStore) {
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
        DesktopVisibilityStore.save(isVisible)
    }
}

// MARK: - Visibility persistence

enum DesktopVisibilityStore {
    private static var fileURL: URL {
        let dir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/claw-fleet", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.appendingPathComponent("desktop-window-visible.json")
    }

    static func load() -> Bool {
        guard let data = try? Data(contentsOf: fileURL),
              let dict = try? JSONDecoder().decode([String: Bool].self, from: data),
              let v = dict["visible"] else {
            return true  // default: visible on first launch
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
