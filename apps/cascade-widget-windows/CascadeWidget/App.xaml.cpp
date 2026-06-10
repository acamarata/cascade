// App.xaml.cpp — Cascade Widget Provider
// Purpose:  Application entry point. CoInitialises the COM apartment, registers
//           CascadeWidgetProvider as a COM out-of-process server with the Windows
//           Widget Board host (WidgetManager), then runs the message loop so the
//           process stays alive for COM activation requests.
// Inputs:   n/a — activated by the Widget Board via COM.
// Outputs:  Registers the widget provider CLSID; responds to COM activation
//           requests from the Widget Board host process.
// Constraints:
//   - CoInitialize(nullptr) initialises an STA (single-threaded apartment).
//     WinUI 3 requires STA; multi-threaded apartment is not supported here.
//   - The message loop (GetMessage / TranslateMessage / DispatchMessage) keeps
//     the process alive after COM server registration so the Widget Board can
//     call CoCreateInstance at any time.
//   - The process exits when the message loop receives WM_QUIT, which the
//     Widget Board sends when all widget instances are removed from the board.
// SPORT: cascade/apps/cascade-widget-windows

#include "pch.h"
#include "WidgetProvider.h"
#include "winrt/CascadeWidget.h"

using namespace winrt;
using namespace winrt::Microsoft::Windows::Widgets::Providers;

/// <summary>
/// Windows entry point for the packaged WinUI 3 widget provider.
///
/// Sequence:
///   1. CoInitialize — STA (WinUI 3 requirement).
///   2. Register CascadeWidgetProvider as the COM out-of-process server for
///      CLSID_CascadeWidgetProvider (matches Package.appxmanifest com:Class/@Id).
///   3. Run the Win32 message loop — keeps the process alive for COM activation.
///   4. CoUninitialize on exit.
/// </summary>
int WINAPI wWinMain(HINSTANCE, HINSTANCE, LPWSTR cmdLine, int)
{
    // STA initialisation — required by WinUI 3.
    winrt::init_apartment(winrt::apartment_type::single_threaded);

    // Register the widget provider COM class factory with the Widget Board host.
    // The CLSID here must match the com:Class/@Id in Package.appxmanifest.
    //
    // WidgetManager::GetDefault().RegisterProvider<T>() registers T as the
    // IClassFactory for the CLSID declared in the manifest. The Widget Board
    // will call CoCreateInstance on that CLSID when it first needs a widget.
    auto widgetManager = WidgetManager::GetDefault();

    try
    {
        widgetManager.RegisterProvider<CascadeWidget::CascadeWidgetProvider>();
    }
    catch (winrt::hresult_error const& ex)
    {
        // Registration failure is non-fatal when running interactively (e.g. for
        // debugging). The widget will simply not appear on the Widget Board.
        OutputDebugStringW(
            (L"[CascadeWidget] RegisterProvider failed: " + ex.message() + L"\n").c_str());
    }

    // Win32 message loop — keeps the COM server alive so the Widget Board can
    // call CoCreateInstance after the initial registration. The loop exits when
    // the Widget Board sends WM_QUIT (all widget tiles removed).
    MSG msg{};
    while (GetMessageW(&msg, nullptr, 0, 0))
    {
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }

    // WidgetManager cleanup — unregisters the COM class factory.
    widgetManager.UnregisterProvider();

    winrt::uninit_apartment();
    return static_cast<int>(msg.wParam);
}
