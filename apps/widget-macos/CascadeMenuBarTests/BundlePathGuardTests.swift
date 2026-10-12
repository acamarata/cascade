import XCTest
@testable import CascadeMenuBar

final class BundlePathGuardTests: XCTestCase {
    private let home = URL(fileURLWithPath: "/Users/widget-test-user", isDirectory: true)

    /// Records every seam call so a test can assert exactly what the guard did.
    final class Recorder {
        var bundle: URL
        var present: Set<String> = []
        var launchSucceeds = true
        var launched: [URL] = []
        var alerts: [String] = []
        var terminations = 0
        init(bundle: URL) { self.bundle = bundle }
    }

    private func makeGuard(_ recorder: Recorder) -> BundlePathGuard {
        BundlePathGuard(seams: .init(
            bundleURL: { recorder.bundle }, home: home,
            exists: { recorder.present.contains($0.standardizedFileURL.path) },
            launch: { url, done in recorder.launched.append(url); done(recorder.launchSucceeds) },
            alert: { recorder.alerts.append($0) },
            terminate: { recorder.terminations += 1 }))
    }

    private var refused: [URL] {
        [home.appendingPathComponent(".Trash/CascadeMenuBar.app"),
         URL(fileURLWithPath: "/Volumes/X/.Trashes/501/CascadeMenuBar.app"),
         URL(fileURLWithPath: "/ApplicationsX/CascadeMenuBar.app"),
         URL(fileURLWithPath: "/tmp/CascadeMenuBar.app"),
         home.appendingPathComponent("Downloads/CascadeMenuBar.app"),
         URL(fileURLWithPath: "/Applications/.Trash/CascadeMenuBar.app"),
         URL(fileURLWithPath: "/Applications/../tmp/CascadeMenuBar.app"),
         URL(fileURLWithPath: "/Applications")]
    }

    func testBundlePathGuardRefusesTrash() {
        for bundle in refused {
            let alone = Recorder(bundle: bundle)
            XCTAssertFalse(makeGuard(alone).check(), bundle.path)
            XCTAssertEqual(alone.alerts, [BundlePathGuard.refusalMessage], bundle.path)
            XCTAssertEqual(alone.terminations, 1, bundle.path)
            XCTAssertTrue(alone.launched.isEmpty, bundle.path)

            let installed = Recorder(bundle: bundle)
            installed.present = ["/Applications/CascadeMenuBar.app"]
            XCTAssertFalse(makeGuard(installed).check(), bundle.path)
            XCTAssertEqual(installed.launched.map(\.path), ["/Applications/CascadeMenuBar.app"], bundle.path)
            XCTAssertTrue(installed.alerts.isEmpty, bundle.path)
            XCTAssertEqual(installed.terminations, 1, bundle.path)
        }
        let passing = [URL(fileURLWithPath: "/Applications/CascadeMenuBar.app"),
                       home.appendingPathComponent("Applications/CascadeMenuBar.app")]
        for bundle in passing {
            let recorder = Recorder(bundle: bundle)
            XCTAssertTrue(makeGuard(recorder).check(), bundle.path)
            XCTAssertTrue(recorder.alerts.isEmpty && recorder.launched.isEmpty && recorder.terminations == 0, bundle.path)
        }
    }

    func testRelaunchPrefersSystemThenUserCandidateOnly() {
        let recorder = Recorder(bundle: home.appendingPathComponent(".Trash/CascadeMenuBar.app"))
        recorder.present = [home.appendingPathComponent("Applications/CascadeMenuBar.app").path]
        let pathGuard = makeGuard(recorder)
        XCTAssertEqual(pathGuard.candidates.map(\.path),
                       ["/Applications/CascadeMenuBar.app", home.appendingPathComponent("Applications/CascadeMenuBar.app").path])
        pathGuard.check()
        XCTAssertEqual(recorder.launched, [home.appendingPathComponent("Applications/CascadeMenuBar.app", isDirectory: true)])
    }

