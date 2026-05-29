import SwiftUI
import Combine

// MARK: - CacheStore

/// Observable store that owns the poll loop and decoded cache state.
///
/// The menu bar app is the real-time surface. It polls every 60 seconds,
/// which keeps the display current without hammering the file system.
/// The 60-second interval is a deliberate design choice: the underlying
/// claw-fleet-refresh daemon refreshes the file every 5 minutes, so
/// polling faster than 60s provides no new data and just wastes CPU.
@MainActor
final class CacheStore: ObservableObject {
    @Published private(set) var cache: UsageCache?
    @Published private(set) var loadError: String?
    @Published private(set) var lastLoadedAt: Date?

    private var timer: Timer?

    init() {
        refresh()
        scheduleTimer()
    }

    deinit {
        timer?.invalidate()
    }

    func refresh() {
        switch loadUsageCache() {
        case .success(let c):
            cache = c
            loadError = nil
            lastLoadedAt = Date()
        case .failure(let err):
            switch err {
            case .notFound:
                loadError = "Cache file not found. Is claw-fleet-refresh running?"
            case .decodeFailure(let underlying):
                // Preserve whatever partial data we had rather than blanking
                // the display on a transient write-in-progress glitch.
                loadError = "JSON parse error: \(underlying.localizedDescription)"
            }
            lastLoadedAt = Date()
        }
    }

    // MARK: - Private

    private func scheduleTimer() {
        timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.refresh()
            }
        }
    }
}

// MARK: - Summary for the menu bar title

extension CacheStore {
    /// A compact string for the MenuBarExtra label: shows how many accounts
    /// have headroom remaining out of the total. Examples: "4/9" (4 free),
    /// "9/9" (all free), "0/9" (all capped). Gemini opaque rows are counted
    /// in total but excluded from the headroom tally (no data to judge by).
    var summaryTitle: String {
        guard let accounts = cache?.accounts, !accounts.isEmpty else {
            return "CF"
        }
        let total = accounts.count
        let available = accounts.filter { entry in
            guard entry.quota_opaque != true else { return false }
            let weekPct = entry.usage?.seven_day?.utilization ?? 0
            let sessPct = entry.usage?.five_hour?.utilization ?? 0
            // An account is "available" when neither slot is fully capped.
            return weekPct < 100 && sessPct < 100
        }.count
        return "\(available)/\(total)"
    }
}
