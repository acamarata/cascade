import WidgetKit
import Foundation

// MARK: - Timeline entry

struct CacheEntry: TimelineEntry {
    /// When this snapshot was taken — used by WidgetKit for scheduling.
    let date: Date
    let cache: UsageCache?
    let loadError: String?
}

// MARK: - Timeline provider

/// Provides timeline entries for the WidgetKit extension.
///
/// The refresh policy uses `.atEnd` with entries spaced 15 minutes apart.
/// WidgetKit will honour this as a hint but may space refreshes further apart
/// depending on device state. Do not rely on this for real-time accuracy.
struct CacheTimelineProvider: TimelineProvider {
    typealias Entry = CacheEntry

    func placeholder(in context: Context) -> CacheEntry {
        CacheEntry(date: Date(), cache: nil, loadError: nil)
    }

    func getSnapshot(in context: Context, completion: @escaping (CacheEntry) -> Void) {
        completion(currentEntry())
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<CacheEntry>) -> Void) {
        let now = Date()
        // Build 4 entries spanning the next hour, each 15 minutes apart.
        // WidgetKit re-calls getTimeline() at the date of the last entry (.atEnd).
        let entries = (0..<4).map { i in
            CacheEntry(
                date: now.addingTimeInterval(Double(i) * 15 * 60),
                cache: currentCache(),
                loadError: currentLoadError()
            )
        }
        let refreshAfter = now.addingTimeInterval(60 * 60)
        completion(Timeline(entries: entries, policy: .after(refreshAfter)))
    }

    // MARK: - Private

    private func currentCache() -> UsageCache? {
        if case .success(let c) = loadUsageCache() { return c }
        return nil
    }

    private func currentLoadError() -> String? {
        if case .failure(let err) = loadUsageCache() {
            switch err {
            case .notFound: return "Cache not found"
            case .decodeFailure: return "Parse error"
            }
        }
        return nil
    }

    private func currentEntry() -> CacheEntry {
        CacheEntry(
            date: Date(),
            cache: currentCache(),
            loadError: currentLoadError()
        )
    }
}
