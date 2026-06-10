import SwiftUI
import WidgetKit

// MARK: - MenubarPopoverView
//
// Purpose: SwiftUI view displayed inside the NSPopover attached to the CascadeApp
//          status-bar icon. Shows daemon health, project count, task totals, and
//          quick-action buttons.
// Inputs:  CascadeCache — loaded fresh by MenubarController.updateIcon() on each
//          30-second tick and on popover open.
// Outputs: Read-only status display + two action buttons (Open Cascade, Refresh Now).
// Constraints: Fixed frame 280×180 to fit inside the NSPopover without scroll.
//              WidgetCenter.shared is available in the container app process; it is
//              NOT accessible from the WidgetExtension process.
// SPORT: .claude/docs/MASTER-APPS.md — MenubarPopoverView component row (P2, T-P2-E04-09)

struct MenubarPopoverView: View {
    let cache: CascadeCache

    // Computed convenience accessors to avoid force-unwrapping in the view body.
    private var projectCount: Int { cache._widget?.projects?.count ?? 0 }
    private var totals: Totals? { cache._widget?.totals }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            // Header row: title + cache age
            HStack {
                Text("Cascade")
                    .font(.headline)
                Spacer()
                Text(cache.ageString)
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            // Status row: health indicator circle + daemon state text
            HStack(spacing: 6) {
                Circle()
                    .fill(statusColor)
                    .frame(width: 8, height: 8)
                Text(statusText)
                    .font(.subheadline)
                    .foregroundColor(.primary)
            }

            // Project count
            Text("\(projectCount) projects active")
                .font(.caption)
                .foregroundColor(.secondary)

            // Task totals row
            if let t = totals {
                Text("\(t.active) active · \(t.blocked) blocked · \(cache._widget?.inbox_unread ?? 0) inbox")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }

            Divider()

            // Quick-action buttons
            HStack(spacing: 8) {
                Button("Open Cascade") {
                    // cascade://open — handled by URL scheme in P3 Tauri GUI.
                    // Falls back gracefully (no-op) if the scheme is unregistered in P2.
                    if let url = URL(string: "cascade://open") {
                        NSWorkspace.shared.open(url)
                    }
                }
                .buttonStyle(.bordered)
                .font(.caption)

                Button("Refresh Now") {
                    // Instruct WidgetKit to reload all timeline providers immediately.
                    WidgetCenter.shared.reloadAllTimelines()
                }
                .buttonStyle(.borderedProminent)
                .font(.caption)
            }
        }
        .padding(12)
        .frame(width: 280, height: 180)
    }

    // MARK: - Private Helpers

    private var statusColor: Color {
        guard CascadeCache.load() != nil else { return .red }
        return cache.isStale ? .orange : .green
    }

    private var statusText: String {
        guard CascadeCache.load() != nil else { return "No cache — run cascaded" }
        return cache.isStale ? "Stale — restart cascaded" : "Daemon active"
    }
}
