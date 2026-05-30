# cascade-widget-windows

Windows 11 WinUI 3 widget for the Cascade fleet dashboard.

Displays T0-T3 tier quota bars, Gemini proxy relay health, and per-account
reset countdowns in the Windows Widget Board.

## Requirements

- Windows 11 22H2 or later (Widget Board support)
- .NET 8 SDK
- Windows App SDK 1.4+
- Visual Studio 2022 17.8+ or `dotnet` CLI

## Architecture

```
App.xaml / App.xaml.cs          Bootstrap — registers COM widget provider
MainWindow.xaml / .cs           Widget board entry, tier status grid
Models/CascadeStatus.cs         Immutable status snapshot (deserialized from daemon)
Services/DaemonIpcClient.cs     Named-pipe reader; HTTP fallback on localhost:3761
ViewModels/MainWindowViewModel  INotifyPropertyChanged bridge for x:Bind
```

The widget connects to the cascade daemon via the `cascade-daemon` named pipe.
If the pipe is unavailable it falls back to `http://localhost:3761/status`.

Status frames are newline-delimited JSON (NDJSON) with the shape defined in
`Models/CascadeStatus.cs`.

## Build

```powershell
dotnet build -c Release
```

## Run (interactive / debug)

```powershell
dotnet run
```

## Widget Board registration

The app self-registers as a COM out-of-process widget provider on first run.
After registration, open the Windows Widget Board (Win + W) and click the
"+" button to add the Cascade widget.

## IPC frame format

```json
{
  "timestamp": "2026-05-30T14:23:01Z",
  "proxy_online": true,
  "proxy_address": "localhost:3761",
  "tiers": [
    { "label": "T0", "quota_percent": 12, "accounts_available": 4, "accounts_total": 4 },
    { "label": "T1", "quota_percent": 34, "accounts_available": 4, "accounts_total": 4 },
    { "label": "T2", "quota_percent": 18, "accounts_available": 4, "accounts_total": 4 },
    { "label": "T3", "quota_percent":  5, "accounts_available": 8, "accounts_total": 8 }
  ]
}
```
