import XCTest
@testable import CascadeMenuBar

final class ResultSlicerTests: XCTestCase {
    func testResultBytesInAnyKeyOrderWithQuotedBraces() throws {
        let result = #"{ "rows": [], "nested": {"text": "a } { \" quote"} }"#
        for envelope in ["{\"result\":\(result),\"id\":1}", "{\"id\":1,\"other\":{\"result\":0},\"result\":\(result)}"] {
            XCTAssertEqual(try ResultSlicer.result(in: Data(envelope.utf8)), Data(result.utf8))
        }
        XCTAssertThrowsError(try ResultSlicer.result(in: Data(#"{"error":{}}"#.utf8)))
    }
}
