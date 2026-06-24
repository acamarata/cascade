import SwiftUI

// MARK: - DesktopFleetView
// Ported from ClawFleet DesktopFleetView.swift.
// Branding: "Claw Fleet" → "Cascade".

struct DesktopFleetView: View {
    @ObservedObject var store: CascadeStore
    @ObservedObject var focus: DesktopFocusMonitor

    var body: some View {
        content
            .padding(14)
            .background(
                RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                    .fill(Color(red: 28/255, green: 28/255, blue: 30/255).opacity(0.72))
                    .background(
                        RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                            .fill(.ultraThinMaterial)
                    )
                    .overlay(
                        RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                            .strokeBorder(Color.white.opacity(0.08), lineWidth: 0.5)
                    )
                    .clipShape(RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous))
            )
            .padding(8)
            .frame(width: 360)
            .saturation(focus.isDesktopActive ? 1.0 : 0.0)
            .brightness(focus.isDesktopActive ? 0.0 : -0.06)
            .opacity(focus.isDesktopActive ? 1.0 : 0.55)
            .animation(.easeInOut(duration: 0.35), value: focus.isDesktopActive)
    }

    private let widgetCornerRadius: CGFloat = 24

    private var content: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
                .overlay(Color(hex: "#2A2D38"))
                .padding(.vertical, 6)
            accountTable
        }
    }

    private var header: some View {
        HStack {
            Text("Cascade")
                .font(.system(size: 11, weight: .semibold))
                .foregroundColor(Color(hex: "#C7CBD1"))
            Spacer()
            freshnessView
        }
    }

    private var freshnessView: some View {
        Group {
            if let lastLoaded = store.lastLoadedAt {
                let age = Int(Date().timeIntervalSince(lastLoaded))
                let label = age < 5 ? "just now" : age < 60 ? "\(age)s ago" : "\(age / 60)m ago"
                let tint: Color = age < 120
                    ? Color(hex: "#7A7F8A")
                    : age < 240 ? Color(hex: "#F5A623") : Color(hex: "#E5484D")
                Text(label)
                    .font(.system(size: 9))
                    .foregroundColor(tint)
            } else {
                Text("loading…")
                    .font(.system(size: 9))
                    .foregroundColor(Color(hex: "#7A7F8A"))
            }
        }
    }

    @ViewBuilder
    private var accountTable: some View {
        if let errorMsg = store.loadError {
            Text(errorMsg)
                .font(.system(size: 10))
                .foregroundColor(Color(hex: "#FB7185"))
                .padding(.vertical, 4)
        } else if let cache = store.cache {
            let rows = labelledAccounts(from: cache.accounts.sorted { $0.account < $1.account })
            let showExtra = rows.contains { fmtExtra($0.entry.usage?.extra_usage) != "—" }
            if rows.isEmpty {
                Text("No accounts in cache yet.")
                    .font(.system(size: 10))
                    .foregroundColor(Color(hex: "#9CA3AF"))
                    .italic()
            } else {
                VStack(alignment: .leading, spacing: 0) {
                    UsageTableHeader(showExtra: showExtra)
                    Divider().opacity(0.25)
                        .padding(.vertical, 2)
                    ForEach(rows, id: \.entry.id) { row in
                        UsageRow(label: row.label, entry: row.entry, showExtra: showExtra)
                            .padding(.vertical, 2)
                        Divider().opacity(0.15)
                    }
                }
            }
        } else {
            Text("Loading cache…")
                .font(.system(size: 10))
                .foregroundColor(Color(hex: "#9CA3AF"))
                .italic()
        }
    }
}
