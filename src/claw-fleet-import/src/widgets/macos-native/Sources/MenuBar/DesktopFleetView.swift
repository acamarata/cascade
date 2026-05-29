import SwiftUI

// MARK: - DesktopFleetView

/// The SwiftUI content rendered inside the borderless desktop panel.
///
/// Draws its own rounded-rect background with the same dark semi-transparent
/// color and blur radius used by the Übersicht widget (rgba(14,16,24,0.76) +
/// backdrop blur 12px), so the desktop window matches the widget aesthetically
/// without relying on NSVisualEffectView.
struct DesktopFleetView: View {
    @ObservedObject var store: CacheStore
    @ObservedObject var focus: DesktopFocusMonitor

    var body: some View {
        content
            .padding(14)
            .background(
                // 24 pt continuous corner radius matches the macOS desktop-widget
                // container (measured against the system Calendar / Weather
                // widgets); the prior 12 pt looked boxy next to them.
                RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                    // Neutral charcoal (#1C1C1E, macOS secondarySystemBackground
                    // dark) over the blur, instead of the old navy — the navy
                    // read noticeably bluer/darker than the stock dark widgets.
                    .fill(Color(red: 28/255, green: 28/255, blue: 30/255).opacity(0.72))
                    .background(
                        // Blur the desktop wallpaper showing through the overlay
                        // so the panel picks up the same wallpaper tint the
                        // system widgets do.
                        RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                            .fill(.ultraThinMaterial)
                    )
                    .overlay(
                        // Hairline edge like the stock widgets' container border.
                        RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                            .strokeBorder(Color.white.opacity(0.08), lineWidth: 0.5)
                    )
                    .clipShape(RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous))
            )
            .padding(8)
            // 360px is the exact macOS medium desktop-widget width (measured:
            // small = 180, medium = 360), so the panel lines up edge-to-edge
            // with the Calendar / Notification-Center widgets. The compact
            // column layout (numeric dates, auto-hidden EX) fits comfortably.
            .frame(width: 360)
            // macOS "Dim widgets on desktop = Automatically": go monochrome and
            // recede when the desktop is not the active context, full-color when
            // it is. Saturation removes the green/red/amber from the % cells;
            // the opacity + brightness drop matches the system's recessed look.
            .saturation(focus.isDesktopActive ? 1.0 : 0.0)
            .brightness(focus.isDesktopActive ? 0.0 : -0.06)
            .opacity(focus.isDesktopActive ? 1.0 : 0.55)
            .animation(.easeInOut(duration: 0.35), value: focus.isDesktopActive)
    }

    /// Matches the macOS desktop-widget container radius (~24 pt continuous).
    private let widgetCornerRadius: CGFloat = 24

    // MARK: - Inner layout (mirrors MenuBarContentView structure)

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
            Text("Claw Fleet")
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
            // Same EX-column gating as MenuBarContentView: hide when nobody
            // has extra-credit data so the empty column doesn't waste pixels.
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
