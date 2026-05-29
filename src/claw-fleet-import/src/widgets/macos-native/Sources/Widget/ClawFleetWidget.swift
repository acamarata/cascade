import WidgetKit
import SwiftUI

// MARK: - Widget bundle

/// Widget bundle declares both size configurations.
///
/// NOTE: WidgetKit is NOT real-time. The system throttles refresh to roughly
/// every 15-60 minutes based on battery state, usage patterns, and the
/// timeline policy returned by getTimeline(). The menu bar app (ClawFleetMenuBar)
/// is the real-time surface — it polls every 60 seconds. The widget provides a
/// glanceable overview from the last cached snapshot, not live data.
@main
struct ClawFleetWidgetBundle: WidgetBundle {
    var body: some Widget {
        ClawFleetWidget()
    }
}

// MARK: - Widget

struct ClawFleetWidget: Widget {
    let kind = "ClawFleetWidget"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: CacheTimelineProvider()) { entry in
            ClawFleetWidgetView(entry: entry)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Claw Fleet")
        .description("AI account usage at a glance.")
        .supportedFamilies([.systemSmall, .systemMedium])
    }
}
