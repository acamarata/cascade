import AppKit

// Purpose: render one feed row verbatim with both windows, age and reauthentication affordance.
// Inputs: a row and injected clock. Outputs: native labels and an optional amber tap target whose tap calls onReauth.
// Constraints: only a row with reauthRequired has a tap target; the tap launches nothing by itself.
final class FleetRowView: NSStackView {
    private(set) var labels: [NSTextField] = []
    private(set) var reauthButton: NSButton?
    private let row: WidgetRow
    var onReauth: ((WidgetRow) -> Void)?

    init(row: WidgetRow, now: () -> Date = Date.init) {
        self.row = row
        super.init(frame: .zero)
        orientation = .horizontal
        alignment = .centerY
        spacing = 12
        let values = [row.label, Formatting.percent(row.fiveHour?.utilizationPct),
                      Formatting.countdown(row.fiveHour?.resetsIn), Formatting.percent(row.sevenDay?.utilizationPct),
                      Formatting.countdown(row.sevenDay?.resetsIn), Formatting.updatedAgo(row.updatedAt, now: now)]
        for value in values {
            let label = NSTextField(labelWithString: value)
            label.font = .monospacedDigitSystemFont(ofSize: 12, weight: .regular)
            if row.reauthRequired { label.textColor = .systemOrange }
            labels.append(label)
            addArrangedSubview(label)
        }
        if row.reauthRequired {
            let button = NSButton(title: "Sign in", target: self, action: #selector(requestReauthentication))
            button.contentTintColor = .systemOrange
            button.setAccessibilityLabel("Sign in to \(row.label)")
            reauthButton = button
            addArrangedSubview(button)
        }
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("Use init(row:now:)") }

    @objc private func requestReauthentication() {
        onReauth?(row)
    }
}
