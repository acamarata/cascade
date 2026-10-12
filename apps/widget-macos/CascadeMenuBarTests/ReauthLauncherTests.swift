import XCTest
import CryptoKit
@testable import CascadeMenuBar

final class ReauthLauncherTests: XCTestCase {
    private var directory: URL!
    private var recorder: URL!
    private var script: URL!
    private var argvs: [[String]] = []

    override func setUpWithError() throws {
        directory = try ReauthTestSupport.temporaryDirectory()
        recorder = try ReauthTestSupport.recorder(in: directory)
        script = try ReauthTestSupport.recorder(in: directory, name: "cascade-reauth.sh")
        argvs = []
    }

    override func tearDown() { try? FileManager.default.removeItem(at: directory) }

    private var cache: URL { directory.appendingPathComponent("cache", isDirectory: true) }
    private var live: WidgetSnapshot {
        ReauthTestSupport.snapshot([ReauthTestSupport.row("acct-a", reauth: true), ReauthTestSupport.row("acct-b", reauth: false)])
    }

    private func launcher(_ extra: [String: String] = [:], clear: Set<String> = [], scriptPath: String?? = nil,
                          runner: ReauthProcessRunner? = nil) -> ReauthLauncher {
        var environment = ["CASCADE_CLI_PATH": recorder.path]
        for (key, value) in extra { environment[key] = value }
        clear.forEach { environment[$0] = nil }
        let path: String? = scriptPath ?? script.path
        return ReauthLauncher(environment: environment, scriptPath: { path }, directory: cache,
                              runner: runner ?? { self.argvs.append($0) })
    }

    private func assertRefused(_ expected: LaunchError, _ call: () throws -> Void, line: UInt = #line) {
        XCTAssertThrowsError(try call(), line: line) { XCTAssertEqual($0 as? LaunchError, expected, line: line) }
        XCTAssertEqual(argvs.count, 0, "nothing may launch on a refusal", line: line)
        XCTAssertFalse(FileManager.default.fileExists(atPath: cache.path), "nothing may be written on a refusal", line: line)
    }

    func testLaunchRecordsExactlyTheOpenArgvAndAFourLineFileWhenHomeIsSet() throws {
        try launcher(["CASCADE_HOME": "/Users/x/.cascade"]).launch(ref: "acct-a", snapshot: live)
        XCTAssertEqual(argvs.count, 1)
        let argv = try XCTUnwrap(argvs.first)
        XCTAssertEqual(Array(argv.prefix(3)), ["/usr/bin/open", "-a", "Terminal"])
        XCTAssertEqual(argv.count, 4)
        let text = try String(contentsOfFile: argv[3], encoding: .utf8)
        XCTAssertEqual(text.split(separator: "\n").count, 4)
        XCTAssertTrue(text.contains("export CASCADE_HOME='/Users/x/.cascade'"))
        XCTAssertTrue(text.hasSuffix("exec '\(script.path)' '\(recorder.path)' 'acct-a'\n"))
    }

    func testAbsentRefAndNotReauthRefBothThrowUnknownProvider() {
        assertRefused(.unknownProvider) { try launcher().launch(ref: "acct-zzz", snapshot: live) }
        assertRefused(.unknownProvider) { try launcher().launch(ref: "acct-b", snapshot: live) }
        assertRefused(.unknownProvider) { try launcher().launch(ref: "acct-a", snapshot: nil) }
        assertRefused(.unknownProvider) { try launcher().launch(ref: "acct-a", snapshot: ReauthTestSupport.snapshot([])) }
    }

    func testBadRefsThrowInvalidProviderRefEvenWhenTheSnapshotListsThem() {
        let bad = ["a b", "a;b", "$(x)", "a\nb", "-x", "a'b", "", String(repeating: "a", count: 65), "x';touch /tmp/pwn;'"]
        for ref in bad {
            let snapshot = ReauthTestSupport.snapshot([ReauthTestSupport.row(ref, reauth: true)])
            assertRefused(.invalidProviderRef) { try launcher().launch(ref: ref, snapshot: snapshot) }
        }
    }

