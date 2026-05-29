import SwiftUI

struct LargeView: View {
    let entry: ClawDashEntry

    var body: some View {
        let w = entry.cache._widget
        let projects = w?.projects ?? []

        VStack(alignment: .leading, spacing: 10) {
            // Header
            HStack {
                Text("Claw Dash")
                    .font(.system(size: 13, weight: .bold))
                Spacer()
                HStack(spacing: 4) {
                    Circle()
                        .fill(entry.cache.isStale ? Color.red : Color.green)
                        .frame(width: 5, height: 5)
                    Text(entry.cache.ageString)
                        .font(.system(size: 10))
                        .foregroundColor(.secondary)
                }
            }

            // Medium content embedded
            MediumView(entry: entry)
                .frame(maxHeight: 100)

            Divider()

            // Projects table header
            HStack {
                Text("PROJECT").frame(maxWidth: .infinity, alignment: .leading)
                Text("ACT").frame(width: 28, alignment: .trailing)
                Text("REV").frame(width: 28, alignment: .trailing)
                Text("BLK").frame(width: 28, alignment: .trailing)
                Text("IN").frame(width: 22, alignment: .trailing)
            }
            .font(.system(size: 9, weight: .semibold))
            .foregroundColor(.secondary)

            ForEach(projects.prefix(5), id: \.label) { p in
                HStack {
                    Text(p.label)
                        .font(.system(size: 11))
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Text(p.active > 0 ? "\(p.active)" : "—")
                        .font(.system(size: 11, design: .rounded))
                        .foregroundColor(p.active > 0 ? .orange : Color(white: 0.35))
                        .frame(width: 28, alignment: .trailing)
                    Text(p.review > 0 ? "\(p.review)" : "—")
                        .font(.system(size: 11, design: .rounded))
                        .foregroundColor(p.review > 0 ? .indigo : Color(white: 0.35))
                        .frame(width: 28, alignment: .trailing)
                    Text(p.blocked > 0 ? "\(p.blocked)" : "—")
                        .font(.system(size: 11, design: .rounded))
                        .foregroundColor(p.blocked > 0 ? .red : Color(white: 0.35))
                        .frame(width: 28, alignment: .trailing)
                    Text(p.inbox > 0 ? "\(p.inbox)" : "—")
                        .font(.system(size: 11, design: .rounded))
                        .foregroundColor(p.inbox > 0 ? .red : Color(white: 0.35))
                        .frame(width: 22, alignment: .trailing)
                }
            }
            Spacer()
        }
        .padding(14)
        .containerBackground(for: .widget) {
            Color(red: 0.07, green: 0.08, blue: 0.12)
        }
    }
}
