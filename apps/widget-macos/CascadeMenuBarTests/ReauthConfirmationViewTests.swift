import XCTest
import AppKit
@testable import CascadeMenuBar

final class ReauthConfirmationViewTests: XCTestCase {
    private struct Recorder: ReauthLaunching {
        let log: Log
        final class Log { var calls: [(String, Bool)] = [] }
        func launch(ref: String, snapshot: WidgetSnapshot?) throws { log.calls.append((ref, snapshot != nil)) }
    }

    private func fixture() throws -> (snapshot: WidgetSnapshot, presenter: ReauthPresenter, log: Recorder.Log) {
        let log = Recorder.Log()
        return (try WidgetModelsTests.snapshot(), ReauthPresenter(launcher: Recorder(log: log)), log)
    }

    private func texts(_ view: NSView) -> [String] {
        view.subviews.flatMap { sub -> [String] in
            let own = (sub as? NSTextField).map { [$0.stringValue] } ?? []
            return own + texts(sub)
        }
    }

    func testViewShowsTheVerbatimCommandAndTheLabel() {
        let view = ReauthConfirmationView(label: "acct-a-pool", ref: "acct-a-pool", onConfirm: {}, onCancel: {})
        XCTAssertEqual(view.commandLabel.stringValue, "cascade provider reauth acct-a-pool")
        XCTAssertTrue(view.titleLabel.stringValue.contains("acct-a-pool"))
        XCTAssertTrue(texts(view).contains("cascade provider reauth acct-a-pool"))
    }

    func testTappingAnAmberRowPresentsTheConfirmationAndLaunchesNothing() throws {
        let (snapshot, presenter, log) = try fixture()
        let popover = FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter)
        XCTAssertNil(popover.confirmation)
        let amber = try XCTUnwrap(popover.rows.first { $0.reauthButton != nil })
        amber.reauthButton?.performClick(nil)
        let confirmation = try XCTUnwrap(popover.confirmation)
        XCTAssertEqual(confirmation.commandLabel.stringValue, "cascade provider reauth acct-a-pool")
        XCTAssertTrue(confirmation.titleLabel.stringValue.contains("acct-a-pool"))
        XCTAssertEqual(log.calls.count, 0, "a tap alone must not launch")
    }

    func testConfirmCallsTheLauncherOnceWithTheLiveSnapshot() throws {
        let (snapshot, presenter, log) = try fixture()
        let popover = FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter)
        popover.rows.first { $0.reauthButton != nil }?.reauthButton?.performClick(nil)
        let confirmation = try XCTUnwrap(popover.confirmation)
        confirmation.confirmButton.performClick(nil)
        confirmation.confirmButton.performClick(nil)
        XCTAssertEqual(log.calls.count, 1)
        XCTAssertEqual(log.calls.first?.0, "acct-a-pool")
        XCTAssertEqual(log.calls.first?.1, true)
        XCTAssertNil(popover.confirmation)
        XCTAssertNil(popover.alertLabel)
    }

    func testCancelLaunchesNothingAndClearsTheRequest() throws {
        let (snapshot, presenter, log) = try fixture()
        let popover = FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter)
        popover.rows.first { $0.reauthButton != nil }?.reauthButton?.performClick(nil)
        try XCTUnwrap(popover.confirmation).cancelButton.performClick(nil)
        XCTAssertEqual(log.calls.count, 0)
        XCTAssertNil(popover.confirmation)
        presenter.confirm(expected: PendingReauth(ref: "acct-a-pool", label: "acct-a-pool"))
        XCTAssertEqual(log.calls.count, 0, "nothing is pending after a cancel")
        XCTAssertNil(FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter).confirmation)
    }

    func testThePendingRequestSurvivesARebuildAndDiesWithItsAmberRow() throws {
        let (snapshot, presenter, log) = try fixture()
        FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter).rows.first { $0.reauthButton != nil }?.reauthButton?.performClick(nil)
        XCTAssertNotNil(FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter).confirmation)
        var cleared = snapshot
        cleared.rows = snapshot.rows.filter { !$0.reauthRequired }
        XCTAssertNil(FleetPopoverView(snapshot: cleared, error: nil, presenter: presenter).confirmation)
        presenter.confirm(expected: PendingReauth(ref: "acct-a-pool", label: "acct-a-pool"))
        XCTAssertEqual(log.calls.count, 0, "a row that stopped needing sign-in must not launch")
        FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter).rows.first { $0.reauthButton != nil }?.reauthButton?.performClick(nil)
        XCTAssertNil(FleetPopoverView(snapshot: nil, error: .unauthorized, presenter: presenter).confirmation)
        presenter.confirm(expected: PendingReauth(ref: "acct-a-pool", label: "acct-a-pool"))
        XCTAssertEqual(log.calls.count, 0, "a daemon error drops the request")
    }

    func testAConfirmButtonLaunchesOnlyTheRequestItDisplays() throws {
        let log = Recorder.Log()
        let presenter = ReauthPresenter(launcher: Recorder(log: log))
        let snapshot = ReauthTestSupport.snapshot([ReauthTestSupport.row("acct-a", reauth: true),
                                                   ReauthTestSupport.row("acct-b", reauth: true)])
        let panel = FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter)
        let popover = FleetPopoverView(snapshot: snapshot, error: nil, presenter: presenter)
        panel.rows[0].reauthButton?.performClick(nil)
        let stale = try XCTUnwrap(panel.confirmation)
        XCTAssertEqual(stale.commandLabel.stringValue, "cascade provider reauth acct-a")
        popover.rows[1].reauthButton?.performClick(nil)
        stale.confirmButton.performClick(nil)
        XCTAssertEqual(log.calls.count, 0, "a stale view that showed acct-a must not launch acct-b")
        XCTAssertEqual(panel.confirmation?.commandLabel.stringValue, "cascade provider reauth acct-b", "the stale view catches up")
        try XCTUnwrap(panel.confirmation).confirmButton.performClick(nil)
        XCTAssertEqual(log.calls.map(\.0), ["acct-b"])
    }

    func testTheTitleDropsControlAndBidirectionalCharactersButTheCommandNeverUsesTheLabel() {
        let hostile = "Work\nFake line \u{202E}evil\u{2028}x"
        let view = ReauthConfirmationView(label: hostile, ref: "acct-a", onConfirm: {}, onCancel: {})
        XCTAssertEqual(view.titleLabel.stringValue, "Sign in to WorkFake line evilx?")
        XCTAssertEqual(view.titleLabel.maximumNumberOfLines, 1)
        XCTAssertEqual(view.commandLabel.stringValue, "cascade provider reauth acct-a")
        XCTAssertEqual(ReauthConfirmationView.displayLabel("Équipe 日本"), "Équipe 日本")
    }
}
