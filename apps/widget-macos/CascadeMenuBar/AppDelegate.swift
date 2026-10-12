import AppKit

// Purpose: own the menu-bar status item, popover, desktop panel, bundle-path guard, self-report and
// daemon-client lifecycle. Inputs: process environment and client updates.
// Outputs: an interactive native status popover, the desktop fleet panel and the self-report file.
// The guard runs at launch and on every poll interval; a refused bundle never shows any UI.
final class AppDelegate: NSObject, NSApplicationDelegate {
    let client: DaemonClient
    let pathGuard: BundlePathGuard
    let selfReport: WidgetSelfReport
    private let guardInterval: TimeInterval
    private(set) var statusItem: NSStatusItem?
    private(set) var panel: DesktopFleetWindow?
    let popover = NSPopover()
    let statusMenu = NSMenu()
    private(set) lazy var panelMenuItem = NSMenuItem(title: "Hide Desktop Panel", action: #selector(toggleDesktopPanel),
                                                    keyEquivalent: "")
    private var ageTimer: Timer?
    private var guardTimer: Timer?

    init(environment: [String: String] = ProcessInfo.processInfo.environment, shared: SharedContainer? = nil,
         pathGuard: BundlePathGuard? = nil, guardInterval: TimeInterval = DaemonPollFallback.productionInterval,
         now: @escaping () -> Date = Date.init) {
        client = DaemonClient(environment: environment, shared: shared)
        self.pathGuard = pathGuard ?? BundlePathGuard.production(environment: environment)
        selfReport = WidgetSelfReport(environment: environment, now: now)
        self.guardInterval = guardInterval
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        guard pathGuard.check() else { return }
        let panel = DesktopFleetWindow()
        self.panel = panel
        selfReport.launched(bundlePath: pathGuard.currentBundleURL.path, windowLevel: panel.level.rawValue)
        panel.onLevelChange = { [weak self] in self?.selfReport.levelChanged($0) }
        installStatusItem()
        client.onChange = { [weak self] in self?.clientChanged() }
        client.start()
        render()
        panel.orderFrontRegardless()
        updatePanelMenuItem()
        startTimers()
    }

    private func installStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem?.button?.title = "Cascade"
        statusItem?.button?.target = self
        statusItem?.button?.action = #selector(statusItemClicked)
        statusItem?.button?.sendAction(on: [.leftMouseUp, .rightMouseUp])
        popover.behavior = .transient
        panelMenuItem.target = self
        statusMenu.addItem(panelMenuItem)
        statusMenu.addItem(.separator())
        statusMenu.addItem(NSMenuItem(title: "Quit Cascade", action: #selector(NSApplication.terminate(_:)),
                                      keyEquivalent: "q"))
    }

    private func startTimers() {
        ageTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self, self.popover.isShown || self.panel?.isVisible == true else { return }
            self.render()
        }
        guardTimer = Timer.scheduledTimer(withTimeInterval: guardInterval, repeats: true) { [weak self] _ in
            self?.pollTick()
        }
    }

    /// One poll tick: re-checks the bundle location; a refused bundle tears its UI down first.
    func pollTick() {
        guard panel != nil, !pathGuard.check() else { return }
        tearDown()
    }

    private func clientChanged() {
        // A failed client clears its snapshot (DaemonClient.fail), so only an applied answer has rows.
        selfReport.answered(rowCount: client.snapshot?.rows.count ?? 0)
        render()
    }

    private func render() {
        let stack = NSStackView(views: [FleetPopoverView(snapshot: client.snapshot, error: client.error)])
        stack.orientation = .vertical
        stack.alignment = .leading
        if let line = selfReport.statusLine { stack.addArrangedSubview(NSTextField(labelWithString: line)) }
        let controller = NSViewController()
        controller.view = stack
        popover.contentViewController = controller
        popover.contentSize = controller.view.fittingSize
        panel?.render(snapshot: client.snapshot, error: client.error)
        let count = client.error == nil ? (client.snapshot?.attentionCount ?? 0) : 0
        statusItem?.button?.title = count > 0 ? "Cascade (\(count))" : "Cascade"
    }

    @objc private func statusItemClicked() {
        if NSApp.currentEvent?.type == .rightMouseUp { showStatusMenu() } else { togglePopover() }
    }

    private func showStatusMenu() {
        guard let statusItem else { return }
        updatePanelMenuItem()
        statusItem.menu = statusMenu
        statusItem.button?.performClick(nil)
        statusItem.menu = nil
    }

    private func togglePopover() {
        guard let button = statusItem?.button else { return }
        if popover.isShown { popover.performClose(nil) }
        else { render(); popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY) }
    }

    /// The status-item menu action: shows or hides the desktop panel.
    @objc func toggleDesktopPanel() {
        panel?.toggle()
        updatePanelMenuItem()
    }

    private func updatePanelMenuItem() {
        panelMenuItem.title = panel?.isVisible == true ? "Hide Desktop Panel" : "Show Desktop Panel"
    }

    func applicationWillTerminate(_ notification: Notification) { tearDown() }

    private func tearDown() {
        client.stop()
        ageTimer?.invalidate()
        ageTimer = nil
        guardTimer?.invalidate()
        guardTimer = nil
        if popover.isShown { popover.performClose(nil) }
        panel?.onLevelChange = nil
        panel?.close()
        panel = nil
        if let statusItem { NSStatusBar.system.removeStatusItem(statusItem) }
        statusItem = nil
    }
}
