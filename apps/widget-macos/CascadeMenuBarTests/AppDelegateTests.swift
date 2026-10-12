import XCTest
import AppKit
@testable import CascadeMenuBar

final class AppDelegateTests: XCTestCase {
    // Paired coverage for the entrypoint's bundle contract and delegate lifecycle.
    func testApplicationMetadata() throws {
        let file = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appendingPathComponent("../CascadeMenuBar/Info.plist")
        let info = try XCTUnwrap(PropertyListSerialization.propertyList(from: Data(contentsOf: file), format: nil) as? [String: Any])
        XCTAssertEqual(info["LSUIElement"] as? Bool, true)
        XCTAssertEqual(info["LSMinimumSystemVersion"] as? String, "14.0")
        XCTAssertEqual(info["CascadeAppGroup"] as? String, "$(TeamIdentifierPrefix)com.acamarata.cascade.widget")
    }

    func testUnconfiguredPopoverAndLifecycle() {
        _ = NSApplication.shared
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let delegate = AppDelegate(environment: [:], shared: SharedContainer(directory: directory),
                                   pathGuard: Self.installedGuard())
        delegate.applicationDidFinishLaunching(Notification(name: Notification.Name("test")))
        XCTAssertEqual(delegate.client.error, .socketUnconfigured)
        XCTAssertNotNil(delegate.statusItem)
        delegate.applicationWillTerminate(Notification(name: Notification.Name("test")))
        XCTAssertNil(delegate.statusItem)
    }

