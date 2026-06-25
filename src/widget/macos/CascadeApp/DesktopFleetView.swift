import SwiftUI
import AppKit

// MARK: - WindowDragHandle
// NSViewRepresentable that calls window?.performDrag(with:) on mouseDown.
// Used as a .background() on non-interactive header views so clicking/dragging
// the title area moves the panel without interfering with button hit targets.

private struct WindowDragHandle: NSViewRepresentable {
    func makeNSView(context: Context) -> DragHandleView { DragHandleView() }
    func updateNSView(_ nsView: DragHandleView, context: Context) {}

    final class DragHandleView: NSView {
        override func mouseDown(with event: NSEvent) {
            window?.performDrag(with: event)
        }
        override func resetCursorRects() {
            addCursorRect(bounds, cursor: .openHand)
        }
    }
}

// MARK: - DesktopFleetView
// The always-on-desktop fleet panel. Header = mountain icon + "Cascade" + a live
// "seconds since last update" counter; a footer holds Refresh + Settings buttons.

struct DesktopFleetView: View {
    @ObservedObject var store: CascadeStore
    @ObservedObject var focus: DesktopFocusMonitor

    private let widgetCornerRadius: CGFloat = 24

    var body: some View {
        content
            .padding(14)
            .background(
                RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                    .fill(Color(red: 24/255, green: 25/255, blue: 28/255).opacity(0.82))
                    .background(
                        RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                            .fill(.ultraThinMaterial)
                    )
                    .overlay(
                        RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous)
                            .strokeBorder(Color.white.opacity(0.10), lineWidth: 0.5)
                    )
                    .clipShape(RoundedRectangle(cornerRadius: widgetCornerRadius, style: .continuous))
            )
            .padding(8)
            .frame(width: 360)
            // Keep most of the color even when the desktop isn't frontmost — the user
            // wants a colorful readout, not a greyed-out one. Only a slight dim.
            .saturation(focus.isDesktopActive ? 1.0 : 0.85)
            .opacity(focus.isDesktopActive ? 1.0 : 0.82)
            .animation(.easeInOut(duration: 0.35), value: focus.isDesktopActive)
    }

    private var content: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider()
                .overlay(Color(hex: "#2A2D38"))
                .padding(.vertical, 6)
            accountTable
            footer
        }
    }

    // MARK: Header (icon + title + live counter)

    private var header: some View {
        HStack(spacing: 5) {
            Image(systemName: "mountain.2.fill")
                .font(.system(size: 11, weight: .bold))
                .foregroundColor(Color(hex: "#4F9BE8"))
                .allowsHitTesting(false)
            Text("Cascade")
                .font(.system(size: 12, weight: .bold))
                .foregroundColor(Color(hex: "#ECEFF3"))
                .allowsHitTesting(false)
            Spacer()
            freshnessView
                .allowsHitTesting(false)
        }
        // WindowDragHandle behind all header content — mouseDown here calls
        // window?.performDrag directly, enabling click-and-drag on the title area.
        .background(WindowDragHandle())
    }

    /// Live "time since last update", re-rendered every second via TimelineView so the
    /// seconds visibly tick up. Dot + value go green → amber → red as data ages.
    private var freshnessView: some View {
        TimelineView(.periodic(from: .now, by: 1)) { _ in
            if let lastLoaded = store.lastLoadedAt {
                let age = max(0, Int(Date().timeIntervalSince(lastLoaded)))
                let label = age < 60
                    ? "\(age)s"
                    : age < 3600 ? "\(age / 60)m \(age % 60)s" : "\(age / 3600)h \((age % 3600) / 60)m"
                let tint: Color = age < 120
                    ? Color(hex: "#4ED17F")
                    : age < 360 ? Color(hex: "#F5A623") : Color(hex: "#E5484D")
                HStack(spacing: 4) {
                    Circle().fill(tint).frame(width: 5, height: 5)
                    Text(label)
                        .font(.system(size: 9, design: .monospaced))
                        .foregroundColor(tint)
                }
            } else {
                Text("loading…")
                    .font(.system(size: 9))
                    .foregroundColor(Color(hex: "#7A7F8A"))
            }
        }
    }

    // MARK: Footer (Refresh + Settings)

    private var footer: some View {
        HStack(spacing: 12) {
            Spacer()
            Button(action: refresh) {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(.plain)
            .foregroundColor(Color(hex: "#9CA3AF"))
            .help("Refresh now")

            Button(action: openSettings) {
                Image(systemName: "gearshape.fill")
            }
            .buttonStyle(.plain)
            .foregroundColor(Color(hex: "#9CA3AF"))
            .help("Open Cascade settings")
        }
        .font(.system(size: 11))
        .padding(.top, 8)
    }

    private func refresh() {
        store.refresh()
    }

    /// Open the full Cascade.app (Tauri dashboard). Falls back to accounts dir if not installed.
    private func openSettings() {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let candidates: [URL] = [
            home.appendingPathComponent("Applications/Cascade.app"),
            URL(fileURLWithPath: "/Applications/Cascade.app"),
        ]
        if let url = candidates.first(where: { FileManager.default.fileExists(atPath: $0.path) }) {
            NSWorkspace.shared.open(url)
        } else {
            NSWorkspace.shared.open(home.appendingPathComponent(".cascade/accounts"))
        }
    }

    // MARK: Table

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
