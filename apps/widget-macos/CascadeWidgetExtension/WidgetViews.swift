import SwiftUI
import WidgetKit

// Purpose: the small layout: active project and its task summary.
// Inputs: widget content. Outputs: verbatim text only (no localisation lookups, no images).
struct SmallWidgetView: View {
    let content: WidgetContent

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(verbatim: WidgetLiteral.title).font(.caption).foregroundStyle(.secondary)
            Text(verbatim: content.project).font(.headline).lineLimit(1)
            Text(verbatim: content.taskSummary).font(.caption).lineLimit(3)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

// Purpose: one provider row with both window percentages.
struct WidgetRowLine: View {
    let row: RowContent

    var body: some View {
        HStack(spacing: 6) {
            Text(verbatim: row.label).lineLimit(1)
            Spacer(minLength: 4)
            Text(verbatim: WidgetLiteral.fiveHour).foregroundStyle(.secondary)
            Text(verbatim: row.fiveHour)
            Text(verbatim: WidgetLiteral.sevenDay).foregroundStyle(.secondary)
            Text(verbatim: row.sevenDay)
        }
        .font(.caption.monospacedDigit())
    }
}

// Purpose: one project table line: label and task summary.
struct ProjectLine: View {
    let project: ProjectContent

    var body: some View {
        HStack {
            Text(verbatim: project.label).lineLimit(1)
            Spacer(minLength: 4)
            Text(verbatim: project.taskSummary).lineLimit(1)
        }
        .font(.caption)
    }
}

// Purpose: the medium layout: small content, attention badge and per-row window stats.
// Row lines are stored so every rendered line is a value of this view, not a closure result.
struct MediumWidgetView: View {
    let content: WidgetContent
    let rowLines: [WidgetRowLine]

    init(content: WidgetContent) {
        self.content = content
        rowLines = content.rows.map(WidgetRowLine.init)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                SmallWidgetView(content: content)
                VStack(spacing: 2) {
                    Text(verbatim: WidgetLiteral.attention).font(.caption2).foregroundStyle(.secondary)
                    Text(verbatim: content.attention).font(.title3.monospacedDigit())
                }
            }
            ForEach(rowLines.indices, id: \.self) { rowLines[$0] }
        }
    }
}

// Purpose: the large layout: medium content plus the project table (at most five rows).
struct LargeWidgetView: View {
    let content: WidgetContent
    let projectLines: [ProjectLine]

    init(content: WidgetContent) {
        self.content = content
        projectLines = content.projects.map(ProjectLine.init)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            MediumWidgetView(content: content)
            Divider()
            Text(verbatim: WidgetLiteral.projects).font(.caption).foregroundStyle(.secondary)
            ForEach(projectLines.indices, id: \.self) { projectLines[$0] }
            Spacer(minLength: 0)
        }
    }
}

// Purpose: map a widget family to its layout. Unsupported families fall back to the small layout.
enum WidgetLayouts {
    static func make(_ family: WidgetFamily, entry: CascadeEntry) -> any View {
        let content = WidgetContent(entry.snapshot)
        switch family {
        case .systemMedium: return MediumWidgetView(content: content)
        case .systemLarge: return LargeWidgetView(content: content)
        default: return SmallWidgetView(content: content)
        }
    }
}

// Purpose: the widget's entry view; reads the family from the environment.
struct CascadeEntryView: View {
    @Environment(\.widgetFamily) private var family
    let entry: CascadeEntry

    var body: some View {
        AnyView(WidgetLayouts.make(family, entry: entry))
            .containerBackground(.fill.tertiary, for: .widget)
    }
}
