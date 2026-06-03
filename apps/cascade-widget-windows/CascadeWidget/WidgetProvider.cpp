// WidgetProvider.cpp — Cascade Widget Provider
// Purpose:  IWidgetProvider implementation. Routes Widget Board callbacks to
//           per-tile CascadeWidgetImpl instances and pushes Adaptive Card payloads.
// Inputs:   Widget Board COM callbacks (OnWidgetContextChanged, OnActionInvoked,
//           OnWidgetCustomizationRequested).
// Outputs:  IWidgetContext::UpdateWidget() calls with Adaptive Card JSON payloads.
// Constraints:
//   - PushStatusCard returns a hardcoded stub Adaptive Card at the P2 scaffold
//     stage. Live data is wired in T-P2-E06-09.
//   - _mutex guards _widgets for all three callback entry points.
// SPORT: cascade/apps/cascade-widget-windows

#include "pch.h"
#include "WidgetProvider.h"

using namespace winrt;
using namespace winrt::Microsoft::Windows::Widgets::Providers;

namespace winrt::CascadeWidget::implementation
{
    // ── IWidgetProvider::OnWidgetContextChanged ───────────────────────────────

    void CascadeWidgetProvider::OnWidgetContextChanged(
        WidgetContextChangedArgs const& args)
    {
        auto ctx = args.WidgetContext();
        auto widgetId = ctx.Id();

        std::lock_guard lock(_mutex);

        // Deleted / hidden tiles: deactivate and remove from tracking.
        if (ctx.IsActive() == false)
        {
            auto it = _widgets.find(widgetId);
            if (it != _widgets.end())
            {
                it->second.Deactivate();
                _widgets.erase(it);
            }
            return;
        }

        // Ensure an impl entry exists for this tile.
        if (!_widgets.contains(widgetId))
        {
            _widgets.emplace(widgetId, CascadeWidgetImpl{ std::wstring(widgetId) });
        }

        auto& impl = _widgets.at(widgetId);
        impl.CurrentSize(ctx.Size());

        // Activate the impl: stores the context, fires an immediate ReadCacheFile,
        // and starts the 30-second poll timer.
        impl.Activate(ctx);
    }

    // ── IWidgetProvider::OnActionInvoked ─────────────────────────────────────

    void CascadeWidgetProvider::OnActionInvoked(
        WidgetActionInvokedArgs const& args)
    {
        // Handle the "Refresh" action verb if the user taps a refresh button.
        // All other actions are ignored at the P2 implementation level.
        auto widgetId = args.WidgetContext().Id();

        std::lock_guard lock(_mutex);
        auto it = _widgets.find(widgetId);
        if (it != _widgets.end())
        {
            it->second.ReadCacheFile();
        }
    }

    // ── IWidgetProvider::OnWidgetCustomizationRequested ───────────────────────

    void CascadeWidgetProvider::OnWidgetCustomizationRequested(
        WidgetCustomizationRequestedArgs const& /* args */)
    {
        // P2 implementation: no customisation panel. Reserved for P3.
    }

    // ── Private: PushStatusCard ───────────────────────────────────────────────

    void CascadeWidgetProvider::PushStatusCard(WidgetContext const& ctx) const
    {
        // Forwarding stub — live data is now served by CascadeWidgetImpl::RenderAdaptiveCard()
        // via the Activate() path in OnWidgetContextChanged.
        // This method is retained for backward compatibility with callers that may
        // invoke it directly; in normal operation Activate() calls RenderAdaptiveCard()
        // which bypasses this method.
        (void)ctx;
    }

} // namespace winrt::CascadeWidget::implementation
