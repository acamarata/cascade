// MediumWidgetView.swift — 338×158 medium widget face
// Purpose: Two-column layout showing all agent slots with usage bars on the left
//          and relay + active-request summary on the right.
// Inputs: CascadeEntry (status + configuration)
// Outputs: SwiftUI View rendered at systemMedium (338×158 pt)
// Constraints: No interactive elements. Keep total view nodes < ~30 for WidgetKit.
// SPORT: apps/cascade-widget-macos — MediumWidgetView

import SwiftUI
import WidgetKit

struct MediumWidgetView: View {
    let entry: CascadeEntry

    private var status: CascadeStatus { entry.status }

    /// Filter agents by the configured group (or all if .all).
    private var visibleAgents: [AgentSlot] {
        switch entry.configuration.agentGroup {
        case .all:        return status.agents
        case .anthropic:  return status.agents.filter { $0.provider == "Anthropic" }
        case .gemini:     return status.agents.filter { $0.provider == "Gemini" }
        case .openai:     return status.agents.filter { $0.provider == "OpenAI" }
        }
    }

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            // Left: agent usage list
            VStack(alignment: .leading, spacing: 0) {
                HeaderRow(status: status)
                Divider().padding(.vertical, 4)
                AgentUsageList(agents: visibleAgents)
                Spacer()
            }

            Divider()

            // Right: summary stats
            VStack(alignment: .leading, spacing: 8) {
                StatBadge(
                    icon: "bolt.horizontal.fill",
                    label: "Active",
                    value: "\(status.activeRequests)"
                )
                if entry.configuration.showRelay {
                    StatBadge(
                        icon: "antenna.radiowaves.left.and.right",
                        label: "Relay",
                        value: status.relayAlive ? "Up" : "Down",
                        valueColor: status.relayAlive ? .green : .red
                    )
                }
                Spacer()
                Text(entry.date, style: .relative)
                    .font(.system(size: 9))
                    .foregroundStyle(.tertiary)
            }
            .frame(width: 90)
        }
        .padding(12)
    }
}

// MARK: - Sub-views

private struct HeaderRow: View {
    let status: CascadeStatus

    var body: some View {
        HStack {
            Text("⚡ Cascade Fleet")
                .font(.system(size: 12, weight: .semibold))
            Spacer()
            Circle()
                .fill(status.daemonAlive ? Color.green : Color.red)
                .frame(width: 7, height: 7)
        }
    }
}

private struct AgentUsageList: View {
    let agents: [AgentSlot]

    var body: some View {
        VStack(spacing: 3) {
            ForEach(agents.prefix(6)) { agent in
                AgentUsageRow(agent: agent)
            }
        }
    }
}

private struct AgentUsageRow: View {
    let agent: AgentSlot

    private var accentColor: Color {
        Color(hex: agent.color) ?? .accentColor
    }

    var body: some View {
        HStack(spacing: 6) {
            // Tier badge
            Text(agent.tier)
                .font(.system(size: 8, weight: .medium, design: .monospaced))
                .foregroundStyle(.secondary)
                .frame(width: 18, alignment: .center)

            // Agent ID
            Text(agent.id)
                .font(.system(size: 10, weight: .medium, design: .monospaced))
                .frame(width: 22, alignment: .leading)

            // Usage bar
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(Color.secondary.opacity(0.15))
                        .frame(height: 5)
                    Capsule()
                        .fill(agent.isActive ? accentColor : accentColor.opacity(0.6))
                        .frame(width: max(geo.size.width * agent.usagePct, 2), height: 5)
                }
            }
            .frame(height: 5)

            // Pct label
            Text("\(Int(agent.usagePct * 100))%")
                .font(.system(size: 9, design: .monospaced))
                .foregroundStyle(.secondary)
                .frame(width: 28, alignment: .trailing)
        }
    }
}

private struct StatBadge: View {
    let icon: String
    let label: String
    let value: String
    var valueColor: Color = .primary

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 4) {
                Image(systemName: icon)
                    .font(.system(size: 9))
                    .foregroundStyle(.secondary)
                Text(label)
                    .font(.system(size: 9))
                    .foregroundStyle(.secondary)
            }
            Text(value)
                .font(.system(size: 16, weight: .semibold, design: .rounded))
                .foregroundStyle(valueColor)
        }
    }
}

// MARK: - Color hex extension (local)

private extension Color {
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