    func testStorageFailureClearsSnapshotAndBadgeAfterSuccessfulWrite() throws {
        try skipWithoutDataProtection()
        try withPublishedSnapshot { delegate, shared, bytes in
            try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: shared.directory.path)
            defer {
                try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: shared.directory.path)
            }
            delegate.client.sse.onSnapshot?(bytes)
            assertFailedClosed(delegate, shared: shared, error: .storageUnavailable)
        }
    }

    func testFetchFailureClearsSnapshotAndBadgeAndCanRecover() throws {
        try skipWithoutDataProtection()
        try withPublishedSnapshot { delegate, shared, bytes in
            delegate.client.sse.onError?(.unavailable)
            assertFailedClosed(delegate, shared: shared, error: .unavailable)
            delegate.client.sse.onSnapshot?(bytes)
            XCTAssertNil(delegate.client.error)
            XCTAssertNotNil(delegate.client.snapshot)
            XCTAssertEqual(delegate.statusItem?.button?.title, "Cascade (1)")
            XCTAssertEqual(try Data(contentsOf: shared.file), bytes)
        }
    }

    func testPanelShownAtLaunchAndMenuItemTogglesIt() throws {
        let delegate = AppDelegate(environment: [:], shared: SharedContainer(directory: scratch()),
                                   pathGuard: Self.installedGuard())
        let notification = Notification(name: Notification.Name("test"))
        delegate.applicationDidFinishLaunching(notification)
        defer { delegate.applicationWillTerminate(notification) }
        let panel = try XCTUnwrap(delegate.panel)
        XCTAssertTrue(panel.isVisible, "shown at launch")
        XCTAssertTrue(panel.headerView.label.stringValue.contains("unsigned fallback"))
        let item = try XCTUnwrap(delegate.statusMenu.items.first { $0.action == #selector(AppDelegate.toggleDesktopPanel) })
        XCTAssertTrue(item.target === delegate)
        XCTAssertEqual(item.title, "Hide Desktop Panel")
        NSApp.sendAction(try XCTUnwrap(item.action), to: item.target, from: item)
        XCTAssertFalse(panel.isVisible)
        XCTAssertEqual(item.title, "Show Desktop Panel")
        NSApp.sendAction(try XCTUnwrap(item.action), to: item.target, from: item)
        XCTAssertTrue(panel.isVisible)
        delegate.applicationWillTerminate(notification)
        XCTAssertNil(delegate.panel)
        XCTAssertFalse(panel.isVisible, "no UI survives termination")
    }

    func testPanelRendersTheClientSnapshot() throws {
        try skipWithoutDataProtection()
        try withPublishedSnapshot { delegate, _, _ in
            let snapshot = try XCTUnwrap(delegate.client.snapshot)
            XCTAssertEqual(delegate.panel?.table?.rows.count, snapshot.rows.count)
        }
    }

    func testSelfReportWrittenThroughTheDelegate() throws {
        try skipWithoutDataProtection()
        let directory = scratch()
        let status = directory.appendingPathComponent("data/widget/widget-status.json")
        let shared = SharedContainer(directory: directory.appendingPathComponent("group"))
        let delegate = AppDelegate(environment: ["CASCADE_SOCKET_PATH": directory.appendingPathComponent("absent.sock").path,
                                                 WidgetSelfReport.pathKey: status.path],
                                   shared: shared, pathGuard: Self.installedGuard())
        let notification = Notification(name: Notification.Name("test"))
        delegate.applicationDidFinishLaunching(notification)
        defer { delegate.applicationWillTerminate(notification) }
        var body = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: status)) as? [String: Any])
        XCTAssertEqual(body["bundle_path"] as? String, "/Applications/CascadeMenuBar.app")
        XCTAssertEqual(body["window_level"] as? Int, DesktopFleetWindow.belowNormalLevel.rawValue)
        XCTAssertTrue(body["first_rows_at"] is NSNull)
        delegate.client.sse.onSnapshot?(try ResultSlicer.result(in: WidgetModelsTests.fixture()))
        body = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: status)) as? [String: Any])
        XCTAssertNotNil(body["first_rows_at"] as? String)
        XCTAssertFalse(Self.labels(in: try XCTUnwrap(delegate.popover.contentViewController?.view))
            .contains(WidgetSelfReport.unsetMessage))
    }

    func testUnsetSelfReportPathShowsOnePopoverLine() throws {
        let delegate = AppDelegate(environment: [:], shared: SharedContainer(directory: scratch()),
                                   pathGuard: Self.installedGuard())
        let notification = Notification(name: Notification.Name("test"))
        delegate.applicationDidFinishLaunching(notification)
        defer { delegate.applicationWillTerminate(notification) }
        let labels = Self.labels(in: try XCTUnwrap(delegate.popover.contentViewController?.view))
        XCTAssertEqual(labels.filter { $0 == WidgetSelfReport.unsetMessage }.count, 1)
    }

    func testRefusedBundleShowsNoUIAndPollTickTearsDown() {
        var bundle = URL(fileURLWithPath: "/tmp/CascadeMenuBar.app")
        var terminations = 0
        let refused = AppDelegate(environment: [:], shared: SharedContainer(directory: scratch()),
                                  pathGuard: Self.installedGuard(bundle: { bundle }, terminate: { terminations += 1 }))
        refused.applicationDidFinishLaunching(Notification(name: Notification.Name("test")))
        XCTAssertNil(refused.statusItem)
        XCTAssertNil(refused.panel)
        XCTAssertEqual(terminations, 1)

        bundle = URL(fileURLWithPath: "/Applications/CascadeMenuBar.app")
        let running = AppDelegate(environment: [:], shared: SharedContainer(directory: scratch()),
                                  pathGuard: Self.installedGuard(bundle: { bundle }, terminate: { terminations += 1 }))
        running.applicationDidFinishLaunching(Notification(name: Notification.Name("test")))
        let panel = running.panel
        running.pollTick()
        XCTAssertNotNil(running.panel)
        bundle = URL(fileURLWithPath: "/Users/widget-test-user/.Trash/CascadeMenuBar.app")
        running.pollTick()
        XCTAssertNil(running.panel)
        XCTAssertNil(running.statusItem)
        XCTAssertEqual(panel?.isVisible, false)
        XCTAssertEqual(terminations, 2)
    }

    static func installedGuard(bundle: @escaping () -> URL = { URL(fileURLWithPath: "/Applications/CascadeMenuBar.app") },
                               terminate: @escaping () -> Void = {}) -> BundlePathGuard {
        BundlePathGuard(seams: .init(bundleURL: bundle, home: URL(fileURLWithPath: "/Users/widget-test-user"),
                                     exists: { _ in false }, launch: { _, done in done(false) },
                                     alert: { _ in }, terminate: terminate))
    }

    static func labels(in view: NSView) -> [String] {
        ((view as? NSTextField).map { [$0.stringValue] } ?? []) + view.subviews.flatMap { labels(in: $0) }
    }

    private func scratch() -> URL {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        addTeardownBlock { try? FileManager.default.removeItem(at: directory) }
        return directory
    }

    private func withPublishedSnapshot(_ action: (AppDelegate, SharedContainer, Data) throws -> Void) throws {
        _ = NSApplication.shared
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let shared = SharedContainer(directory: directory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let delegate = AppDelegate(environment: ["CASCADE_SOCKET_PATH": directory.appendingPathComponent("absent.sock").path],
                                   shared: shared, pathGuard: Self.installedGuard())
        let notification = Notification(name: Notification.Name("test"))
        delegate.applicationDidFinishLaunching(notification)
        defer { delegate.applicationWillTerminate(notification) }
        let bytes = try ResultSlicer.result(in: WidgetModelsTests.fixture())
        delegate.client.sse.onSnapshot?(bytes)
        XCTAssertNil(delegate.client.error)
        XCTAssertEqual(delegate.client.snapshot?.attentionCount, 1)
        XCTAssertEqual(delegate.statusItem?.button?.title, "Cascade (1)")
        XCTAssertEqual(try Data(contentsOf: shared.file), bytes)
        try action(delegate, shared, bytes)
    }

    private func assertFailedClosed(_ delegate: AppDelegate, shared: SharedContainer, error: DaemonError,
                                    file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertNil(delegate.client.snapshot, file: file, line: line)
        XCTAssertEqual(delegate.client.error, error, file: file, line: line)
        XCTAssertEqual(delegate.statusItem?.button?.title, "Cascade", file: file, line: line)
        let stored = try? Data(contentsOf: shared.file)
        XCTAssertTrue(stored == nil || stored?.isEmpty == true, "Prior bytes remain readable", file: file, line: line)
        let view = FleetPopoverView(snapshot: delegate.client.snapshot, error: delegate.client.error)
        XCTAssertTrue(view.rows.isEmpty, file: file, line: line)
        XCTAssertEqual(view.labels.map(\.stringValue), [error.localizedDescription], file: file, line: line)
    }
}
