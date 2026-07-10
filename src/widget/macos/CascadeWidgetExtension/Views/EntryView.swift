import SwiftUI
import WidgetKit

struct CascadeEntryView: View {
    let entry: CascadeEntry
    @Environment(\.widgetFamily) var family

    var body: some View {
        Group {
            switch family {
            case .systemSmall:  SmallView(entry: entry)
            case .systemMedium: MediumView(entry: entry)
            case .systemLarge:  LargeView(entry: entry)
            default:            MediumView(entry: entry)
            }
        }
        // Dim and desaturate when the macOS desktop is inactive (widgetRenderingMode
        // == .accented). Applied at the root so all sub-views inherit the effect.
        .monochromeDimWhenInactive()
    }
}

func widgetStatusColor(for cache: CascadeCache) -> Color {
    guard cache.hasWidgetData else { return .gray }
    return cache.isStale ? .red : .green
}
