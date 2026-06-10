// CascadeWidgetImpl.cpp — Cascade Widget Provider
// Purpose:  StatusCache file reader, 30-second ThreadPool poll timer, and
//           Adaptive Card renderer for a single Cascade widget tile.
// Inputs:   ~/.cascade/status-cache.json written by the cascaded daemon.
//           WidgetContext from the Widget Board host (via Activate()).
// Outputs:  Adaptive Card JSON pushed to the Widget Board via
//           WidgetManager::GetDefault().UpdateWidget() on every poll tick.
// Constraints:
//   - ReadCacheFile uses CreateFileW with GENERIC_READ + FILE_SHARE_READ|FILE_SHARE_WRITE
//     so the daemon can keep the file open for writing without causing ERROR_SHARING_VIOLATION.
//   - PollTimer uses SetThreadpoolTimer (not Sleep); the callback fires on the
//     Windows thread pool, not the UI thread. UpdateWidget is COM-safe cross-thread.
//   - Malformed or absent cache JSON keeps m_lastState unchanged (last-known-good).
//     The widget never crashes or goes blank on a corrupt file.
//   - RenderAdaptiveCard injects live state into the cascade_widget_medium.json
//     Adaptive Card template using Windows.Data.Json data binding.
// SPORT: cascade/apps/cascade-widget-windows

#include "pch.h"
#include "CascadeWidgetImpl.h"
#include <winrt/Windows.Data.Json.h>
#include <winrt/Windows.Foundation.h>
#include <shlobj.h>          // SHGetKnownFolderPath
#include <pathcch.h>         // PathCchCombine

using namespace winrt;
using namespace winrt::Microsoft::Windows::Widgets::Providers;
using namespace winrt::Windows::Data::Json;

namespace winrt::CascadeWidget::implementation
{
    // ── Constructor / Destructor ──────────────────────────────────────────────

    CascadeWidgetImpl::CascadeWidgetImpl(std::wstring widgetId)
        : _widgetId(std::move(widgetId))
    {
        // Expand ~/.cascade/status-cache.json to an absolute path.
        //
        // Why USERPROFILE and not SHGetKnownFolderPath(FOLDERID_Profile)?
        // Both resolve the same path; USERPROFILE avoids a COM round-trip and
        // is set unconditionally in all modern Windows environments.
        wchar_t expandedPath[MAX_PATH]{};
        DWORD result = ExpandEnvironmentStringsW(
            L"%USERPROFILE%\\.cascade\\status-cache.json",
            expandedPath,
            MAX_PATH);

        if (result == 0 || result > MAX_PATH)
        {
            // Fall back to a relative path if expansion fails (should not happen).
            OutputDebugStringW(L"[CascadeWidget] ExpandEnvironmentStrings failed — "
                               L"using fallback cache path.\n");
            m_cachePath = L".cascade\\status-cache.json";
        }
        else
        {
            m_cachePath = expandedPath;
        }
    }

    CascadeWidgetImpl::~CascadeWidgetImpl()
    {
        // Cancel and release the thread-pool timer before destruction.
        // WaitForThreadpoolTimerCallbacks ensures any in-flight callback
        // finishes before we free memory it may be accessing.
        Deactivate();
    }

    // ── Lifecycle ─────────────────────────────────────────────────────────────

    void CascadeWidgetImpl::Activate(WidgetContext const& ctx)
    {
        {
            std::lock_guard lock(m_stateMutex);
            m_widgetContext = ctx;
        }

        // Immediate first read so the tile shows live data without a 30-second wait.
        ReadCacheFile();

        // Create a thread-pool timer that fires every 30 seconds.
        //
        // SetThreadpoolTimer parameters:
        //   pftDueTime  — pointer to FILETIME specifying the first fire time.
        //                 A zero FILETIME means "fire immediately" (dueTime = 0).
        //   msPeriod    — repeat interval in milliseconds (30 000 ms = 30 s).
        //   msWindowLength — coalescing window. 0 = exact timing.
        if (m_timer == nullptr)
        {
            m_timer = CreateThreadpoolTimer(TimerCallback, this, nullptr);
            if (m_timer != nullptr)
            {
                // dueTime = 0 triggers the first callback immediately on the
                // thread pool (in addition to the synchronous ReadCacheFile() call
                // above). Subsequent calls fire every msPeriod milliseconds.
                FILETIME dueTime{};
                SetThreadpoolTimer(m_timer, &dueTime, /*msPeriod*/ 30000, 0);
            }
            else
            {
                OutputDebugStringW(
                    L"[CascadeWidget] CreateThreadpoolTimer failed — "
                    L"live polling disabled.\n");
            }
        }
    }

