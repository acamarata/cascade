import SwiftUI

// MARK: - CascadeMenuBarContentView
// Popover shown when the user clicks the menu bar status item.
// Layout: status summary → account table → footer actions.

struct CascadeMenuBarContentView: View {
    @ObservedObject var store: CascadeStore
    @EnvironmentObject private var desktopManager: CascadeDesktopManager

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            statusBar
            Divider()
                .padding(.vertical, 4)
            accountTable
            Divider()
                .padding(.vertical, 4)
            footer
        }
        .padding(12)
        .frame(width: 440)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    // MARK: Status bar — overall fleet health + freshness

    private var statusBar: some View {
        HStack(spacing: 8) {
            statusDot
            statusLabel
            Spacer()
            freshnessView
        }
    }

    private var statusDot: some View {
        Circle()
            .fill(statusColor)
            .frame(width: 7, height: 7)
    }

    private var statusColor: Color {
        guard let cache = store.cache else { return Color(hex: "#6B7280") }
        let rows = renderedEntries(for: cache)
        let healthy = rows.filter(isHealthyForHeader)
        if healthy.isEmpty { return Color(hex: "#6B7280") }
        let exhausted = healthy.filter {
            let w = $0.usage?.seven_day_opus?.utilization ?? $0.usage?.seven_day?.utilization ?? 0
            return w >= 100
        }
        if exhausted.count == healthy.count { return Color(hex: "#E5484D") }
        if !exhausted.isEmpty           { return Color(hex: "#F5A623") }
        return Color(hex: "#4ED17F")
    }

    private var statusLabel: some View {
        Group {
            if let cache = store.cache {
                let all = renderedEntries(for: cache)
                let auth = all.filter(isAuthNeededForHeader).count
                let healthy = all.filter(isHealthyForHeader)
                let exh = healthy.filter {
                    let w = $0.usage?.seven_day_opus?.utilization ?? $0.usage?.seven_day?.utilization ?? 0
                    return w >= 100
                }.count
                let text: String = {
                    if auth > 0 && exh > 0 { return "\(auth) need re-auth · \(exh) exhausted" }
                    if auth > 0 { return "\(auth) account\(auth == 1 ? "" : "s") need re-auth" }
                    if exh > 0  { return "\(exh) exhausted · \(healthy.count - exh) available" }
                    if healthy.count == all.count { return "\(healthy.count) accounts healthy" }
                    return "\(healthy.count)/\(all.count) accounts healthy"
                }()
                Text(text)
                    .font(.system(size: 11, weight: .medium))
            } else if store.loadError != nil {
                Text("Cache error")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundColor(Color(hex: "#E5484D"))
            } else {
                Text("Loading…")
                    .font(.system(size: 11))
                    .foregroundColor(Color(hex: "#6B7280"))
            }
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

    // MARK: Account table

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

    // MARK: Footer

    private var footer: some View {
        HStack(spacing: 0) {
            Button("Refresh") {
                store.refresh()
            }
            .buttonStyle(.plain)
            .font(.system(size: 11))

            Spacer()

            Button {
                desktopManager.toggle()
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: desktopManager.isVisible ? "checkmark.square" : "square")
                        .font(.system(size: 10))
                    Text("Widget")
                        .font(.system(size: 11))
                }
            }
            .buttonStyle(.plain)
            .foregroundColor(Color(hex: "#9CA3AF"))

            Spacer()

            Button("Open Cascade") {
                openCascadeApp()
            }
            .buttonStyle(.plain)
            .font(.system(size: 11))
            .foregroundColor(Color(hex: "#4F9BE8"))

            Spacer()

            Button("Quit") {
                NSApplication.shared.terminate(nil)
            }
            .buttonStyle(.plain)
            .font(.system(size: 11))
            .foregroundColor(Color(hex: "#E5484D"))
        }
    }

    private func openCascadeApp() {
        // Look for Cascade.app in ~/Applications first, then /Applications.
        let home = FileManager.default.homeDirectoryForCurrentUser
        let candidates: [URL] = [
            home.appendingPathComponent("Applications/Cascade.app"),
            URL(fileURLWithPath: "/Applications/Cascade.app"),
        ]
        if let url = candidates.first(where: { FileManager.default.fileExists(atPath: $0.path) }) {
            NSWorkspace.shared.open(url)
        } else {
            // Cascade not installed — open the accounts folder as fallback.
            NSWorkspace.shared.open(home.appendingPathComponent(".cascade/accounts"))
        }
    }

    private func renderedEntries(for cache: UsageCache) -> [AccountEntry] {
        labelledAccounts(from: cache.accounts.sorted { $0.account < $1.account }).map { $0.entry }
    }

    private func isHealthyForHeader(_ entry: AccountEntry) -> Bool {
        entry.status == "ok" || entry.status == "pool" || entry.authenticated == true
    }

    private func isAuthNeededForHeader(_ entry: AccountEntry) -> Bool {
        entry.status == "auth" || entry.authenticated == false
    }
}
