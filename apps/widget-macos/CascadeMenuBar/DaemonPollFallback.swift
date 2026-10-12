import Foundation

// Purpose: refresh after silence on the event stream. Owned and used on the main queue.
// Inputs: injected interval/clock/request. Outputs: RPC triggers. Constraints: 30 s in production.
final class DaemonPollFallback {
    static let productionInterval: TimeInterval = 30
    private let interval: TimeInterval
    private let now: () -> Date
    private let request: () -> Void
    private var lastActivity: Date
    private var timer: Timer?

    init(interval: TimeInterval = productionInterval, now: @escaping () -> Date = Date.init,
         request: @escaping () -> Void) {
        self.interval = interval
        self.now = now
        self.request = request
        lastActivity = now()
    }

    func receivedEvent() { lastActivity = now() }

    func tick() {
        guard now().timeIntervalSince(lastActivity) >= interval else { return }
        lastActivity = now()
        request()
    }

    func start() {
        stop()
        timer = Timer.scheduledTimer(withTimeInterval: min(interval, 1), repeats: true) { [weak self] _ in self?.tick() }
    }

    func stop() { timer?.invalidate(); timer = nil }
    deinit { timer?.invalidate() }
}
