import XCTest
@testable import CascadeMenuBar

final class ReauthScriptTests: XCTestCase {
    private var directory: URL!
    private var cli: URL!
    private var marker: URL { directory.appendingPathComponent("argv.txt") }

    override func setUpWithError() throws {
        directory = try ReauthTestSupport.temporaryDirectory()
        cli = try ReauthTestSupport.recorder(in: directory)
    }

    override func tearDown() { try? FileManager.default.removeItem(at: directory) }

    private func script() throws -> URL {
        try XCTUnwrap(Bundle(for: ReauthLauncher.self).url(forResource: ReauthLauncher.scriptName, withExtension: "sh"))
    }

    private func run(_ arguments: [String]) throws -> (status: Int32, out: String, err: String) {
        try ReauthTestSupport.run(ReauthCommandFile.interpreter, [try script().path] + arguments, environment: ["PATH": "/usr/bin:/bin"])
    }

    func testBundledScriptIsExecutableAndStartsWithTheShebang() throws {
        let url = try script()
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: url.path))
        XCTAssertTrue(try String(contentsOf: url, encoding: .utf8).hasPrefix("#!" + ReauthCommandFile.interpreter + "\n"))
    }

    private func run(_ arguments: [String], environment: [String: String]) throws -> (status: Int32, out: String, err: String) {
        try ReauthTestSupport.run(ReauthCommandFile.interpreter, [try script().path] + arguments, environment: environment)
    }

    func testEveryBadRefExitsNonZeroAndNeverInvokesTheCLI() throws {
        let bad = ["a b", "a;b", "$(x)", "a\nb", "-x", "a'b", "", String(repeating: "a", count: 65), "x';touch /tmp/pwn;'", "a`b`", "a|b", "a/b",
                   "\u{e9}", "\u{df}", "\u{ff58}", "\u{2170}", "a\u{e9}b"]
        let environments = [["PATH": "/usr/bin:/bin"], ["PATH": "/usr/bin:/bin", "LANG": "en_US.UTF-8", "LC_CTYPE": "en_US.UTF-8"]]
        for environment in environments {
            for ref in bad {
                let result = try run([cli.path, ref], environment: environment)
                XCTAssertNotEqual(result.status, 0, "ref \(ref.debugDescription) must be refused under \(environment)")
                XCTAssertFalse(FileManager.default.fileExists(atPath: marker.path), "the CLI ran for \(ref.debugDescription)")
            }
        }
    }

    func testANonASCIICLIPathIsAcceptedAndAControlCharacterIsNot() throws {
        let accented = directory.appendingPathComponent("D\u{e9}veloppement", isDirectory: true)
        try FileManager.default.createDirectory(at: accented, withIntermediateDirectories: true)
        let accentedCLI = try ReauthTestSupport.recorder(in: accented)
        let result = try run([accentedCLI.path, "acct-a"])
        XCTAssertEqual(result.status, 0, result.err)
        XCTAssertEqual(try String(contentsOf: accented.appendingPathComponent("argv.txt"), encoding: .utf8), "provider\nreauth\nacct-a\n")
        XCTAssertFalse(FileManager.default.fileExists(atPath: marker.path))
    }

    /// Executable files whose names hold a control character exist, so only the script's control-character guard
    /// (not its is-a-file check) can refuse them.
    func testAnExistingExecutableWithAControlCharacterInItsPathIsRefused() throws {
        for (index, control) in ["\u{1}", "\u{7f}", "\n"].enumerated() {
            let folder = directory.appendingPathComponent("ctl\(index)\(control)x", isDirectory: true)
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            let hostile = try ReauthTestSupport.recorder(in: folder)
            XCTAssertTrue(FileManager.default.isExecutableFile(atPath: hostile.path))
            XCTAssertNotEqual(try run([hostile.path, "acct-a"]).status, 0, hostile.path.debugDescription)
            XCTAssertFalse(FileManager.default.fileExists(atPath: folder.appendingPathComponent("argv.txt").path))
        }
    }

    /// A relative path with a slash resolves from the working directory, exists and is executable (exec would run it),
    /// so only the script's absolute-path check can refuse it.
    func testARelativeCLIPathThatExistsIsStillRefused() throws {
        let result = try ReauthTestSupport.run(ReauthCommandFile.interpreter, [try script().path, "./" + cli.lastPathComponent, "acct-a"],
                                               environment: ["PATH": "/usr/bin:/bin"], currentDirectory: directory)
        XCTAssertNotEqual(result.status, 0)
        XCTAssertFalse(FileManager.default.fileExists(atPath: marker.path))
    }

    func testWrongArityAndRelativeOrUnusableCLIExitNonZeroWithoutInvokingTheCLI() throws {
        let plain = directory.appendingPathComponent("plain")
        try Data("x".utf8).write(to: plain)
        let calls: [[String]] = [[], [cli.path], [cli.path, "r1", "extra"], ["bin/cascade", "r1"], [directory.path, "r1"],
                                 [plain.path, "r1"], [directory.appendingPathComponent("absent").path, "r1"]]
        for arguments in calls {
            let result = try run(arguments)
            XCTAssertNotEqual(result.status, 0, "arguments \(arguments)")
            XCTAssertFalse(FileManager.default.fileExists(atPath: marker.path), "the CLI ran for \(arguments)")
        }
    }

    func testGoodRefRunsTheCLIWithProviderReauthAndTheRef() throws {
        for ref in ["acct-a", "a.b_c-1", String(repeating: "z", count: 64), "ref-b4c9a289323b"] {
            let result = try run([cli.path, ref])
            XCTAssertEqual(result.status, 0, result.err)
            XCTAssertEqual(try String(contentsOf: marker, encoding: .utf8), "provider\nreauth\n\(ref)\n")
            try FileManager.default.removeItem(at: marker)
        }
    }

    func testTheCLIExitStatusIsTheScriptsExitStatus() throws {
        let failing = directory.appendingPathComponent("failing")
        try Data("#!\(ReauthCommandFile.interpreter)\nexit 7\n".utf8).write(to: failing)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: failing.path)
        XCTAssertEqual(try run([failing.path, "r1"]).status, 7)
    }
}
