import WidgetKit

struct CascadeWidgetProvider: TimelineProvider {
    func placeholder(in context: Context) -> CascadeEntry {
        CascadeEntry(date: Date(), cache: .placeholder)
    }

    func getSnapshot(in context: Context, completion: @escaping (CascadeEntry) -> Void) {
        let cache = CascadeCache.load() ?? .placeholder
        completion(CascadeEntry(date: Date(), cache: cache))
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<CascadeEntry>) -> Void) {
        let cache = CascadeCache.load() ?? .placeholder
        let entry = CascadeEntry(date: Date(), cache: cache)
        // Request 30-second refresh. WidgetKit may coalesce; 30s is the requested
        // minimum, not guaranteed — on battery macOS typically budgets 40-70 reloads/day.
        // The companion menubar app (T-P2-E04-10) will also call
        // WidgetCenter.shared.reloadTimelines(ofKind: "CascadeWidget") whenever
        // cascaded writes a fresh cache.json, providing event-driven updates.
        let nextUpdate = Calendar.current.date(byAdding: .second, value: 30, to: Date())!
        let timeline = Timeline(entries: [entry], policy: .after(nextUpdate))
        completion(timeline)
    }
}
