import XCTest
@testable import CascadeMenuBar

/// Shared helpers for the reauthentication tests: temp directories, a recorder CLI and snapshots.
enum ReauthTestSupport {
    static let cli = "/usr/local/bin/cascade-fake"

    static func temporaryDirectory() throws -> URL {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("reauth-" + UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    /// An executable script that records its argv (one per line) to argv.txt and CASCADE_HOME to home.txt beside it.
    static func recorder(in directory: URL, name: String = "recorder") throws -> URL {
        let url = directory.appendingPathComponent(name)
        let body = "#!\(ReauthCommandFile.interpreter)\nd=\"$(dirname \"$0\")\"\nprintf '%s\\n' \"$@\" > \"$d/argv.txt\"\n"
            + "printf '%s' \"${CASCADE_HOME-unset}\" > \"$d/home.txt\"\n"
        try Data(body.utf8).write(to: url)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }

    static func row(_ ref: String, reauth: Bool, label: String = "Profile") -> WidgetRow {
        WidgetRow(ref: ref, label: label, kind: "profile", fiveHour: nil, sevenDay: nil,
                  state: reauth ? "auth-required" : "available", reauthRequired: reauth,
                  updatedAt: Date(timeIntervalSince1970: 0))
    }

    static func snapshot(_ rows: [WidgetRow]) -> WidgetSnapshot {
        WidgetSnapshot(rows: rows, attentionCount: rows.filter(\.reauthRequired).count, nodes: [], projects: nil,
                       generatedAt: Date(timeIntervalSince1970: 0), seq: 0)
    }

    /// Runs `executable arguments` to completion and returns its status, stdout and stderr.
    static func run(_ executable: String, _ arguments: [String], environment: [String: String] = [:],
                    currentDirectory: URL? = nil) throws -> (status: Int32, out: String, err: String) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        process.environment = environment
        process.currentDirectoryURL = currentDirectory
        let out = Pipe()
        let err = Pipe()
        process.standardOutput = out
        process.standardError = err
        try process.run()
        let outData = out.fileHandleForReading.readDataToEndOfFile()
        let errData = err.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        return (process.terminationStatus, String(decoding: outData, as: UTF8.self), String(decoding: errData, as: UTF8.self))
    }

    static func mode(_ url: URL) throws -> Int {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        return (attributes[.posixPermissions] as? Int ?? -1) & 0o777
    }
}

final class ReauthCommandFileTests: XCTestCase {
    private var directory: URL!
    private var cache: URL!
    private let shebang = "#!" + ReauthCommandFile.interpreter
    private let script = "/Applications/CascadeMenuBar.app/Contents/Resources/cascade-reauth.sh"

    override func setUpWithError() throws {
        directory = try ReauthTestSupport.temporaryDirectory()
        cache = directory.appendingPathComponent("cache", isDirectory: true)
    }

    override func tearDown() {
        try? FileManager.default.removeItem(at: directory)
    }

    private func lines(_ url: URL) throws -> [String] {
        try String(contentsOf: url, encoding: .utf8).split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
    }

    func testThreeLinesQuotedAndModes() throws {
        let file = try ReauthCommandFile.create(in: cache, script: script, cli: ReauthTestSupport.cli, home: nil, ref: "acct-a.pool_1")
        XCTAssertEqual(try lines(file), [shebang, "rm -f \"$0\"", "exec '\(script)' '\(ReauthTestSupport.cli)' 'acct-a.pool_1'", ""])
        XCTAssertEqual(try ReauthTestSupport.mode(file), 0o700)
        XCTAssertEqual(try ReauthTestSupport.mode(cache), 0o700)
        XCTAssertTrue(file.lastPathComponent.hasPrefix("reauth-"))
        XCTAssertEqual(file.pathExtension, "command")
    }

    func testFourLinesWhenCascadeHomeIsSet() throws {
        let file = try ReauthCommandFile.create(in: cache, script: script, cli: ReauthTestSupport.cli, home: "/Users/x/.cascade", ref: "r1")
        XCTAssertEqual(try lines(file), [shebang, "rm -f \"$0\"", "export CASCADE_HOME='/Users/x/.cascade'",
                                         "exec '\(script)' '\(ReauthTestSupport.cli)' 'r1'", ""])
    }

