import Foundation

struct GCICats: Codable {
    var rules: GCICat?
    var references: GCICat?
    var memory: GCICat?
    var skills: GCICat?
    var agents: GCICat?
    var spine: GCICat?
}

struct GCICat: Codable {
    var count: Int
    var mtime: Int?
}

struct GCIData: Codable {
    var rules: Int?
    var references: Int?
    var memory: Int?
    var instructions: Int?
    var agents: Int?
    var cats: GCICats?
}

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

struct Totals: Codable {
    var active: Int
    var ready: Int
    var planned: Int
    var review: Int
    var blocked: Int
    var inbox: Int
    var ideas: Int
}

struct WidgetData: Codable {
    var gci: GCIData?
    var projects: [ProjectData]?
    var totals: Totals?
    var active_project: String?
    var active_phase: String?
}

struct ClawCache: Codable {
    var _widget: WidgetData?
    var generated_at: Int?
    var _port: String?
}

extension ClawCache {
    static func load() -> ClawCache? {
        guard let containerURL = FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: "group.io.clawdash") else { return nil }
        let fileURL = containerURL.appendingPathComponent("cache.json")
        guard let data = try? Data(contentsOf: fileURL) else { return nil }
        return try? JSONDecoder().decode(ClawCache.self, from: data)
    }

    static var placeholder: ClawCache {
        ClawCache(
            _widget: WidgetData(
                gci: GCIData(rules: 13, references: 43, memory: 13, instructions: 20, agents: 5, cats: nil),
                projects: [ProjectData(label: "claw-dash", active: 2, ready: 1, planned: 3, review: 0, blocked: 0, done: 5, inbox: 1, ideas: 4, memory: 2)],
                totals: Totals(active: 2, ready: 1, planned: 3, review: 0, blocked: 0, inbox: 1, ideas: 4),
                active_project: "claw-dash",
                active_phase: "Phase 1: Core"
            ),
            generated_at: Int(Date().timeIntervalSince1970),
            _port: "3077"
        )
    }

    var ageSeconds: Int? {
        guard let g = generated_at else { return nil }
        return Int(Date().timeIntervalSince1970) - g
    }

    var ageString: String {
        guard let age = ageSeconds else { return "—" }
        if age < 5 { return "just now" }
        if age < 60 { return "\(age)s ago" }
        return "\(age / 60)m ago"
    }

    var isStale: Bool { (ageSeconds ?? 0) > 300 }
}
