// MainWindow.xaml.cs — Cascade Widget (Windows 11 WinUI 3)
// Purpose:  Code-behind for the widget board entry window. Binds the
//           MainWindowViewModel to the view, subscribes to IPC status events,
//           and handles window sizing for the Widget Board large slot.
// Constraints:
//   - Window is hidden when the app is activated as a COM widget host.
//   - ViewModel updates are marshalled to the UI thread via DispatcherQueue.
// SPORT: cascade/apps/cascade-widget-windows

using Microsoft.UI;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using CascadeWidget.Services;
using CascadeWidget.ViewModels;

namespace CascadeWidget;

/// <summary>
/// Widget board entry window. Shows the fleet status dashboard for all
/// cascade tiers (T0-T3) and Gemini proxy health.
/// </summary>
public sealed partial class MainWindow : Window
{
    /// <summary>View model bound to the XAML layout.</summary>
    public MainWindowViewModel ViewModel { get; }

    public MainWindow()
    {
        InitializeComponent();

        ViewModel = new MainWindowViewModel(App.IpcClient);

        // Size the window to the Widget Board large slot (373 × 466 px @ 100%).
        ConfigureWindowSize();

        // Forward daemon status changes to the view model on the UI thread.
        App.IpcClient.StatusChanged += OnDaemonStatusChanged;
    }

    // ── Event handlers ───────────────────────────────────────────────────────

    /// <summary>
    /// Invoked when the daemon IPC client receives a fresh status payload.
    /// Marshals the update to the UI dispatcher thread.
    /// </summary>
    private void OnDaemonStatusChanged(object? sender, CascadeStatus status)
    {
        DispatcherQueue.TryEnqueue(() => ViewModel.ApplyStatus(status));
    }

    // ── Window setup ─────────────────────────────────────────────────────────

    /// <summary>
    /// Configures the window to match the Windows Widget Board large tile
    /// dimensions (373 × 466 logical pixels). Uses AppWindow for precise
    /// control independent of DPI scaling.
    /// </summary>
    private void ConfigureWindowSize()
    {
        var hWnd = WinRT.Interop.WindowNative.GetWindowHandle(this);
        var windowId = Win32Interop.GetWindowIdFromWindow(hWnd);
        var appWindow = AppWindow.GetFromWindowId(windowId);

        appWindow.Resize(new Windows.Graphics.SizeInt32(373, 466));
        appWindow.SetPresenter(AppWindowPresenterKind.Default);
    }
}