    func testSecondCreateOfTheSameNameFailsAndKeepsTheFirst() throws {
        let id = UUID()
        let first = try ReauthCommandFile.create(in: cache, id: id, script: script, cli: ReauthTestSupport.cli, home: nil, ref: "first")
        XCTAssertThrowsError(try ReauthCommandFile.create(in: cache, id: id, script: script, cli: ReauthTestSupport.cli, home: nil, ref: "second")) {
            XCTAssertEqual($0 as? LaunchError, .commandFileUnwritable)
        }
        XCTAssertTrue(try String(contentsOf: first, encoding: .utf8).contains("'first'"))
    }

    func testBundlePathWithAQuoteIsInvalidBundlePath() throws {
        for bad in ["/Applications/It's.app/cascade-reauth.sh", "relative/cascade-reauth.sh", "/a\nb/c.sh", ""] {
            XCTAssertThrowsError(try ReauthCommandFile.create(in: cache, script: bad, cli: ReauthTestSupport.cli, home: nil, ref: "r1")) {
                XCTAssertEqual($0 as? LaunchError, .invalidBundlePath, bad)
            }
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: cache.path), "a refusal must not create the directory")
    }

    func testCLIAndHomeValuesAreRefusedAsCliUnavailable() throws {
        for bad in ["/usr/bin/it's", "usr/bin/cascade", "/usr/bin/a\nb", "/usr/bin/a\rb", ""] {
            XCTAssertThrowsError(try ReauthCommandFile.create(in: cache, script: script, cli: bad, home: nil, ref: "r1")) {
                XCTAssertEqual($0 as? LaunchError, .cliUnavailable, bad)
            }
            XCTAssertThrowsError(try ReauthCommandFile.create(in: cache, script: script, cli: ReauthTestSupport.cli, home: bad, ref: "r1")) {
                XCTAssertEqual($0 as? LaunchError, .cliUnavailable, bad)
            }
        }
    }

    func testBadRefIsRefusedBeforeAnythingIsWritten() throws {
        for bad in ["a b", "a;b", "$(x)", "a\nb", "-x", "a'b", "", String(repeating: "a", count: 65), "x';touch /tmp/pwn;'"] {
            XCTAssertThrowsError(try ReauthCommandFile.create(in: cache, script: script, cli: ReauthTestSupport.cli, home: nil, ref: bad)) {
                XCTAssertEqual($0 as? LaunchError, .invalidProviderRef, bad)
            }
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: cache.path))
        XCTAssertTrue(ReauthCommandFile.isValidRef(String(repeating: "a", count: 64)))
    }

    func testLooseDirectoryIsTightenedTo0700() throws {
        try FileManager.default.createDirectory(at: cache, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o755])
        _ = try ReauthCommandFile.create(in: cache, script: script, cli: ReauthTestSupport.cli, home: nil, ref: "r1")
        XCTAssertEqual(try ReauthTestSupport.mode(cache), 0o700)
    }

    func testSymlinkedDirectoryAndPlantedFileLinkAreRefused() throws {
        let victim = directory.appendingPathComponent("victim.txt")
        try Data("keep".utf8).write(to: victim)
        let real = directory.appendingPathComponent("real", isDirectory: true)
        try FileManager.default.createDirectory(at: real, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let link = directory.appendingPathComponent("link")
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: real)
        XCTAssertThrowsError(try ReauthCommandFile.create(in: link, script: script, cli: ReauthTestSupport.cli, home: nil, ref: "r1")) {
            XCTAssertEqual($0 as? LaunchError, .commandFileUnwritable)
        }
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: real.path), [])
        let id = UUID()
        try FileManager.default.createSymbolicLink(at: real.appendingPathComponent("reauth-\(id.uuidString).command"), withDestinationURL: victim)
        XCTAssertThrowsError(try ReauthCommandFile.create(in: real, id: id, script: script, cli: ReauthTestSupport.cli, home: nil, ref: "r1")) {
            XCTAssertEqual($0 as? LaunchError, .commandFileUnwritable)
        }
        XCTAssertEqual(try String(contentsOf: victim, encoding: .utf8), "keep")
    }
}

