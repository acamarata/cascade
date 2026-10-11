import Foundation

// Purpose: decode the daemon feed without substituting values or resolving labels.
// Inputs: received JSON. Outputs: typed snapshot. Constraints: null stays unknown.
struct WidgetSnapshot: Decodable {
    var rows: [WidgetRow]
    let attentionCount: Int
    var activeJobsCount: Int?
    let nodes: [NodePresenceSummary]
    let projects: [ProjectRow]?
    let generatedAt: Date
    let seq: UInt64

    static func decode(_ data: Data) throws -> WidgetSnapshot {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = formatter.date(from: text) { return date }
            formatter.formatOptions = [.withInternetDateTime]
            guard let date = formatter.date(from: text) else {
                throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "Invalid timestamp"))
            }
            return date
        }
        return try decoder.decode(Self.self, from: data)
    }
}

struct WidgetWindow: Decodable {
    let utilizationPct: Double?
    let resetsIn: Int64?
}

struct WidgetRow: Decodable {
    let ref: String
    var label: String
    let kind: String
    let fiveHour: WidgetWindow?
    let sevenDay: WidgetWindow?
    var state: String
    let reauthRequired: Bool
    let updatedAt: Date
}

struct NodePresenceSummary: Decodable {
    let id: String
    let presence: String
    let reachable: Bool
    let trustTier: String
}

struct ProjectRow: Decodable {
    let ref: String
    let label: String
    let taskSummary: String?
    let updatedAt: Date
}
