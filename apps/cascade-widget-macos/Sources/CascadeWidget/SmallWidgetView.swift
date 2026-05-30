// SmallWidgetView.swift — 158×158 small widget face
// Purpose: Compact summary showing daemon liveness, active request count,
//          and a three-slot mini usage bar for the top three agents.
// Inputs: CascadeEntry (status + configuration)
// Outputs: SwiftUI View rendered at systemSmall (158×158 pt)
// Constraints: No interactive elements (WidgetKit is read-only on macOS).
//              Keep hierarchy shallow — WidgetKit has strict view-count limits.
// SPORT: apps/cascade-widget-macos — SmallWidgetView

import SwiftUI
import WidgetKit

struct SmallWidgetView: View {
    let entry: CascadeEntry

    private var status: CascadeStatus { entry.status }

    // Colour for the daemon liveness badge.
    private var statusColor: Color {
        status.daemonAlive ? .green : .red
    }

    // Top 3 agents sorted by usage descending — fills the mini bars.
    private var topAgents: [AgentSlot] {
        Array(
            status.agents
                .sorted { $0.usagePct > $1.usagePct }
                .prefix(3)
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            // Header row — logo + liveness dot
            HStack {
                Text("⚡")
                    .font(.system(size: 16))
                Text("Cascade")
                    .font(.system(size: 13, weight: .semibold))
                Spacer()
                Circle()
                    .fill(statusColor)
                    .frame(width: 8, height: 8)
            }

            Divider()

            // Active requests summary
            HStack(spacing: 4) {
                Image(systemName: "bolt.horizontal.fill")
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
                Text("\(status.activeRequests) active")
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
            }

            Spacer()

            // Mini usage bars — top 3 agents
            VStack(spacing: 4) {
                ForEach(topAgents) { agent in
                    MiniUsageBar(agent: agent)
                }
            }

            // Timestamp
            Text("Updated \(entry.date, style: .relative) ago")
                .font(.system(size: 9))
                .foregroundStyle(.tertiary)
        }
        .padding(10)
    }
}

// MARK: - Mini usage bar

/// Compact single-agent usage row: ID label + progress bar.
struct MiniUsageBar: View {
    let agent: AgentSlot

    private var accentColor: Color {
        Color(hex: agent.color) ?? .accentColor
    }

    var body: some View {
        HStack(spacing: 6) {
            Text(agent.id)
                .font(.system(size: 10, weight: .medium, design: .monospaced))
                .frame(width: 22, alignment: .leading)

            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(Color.secondary.opacity(0.2))
                        .frame(height: 5)
                    Capsule()
                        .fill(accentColor)
                        .frame(width: max(geo.size.width * agent.usagePct, 2), height: 5)
                }
            }
            .frame(height: 5)

            Text("\(Int(agent.usagePct * 100))%")
                .font(.system(size: 9, design: .monospaced))
                .foregroundStyle(.secondary)
                .frame(width: 26, alignment: .trailing)
        }
    }
}

// MARK: - Color hex extension (local utility)

private extension Color {
    /// Initialise from a CSS hex string, e.g. "#E87040" or "E87040".
    init?(hex: String) {
        let hex = hex.trimmingCharacters(in: CharacterSet.alphanumerics.inverted)
        guard hex.count == 6, let value = UInt64(hex, radix: 16) else { return nil }
        self.init(
            red:   Double((value >> 16) & 0xFF) / 255,
            green: Double((value >>  8) & 0xFF) / 255,
            blue:  Double( value        & 0xFF) / 255
        )
    }
}
