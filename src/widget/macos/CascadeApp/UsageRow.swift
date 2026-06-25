import SwiftUI
import AppKit

// MARK: - UsageRow
// Ported verbatim from ClawFleet Sources/Shared/UsageRow.swift.
// Columns: Label | 5H% | Wk% | [Ex] | SR countdown | Reset

struct UsageRow: View {
    let label: String
    let entry: AccountEntry
    var showExtra: Bool = true

    private var u: UsageBlock? { entry.usage }

    private var sessUtil: Double? { u?.five_hour?.utilization }
    /// Wk column: opus weekly; falls back to seven_day so non-Claude providers still show.
    private var weekUtil: Double? { u?.seven_day_opus?.utilization ?? u?.seven_day?.utilization }
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

            if entry.status == "auth" {
                authColumns
            } else if entry.key_count != nil {
                poolColumns
            } else if isOpaque {
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
        if showExtra {
            numCell(fmtExtra(u?.extra_usage), color: Color(hex: "#C7CBD1"))
        }
        let countdownColor: Color = fiveHCapped ? .white : Color(hex: "#6B7280")
        numCell(fiveHResetAt != nil ? fmtCountdown(fiveHResetAt) : "—", color: countdownColor)
            .fontWeight(fiveHCapped ? .semibold : .regular)
        // Reset column: single monospaced string so (Xh) suffix aligns vertically.
        // Pad suffix to fixed 6-char width: "  (9h)" / " (15h)" / "(150h)".
        // Color: dark grey when healthy (secondary info), light grey when exhausted
        // (you need to see when the window resets).
        let h = hoursUntil(weekResetAt)
        let hrSuffix: String = {
            guard let h = h else { return "" }
            if h < 10  { return "  (\(h)h)" }
            if h < 100 { return " (\(h)h)" }
            return "(\(h)h)"
        }()
        let resetColor: Color = weekCapped ? Color(hex: "#C7CBD1") : Color(hex: "#4A4D54")
        Text(fmtDayHour(weekResetAt) + hrSuffix)
            .foregroundColor(resetColor)
            .lineLimit(1)
            .frame(minWidth: 116, alignment: .trailing)
            .fontWeight(weekCapped ? .bold : .regular)
    }

    @ViewBuilder
    private var opaqueColumns: some View {
        let dim = Color(hex: "#6B7280")
        numCell("—", color: dim)
        numCell("—", color: dim)
        if showExtra { numCell("—", color: dim) }
        numCell("—", color: dim)
        Text("—")
            .foregroundColor(dim)
            .frame(minWidth: 116, alignment: .trailing)
    }

    /// Credential-dead account (expired Claude OAuth / invalid Gemini key). The whole
    /// data area becomes a clickable amber call-to-action that launches the right
    /// re-auth flow: `claude auth login` for Claude, the API-key page for Gemini.
    @ViewBuilder
    private var authColumns: some View {
        Button(action: reauth) {
            HStack(spacing: 4) {
                Image(systemName: "arrow.up.forward.app")
                    .font(.system(size: 10))
                Text("Click here to re-auth")
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .foregroundColor(Color(hex: "#F5A623"))
        .help("Sign in to refresh this account's usage data")
    }

    /// Launch the full re-auth flow via the `cascade-reauth` helper in a Terminal
    /// window. The helper runs `claude auth login` (which opens the browser and runs
    /// its own localhost OAuth callback — the whole handshake, no gaps), scoped to this
    /// account's config dir, then immediately re-polls so the row fills within seconds.
    /// For Gemini the helper opens the Google AI Studio API-key page instead.
    private func reauth() {
        let helper = "\(NSHomeDirectory())/.cascade/bin/cascade-reauth"
        let cmd = "\(helper) \(entry.account)"
        let script = """
        tell application "Terminal"
            activate
            do script "\(cmd)"
        end tell
        """
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        p.arguments = ["-e", script]
        try? p.run()
    }

    /// GFP free-Flash key pool: show the round-robin key count as available capacity.
    @ViewBuilder
    private var poolColumns: some View {
        let dim = Color(hex: "#4A4D54")
        numCell("—", color: dim)
        numCell("—", color: dim)
        if showExtra { numCell("—", color: dim) }
        numCell("—", color: dim)
        Text("\(entry.key_count ?? 0) keys free")
            .foregroundColor(Color(hex: "#4ED17F"))
            .frame(minWidth: 116, alignment: .trailing)
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
            lines.append("(SR col = 5h rolling reset countdown.)")
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
            ? ["5H", "Wk", "Ex", "SR", "Reset"]
            : ["5H", "Wk", "SR", "Reset"]
        HStack(spacing: 0) {
            Text("#")
                .frame(width: 28, alignment: .leading)
            ForEach(cols, id: \.self) { col in
                Text(col)
                    .frame(minWidth: col == "Reset" ? 116 : 36, alignment: .trailing)
                    .padding(.horizontal, 2)
            }
        }
        .font(.system(size: 9, weight: .medium, design: .default))
        .foregroundColor(Color(hex: "#7A7F8A"))
        .textCase(.uppercase)
        .kerning(0.8)
    }
}