    void CascadeWidgetImpl::Deactivate()
    {
        if (m_timer != nullptr)
        {
            // Cancel any future callbacks.
            SetThreadpoolTimer(m_timer, nullptr, 0, 0);

            // Wait for any in-progress callback to finish before releasing state
            // that the callback might be accessing (m_lastState, m_widgetContext).
            //
            // fCancelPendingCallbacks = TRUE: cancel enqueued-but-not-yet-running
            // callbacks; only wait for already-running ones.
            WaitForThreadpoolTimerCallbacks(m_timer, TRUE);

            CloseThreadpoolTimer(m_timer);
            m_timer = nullptr;
        }

        std::lock_guard lock(m_stateMutex);
        m_widgetContext.reset();
    }

    // ── Cache file reader ─────────────────────────────────────────────────────

    void CascadeWidgetImpl::ReadCacheFile()
    {
        // Open the status-cache.json with FILE_SHARE_READ|FILE_SHARE_WRITE so
        // the cascaded daemon can continue writing to the file while we read it.
        //
        // Why CreateFileW instead of std::ifstream?
        //   - std::ifstream uses GENERIC_READ without FILE_SHARE_WRITE, which
        //     causes ERROR_SHARING_VIOLATION if the daemon holds the file open.
        //   - CreateFileW gives explicit share-mode control.
        HANDLE hFile = CreateFileW(
            m_cachePath.c_str(),
            GENERIC_READ,
            FILE_SHARE_READ | FILE_SHARE_WRITE,
            nullptr,
            OPEN_EXISTING,
            FILE_ATTRIBUTE_NORMAL,
            nullptr);

        if (hFile == INVALID_HANDLE_VALUE)
        {
            // File absent or access denied — keep last-known-good state.
            DWORD err = GetLastError();
            if (err != ERROR_FILE_NOT_FOUND)
            {
                wchar_t msg[128]{};
                swprintf_s(msg,
                    L"[CascadeWidget] ReadCacheFile: CreateFileW failed (err=%lu).\n",
                    err);
                OutputDebugStringW(msg);
            }
            return;
        }

        // Scope the HANDLE in a lambda-on-exit guard using WIL.
        wil::unique_hfile fileGuard{ hFile };

        // Read the entire file into a std::string.
        // Status-cache.json is typically < 1 KB; read it in one shot.
        LARGE_INTEGER fileSize{};
        if (!GetFileSizeEx(hFile, &fileSize) || fileSize.QuadPart == 0)
        {
            return; // Empty or unreadable — keep last-known-good.
        }

        // Guard against absurdly large files (> 1 MB = not a valid status cache).
        if (fileSize.QuadPart > 1024 * 1024)
        {
            OutputDebugStringW(
                L"[CascadeWidget] ReadCacheFile: cache file unexpectedly large — skipping.\n");
            return;
        }

        std::string jsonUtf8(static_cast<size_t>(fileSize.QuadPart), '\0');
        DWORD bytesRead = 0;
        if (!ReadFile(hFile,
                      jsonUtf8.data(),
                      static_cast<DWORD>(fileSize.QuadPart),
                      &bytesRead,
                      nullptr)
            || bytesRead == 0)
        {
            OutputDebugStringW(L"[CascadeWidget] ReadCacheFile: ReadFile failed.\n");
            return;
        }
        jsonUtf8.resize(bytesRead);

        // Convert UTF-8 → UTF-16 for Windows.Data.Json.
        int wLen = MultiByteToWideChar(CP_UTF8, 0,
                                       jsonUtf8.c_str(),
                                       static_cast<int>(jsonUtf8.size()),
                                       nullptr, 0);
        if (wLen <= 0)
        {
            OutputDebugStringW(L"[CascadeWidget] ReadCacheFile: UTF-8 decode failed.\n");
            return;
        }
        std::wstring jsonWide(static_cast<size_t>(wLen), L'\0');
        MultiByteToWideChar(CP_UTF8, 0,
                            jsonUtf8.c_str(),
                            static_cast<int>(jsonUtf8.size()),
                            jsonWide.data(), wLen);

        // Parse JSON using Windows.Data.Json::JsonObject::TryParse().
        //
        // Why Windows.Data.Json and not nlohmann/json?
        //   - Windows.Data.Json is a built-in WinRT API (no extra dependency).
        //   - TryParse returns false on malformed input without throwing, which
        //     makes the last-known-good fallback straightforward.
        JsonObject root{ nullptr };
        if (!JsonObject::TryParse(hstring{ jsonWide }, root))
        {
            OutputDebugStringW(
                L"[CascadeWidget] ReadCacheFile: JSON parse failed — "
                L"retaining last-known-good state.\n");
            return; // Keep m_lastState unchanged.
        }

        // ── Map JSON fields to CascadeStatus ─────────────────────────────────

        CascadeStatus newState;

        // tier_counts: object of int values per tier (GCI, PCI, PPC, PRC, …).
        // Sum all values for tierCountsTotal.
        if (root.HasKey(L"tier_counts"))
        {
            auto tierCounts = root.GetNamedObject(L"tier_counts");
            for (auto pair : tierCounts)
            {
                if (pair.Value().ValueType() == JsonValueType::Number)
                {
                    newState.tierCountsTotal +=
                        static_cast<int>(pair.Value().GetNumber());
                }
            }
        }

        // rag_freshness_secs: integer seconds since last RAG index update.
        if (root.HasKey(L"rag_freshness_secs"))
        {
            double freshnessSecs =
                root.GetNamedNumber(L"rag_freshness_secs", 9999.0);
            if (freshnessSecs < 300.0)
            {
                newState.ragFreshness = L"fresh";
            }
            else
            {
                // Format as "stale (Xs)" where X is the integer seconds value.
                wchar_t buf[64]{};
                swprintf_s(buf, L"stale (%ds)",
                           static_cast<int>(freshnessSecs));
                newState.ragFreshness = buf;
            }
        }

        // active_agents: integer count of currently running AI agents.
        newState.activeAgents =
            static_cast<int>(root.GetNamedNumber(L"active_agents", 0.0));

        // inbox_unread: integer count of unprocessed inbox messages.
        newState.inboxUnread =
            static_cast<int>(root.GetNamedNumber(L"inbox_unread", 0.0));

        // daemon_status: string ("Running", "Stopped", "Error", etc.).
        if (root.HasKey(L"daemon_status"))
        {
            newState.daemonStatus =
                std::wstring(root.GetNamedString(L"daemon_status"));
        }

        // Commit the new state under lock and trigger a card update.
        {
            std::lock_guard lock(m_stateMutex);
            m_lastState = std::move(newState);
        }

        RenderAdaptiveCard();
    }

