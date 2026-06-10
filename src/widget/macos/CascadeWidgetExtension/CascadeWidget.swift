import WidgetKit
import SwiftUI

struct CascadeWidget: Widget {
    let kind: String = "CascadeWidget"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: CascadeWidgetProvider()) { entry in
            CascadeEntryView(entry: entry)
        }
        .configurationDisplayName("Cascade")
        .description("Cascade status — tasks, inbox, and tier stats.")
        .supportedFamilies([.systemSmall, .systemMedium, .systemLarge])
    }
}

#Preview(as: .systemMedium) {
    CascadeWidget()
} timeline: {
    CascadeEntry(date: .now, cache: .placeholder)
}
