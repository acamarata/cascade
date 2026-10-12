import AppKit

// Purpose: own the menu-bar status item, popover and daemon-client lifecycle.
// Inputs: process environment and client updates. Outputs: an interactive native status popover.
final class AppDelegate: NSObject, NSApplicationDelegate {
    let client: DaemonClient
    private(set) var statusItem: NSStatusItem?
    private let popover = NSPopover()
    private var ageTimer: Timer?

    init(environment: [String: String] = ProcessInfo.processInfo.environment, shared: SharedContainer? = nil) {
        client = DaemonClient(environment: environment, shared: shared)
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem?.button?.title = "Cascade"
        statusItem?.button?.target = self
        statusItem?.button?.action = #selector(togglePopover)
        popover.behavior = .transient
        client.onChange = { [weak self] in self?.render() }
        client.start()
        render()
        ageTimer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self, self.popover.isShown else { return }
            self.render()
        }
    }

    private func render() {
        let controller = NSViewController()
        controller.view = FleetPopoverView(snapshot: client.snapshot, error: client.error)
        popover.contentViewController = controller
        popover.contentSize = controller.view.fittingSize
        let count = client.error == nil ? (client.snapshot?.attentionCount ?? 0) : 0
        statusItem?.button?.title = count > 0 ? "Cascade (\(count))" : "Cascade"
    }

    @objc private func togglePopover() {
        guard let button = statusItem?.button else { return }
        if popover.isShown { popover.performClose(nil) }
        else { render(); popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY) }
    }

    func applicationWillTerminate(_ notification: Notification) {
        client.stop()
        ageTimer?.invalidate()
        ageTimer = nil
        if let statusItem { NSStatusBar.system.removeStatusItem(statusItem) }
        statusItem = nil
    }
}
