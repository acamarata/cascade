# Windows Tray Icon — Manual Smoke Test Procedure

**Scope:** Verify that `cascaded` places a functional tray icon in the Windows 11 system tray, that the tooltip shows live daemon state, and that the right-click context menu exposes all four expected actions.

**Pre-conditions:**

- Windows 11 (22H2 or later) development machine with Rust toolchain (stable, `x86_64-pc-windows-msvc`).
- `cascaded` binary built in release mode: `cargo build -p cascade-daemon --release`.
- No prior `cascaded` process running (check Task Manager → Details → `cascaded.exe`).

---

## Step 1 — Build the daemon

Open a PowerShell terminal in the `cascade` repo root.

```powershell
cargo build -p cascade-daemon --release
```

Expected output ends with:

```
Finished `release` profile [optimized] target(s) in ...
```

Binary location: `target\release\cascaded.exe`.

---

## Step 2 — Launch the daemon

```powershell
.\target\release\cascaded.exe --log-level info
```

The terminal should show a startup log line similar to:

```
INFO cascade_daemon: daemon started  version=...
INFO cascade-tray: tray thread started
```

Leave the terminal open — it tails the daemon log.

---

## Step 3 — Verify tray icon appears

1. Look at the Windows system tray (bottom-right corner of the taskbar).  
   If the icon is hidden, click the **^** (Show hidden icons) chevron.
2. A small Cascade icon must appear **within 5 seconds** of launch.
3. **Screenshot the tray area** showing the icon. Paste the screenshot below this line.

> _[Paste screenshot: tray icon visible]_

---

## Step 4 — Verify tooltip text

Hover the mouse pointer over the Cascade tray icon and hold for ~1 second.

The tooltip must contain:

- Rule count in the format: `Cascade: X rules | …`
- Daemon status (e.g. `Running` or `Paused`)

**Screenshot the tooltip.** Paste below.

> _[Paste screenshot: tooltip text]_

---

## Step 5 — Verify right-click context menu

Right-click the Cascade tray icon. A popup menu must appear with **exactly these four items** in this order:

| # | Label | Separator before? |
|---|-------|-------------------|
| 1 | Open Cascade | no |
| 2 | Open Dashboard | no |
| 3 | _(separator)_ | — |
| 4 | Pause Daemon | after item 2 |
| 5 | Quit | no |

**Screenshot the popup menu.** Paste below.

> _[Paste screenshot: right-click menu — all 4 actions visible]_

---

## Step 6 — Test "Open Dashboard" action

Click **Open Dashboard** from the context menu.

Expected: the default browser opens `http://localhost:9761` (or the configured dashboard port) and displays the Cascade fleet dashboard.

---

## Step 7 — Test "Pause Daemon" action

Click **Pause Daemon** from the context menu.

Expected:

- Daemon log shows: `INFO cascade_daemon: daemon paused via tray action`
- Tray tooltip updates to show `Paused` within 15 seconds.
- Right-click menu shows a **Resume Daemon** item in place of **Pause Daemon** (or the label changes to indicate paused state).

---

## Step 8 — Test "Quit" action

Click **Quit** from the context menu.

Expected:

- Daemon exits with code 0 within 2 seconds.
- Tray icon disappears from the system tray.
- Terminal shows: `INFO cascade_daemon: clean shutdown — goodbye`
- Task Manager no longer lists `cascaded.exe`.

---

## Pass criteria

All eight steps complete without error and all screenshots show the expected state. Any deviation is a test failure — open a bug with the step number and the screenshot.

---

## Known limitations (P2 scope)

- The Windows tray backend is compiled but the native `WindowsTrayImpl` may fall back to `NoopTray` on machines without a compositor or on headless build agents. CI validates the `MockTrayHandle` path only; this document covers the physical Windows device path.
- "Open Cascade" launches the Tauri GUI, which is built in P3. In P2 this action may be a no-op or open a placeholder URL.
