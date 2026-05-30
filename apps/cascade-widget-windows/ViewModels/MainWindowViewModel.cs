// ViewModels/MainWindowViewModel.cs — Cascade Widget (Windows 11 WinUI 3)
// Purpose:  MVVM view model bridging DaemonIpcClient status frames to the
//           XAML bindings in MainWindow.xaml.
// Inputs:   CascadeStatus from DaemonIpcClient.StatusChanged.
// Outputs:  Observable properties consumed by x:Bind expressions.
// Constraints:
//   - Must be called on the UI thread (DispatcherQueue.TryEnqueue in code-behind).
//   - QuotaPercent is clamped to [0, 100].
// SPORT: cascade/apps/cascade-widget-windows

using System.Collections.ObjectModel;
using System.ComponentModel;
using System.Runtime.CompilerServices;
using CascadeWidget.Models;
using CascadeWidget.Services;

namespace CascadeWidget.ViewModels;

/// <summary>
/// Provides bindable properties for the main fleet-status window.
/// Implements <see cref="INotifyPropertyChanged"/> for WinUI 3 x:Bind
/// one-way / two-way binding.
/// </summary>
public sealed class MainWindowViewModel : INotifyPropertyChanged
{
    private bool _daemonOnline;
    private string _proxyAddress = "localhost:3761";
    private string _lastUpdated = "—";

    public MainWindowViewModel(DaemonIpcClient client)
    {
        // Seed placeholder tiers so the UI is never empty on first load.
        Tiers = new ObservableCollection<TierStatus>(
        [
            new TierStatus { Label = "T0", QuotaPercent = 0, AccountsAvailable = 0, AccountsTotal = 0 },
            new TierStatus { Label = "T1", QuotaPercent = 0, AccountsAvailable = 0, AccountsTotal = 0 },
            new TierStatus { Label = "T2", QuotaPercent = 0, AccountsAvailable = 0, AccountsTotal = 0 },
            new TierStatus { Label = "T3", QuotaPercent = 0, AccountsAvailable = 0, AccountsTotal = 0 },
        ]);
    }

    // ── Bindable properties ──────────────────────────────────────────────────

    /// <summary>True when the daemon named pipe or HTTP endpoint responded.</summary>
    public bool DaemonOnline
    {
        get => _daemonOnline;
        private set => SetField(ref _daemonOnline, value);
    }

    /// <summary>Proxy address string shown in the header, e.g. "localhost:3761".</summary>
    public string ProxyAddress
    {
        get => _proxyAddress;
        private set => SetField(ref _proxyAddress, value);
    }

    /// <summary>Human-readable "last updated" label, e.g. "14:23:01 UTC".</summary>
    public string LastUpdated
    {
        get => _lastUpdated;
        private set => SetField(ref _lastUpdated, value);
    }

    /// <summary>Observable tier rows bound to the ItemsControl in the XAML.</summary>
    public ObservableCollection<TierStatus> Tiers { get; }

    // ── Update entry point ───────────────────────────────────────────────────

    /// <summary>
    /// Applies a fresh <see cref="CascadeStatus"/> snapshot to all bindable
    /// properties. Must be called on the UI thread.
    /// </summary>
    public void ApplyStatus(CascadeStatus status)
    {
        DaemonOnline = status.ProxyOnline;
        ProxyAddress = status.ProxyAddress;
        LastUpdated = status.Timestamp is not null
            ? $"Updated {DateTimeOffset.Parse(status.Timestamp).ToLocalTime():HH:mm:ss}"
            : DateTimeOffset.Now.ToString("HH:mm:ss");

        // Merge tier rows: update existing entries in-place to avoid
        // list flicker; add/remove rows if the daemon reports a different count.
        var incoming = status.Tiers;
        for (int i = 0; i < Math.Min(Tiers.Count, incoming.Count); i++)
            Tiers[i] = incoming[i] with { QuotaPercent = Math.Clamp(incoming[i].QuotaPercent, 0, 100) };

        while (Tiers.Count < incoming.Count)
            Tiers.Add(incoming[Tiers.Count]);

        while (Tiers.Count > incoming.Count)
            Tiers.RemoveAt(Tiers.Count - 1);
    }

    // ── INotifyPropertyChanged ───────────────────────────────────────────────

    public event PropertyChangedEventHandler? PropertyChanged;

    private void OnPropertyChanged([CallerMemberName] string? name = null) =>
        PropertyChanged?.Invoke(this, new PropertyChangedEventArgs(name));

    private bool SetField<T>(ref T field, T value, [CallerMemberName] string? name = null)
    {
        if (EqualityComparer<T>.Default.Equals(field, value)) return false;
        field = value;
        OnPropertyChanged(name);
        return true;
    }
}
