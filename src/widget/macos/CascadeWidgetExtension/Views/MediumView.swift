import SwiftUI

// Purpose: Medium widget view — left panel shows active project + task badges,
//          right panel shows CASCADE TIERS stats (GCI/ASI rules+memory) and inbox/ideas counts.
// Inputs:  CascadeEntry with P2 CascadeCache schema
// Outputs: SwiftUI View for .systemMedium WidgetKit family
// Constraints: No @State, no imperative calls — pure SwiftUI for WidgetKit.
//              Inline TierRow replaces the legacy badge component.
// SPORT: .claude/docs/MASTER-APPS.md — MediumView, P2

struct MediumView: View {
    let entry: CascadeEntry

    var body: some View {
        let w = entry.cache._widget
        let gci = w?.cascade_tiers?.gci
        let asi = w?.cascade_tiers?.asi
        let proj = w?.active_project ?? "no data"
        let phase = w?.active_phase
        let t = w?.totals
        let inboxCount = w?.inbox_unread
        let ideasCount = w?.ideas_count

        HStack(spacing: 16) {
            // Left: project + task counts
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 4) {
                    Circle()
                        .fill(widgetStatusColor(for: entry.cache))
                        .frame(width: 5, height: 5)
                    Text("Cascade · " + entry.cache.ageString)
                        .font(.system(size: 9))
                        .foregroundColor(.secondary)
                }
                Text(proj)
                    .font(.system(size: 14, weight: .bold))
                    .lineLimit(1)
                if let phase = phase {
                    Text(phase)
                        .font(.system(size: 10))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                } else if !entry.cache.hasWidgetData {
                    Text("—")
                        .font(.system(size: 10))
                        .foregroundColor(.secondary)
                }
                Spacer()
                HStack(spacing: 12) {
                    TaskBadge(count: t?.active, label: "ACT", color: .orange)
                    TaskBadge(count: t?.ready, label: "RDY", color: .blue)
                    TaskBadge(count: t?.review, label: "REV", color: .indigo)
                    TaskBadge(count: t?.blocked, label: "BLK", color: .red)
                }
            }

            Divider()

            // Right: CASCADE TIERS stats
            VStack(alignment: .leading, spacing: 8) {
                Text("CASCADE TIERS")
                    .font(.system(size: 9, weight: .semibold))
                    .foregroundColor(.secondary)
                // GCI row: rules + memory
                TierRow(
                    label: "GCI",
                    rules: gci.map { "\($0.rules)" } ?? "—",
                    memory: gci.map { "\($0.memory)" } ?? "—"
                )
                // ASI row: rules + memory (nil → "—")
                TierRow(
                    label: "ASI",
                    rules: asi.map { "\($0.rules)" } ?? "—",
                    memory: asi.map { "\($0.memory)" } ?? "—"
                )
                Spacer()
                HStack(spacing: 8) {
                    if let inboxCount = inboxCount, inboxCount > 0 {
                        Label("\(inboxCount)", systemImage: "envelope.fill")
                            .font(.system(size: 10))
                            .foregroundColor(.red)
                    }
                    if let ideasCount = ideasCount, ideasCount > 0 {
                        Label("\(ideasCount)", systemImage: "lightbulb.fill")
                            .font(.system(size: 10))
                            .foregroundColor(.yellow)
                    }
                    if !entry.cache.hasWidgetData {
                        Text("—")
                            .font(.system(size: 10))
                            .foregroundColor(.secondary)
                    }
                }
            }
        }
        .padding(14)
        .containerBackground(for: .widget) {
            Color(red: 0.07, green: 0.08, blue: 0.12)
        }
    }
}

// MARK: - Helper components

struct TaskBadge: View {
    let count: Int?; let label: String; let color: Color
    var body: some View {
        VStack(spacing: 1) {
            Text(count.map { "\($0)" } ?? "—")
                .font(.system(size: 16, weight: .bold, design: .rounded))
                .foregroundColor((count ?? 0) > 0 ? color : .secondary)
            Text(label)
                .font(.system(size: 8, weight: .medium))
                .foregroundColor(.secondary)
        }
    }
}

// TierRow: displays one cascade tier's rules + memory counts inline.
struct TierRow: View {
    let label: String
    let rules: String
    let memory: String
    var body: some View {
        HStack(spacing: 6) {
            Text(label)
                .font(.system(size: 9, weight: .semibold))
                .foregroundColor(.secondary)
                .frame(width: 24, alignment: .leading)
            Text(rules)
                .font(.system(size: 13, weight: .semibold, design: .rounded))
                .foregroundColor(.primary)
            Text("rules")
                .font(.system(size: 9))
                .foregroundColor(.secondary)
            Text(memory)
                .font(.system(size: 13, weight: .semibold, design: .rounded))
                .foregroundColor(.primary)
            Text("mem")
                .font(.system(size: 9))
                .foregroundColor(.secondary)
        }
    }
}
