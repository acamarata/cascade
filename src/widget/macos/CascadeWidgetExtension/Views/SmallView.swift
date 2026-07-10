import SwiftUI

struct SmallView: View {
    let entry: CascadeEntry

    var body: some View {
        let w = entry.cache._widget
        let proj = w?.active_project ?? "no data"
        let active = w?.totals?.active
        let blocked = w?.totals?.blocked
        let inbox = w?.inbox_unread

        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Circle()
                    .fill(widgetStatusColor(for: entry.cache))
                    .frame(width: 6, height: 6)
                Text("Cascade")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundColor(.secondary)
                Spacer()
                Text(entry.cache.ageString)
                    .font(.system(size: 9))
                    .foregroundColor(.secondary)
            }

            Text(proj)
                .font(.system(size: 13, weight: .bold))
                .lineLimit(1)

            Spacer()

            HStack(spacing: 8) {
                VStack(spacing: 2) {
                    Text(active.map { "\($0)" } ?? "—")
                        .font(.system(size: 22, weight: .bold, design: .rounded))
                        .foregroundColor((active ?? 0) > 0 ? .orange : .secondary)
                    Text("Active")
                        .font(.system(size: 9))
                        .foregroundColor(.secondary)
                }
                Spacer()
                if (blocked ?? 0) > 0 || !entry.cache.hasWidgetData {
                    VStack(spacing: 2) {
                        Text(blocked.map { "\($0)" } ?? "—")
                            .font(.system(size: 18, weight: .semibold, design: .rounded))
                            .foregroundColor((blocked ?? 0) > 0 ? .red : .secondary)
                        Text("Blocked")
                            .font(.system(size: 9))
                            .foregroundColor(.secondary)
                    }
                }
                if (inbox ?? 0) > 0 || !entry.cache.hasWidgetData {
                    VStack(spacing: 2) {
                        Text(inbox.map { "\($0)" } ?? "—")
                            .font(.system(size: 18, weight: .semibold, design: .rounded))
                            .foregroundColor((inbox ?? 0) > 0 ? .red : .secondary)
                        Text("Inbox")
                            .font(.system(size: 9))
                            .foregroundColor(.secondary)
                    }
                }
            }
        }
        .padding(12)
        .containerBackground(for: .widget) {
            Color(red: 0.07, green: 0.08, blue: 0.12)
        }
    }
}
