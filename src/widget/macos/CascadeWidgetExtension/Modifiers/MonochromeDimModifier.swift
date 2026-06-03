import SwiftUI

// Purpose: Dim and desaturate the widget when the macOS desktop is inactive.
// Inputs:  content — the wrapped view
// Outputs: content unchanged (active) or desaturated + dimmed (inactive/accented)
// Constraints: Uses widgetRenderingMode environment value — the correct WidgetKit
//   API for responding to desktop state. NSWorkspace.isActive is not available
//   inside Widget extensions. widgetRenderingMode == .accented when the desktop is
//   locked or unfocused (macOS 14+).
// SPORT: MonochromeDimModifier — CascadeWidgetExtension/Modifiers/

struct MonochromeDimModifier: ViewModifier {
    @Environment(\.widgetRenderingMode) var renderingMode

    func body(content: Content) -> some View {
        if renderingMode == .accented {
            content
                .saturation(0.0)
                .opacity(0.5)
        } else {
            content
        }
    }
}

extension View {
    func monochromeDimWhenInactive() -> some View {
        modifier(MonochromeDimModifier())
    }
}
