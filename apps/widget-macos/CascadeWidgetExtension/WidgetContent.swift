import Foundation

// Purpose: the fixed UI text a widget may show beside values taken from the snapshot file.
// Inputs: none. Outputs: literal strings; `all` is the complete allowed set.
enum WidgetLiteral {
    static let dash = "—"
    static let title = "Cascade"
    static let attention = "Attention"
    static let fiveHour = "5h"
    static let sevenDay = "7d"
    static let projects = "Projects"
    static let all: Set<String> = [dash, title, attention, fiveHour, sevenDay, projects]
}

// Purpose: one provider row as display text: label verbatim, both window percentages or the em-dash.
struct RowContent: Equatable {
    let label: String
    let fiveHour: String
    let sevenDay: String

    init(_ row: WidgetRow) {
        label = row.label
        fiveHour = Formatting.percent(row.fiveHour?.utilizationPct)
        sevenDay = Formatting.percent(row.sevenDay?.utilizationPct)
    }
}

// Purpose: one project row as display text: label verbatim, task summary or the em-dash.
struct ProjectContent: Equatable {
    let label: String
    let taskSummary: String

    init(_ project: ProjectRow) {
        label = project.label
        taskSummary = Formatting.text(project.taskSummary)
    }
}

// Purpose: everything a widget family renders, derived only from the snapshot.
// Inputs: an optional snapshot (nil = no data). Outputs: display strings; no lookups, caches or defaults
// other than the em-dash. Constraints: medium shows at most `maxRows` rows, large at most `maxProjects` projects.
struct WidgetContent: Equatable {
    static let maxRows = 4
    static let maxProjects = 5

    let project: String
    let taskSummary: String
    let attention: String
    let rows: [RowContent]
    let projects: [ProjectContent]

    init(_ snapshot: WidgetSnapshot?) {
        let first = snapshot?.projects?.first
        project = first?.label ?? WidgetLiteral.dash
        taskSummary = Formatting.text(first?.taskSummary)
        attention = Formatting.count(snapshot?.attentionCount)
        rows = (snapshot?.rows ?? []).prefix(Self.maxRows).map(RowContent.init)
        projects = (snapshot?.projects ?? []).prefix(Self.maxProjects).map(ProjectContent.init)
    }

    var smallTexts: [String] { [WidgetLiteral.title, project, taskSummary] }

    var mediumTexts: [String] {
        smallTexts + [WidgetLiteral.attention, attention]
            + rows.flatMap { [$0.label, WidgetLiteral.fiveHour, $0.fiveHour, WidgetLiteral.sevenDay, $0.sevenDay] }
    }

    var largeTexts: [String] {
        mediumTexts + [WidgetLiteral.projects] + projects.flatMap { [$0.label, $0.taskSummary] }
    }
}
