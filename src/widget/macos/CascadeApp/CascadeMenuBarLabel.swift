import SwiftUI

// MARK: - CascadeMenuBarLabelView
// Ported from ClawFleet MenuBarLabel.swift.
// Shows "X/N" summary from CascadeStore.summaryTitle. Falls back to "CS".

struct CascadeMenuBarLabelView: View {
    @ObservedObject var store: CascadeStore

    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: "mountain.2.fill")
                .font(.system(size: 12, weight: .semibold))
            Text(store.summaryTitle)
                .font(.system(size: 11, weight: .medium, design: .monospaced))
        }
    }
}
