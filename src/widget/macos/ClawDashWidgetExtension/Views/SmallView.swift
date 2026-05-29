import SwiftUI

struct SmallView: View {
    let entry: ClawDashEntry

    var body: some View {
        let w = entry.cache._widget
        let proj = w?.active_project ?? "No project"
        let active = w?.totals?.active ?? 0
        let blocked = w?.totals?.blocked ?? 0
        let inbox = w?.totals?.inbox ?? 0

        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Circle()
                    .fill(entry.cache.isStale ? Color.red : Color.green)
                    .frame(width: 6, height: 6)
                Text("Claw Dash")
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
                    Text("\(active)")
                        .font(.system(size: 22, weight: .bold, design: .rounded))
                        .foregroundColor(active > 0 ? .orange : .secondary)
                    Text("Active")
                        .font(.system(size: 9))
                        .foregroundColor(.secondary)
                }
                Spacer()
                if blocked > 0 {
                    VStack(spacing: 2) {
                        Text("\(blocked)")
                            .font(.system(size: 18, weight: .semibold, design: .rounded))
                            .foregroundColor(.red)
                        Text("Blocked")
                            .font(.system(size: 9))
                            .foregroundColor(.secondary)
                    }
                }
                if inbox > 0 {
                    VStack(spacing: 2) {
                        Text("\(inbox)")
                            .font(.system(size: 18, weight: .semibold, design: .rounded))
                            .foregroundColor(.red)
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
