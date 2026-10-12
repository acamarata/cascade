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
        let delegate = AppDelegate(environment: [:], shared: SharedContainer(directory: directory))
        delegate.applicationDidFinishLaunching(Notification(name: Notification.Name("test")))
        XCTAssertEqual(delegate.client.error, .socketUnconfigured)
        XCTAssertNotNil(delegate.statusItem)
        delegate.applicationWillTerminate(Notification(name: Notification.Name("test")))
        XCTAssertNil(delegate.statusItem)
    }

    func testStorageFailureClearsSnapshotAndBadgeAfterSuccessfulWrite() throws {
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

    private func withPublishedSnapshot(_ action: (AppDelegate, SharedContainer, Data) throws -> Void) throws {
        _ = NSApplication.shared
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let shared = SharedContainer(directory: directory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let delegate = AppDelegate(environment: ["CASCADE_SOCKET_PATH": directory.appendingPathComponent("absent.sock").path],
                                   shared: shared)
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
