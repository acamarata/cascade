import WidgetKit
import XCTest

final class CascadeWidgetTests: XCTestCase {
    func testConfigurationOffersExactlySmallMediumAndLarge() {
        XCTAssertEqual(CascadeWidget.families, [.systemSmall, .systemMedium, .systemLarge])
        XCTAssertEqual(CascadeWidget.kind, "CascadeWidget")
        XCTAssertEqual(CascadeProvider.refreshInterval, 30)
    }
}
