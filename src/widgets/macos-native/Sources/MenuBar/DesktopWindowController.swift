import AppKit
import SwiftUI

// MARK: - DesktopWindowController

/// Manages the borderless, always-on-desktop NSPanel that floats below all
/// app windows while remaining visible across all Spaces.
///
/// Why NSPanel over NSWindow: NSPanel supports the .nonactivatingPanel style
/// mask, which prevents the panel from stealing key focus when clicked — the
/// foreground app stays active. NSWindow has no equivalent without subclassing
/// and overriding canBecomeKey/canBecomeMain.
///
/// Why .desktopIconWindow level: panels at the bare .desktopWindow level
/// are treated by AppKit as part of the desktop canvas — mouse events pass
/// through to Finder so the panel cannot be dragged or interacted with.
/// .desktopIconWindow sits one step above (above wallpaper, above icons,
/// still below every app window) and receives mouse events normally,
/// which is the level Übersicht-style widgets actually use.
@MainActor
final class DesktopWindowController: NSObject {

    private var panel: NSPanel?
    private let store: CacheStore
    private let focus = DesktopFocusMonitor()

    init(store: CacheStore) {
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
        // .titled is included in the style mask because certain NSPanel APIs
        // (notably the window shadow and title bar height calculations) behave
        // incorrectly when the mask is .borderless only. We immediately hide
        // the title bar via titleVisibility / titlebarAppearsTransparent so
        // the visual result is fully borderless.
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
        p.hasShadow = false   // shadow looks wrong on a rounded-rect SwiftUI view
        p.ignoresMouseEvents = false
        p.collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        p.isReleasedWhenClosed = false

        // Sit just above the wallpaper + desktop-icon canvas so mouse drag
        // and toggle clicks reach the panel instead of being routed to Finder.
        // Still below every app window via the standard "desktop icon" layer.
        let level = Int(CGWindowLevelForKey(.desktopIconWindow))
        p.level = NSWindow.Level(rawValue: level)

        // Embed the SwiftUI fleet view. The focus monitor drives the macOS-style
        // monochrome dimming when the desktop is not the active context.
        let contentView = NSHostingView(
            rootView: DesktopFleetView(store: store, focus: focus)
                .ignoresSafeArea()
        )
        contentView.autoresizingMask = [.width, .height]
        p.contentView = contentView

        // 360px is the exact macOS medium desktop-widget width (measured via
        // CGWindowList: system small widgets = 180, medium = 360). Matching it
        // makes the panel line up edge-to-edge with the Calendar / Weather
        // widgets. The compact column layout (numeric "5/23 10a" dates,
        // auto-hidden EX, W Rst minWidth) stays single-line at this width.
        let panelWidth: CGFloat = 360
        let size = NSSize(width: panelWidth, height: contentView.fittingSize.height + 24)
        p.setContentSize(size)

        // Restore the saved position exactly, or default to top-right with a
        // 24 px inset. We deliberately do NOT snap here — the panel may settle
        // on a different display than NSScreen.main on a multi-monitor setup,
        // and snapping against the wrong screen's visibleFrame anchor made the
        // saved origin drift a little on every launch. Grid snapping happens
        // only on an actual user drag (panelDidMove), where p.screen is valid.
        let initialOrigin = loadSavedPosition()
            ?? defaultOrigin(panelWidth: panelWidth, panelHeight: size.height)
        p.setFrameOrigin(initialOrigin)

        // Persist position whenever the user drags the window.
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
            .appendingPathComponent(".config/claw-fleet", isDirectory: true)
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

    // MARK: - Grid snapping

    /// Grid geometry measured directly from the macOS desktop widgets via
    /// CGWindowList (the system Calendar / Weather widgets): 180 pt cells laid
    /// out flush (no inter-cell gap), an 8 pt left margin from the screen edge,
    /// and the first row 38 pt below the screen top. Snapping to this exact grid
    /// makes the panel land in the same columns and rows as the native widgets
    /// instead of a parallel grid. 22 pt magnetic threshold; beyond it the free
    /// drop position is kept.
    private let gridCell: CGFloat = 180
    private let gridMarginLeft: CGFloat = 8
    private let gridMarginTop: CGFloat = 38
    private let snapThreshold: CGFloat = 22

    /// Re-entry guard. `setFrameOrigin` inside the snap handler fires another
    /// didMove notification; without this flag the snap would reschedule itself
    /// in a loop.
    private var isSnapping = false

    /// Debounce so the panel drags smoothly and only snaps once the user pauses
    /// or releases, rather than jumping between grid cells on every move event.
    private var snapWorkItem: DispatchWorkItem?

    @objc private func panelDidMove(_ note: Notification) {
        guard let p = note.object as? NSPanel, !isSnapping else { return }
        // Persist the raw position immediately so a crash mid-drag still
        // restores roughly where the user left it.
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

    /// Magnetically snaps the panel origin to the macOS desktop-widget grid.
    /// The X axis (AppKit left origin == CGWindow left) snaps to
    /// `marginLeft + n*cell`. The Y axis is trickier: macOS measures the top
    /// margin from the screen top while AppKit origins are bottom-left, so the
    /// panel TOP is converted to a distance-from-screen-top, snapped to
    /// `marginTop + n*cell`, then converted back. Each axis snaps independently
    /// and only when the nearest gridline is within `snapThreshold`.
    private func snapToGrid(_ origin: NSPoint, panelHeight: CGFloat, screen: NSScreen?) -> NSPoint {
        let frame = (screen ?? NSScreen.main ?? NSScreen.screens.first)?.frame ?? .zero

        // X — align the panel's left edge to the macOS widget columns.
        let xAnchor = frame.minX + gridMarginLeft
        let relX = origin.x - xAnchor
        let nearestX = (relX / gridCell).rounded() * gridCell
        let snappedX = abs(nearestX - relX) < snapThreshold ? xAnchor + nearestX : origin.x

        // Y — align the panel's top edge to the macOS widget rows.
        let topFromScreenTop = frame.maxY - (origin.y + panelHeight)
        let relY = topFromScreenTop - gridMarginTop
        let nearestY = (relY / gridCell).rounded() * gridCell
        let snappedTop = abs(nearestY - relY) < snapThreshold ? gridMarginTop + nearestY : topFromScreenTop
        let snappedY = frame.maxY - snappedTop - panelHeight

        return NSPoint(x: snappedX, y: snappedY)
    }

    private func defaultOrigin(panelWidth: CGFloat, panelHeight: CGFloat) -> NSPoint {
        // Prefer the primary display (the one whose AppKit frame origin is (0,0)).
        // NSScreen.main returns the screen with the menu bar, which on a multi-
        // monitor setup may not be the primary display.
        let screen = NSScreen.screens.first(where: { $0.frame.origin == .zero })
                     ?? NSScreen.main
                     ?? NSScreen.screens[0]
        let frame = screen.frame
        // Align to the macOS widget grid: left column, third row — directly
        // beneath the two rows the stock widgets occupy (rows 0 and 1). This
        // gives the panel the same 8 pt left margin as the system widgets.
        let x = frame.minX + gridMarginLeft
        let topFromScreenTop = gridMarginTop + gridCell * 2
        let y = frame.maxY - topFromScreenTop - panelHeight
        return NSPoint(x: x, y: y)
    }
}

// MARK: - DesktopFocusMonitor

/// Tracks whether the desktop is the active context, mirroring the macOS
/// "Dim widgets on desktop = Automatically" behavior: the system renders its
/// desktop widgets in monochrome while you work in an app, then restores full
/// color when you click the desktop. The signal is the frontmost application —
/// Finder frontmost means the desktop (or a Finder window) is in focus.
///
/// This is the closest a non-WidgetKit panel can get to the system behavior;
/// the system's own trigger is private. Edge case: the "Show Desktop" gesture
/// reveals widgets in full color without changing the frontmost app, which this
/// monitor cannot observe via public API, so the panel stays dimmed during a
/// transient Show-Desktop peek. Clicking the wallpaper (the common path) works.
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
