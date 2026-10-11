import Foundation

// Purpose: display feed values and ages. No quota or reset estimates are derived here.
// Inputs: optional scalars, daemon timestamps and injected clock. Outputs: display text.
enum Formatting {
    static func percent(_ value: Double?) -> String {
        guard let value else { return "—" }
        return String(format: "%.0f%%", value)
    }

    static func count(_ value: Int?) -> String {
        value.map(String.init) ?? "—"
    }

    static func text(_ value: String?) -> String { value ?? "—" }

    static func countdown(_ nanoseconds: Int64?) -> String {
        guard let nanoseconds else { return "—" }
        let minutes = max(0, nanoseconds / 60_000_000_000)
        return "\(minutes / 60)h\(minutes % 60)m"
    }

    static func updatedAgo(_ date: Date, now: () -> Date = Date.init) -> String {
        let seconds = max(0, Int(now().timeIntervalSince(date)))
        if seconds < 60 { return "\(seconds)s ago" }
        if seconds < 3600 { return "\(seconds / 60)m ago" }
        return "\(seconds / 3600)h ago"
    }
}
