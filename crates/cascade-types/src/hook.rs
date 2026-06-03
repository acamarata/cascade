//! Hook system types: HookEvent enum, HookDef, HookAction.
//!
//! Inputs:  Serialized from config.toml [[hooks]] sections and JSON round-trips.
//! Outputs: HookDef structs consumed by HookStore; HookEvent matched at runtime.
//! Constraints: HookAction::RunCommand uses arg arrays (not shell strings) to prevent
//!   command injection — same pattern as ScheduledTask.command/args.
//! SPORT: cascade-types/src/lib.rs — HookEvent, HookDef, HookAction exported.

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// The event that triggers a hook.
///
/// Variants mirror the daemon's internal event bus events so hooks can subscribe
/// to any system-level lifecycle event. `Custom(String)` lets users define
/// application-specific event kinds without changing the core enum.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum HookEvent {
    /// Any `.cascade/` file changed (any tier).
    CascadeChanged,

    /// Cascade compat files were regenerated (AGENTS.md, CLAUDE.md, etc.).
    RegenComplete,

    /// A scheduled task completed. Carries the task ID string.
    TaskDone {
        /// The task ID that completed (UUID as string).
        task_id: String,
    },

    /// Daemon startup sequence completed successfully.
    DaemonStart,

    /// Daemon graceful stop was initiated.
    DaemonStop,

    /// Backup sync completed (any tier's backup).
    BackupComplete,

    /// User-defined event kind string — for application-specific use.
    Custom {
        /// Arbitrary event kind string provided by the user.
        kind: String,
    },
}

impl std::fmt::Display for HookEvent {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            HookEvent::CascadeChanged => write!(f, "CascadeChanged"),
            HookEvent::RegenComplete => write!(f, "RegenComplete"),
            HookEvent::TaskDone { task_id } => write!(f, "TaskDone({})", task_id),
            HookEvent::DaemonStart => write!(f, "DaemonStart"),
            HookEvent::DaemonStop => write!(f, "DaemonStop"),
            HookEvent::BackupComplete => write!(f, "BackupComplete"),
            HookEvent::Custom { kind } => write!(f, "Custom({})", kind),
        }
    }
}

impl HookEvent {
    /// Returns true if this event matches the given pattern event.
    ///
    /// For most variants, matching is identity. `TaskDone` matches any other
    /// `TaskDone` regardless of `task_id` when `task_id` is `"*"` (wildcard).
    /// `Custom` matches when `kind` strings are equal.
    pub fn matches(&self, pattern: &HookEvent) -> bool {
        match (self, pattern) {
            (HookEvent::TaskDone { task_id: actual }, HookEvent::TaskDone { task_id: pat }) => {
                pat == "*" || actual == pat
            }
            (HookEvent::Custom { kind: a }, HookEvent::Custom { kind: b }) => a == b,
            _ => std::mem::discriminant(self) == std::mem::discriminant(pattern),
        }
    }
}

/// The action to take when a hook fires.
///
/// Uses `#[serde(tag = "type")]` for clean TOML/JSON authoring:
///
/// ```toml
/// [[hooks]]
/// name = "run-backup"
/// event = { type = "BackupComplete" }
/// [hooks.action]
/// type = "RunCommand"
/// command = "/usr/local/bin/notify-me"
/// args = ["--subject", "Backup done"]
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum HookAction {
    /// Execute an external command.
    ///
    /// `args` is always a Vec<String> — never a shell string — to prevent
    /// command injection. This mirrors the ScheduledTask pattern from T-P2-E02-22.
    RunCommand {
        /// The command binary to execute (path or name on $PATH).
        command: String,

        /// Arguments passed to the command. Never interpolated through a shell.
        #[serde(default)]
        args: Vec<String>,
    },

    /// Emit a structured log message at the given level.
    LogMessage {
        /// Log level: "trace", "debug", "info", "warn", or "error".
        #[serde(default = "default_log_level")]
        level: String,

        /// The message to log.
        message: String,
    },

    /// Publish an event onto the daemon's internal event bus.
    PublishEvent {
        /// Event kind string (used to route to subscribers).
        kind: String,

        /// Arbitrary JSON payload attached to the published event.
        #[serde(default)]
        payload: serde_json::Value,
    },
}

fn default_log_level() -> String {
    "info".to_string()
}

/// A single hook definition — ties an event to an action.
///
/// Stored in `hooks` table in `events.db`. Loaded from `[[hooks]]` arrays in
/// each tier's `config.toml` on daemon startup.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HookDef {
    /// Unique identifier for this hook. Generated on insert if not provided.
    pub id: Uuid,

    /// Human-readable name for this hook (used as a natural key when loading
    /// from config.toml to make re-loading idempotent).
    pub name: String,

    /// The event that triggers this hook.
    pub event: HookEvent,

    /// The action to perform when the event fires.
    pub action: HookAction,

    /// Whether this hook is active. Defaults to `true`.
    #[serde(default = "default_enabled")]
    pub enabled: bool,
}

fn default_enabled() -> bool {
    true
}

impl HookDef {
    /// Create a new HookDef with a freshly generated UUID.
    pub fn new(name: impl Into<String>, event: HookEvent, action: HookAction) -> Self {
        Self {
            id: Uuid::new_v4(),
            name: name.into(),
            event,
            action,
            enabled: true,
        }
    }
}

