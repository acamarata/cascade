// CascadeWidgetImpl.h — Cascade Widget Provider
// Purpose:  Per-tile widget instance tracking. Holds the WidgetId, current size
//           preference, StatusCache poller, and last-known daemon state for a
//           single tile pinned to the Windows Widget Board.
// Inputs:   WidgetId (string) assigned by the Widget Board host;
//           WidgetContext from OnWidgetContextChanged to push Adaptive Card updates.
// Outputs:  Exposes WidgetId, CurrentSize for CascadeWidgetProvider routing.
//           Pushes Adaptive Card JSON via IWidgetContext::UpdateWidget() on every
//           30-second poll tick and on explicit Activate() calls.
// Constraints:
//   - One CascadeWidgetImpl per tile; the board may have multiple tiles of the
//     same widget type (CascadeStatus) each with a distinct WidgetId.
//   - CurrentSize is updated on every OnWidgetContextChanged callback.
//   - Implements IWidget and IWidgetOptions from Windows App SDK 1.5.
//   - StatusCache file is read with CreateFileW using FILE_SHARE_READ|FILE_SHARE_WRITE
//     so the daemon can keep the file open for writing while the widget reads it.
//   - PollTimer fires every 30 seconds via SetThreadpoolTimer on the thread pool;
//     the callback is NOT on the UI thread — UpdateWidget is COM-safe cross-thread.
//   - Malformed JSON (parse failure) retains the previous m_lastState; logs to
//     OutputDebugStringW. The widget never crashes on a corrupt cache file.
// SPORT: cascade/apps/cascade-widget-windows

#pragma once
#include "pch.h"
#include <threadpoolapiset.h>

namespace winrt::CascadeWidget::implementation
{
    // ── CascadeStatus ─────────────────────────────────────────────────────────
    //
    // Mirrors the TrayState schema written by the cascade daemon to
    // ~/.cascade/status-cache.json.
    //
    // Fields are kept as wide strings / ints so they can be embedded directly
    // into the Adaptive Card JSON template without re-conversion.
    struct CascadeStatus
    {
        // Sum of all values in the tier_counts JSON object.
        int tierCountsTotal{ 0 };

        // "fresh" if rag_freshness_secs < 300; otherwise "stale (Xs)".
        std::wstring ragFreshness{ L"unknown" };

        // active_agents integer from cache JSON.
        int activeAgents{ 0 };

        // inbox_unread integer from cache JSON.
        int inboxUnread{ 0 };

        // daemon_status string from cache JSON ("Running", "Stopped", etc.).
        std::wstring daemonStatus{ L"Unknown" };
    };

    // ── CascadeWidgetImpl ─────────────────────────────────────────────────────

    /// <summary>
    /// Tracks the state of a single Cascade Status widget tile, including the
    /// live StatusCache poller and last-known daemon state.
    ///
    /// The Widget Board calls OnWidgetContextChanged once per tile per event
    /// (create / resize / hide / delete). CascadeWidgetProvider creates one
    /// CascadeWidgetImpl per unique WidgetId seen in those callbacks and calls
    /// Activate() to start the 30-second poll timer.
    ///
    /// Thread safety: m_lastState is guarded by m_stateMutex. The thread-pool
    /// timer callback acquires the lock before updating state and pushing a card.
    /// </summary>
    struct CascadeWidgetImpl :
        implements<CascadeWidgetImpl,
                   Microsoft::Windows::Widgets::Providers::IWidget,
                   Microsoft::Windows::Widgets::Providers::IWidgetOptions>
    {
        explicit CascadeWidgetImpl(std::wstring widgetId);
        ~CascadeWidgetImpl();

        // ── IWidget ──────────────────────────────────────────────────────────

        /// <summary>The WidgetId assigned by the Windows Widget Board host.</summary>
        hstring Id() const noexcept { return _widgetId; }

        // ── IWidgetOptions ───────────────────────────────────────────────────

        /// <summary>The tile size currently requested by the Widget Board host.</summary>
        Microsoft::Windows::Widgets::Providers::WidgetSize CurrentSize() const noexcept
        {
            return _size;
        }

        /// <summary>Updates the tile size on resize events.</summary>
        void CurrentSize(
            Microsoft::Windows::Widgets::Providers::WidgetSize const& size) noexcept
        {
            _size = size;
        }

        // ── Lifecycle ────────────────────────────────────────────────────────

        /// <summary>
        /// Activates the widget: stores the WidgetContext, performs an immediate
        /// cache read + card push, then starts the 30-second poll timer.
        ///
        /// Called by CascadeWidgetProvider::OnWidgetContextChanged when
        /// ctx.IsActive() == true.
        /// </summary>
        void Activate(
            Microsoft::Windows::Widgets::Providers::WidgetContext const& ctx);

        /// <summary>
        /// Deactivates the widget: cancels the poll timer and clears the stored
        /// WidgetContext. Called when ctx.IsActive() == false.
        /// </summary>
        void Deactivate();

        // ── Cache polling (public for testability) ───────────────────────────

        /// <summary>
        /// Reads ~/.cascade/status-cache.json using CreateFileW with
        /// FILE_SHARE_READ|FILE_SHARE_WRITE (allows concurrent daemon writes),
        /// parses via Windows.Data.Json::JsonObject::TryParse(), updates
        /// m_lastState, then calls RenderAdaptiveCard().
        ///
        /// On any parse failure: logs to OutputDebugStringW and keeps the
        /// previous m_lastState unchanged (last-known-good behaviour).
        /// </summary>
        void ReadCacheFile();

    private:
        // ── Private helpers ──────────────────────────────────────────────────

        /// Loads cascade_widget_medium.json (or small variant based on _size),
        /// injects m_lastState fields, and calls WidgetManager::UpdateWidget().
        void RenderAdaptiveCard();

        /// Timer callback trampoline — bridges the C-style PTP_CALLBACK_INSTANCE
        /// to ReadCacheFile() on the owning CascadeWidgetImpl.
        static void CALLBACK TimerCallback(
            PTP_CALLBACK_INSTANCE instance,
            PVOID context,
            PTP_TIMER timer) noexcept;

        // ── State ────────────────────────────────────────────────────────────

        hstring _widgetId;
        Microsoft::Windows::Widgets::Providers::WidgetSize _size{
            Microsoft::Windows::Widgets::Providers::WidgetSize::Small };

        // Path to the status-cache.json file (expanded from %USERPROFILE%\.cascade).
        std::wstring m_cachePath;

        // Last successfully parsed daemon state. Guarded by m_stateMutex.
        CascadeStatus m_lastState;
        mutable std::mutex m_stateMutex;

        // Windows thread-pool timer. nullptr when inactive.
        PTP_TIMER m_timer{ nullptr };

        // The most recently provided WidgetContext (used in RenderAdaptiveCard).
        // Stored as optional<WidgetContext> — absent until Activate() is called.
        std::optional<Microsoft::Windows::Widgets::Providers::WidgetContext> m_widgetContext;
    };
}

