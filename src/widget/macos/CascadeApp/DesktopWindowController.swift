import AppKit
import SwiftUI

// MARK: - DesktopWindowController
// Ported verbatim from ClawFleet DesktopWindowController.swift.
// Primary-screen guard: defaultOrigin() uses the screen whose frame.origin == .zero,
// not NSScreen.main — NSScreen.main returns the screen with the menu bar, which on
// a multi-monitor setup can be a secondary display (e.g. x=-1920), pushing the panel
// off-screen. The .zero origin is always the primary/built-in display.
// Position persistence: ~/.config/cascade/desktop-window-position.json

@MainActor
final class DesktopWindowController: NSObject {

    private var panel: NSPanel?
    private let store: CascadeStore
    private let focus = DesktopFocusMonitor()

    init(store: CascadeStore) {
        self.store = store
        super.init()
    }

    // MARK: - Lifecycle

    func show() {
        if panel == nil {
            panel = makePanel()
        }
        panel?.orderFront(nil)
    }

    func hide() {
        panel?.orderOut(nil)
    }

    var isVisible: Bool {
        panel?.isVisible ?? false
    }

    // MARK: - Panel construction

    private func makePanel() -> NSPanel {
        let mask: NSWindow.StyleMask = [
            .nonactivatingPanel,
            .borderless,
            .titled,
            .fullSizeContentView,
        ]
        let p = NSPanel(
            contentRect: .zero,
            styleMask: mask,
            backing: .buffered,
            defer: false
        )

        p.titleVisibility = .hidden
        p.titlebarAppearsTransparent = true
        p.isMovableByWindowBackground = true
        p.backgroundColor = .clear
        p.isOpaque = false
        p.hasShadow = false
        p.ignoresMouseEvents = false
        p.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        p.isReleasedWhenClosed = false

        // One step above the wallpaper + desktop icons so mouse events reach
        // the panel; still below every app window.
        let level = Int(CGWindowLevelForKey(.desktopIconWindow))
        p.level = NSWindow.Level(rawValue: level)

        let contentView = NSHostingView(
            rootView: DesktopFleetView(store: store, focus: focus)
                .ignoresSafeArea()
        )
        contentView.autoresizingMask = [.width, .height]
        p.contentView = contentView

        let panelWidth: CGFloat = 360
        let size = NSSize(width: panelWidth, height: contentView.fittingSize.height + 24)
        p.setContentSize(size)

        // Restore saved position or default to primary-screen top-left widget grid.
        let initialOrigin = loadSavedPosition()
            ?? defaultOrigin(panelWidth: panelWidth, panelHeight: size.height)
        p.setFrameOrigin(initialOrigin)

        NotificationCenter.default.addObserver(
            self,
            selector: #selector(panelDidMove),
            name: NSWindow.didMoveNotification,
            object: p
        )

        return p
    }

    // MARK: - Position persistence

    private var positionFileURL: URL {
        let config = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/cascade", isDirectory: true)
        try? FileManager.default.createDirectory(at: config, withIntermediateDirectories: true)
        return config.appendingPathComponent("desktop-window-position.json")
    }

    private func loadSavedPosition() -> NSPoint? {
        guard let data = try? Data(contentsOf: positionFileURL),
              let dict = try? JSONDecoder().decode([String: Double].self, from: data),
              let x = dict["x"], let y = dict["y"] else { return nil }
        return NSPoint(x: x, y: y)
    }

    private func savePosition(_ origin: NSPoint) {
        let dict = ["x": origin.x, "y": origin.y]
        if let data = try? JSONEncoder().encode(dict) {
            try? data.write(to: positionFileURL)
        }
    }

    // MARK: - Grid snapping (macOS desktop-widget grid: 180pt cells, 8pt left, 38pt top)

    private let gridCell: CGFloat = 180
    private let gridMarginLeft: CGFloat = 8
    private let gridMarginTop: CGFloat = 38
    private let snapThreshold: CGFloat = 22
    private var isSnapping = false
    private var snapWorkItem: DispatchWorkItem?

    @objc private func panelDidMove(_ note: Notification) {
        guard let p = note.object as? NSPanel, !isSnapping else { return }
        savePosition(p.frame.origin)
        snapWorkItem?.cancel()
        let work = DispatchWorkItem { [weak self, weak p] in
            guard let self, let p else { return }
            let snapped = self.snapToGrid(p.frame.origin, panelHeight: p.frame.height, screen: p.screen)
            guard snapped != p.frame.origin else { return }
            self.isSnapping = true
            p.setFrameOrigin(snapped)
            self.isSnapping = false
            self.savePosition(snapped)
        }
        snapWorkItem = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.15, execute: work)
    }

    private func snapToGrid(_ origin: NSPoint, panelHeight: CGFloat, screen: NSScreen?) -> NSPoint {
        let frame = (screen ?? NSScreen.main ?? NSScreen.screens.first)?.frame ?? .zero
        let xAnchor = frame.minX + gridMarginLeft
        let relX = origin.x - xAnchor
        let nearestX = (relX / gridCell).rounded() * gridCell
        let snappedX = abs(nearestX - relX) < snapThreshold ? xAnchor + nearestX : origin.x
        let topFromScreenTop = frame.maxY - (origin.y + panelHeight)
        let relY = topFromScreenTop - gridMarginTop
        let nearestY = (relY / gridCell).rounded() * gridCell
        let snappedTop = abs(nearestY - relY) < snapThreshold ? gridMarginTop + nearestY : topFromScreenTop
        let snappedY = frame.maxY - snappedTop - panelHeight
        return NSPoint(x: snappedX, y: snappedY)
    }

    /// Default: primary screen (frame.origin == .zero), left column, third row of widget grid.
    private func defaultOrigin(panelWidth: CGFloat, panelHeight: CGFloat) -> NSPoint {
        let screen = NSScreen.screens.first(where: { $0.frame.origin == .zero })
                     ?? NSScreen.main
                     ?? NSScreen.screens[0]
        let frame = screen.frame
        let x = frame.minX + gridMarginLeft
        let topFromScreenTop = gridMarginTop + gridCell * 2
        let y = frame.maxY - topFromScreenTop - panelHeight
        return NSPoint(x: x, y: y)
    }
}

// MARK: - DesktopFocusMonitor
// Tracks Finder frontmost state to drive macOS-style monochrome dimming
// when the user is working in an app vs. looking at the desktop.

@MainActor
final class DesktopFocusMonitor: ObservableObject {
    @Published private(set) var isDesktopActive: Bool

    init() {
        isDesktopActive = Self.finderIsFrontmost()
        NSWorkspace.shared.notificationCenter.addObserver(
            self,
            selector: #selector(activeAppChanged),
            name: NSWorkspace.didActivateApplicationNotification,
            object: nil
        )
    }

    deinit {
        NSWorkspace.shared.notificationCenter.removeObserver(self)
    }

    @objc private func activeAppChanged() {
        let active = Self.finderIsFrontmost()
        if active != isDesktopActive { isDesktopActive = active }
    }

    private static func finderIsFrontmost() -> Bool {
        NSWorkspace.shared.frontmostApplication?.bundleIdentifier == "com.apple.finder"
    }
}