/// A hook entry as it appears in a single tier's `config.toml` `[[hooks]]` array.
///
/// This is the deserialization target for TOML parsing. After parsing, entries
/// are converted to [`HookDef`] structs for storage.
///
/// TOML example:
/// ```toml
/// [[hooks]]
/// name = "log-regen"
/// event = "RegenComplete"
/// action.type = "LogMessage"
/// action.level = "info"
/// action.message = "Cascade regenerated"
/// enabled = true
/// ```
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HookConfigEntry {
    /// Hook name — used as the natural upsert key.
    pub name: String,

    /// The trigger event. Serialized as a string shorthand for simple variants
    /// (e.g. `"RegenComplete"`) or a tagged object for variants with data.
    pub event: HookEventShorthand,

    /// The action to execute.
    pub action: HookAction,

    /// Whether enabled. Defaults to `true` when omitted.
    #[serde(default = "default_enabled")]
    pub enabled: bool,
}

/// Supports both short-form string events (`"RegenComplete"`) and full tagged
/// objects (`{ type = "TaskDone", task_id = "..." }`) in TOML.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum HookEventShorthand {
    /// Short-form: just a string like `"RegenComplete"` or `"DaemonStart"`.
    Short(String),

    /// Full tagged form with data fields (e.g. `TaskDone`, `Custom`).
    Full(HookEvent),
}

impl HookEventShorthand {
    /// Resolve to a `HookEvent`. Returns an error if the short-form string is
    /// not a recognized variant name for a unit variant.
    pub fn resolve(self) -> Result<HookEvent, String> {
        match self {
            HookEventShorthand::Full(ev) => Ok(ev),
            HookEventShorthand::Short(s) => match s.as_str() {
                "CascadeChanged" => Ok(HookEvent::CascadeChanged),
                "RegenComplete" => Ok(HookEvent::RegenComplete),
                "DaemonStart" => Ok(HookEvent::DaemonStart),
                "DaemonStop" => Ok(HookEvent::DaemonStop),
                "BackupComplete" => Ok(HookEvent::BackupComplete),
                other => Err(format!(
                    "Unknown hook event shorthand '{}'. Use a tagged object for TaskDone or Custom.",
                    other
                )),
            },
        }
    }
}

impl TryFrom<HookConfigEntry> for HookDef {
    type Error = String;

    fn try_from(entry: HookConfigEntry) -> Result<Self, Self::Error> {
        let event = entry.event.resolve()?;
        Ok(HookDef {
            id: Uuid::new_v4(),
            name: entry.name,
            event,
            action: entry.action,
            enabled: entry.enabled,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_hook_def_serde_round_trip() {
        let hook = HookDef::new(
            "log-regen",
            HookEvent::RegenComplete,
            HookAction::LogMessage {
                level: "info".to_string(),
                message: "Cascade regenerated".to_string(),
            },
        );

        let json = serde_json::to_string(&hook).expect("serialize");
        let back: HookDef = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(back.name, hook.name);
        assert_eq!(back.enabled, hook.enabled);
        assert!(matches!(back.event, HookEvent::RegenComplete));
    }

    #[test]
    fn test_hook_event_matches_simple() {
        assert!(HookEvent::RegenComplete.matches(&HookEvent::RegenComplete));
        assert!(!HookEvent::RegenComplete.matches(&HookEvent::DaemonStart));
        assert!(!HookEvent::CascadeChanged.matches(&HookEvent::RegenComplete));
    }

    #[test]
    fn test_hook_event_task_done_wildcard() {
        let fired = HookEvent::TaskDone {
            task_id: "abc-123".to_string(),
        };
        let wildcard_pattern = HookEvent::TaskDone {
            task_id: "*".to_string(),
        };
        let exact_pattern = HookEvent::TaskDone {
            task_id: "abc-123".to_string(),
        };
        let wrong_pattern = HookEvent::TaskDone {
            task_id: "xyz".to_string(),
        };

        assert!(fired.matches(&wildcard_pattern));
        assert!(fired.matches(&exact_pattern));
        assert!(!fired.matches(&wrong_pattern));
    }

    #[test]
    fn test_hook_action_run_command_no_shell_interpolation() {
        // The args field is Vec<String> — not a shell string. Verify serialization
        // preserves the separate args.
        let action = HookAction::RunCommand {
            command: "/usr/bin/notify".to_string(),
            args: vec!["--msg".to_string(), "done; rm -rf /".to_string()],
        };

        let json = serde_json::to_string(&action).expect("serialize");
        let back: HookAction = serde_json::from_str(&json).expect("deserialize");
        if let HookAction::RunCommand { args, .. } = back {
            // The dangerous string is preserved as a literal arg, not executed as shell.
            assert_eq!(args[1], "done; rm -rf /");
        } else {
            panic!("Wrong variant");
        }
    }

    #[test]
    fn test_hook_event_shorthand_resolve() {
        let sh = HookEventShorthand::Short("RegenComplete".to_string());
        assert!(matches!(sh.resolve().unwrap(), HookEvent::RegenComplete));

        let sh2 = HookEventShorthand::Short("CascadeChanged".to_string());
        assert!(matches!(sh2.resolve().unwrap(), HookEvent::CascadeChanged));

        let sh3 = HookEventShorthand::Short("Unknown".to_string());
        assert!(sh3.resolve().is_err());
    }

    #[test]
    fn test_hook_config_entry_to_hook_def() {
        let entry = HookConfigEntry {
            name: "test-hook".to_string(),
            event: HookEventShorthand::Short("DaemonStart".to_string()),
            action: HookAction::LogMessage {
                level: "info".to_string(),
                message: "Daemon started".to_string(),
            },
            enabled: true,
        };

        let def: HookDef = entry.try_into().expect("convert");
        assert_eq!(def.name, "test-hook");
        assert!(matches!(def.event, HookEvent::DaemonStart));
    }
}
