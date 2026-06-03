import AppKit
import SwiftUI

// MARK: - MenubarController
//
// Purpose: Manage the NSStatusItem (tray icon) and NSPopover for the CascadeApp
//          menubar companion. Queries the cascaded daemon via DaemonIPCClient for
//          live health state; falls back to CascadeCache file-age check when the
//          daemon is not reachable. Refreshes every 30 seconds.
// Inputs:  DaemonIPCClient.queryStatus() — live health; nil when daemon is offline.
//          CascadeCache.load() — file-age fallback when daemon is nil.
// Outputs: Status-bar icon (template SF Symbol) + transient NSPopover with MenubarPopoverView.
// Constraints:
//   Must be in CascadeApp target only — NOT the WidgetExtension process.
//   DaemonIPCClient dispatches to a background queue and calls completion on main.
//   Timer uses [weak self] to avoid retain cycle after statusItem deallocates.
//   NSImage.isTemplate = true ensures automatic dark/light tint adaptation.
// Icon states:
//   checkmark.circle.fill   — daemon healthy (status == "healthy"), cache present
//   exclamationmark.triangle — daemon healthy but cache stale, OR daemon degraded
//   circle.slash             — daemon not responding (nil) — no cache either
// SPORT: .opencode/phases/sport/ — MenubarController component (P2, T-P2-E04-10)

class MenubarController: NSObject {
    private var statusItem: NSStatusItem?
    private var popover = NSPopover()
    private var timer: Timer?

    /// Install the status item, configure the click handler, and start the refresh timer.
    func setup() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        updateIcon()

        // Refresh every 30 seconds to match widget timeline cadence.
        timer = Timer.scheduledTimer(withTimeInterval: 30.0, repeats: true) { [weak self] _ in
            self?.updateIcon()
        }

        // Wire the click handler to toggle the popover.
        if let button = statusItem?.button {
            button.action = #selector(togglePopover(_:))
            button.target = self
        }

        // Embed SwiftUI popover content via NSHostingController.
        // .transient closes the popover when the user clicks outside it.
        popover.contentViewController = NSHostingController(
            rootView: MenubarPopoverView(cache: CascadeCache.load() ?? .placeholder)
        )
        popover.behavior = .transient
    }

    /// Query the daemon via IPC and update the icon SF Symbol + popover content.
    ///
    /// Priority:
    ///  1. DaemonIPCClient.queryStatus() — live response from cascaded.
    ///     - status == "healthy"  + cache fresh  → checkmark.circle.fill
    ///     - status == "healthy"  + cache stale  → exclamationmark.triangle
    ///     - status != "healthy"  (degraded etc) → exclamationmark.triangle
    ///  2. Daemon nil (socket absent / ECONNREFUSED) → cache-age fallback:
    ///     - cache present + fresh  → checkmark.circle.fill   (cached healthy)
    ///     - cache present + stale  → exclamationmark.triangle
    ///     - cache absent           → circle.slash
    func updateIcon() {
        DaemonIPCClient().queryStatus { [weak self] daemonStatus in
            guard let self else { return }
            let cache = CascadeCache.load()
            let imageName: String

            if let ds = daemonStatus {
                // Daemon responded — use live status as primary signal.
                let healthy = (ds.status == "healthy")
                let uptimeOk = (ds.uptime_seconds ?? 0) >= 10
                if healthy && uptimeOk {
                    // Daemon is healthy; secondary signal is cache freshness.
                    imageName = (cache?.isStale ?? true) ? "exclamationmark.triangle" : "checkmark.circle.fill"
                } else {
                    // Daemon running but degraded or just started.
                    imageName = "exclamationmark.triangle"
                }
            } else {
                // Daemon not responding — fall back to cache-age check.
                if let cache = cache {
                    imageName = cache.isStale ? "exclamationmark.triangle" : "checkmark.circle.fill"
                } else {
                    imageName = "circle.slash"
                }
            }

            let img = NSImage(systemSymbolName: imageName, accessibilityDescription: "Cascade")
            img?.isTemplate = true
            self.statusItem?.button?.image = img

            // Refresh the popover view with latest cache so it is current when opened.
            if let cache = cache {
                self.popover.contentViewController = NSHostingController(
                    rootView: MenubarPopoverView(cache: cache)
                )
            }
        }
    }

    /// Toggle the NSPopover relative to the status-bar button.
    @objc func togglePopover(_ sender: Any?) {
        if let button = statusItem?.button {
            if popover.isShown {
                popover.performClose(sender)
            } else {
                popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            }
        }
    }
}
