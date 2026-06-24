import AppKit
import SwiftUI

// MARK: - DesktopPanelController
//
// Purpose: Owns an always-on-desktop floating panel that shows the Cascade fleet
//          table continuously — the native replacement for the retired Claw-Fleet
//          (Übersicht) desktop widget. Unlike the menu-bar popover (click to open),
//          this panel is painted on the desktop and stays visible.
// Inputs:  QuotaStore.load() — read from ~/.cascade/accounts/quota.json every 30s.
// Outputs: A borderless, opaque NSPanel hosting MenubarPopoverView, floating above app
//          windows, present across Spaces as a SINGLE window (no per-Space duplication —
//          the problem that made us drop Übersicht).
// Constraints:
//   - styleMask .borderless + .nonactivatingPanel: never steals focus / key window.
//   - level = .floating: stays glanceable above normal windows (a desktop-level widget
//     would hide behind every window — the reason nothing was visible before).
//     collectionBehavior keeps ONE window across all Spaces.
//   - Anchored to the PRIMARY screen's top-right (NSScreen.main can be a secondary
//     display in a multi-monitor layout, which pushed the panel off-screen).
//   - isMovableByWindowBackground: user drags it anywhere by its body (session-only).
//   - Self-contained 30s refresh timer; [weak self] to avoid a retain cycle.
// SPORT: .opencode/phases/sport/ — DesktopPanelController component (desktop fleet widget)

final class DesktopPanelController: NSObject {
    private var panel: NSPanel?
    private var timer: Timer?
    private var quota: QuotaStore?

    private static let visibleKey = "io.cascade.desktopPanel.visible"

    /// Build the panel, position it, load data, and start the refresh timer.
    /// Shown by default unless the user previously hid it.
    func setup() {
        let hosting = NSHostingController(rootView: MenubarPopoverView(quota: nil))
        let size = hosting.view.fittingSize
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: max(size.width, 320), height: max(size.height, 160)),
            styleMask: [.borderless, .nonactivatingPanel],
            backing: .buffered,
            defer: false
        )
        panel.isFloatingPanel = true
        panel.hidesOnDeactivate = false
        panel.isMovableByWindowBackground = true
        panel.isOpaque = true
        panel.backgroundColor = .windowBackgroundColor
        panel.hasShadow = true

        // Floating layer: stays visible above normal app windows so the fleet is
        // always glanceable (a desktop-level widget would hide behind every window).
        // Below the menu bar and modal panels; unobtrusive because it's small and
        // corner-anchored. Drag it anywhere; right-click the menu-bar icon to hide.
        panel.level = .floating
        // One window, present on every Space, never in Cmd-Tab / Exposé cycling — a
        // single shared instance (not the per-Space duplication that made us drop Übersicht).
        panel.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle, .fullScreenAuxiliary]

        // Fleet table host. The panel itself is opaque (windowBackgroundColor) so the
        // table reads clearly on the desktop against any wallpaper.
        let host = NSHostingView(rootView: DesktopCard(quota: nil))
        host.translatesAutoresizingMaskIntoConstraints = false
        let container = NSView(frame: panel.contentRect(forFrameRect: panel.frame))
        container.addSubview(host)
        NSLayoutConstraint.activate([
            host.leadingAnchor.constraint(equalTo: container.leadingAnchor),
            host.trailingAnchor.constraint(equalTo: container.trailingAnchor),
            host.topAnchor.constraint(equalTo: container.topAnchor),
            host.bottomAnchor.constraint(equalTo: container.bottomAnchor),
        ])
        panel.contentView = container
        self.panel = panel
        self.hostingView = host

        positionPanel(panel)
        refresh()

        timer = Timer.scheduledTimer(withTimeInterval: 30.0, repeats: true) { [weak self] _ in
            self?.refresh()
        }

        // Default visible; respect a prior hide.
        let visible = UserDefaults.standard.object(forKey: Self.visibleKey) as? Bool ?? true
        if visible { panel.orderFrontRegardless() }
    }

    private var hostingView: NSHostingView<DesktopCard>?

    /// Toggle desktop visibility (driven by the menu-bar icon click).
    func toggle() {
        guard let panel = panel else { return }
        if panel.isVisible {
            panel.orderOut(nil)
            UserDefaults.standard.set(false, forKey: Self.visibleKey)
        } else {
            refresh()
            panel.orderFrontRegardless()
            UserDefaults.standard.set(true, forKey: Self.visibleKey)
        }
    }

    var isVisible: Bool { panel?.isVisible ?? false }

    /// Reload quota.json and update the card (also called by the menu-bar refresh).
    func refresh() {
        DispatchQueue.global(qos: .background).async { [weak self] in
            let store = QuotaStore.load()
            DispatchQueue.main.async {
                guard let self else { return }
                self.quota = store
                self.hostingView?.rootView = DesktopCard(quota: store)
                if let panel = self.panel, let host = self.hostingView {
                    let fit = host.fittingSize
                    var f = panel.frame
                    // Keep the top-left anchored while height changes.
                    let newH = max(fit.height, 160)
                    f.origin.y += (f.size.height - newH)
                    f.size = NSSize(width: max(fit.width, 320), height: newH)
                    panel.setFrame(f, display: true)
                }
            }
        }
    }

    /// Place the panel near the top-right of the PRIMARY screen (the one carrying the
    /// menu bar, whose frame origin is (0,0)). NSScreen.main can return a secondary
    /// display in a multi-monitor layout, which pushed the panel off-screen.
    private func positionPanel(_ panel: NSPanel) {
        let primary = NSScreen.screens.first(where: { $0.frame.origin == .zero })
            ?? NSScreen.main
            ?? NSScreen.screens.first
        guard let screen = primary else { return }
        let vf = screen.visibleFrame
        let x = vf.maxX - panel.frame.width - 24
        let y = vf.maxY - panel.frame.height - 24
        panel.setFrameOrigin(NSPoint(x: x, y: y))
    }

    deinit {
        timer?.invalidate()
        NotificationCenter.default.removeObserver(self)
    }
}

// MARK: - DesktopCard
//
// Hosts the shared fleet table for the desktop panel. Reuses MenubarPopoverView verbatim
// so the desktop widget and the menu-bar popover always show identical data.

private struct DesktopCard: View {
    let quota: QuotaStore?

    var body: some View {
        MenubarPopoverView(quota: quota)
    }
}
