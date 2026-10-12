import AppKit

// Purpose: render feed state or a single error line. Unknown values retain the em-dash.
// Inputs: snapshot, error and clock. Outputs: populated native fleet table and summaries.
final class FleetPopoverView: NSStackView {
    private(set) var rows: [FleetRowView] = []
    private(set) var labels: [NSTextField] = []

    init(snapshot: WidgetSnapshot?, error: DaemonError?, now: () -> Date = Date.init) {
        super.init(frame: .zero)
        orientation = .vertical
        alignment = .leading
        spacing = 10
        edgeInsets = NSEdgeInsets(top: 16, left: 16, bottom: 16, right: 16)
        if let error { addLabel(error.localizedDescription); return }
        guard let snapshot else { addLabel("Waiting for daemon status."); return }
        addLabel("Attention \(snapshot.attentionCount)")
        addLabel("Updated \(Formatting.updatedAgo(snapshot.generatedAt, now: now))")
        addLabel("Profile    5-hour usage / reset    7-day usage / reset    Age")
        for row in snapshot.rows {
            let view = FleetRowView(row: row, now: now)
            rows.append(view)
            addArrangedSubview(view)
        }
        for node in snapshot.nodes { addLabel("\(node.id): \(node.presence)") }
        addLabel("Active jobs \(Formatting.count(snapshot.activeJobsCount))")
        for project in snapshot.projects ?? [] {
            addLabel("\(project.label): \(Formatting.text(project.taskSummary))")
        }
    }

    private func addLabel(_ text: String) {
        let label = NSTextField(labelWithString: text)
        labels.append(label)
        addArrangedSubview(label)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("Use init(snapshot:error:now:)") }
}
