import SwiftUI
import WidgetKit

// MARK: - Widget view

struct ClawFleetWidgetView: View {
    let entry: CacheEntry

    @Environment(\.widgetFamily) private var family

    var body: some View {
        switch family {
        case .systemSmall:
            SmallWidgetView(entry: entry)
        default:
            MediumWidgetView(entry: entry)
        }
    }
}

// MARK: - Small widget (top 4 accounts)

private struct SmallWidgetView: View {
    let entry: CacheEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text("Claw Fleet")
                .font(.system(size: 9, weight: .semibold))
                .foregroundColor(Color(hex: "#7A7F8A"))

            if let err = entry.loadError {
                Text(err)
                    .font(.system(size: 9))
                    .foregroundColor(Color(hex: "#FB7185"))
            } else if let cache = entry.cache {
                let rows = labelledAccounts(from: cache.accounts
                    .sorted { $0.account < $1.account })
                    .prefix(4)  // Small widget shows top 4.
                ForEach(Array(rows), id: \.entry.id) { row in
                    CompactUsageRow(label: row.label, entry: row.entry)
                }
            } else {
                Text("No data")
                    .font(.system(size: 9))
                    .foregroundColor(Color(hex: "#9CA3AF"))
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

// MARK: - Medium widget (all accounts, condensed)

private struct MediumWidgetView: View {
    let entry: CacheEntry

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text("Claw Fleet")
                    .font(.system(size: 10, weight: .semibold))
                Spacer()
                Text(widgetTimestamp)
                    .font(.system(size: 9))
                    .foregroundColor(Color(hex: "#7A7F8A"))
            }

            if let err = entry.loadError {
                Text(err)
                    .font(.system(size: 10))
                    .foregroundColor(Color(hex: "#FB7185"))
            } else if let cache = entry.cache {
                let rows = labelledAccounts(from: cache.accounts
                    .sorted { $0.account < $1.account })
                ForEach(rows, id: \.entry.id) { row in
                    CompactUsageRow(label: row.label, entry: row.entry)
                }
            } else {
                Text("No data")
                    .font(.system(size: 10))
                    .foregroundColor(Color(hex: "#9CA3AF"))
                    .italic()
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var widgetTimestamp: String {
        let formatter = DateFormatter()
        formatter.dateFormat = "h:mma"
        return formatter.string(from: entry.date).lowercased()
    }
}

// MARK: - Compact row for widget surfaces

/// Condensed row: label + week% + 5h%. Fits within the tight width
/// constraints of .systemSmall and .systemMedium.
private struct CompactUsageRow: View {
    let label: String
    let entry: AccountEntry

    private var weekUtil: Double? { entry.usage?.seven_day?.utilization }
    private var sessUtil: Double? { entry.usage?.five_hour?.utilization }
    private var opacity: Double   { rowOpacity(fiveHourPct: sessUtil, weekPct: weekUtil) }

    var body: some View {
        HStack(spacing: 4) {
            Text(label)
                .foregroundColor(Color(hex: "#7A7F8A"))
                .frame(width: 20, alignment: .leading)

            Text(fmtPct(weekUtil))
                .foregroundColor(.utilColor(weekUtil))
                .frame(width: 32, alignment: .trailing)
                .fontWeight((weekUtil ?? 0) >= 100 ? .bold : .regular)

            Text(fmtPct(sessUtil))
                .foregroundColor(.utilColor(sessUtil))
                .frame(width: 32, alignment: .trailing)

            Spacer()
        }
        .font(.system(size: 10, design: .monospaced))
        .opacity(opacity)
    }
}
