// CascadeStatus.swift — Cascade daemon status model
// Purpose: Reads fleet status from the Cascade daemon via Unix domain socket IPC
// Inputs: Daemon socket at ~/Library/Application Support/cascade/daemon.sock (or shared group container)
// Outputs: CascadeStatus value, refreshed on timeline reload
// Constraints: WidgetKit extensions run in a sandboxed process; uses App Group container
//              for shared data written by the Tauri daemon. No direct socket access from widget.
// SPORT: apps/cascade-widget-macos — CascadeStatus model

import Foundation
import SwiftUI

// MARK: - Agent status

/// Live status snapshot for a single AI agent slot.
public struct AgentSlot: Identifiable, Codable, Sendable {
    public let id: String          // e.g. "A1", "A2", "G1"
    public let provider: String    // e.g. "Anthropic", "Gemini", "OpenAI"
    public let tier: String        // "T0" | "T1" | "T2" | "T3"
    public let usagePct: Double    // 0.0–1.0 utilisation fraction
    public let quotaLabel: String  // human-readable e.g. "32 % · resets 02:14"
    public let isActive: Bool      // true if slot is currently processing a request
    public let color: String       // hex color string for tier accent

    public init(
        id: String,
        provider: String,
        tier: String,
        usagePct: Double,
        quotaLabel: String,
        isActive: Bool,
        color: String
    ) {
        self.id = id
        self.provider = provider
        self.tier = tier
        self.usagePct = usagePct
        self.quotaLabel = quotaLabel
        self.isActive = isActive
        self.color = color
    }
}

// MARK: - Fleet status

/// Top-level status snapshot written by the Cascade daemon to a shared App Group JSON file.
public struct CascadeStatus: Codable, Sendable {
    /// ISO-8601 timestamp when the daemon last wrote this snapshot.
    public let updatedAt: Date
    /// Whether the daemon is reachable. False if the JSON is stale (>90 s old).
    public let daemonAlive: Bool
    /// All tracked agent slots.
    public let agents: [AgentSlot]
    /// Gemini proxy relay endpoint liveness.
    public let relayAlive: Bool
    /// Number of active in-flight requests across all agents.
    public let activeRequests: Int

    // MARK: Derived helpers

    /// Fraction of T2 (Sonnet) capacity currently in use, averaged across all T2 slots.
    public var t2UsagePct: Double {
        let t2 = agents.filter { $0.tier == "T2" }
        guard !t2.isEmpty else { return 0 }
        return t2.map(\.usagePct).reduce(0, +) / Double(t2.count)
    }

    /// Returns a placeholder status used when the daemon has not written a snapshot yet
    /// or the widget container has no data.
    public static var placeholder: CascadeStatus {
        CascadeStatus(
            updatedAt: Date(),
            daemonAlive: false,
            agents: [
                AgentSlot(id: "A1", provider: "Anthropic", tier: "T2",
                          usagePct: 0.0, quotaLabel: "—", isActive: false, color: "#E87040"),
                AgentSlot(id: "G1", provider: "Gemini",    tier: "T3",
                          usagePct: 0.0, quotaLabel: "—", isActive: false, color: "#4285F4"),
                AgentSlot(id: "C1", provider: "OpenAI",    tier: "T2",
                          usagePct: 0.0, quotaLabel: "—", isActive: false, color: "#10A37F")
            ],
            relayAlive: false,
            activeRequests: 0
        )
    }

    // MARK: - IPC reader

    /// App Group identifier shared between the main Cascade app (Tauri) and this widget extension.
    /// Must match entitlements on both targets.
    static let appGroup = "group.com.acamarata.cascade"

    /// File URL for the daemon-written status JSON in the shared App Group container.
    static var statusFileURL: URL? {
        FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: appGroup)?
            .appendingPathComponent("daemon-status.json")
    }

    /// Load current status from the shared App Group JSON file.
    /// Falls back to `.placeholder` if the file is missing, unreadable, or stale.
    public static func load() -> CascadeStatus {
        guard
            let url = statusFileURL,
            let data = try? Data(contentsOf: url)
        else { return .placeholder }

        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601

        guard let status = try? decoder.decode(CascadeStatus.self, from: data) else {
            return .placeholder
        }

        // Treat snapshot older than 90 seconds as daemon-dead.
        let age = Date().timeIntervalSince(status.updatedAt)
        if age > 90 {
            return CascadeStatus(
                updatedAt: status.updatedAt,
                daemonAlive: false,
                agents: status.agents,
                relayAlive: false,
                activeRequests: 0
            )
        }
        return status
    }
}
