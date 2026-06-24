import SwiftUI

// MARK: - UsageRow
// Ported verbatim from ClawFleet Sources/Shared/UsageRow.swift.
// Columns: Label | Ses% | Wk% | M/S% | [Ex] | 5H countdown | Wk Rst

struct UsageRow: View {
    let label: String
    let entry: AccountEntry
    var showExtra: Bool = true

    private var u: UsageBlock? { entry.usage }

    private var sessUtil: Double? { u?.five_hour?.utilization }
    /// Wk column: opus weekly; falls back to seven_day so non-Claude providers still show.
    private var weekUtil: Double? { u?.seven_day_opus?.utilization ?? u?.seven_day?.utilization }
    private var sonnUtil: Double? { u?.seven_day_sonnet?.utilization }
    private var fiveHResetAt: Double? { u?.five_hour?.resets_at }
    /// Week reset: use opus slot when available, else generic seven_day.
    private var weekResetAt: Double? { u?.seven_day_opus?.resets_at ?? u?.seven_day?.resets_at }

    private var weekCapped: Bool {
        let opusStatus = u?.seven_day_opus?.status ?? u?.seven_day?.status
        if opusStatus == "rate-limited" { return true }
        return (weekUtil ?? 0) >= 100
    }
    private var fiveHCapped: Bool {
        if u?.five_hour?.status == "rate-limited" { return true }
        return (sessUtil ?? 0) >= 100
    }
    private var opacity: Double { rowOpacity(fiveHourPct: sessUtil, weekPct: weekUtil) }
    private var stale: Bool     { isStale(entry: entry) }
    private var isOpaque: Bool  { entry.quota_opaque == true }

    var body: some View {
        HStack(spacing: 0) {
            HStack(spacing: 0) {
                Text(label)
                    .foregroundColor(Color(hex: "#7A7F8A"))
                    .fontWeight(.medium)
                if stale {
                    Text("*")
                        .foregroundColor(Color(hex: "#E5484D"))
                        .fontWeight(.bold)
                }
            }
            .frame(width: 28, alignment: .leading)
            .help(rowTooltip)

            if isOpaque {
                opaqueColumns
            } else {
                dataColumns
            }
        }
        .font(.system(size: 11, design: .monospaced))
        .opacity(opacity)
    }

    @ViewBuilder
    private var dataColumns: some View {
        numCell(fmtPct(sessUtil), color: .utilColor(sessUtil))
        numCell(fmtPct(weekUtil), color: .utilColor(weekUtil))
            .fontWeight(weekCapped ? .bold : .medium)
        numCell(fmtPct(sonnUtil), color: .utilColor(sonnUtil))
        if showExtra {
            numCell(fmtExtra(u?.extra_usage), color: Color(hex: "#C7CBD1"))
        }
        let countdownColor: Color = fiveHCapped ? .white : Color(hex: "#6B7280")
        numCell(fiveHResetAt != nil ? fmtCountdown(fiveHResetAt) : "—", color: countdownColor)
            .fontWeight(fiveHCapped ? .semibold : .regular)
        let h = hoursUntil(weekResetAt)
        HStack(spacing: 3) {
            Text(fmtDayHour(weekResetAt))
                .foregroundColor(Color(hex: "#C7CBD1"))
                .lineLimit(1)
            if let h = h {
                let hrColor = weekCapped ? Color(hex: "#C7CBD1") : Color(hex: "#4A4D54")
                Text("(\(h)h)")
                    .foregroundColor(hrColor)
                    .lineLimit(1)
            }
        }
        .frame(minWidth: 116, alignment: .trailing)
        .fontWeight(weekCapped ? .bold : .regular)
    }

    @ViewBuilder
    private var opaqueColumns: some View {
        ForEach(0..<6, id: \.self) { _ in
            numCell("—", color: Color(hex: "#6B7280"))
        }
    }

    private func numCell(_ text: String, color: Color) -> some View {
        Text(text)
            .foregroundColor(color)
            .frame(minWidth: 36, alignment: .trailing)
            .padding(.horizontal, 2)
    }

    private var rowTooltip: String {
        let email = entry.email ?? entry.account
        let prov  = entry.provider ?? "claude"
        if prov == "opencode", let m = entry.opencode_meta {
            let plan = m.subscription_plan ?? "Go"
            let bal  = Double(m.balance ?? 0) / 100.0
            let useBal = m.use_balance ?? false
            let s = m.spent_usd
            let l = m.limits_usd
            func fmt(_ spent: Double?, _ limit: Int?, _ status: String?) -> String {
                guard let sp = spent, let lim = limit else { return "—" }
                let base = "$\(Int(sp.rounded())) / $\(lim)"
                if status == "rate-limited" { return base + " RATE-LIMITED" }
                return base
            }
            var lines = [
                "\(email) — OpenCode \(plan.capitalized) plan",
                "5h:  \(fmt(s?.rolling, l?.rolling, u?.five_hour?.status))",
                "7d:  \(fmt(s?.weekly,  l?.weekly,  u?.seven_day?.status))",
                "mo:  \(fmt(s?.monthly, l?.monthly, u?.seven_day_sonnet?.status))",
                "Zen balance: $\(String(format: "%.2f", bal)) • useBalance: \(useBal ? "on" : "off")",
            ]
            if weekCapped {
                if bal > 0 && !useBal {
                    lines.append("Unblock: toggle Use balance ON in console.")
                } else if bal == 0 {
                    lines.append("Unblock: top up Zen balance + enable Use balance.")
                }
            }
            lines.append("(M/S column = Monthly% for OpenCode rows.)")
            return lines.joined(separator: "\n")
        }
        let stRaw = entry.status ?? "live"
        return "\(email) • \(prov) • \(stRaw)"
    }
}

// MARK: - Table header

struct UsageTableHeader: View {
    var showExtra: Bool = true

    var body: some View {
        let cols: [String] = showExtra
            ? ["Ses", "Wk", "M/S", "Ex", "5H", "Wk Reset"]
            : ["Ses", "Wk", "M/S", "5H", "Wk Reset"]
        HStack(spacing: 0) {
            Text("#")
                .frame(width: 28, alignment: .leading)
            ForEach(cols, id: \.self) { col in
                Text(col)
                    .frame(minWidth: col == "Wk Reset" ? 116 : 36, alignment: .trailing)
                    .padding(.horizontal, 2)
            }
        }
        .font(.system(size: 9, weight: .medium, design: .default))
        .foregroundColor(Color(hex: "#7A7F8A"))
        .textCase(.uppercase)
        .kerning(0.8)
    }
}
