import XCTest
import AppKit
@testable import CascadeMenuBar

final class DesktopFleetWindowTests: XCTestCase {
    override func setUp() { _ = NSApplication.shared }

    func testDesktopPanelLevelBelowNormalAboveDesktop() {
        let panel = DesktopFleetWindow()
        defer { panel.close() }
        let normal = Int(CGWindowLevelForKey(.normalWindow))
        let desktop = Int(CGWindowLevelForKey(.desktopWindow))
        XCTAssertTrue(panel.styleMask.contains(.nonactivatingPanel))
        XCTAssertEqual(panel.level.rawValue, normal - 1)
        XCTAssertLessThan(panel.level.rawValue, normal, "a normal-level panel covers work windows")
        XCTAssertGreaterThan(panel.level.rawValue, desktop, "a desktop-level panel loses clicks to reveal-desktop")
        XCTAssertTrue(panel.collectionBehavior.contains(.stationary))
        XCTAssertTrue(panel.collectionBehavior.contains(.canJoinAllSpaces))
        XCTAssertTrue(panel.isMovableByWindowBackground)
        XCTAssertFalse(panel.hidesOnDeactivate)
        XCTAssertTrue(panel.headerView.mouseDownCanMoveWindow)
    }

    func testHostingViewAcceptsFirstMouse() {
        let panel = DesktopFleetWindow()
        defer { panel.close() }
        XCTAssertTrue(panel.hosting.acceptsFirstMouse(for: nil))
        XCTAssertTrue(panel.hosting.subviews.first === panel.table)
    }

    func testPanelHostsThePopoverTableOneRowPerFixtureRow() throws {
        let panel = DesktopFleetWindow()
        defer { panel.close() }
        let snapshot = try WidgetModelsTests.snapshot()
        XCTAssertFalse(snapshot.rows.isEmpty)
        panel.render(snapshot: snapshot, error: nil, now: { snapshot.generatedAt })
        let table = try XCTUnwrap(panel.table)
        XCTAssertEqual(table.rows.count, snapshot.rows.count)
        XCTAssertEqual(table.rows.map { $0.labels[0].stringValue }, snapshot.rows.map(\.label))
        XCTAssertTrue(panel.hosting.subviews.first === table)
        XCTAssertTrue(panel.headerView.label.stringValue.contains("unsigned fallback"))
        panel.render(snapshot: snapshot, error: .unavailable)
        XCTAssertEqual(panel.table?.rows.count, 0)
        XCTAssertEqual(panel.hosting.subviews.count, 1, "re-render replaces the table")
    }

    func testLevelChangeIsReportedOnlyWhenItChanges() {
        let panel = DesktopFleetWindow()
        defer { panel.close() }
        var reported: [Int] = []
        panel.onLevelChange = { reported.append($0) }
        panel.level = DesktopFleetWindow.belowNormalLevel
        panel.level = NSWindow.Level(rawValue: DesktopFleetWindow.belowNormalLevel.rawValue - 1)
        XCTAssertEqual(reported, [DesktopFleetWindow.belowNormalLevel.rawValue - 1])
    }

    func testToggleShowsAndHides() {
        let panel = DesktopFleetWindow()
        defer { panel.close() }
        XCTAssertFalse(panel.isVisible)
        panel.toggle()
        XCTAssertTrue(panel.isVisible)
        panel.toggle()
        XCTAssertFalse(panel.isVisible)
    }
}
