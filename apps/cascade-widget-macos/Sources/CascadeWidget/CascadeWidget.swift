// CascadeWidget.swift — main WidgetKit entry point
// Purpose: Declares the widget bundle, timeline provider, and timeline entry.
//          Supports small (158×158), medium (338×158), and large (338×354) system families.
// Inputs: CascadeStatus.load() from shared App Group container
// Outputs: SwiftUI views refreshed on a 30-second timeline
// Constraints: WidgetKit on macOS requires Xcode 15+, macOS 13+.
//              Timeline refresh rate is at Apple's discretion — budget 30 s minimum.
// SPORT: apps/cascade-widget-macos — CascadeWidget entry

import WidgetKit
import SwiftUI
import AppIntents

// MARK: - Timeline entry

/// Single point on the WidgetKit timeline carrying the daemon status snapshot.
public struct CascadeEntry: TimelineEntry {
    /// The date WidgetKit should render this entry.
    public let date: Date
    /// Fleet status at this point in time.
    public let status: CascadeStatus
    /// Widget configuration (selected agent group + relay visibility).
    public let configuration: CascadeConfigurationIntent

    public init(
        date: Date,
        status: CascadeStatus,
        configuration: CascadeConfigurationIntent = CascadeConfigurationIntent()
    ) {
        self.date = date
        self.status = status
        self.configuration = configuration
    }

    /// Placeholder entry shown while WidgetKit loads real data.
    public static var placeholder: CascadeEntry {
        CascadeEntry(date: Date(), status: .placeholder)
    }
}

// MARK: - Timeline provider

/// Provides timeline entries to WidgetKit.
/// Reads daemon status from the shared App Group JSON file written by the Tauri main process.
public struct CascadeTimelineProvider: AppIntentTimelineProvider {
    public typealias Intent = CascadeConfigurationIntent
    public typealias Entry = CascadeEntry

    public func placeholder(in context: Context) -> CascadeEntry {
        .placeholder
    }

    /// Snapshot entry — rendered immediately when the widget gallery opens.
    public func snapshot(
        for configuration: CascadeConfigurationIntent,
        in context: Context
    ) async -> CascadeEntry {
        CascadeEntry(date: Date(), status: CascadeStatus.load(), configuration: configuration)
    }

    /// Full timeline — one entry per 30 seconds for the next 5 minutes.
    /// WidgetKit will call this again at the end of the timeline.
    public func timeline(
        for configuration: CascadeConfigurationIntent,
        in context: Context
    ) async -> Timeline<CascadeEntry> {
        let now = Date()
        let status = CascadeStatus.load()
        let interval: TimeInterval = 30

        // Build 10 entries × 30-second spacing (5-minute window).
        let entries: [CascadeEntry] = (0..<10).map { i in
            CascadeEntry(
                date: now.addingTimeInterval(Double(i) * interval),
                status: status,
                configuration: configuration
            )
        }

        // Reload after the last entry expires.
        let reloadDate = now.addingTimeInterval(Double(entries.count) * interval)
        return Timeline(entries: entries, policy: .after(reloadDate))
    }
}

// MARK: - Widget definition

/// The Cascade fleet-status widget.
/// Supports small, medium, and large system families on macOS.
struct CascadeWidgetView: View {
    let entry: CascadeEntry
    @Environment(\.widgetFamily) private var family

    var body: some View {
        switch family {
        case .systemSmall:
            SmallWidgetView(entry: entry)
        case .systemMedium:
            MediumWidgetView(entry: entry)
        case .systemLarge:
            LargeWidgetView(entry: entry)
        default:
            SmallWidgetView(entry: entry)
        }
    }
}

struct CascadeWidget: Widget {
    let kind: String = "CascadeWidget"

    var body: some WidgetConfiguration {
        AppIntentConfiguration(
            kind: kind,
            intent: CascadeConfigurationIntent.self,
            provider: CascadeTimelineProvider()
        ) { entry in
            CascadeWidgetView(entry: entry)
                .containerBackground(Color(.windowBackgroundColor), for: .widget)
        }
        .configurationDisplayName("Cascade Fleet")
        .description("Monitor AI agent utilisation and relay status.")
        .supportedFamilies([.systemSmall, .systemMedium, .systemLarge])
    }
}

// MARK: - Widget bundle

/// Entry point for the Cascade WidgetKit extension bundle.
@main
struct CascadeWidgetBundle: WidgetBundle {
    var body: some Widget {
        CascadeWidget()
    }
}

// MARK: - Previews

#Preview(as: .systemSmall) {
    CascadeWidget()
} timeline: {
    CascadeEntry.placeholder
}

#Preview(as: .systemMedium) {
    CascadeWidget()
} timeline: {
    CascadeEntry.placeholder
}

#Preview(as: .systemLarge) {
    CascadeWidget()
} timeline: {
    CascadeEntry.placeholder
}
