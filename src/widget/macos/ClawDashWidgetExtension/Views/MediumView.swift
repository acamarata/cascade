import SwiftUI

struct MediumView: View {
    let entry: ClawDashEntry

    var body: some View {
        let w = entry.cache._widget
        let gci = w?.gci
        let proj = w?.active_project ?? "No project"
        let phase = w?.active_phase
        let t = w?.totals

        HStack(spacing: 16) {
            // Left: project + task counts
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 4) {
                    Circle()
                        .fill(entry.cache.isStale ? Color.red : Color.green)
                        .frame(width: 5, height: 5)
                    Text("Claw Dash · " + entry.cache.ageString)
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
                }
                Spacer()
                HStack(spacing: 12) {
                    TaskBadge(count: t?.active ?? 0, label: "ACT", color: .orange)
                    TaskBadge(count: t?.ready ?? 0, label: "RDY", color: .blue)
                    TaskBadge(count: t?.review ?? 0, label: "REV", color: .indigo)
                    TaskBadge(count: t?.blocked ?? 0, label: "BLK", color: .red)
                }
            }

            Divider()

            // Right: GCI mini-stats
            VStack(alignment: .leading, spacing: 8) {
                Text("GLOBAL")
                    .font(.system(size: 9, weight: .semibold))
                    .foregroundColor(.secondary)
                HStack(spacing: 10) {
                    GCIBadge(count: gci?.rules ?? 0, label: "Rules")
                    GCIBadge(count: gci?.references ?? 0, label: "Refs")
                }
                HStack(spacing: 10) {
                    GCIBadge(count: gci?.memory ?? 0, label: "Memory")
                    GCIBadge(count: gci?.instructions ?? 0, label: "Skills")
                }
                Spacer()
                HStack(spacing: 8) {
                    if (t?.inbox ?? 0) > 0 {
                        Label("\(t!.inbox)", systemImage: "envelope.fill")
                            .font(.system(size: 10))
                            .foregroundColor(.red)
                    }
                    if (t?.ideas ?? 0) > 0 {
                        Label("\(t!.ideas)", systemImage: "lightbulb.fill")
                            .font(.system(size: 10))
                            .foregroundColor(.yellow)
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

struct TaskBadge: View {
    let count: Int; let label: String; let color: Color
    var body: some View {
        VStack(spacing: 1) {
            Text("\(count)")
                .font(.system(size: 16, weight: .bold, design: .rounded))
                .foregroundColor(count > 0 ? color : .secondary)
            Text(label)
                .font(.system(size: 8, weight: .medium))
                .foregroundColor(.secondary)
        }
    }
}

struct GCIBadge: View {
    let count: Int; let label: String
    var body: some View {
        HStack(spacing: 3) {
            Text("\(count)")
                .font(.system(size: 13, weight: .semibold, design: .rounded))
                .foregroundColor(.primary)
            Text(label)
                .font(.system(size: 9))
                .foregroundColor(.secondary)
        }
    }
}
