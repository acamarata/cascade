// LargeWidgetView.swift — 338×354 large widget face
// Purpose: Full fleet dashboard with per-provider sections, relay status,
//          active request count, quota labels, and a timestamp.
// Inputs: CascadeEntry (status + configuration)
// Outputs: SwiftUI View rendered at systemLarge (338×354 pt)
// Constraints: WidgetKit view-node budget is ~50. Provider sections use lazy stacks
//              only if agent count exceeds 8 (unlikely — keep it simple with ForEach).
// SPORT: apps/cascade-widget-macos — LargeWidgetView

import SwiftUI
import WidgetKit

struct LargeWidgetView: View {
    let entry: CascadeEntry

    private var status: CascadeStatus { entry.status }

    // Group agents by provider for section rendering.
    private var groupedAgents: [(provider: String, agents: [AgentSlot])] {
        let providers: [String]
        switch entry.configuration.agentGroup {
        case .all:        providers = ["Anthropic", "Gemini", "OpenAI"]
        case .anthropic:  providers = ["Anthropic"]
        case .gemini:     providers = ["Gemini"]
        case .openai:     providers = ["OpenAI"]
        }
        return providers.compactMap { p in
            let slots = status.agents.filter { $0.provider == p }
            guard !slots.isEmpty else { return nil }
            return (provider: p, agents: slots)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            // Top header
            LargeHeader(status: status)

            Divider()

            // Provider sections
            ForEach(groupedAgents, id: \.provider) { group in
                ProviderSection(provider: group.provider, agents: group.agents)
            }

            Spacer()

            Divider()

            // Footer: relay + timestamp
            LargeFooter(
                status: status,
                showRelay: entry.configuration.showRelay,
                date: entry.date
            )
        }
        .padding(14)
    }
}

// MARK: - Sub-views

private struct LargeHeader: View {
    let status: CascadeStatus

    var body: some View {
        HStack {
            Text("⚡")
                .font(.system(size: 18))
            VStack(alignment: .leading, spacing: 1) {
                Text("Cascade Fleet")
                    .font(.system(size: 14, weight: .bold))
                Text(status.daemonAlive ? "Daemon online" : "Daemon offline")
                    .font(.system(size: 10))
                    .foregroundStyle(status.daemonAlive ? .green : .red)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 2) {
                Text("\(status.activeRequests)")
                    .font(.system(size: 24, weight: .bold, design: .rounded))
                Text("active")
                    .font(.system(size: 9))
                    .foregroundStyle(.secondary)
            }
        }
    }
}

private struct ProviderSection: View {
    let provider: String
    let agents: [AgentSlot]

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(provider)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(.secondary)
                .textCase(.uppercase)
                .tracking(0.5)

            VStack(spacing: 3) {
                ForEach(agents) { agent in
                    LargeAgentRow(agent: agent)
                }
            }
        }
    }
}

private struct LargeAgentRow: View {
    let agent: AgentSlot

    private var accentColor: Color {
        Color(hex: agent.color) ?? .accentColor
    }

    var body: some View {
        HStack(spacing: 8) {
            // Active pulse indicator
            Circle()
                .fill(agent.isActive ? accentColor : Color.secondary.opacity(0.3))
                .frame(width: 6, height: 6)

            // Tier + ID
            HStack(spacing: 3) {
                Text(agent.tier)
                    .font(.system(size: 8, weight: .regular, design: .monospaced))
                    .foregroundStyle(.tertiary)
                Text(agent.id)
                    .font(.system(size: 11, weight: .semibold, design: .monospaced))
            }
            .frame(width: 46, alignment: .leading)

            // Usage bar
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    RoundedRectangle(cornerRadius: 3)
                        .fill(Color.secondary.opacity(0.15))
                        .frame(height: 7)
                    RoundedRectangle(cornerRadius: 3)
                        .fill(
                            LinearGradient(
                                colors: [accentColor.opacity(0.7), accentColor],
                                startPoint: .leading,
                                endPoint: .trailing
                            )
                        )
                        .frame(width: max(geo.size.width * agent.usagePct, 3), height: 7)
                }
            }
            .frame(height: 7)

            // Quota label
            Text(agent.quotaLabel)
                .font(.system(size: 9, design: .monospaced))
                .foregroundStyle(.secondary)
                .frame(width: 100, alignment: .trailing)
                .lineLimit(1)
                .truncationMode(.tail)
        }
    }
}

private struct LargeFooter: View {
    let status: CascadeStatus
    let showRelay: Bool
    let date: Date

    var body: some View {
        HStack {
            if showRelay {
                HStack(spacing: 4) {
                    Image(systemName: "antenna.radiowaves.left.and.right")
                        .font(.system(size: 9))
                        .foregroundStyle(status.relayAlive ? .green : .red)
                    Text("Relay \(status.relayAlive ? "up" : "down")")
                        .font(.system(size: 9))
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
            Text("Refreshed ")
                .font(.system(size: 9))
                .foregroundStyle(.tertiary)
            + Text(date, style: .relative)
                .font(.system(size: 9))
                .foregroundStyle(.tertiary)
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
