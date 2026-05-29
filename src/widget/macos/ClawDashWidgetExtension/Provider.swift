import WidgetKit

struct ClawDashProvider: TimelineProvider {
    func placeholder(in context: Context) -> ClawDashEntry {
        ClawDashEntry(date: Date(), cache: .placeholder)
    }

    func getSnapshot(in context: Context, completion: @escaping (ClawDashEntry) -> Void) {
        let cache = ClawCache.load() ?? .placeholder
        completion(ClawDashEntry(date: Date(), cache: cache))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<ClawDashEntry>) -> Void) {
        let cache = ClawCache.load() ?? .placeholder
        let entry = ClawDashEntry(date: Date(), cache: cache)
        // Refresh every 5 minutes
        let nextUpdate = Calendar.current.date(byAdding: .minute, value: 5, to: Date())!
        let timeline = Timeline(entries: [entry], policy: .after(nextUpdate))
        completion(timeline)
    }
}
