// Services/DaemonIpcClient.cs — Cascade Widget (Windows 11 WinUI 3)
// Purpose:  Named-pipe IPC client that connects to the cascade daemon and
//           receives periodic CascadeStatus JSON frames.
// Inputs:   Named pipe "\\\\.\\pipe\\cascade-daemon" (daemon creates it).
// Outputs:  StatusChanged event carrying a parsed CascadeStatus snapshot.
// Constraints:
//   - Reconnects automatically with exponential back-off on disconnect.
//   - Does NOT write to the pipe — the widget is read-only.
//   - Status frames are newline-delimited JSON (NDJSON).
//   - Falls back to HTTP GET http://localhost:3761/status when the named pipe
//     is unavailable (daemon may expose both transport types).
// SPORT: cascade/apps/cascade-widget-windows

using System.IO.Pipes;
using System.Text;
using System.Text.Json;
using CascadeWidget.Models;

namespace CascadeWidget.Services;

/// <summary>
/// Reads real-time status frames from the cascade daemon over a Windows
/// named pipe. Raises <see cref="StatusChanged"/> each time a valid frame
/// arrives so the UI can update without polling.
/// </summary>
public sealed class DaemonIpcClient : IDisposable
{
    private const string PipeName = "cascade-daemon";
    private const string FallbackUrl = "http://localhost:3761/status";
    private static readonly TimeSpan[] BackoffTable =
        [TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(5), TimeSpan.FromSeconds(15), TimeSpan.FromSeconds(30)];

    private readonly HttpClient _http = new() { Timeout = TimeSpan.FromSeconds(5) };
    private CancellationTokenSource? _cts;
    private bool _disposed;

    // ── Public surface ───────────────────────────────────────────────────────

    /// <summary>
    /// Raised on the thread-pool thread when a fresh status frame is parsed.
    /// Subscribers must marshal to the UI thread if updating XAML bindings.
    /// </summary>
    public event EventHandler<CascadeStatus>? StatusChanged;

    /// <summary>True while the background read loop is running.</summary>
    public bool IsConnected { get; private set; }

    /// <summary>
    /// Starts the background connect-and-read loop. Safe to call multiple times;
    /// a running loop is not re-started.
    /// </summary>
    /// <param name="ct">Cancellation token; pass <see cref="CancellationToken.None"/> for app lifetime.</param>
    public async Task ConnectAsync(CancellationToken ct)
    {
        if (_cts is not null) return;   // already running

        _cts = CancellationTokenSource.CreateLinkedTokenSource(ct);
        await Task.Run(() => RunLoopAsync(_cts.Token), _cts.Token);
    }

    /// <inheritdoc/>
    public void Dispose()
    {
        if (_disposed) return;
        _disposed = true;
        _cts?.Cancel();
        _cts?.Dispose();
        _http.Dispose();
    }

    // ── Private loop ─────────────────────────────────────────────────────────

    /// <summary>
    /// Outer reconnect loop: try named pipe first, fall back to HTTP polling,
    /// back off on repeated failures.
    /// </summary>
    private async Task RunLoopAsync(CancellationToken ct)
    {
        int attempt = 0;

        while (!ct.IsCancellationRequested)
        {
            try
            {
                await ReadPipeAsync(ct);
                attempt = 0;    // successful read cycle — reset back-off
            }
            catch (OperationCanceledException) { break; }
            catch
            {
                IsConnected = false;

                // Named pipe failed — try HTTP fallback once before waiting.
                try { await PollHttpAsync(ct); }
                catch (OperationCanceledException) { break; }
                catch { /* HTTP also unavailable — just wait */ }

                var backoff = BackoffTable[Math.Min(attempt, BackoffTable.Length - 1)];
                attempt++;
                await Task.Delay(backoff, ct);
            }
        }
    }

    /// <summary>
    /// Connects to the named pipe and reads NDJSON frames until the pipe
    /// closes or the token is cancelled.
    /// </summary>
    private async Task ReadPipeAsync(CancellationToken ct)
    {
        await using var pipe = new NamedPipeClientStream(
            ".", PipeName, PipeDirection.In, PipeOptions.Asynchronous);

        await pipe.ConnectAsync(timeoutMs: 3_000, ct);
        IsConnected = true;

        using var reader = new StreamReader(pipe, Encoding.UTF8, leaveOpen: true);
        while (!ct.IsCancellationRequested)
        {
            var line = await reader.ReadLineAsync(ct);
            if (line is null) break;    // pipe closed by daemon

            TryRaise(line);
        }
    }

    /// <summary>
    /// Single HTTP GET to the daemon's /status endpoint. Used as a one-shot
    /// fallback when the named pipe is unavailable.
    /// </summary>
    private async Task PollHttpAsync(CancellationToken ct)
    {
        var json = await _http.GetStringAsync(FallbackUrl, ct);
        TryRaise(json);
    }

    /// <summary>
    /// Deserialises a JSON string into a <see cref="CascadeStatus"/> and fires
    /// <see cref="StatusChanged"/>. Ignores malformed frames.
    /// </summary>
    private void TryRaise(string json)
    {
        try
        {
            var status = JsonSerializer.Deserialize<CascadeStatus>(json);
            if (status is not null)
                StatusChanged?.Invoke(this, status);
        }
        catch (JsonException ex)
        {
            System.Diagnostics.Debug.WriteLine($"[DaemonIpcClient] Bad frame: {ex.Message}");
        }
    }
}
