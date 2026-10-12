import XCTest
import AppKit
@testable import CascadeMenuBar

final class FleetRowViewTests: XCTestCase {
    func testReauthAmberAndOnlyReauthHasTapTarget() throws {
        let snapshot = try WidgetModelsTests.snapshot()
        for row in snapshot.rows {
            let view = FleetRowView(row: row, now: { row.updatedAt.addingTimeInterval(120) })
            XCTAssertEqual(view.labels.first?.stringValue, row.label)
            XCTAssertTrue(view.labels.contains { $0.stringValue == "2m ago" })
            XCTAssertEqual(view.reauthButton != nil, row.reauthRequired)
            if row.reauthRequired {
                XCTAssertEqual(view.labels.first?.textColor, NSColor.systemOrange)
                XCTAssertEqual(view.reauthButton?.isEnabled, true)
            }
        }
        var row = snapshot.rows[2]
        for state in ["available", "unknown", "exhausted", "constrained"] {
            row.state = state
            XCTAssertNil(FleetRowView(row: row).reauthButton)
        }
    }

    func testNoOpaqueStaleGlyph() throws {
        for row in try WidgetModelsTests.snapshot().rows {
            XCTAssertFalse(FleetRowView(row: row).labels.contains { $0.stringValue.contains("*") })
        }
    }

    func testTapCallsOnReauthWithTheRowAndLaunchesNothingItself() throws {
        let snapshot = try WidgetModelsTests.snapshot()
        let amber = try XCTUnwrap(snapshot.rows.first { $0.reauthRequired })
        let view = FleetRowView(row: amber)
        var tapped: [String] = []
        view.onReauth = { tapped.append($0.ref) }
        view.reauthButton?.performClick(nil)
        XCTAssertEqual(tapped, [amber.ref])
        var quiet = snapshot.rows[2]
        quiet.state = "auth-required"
        XCTAssertNil(FleetRowView(row: quiet).reauthButton, "only reauth_required rows have a tap target")
    }
}
