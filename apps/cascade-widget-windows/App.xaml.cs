// App.xaml.cs — Cascade Widget (Windows 11 WinUI 3)
// Purpose:  Bootstraps the Windows App SDK, registers the cascade widget provider
//           with the Windows Widget Board host (WidgetManager), and starts the
//           daemon IPC connection on port 3761.
// Constraints:
//   - Widget provider COM activation requires the app to stay alive as a background
//     COM server; MainWindow is hidden when running as a widget.
//   - IPC to the cascade daemon uses a named pipe; reconnects on drop.
// SPORT: cascade/apps/cascade-widget-windows

using Microsoft.UI.Xaml;
using Microsoft.Windows.Widgets.Providers;
using CascadeWidget.Services;

namespace CascadeWidget;

/// <summary>
/// Application entry point. Registers the Cascade widget provider and wires
/// up the daemon IPC client used by all widget instances.
/// </summary>
public partial class App : Application
{
    /// <summary>Singleton IPC client shared across all widget instances.</summary>
    public static DaemonIpcClient IpcClient { get; } = new DaemonIpcClient();

    private MainWindow? _window;

    public App()
    {
        InitializeComponent();
    }

    /// <inheritdoc/>
    protected override void OnLaunched(LaunchActivatedEventArgs args)
    {
        // Register the widget provider so the Windows Widget Board can
        // instantiate CascadeWidgetProvider via COM activation.
        RegisterWidgetProvider();

        // Only show a window when launched interactively (not by the widget host).
        if (!IsWidgetHostActivation(args))
        {
            _window = new MainWindow();
            _window.Activate();
        }

        // Start background IPC polling — non-blocking.
        _ = IpcClient.ConnectAsync(CancellationToken.None);
    }

    // ── Helpers ──────────────────────────────────────────────────────────────

    /// <summary>
    /// Registers <see cref="CascadeWidgetProvider"/> as a COM out-of-process
    /// widget provider with the Windows Widget Service.
    /// </summary>
    private static void RegisterWidgetProvider()
    {
        // WidgetManager.GetDefault().RegisterProvider registers the OOP COM
        // server; the Widget Board will call CreateInstance when it needs a
        // widget. Errors here are non-fatal — the widget simply won't appear
        // in the board until the next launch.
        try
        {
            var widgetManager = WidgetManager.GetDefault();
            widgetManager.RegisterProvider<CascadeWidgetProvider>();
        }
        catch (Exception ex)
        {
            // Log and continue; interactive mode still works.
            System.Diagnostics.Debug.WriteLine($"[CascadeWidget] Widget registration failed: {ex.Message}");
        }
    }

    /// <summary>
    /// Returns true when the app was activated by the Windows Widget Board
    /// (COM activation) rather than by the user launching it directly.
    /// </summary>
    private static bool IsWidgetHostActivation(LaunchActivatedEventArgs args) =>
        args.Arguments?.Contains("--widget-host", StringComparison.OrdinalIgnoreCase) == true;
}
