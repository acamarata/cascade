// Models/CascadeStatus.cs — Cascade Widget (Windows 11 WinUI 3)
// Purpose:  Immutable snapshot of the cascade daemon status payload.
//           Deserialized from the JSON frame received over the named-pipe IPC
//           connection on localhost:3761 (Gemini proxy relay port).
// Inputs:   Raw JSON string from DaemonIpcClient.
// Outputs:  Structured status consumed by MainWindowViewModel.
// Constraints:
//   - All fields are nullable — partial frames are valid (daemon may omit
//     tiers that have not yet reported in since the last restart).
//   - QuotaPercent is 0-100; values outside that range are clamped by the VM.
// SPORT: cascade/apps/cascade-widget-windows

using System.Text.Json.Serialization;

namespace CascadeWidget.Models;

/// <summary>
/// Full status snapshot returned by the cascade daemon once per poll cycle
/// (default 60 s). Mirrors the JSON shape produced by the daemon's
/// <c>/status</c> endpoint and the named-pipe frame protocol.
/// </summary>
public sealed record CascadeStatus
{
    /// <summary>ISO 8601 timestamp of when the daemon generated this snapshot.</summary>
    [JsonPropertyName("timestamp")]
    public string? Timestamp { get; init; }

    /// <summary>True when the Gemini proxy relay on port 3761 is accepting connections.</summary>
    [JsonPropertyName("proxy_online")]
    public bool ProxyOnline { get; init; }

    /// <summary>Proxy address, e.g. "localhost:3761".</summary>
    [JsonPropertyName("proxy_address")]
    public string ProxyAddress { get; init; } = "localhost:3761";

    /// <summary>Per-tier quota and health rows (T0 through T3).</summary>
    [JsonPropertyName("tiers")]
    public IReadOnlyList<TierStatus> Tiers { get; init; } = [];
}

/// <summary>
/// Quota and health state for a single cascade tier (T0, T1, T2, or T3).
/// </summary>
public sealed record TierStatus
{
    /// <summary>Human-readable tier label, e.g. "T0", "T1", "T2", "T3".</summary>
    [JsonPropertyName("label")]
    public string Label { get; init; } = string.Empty;

    /// <summary>
    /// Current quota consumption as a percentage (0-100).
    /// Derived from the daemon's per-account utilisation tracking.
    /// </summary>
    [JsonPropertyName("quota_percent")]
    public double QuotaPercent { get; init; }

    /// <summary>
    /// Number of active accounts in this tier that are available
    /// (not rate-limited or in error state).
    /// </summary>
    [JsonPropertyName("accounts_available")]
    public int AccountsAvailable { get; init; }

    /// <summary>Total accounts registered in this tier.</summary>
    [JsonPropertyName("accounts_total")]
    public int AccountsTotal { get; init; }

    /// <summary>
    /// UTC epoch seconds at which the tier's quota resets.
    /// Null if the daemon has not yet received a reset signal.
    /// </summary>
    [JsonPropertyName("resets_at_epoch")]
    public long? ResetsAtEpoch { get; init; }

    /// <summary>
    /// Convenience: time until quota reset as a human-readable string,
    /// e.g. "2h 14m". Null when <see cref="ResetsAtEpoch"/> is null.
    /// </summary>
    public string? ResetsIn =>
        ResetsAtEpoch is long epoch
            ? FormatDuration(DateTimeOffset.FromUnixTimeSeconds(epoch) - DateTimeOffset.UtcNow)
            : null;

    // ── Helpers ──────────────────────────────────────────────────────────────

    private static string FormatDuration(TimeSpan ts)
    {
        if (ts <= TimeSpan.Zero) return "now";
        if (ts.TotalHours >= 1) return $"{(int)ts.TotalHours}h {ts.Minutes}m";
        return $"{ts.Minutes}m {ts.Seconds}s";
    }
}
