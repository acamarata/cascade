import XCTest
@testable import CascadeMenuBar

extension XCTestCase {
    // The shared snapshot file is written with FileProtectionType.complete (the widget's privacy rule).
    // A hosted macOS VM has no data protection to apply it, so the tests that publish a snapshot are skipped
    // there; the owner-present widget parity run on a real Mac covers them.
    func skipWithoutDataProtection() throws {
        let manager = FileManager.default
        let probe = manager.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? manager.removeItem(at: probe) }
        guard manager.createFile(atPath: probe.path, contents: nil) else { throw XCTSkip("no scratch file") }
        try? manager.setAttributes([.protectionKey: FileProtectionType.complete], ofItemAtPath: probe.path)
        let applied = (try? manager.attributesOfItem(atPath: probe.path))?[.protectionKey] as? FileProtectionType
        try XCTSkipUnless(applied == .complete, "this machine cannot apply FileProtectionType.complete")
    }
}

final class SharedContainerPrivacyTests: XCTestCase {
    func testDeniedRenameInvalidatesPreviouslyPublishedBytes() throws {
        try skipWithoutDataProtection()
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let shared = SharedContainer(directory: directory)
        let bytes = try ResultSlicer.result(in: WidgetModelsTests.fixture())
        defer {
            if FileManager.default.fileExists(atPath: shared.file.path) {
                try? setDeletionDenied(false, at: shared.file)
            }
            try? FileManager.default.removeItem(at: directory)
        }
        try shared.write(bytes)
        XCTAssertEqual(try Data(contentsOf: shared.file), bytes)
        try setDeletionDenied(true, at: shared.file)
        XCTAssertThrowsError(try shared.write(bytes))
        let stored = try? Data(contentsOf: shared.file)
        XCTAssertTrue(stored == nil || stored?.isEmpty == true, "Prior bytes remain readable after rename failure")
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: directory.path), ["widget-snapshot.json"])
    }

    private func setDeletionDenied(_ denied: Bool, at file: URL) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/chmod")
        process.arguments = denied ? ["+a", "everyone deny delete", file.path] : ["-N", file.path]
        try process.run()
        process.waitUntilExit()
        XCTAssertEqual(process.terminationStatus, 0)
    }

    func testUnwritableDirectoryInvalidatesPreviouslyPublishedBytes() throws {
        try skipWithoutDataProtection()
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let shared = SharedContainer(directory: directory)
        defer {
            try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
            try? FileManager.default.removeItem(at: directory)
        }
        let bytes = try ResultSlicer.result(in: WidgetModelsTests.fixture())
        try shared.write(bytes)
        XCTAssertEqual(try Data(contentsOf: shared.file), bytes)
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: directory.path)
        XCTAssertThrowsError(try shared.write(bytes))
        let stored = try? Data(contentsOf: shared.file)
        XCTAssertTrue(stored == nil || stored?.isEmpty == true, "Prior bytes remain readable")
    }

    func testReloadsTimelinesAfterEachSuccessfulWriteOnly() throws {
        try skipWithoutDataProtection()
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        var reloads = 0
        let shared = SharedContainer(directory: directory, reloadTimelines: { reloads += 1 })
        defer {
            try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
            try? FileManager.default.removeItem(at: directory)
        }
        let bytes = try ResultSlicer.result(in: WidgetModelsTests.fixture())
        try shared.write(bytes)
        XCTAssertEqual(reloads, 1)
        try shared.write(bytes)
        XCTAssertEqual(reloads, 2)
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: directory.path)
        XCTAssertThrowsError(try shared.write(bytes))
        XCTAssertEqual(reloads, 2, "A failed write reloaded the widget timelines")
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
        try shared.write(bytes)
        XCTAssertEqual(reloads, 3)
    }

    func testRawRPCAndSSEBytesProtectionModeAndTruncation() throws {
        try skipWithoutDataProtection()
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let shared = SharedContainer(directory: dir)
        let rpc = try ResultSlicer.result(in: WidgetModelsTests.fixture())
        try shared.write(rpc)
        XCTAssertEqual(try Data(contentsOf: shared.file), rpc)
        let sse = DaemonSSE(environment: [:])
        var payload: Data?
        sse.onSnapshot = { payload = $0 }
        sse.consume(try WidgetModelsTests.fixture("fixture_status_widget_changed_sse", ext: "txt"))
        let bytes = try XCTUnwrap(payload)
        XCTAssertLessThan(bytes.count, rpc.count)
        try shared.write(bytes)
        XCTAssertEqual(try Data(contentsOf: shared.file), bytes)
        let attributes = try FileManager.default.attributesOfItem(atPath: shared.file.path)
        XCTAssertEqual((attributes[.posixPermissions] as? NSNumber)?.intValue, 0o600)
        XCTAssertEqual(attributes[.protectionKey] as? FileProtectionType, .complete)
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), ["widget-snapshot.json"])
        let result = #"{ "nested": {"value": "brace } and {"} }"#
        for response in ["{\"result\":\(result),\"id\":1}", "{\"id\":1,\"result\":\(result)}"] {
            try shared.write(ResultSlicer.result(in: Data(response.utf8)))
            XCTAssertEqual(try Data(contentsOf: shared.file), Data(result.utf8))
        }
    }
}
