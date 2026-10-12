import AppKit

// Purpose: the unsigned-fallback desktop fleet panel, one level below normal windows on every Space.
// Inputs: snapshot and error from DaemonClient. Outputs: the popover's FleetPopoverView hosted at full size.
// Constraints: never a desktop-level window (macOS 14 "click wallpaper to reveal desktop" swallows its clicks)
// and never a normal-level one (it would cover work windows). Level changes are reported through onLevelChange.
final class DesktopFleetWindow: NSPanel {
    static let header = "Cascade fleet (unsigned fallback: the signed desktop widget is not installed)"

    /// CGWindowLevelForKey(.normalWindow) - 1: above the desktop, below every normal window.
    static var belowNormalLevel: NSWindow.Level {
        NSWindow.Level(rawValue: Int(CGWindowLevelForKey(.normalWindow)) - 1)
    }

    var onLevelChange: ((Int) -> Void)?
    let hosting = FirstMouseHostingView()
    let headerView = DragHandleView(title: DesktopFleetWindow.header)
    private(set) var table: FleetPopoverView?

    override var level: NSWindow.Level {
        didSet { if level != oldValue { onLevelChange?(level.rawValue) } }
    }

    init() {
        super.init(contentRect: NSRect(x: 80, y: 80, width: 560, height: 360),
                   styleMask: [.nonactivatingPanel, .titled, .fullSizeContentView], backing: .buffered, defer: true)
        titleVisibility = .hidden
        titlebarAppearsTransparent = true
        isReleasedWhenClosed = false
        hidesOnDeactivate = false
        isFloatingPanel = false
        level = Self.belowNormalLevel
        collectionBehavior = [.canJoinAllSpaces, .stationary, .ignoresCycle]
        isMovableByWindowBackground = true
        let stack = NSStackView(views: [headerView, hosting])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 0
        contentView = stack
        render(snapshot: nil, error: nil)
    }

    /// Replaces the hosted fleet table with one built from the latest client state.
    func render(snapshot: WidgetSnapshot?, error: DaemonError?, now: @escaping () -> Date = Date.init) {
        let view = FleetPopoverView(snapshot: snapshot, error: error, now: now)
        hosting.host(view)
        table = view
        setContentSize(contentView?.fittingSize ?? frame.size)
    }

    /// Shows the panel without activating the app or taking key focus.
    func toggle() {
        if isVisible { orderOut(nil) } else { orderFrontRegardless() }
    }
}

/// Hosts the fleet table; accepts the first click so rows react without activating the app first.
final class FirstMouseHostingView: NSView {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    func host(_ view: NSView) {
        subviews.forEach { $0.removeFromSuperview() }
        view.translatesAutoresizingMaskIntoConstraints = false
        addSubview(view)
        NSLayoutConstraint.activate([
            view.leadingAnchor.constraint(equalTo: leadingAnchor), view.trailingAnchor.constraint(equalTo: trailingAnchor),
            view.topAnchor.constraint(equalTo: topAnchor), view.bottomAnchor.constraint(equalTo: bottomAnchor)
        ])
    }
}

/// The header strip: states the fallback and drags the panel.
final class DragHandleView: NSView {
    let label: NSTextField

    init(title: String) {
        label = NSTextField(labelWithString: title)
        super.init(frame: .zero)
        label.translatesAutoresizingMaskIntoConstraints = false
        addSubview(label)
        NSLayoutConstraint.activate([
            label.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 16),
            label.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -16),
            label.topAnchor.constraint(equalTo: topAnchor, constant: 28),
            label.bottomAnchor.constraint(equalTo: bottomAnchor, constant: -4)
        ])
    }

    override var mouseDownCanMoveWindow: Bool { true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("Use init(title:)") }
}
