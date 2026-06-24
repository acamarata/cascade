import AppKit
import SwiftUI

// MARK: - MenubarController
//
// Purpose: Manage the NSStatusItem (tray icon) and NSPopover for the Cascade Fleet
//          menu-bar app. Reads ~/.cascade/accounts/quota.json every 30 seconds and
//          on manual refresh. Drives icon state from quota freshness.
// Inputs:  QuotaStore.load() — reads quota.json / quota-store.json from ~/.cascade/
// Outputs: Status-bar icon (SF Symbol template) + transient NSPopover with Fleet table.
// Constraints:
//   LSUIElement = true in Info.plist ensures no Dock icon.
//   Timer uses [weak self] to avoid retain cycle.
//   NSImage.isTemplate = true ensures dark/light tint adaptation.
//   Single instance enforced by NSApplication (menu-bar-only, no window).
// Icon states:
//   circle.grid.2x2.fill   — quota data fresh (< 120s)
//   exclamationmark.triangle — quota data stale (>= 120s)
//   circle.slash             — no quota file found
// SPORT: .opencode/phases/sport/ — MenubarController component (Fleet popover)

class MenubarController: NSObject {
    private var statusItem: NSStatusItem?
    private var popover = NSPopover()
    private var timer: Timer?
    private var quota: QuotaStore?

    /// The always-on-desktop panel. The menu-bar icon toggles its visibility, and
    /// each refresh tick updates it alongside the popover. Owned by AppDelegate.
    weak var desktopPanel: DesktopPanelController?

    /// Install the status item, configure the click handler, and start the refresh timer.
    func setup() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)

        // Wire click handler first. Left-click toggles the desktop panel; right-click
        // opens the popover (controls + a focused read of the same table).
        if let button = statusItem?.button {
            button.action = #selector(statusClicked(_:))
            button.target = self
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }

        // Popover is transient — closes when user clicks outside.
        popover.behavior = .transient

        // Observe manual refresh requests from the popover "Refresh" button.
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(handleRefreshNow),
            name: .cascadeRefreshNow,
            object: nil
        )

        // Initial load.
        refresh()

        // Refresh every 30 seconds.
        timer = Timer.scheduledTimer(withTimeInterval: 30.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    // MARK: - Refresh

    @objc private func handleRefreshNow() {
        refresh()
        desktopPanel?.refresh()
    }

    /// Load quota.json, update icon, and refresh the popover content.
    private func refresh() {
        DispatchQueue.global(qos: .background).async { [weak self] in
            let store = QuotaStore.load()
            DispatchQueue.main.async {
                guard let self else { return }
                self.quota = store
                self.updateIcon(store: store)
                self.updatePopover(store: store)
            }
        }
    }

    private func updateIcon(store: QuotaStore?) {
        let imageName: String
        if let store = store {
            let age = store.ageSeconds ?? Int.max
            imageName = age < 120 ? "circle.grid.2x2.fill" : "exclamationmark.triangle"
        } else {
            imageName = "circle.slash"
        }
        let img = NSImage(systemSymbolName: imageName, accessibilityDescription: "Cascade Fleet")
        img?.isTemplate = true
        statusItem?.button?.image = img
    }

    private func updatePopover(store: QuotaStore?) {
        let view = MenubarPopoverView(quota: store)
        if popover.isShown {
            // Update in place without closing.
            popover.contentViewController = NSHostingController(rootView: view)
        } else {
            popover.contentViewController = NSHostingController(rootView: view)
        }
    }

    // MARK: - Toggle

    /// Route the menu-bar click: right-click → popover (controls), else → toggle the
    /// always-on-desktop panel. Falls back to the popover if no desktop panel is set.
    @objc func statusClicked(_ sender: Any?) {
        let isRight = NSApp.currentEvent?.type == .rightMouseUp
        if isRight || desktopPanel == nil {
            togglePopover(sender)
        } else {
            desktopPanel?.toggle()
        }
    }

    /// Toggle the NSPopover relative to the status-bar button.
    @objc func togglePopover(_ sender: Any?) {
        guard let button = statusItem?.button else { return }
        if popover.isShown {
            popover.performClose(sender)
        } else {
            // Refresh content just before showing.
            updatePopover(store: quota)
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        }
    }

    deinit {
        timer?.invalidate()
        NotificationCenter.default.removeObserver(self)
    }
}
