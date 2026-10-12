import XCTest
@testable import CascadeMenuBar

final class DaemonPollFallbackTests: XCTestCase {
    func testSilencePollsAndRecentEventSuppressesPoll() {
        var now = Date(timeIntervalSince1970: 100)
        var calls = 0
        let poll = DaemonPollFallback(interval: 1, now: { now }, request: { calls += 1 })
        poll.tick()
        XCTAssertEqual(calls, 0)
        now = now.addingTimeInterval(1)
        poll.tick()
        XCTAssertEqual(calls, 1)
        poll.receivedEvent()
        poll.tick()
        XCTAssertEqual(calls, 1)
        now = now.addingTimeInterval(1)
        poll.tick()
        XCTAssertEqual(calls, 2)
        XCTAssertEqual(DaemonPollFallback.productionInterval, 30)
    }
}
