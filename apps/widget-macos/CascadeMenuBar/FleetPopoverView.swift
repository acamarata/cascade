import AppKit

// Purpose: render feed state or a single error line. Unknown values retain the em-dash.
// Inputs: snapshot, error, clock and the reauthentication presenter. Outputs: populated native fleet table and
// summaries, the confirmation view for a tapped amber row, and one inline alert line for a refused launch.
// Constraints: a row tap only asks the presenter; the launcher runs after the confirmation view's confirm, and
// only for the request that view displayed.
final class FleetPopoverView: NSStackView {
    private(set) var rows: [FleetRowView] = []
    private(set) var labels: [NSTextField] = []
    private(set) var confirmation: ReauthConfirmationView?
    private(set) var alertLabel: NSTextField?
    private let snapshot: WidgetSnapshot?
    private let error: DaemonError?
    private let now: () -> Date
    private let presenter: ReauthPresenter

    init(snapshot: WidgetSnapshot?, error: DaemonError?, now: @escaping () -> Date = Date.init,
         presenter: ReauthPresenter = .shared) {
        self.snapshot = snapshot
        self.error = error
        self.now = now
        self.presenter = presenter
        super.init(frame: .zero)
        orientation = .vertical
        alignment = .leading
        spacing = 10
        edgeInsets = NSEdgeInsets(top: 16, left: 16, bottom: 16, right: 16)
        presenter.observe(error == nil ? snapshot : nil)
        build()
    }

    private func build() {
        rows = []
        labels = []
        confirmation = nil
        alertLabel = nil
        arrangedSubviews.forEach { $0.removeFromSuperview() }
        if let error { addLabel(error.localizedDescription); return }
        guard let snapshot else { addLabel("Waiting for daemon status."); return }
        addReauthViews()
        addLabel("Attention \(snapshot.attentionCount)")
        addLabel("Updated \(Formatting.updatedAgo(snapshot.generatedAt, now: now))")
        addLabel("Profile    5-hour usage / reset    7-day usage / reset    Age")
        for row in snapshot.rows {
            let view = FleetRowView(row: row, now: now)
            view.onReauth = { [weak self] in self?.reauthRequested($0) }
            rows.append(view)
            addArrangedSubview(view)
        }
        for node in snapshot.nodes { addLabel("\(node.id): \(node.presence)") }
        addLabel("Active jobs \(Formatting.count(snapshot.activeJobsCount))")
        for project in snapshot.projects ?? [] {
            addLabel("\(project.label): \(Formatting.text(project.taskSummary))")
        }
    }

    /// The inline alert line and the pending confirmation, when the presenter holds either.
    private func addReauthViews() {
        if let message = presenter.alert {
            let line = NSTextField(labelWithString: message)
            line.textColor = .systemRed
            line.maximumNumberOfLines = 1
            line.lineBreakMode = .byTruncatingTail
            alertLabel = line
            addArrangedSubview(line)
        }
        if let pending = presenter.pending {
            let view = ReauthConfirmationView(label: pending.label, ref: pending.ref,
                                              onConfirm: { [weak self] in self?.confirmReauth(pending) },
                                              onCancel: { [weak self] in self?.cancelReauth() })
            confirmation = view
            addArrangedSubview(view)
        }
    }

    private func reauthRequested(_ row: WidgetRow) {
        presenter.request(row)
        build()
    }

    private func confirmReauth(_ shown: PendingReauth) {
        presenter.confirm(expected: shown)
        build()
    }

    private func cancelReauth() {
        presenter.cancel()
        build()
    }

    private func addLabel(_ text: String) {
        let label = NSTextField(labelWithString: text)
        labels.append(label)
        addArrangedSubview(label)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("Use init(snapshot:error:now:presenter:)") }
}