    func testFailedRelaunchFallsBackToOneAlert() {
        let recorder = Recorder(bundle: URL(fileURLWithPath: "/tmp/CascadeMenuBar.app"))
        recorder.present = ["/Applications/CascadeMenuBar.app"]
        recorder.launchSucceeds = false
        makeGuard(recorder).check()
        XCTAssertEqual(recorder.launched.count, 1)
        XCTAssertEqual(recorder.alerts, [BundlePathGuard.refusalMessage])
        XCTAssertEqual(recorder.terminations, 1)
    }

    func testRelaunchInheritsEnvironmentAsNewInstance() {
        let environment = ["CASCADE_WIDGET_STATUS_PATH": "/fake/data/widget/widget-status.json",
                           "CASCADE_SOCKET_PATH": "/fake/run/cascade.sock", "HOME": "/fake/home"]
        let configuration = BundlePathGuard.openConfiguration(environment: environment)
        XCTAssertEqual(configuration.environment, environment)
        XCTAssertTrue(configuration.createsNewApplicationInstance)
    }

    func testRunningCandidateIsNotStartedTwice() {
        let installed = URL(fileURLWithPath: "/Applications/CascadeMenuBar.app")
        XCTAssertTrue(BundlePathGuard.isRunning(installed, bundleURLs: [nil, URL(fileURLWithPath: "/Applications/CascadeMenuBar.app/")]))
        XCTAssertFalse(BundlePathGuard.isRunning(installed, bundleURLs: [nil, URL(fileURLWithPath: "/ApplicationsX/CascadeMenuBar.app")]))
        XCTAssertFalse(BundlePathGuard.isRunning(installed, bundleURLs: []))
    }

    func testInstalledCopyRequiresThisBundleIdentifier() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        func bundle(_ name: String, identifier: String?) throws -> URL {
            let url = root.appendingPathComponent(name)
            try FileManager.default.createDirectory(at: url.appendingPathComponent("Contents"), withIntermediateDirectories: true)
            if let identifier {
                let data = try PropertyListSerialization.data(fromPropertyList: ["CFBundleIdentifier": identifier], format: .xml, options: 0)
                try data.write(to: url.appendingPathComponent("Contents/Info.plist"))
            }
            return url
        }
        let id = "com.example.widget-test"
        XCTAssertTrue(BundlePathGuard.isInstalledCopy(try bundle("ours.app", identifier: id), bundleIdentifier: id))
        XCTAssertFalse(BundlePathGuard.isInstalledCopy(try bundle("other.app", identifier: "com.example.other"), bundleIdentifier: id))
        XCTAssertFalse(BundlePathGuard.isInstalledCopy(try bundle("empty.app", identifier: nil), bundleIdentifier: id))
        XCTAssertFalse(BundlePathGuard.isInstalledCopy(root.appendingPathComponent("absent.app"), bundleIdentifier: id))
        XCTAssertFalse(BundlePathGuard.isInstalledCopy(try bundle("ours.app", identifier: id), bundleIdentifier: nil))
    }

    func testMoveIntoTrashBetweenPollTicksTripsOnNextTickOnce() {
        let recorder = Recorder(bundle: URL(fileURLWithPath: "/Applications/CascadeMenuBar.app"))
        let pathGuard = makeGuard(recorder)
        XCTAssertTrue(pathGuard.check())
        XCTAssertTrue(pathGuard.check())
        recorder.bundle = home.appendingPathComponent(".Trash/CascadeMenuBar.app")
        XCTAssertFalse(pathGuard.check())
        XCTAssertTrue(pathGuard.tripped)
        XCTAssertEqual(recorder.alerts.count + recorder.launched.count, 1)
        XCTAssertFalse(pathGuard.check())
        XCTAssertEqual(recorder.terminations, 1, "a tripped guard never fires twice")
    }
}
