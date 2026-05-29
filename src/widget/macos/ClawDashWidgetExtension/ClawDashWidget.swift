import WidgetKit
import SwiftUI

struct ClawDashWidget: Widget {
    let kind: String = "ClawDashWidget"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: ClawDashProvider()) { entry in
            ClawDashEntryView(entry: entry)
        }
        .configurationDisplayName("Claw Dash")
        .description("Claude Code project overview — tasks, inbox, and GCI stats.")
        .supportedFamilies([.systemSmall, .systemMedium, .systemLarge])
    }
}

#Preview(as: .systemMedium) {
    ClawDashWidget()
} timeline: {
    ClawDashEntry(date: .now, cache: .placeholder)
}
