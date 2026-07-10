import SwiftUI

// MARK: - Color thresholds
// Ported from ClawFleet Sources/Shared/Formatting.swift.

extension Color {
    /// Returns the display color for a utilization percentage.
    /// nil → gray (unknown), 100+ → muted gray (capped), then red/orange/amber/green.
    static func utilColor(_ p: Double?) -> Color {
        guard let p = p else { return Color(hex: "#6B7280") }
        if p >= 100 { return Color(hex: "#9CA3AF") }
        if p >= 76  { return Color(hex: "#E5484D") }
        if p >= 51  { return Color(hex: "#F76808") }
        if p >= 26  { return Color(hex: "#F5A623") }
        return Color(hex: "#30A46C")
    }

    /// Convenience hex initializer (6-digit, with or without leading #).
    init(hex: String) {
        let h = hex.hasPrefix("#") ? String(hex.dropFirst()) : hex
        var rgb: UInt64 = 0
        Scanner(string: h).scanHexInt64(&rgb)
        let r = Double((rgb >> 16) & 0xFF) / 255.0
        let g = Double((rgb >>  8) & 0xFF) / 255.0
        let b = Double( rgb        & 0xFF) / 255.0
        self.init(red: r, green: g, blue: b)
    }
}

// MARK: - Account label logic
// Adapted from ClawFleet labelledAccounts() with new provider/label scheme:
//   claude   → "An" (single) / "A1","A2",… (multiple)
//   codex    → "Co" (single) / "C1","C2",… (multiple)
//   gemini   → "GP" (single) / "G1","G2",… (multiple)  — Google Pro (paid Antigravity)
//   opencode → always "Oc" (single pool, no number)
//   zai/glm  → "Za" (single) / "Z1","Z2",… (multiple)  — z.ai GLM Coding Plan
//   gfp      → always "GF" (Google Free — Gemini Flash key pool, no number)
// Provider display order: claude, codex, gemini(GP), opencode, zai, gfp(GF).
// shouldAutoHide() applies only to claude rows (hide when never-pulled + all-null usage).

func shouldAutoHide(_ e: AccountEntry) -> Bool {
    guard (e.provider ?? "claude") == "claude" else { return false }
    let u = e.usage
    let hasAnyUsage = (u?.five_hour != nil) || (u?.seven_day != nil) || (u?.seven_day_sonnet != nil)
    return e.last_pull_at == nil && !hasAnyUsage
}

/// Groups accounts by provider, drops cancelled-subscription claude rows, and
/// assigns display labels per the Cascade labeling scheme.
///
/// - Single account in a provider family → 2-letter label ("An", "Co", "GP", "Oc", "Za", "GF")
/// - Multiple accounts in a family → letter + number ("A1","A2", "C1","C2", "G1","G2", "Z1","Z2")
/// - opencode and gfp are always single-pool labels regardless of count
func labelledAccounts(from entries: [AccountEntry]) -> [(label: String, entry: AccountEntry)] {
    let providerOrder = ["claude", "codex", "gemini", "opencode", "zai", "gfp"]

    // Group by provider, dropping auto-hidden claude rows.
    var byProvider: [String: [AccountEntry]] = [:]
    for entry in entries where !shouldAutoHide(entry) {
        let p = entry.provider ?? "claude"
        byProvider[p, default: []].append(entry)
    }

    var result: [(label: String, entry: AccountEntry)] = []
    for provider in providerOrder {
        guard let group = byProvider[provider] else { continue }
        let label = makeLabels(provider: provider, group: group)
        for (entry, lbl) in zip(group, label) {
            result.append((label: lbl, entry: entry))
        }
    }
    return result
}

