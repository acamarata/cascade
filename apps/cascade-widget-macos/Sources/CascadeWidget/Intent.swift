// Intent.swift — Cascade widget configuration intent
// Purpose: AppIntents-based configuration so users can choose which agent group
//          to highlight in the widget (All / Anthropic / Gemini / OpenAI).
// Inputs: AppIntents framework (macOS 13+)
// Outputs: ConfigurationAppIntent used by CascadeWidget TimelineProvider
// Constraints: Requires AppIntents — not available on macOS < 13.
// SPORT: apps/cascade-widget-macos — Intent

import AppIntents
import WidgetKit

// MARK: - Agent group enum

/// Which subset of agents the widget should display in the headline bar.
public enum AgentGroup: String, AppEnum {
    case all        = "All"
    case anthropic  = "Anthropic"
    case gemini     = "Gemini"
    case openai     = "OpenAI"

    public static var typeDisplayRepresentation = TypeDisplayRepresentation(name: "Agent Group")

    public static var caseDisplayRepresentations: [AgentGroup: DisplayRepresentation] = [
        .all:       "All Providers",
        .anthropic: "Anthropic (A1–A4)",
        .gemini:    "Gemini (G1–G4)",
        .openai:    "OpenAI (C1)"
    ]
}

// MARK: - Widget configuration intent

/// Interactive configuration intent exposed in the WidgetKit edit sheet.
/// Users can select which provider group to display and whether to show relay status.
public struct CascadeConfigurationIntent: WidgetConfigurationIntent {
    public static var title: LocalizedStringResource = "Cascade Configuration"
    public static var description = IntentDescription("Choose which agent group to display.")

    /// Which provider group to highlight.
    @Parameter(title: "Agent Group", default: .all)
    public var agentGroup: AgentGroup

    /// When true, the widget shows the Gemini relay liveness badge.
    @Parameter(title: "Show Relay Status", default: true)
    public var showRelay: Bool

    public init() {}

    public init(agentGroup: AgentGroup, showRelay: Bool) {
        self.agentGroup = agentGroup
        self.showRelay = showRelay
    }
}
