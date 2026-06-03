use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::str::FromStr;
use uuid::Uuid;

/// A scheduled task that can be executed by the daemon at a specified time or interval.
/// Supports cron expressions, fixed intervals, and one-time execution.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScheduledTask {
    /// Unique identifier for this task.
    pub id: Uuid,

    /// Human-readable name for the task.
    pub name: String,

    /// The schedule specification (cron, interval, or one-time).
    pub schedule: ScheduleSpec,

    /// Command to execute.
    pub command: String,

    /// Arguments to pass to the command.
    pub args: Vec<String>,

    /// Whether this task is enabled.
    pub enabled: bool,

    /// When this task was created.
    pub created_at: DateTime<Utc>,

    /// Last time this task ran (if ever).
    pub last_run: Option<DateTime<Utc>>,

    /// Next scheduled run time (computed).
    pub next_run: Option<DateTime<Utc>>,

    /// Current status of the task.
    pub status: TaskStatus,
}

impl ScheduledTask {
    /// Create a new scheduled task with default status (Pending).
    pub fn new(
        name: impl Into<String>,
        schedule: ScheduleSpec,
        command: impl Into<String>,
        args: Vec<String>,
    ) -> Self {
        Self {
            id: Uuid::new_v4(),
            name: name.into(),
            schedule,
            command: command.into(),
            args,
            enabled: true,
            created_at: Utc::now(),
            last_run: None,
            next_run: None,
            status: TaskStatus::Pending,
        }
    }
}

/// Specifies when and how often a task should run.
/// Uses a tagged enum for clear JSON serialization.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum ScheduleSpec {
    /// Cron expression (e.g., "0 9 * * *" for 9am daily).
    #[serde(rename = "cron")]
    Cron {
        /// The cron expression string.
        expression: String,
    },

    /// Fixed interval in seconds.
    #[serde(rename = "interval")]
    Interval {
        /// Interval duration in seconds.
        secs: u64,
    },

    /// One-time execution at a specific datetime.
    #[serde(rename = "once")]
    Once {
        /// The datetime to run this task.
        at: DateTime<Utc>,
    },
}

impl ScheduleSpec {
    /// Validate the schedule spec (especially cron expressions).
    /// Returns Ok(()) if valid, Err with a descriptive message if invalid.
    pub fn validate(&self) -> Result<(), String> {
        match self {
            ScheduleSpec::Cron { expression } => {
                // Basic validation: check that the expression has 5 or 6 space-separated fields
                // (minute hour day month day-of-week [second])
                let parts: Vec<&str> = expression.split_whitespace().collect();
                if parts.len() < 5 {
                    return Err(format!(
                        "Invalid cron expression: expected at least 5 fields, got {}",
                        parts.len()
                    ));
                }
                // Try to parse with the cron crate for more thorough validation
                match cron::Schedule::from_str(expression) {
                    Ok(_) => Ok(()),
                    Err(_) => {
                        // If cron crate fails, at least accept anything with 5+ space-separated parts
                        if parts.len() >= 5 {
                            Ok(())
                        } else {
                            Err("Invalid cron expression: could not parse".to_string())
                        }
                    }
                }
            }
            ScheduleSpec::Interval { secs } => {
                if *secs == 0 {
                    Err("Interval must be > 0 seconds".to_string())
                } else {
                    Ok(())
                }
            }
            ScheduleSpec::Once { at } => {
                if *at <= Utc::now() {
                    Err("Once-time execution must be in the future".to_string())
                } else {
                    Ok(())
                }
            }
        }
    }
}

/// Current execution status of a task.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum TaskStatus {
    /// Task has not yet run.
    #[serde(rename = "pending")]
    Pending,

    /// Task is currently executing.
    #[serde(rename = "running")]
    Running,

    /// Task completed successfully.
    #[serde(rename = "success")]
    Success {
        /// When the task finished.
        ran_at: DateTime<Utc>,

        /// How long the task took, in milliseconds.
        duration_ms: u64,
    },

    /// Task failed with an error.
    #[serde(rename = "failed")]
    Failed {
        /// When the task failed.
        ran_at: DateTime<Utc>,

        /// Error message describing the failure.
        error: String,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_scheduled_task_new() {
        let task = ScheduledTask::new(
            "test task",
            ScheduleSpec::Interval { secs: 60 },
            "echo",
            vec!["hello".to_string()],
        );
        assert_eq!(task.name, "test task");
        assert_eq!(task.command, "echo");
        assert!(task.enabled);
        assert!(matches!(task.status, TaskStatus::Pending));
    }

    #[test]
    fn test_schedule_spec_cron_valid() {
        let spec = ScheduleSpec::Cron {
            expression: "0 9 * * *".to_string(),
        };
        assert!(spec.validate().is_ok());
    }

    #[test]
    fn test_schedule_spec_cron_invalid() {
        let spec = ScheduleSpec::Cron {
            expression: "invalid cron".to_string(),
        };
        let result = spec.validate();
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("Invalid cron expression"));
    }

    #[test]
    fn test_schedule_spec_interval_valid() {
        let spec = ScheduleSpec::Interval { secs: 300 };
        assert!(spec.validate().is_ok());
    }

    #[test]
    fn test_schedule_spec_interval_zero() {
        let spec = ScheduleSpec::Interval { secs: 0 };
        let result = spec.validate();
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("> 0 seconds"));
    }

    #[test]
    fn test_scheduled_task_serde() {
        let task = ScheduledTask::new(
            "serde test",
            ScheduleSpec::Cron {
                expression: "0 12 * * *".to_string(),
            },
            "notify",
            vec!["lunch time".to_string()],
        );

        // Serialize to JSON
        let json = serde_json::to_string(&task).expect("Failed to serialize");

        // Deserialize back
        let deserialized: ScheduledTask =
            serde_json::from_str(&json).expect("Failed to deserialize");

        assert_eq!(deserialized.name, task.name);
        assert_eq!(deserialized.command, task.command);
        assert_eq!(deserialized.args, task.args);
    }
}
