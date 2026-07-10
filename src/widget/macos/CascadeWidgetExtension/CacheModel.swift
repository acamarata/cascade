import Foundation

// MARK: - P2 Cascade Cache Schema
//
// Purpose: Decode cache.json written by the cascaded daemon into widget-ready structs.
// Inputs:  cache.json at the shared App Group container (group.io.cascade)
// Outputs: CascadeCache value with optional _widget WidgetData payload
// Constraints: All fields Optional — widget must render gracefully if fields are absent
//              or cache is missing. isStale threshold is 120s (refresh cadence of daemon).
// SPORT: .claude/docs/MASTER-APPS.md — widget cache schema row (CacheModel, P2)

/// Per-tier instruction-cascade statistics.
/// Matches the "gci"/"asi"/"ppi" objects inside cache.json `_widget.cascade_tiers`.
struct CascadeTierStats: Codable {
    var rules: Int
    var references: Int
    var memory: Int
    var skills: Int
    var agents: Int
}

/// Container for the three cascade tiers (GCI, ASI, PPI).
struct CascadeTiers: Codable {
    var gci: CascadeTierStats?
    var asi: CascadeTierStats?
    var ppi: CascadeTierStats?
}

/// Per-project task/work-state summary.
struct ProjectData: Codable {
    var label: String
    var active: Int
    var ready: Int
    var planned: Int
    var review: Int
    var blocked: Int
    var done: Int
    var inbox: Int
    var ideas: Int
    var memory: Int
}

/// Aggregated totals across all tracked projects.
struct Totals: Codable {
    var active: Int
    var ready: Int
    var planned: Int
    var review: Int
    var blocked: Int
    var inbox: Int
    var ideas: Int
}

/// Top-level widget payload written by the cascaded daemon under the `_widget` key.
struct WidgetData: Codable {
    var cascade_tiers: CascadeTiers?
    var projects: [ProjectData]?
    var totals: Totals?
    var active_project: String?
    var active_phase: String?
    var inbox_unread: Int?
    var ideas_count: Int?
}

/// Root cache.json structure written by the cascaded daemon.
struct CascadeCache: Codable {
    var _widget: WidgetData?
    var generated_at: Int?
    var daemon_version: String?
}

// MARK: - Load, Placeholder & Staleness

extension CascadeCache {
    /// Load cache.json from the shared App Group container.
    /// Returns nil when the container or file is unavailable (widget shows placeholder).
    static func load() -> CascadeCache? {
        guard let containerURL = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: "group.io.cascade") else { return nil }
        let fileURL = containerURL.appendingPathComponent("cache.json")
        guard let data = try? Data(contentsOf: fileURL) else { return nil }
        return try? JSONDecoder().decode(CascadeCache.self, from: data)
    }

    /// Honest placeholder used when no cache.json is available.
    static var placeholder: CascadeCache {
        CascadeCache(
            _widget: nil,
            generated_at: nil,
            daemon_version: nil
        )
    }

    var hasWidgetData: Bool {
        _widget != nil
    }

    /// Seconds elapsed since the daemon last wrote cache.json.
    var ageSeconds: Int? {
        guard let g = generated_at else { return nil }
        return Int(Date().timeIntervalSince1970) - g
    }

    /// Human-readable age string for display in the widget.
    var ageString: String {
        guard let age = ageSeconds else { return "—" }
        if age < 5 { return "just now" }
        if age < 60 { return "\(age)s ago" }
        return "\(age / 60)m ago"
    }

    /// True when the cache is older than 120 seconds — the daemon refresh cadence.
    /// 120s chosen to match the cascaded polling interval; anything older suggests
    /// the daemon has stopped or the App Group write is failing.
    var isStale: Bool { (ageSeconds ?? 0) > 120 }
}
