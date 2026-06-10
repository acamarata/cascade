# Windows 11 Widget — Manual Smoke Test Procedure

**Scope:** Verify that the Cascade Windows Widget (MSIX package) installs correctly, appears on the Windows 11 Widget Board, and displays live daemon telemetry data from `status-cache.json`.

**Pre-conditions:**

- Windows 11 (22H2 or later) with the Windows Widget Board enabled (Settings → Personalisation → Widgets → On).
- Cascade daemon (`cascaded.exe`) running and writing `status-cache.json` to `%LOCALAPPDATA%\Cascade\status-cache.json`.
- MSIX installer package built: `cargo build -p cascade-daemon --release` followed by the MSIX packaging step (see `apps/windows-widget/README.md`).
- Developer mode enabled in Windows settings (required for sideloading unsigned MSIX in development).

---

## Step 1 — Build the MSIX package

```powershell
# From repo root
cargo build -p cascade-daemon --release
# Package the widget (adjust path when the build script lands in P3)
.\scripts\build-windows-widget.ps1 -Configuration Release
```

Expected output:

```
Package created: dist\CascadeWidget_1.0.0.0_x64.msix
```

---

## Step 2 — Install the MSIX package

```powershell
Add-AppxPackage -Path .\dist\CascadeWidget_1.0.0.0_x64.msix
```

Expected: PowerShell returns without error. In Settings → Apps → Installed apps, `Cascade Widget` should appear.

**Screenshot the Apps list.** Paste below.

> _[Paste screenshot: Cascade Widget in installed apps list]_

---

## Step 3 — Verify daemon is running and writing status-cache.json

```powershell
Get-Content "$env:LOCALAPPDATA\Cascade\status-cache.json" | ConvertFrom-Json | Format-List
```

Expected output includes:

```
daemon_status  : Running
active_agents  : ...
inbox_unread   : ...
rag_freshness_secs : ...
tier_counts    : ...
```

If the file is absent, start the daemon first (`.\target\release\cascaded.exe`).

---

## Step 4 — Add the Cascade widget to the Widget Board

1. Click the **Widgets** button in the taskbar (or press **Win + W**).
2. In the Widget Board, click the **+** (Add widgets) button in the top-right corner.
3. Find **Cascade** in the widget gallery. If it does not appear, scroll down or search for "Cascade".
4. Click **+** next to the Cascade widget to add it to the board.

**Screenshot the widget appearing on the board.** Paste below.

> _[Paste screenshot: Cascade widget visible on Widget Board]_

---

## Step 5 — Verify widget shows live data

After adding the widget, it should display values read from `status-cache.json`:

| Field | Expected display |
|-------|-----------------|
| Daemon status | `Running` (green indicator) |
| Active agents | Numeric count (matches daemon log) |
| Inbox unread | Numeric count |
| RAG freshness | Time since last index refresh |
| Tier breakdown | Per-tier agent counts (T0/T1/T2/T3) |

**Screenshot the widget showing live data.** Paste below.

> _[Paste screenshot: widget with live telemetry fields populated]_

---

## Step 6 — Verify widget updates automatically

1. In a separate PowerShell window, trigger a state change (e.g. start a new cascade run that increases active agents).
2. Wait up to **35 seconds** (widget polling interval).
3. Observe the widget — the active-agent count or RAG freshness must update to reflect the new state.

**Screenshot the widget after the update.** Paste below.

> _[Paste screenshot: widget reflecting updated daemon state]_

---

## Step 7 — Synthetic fixture verification (CI path)

For automated verification without a real daemon, use the synthetic fixture approach:

```powershell
# Write a synthetic status-cache.json to the expected location.
$fixture = @{
    daemon_status      = "Running"
    active_agents      = 7
    inbox_unread       = 2
    rag_freshness_secs = 120
    tier_counts        = @{ T0 = 1; T1 = 0; T2 = 5; T3 = 1 }
} | ConvertTo-Json
New-Item -Path "$env:LOCALAPPDATA\Cascade" -ItemType Directory -Force | Out-Null
$fixture | Set-Content -Path "$env:LOCALAPPDATA\Cascade\status-cache.json" -Encoding UTF8
```

After writing the fixture, wait 35 seconds and verify the widget displays:

- Active agents: **7**
- Inbox unread: **2**
- RAG freshness: approximately **120 s**

This step validates that the `IWidgetProvider` implementation reads the correct file path and parses all fields.

---

## Pass criteria

All seven steps complete without error. Screenshots confirm:

1. MSIX installed successfully.
2. Widget appears on the Widget Board.
3. Widget shows live (or fixture) daemon telemetry.
4. Widget refreshes within 35 seconds on a state change.

Any deviation is a test failure — open a bug with the step number, the expected value, the actual value, and a screenshot.

---

## Known limitations (P2 scope)

- The `IWidgetProvider` COM registration and MSIX packaging pipeline are placeholders in P2. The `status-cache.json` reader is the only fully functional piece; the Windows Widget Board integration is completed in P3 (T-P3-E04-*).
- If Developer Mode is disabled, MSIX sideloading requires a code-signing certificate. For internal testing, enable Developer Mode (Settings → Privacy & security → For developers → Developer Mode → On).
- The Cascade widget does not yet support dark/light theme switching. This is a P3 enhancement.
