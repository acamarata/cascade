import SwiftUI

// MARK: - MenuBarLabelView

/// The compact title displayed in the system menu bar.
///
/// Shows "X/N" — accounts with headroom out of total. Keeps the menu bar
/// footprint to a single word-width token. "CF" is the fallback before the
/// cache loads. The store is injected from ClawFleetApp so the label and the
/// desktop window share one poll loop.
struct MenuBarLabelView: View {
    @ObservedObject var store: CacheStore

    var body: some View {
        Text(store.summaryTitle)
            .font(.system(size: 11, weight: .medium, design: .monospaced))
    }
}