    // ── Adaptive Card renderer ────────────────────────────────────────────────

    void CascadeWidgetImpl::RenderAdaptiveCard()
    {
        // Snapshot state under lock before building the card JSON.
        CascadeStatus snapshot;
        std::optional<WidgetContext> ctx;
        {
            std::lock_guard lock(m_stateMutex);
            snapshot = m_lastState;
            ctx = m_widgetContext;
        }

        if (!ctx.has_value())
        {
            // Widget not yet active — nothing to push.
            return;
        }

        // Build the data JSON that the Adaptive Card template's ${...} bindings
        // will be substituted into.
        //
        // Template bindings used in cascade_widget_medium.json:
        //   ${daemon_status}        — string
        //   ${tier_counts_total}    — number (cast from int)
        //   ${active_agents}        — number
        //   ${inbox_unread}         — number
        //   ${rag_freshness}        — string
        //   ${tier_counts.GCI/PCI/PPC/PRC} — numbers (set to 0 here; per-tier
        //                                    breakdown is a P3 enhancement)
        wchar_t dataJson[512]{};
        swprintf_s(dataJson,
            L"{"
            L"\"daemon_status\":\"%s\","
            L"\"tier_counts_total\":%d,"
            L"\"active_agents\":%d,"
            L"\"inbox_unread\":%d,"
            L"\"rag_freshness\":\"%s\","
            L"\"tier_counts\":{"
            L"  \"GCI\":0,\"PCI\":0,\"PPC\":0,\"PRC\":0"
            L"}"
            L"}",
            snapshot.daemonStatus.c_str(),
            snapshot.tierCountsTotal,
            snapshot.activeAgents,
            snapshot.inboxUnread,
            snapshot.ragFreshness.c_str());

        // Load the Adaptive Card template JSON from the assets directory.
        //
        // The template file is packaged alongside the widget binary in the
        // app package Assets folder. For the P2 implementation we embed the
        // medium template as a constant string to avoid a file-system dependency
        // at run time; the full template is in assets/cascade_widget_medium.json.
        //
        // Why embed here? At P2, the widget binary and assets live in the build
        // output. The template is small (<2 KB) and stable. Embedding it avoids
        // a runtime path-resolution step and makes the widget self-contained.
        // P3 can add a file watcher if hot-reload of templates is desired.
        //
        // The template uses Adaptive Card Template Language ${...} syntax; the
        // Widget Board host substitutes dataJson at render time.
        constexpr std::wstring_view kMediumTemplate = LR"({
  "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
  "type": "AdaptiveCard",
  "version": "1.5",
  "body": [
    {
      "type": "ColumnSet",
      "columns": [
        {
          "type": "Column",
          "width": "stretch",
          "items": [
            {
              "type": "TextBlock",
              "text": "Cascade",
              "weight": "Bolder",
              "size": "Small",
              "wrap": false
            },
            {
              "type": "TextBlock",
              "text": "${daemon_status}",
              "color": "${if(daemon_status == 'Running', 'Good', 'Warning')}",
              "size": "Small",
              "wrap": false
            }
          ]
        },
        {
          "type": "Column",
          "width": "auto",
          "horizontalAlignment": "Right",
          "items": [
            {
              "type": "TextBlock",
              "text": "${string(tier_counts_total)}",
              "size": "ExtraLarge",
              "weight": "Bolder",
              "horizontalAlignment": "Right",
              "wrap": false
            },
            {
              "type": "TextBlock",
              "text": "rules",
              "size": "Small",
              "color": "Accent",
              "horizontalAlignment": "Right",
              "wrap": false
            }
          ]
        }
      ]
    },
    {
      "type": "ColumnSet",
      "spacing": "Small",
      "columns": [
        {
          "type": "Column",
          "width": "stretch",
          "items": [
            {
              "type": "TextBlock",
              "text": "${string(active_agents)} agents",
              "size": "Small",
              "wrap": false
            }
          ]
        },
        {
          "type": "Column",
          "width": "auto",
          "items": [
            {
              "type": "TextBlock",
              "text": "${if(inbox_unread > 0, string(inbox_unread) + ' unread', '')}",
              "size": "Small",
              "color": "${if(inbox_unread > 0, 'Attention', 'Default')}",
              "horizontalAlignment": "Right",
              "wrap": false
            }
          ]
        }
      ]
    },
    {
      "type": "FactSet",
      "spacing": "Small",
      "facts": [
        { "title": "GCI", "value": "${string(tier_counts.GCI)}" },
        { "title": "PCI", "value": "${string(tier_counts.PCI)}" },
        { "title": "PPC", "value": "${string(tier_counts.PPC)}" },
        { "title": "PRC", "value": "${string(tier_counts.PRC)}" }
      ]
    },
    {
      "type": "TextBlock",
      "text": "${rag_freshness}",
      "size": "Small",
      "isSubtle": true,
      "wrap": false,
      "spacing": "Small"
    }
  ]
})";

        WidgetUpdateRequestOptions opts{ ctx->Id() };
        opts.Template(hstring{ kMediumTemplate });
        opts.Data(hstring{ dataJson });
        opts.CustomState(L"");

        WidgetManager::GetDefault().UpdateWidget(opts);
    }

    // ── Thread-pool timer callback ────────────────────────────────────────────

    // static
    void CALLBACK CascadeWidgetImpl::TimerCallback(
        PTP_CALLBACK_INSTANCE /*instance*/,
        PVOID context,
        PTP_TIMER /*timer*/) noexcept
    {
        // The context pointer is the owning CascadeWidgetImpl*.
        // The timer is cancelled in Deactivate() and we call
        // WaitForThreadpoolTimerCallbacks before destroying the instance,
        // so `self` is always valid here.
        auto* self = static_cast<CascadeWidgetImpl*>(context);
        self->ReadCacheFile();
    }

} // namespace winrt::CascadeWidget::implementation