/// Produces one label string per entry in the group, using the Cascade scheme.
private func makeLabels(provider: String, group: [AccountEntry]) -> [String] {
    switch provider {
    case "opencode":
        return group.map { _ in "Oc" }
    case "gfp":
        return group.map { _ in "GF" }
    case "claude":
        if group.count == 1 {
            return ["An"]
        }
        return group.map { entry in
            let num = providerSuffix(entry.account) ?? "\(group.firstIndex(where: { $0.account == entry.account }).map { $0 + 1 } ?? 1)"
            return "A\(num)"
        }
    case "codex":
        if group.count == 1 {
            return ["Co"]
        }
        return group.enumerated().map { (idx, entry) in
            let num = providerSuffix(entry.account) ?? "\(idx + 1)"
            return "C\(num)"
        }
    case "gemini":
        if group.count == 1 {
            return ["GP"]
        }
        return group.enumerated().map { (idx, entry) in
            let num = providerSuffix(entry.account) ?? "\(idx + 1)"
            return "G\(num)"
        }
    case "zai", "glm":
        if group.count == 1 {
            return ["Za"]
        }
        return group.enumerated().map { (idx, entry) in
            let num = providerSuffix(entry.account) ?? "\(idx + 1)"
            return "Z\(num)"
        }
    default:
        // Unknown provider: use first letter uppercased + number.
        let prefix = provider.prefix(1).uppercased()
        if group.count == 1 {
            return ["\(prefix)x"]
        }
        return group.enumerated().map { (idx, _) in "\(prefix)\(idx + 1)" }
    }
}

/// Extracts the trailing numeric suffix from an account name like
/// "claude-acc3" -> "3", "codex" -> nil, "gemini-1" -> "1".
private func providerSuffix(_ account: String) -> String? {
    let parts = account.split(whereSeparator: { !$0.isNumber })
    guard let last = parts.last, let _ = Int(last) else { return nil }
    return String(last)
}

// MARK: - Formatting helpers

/// Formats a utilization as "42%" or "—" when nil.
func fmtPct(_ p: Double?) -> String {
    guard let p = p else { return "—" }
    return "\(Int(p.rounded()))%"
}

/// Formats a reset timestamp as "H:MM" countdown (5-hour window max).
func fmtCountdown(_ ts: Double?) -> String {
    guard let ts = ts else { return "0:00" }
    let rem = max(0, ts - Date().timeIntervalSince1970)
    let h = Int(rem / 3600)
    let m = Int((rem.truncatingRemainder(dividingBy: 3600)) / 60)
    return "\(h):\(String(format: "%02d", m))"
}

/// Formats a reset timestamp compactly with minutes, e.g. "6/25 6:00p" or "6/26 10:30a".
func fmtDayHour(_ ts: Double?) -> String {
    guard let ts = ts, ts > 0 else { return "—" }
    let date = Date(timeIntervalSince1970: ts)
    let cal = Calendar.current
    let month = cal.component(.month,  from: date)
    let day   = cal.component(.day,    from: date)
    let h24   = cal.component(.hour,   from: date)
    let min   = cal.component(.minute, from: date)
    let h     = h24 == 0 ? 12 : h24 > 12 ? h24 - 12 : h24
    let ap    = h24 < 12 ? "a" : "p"
    // Space-pad the hour so "8:00a" and "11:59p" align in monospaced columns.
    return "\(month)/\(day) \(String(format: "%2d", h)):\(String(format: "%02d", min))\(ap)"
}

/// Returns hours until a future timestamp (rounded, minimum 0).
func hoursUntil(_ ts: Double?) -> Int? {
    guard let ts = ts else { return nil }
    return max(0, Int(((ts - Date().timeIntervalSince1970) / 3600).rounded()))
}

/// Formats extra usage credits remaining.
func fmtExtra(_ extra: ExtraUsage?) -> String {
    guard let extra = extra, extra.is_enabled == true else { return "—" }
    let limit = extra.monthly_limit ?? 0
    let used  = extra.used_credits  ?? 0
    if limit == 0 { return "\(Int((used / 100).rounded()))" }
    let rem = (limit - used) / 100
    if rem <= 0 { return rem < 0 ? "-\(Int(abs(rem)))" : "0" }
    if rem < 1  { return String(format: "%.2f", rem) }
    return "\(Int(rem.rounded()))"
}

// MARK: - Stale detection

func isStale(entry: AccountEntry) -> Bool {
    guard let lp = entry.last_pull_at else { return false }
    let now = Date().timeIntervalSince1970
    let inBackoff = (entry.back_off_until ?? 0) > now
    return (now - lp > 1800) && !inBackoff
}

// MARK: - Row opacity

func rowOpacity(fiveHourPct: Double?, weekPct: Double?) -> Double {
    if (weekPct ?? 0) >= 100  { return 0.28 }
    if (fiveHourPct ?? 0) >= 100 { return 0.6 }
    return 1.0
}
