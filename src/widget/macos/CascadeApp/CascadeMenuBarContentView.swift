import SwiftUI

// MARK: - CascadeMenuBarContentView
// Ported from ClawFleet MenuBarContentView.swift.
// Branding: "Claw Fleet" → "Cascade".

struct CascadeMenuBarContentView: View {
    @ObservedObject var store: CascadeStore
    @EnvironmentObject private var desktopManager: CascadeDesktopManager

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
                .padding(.vertical, 4)
            accountTable
            Divider()
                .padding(.vertical, 4)
            footer
        }
        .padding(12)
        .frame(width: 420)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private var header: some View {
        HStack {
            Text("Cascade")
                .font(.system(size: 12, weight: .semibold))
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
                    .font(.system(size: 10))
                    .foregroundColor(tint)
            } else {
                Text("loading…")
                    .font(.system(size: 10))
                    .foregroundColor(Color(hex: "#7A7F8A"))
            }
        }
    }

    @ViewBuilder
    private var accountTable: some View {
        if let errorMsg = store.loadError {
            Text(errorMsg)
                .font(.system(size: 11))
                .foregroundColor(Color(hex: "#FB7185"))
                .padding(.vertical, 4)
        } else if let cache = store.cache {
            let rows = labelledAccounts(from: cache.accounts.sorted { $0.account < $1.account })
            let showExtra = rows.contains { fmtExtra($0.entry.usage?.extra_usage) != "—" }
            if rows.isEmpty {
                Text("No accounts in cache yet.")
                    .font(.system(size: 11))
                    .foregroundColor(Color(hex: "#9CA3AF"))
                    .italic()
            } else {
                VStack(alignment: .leading, spacing: 0) {
                    UsageTableHeader(showExtra: showExtra)
                    Divider().padding(.vertical, 2)
                    ForEach(rows, id: \.entry.id) { row in
                        UsageRow(label: row.label, entry: row.entry, showExtra: showExtra)
                            .padding(.vertical, 3)
                        Divider().opacity(0.3)
                    }
                }
            }
        } else {
            Text("Loading cache…")
                .font(.system(size: 11))
                .foregroundColor(Color(hex: "#9CA3AF"))
                .italic()
        }
    }

    private var footer: some View {
        HStack {
            Button("Refresh now") {
                store.refresh()
            }
            .buttonStyle(.plain)
            .font(.system(size: 11))

            Spacer()

            Button {
                desktopManager.toggle()
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: desktopManager.isVisible ? "checkmark.circle.fill" : "circle")
                        .font(.system(size: 10))
                    Text("Desktop widget")
                        .font(.system(size: 11))
                }
            }
            .buttonStyle(.plain)
            .foregroundColor(Color(hex: "#9CA3AF"))

            Spacer()

            Button("Quit") {
                NSApplication.shared.terminate(nil)
            }
            .buttonStyle(.plain)
            .font(.system(size: 11))
            .foregroundColor(Color(hex: "#E5484D"))
        }
    }
}
