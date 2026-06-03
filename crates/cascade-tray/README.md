# cascade-tray

Platform-agnostic system-tray abstraction for the Cascade daemon.

## Purpose

`cascade-tray` defines the trait and data types that decouple the cascade daemon from OS-specific tray icon libraries. Platform backends (macOS, Linux, Windows) implement `TrayHandle` in separate crates.

## API surface

| Item | Kind | Description |
|------|------|-------------|
| `TrayHandle` | trait | Platform backend contract — `update`, `show`, `hide`, `destroy`, `set_menu`, `last_action` |
| `TrayState` | struct | Daemon telemetry snapshot pushed to the tray (tier counts, agent count, inbox unread, etc.) |
| `DaemonStatus` | enum | `Running` / `Paused` / `Stopped` — operational state of the cascade daemon |
| `TrayError` | enum | `Platform(String)` / `Io(std::io::Error)` / `Serialize(String)` |
| `TrayAction` | enum | `#[non_exhaustive]` menu action: `OpenApp` / `OpenDashboard` / `PauseDaemon` / `Quit` |
| `TrayMenuSpec` | struct | Full menu specification — ordered list of `TrayMenuItem` (action or separator) |
| `TrayMenuItem` | enum | `Action { label, action }` or `Separator` — serializable menu item |

## Design

- `TrayHandle` is object-safe. Store backends as `Box<dyn TrayHandle>`.
- `TrayState` derives `Serialize + Deserialize` for JSON IPC between daemon and tray process.
- No platform-specific dependencies in this crate. Platform deps live in the backend crates.

## Usage

```rust
use cascade_tray::{TrayHandle, TrayState, TrayError};

// Implement TrayHandle for your platform backend:
struct MyTray;

impl TrayHandle for MyTray {
    fn update(&mut self, state: &TrayState) -> Result<(), TrayError> {
        // refresh native menu from state
        Ok(())
    }
    fn show(&mut self) -> Result<(), TrayError> { Ok(()) }
    fn hide(&mut self) {}
    fn destroy(self) {}
}

// Store as a trait object:
let tray: Box<dyn TrayHandle> = Box::new(MyTray);
```

## License

MIT
