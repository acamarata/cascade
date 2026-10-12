import XCTest
import AppKit
@testable import CascadeMenuBar

final class DaemonClientTests: XCTestCase {
    override func setUp() {
        super.setUp()
        _ = NSApplication.shared
    }

    private func executable(_ key: String) throws -> URL {
        let path = try XCTUnwrap(ProcessInfo.processInfo.environment[key], "Required scheme pre-action binary: \(key)")
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: path), "Not executable: \(key)")
        return URL(fileURLWithPath: path)
    }

    private func seed(_ binary: URL, _ args: [String], environment: [String: String]) throws {
        let process = Process()
        process.executableURL = binary
        process.arguments = args
        process.environment = environment
        try process.run()
        process.waitUntilExit()
        XCTAssertEqual(process.terminationStatus, 0)
    }

    private func requireIsolatedKeychain(_ environment: [String: String]) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/security")
        process.arguments = ["default-keychain", "-d", "user"]
        process.environment = environment
        let output = Pipe()
        let error = Pipe()
        process.standardOutput = output
        process.standardError = error
        try process.run()
        process.waitUntilExit()
        let stdout = output.fileHandleForReading.readDataToEndOfFile()
        let stderr = String(decoding: error.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        guard process.terminationStatus != 0, stdout.isEmpty, stderr.contains("default keychain could not be found") else {
            XCTFail("Fresh HOME did not isolate the login keychain; refusing to start daemon")
            throw DaemonError.unauthorized
        }
    }

    private func observe(_ client: DaemonClient, rows: Int, source: String? = nil,
                         timeout: TimeInterval, action: () throws -> Void) throws {
        let updated = expectation(description: "\(rows) rows from \(source ?? "initial request")")
        var fulfilled = false
        client.onChange = {
            if client.snapshot?.rows.count == rows && (source == nil || client.lastSource == source) && !fulfilled {
                fulfilled = true
                updated.fulfill()
            }
        }
        try action()
        wait(for: [updated], timeout: timeout)
        XCTAssertEqual(client.snapshot?.rows.count, rows, client.error?.localizedDescription ?? "No client error")
    }

    func testRealDaemonSeedSSEAndPollFallback() throws {
        try skipWithoutDataProtection()
        let binary = try executable("CASCADE_TEST_BINARY")
        let seeder = try executable("CASCADE_TEST_SEEDER")
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("w-" + String(UUID().uuidString.prefix(8)))
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: home) }
        let root = home.appendingPathComponent("cascade")
        let env = ["HOME": home.path, "USERPROFILE": home.path, "CASCADE_HOME": root.path,
                   "PATH": ProcessInfo.processInfo.environment["PATH"] ?? "/usr/bin:/bin"]
        try requireIsolatedKeychain(env)
        let data = root.appendingPathComponent("data").path
        try seed(seeder, ["seed", "--data-dir", data, "--count", "2"], environment: env)
        let daemon = Process()
        daemon.executableURL = binary
        daemon.arguments = ["daemon", "run"]
        daemon.environment = env
        let log = home.appendingPathComponent("daemon.log")
        FileManager.default.createFile(atPath: log.path, contents: nil)
        let handle = try FileHandle(forWritingTo: log)
        daemon.standardOutput = handle
        daemon.standardError = handle
        try daemon.run()
        defer { daemon.terminate(); daemon.waitUntilExit(); try? handle.close() }
        let shared = SharedContainer(directory: home)
        let client = DaemonClient(environment: ["CASCADE_SOCKET_PATH": root.appendingPathComponent("daemon.sock").path],
                                  shared: shared, pollInterval: 1)
        defer { client.stop() }
        try observe(client, rows: 2, timeout: 15) { client.start() }
        XCTAssertEqual(client.snapshot?.rows.count, 2, (try? String(contentsOf: log)) ?? "")
        try observe(client, rows: 3, source: "sse", timeout: 15) {
            try seed(seeder, ["provider", "--data-dir", data, "--name", "widget-third"], environment: env)
        }
        client.sse.stop()
        try observe(client, rows: 4, source: "rpc", timeout: 2) {
            try seed(seeder, ["provider", "--data-dir", data, "--name", "user" + "@example.com"], environment: env)
        }
        let bytes = try Data(contentsOf: shared.file)
        XCTAssertEqual(try WidgetSnapshot.decode(bytes).rows.count, 4)
        assertNoPII(bytes)
    }

    func testDefaultSeedPreservesOriginalFiveProviders() throws {
        let seeder = try executable("CASCADE_TEST_SEEDER")
        let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: home) }
        let directory = home.appendingPathComponent("data")
        let environment = ["HOME": home.path, "USERPROFILE": home.path, "CASCADE_HOME": home.path]
        try seed(seeder, ["seed", "--data-dir", directory.path], environment: environment)
        let query = Process()
        query.executableURL = URL(fileURLWithPath: "/usr/bin/sqlite3")
        query.arguments = ["-readonly", directory.appendingPathComponent("providers.db").path,
                           "SELECT name FROM provider_records ORDER BY name;"]
        query.environment = environment
        let output = Pipe()
        query.standardOutput = output
        try query.run()
        let bytes = output.fileHandleForReading.readDataToEndOfFile()
        query.waitUntilExit()
        XCTAssertEqual(query.terminationStatus, 0)
        let names = String(decoding: bytes, as: UTF8.self).split(separator: "\n").map(String.init)
        XCTAssertEqual(names, ["user" + "@example.com", "widget-avail", "widget-limit", "widget-nolane", "widget-pool"])
    }

    func testUnconfiguredSocketInvalidatesPreviousRunSnapshot() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let shared = SharedContainer(directory: directory)
        try Data("previous-run".utf8).write(to: shared.file)
        let client = DaemonClient(environment: [:], shared: nil, resolveShared: { shared })
        client.start()
        XCTAssertEqual(client.error, .socketUnconfigured)
        XCTAssertNil(client.snapshot)
        let left = try? Data(contentsOf: shared.file)
        XCTAssertTrue(left == nil || left?.isEmpty == true, "Previous snapshot survived")
    }

    private func assertNoPII(_ bytes: Data) {
        let text = String(decoding: bytes, as: UTF8.self)
        // The five families in the daemon contract, with both absolute-path forms.
        for pattern in [#"[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}"#,
                        #"(?i)\b(?:https?|ssh|git|ftp)://\S+"#,
                        #"(?:^|[\s"'`])(?:/[A-Za-z0-9_.\-]+){2,}"#,
                        #"[A-Za-z]:\\[A-Za-z0-9_.\\\-]+"#,
                        #"\b[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?){1,}\.(?:local|lan|[A-Za-z]{2,})\b"#,
                        #"@[A-Za-z0-9_-]{2,32}\b(?:[^.]|$)"#] {
            XCTAssertNil(text.range(of: pattern, options: .regularExpression), pattern)
        }
    }
}