    func testReauthLauncherRequiresInstalledCLIPath() throws {
        let plain = directory.appendingPathComponent("not-executable")
        try Data("x".utf8).write(to: plain)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: plain.path)
        let fakeCascade = directory.appendingPathComponent("bin", isDirectory: true)
        try FileManager.default.createDirectory(at: fakeCascade, withIntermediateDirectories: true)
        _ = try ReauthTestSupport.recorder(in: fakeCascade, name: "cascade")
        let cases: [(String, [String: String], Set<String>)] = [
            ("unset", ["PATH": fakeCascade.path], ["CASCADE_CLI_PATH"]),
            ("relative", ["CASCADE_CLI_PATH": "bin/cascade"], []),
            ("not executable", ["CASCADE_CLI_PATH": plain.path], []),
            ("a directory", ["CASCADE_CLI_PATH": directory.path], []),
            ("quote", ["CASCADE_CLI_PATH": "/usr/bin/it's"], []),
            ("empty", ["CASCADE_CLI_PATH": ""], []),
            ("relative home", ["CASCADE_HOME": "rel/home"], []),
            ("quote home", ["CASCADE_HOME": "/Users/x/it's"], [])
        ]
        for (name, extra, clear) in cases {
            assertRefused(.cliUnavailable, { try launcher(extra, clear: clear).launch(ref: "acct-a", snapshot: live) })
            XCTAssertEqual(argvs.count, 0, name)
        }
    }

    func testEmptyCascadeHomeIsTreatedAsUnset() throws {
        try launcher(["CASCADE_HOME": ""]).launch(ref: "acct-a", snapshot: live)
        let text = try String(contentsOfFile: try XCTUnwrap(argvs.first).last ?? "", encoding: .utf8)
        XCTAssertEqual(text.split(separator: "\n").count, 3)
    }

    func testBundleScriptProblemsThrowInvalidBundlePath() throws {
        let plain = directory.appendingPathComponent("plain.sh")
        try Data("x".utf8).write(to: plain)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: plain.path)
        for path in [nil, "relative.sh", "/Applications/It's.app/x.sh", directory.appendingPathComponent("absent.sh").path, plain.path] {
            assertRefused(.invalidBundlePath) { try launcher(scriptPath: .some(path)).launch(ref: "acct-a", snapshot: live) }
        }
    }

    func testFailingRunnerThrowsTerminalUnavailableAndRemovesTheFile() throws {
        struct Boom: Error {}
        var written: String?
        let failing = launcher(runner: { argv in written = argv.last; throw Boom() })
        XCTAssertThrowsError(try failing.launch(ref: "acct-a", snapshot: live)) {
            XCTAssertEqual($0 as? LaunchError, .terminalUnavailable)
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: try XCTUnwrap(written)))
    }

    func testUnwritableCacheDirectoryThrowsCommandFileUnwritable() throws {
        let blocker = directory.appendingPathComponent("blocker")
        try Data("x".utf8).write(to: blocker)
        let blocked = ReauthLauncher(environment: ["CASCADE_CLI_PATH": recorder.path], scriptPath: { self.script.path },
                                     directory: blocker.appendingPathComponent("cache"), runner: { self.argvs.append($0) })
        XCTAssertThrowsError(try blocked.launch(ref: "acct-a", snapshot: live)) {
            XCTAssertEqual($0 as? LaunchError, .commandFileUnwritable)
        }
        XCTAssertEqual(argvs.count, 0)
    }

    func testEveryLaunchErrorIsADistinctOneLineMessage() {
        let all: [LaunchError] = [.unknownProvider, .invalidProviderRef, .commandFileUnwritable, .cliUnavailable,
                                  .invalidBundlePath, .terminalUnavailable]
        let texts = all.compactMap(\.errorDescription)
        XCTAssertEqual(Set(texts).count, all.count)
        for text in texts {
            XCTAssertFalse(text.isEmpty)
            XCTAssertFalse(text.contains("\n"))
        }
    }
}

