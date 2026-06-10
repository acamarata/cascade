// WidgetProvider.h — Cascade Widget Provider
// Purpose:  Declaration of CascadeWidgetProvider, the COM-activatable widget
//           provider registered with the Windows Widget Board host.
// Inputs:   Widget Board COM activation; WidgetContextChangedArgs / action events.
// Outputs:  IWidgetProvider interface implementation; creates/destroys per-tile
//           CascadeWidgetImpl instances.
// Constraints:
//   - Implements IWidgetProvider from Windows App SDK 1.5.
//   - Thread-safe: OnWidgetContextChanged may be called from any thread; guard
//     the _widgets map with _mutex.
//   - The provider is a singleton per process — WidgetManager registers one
//     class factory and the Widget Board calls it once per process lifetime.
// SPORT: cascade/apps/cascade-widget-windows

#pragma once
#include "pch.h"
#include "CascadeWidgetImpl.h"
#include "winrt/CascadeWidget.h"

namespace winrt::CascadeWidget::implementation
{
    /// <summary>
    /// COM-activatable widget provider served to the Windows Widget Board.
    ///
    /// Lifecycle: the Widget Board calls CoCreateInstance(CLSID_CascadeWidgetProvider)
    /// once to obtain this object, then calls OnWidgetContextChanged for each tile
    /// the user has pinned to the board.
    ///
    /// Thread safety: all public methods acquire _mutex before accessing _widgets.
    /// </summary>
    struct CascadeWidgetProvider :
        implements<CascadeWidgetProvider,
                   Microsoft::Windows::Widgets::Providers::IWidgetProvider>
    {
        // ── IWidgetProvider ──────────────────────────────────────────────────

        /// <summary>
        /// Called when a widget tile is created, resized, hidden, or destroyed.
        /// The provider must push a fresh Adaptive Card payload for every
        /// non-deleted context change.
        /// </summary>
        void OnWidgetContextChanged(
            Microsoft::Windows::Widgets::Providers::WidgetContextChangedArgs const& args);

        /// <summary>
        /// Called when the user clicks a button declared in the Adaptive Card
        /// template (e.g. a "Refresh" button). The action JSON is in args.Action.
        /// </summary>
        void OnActionInvoked(
            Microsoft::Windows::Widgets::Providers::WidgetActionInvokedArgs const& args);

        /// <summary>
        /// Called when the user opens the widget's customisation panel.
        /// Push a settings Adaptive Card here if customisation is supported.
        /// For the P2 scaffold this is a no-op.
        /// </summary>
        void OnWidgetCustomizationRequested(
            Microsoft::Windows::Widgets::Providers::WidgetCustomizationRequestedArgs const& args);

    private:
        // ── Private helpers ──────────────────────────────────────────────────

        /// Push the current status card payload to the given widget context.
        void PushStatusCard(
            Microsoft::Windows::Widgets::Providers::WidgetContext const& ctx) const;

        // ── State ────────────────────────────────────────────────────────────

        /// Per-tile widget instances, keyed by WidgetId.
        std::unordered_map<hstring, CascadeWidgetImpl> _widgets;

        /// Guards _widgets for concurrent Widget Board callbacks.
        mutable std::mutex _mutex;
    };
}

// Factory glue — exposes CascadeWidgetProvider::make() via the winrt projection.
namespace winrt::CascadeWidget::factory_implementation
{
    struct CascadeWidgetProvider :
        implements<CascadeWidgetProvider,
                   winrt::Windows::Foundation::IActivationFactory,
                   implementation::CascadeWidgetProvider>
    {};
}
