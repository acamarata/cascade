import XCTest

final class WidgetContentTests: XCTestCase {
    func testRowAndProjectContentKeepFileValuesAndNeverZeroFillNulls() throws {
        let snapshot = try WidgetSnapshot.decode(WidgetProviderTests.fixture("snapshot_seven_projects"))
        let unknown = RowContent(snapshot.rows[3])
        XCTAssertEqual([unknown.label, unknown.fiveHour, unknown.sevenDay], ["acct-d-auth", "—", "—"])
        let project = ProjectContent(try XCTUnwrap(snapshot.projects?[2]))
        XCTAssertEqual([project.label, project.taskSummary], ["project-c", "—"])
        XCTAssertFalse(WidgetContent(snapshot).largeTexts.contains("0%"))
    }

    func testLiteralSetIsClosed() {
        XCTAssertEqual(WidgetLiteral.all, ["—", "Cascade", "Attention", "5h", "7d", "Projects"])
        XCTAssertEqual(WidgetContent.maxProjects, 5)
        XCTAssertEqual(WidgetContent.maxRows, 4)
    }
}
