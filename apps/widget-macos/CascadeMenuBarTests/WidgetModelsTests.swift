import XCTest
import CryptoKit
@testable import CascadeMenuBar

final class WidgetModelsTests: XCTestCase {
    static func fixture(_ name: String = "fixture_status_widget_rpc", ext: String = "json") throws -> Data {
        let bundle = Bundle(for: WidgetModelsTests.self)
        return try Data(contentsOf: XCTUnwrap(bundle.url(forResource: name, withExtension: ext, subdirectory: "testdata")))
    }

    static func snapshot() throws -> WidgetSnapshot {
        try WidgetSnapshot.decode(ResultSlicer.result(in: fixture()))
    }

    func testFixtureEveryFieldAndProvenance() throws {
        let bytes = try Self.fixture()
        let source = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appendingPathComponent("../../../internal/daemon/testdata/fixture_status_widget_rpc.json")
        if FileManager.default.fileExists(atPath: source.path) {
            XCTAssertEqual(SHA256.hash(data: bytes), SHA256.hash(data: try Data(contentsOf: source)))
        }
        let snap = try Self.snapshot()
        XCTAssertEqual(snap.rows.count, 5)
        XCTAssertEqual(snap.attentionCount, 1)
        XCTAssertEqual(snap.activeJobsCount, 2)
        XCTAssertEqual(snap.seq, 0)
        XCTAssertEqual(snap.generatedAt, ISO8601DateFormatter().date(from: "2026-10-05T12:00:00Z"))
        XCTAssertEqual(snap.projects?.count, 0)
        XCTAssertEqual(snap.nodes.count, 1)
        XCTAssertEqual(snap.nodes[0].id, "node-0001")
        XCTAssertEqual(snap.nodes[0].presence, "reachable")
        XCTAssertTrue(snap.nodes[0].reachable)
        XCTAssertEqual(snap.nodes[0].trustTier, "worker-trusted")
        let raw = try XCTUnwrap(JSONSerialization.jsonObject(with: ResultSlicer.result(in: bytes)) as? [String: Any])
        let rows = try XCTUnwrap(raw["rows"] as? [[String: Any]])
        for (row, expected) in zip(snap.rows, rows) {
            XCTAssertEqual(row.ref, expected["ref"] as? String)
            XCTAssertEqual(row.label, expected["label"] as? String)
            XCTAssertEqual(row.kind, "profile")
            XCTAssertEqual(row.state, expected["state"] as? String)
            XCTAssertEqual(row.reauthRequired, expected["reauth_required"] as? Bool)
            XCTAssertEqual(row.updatedAt, snap.generatedAt)
            XCTAssertNil(row.fiveHour?.utilizationPct)
            XCTAssertNil(row.sevenDay?.utilizationPct)
            XCTAssertNil(row.sevenDay?.resetsIn)
        }
        XCTAssertEqual(snap.rows[1].fiveHour?.resetsIn, 7_200_000_000_000)
        XCTAssertNil(snap.rows[0].fiveHour?.resetsIn)
    }

    func testZeroIsNotNilAndMissingRowsIsTypedError() throws {
        let bytes = try ResultSlicer.result(in: Self.fixture())
        let zero = String(decoding: bytes, as: UTF8.self).replacingOccurrences(of: "\"utilization_pct\": null", with: "\"utilization_pct\": 0")
        XCTAssertEqual(try WidgetSnapshot.decode(Data(zero.utf8)).rows[0].fiveHour?.utilizationPct, 0)
        XCTAssertThrowsError(try WidgetSnapshot.decode(Data("{}".utf8))) { error in
            guard case DecodingError.keyNotFound(let key, _) = error else { return XCTFail("Expected typed missing-key error: \(error)") }
            XCTAssertEqual(key.stringValue, "rows")
        }
    }
}
