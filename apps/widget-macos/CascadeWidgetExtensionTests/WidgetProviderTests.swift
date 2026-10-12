import WidgetKit
import XCTest

final class WidgetProviderTests: XCTestCase {
    static func fixture(_ name: String) throws -> Data {
        let url = try XCTUnwrap(Bundle(for: WidgetProviderTests.self)
            .url(forResource: name, withExtension: "json", subdirectory: "testdata"))
        return try Data(contentsOf: url)
    }

    private var directory: URL!
    private var file: URL { directory.appendingPathComponent(SnapshotFeed.fileName) }
    private let now = Date(timeIntervalSince1970: 1_791_000_000)

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    private func provider(_ url: URL?) -> CascadeProvider {
        CascadeProvider(file: { url }, now: { self.now })
    }

    func testEveryTimelineRefreshesThirtySecondsAfterItsEntry() throws {
        let missing = provider(file).timeline()
        try Self.fixture("snapshot_seven_projects").write(to: file)
        let valid = provider(file).timeline()
        try Data("{".utf8).write(to: file)
        let corrupt = provider(file).timeline()
        for timeline in [missing, valid, corrupt, provider(nil).timeline()] {
            XCTAssertEqual(timeline.entries.count, 1)
            XCTAssertEqual(timeline.entries.first?.date, now)
            XCTAssertEqual(timeline.policy, .after(now.addingTimeInterval(30)))
        }
        XCTAssertNotNil(valid.entries.first?.snapshot)
        XCTAssertNil(corrupt.entries.first?.snapshot)
    }

    func testProviderReadsOnlyTheInjectedFile() throws {
        let other = directory.appendingPathComponent("other.json")
        try Self.fixture("snapshot_seven_projects").write(to: file)
        try Self.fixture("snapshot_no_projects").write(to: other)
        var calls = 0
        let counted = CascadeProvider(file: { calls += 1; return other }, now: { self.now })
        let entry = counted.entry()
        XCTAssertEqual(calls, 1)
        XCTAssertEqual(entry.snapshot?.projects?.count, 0)
        XCTAssertEqual(provider(file).entry().snapshot?.projects?.first?.label, "project-a")
        XCTAssertNil(provider(nil).entry().snapshot, "No injected file must mean no data")
        XCTAssertNil(SnapshotFeed.applicationFile(), "Test bundle has no App Group key; resolution must fail closed")
    }

    func testMissingEmptyCorruptOrOversizedFileNeverShowsAnEarlierRead() throws {
        let reader = provider(file)
        try Self.fixture("snapshot_seven_projects").write(to: file)
        XCTAssertEqual(reader.entry().snapshot?.rows.count, 5)
        // A complete, valid document padded past the cap: only the size guard can reject it.
        let valid = try Self.fixture("snapshot_seven_projects")
        let oversized = valid + Data(repeating: 0x20, count: SnapshotFeed.maxBytes + 1 - valid.count)
        XCTAssertNoThrow(try WidgetSnapshot.decode(oversized), "Padding must keep the document decodable")
        let failures: [(String, () throws -> Void)] = [
            ("corrupt", { try Data("{\"rows\": [".utf8).write(to: self.file) }),
            ("empty", { try Data().write(to: self.file) }),
            ("missing", { try FileManager.default.removeItem(at: self.file) }),
            ("oversized", { try oversized.write(to: self.file) }),
        ]
        for (name, damage) in failures {
            try Self.fixture("snapshot_seven_projects").write(to: file)
            XCTAssertNotNil(reader.entry().snapshot, name)
            try damage()
            let entry = reader.entry()
            XCTAssertNil(entry.snapshot, "\(name) file produced data")
            let content = WidgetContent(entry.snapshot)
            XCTAssertEqual(content.rows, [], name)
            XCTAssertEqual(content.projects, [], name)
            XCTAssertEqual(content.project, "—", name)
            XCTAssertEqual(content.taskSummary, "—", name)
            XCTAssertEqual(content.attention, "—", name)
        }
    }
}