final class ReauthCommandFileExecTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws { directory = try ReauthTestSupport.temporaryDirectory() }
    override func tearDown() { try? FileManager.default.removeItem(at: directory) }

    private func scriptURL() throws -> URL {
        try XCTUnwrap(Bundle(for: ReauthLauncher.self).url(forResource: ReauthLauncher.scriptName, withExtension: "sh"))
    }

    func testExecutingTheFileRunsTheRecorderWithExactArgvAndRemovesItself() throws {
        let recorder = try ReauthTestSupport.recorder(in: directory)
        let file = try ReauthCommandFile.create(in: directory.appendingPathComponent("cache"), script: try scriptURL().path,
                                                cli: recorder.path, home: "/Users/x/.cascade", ref: "acct-a.pool_1")
        let result = try ReauthTestSupport.run(ReauthCommandFile.interpreter, [file.path], environment: ["PATH": "/usr/bin:/bin"])
        XCTAssertEqual(result.status, 0, result.err)
        XCTAssertEqual(try String(contentsOf: directory.appendingPathComponent("argv.txt"), encoding: .utf8),
                       "provider\nreauth\nacct-a.pool_1\n")
        XCTAssertEqual(try String(contentsOf: directory.appendingPathComponent("home.txt"), encoding: .utf8), "/Users/x/.cascade")
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path), "the command file must delete itself")
    }

    func testNonASCIIPathsRunThroughTheLauncherChain() throws {
        let accented = directory.appendingPathComponent("D\u{e9}veloppement", isDirectory: true)
        try FileManager.default.createDirectory(at: accented, withIntermediateDirectories: true)
        let recorder = try ReauthTestSupport.recorder(in: accented)
        let file = try ReauthCommandFile.create(in: directory.appendingPathComponent("cache"), script: try scriptURL().path,
                                                cli: recorder.path, home: accented.path, ref: "r1")
        let result = try ReauthTestSupport.run(ReauthCommandFile.interpreter, [file.path], environment: ["PATH": "/usr/bin:/bin"])
        XCTAssertEqual(result.status, 0, result.err)
        XCTAssertEqual(try String(contentsOf: accented.appendingPathComponent("home.txt"), encoding: .utf8), accented.path)
    }

    func testNoHomeLineLeavesCascadeHomeUntouched() throws {
        let recorder = try ReauthTestSupport.recorder(in: directory)
        let file = try ReauthCommandFile.create(in: directory.appendingPathComponent("cache"), script: try scriptURL().path,
                                                cli: recorder.path, home: nil, ref: "r1")
        let result = try ReauthTestSupport.run(ReauthCommandFile.interpreter, [file.path], environment: ["PATH": "/usr/bin:/bin"])
        XCTAssertEqual(result.status, 0, result.err)
        XCTAssertEqual(try String(contentsOf: directory.appendingPathComponent("home.txt"), encoding: .utf8), "unset")
    }

    func testSpacesInPathsSurviveSingleQuoting() throws {
        let spaced = directory.appendingPathComponent("with space", isDirectory: true)
        try FileManager.default.createDirectory(at: spaced, withIntermediateDirectories: true)
        let recorder = try ReauthTestSupport.recorder(in: spaced)
        let file = try ReauthCommandFile.create(in: directory.appendingPathComponent("cache"), script: try scriptURL().path,
                                                cli: recorder.path, home: spaced.path, ref: "r1")
        let result = try ReauthTestSupport.run(ReauthCommandFile.interpreter, [file.path], environment: ["PATH": "/usr/bin:/bin"])
        XCTAssertEqual(result.status, 0, result.err)
        XCTAssertEqual(try String(contentsOf: spaced.appendingPathComponent("home.txt"), encoding: .utf8), spaced.path)
    }
}
