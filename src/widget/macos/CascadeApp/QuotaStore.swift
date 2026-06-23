import Foundation

// MARK: - QuotaStore
//
// Purpose: Decode ~/.cascade/accounts/quota.json written by the cascade daemon.
//          Used by MenubarController to drive the Fleet popover table.
// Inputs:  ~/.cascade/accounts/quota.json (primary)
//          ~/.cascade/quota-store.json (fallback)
// Outputs: QuotaStore with per-account windows (5h, weekly) + reset countdowns.
// Constraints:
//   All fields Optional — display "—" gracefully when absent.
//   No crash on missing file (returns nil).
//   updated_at is ISO8601; fall back to file mtime if absent.
// SPORT: .opencode/phases/sport/ — QuotaStore model (Fleet popover)

struct QuotaWindow: Decodable {
    var used: Double?
    var limit: Double?
    var pct: Double?
    var reset_at: String?   // ISO8601
}

struct QuotaAccount: Decodable {
    var id: String?
    var family: String?        // "claude", "gfp", etc.
    var display: String?
    var reserved: Bool?
    var windows: QuotaWindows?
    var models: [String]?
}

struct QuotaWindows: Decodable {
    var fiveHour: QuotaWindow?
    var weekly: QuotaWindow?

    enum CodingKeys: String, CodingKey {
        case fiveHour = "5h"
        case weekly
    }
}

struct QuotaStore: Decodable {
    var updated_at: String?     // ISO8601
    var accounts: [QuotaAccount]?
}

// MARK: - Load

extension QuotaStore {
    /// Primary path: ~/.cascade/accounts/quota.json
    /// Fallback: ~/.cascade/quota-store.json
    static func load() -> QuotaStore? {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let candidates = [
            home.appendingPathComponent(".cascade/accounts/quota.json"),
            home.appendingPathComponent(".cascade/quota-store.json"),
        ]
        for url in candidates {
            if let data = try? Data(contentsOf: url),
               let store = try? JSONDecoder().decode(QuotaStore.self, from: data) {
                return store
            }
        }
        return nil
    }

    // MARK: - Age helpers

    /// Seconds since updated_at (ISO8601). nil when updated_at is absent.
    var ageSeconds: Int? {
        guard let s = updated_at else { return nil }
        let fmt = ISO8601DateFormatter()
        fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let date = fmt.date(from: s) ?? ISO8601DateFormatter().date(from: s)
        guard let d = date else { return nil }
        return Int(Date().timeIntervalSince(d))
    }

    /// "Ns ago" / "Nm ago" / "—"
    var ageString: String {
        guard let age = ageSeconds else { return "—" }
        if age < 5  { return "just now" }
        if age < 60 { return "\(age)s ago" }
        return "\(age / 60)m ago"
    }
}

// MARK: - Window display helpers

extension QuotaWindow {
    /// "80% · 2h14m"  or  "—"
    func displayCell() -> String {
        guard let pct = pct else { return "—" }
        let pctStr = "\(Int(pct.rounded()))%"
        if let countdown = resetCountdown() {
            return "\(pctStr) · \(countdown)"
        }
        return pctStr
    }

    /// Time remaining until reset_at. nil when absent or already past.
    func resetCountdown() -> String? {
        guard let s = reset_at else { return nil }
        let fmt = ISO8601DateFormatter()
        fmt.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let date = fmt.date(from: s) ?? ISO8601DateFormatter().date(from: s)
        guard let d = date else { return nil }
        let secs = Int(d.timeIntervalSinceNow)
        guard secs > 0 else { return nil }
        let h = secs / 3600
        let m = (secs % 3600) / 60
        if h > 0 { return "\(h)h\(String(format: "%02d", m))m" }
        return "\(m)m"
    }
}
