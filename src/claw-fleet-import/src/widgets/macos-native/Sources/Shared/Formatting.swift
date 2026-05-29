import SwiftUI

// MARK: - Color thresholds
// Mirrors the color() function from the Übersicht widget exactly.

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
// Mirrors the PROVIDER_PREFIX / localIdx+1 logic from the Übersicht widget.

/// True when an account looks like a cancelled / abandoned Claude subscription
/// and should be hidden from the widget entirely (vs. rendered as a row of
/// dashes). The signal is Claude-specific: no successful pull has ever landed
/// AND every usage slot is null. When the user resubscribes, the next poll
/// fills usage data and the row reappears automatically.
///
/// Other providers (codex, gemini, opencode) are always shown even when their
/// data is empty — the user wants those rows visible so missing data is
/// obvious rather than silently hidden.
func shouldAutoHide(_ e: AccountEntry) -> Bool {
    guard (e.provider ?? "claude") == "claude" else { return false }
    let u = e.usage
    let hasAnyUsage = (u?.five_hour != nil) || (u?.seven_day != nil) || (u?.seven_day_sonnet != nil)
    return e.last_pull_at == nil && !hasAnyUsage
}

/// Groups accounts by provider, drops cancelled-subscription rows, and
/// assigns display labels (A1, A2, C1, G1 …). Labels number the *remaining*
/// active accounts so removing A3 doesn't leave a gap (A1, A2 stay, A4 isn't
/// renumbered to A3 since labels track the underlying account-N config dir).
func labelledAccounts(from entries: [AccountEntry]) -> [(label: String, entry: AccountEntry)] {
    // "GP" (Gemini Pool) rather than "G1": the Gemini row represents the
    // openclaw-io GCP project that backs OpenCode's Gemini Free Pool (GFP) and
    // Pro Pool (GPP), not a single per-user account. The label intentionally
    // has no numeric suffix to signal "this is the pool" — see the per-account
    // semantic table in .github/docs/limits/README.md.
    let providerPrefix: [String: String] = [
        "claude":   "A",
        "codex":    "C",
        "gemini":   "GP",
        "opencode": "O",
    ]
    let providerOrder = ["claude", "codex", "gemini", "opencode"]

    var byProvider: [String: [AccountEntry]] = [:]
    for entry in entries where !shouldAutoHide(entry) {
        let p = entry.provider ?? "claude"
        byProvider[p, default: []].append(entry)
    }

    var result: [(label: String, entry: AccountEntry)] = []
    for provider in providerOrder {
        guard let group = byProvider[provider] else { continue }
        let prefix = providerPrefix[provider] ?? provider.prefix(1).uppercased()
        // Preserve the account-N -> A-N mapping so A1 stays A1 even when A3
        // is hidden. The account field looks like "claude-acc3"; pull the
        // numeric suffix when available, fall back to enumeration order.
        // Gemini is the exception: it's a pool concept, labelled "GP" with
        // no suffix regardless of how many backing API keys/projects exist.
        for (idx, entry) in group.enumerated() {
            let label: String
            if provider == "gemini" {
                label = prefix   // "GP" — pool, no per-account number
            } else {
                let suffix = providerSuffix(entry.account) ?? "\(idx + 1)"
                label = "\(prefix)\(suffix)"
            }
            result.append((label: label, entry: entry))
        }
    }
    return result
}

/// Extracts the trailing numeric suffix from an account name like
/// "claude-acc3" -> "3", "codex" -> nil, "gemini-1" -> "1".
private func providerSuffix(_ account: String) -> String? {
    let parts = account.split(whereSeparator: { !$0.isNumber })
    guard let last = parts.last, let _ = Int(last) else { return nil }
    return String(last)
}

// MARK: - Formatting helpers
// All functions port the Übersicht helpers precisely.

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

/// Formats a reset timestamp compactly as "5/23 10a" or "5/24  8p".
/// The hour field is right-padded to 3 characters so single-digit hours
/// align under two-digit ones in monospaced text (" 8p" vs "10p").
func fmtDayHour(_ ts: Double?) -> String {
    guard let ts = ts, ts > 0 else { return "—" }
    let date = Date(timeIntervalSince1970: ts)
    let cal = Calendar.current
    let month = cal.component(.month, from: date)
    let day   = cal.component(.day,   from: date)
    let h24   = cal.component(.hour,  from: date)
    let h     = h24 == 0 ? 12 : h24 > 12 ? h24 - 12 : h24
    let ap    = h24 < 12 ? "a" : "p"
    let hourStr = "\(h)\(ap)"
    let paddedHour = String(repeating: " ", count: max(0, 3 - hourStr.count)) + hourStr
    return "\(month)/\(day) \(paddedHour)"
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

/// An account's last-pull timestamp is considered stale when it exceeds 30 minutes
/// AND the account is not in an intentional back-off window. The back-off window
/// suppresses the stale marker because the refresh daemon is deliberately not
/// querying that account — not a failure mode.
func isStale(entry: AccountEntry) -> Bool {
    guard let lp = entry.last_pull_at else { return false }
    let now = Date().timeIntervalSince1970
    let inBackoff = (entry.back_off_until ?? 0) > now
    return (now - lp > 1800) && !inBackoff
}

// MARK: - Row opacity

/// Opacity for a data row. Weekly cap dims strongly (0.28); 5-hour cap dims lightly (0.6).
/// This mirrors the Übersicht widget's rowOpacity logic.
func rowOpacity(fiveHourPct: Double?, weekPct: Double?) -> Double {
    if (weekPct ?? 0) >= 100  { return 0.28 }
    if (fiveHourPct ?? 0) >= 100 { return 0.6 }
    return 1.0
}
