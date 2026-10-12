import SwiftUI
import WidgetKit

// Purpose: the static widget configuration: kind, provider and the three supported families.
// Inputs: none. Outputs: a WidgetKit configuration over the App Group snapshot file.
struct CascadeWidget: Widget {
    static let kind = "CascadeWidget"
    static let families: [WidgetFamily] = [.systemSmall, .systemMedium, .systemLarge]

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: Self.kind, provider: CascadeProvider()) { entry in
            CascadeEntryView(entry: entry)
        }
        .configurationDisplayName("Cascade")
        .description("Fleet and project status from the Cascade menu bar app.")
        .supportedFamilies(Self.families)
    }
}

// Purpose: the extension entry point.
@main
struct CascadeWidgetBundle: WidgetBundle {
    var body: some Widget { CascadeWidget() }
}
