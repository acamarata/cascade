import SwiftUI
import WidgetKit
import XCTest

final class WidgetViewsTests: XCTestCase {
    private func entry(_ name: String?) throws -> CascadeEntry {
        let snapshot = try name.map { try WidgetSnapshot.decode(WidgetProviderTests.fixture($0)) }
        return CascadeEntry(date: Date(timeIntervalSince1970: 0), snapshot: snapshot)
    }

    func testThreeFamiliesReturnThreeDistinctViewTypes() throws {
        let entry = try entry("snapshot_seven_projects")
        let small = WidgetLayouts.make(.systemSmall, entry: entry)
        let medium = WidgetLayouts.make(.systemMedium, entry: entry)
        let large = WidgetLayouts.make(.systemLarge, entry: entry)
        XCTAssertTrue(small is SmallWidgetView)
        XCTAssertTrue(medium is MediumWidgetView)
        XCTAssertTrue(large is LargeWidgetView)
        let types = Set([small, medium, large].map { ObjectIdentifier(type(of: $0)) })
        XCTAssertEqual(types.count, 3)
        XCTAssertTrue(WidgetLayouts.make(.systemExtraLarge, entry: entry) is SmallWidgetView)
    }

    func testSmallShowsFirstProjectAndTaskSummaryOrTheDash() throws {
        let content = WidgetContent(try entry("snapshot_seven_projects").snapshot)
        XCTAssertEqual(content.smallTexts, ["Cascade", "project-a", "2 open tasks in a"])
        XCTAssertEqual(WidgetContent(try entry("snapshot_no_projects").snapshot).smallTexts, ["Cascade", "—", "—"])
        XCTAssertEqual(WidgetContent(nil).smallTexts, ["Cascade", "—", "—"])
    }

    func testMediumAddsPerRowWindowStatsAndAttentionBadge() throws {
        let content = WidgetContent(try entry("snapshot_seven_projects").snapshot)
        XCTAssertEqual(Array(content.mediumTexts.prefix(5)), ["Cascade", "project-a", "2 open tasks in a", "Attention", "3"])
        XCTAssertEqual(content.rows.count, WidgetContent.maxRows)
        XCTAssertEqual(cells(content.rows[0]), ["acct-a-pool", "42%", "18%"])
        XCTAssertEqual(cells(content.rows[1]), ["redacted", "—", "—"])
        XCTAssertEqual(cells(content.rows[2]), ["acct-c-limit", "100%", "63%"])
        XCTAssertFalse(content.mediumTexts.contains("acct-e-extra"), "Medium shows at most four rows")
        let quiet = WidgetContent(try entry("snapshot_no_projects").snapshot)
        XCTAssertEqual(quiet.attention, "0")
    }

    func testLargeCapsTheProjectTableAtFiveRows() throws {
        let content = WidgetContent(try entry("snapshot_seven_projects").snapshot)
        XCTAssertEqual(content.projects.map(\.label), ["project-a", "project-b", "project-c", "project-d", "project-e"])
        XCTAssertEqual(content.projects[2].taskSummary, "—")
        XCTAssertFalse(content.largeTexts.contains("project-f"))
        XCTAssertFalse(content.largeTexts.contains("project-g"))
        XCTAssertTrue(content.largeTexts.starts(with: content.mediumTexts))
        XCTAssertFalse(content.mediumTexts.contains("project-b"), "Project table belongs to the large layout")
    }

    func testNoDataEntryRendersTheDashAndZeroRowsInEveryFamily() throws {
        let content = WidgetContent(try entry(nil).snapshot)
        XCTAssertEqual(content.rows, [])
        XCTAssertEqual(content.projects, [])
        XCTAssertEqual(content.largeTexts, ["Cascade", "—", "—", "Attention", "—", "Projects"])
    }
}

private func cells(_ row: RowContent) -> [String] { [row.label, row.fiveHour, row.sevenDay] }
