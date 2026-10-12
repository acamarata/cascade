import AppKit

// Purpose: refuse to run from a Trash or from outside /Applications and ~/Applications.
// Inputs: injected bundle URL, home URL, file check, launcher, alert and terminator seams.
// Outputs: a relaunch of the installed copy, else one alert and termination. Fires once.
// Constraints: compares path components, never string prefixes (/ApplicationsX is not /Applications).
final class BundlePathGuard {
    static let bundleName = "CascadeMenuBar.app"
    static let refusalMessage = "Cascade must run from /Applications or ~/Applications. Move CascadeMenuBar.app there and reopen it."

    struct Seams {
        var bundleURL: () -> URL
        var home: URL
        var exists: (URL) -> Bool
        var launch: (URL, @escaping (Bool) -> Void) -> Void
        var alert: (String) -> Void
        var terminate: () -> Void
    }

    private let seams: Seams
    private(set) var tripped = false

    init(seams: Seams) { self.seams = seams }

    /// The fixed relaunch candidates, in preference order. Nothing else is ever launched.
    var candidates: [URL] {
        [URL(fileURLWithPath: "/Applications", isDirectory: true),
         seams.home.appendingPathComponent("Applications", isDirectory: true)]
            .map { $0.appendingPathComponent(Self.bundleName, isDirectory: true) }
    }

    /// The bundle URL as it is now (a moved bundle reports its new location).
    var currentBundleURL: URL { seams.bundleURL() }

    /// Checks the current bundle location. Returns true when the app may keep running.
    @discardableResult
    func check() -> Bool {
        if tripped { return false }
        if isAllowed(currentBundleURL) { return true }
        tripped = true
        guard let installed = candidates.first(where: { isAllowed($0) && seams.exists($0) }) else {
            refuse()
            return false
        }
        seams.launch(installed) { [weak self] launched in
            if launched { self?.seams.terminate() } else { self?.refuse() }
        }
        return false
    }

    /// True only for a bundle inside /Applications or ~/Applications and never inside a Trash.
    func isAllowed(_ url: URL) -> Bool {
        let parts = Self.components(url)
        if parts.contains(where: { $0 == ".Trash" || $0 == ".Trashes" }) { return false }
        let roots = [URL(fileURLWithPath: "/Applications", isDirectory: true),
                     seams.home.appendingPathComponent("Applications", isDirectory: true)]
        return roots.contains { root in
            let base = Self.components(root)
            return parts.count > base.count && Array(parts.prefix(base.count)) == base
        }
    }

    private func refuse() {
        seams.alert(Self.refusalMessage)
        seams.terminate()
    }

    private static func components(_ url: URL) -> [String] {
        url.resolvingSymlinksInPath().standardizedFileURL.pathComponents
    }
}

extension BundlePathGuard {
    /// Production seams: the bundle is tracked through a file reference so a move between polls is seen.
    /// The home is the process HOME when absolute (a LaunchAgent or QA run may set it), else the account home.
    static func production(environment: [String: String] = ProcessInfo.processInfo.environment) -> BundlePathGuard {
        let initial = Bundle.main.bundleURL
        let reference = (initial as NSURL).fileReferenceURL()
        let home = environment["HOME"].flatMap { $0.hasPrefix("/") ? URL(fileURLWithPath: $0, isDirectory: true) : nil }
        let seams = Seams(
            bundleURL: { (reference as NSURL?)?.filePathURL ?? initial },
            home: home ?? FileManager.default.homeDirectoryForCurrentUser,
            exists: { isInstalledCopy($0, bundleIdentifier: Bundle.main.bundleIdentifier) },
            launch: { url, done in
                let running = NSWorkspace.shared.runningApplications.map(\.bundleURL)
                if isRunning(url, bundleURLs: running) { done(true); return }
                NSWorkspace.shared.openApplication(at: url, configuration: openConfiguration(environment: environment)) { app, error in
                    DispatchQueue.main.async { done(app != nil && error == nil) }
                }
            },
            alert: { message in
                let alert = NSAlert()
                alert.messageText = message
                alert.runModal()
            },
            terminate: { NSApp.terminate(nil) })
        return BundlePathGuard(seams: seams)
    }

    /// A new instance that inherits this process's environment (socket, self-report path, HOME):
    /// LaunchServices would otherwise start the installed copy with the session's default environment.
    static func openConfiguration(environment: [String: String]) -> NSWorkspace.OpenConfiguration {
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.createsNewApplicationInstance = true
        configuration.environment = environment
        return configuration
    }

    /// True when the candidate is already running, so the refused copy only quits instead of starting a duplicate.
    static func isRunning(_ url: URL, bundleURLs: [URL?]) -> Bool {
        let target = url.resolvingSymlinksInPath().standardizedFileURL.path
        return bundleURLs.contains { $0?.resolvingSymlinksInPath().standardizedFileURL.path == target }
    }

    /// True for a bundle on disk that carries this app's bundle identifier; any other bundle is never launched.
    static func isInstalledCopy(_ url: URL, bundleIdentifier: String?) -> Bool {
        let plist = url.appendingPathComponent("Contents/Info.plist")
        guard let identifier = bundleIdentifier, let data = try? Data(contentsOf: plist),
              let info = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any] else {
            return false
        }
        return info["CFBundleIdentifier"] as? String == identifier
    }
}