/// The redacted-ref round trip against the real cascade binary: the widget hands the CLI an opaque ref and the
/// CLI resolves it to the email-named provider (P1-WID-08). Needs the scheme pre-action binaries.
final class ReauthRedactedRefRoundTripTests: XCTestCase {
    private func executable(_ key: String) throws -> URL {
        let path = try XCTUnwrap(ProcessInfo.processInfo.environment[key], "Required scheme pre-action binary: \(key)")
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: path), "Not executable: \(key)")
        return URL(fileURLWithPath: path)
    }

    private func requireIsolatedKeychain(_ environment: [String: String]) throws {
        let result = try ReauthTestSupport.run("/usr/bin/security", ["default-keychain", "-d", "user"], environment: environment)
        guard result.status != 0, result.out.isEmpty, result.err.contains("default keychain could not be found") else {
            XCTFail("Fresh HOME did not isolate the login keychain; refusing to run the real binary")
            throw LaunchError.cliUnavailable
        }
    }

    func testEmailNamedProviderRefResolvesInTheRealBinary() throws {
        let binary = try executable("CASCADE_TEST_BINARY")
        let seeder = try executable("CASCADE_TEST_SEEDER")
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("rr-" + String(UUID().uuidString.prefix(8)))
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: home) }
        let root = home.appendingPathComponent("cascade")
        let environment = ["HOME": home.path, "USERPROFILE": home.path, "CASCADE_HOME": root.path, "PATH": "/usr/bin:/bin"]
        try requireIsolatedKeychain(environment)
        let name = "user" + "@example.com"
        let seeded = try ReauthTestSupport.run(seeder.path, ["provider", "--data-dir", root.appendingPathComponent("data").path, "--name", name],
                                               environment: environment)
        XCTAssertEqual(seeded.status, 0, seeded.err)
        let digest = SHA256.hash(data: Data(name.utf8)).map { ($0 < 16 ? "0" : "") + String($0, radix: 16) }.joined()
        let ref = "ref-" + digest.prefix(12)
        let fixtureRow = try WidgetModelsTests.snapshot().rows[4]
        XCTAssertEqual(ref, fixtureRow.ref, "the fixture row of the email-named provider carries this ref")
        XCTAssertEqual(fixtureRow.label, "redacted")
        XCTAssertEqual(ReauthConfirmationView(label: fixtureRow.label, ref: ref, onConfirm: {}, onCancel: {}).commandLabel.stringValue,
                       "cascade provider reauth " + ref)
        let cache = home.appendingPathComponent("cache", isDirectory: true)
        let bundled = try XCTUnwrap(Bundle(for: ReauthLauncher.self).url(forResource: ReauthLauncher.scriptName, withExtension: "sh"))
        var argvs: [[String]] = []
        let launcher = ReauthLauncher(environment: ["CASCADE_CLI_PATH": binary.path, "CASCADE_HOME": root.path],
                                      scriptPath: { bundled.path }, directory: cache, runner: { argvs.append($0) })
        try launcher.launch(ref: ref, snapshot: ReauthTestSupport.snapshot([ReauthTestSupport.row(ref, reauth: true, label: "redacted")]))
        let file = try XCTUnwrap(argvs.first?.last)
        let result = try ReauthTestSupport.run(ReauthCommandFile.interpreter, [file], environment: environment)
        XCTAssertNotEqual(result.status, 0)
        XCTAssertFalse(result.err.contains("not-found"), "the ref must resolve: " + result.err)
        XCTAssertTrue(result.err.contains("invalid-input"), result.err)
        XCTAssertTrue(result.err.contains("provider \"" + name + "\""), "the remedy names the resolved provider: " + result.err)
        XCTAssertFalse(FileManager.default.fileExists(atPath: file))
    }
}
