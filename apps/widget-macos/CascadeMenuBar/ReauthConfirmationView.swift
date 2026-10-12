import AppKit

// Purpose: show the verbatim command a tap on an amber row would run, and launch it only on confirm.
// Inputs: the row's label and ref, plus confirm and cancel callbacks. Outputs: native labels and two buttons.
// Constraints: the shown text is exactly `cascade provider reauth <ref>`; nothing launches before confirm;
// the title shows the label with control and format characters removed.
final class ReauthConfirmationView: NSStackView {
    static func commandText(ref: String) -> String { "cascade provider reauth \(ref)" }

    /// The daemon's label without control, line-break or bidirectional-format characters, so it cannot add lines or
    /// reorder the title. The command line never uses the label.
    static func displayLabel(_ label: String) -> String {
        String(String.UnicodeScalarView(label.unicodeScalars.filter { scalar in
            switch scalar.properties.generalCategory {
            case .control, .format, .lineSeparator, .paragraphSeparator: return false
            default: return true
            }
        }))
    }

    private(set) var titleLabel: NSTextField
    private(set) var commandLabel: NSTextField
    private(set) var confirmButton: NSButton
    private(set) var cancelButton: NSButton
    private let onConfirm: () -> Void
    private let onCancel: () -> Void

    init(label: String, ref: String, onConfirm: @escaping () -> Void, onCancel: @escaping () -> Void) {
        self.onConfirm = onConfirm
        self.onCancel = onCancel
        titleLabel = NSTextField(labelWithString: "Sign in to \(Self.displayLabel(label))?")
        titleLabel.maximumNumberOfLines = 1
        titleLabel.lineBreakMode = .byTruncatingTail
        commandLabel = NSTextField(labelWithString: Self.commandText(ref: ref))
        confirmButton = NSButton(title: "Open Terminal and run", target: nil, action: nil)
        cancelButton = NSButton(title: "Cancel", target: nil, action: nil)
        super.init(frame: .zero)
        orientation = .vertical
        alignment = .leading
        spacing = 6
        titleLabel.font = .boldSystemFont(ofSize: 12)
        commandLabel.font = .monospacedSystemFont(ofSize: 12, weight: .regular)
        commandLabel.isSelectable = true
        confirmButton.target = self
        confirmButton.action = #selector(confirmed)
        cancelButton.target = self
        cancelButton.action = #selector(cancelled)
        confirmButton.setAccessibilityLabel("Open Terminal and run \(Self.commandText(ref: ref))")
        let buttons = NSStackView(views: [cancelButton, confirmButton])
        buttons.spacing = 8
        for view in [titleLabel, NSTextField(labelWithString: "This opens Terminal and runs:"), commandLabel, buttons] {
            addArrangedSubview(view)
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("Use init(label:ref:onConfirm:onCancel:)") }

    @objc private func confirmed() { onConfirm() }
    @objc private func cancelled() { onCancel() }
}

/// The reauthentication a row tap asked for, until it is confirmed or cancelled.
struct PendingReauth: Equatable {
    let ref: String
    let label: String
}

// Purpose: hold the pending confirmation and the inline alert across the popover's once-a-second rebuilds,
// and call the launcher exactly once per confirm. Inputs: a launcher and the latest rendered snapshot.
// Outputs: pending request and alert line. Constraints: a snapshot that no longer lists the pending ref as
// reauth_required drops the request (fail closed); a nil snapshot (daemon error) drops it too.
final class ReauthPresenter {
    static let shared = ReauthPresenter(launcher: ReauthLauncher())

    private let launcher: ReauthLaunching
    private(set) var pending: PendingReauth?
    private(set) var alert: String?
    private(set) var snapshot: WidgetSnapshot?

    init(launcher: ReauthLaunching) { self.launcher = launcher }

    /// Records the snapshot being rendered and drops a request whose row is no longer a live amber row.
    func observe(_ snapshot: WidgetSnapshot?) {
        self.snapshot = snapshot
        guard let pending else { return }
        if snapshot?.rows.contains(where: { $0.ref == pending.ref && $0.reauthRequired }) != true { self.pending = nil }
    }

    func request(_ row: WidgetRow) {
        pending = PendingReauth(ref: row.ref, label: row.label)
        alert = nil
    }

    func cancel() { pending = nil }

    /// Launches the pending request once, and only when it is the one the confirming view displayed: two surfaces
    /// share this presenter, so a view that is a tick behind must not launch a different ref than it shows.
    /// A second call finds nothing pending and does nothing.
    func confirm(expected: PendingReauth) {
        guard let request = pending, request == expected else { return }
        pending = nil
        do {
            try launcher.launch(ref: request.ref, snapshot: snapshot)
            alert = nil
        } catch {
            alert = (error as? LaunchError)?.errorDescription ?? LaunchError.terminalUnavailable.errorDescription
        }
    }
}
