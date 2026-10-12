import XCTest
@testable import CascadeMenuBar

final class WidgetSelfReportTests: XCTestCase {
    private var root: URL!
    private var clock = Date(timeIntervalSince1970: 1_800_000_000)

    override func setUpWithError() throws {
        root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: root.appendingPathComponent("home"), withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws { try? FileManager.default.removeItem(at: root) }

    private var home: URL { root.appendingPathComponent("home") }
    private var target: URL { root.appendingPathComponent("data/widget/widget-status.json") }

    private func makeReport(_ environment: [String: String]) -> WidgetSelfReport {
        WidgetSelfReport(environment: environment, now: { [unowned self] in self.clock })
    }

    private func read() throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: target)) as? [String: Any])
    }

    private func homeEntries() throws -> [String] {
        let walker = FileManager.default.enumerator(atPath: home.path)
        return (walker?.allObjects as? [String]) ?? []
    }

    func testWidgetStatusSelfReportWritten() throws {
        let report = makeReport(["HOME": home.path, WidgetSelfReport.pathKey: target.path])
        XCTAssertNil(report.statusLine)
        report.launched(bundlePath: "/Applications/CascadeMenuBar.app", windowLevel: 7)
        var body = try read()
        XCTAssertEqual(Set(body.keys), ["bundle_path", "window_level", "written_at", "first_rows_at"])
        XCTAssertEqual(body["bundle_path"] as? String, "/Applications/CascadeMenuBar.app")
        XCTAssertEqual(body["window_level"] as? Int, 7)
        XCTAssertEqual(body["written_at"] as? String, "2027-01-15T08:00:00Z")
        XCTAssertTrue(body["first_rows_at"] is NSNull)
        let attributes = try FileManager.default.attributesOfItem(atPath: target.path)
        XCTAssertEqual((attributes[.posixPermissions] as? NSNumber)?.intValue, 0o600)
        let parent = try FileManager.default.attributesOfItem(atPath: target.deletingLastPathComponent().path)
        XCTAssertEqual((parent[.posixPermissions] as? NSNumber)?.intValue, 0o700)
        XCTAssertEqual(try homeEntries(), [], "no home-relative write")
        XCTAssertNil(report.lastError)

        clock.addTimeInterval(5)
        report.answered(rowCount: 0)
        XCTAssertTrue(try read()["first_rows_at"] is NSNull, "an empty answer leaves first_rows_at null")
        report.answered(rowCount: 3)
        body = try read()
        XCTAssertEqual(body["first_rows_at"] as? String, "2027-01-15T08:00:05Z")
        clock.addTimeInterval(60)
        report.answered(rowCount: 4)
        report.levelChanged(6)
        body = try read()
        XCTAssertEqual(body["first_rows_at"] as? String, "2027-01-15T08:00:05Z", "first_rows_at never moves")
        XCTAssertEqual(body["window_level"] as? Int, 6)
        XCTAssertEqual(body["written_at"] as? String, "2027-01-15T08:01:05Z")
        let leftovers = try FileManager.default.contentsOfDirectory(atPath: target.deletingLastPathComponent().path)
        XCTAssertEqual(leftovers, ["widget-status.json"], "atomic replace leaves no temporary file")
    }

    func testLaterAnswersDoNotRewriteTheFile() throws {
        let report = makeReport([WidgetSelfReport.pathKey: target.path])
        report.launched(bundlePath: "/Applications/CascadeMenuBar.app", windowLevel: 7)
        report.answered(rowCount: 1)
        let first = try Data(contentsOf: target)
        clock.addTimeInterval(30)
        report.answered(rowCount: 2)
        report.levelChanged(7)
        XCTAssertEqual(try Data(contentsOf: target), first)
    }

    func testUnsetOrRelativePathWritesNothingAndNamesInstall() throws {
        for environment in [["HOME": home.path], ["HOME": home.path, WidgetSelfReport.pathKey: ""],
                            ["HOME": home.path, WidgetSelfReport.pathKey: "widget/widget-status.json"]] {
            let report = makeReport(environment)
            report.launched(bundlePath: "/Applications/CascadeMenuBar.app", windowLevel: 7)
            report.answered(rowCount: 1)
            XCTAssertNil(report.file)
            XCTAssertEqual(report.statusLine, WidgetSelfReport.unsetMessage)
            XCTAssertTrue(WidgetSelfReport.unsetMessage.contains("cascade widget install"))
            XCTAssertFalse(FileManager.default.fileExists(atPath: target.path))
            XCTAssertEqual(try homeEntries(), [])
        }
    }

    func testExistingParentIsTightenedTo0700() throws {
        let parent = target.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: parent, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o755])
        makeReport([WidgetSelfReport.pathKey: target.path])
            .launched(bundlePath: "/Applications/CascadeMenuBar.app", windowLevel: 7)
        let attributes = try FileManager.default.attributesOfItem(atPath: parent.path)
        XCTAssertEqual((attributes[.posixPermissions] as? NSNumber)?.intValue, 0o700)
        XCTAssertTrue(FileManager.default.fileExists(atPath: target.path))
    }

    func testNothingIsWrittenBeforeLaunch() {
        let report = makeReport([WidgetSelfReport.pathKey: target.path])
        report.answered(rowCount: 2)
        report.levelChanged(3)
        XCTAssertNil(report.firstRowsAt)
        XCTAssertFalse(FileManager.default.fileExists(atPath: target.path))
    }
}
