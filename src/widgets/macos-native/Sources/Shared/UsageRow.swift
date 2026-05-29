import SwiftUI

// MARK: - UsageRow

/// A single row in the account table, shared between the menu-bar detail view
/// and the widget. Mirrors the <tr> layout in the Übersicht widget:
///
///   Label | Ses% | Wk% | Son% | [Ex] | 5H countdown | Wk Rst
///
/// `showExtra` controls whether the Ex column is rendered. When every account
/// has empty/null extra-credit data, callers hide the column for both the
/// header and every row so the layout reclaims the space.
struct UsageRow: View {
    let label: String
    let entry: AccountEntry
    var showExtra: Bool = true

    private var u: UsageBlock? { entry.usage }

    private var sessUtil: Double? { u?.five_hour?.utilization }
    private var weekUtil: Double? { u?.seven_day?.utilization }
    private var sonnUtil: Double? { u?.seven_day_sonnet?.utilization }
    private var fiveHResetAt: Double? { u?.five_hour?.resets_at }
    private var weekResetAt: Double? { u?.seven_day?.resets_at }

    // OpenCode emits per-slot `status` strings ("ok" / "rate-limited") — use
    // them as the authoritative cap signal when present, falling back to a
    // numeric utilization >= 100 check for providers that don't emit status
    // (Anthropic, Codex, Gemini).
    private var weekCapped: Bool {
        if u?.seven_day?.status == "rate-limited" { return true }
        return (weekUtil ?? 0) >= 100
    }
    private var fiveHCapped: Bool {
        if u?.five_hour?.status == "rate-limited" { return true }
        return (sessUtil ?? 0) >= 100
    }
    private var opacity: Double   { rowOpacity(fiveHourPct: sessUtil, weekPct: weekUtil) }
    private var stale: Bool       { isStale(entry: entry) }
    // Gemini rows report no numeric data — only the label and presence matter.
    private var isOpaque: Bool    { entry.quota_opaque == true }

    var body: some View {
        HStack(spacing: 0) {
            // Label column — left-aligned, muted gray with an asterisk when stale.
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
                // Gemini: show label only; all quota columns say "—".
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
        // Session (5-hour) utilization.
        numCell(fmtPct(sessUtil), color: .utilColor(sessUtil))

        // Weekly utilization — bold when capped.
        numCell(fmtPct(weekUtil), color: .utilColor(weekUtil))
            .fontWeight(weekCapped ? .bold : .medium)

        // Sonnet weekly utilization.
        numCell(fmtPct(sonnUtil), color: .utilColor(sonnUtil))

        // Extra credits remaining — only when at least one row has visible data.
        if showExtra {
            numCell(fmtExtra(u?.extra_usage), color: Color(hex: "#C7CBD1"))
        }

        // 5-hour countdown. Show whenever the slot reports a reset time —
        // for providers like OpenCode the rolling window runs independently
        // of the weekly slot, so collapsing it to "—" when weekly is capped
        // would hide live data. When the 5-hour slot itself is capped, make
        // it white so it stands out as the active blocker.
        let countdownColor: Color = fiveHCapped ? .white : Color(hex: "#6B7280")
        numCell(fiveHResetAt != nil ? fmtCountdown(fiveHResetAt) : "—", color: countdownColor)
            .fontWeight(fiveHCapped ? .semibold : .regular)

        // Weekly reset timestamp. Force single-line so we don't get the
        // "May 22 / 12a" stack when the column is tight. 110px comfortably
        // holds "(Xh) 5/23 10a" at the 360px macOS-medium-widget panel width.
        let h = hoursUntil(weekResetAt)
        HStack(spacing: 2) {
            if let h = h {
                let hrColor = weekCapped ? Color(hex: "#C7CBD1") : Color(hex: "#4A4D54")
                Text("(\(h)h)")
                    .foregroundColor(hrColor)
                    .lineLimit(1)
            }
            Text(fmtDayHour(weekResetAt))
                .foregroundColor(Color(hex: "#C7CBD1"))
                .lineLimit(1)
        }
        .frame(minWidth: 110, alignment: .trailing)
        .fontWeight(weekCapped ? .bold : .regular)
    }

    @ViewBuilder
    private var opaqueColumns: some View {
        // Quota data is intentionally not available for these accounts.
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

    /// Hover tooltip on the label cell. For OpenCode rows it explains what the
    /// "Son" column actually represents (monthly%), surfaces the dollar context
    /// from the published Go limits, shows the Zen balance + useBalance state,
    /// and spells out the unblock path when the weekly slot is capped.
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
            // Note: the M/S column means Monthly% for OpenCode rows and
            // Sonnet weekly% for Claude rows; Codex / Gemini leave it null.
            lines.append("(M/S column = Monthly% for OpenCode rows.)")
            return lines.joined(separator: "\n")
        }
        // Non-opencode rows get the compact one-liner the c-idx title used to carry.
        let stRaw = entry.status ?? "live"
        return "\(email) • \(prov) • \(stRaw)"
    }
}

// MARK: - Table header

/// Column header row matching the Übersicht widget's thead layout.
/// `showExtra` mirrors the same flag on `UsageRow` so the header column
/// count matches the row column count when EX is hidden.
struct UsageTableHeader: View {
    var showExtra: Bool = true

    var body: some View {
        // "M/S" = Monthly OR Sonnet, depending on the row's provider. Only the
        // Claude rows use this slot for Sonnet-weekly%; OpenCode repurposes it
        // for Monthly% (Go plan $60 30-day cap); Codex and Gemini leave it
        // null. The dual-letter header signals that meaning differs per row.
        let cols: [String] = showExtra
            ? ["Ses", "Wk", "M/S", "Ex", "5H", "Wk Rst"]
            : ["Ses", "Wk", "M/S", "5H", "Wk Rst"]
        HStack(spacing: 0) {
            Text("#")
                .frame(width: 28, alignment: .leading)
            ForEach(cols, id: \.self) { col in
                Text(col)
                    .frame(minWidth: col == "Wk Rst" ? 110 : 36, alignment: .trailing)
                    .padding(.horizontal, 2)
            }
        }
        .font(.system(size: 9, weight: .medium, design: .default))
        .foregroundColor(Color(hex: "#7A7F8A"))
        .textCase(.uppercase)
        .kerning(0.8)
    }
}
