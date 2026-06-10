//! # Tray menu actions and menu specification
//!
//! Types for representing user interactions with the system-tray menu and the
//! standard menu structure.
//!
//! ## Design
//!
//! `TrayAction` is a `#[non_exhaustive]` enum to allow future extensions without
//! breaking existing code. Derives `Serialize` and `Deserialize` for IPC.
//!
//! `TrayMenuSpec` specifies a full menu: items are either actions or separators,
//! all serializable so menus can be sent from the daemon to the tray backend
//! without platform-specific coupling.

use serde::{Deserialize, Serialize};

/// User actions triggered by clicking a menu item in the system tray.
///
/// `#[non_exhaustive]` allows new actions to be added in the future without
/// breaking match expressions in stable/released code.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[non_exhaustive]
pub enum TrayAction {
    /// Open the Cascade application.
    OpenApp,
    /// Open the web-based dashboard.
    OpenDashboard,
    /// Pause the cascade daemon.
    PauseDaemon,
    /// Quit the daemon and exit.
    Quit,
}

/// A single menu item: either an action or a separator.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "type", content = "data")]
pub enum TrayMenuItem {
    /// An action item with a label and associated action.
    Action { label: String, action: TrayAction },
    /// A visual separator line.
    Separator,
}

/// The full specification of a system-tray menu.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TrayMenuSpec {
    /// Ordered list of menu items (actions and separators).
    pub items: Vec<TrayMenuItem>,
}

impl TrayMenuSpec {
    /// The standard 4-action cascade menu: Open Cascade, Open Dashboard,
    /// separator, Pause Daemon, Quit.
    pub fn default_menu() -> Self {
        TrayMenuSpec {
            items: vec![
                TrayMenuItem::Action {
                    label: "Open Cascade".to_string(),
                    action: TrayAction::OpenApp,
                },
                TrayMenuItem::Action {
                    label: "Open Dashboard".to_string(),
                    action: TrayAction::OpenDashboard,
                },
                TrayMenuItem::Separator,
                TrayMenuItem::Action {
                    label: "Pause Daemon".to_string(),
                    action: TrayAction::PauseDaemon,
                },
                TrayMenuItem::Action {
                    label: "Quit".to_string(),
                    action: TrayAction::Quit,
                },
            ],
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Verify default_menu() survives a full serde round-trip through JSON.
    /// This is the acceptance criterion for this ticket.
    #[test]
    fn default_menu_serde_round_trip() {
        let original = TrayMenuSpec::default_menu();
        let json = serde_json::to_string(&original).expect("TrayMenuSpec must serialize to JSON");
        let restored: TrayMenuSpec =
            serde_json::from_str(&json).expect("TrayMenuSpec must deserialize from JSON");
        assert_eq!(
            original, restored,
            "serde round-trip through JSON must be lossless"
        );
    }
}
