import Foundation

// Purpose: turn one confirmed tap on an amber row into a Terminal window running
// `cascade provider reauth <ref>`, and nothing else.
// Inputs: a ref, the live snapshot, the widget's environment, the bundled script and an injected process runner.
// Outputs: a launched Terminal, or one LaunchError. Nothing is written or launched on any refusal before the
// command file step; a failed launch removes the file it wrote.
// Constraints: argv only. The ref must pass the charset guard and match a live reauth_required row, the CLI is
// the installer-recorded CASCADE_CLI_PATH (never a PATH lookup), and the ref is never resolved to a provider
// name here: the CLI does that.

/// Every way a launch can be refused. Descriptions are fixed one-line text and never echo the ref.
enum LaunchError: Error, Equatable, LocalizedError {
    case unknownProvider
    case invalidProviderRef
    case commandFileUnwritable
    case cliUnavailable
    case invalidBundlePath
    case terminalUnavailable

    var errorDescription: String? {
        switch self {
        case .unknownProvider: return "Sign-in refused: that profile no longer needs sign-in."
        case .invalidProviderRef: return "Sign-in refused: the profile reference is not valid."
        case .commandFileUnwritable: return "Sign-in failed: the command file could not be created."
        case .cliUnavailable: return "Sign-in failed: the installed cascade command is unavailable. Run cascade widget install."
        case .invalidBundlePath: return "Sign-in failed: the widget's sign-in script is missing or has an unsafe path."
        case .terminalUnavailable: return "Sign-in failed: Terminal could not be opened."
        }
    }
}

/// Runs one process from an argv whose first element is the executable. Throws when it cannot run or fails.
typealias ReauthProcessRunner = ([String]) throws -> Void

/// The seam the presenter calls; tests substitute a recorder.
protocol ReauthLaunching {
    func launch(ref: String, snapshot: WidgetSnapshot?) throws
}

final class ReauthLauncher: ReauthLaunching {
    static let openTool = "/usr/bin/open"
    static let scriptName = "cascade-reauth"

    private let environment: [String: String]
    private let scriptPath: () -> String?
    private let directory: URL
    private let runner: ReauthProcessRunner
    private let isExecutable: (String) -> Bool

    init(environment: [String: String] = ProcessInfo.processInfo.environment,
         scriptPath: @escaping () -> String? = { Bundle.main.url(forResource: ReauthLauncher.scriptName, withExtension: "sh")?.path },
         directory: URL = ReauthLauncher.cacheDirectory(), runner: @escaping ReauthProcessRunner = ReauthLauncher.runProcess,
         isExecutable: @escaping (String) -> Bool = ReauthLauncher.isExecutableFile) {
        self.environment = environment
        self.scriptPath = scriptPath
        self.directory = directory
        self.runner = runner
        self.isExecutable = isExecutable
    }

    /// Validates, writes the command file and asks Terminal to open it. The order is the fail-closed order.
    func launch(ref: String, snapshot: WidgetSnapshot?) throws {
        guard ReauthCommandFile.isValidRef(ref) else { throw LaunchError.invalidProviderRef }
        guard snapshot?.rows.contains(where: { $0.ref == ref && $0.reauthRequired }) == true else {
            throw LaunchError.unknownProvider
        }
        let cli = try installedCLI()
        let home = try recordedHome()
        guard let script = scriptPath(), ReauthCommandFile.isQuotableAbsolutePath(script), isExecutable(script) else {
            throw LaunchError.invalidBundlePath
        }
        let file = try ReauthCommandFile.create(in: directory, script: script, cli: cli, home: home, ref: ref)
        do { try runner([Self.openTool, "-a", "Terminal", file.path]) }
        catch {
            try? FileManager.default.removeItem(at: file)
            throw LaunchError.terminalUnavailable
        }
    }

    /// The installer-recorded CLI: set, absolute, quote-free and executable, else cliUnavailable.
    private func installedCLI() throws -> String {
        guard let cli = environment["CASCADE_CLI_PATH"], ReauthCommandFile.isQuotableAbsolutePath(cli), isExecutable(cli) else {
            throw LaunchError.cliUnavailable
        }
        return cli
    }

    /// The recorded CASCADE_HOME: nil when unset or empty (the installer omits it), else absolute and quote-free.
    private func recordedHome() throws -> String? {
        guard let home = environment["CASCADE_HOME"], !home.isEmpty else { return nil }
        guard ReauthCommandFile.isQuotableAbsolutePath(home) else { throw LaunchError.cliUnavailable }
        return home
    }

    /// ~/Library/Caches/cascade, the per-user cache directory (created 0700 on first use).
    static func cacheDirectory() -> URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Caches/cascade", isDirectory: true)
    }

    static func isExecutableFile(_ path: String) -> Bool {
        var isDirectory: ObjCBool = false
        return FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory) && !isDirectory.boolValue
            && FileManager.default.isExecutableFile(atPath: path)
    }

    /// Foundation.Process with the argv as given; a non-zero exit or a failure to start throws.
    static func runProcess(_ argv: [String]) throws {
        guard let tool = argv.first else { throw LaunchError.terminalUnavailable }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: tool)
        process.arguments = Array(argv.dropFirst())
        try process.run()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { throw LaunchError.terminalUnavailable }
    }
}
