import SwiftUI

// MARK: - CascadeMenuBarLabelView
// Ported from ClawFleet MenuBarLabel.swift.
// Shows "X/N" summary from CascadeStore.summaryTitle. Falls back to "CS".

struct CascadeMenuBarLabelView: View {
    @ObservedObject var store: CascadeStore

    var body: some View {
        Text(store.summaryTitle)
            .font(.system(size: 11, weight: .medium, design: .monospaced))
    }
}
