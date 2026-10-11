import XCTest
@testable import CascadeMenuBar

final class FleetPopoverViewTests: XCTestCase {
    func testPopulatedFleetShowsBothWindowsNodesJobsAndAttention() throws {
        let snapshot = try WidgetModelsTests.snapshot()
        let view = FleetPopoverView(snapshot: snapshot, error: nil, now: { snapshot.generatedAt })
        XCTAssertEqual(view.rows.count, snapshot.rows.count)
        XCTAssertTrue(view.labels.contains { $0.stringValue == "Attention 1" })
        XCTAssertTrue(view.labels.contains { $0.stringValue == "Active jobs 2" })
        for node in snapshot.nodes {
            XCTAssertTrue(view.labels.contains { $0.stringValue == "\(node.id): \(node.presence)" })
        }
        for (row, model) in zip(view.rows, snapshot.rows) {
            XCTAssertEqual(row.labels.map(\.stringValue).dropFirst().prefix(4), [
                Formatting.percent(model.fiveHour?.utilizationPct), Formatting.countdown(model.fiveHour?.resetsIn),
                Formatting.percent(model.sevenDay?.utilizationPct), Formatting.countdown(model.sevenDay?.resetsIn)
            ][...])
        }
        var null = snapshot
        null.activeJobsCount = nil
        XCTAssertTrue(FleetPopoverView(snapshot: null, error: nil).labels.contains { $0.stringValue == "Active jobs —" })
    }

    func testUnauthorizedAndUnconfiguredAreOneLine() throws {
        for error in [DaemonError.unauthorized, .socketUnconfigured] {
            let view = FleetPopoverView(snapshot: try WidgetModelsTests.snapshot(), error: error)
            XCTAssertEqual(view.labels.map(\.stringValue), [error.localizedDescription])
            XCTAssertTrue(view.rows.isEmpty)
        }
    }

    func testLabelsRenderVerbatim() throws {
        var snapshot = try WidgetModelsTests.snapshot()
        snapshot.rows[0].label = "Project 1"
        let view = FleetPopoverView(snapshot: snapshot, error: nil)
        XCTAssertEqual(view.rows[0].labels[0].stringValue, "Project 1")
        XCTAssertEqual(view.rows[4].labels[0].stringValue, "redacted")
    }
}
