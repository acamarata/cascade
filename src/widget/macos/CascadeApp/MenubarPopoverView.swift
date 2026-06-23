import SwiftUI

// MARK: - MenubarPopoverView
//
// Purpose: SwiftUI view shown inside the NSPopover attached to the Cascade menu-bar icon.
//          Displays the Fleet as a concise table: one row per account, columns 5H / Wk,
//          with usage % and compact reset countdown. Groups: reserved (primary) first,
//          then pooled Claude, then GFP accounts.
// Inputs:  QuotaStore — loaded by MenubarController on each 30s tick.
// Outputs: Read-only fleet table + footer menu (Open Cascade, Refresh Now, Quit).
// Constraints:
//   Width 320, height auto-sizes (min 120 for empty/error state).
//   All cells show "—" when data is absent — never fakes numbers.
//   Monospaced font for table columns for alignment.
// SPORT: .opencode/phases/sport/ — MenubarPopoverView (Fleet table)

struct MenubarPopoverView: View {
    let quota: QuotaStore?

    // Sorted accounts: reserved first, then pooled claude, then gfp/other
    private var sortedAccounts: [QuotaAccount] {
        guard let accounts = quota?.accounts else { return [] }
        return accounts.sorted { a, b in
            let aReserved = a.reserved ?? false
            let bReserved = b.reserved ?? false
            if aReserved != bReserved { return aReserved }
            // Within same reserved tier: claude before gfp
            let aFamily = a.family ?? ""
            let bFamily = b.family ?? ""
            if aFamily != bFamily {
                if aFamily == "claude" { return true }
                if bFamily == "claude" { return false }
            }
            return (a.display ?? a.id ?? "") < (b.display ?? b.id ?? "")
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {

            // ── Header ──────────────────────────────────────────────────
            HStack {
                Text("Cascade")
                    .font(.system(size: 13, weight: .semibold))
                Spacer()
                Text("updated \(quota?.ageString ?? "—")")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .padding(.horizontal, 12)
            .padding(.top, 10)
            .padding(.bottom, 6)

            Divider()

            // ── Column headers ───────────────────────────────────────────
            HStack(spacing: 0) {
                Text("Account")
                    .frame(maxWidth: .infinity, alignment: .leading)
                Text("5H")
                    .frame(width: 88, alignment: .trailing)
                Text("Wk")
                    .frame(width: 88, alignment: .trailing)
            }
            .font(.system(size: 10, weight: .medium))
            .foregroundColor(.secondary)
            .padding(.horizontal, 12)
            .padding(.vertical, 4)

            Divider()

            // ── Account rows ─────────────────────────────────────────────
            if sortedAccounts.isEmpty {
                Text(quota == nil ? "No quota data — run cascade" : "No accounts")
                    .font(.system(size: 11))
                    .foregroundColor(.secondary)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
            } else {
                ForEach(Array(sortedAccounts.enumerated()), id: \.offset) { idx, account in
                    AccountRow(account: account)
                    if idx < sortedAccounts.count - 1 {
                        Divider()
                            .padding(.leading, 12)
                    }
                }
            }

            Divider()

            // ── Footer buttons ───────────────────────────────────────────
            HStack(spacing: 6) {
                Button("Open Cascade") {
                    if let url = URL(string: "cascade://open") {
                        NSWorkspace.shared.open(url)
                    }
                }
                .buttonStyle(.bordered)
                .controlSize(.small)

                Button("Refresh") {
                    NotificationCenter.default.post(name: .cascadeRefreshNow, object: nil)
                }
                .buttonStyle(.bordered)
                .controlSize(.small)

                Spacer()

                Button("Quit") {
                    NSApplication.shared.terminate(nil)
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
        }
        .frame(width: 320)
    }
}

// MARK: - AccountRow

private struct AccountRow: View {
    let account: QuotaAccount

    private var label: String {
        account.display ?? account.id ?? "—"
    }

    private var isReserved: Bool { account.reserved ?? false }

    private var fiveHourCell: String {
        account.windows?.fiveHour?.displayCell() ?? "—"
    }

    private var weeklyCell: String {
        account.windows?.weekly?.displayCell() ?? "—"
    }

    var body: some View {
        HStack(spacing: 0) {
            // Account label with reserved indicator
            HStack(spacing: 4) {
                if isReserved {
                    Image(systemName: "star.fill")
                        .font(.system(size: 8))
                        .foregroundColor(.yellow)
                }
                Text(label)
                    .font(.system(size: 11, weight: isReserved ? .medium : .regular))
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            .frame(maxWidth: .infinity, alignment: .leading)

            // 5H window
            Text(fiveHourCell)
                .font(.system(size: 11, design: .monospaced))
                .foregroundColor(pctColor(account.windows?.fiveHour?.pct))
                .frame(width: 88, alignment: .trailing)
                .lineLimit(1)

            // Weekly window
            Text(weeklyCell)
                .font(.system(size: 11, design: .monospaced))
                .foregroundColor(pctColor(account.windows?.weekly?.pct))
                .frame(width: 88, alignment: .trailing)
                .lineLimit(1)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 5)
    }

    private func pctColor(_ pct: Double?) -> Color {
        guard let p = pct else { return .secondary }
        if p >= 90 { return .red }
        if p >= 70 { return .orange }
        return .primary
    }
}

// MARK: - Notification

extension Notification.Name {
    static let cascadeRefreshNow = Notification.Name("io.cascade.refreshNow")
}
