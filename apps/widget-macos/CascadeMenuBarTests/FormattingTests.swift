import XCTest
@testable import CascadeMenuBar

final class FormattingTests: XCTestCase {
    func testUnknownNeverBecomesZero() {
        XCTAssertEqual(Formatting.percent(nil), "—")
        XCTAssertEqual(Formatting.count(nil), "—")
        XCTAssertEqual(Formatting.countdown(nil), "—")
        XCTAssertEqual(Formatting.text(nil), "—")
        XCTAssertEqual(Formatting.percent(0), "0%")
        XCTAssertEqual(Formatting.countdown(8_040_000_000_000), "2h14m")
    }

    func testUpdatedAgoUsesInjectedClock() throws {
        let snap = try WidgetModelsTests.snapshot()
        XCTAssertEqual(Formatting.updatedAgo(snap.generatedAt, now: { snap.generatedAt.addingTimeInterval(120) }), "2m ago")
    }
}
