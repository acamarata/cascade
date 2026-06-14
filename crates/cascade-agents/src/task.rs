//! AgentTask — the unit of work dispatched to an agent.
//!
//! Purpose: define the task seam between the registry/dispatch layers and the
//!   execution engine (E-P6-02/03). The dispatch engine drops in by populating
//!   tasks and watching their status transitions.
//!
//! Inputs: created by the CEO/orchestrator when a goal is decomposed.
//!
//! Outputs: consumed by the execution engine; final status stored for observability.
//!
//! Constraints:
//!   - DISTINCT from the user-facing kanban `Task` in P8; named `AgentTask`.
//!   - Parent/child hierarchy allows tree-shaped work decomposition.
//!   - All fields serde camelCase; no execution engine imports.
//!
//! SPORT: cascade-agents / task — E-P6-01

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::spec::AgentRole;

// ── TaskStatus ────────────────────────────────────────────────────────────────

/// Lifecycle states of an `AgentTask`.
///
/// Transitions (valid): `Pending → Running → {Done | Failed | Cancelled}`.
/// `Pending → Cancelled` is also valid (cancelled before start).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum TaskStatus {
    /// Waiting for a free agent slot.
    Pending,
    /// Actively being executed by an agent.
    Running,
    /// Completed successfully.
    Done,
    /// Execution failed; see `error` field on `AgentTask`.
    Failed,
    /// Cancelled before or during execution.
    Cancelled,
}

impl std::fmt::Display for TaskStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            Self::Pending => "pending",
            Self::Running => "running",
            Self::Done => "done",
            Self::Failed => "failed",
            Self::Cancelled => "cancelled",
        };
        f.write_str(s)
    }
}

// ── AgentTask ─────────────────────────────────────────────────────────────────

/// A discrete unit of work dispatched to an agent.
///
/// The execution engine (E-P6-02/03) consumes `AgentTask` values produced by
/// the orchestrator. This struct is the stable seam — the engine attaches to
/// it without requiring changes to this module.
///
/// # Parent/child
/// `parent_id` is `None` for root tasks. Child tasks share the same
/// `root_id` as their topmost ancestor, enabling tree-wide observability.
///
/// # serde contract
/// All fields serialize as `camelCase`.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentTask {
    /// Unique task identifier (UUIDv4).
    pub id: String,

    /// The goal or instruction for the agent (plain text or structured JSON).
    pub goal: String,

    /// The agent role this task is assigned to.
    pub assigned_role: AgentRole,

    /// Lifecycle status.
    pub status: TaskStatus,

    /// Optional id of the parent `AgentTask` (for sub-tasks).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub parent_id: Option<String>,

    /// The root task in the decomposition tree (self if root).
    pub root_id: String,

    /// Wall-clock creation time.
    pub created_at: DateTime<Utc>,

    /// When the task transitioned to `Running`, `Done`, or `Failed`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub updated_at: Option<DateTime<Utc>>,

    /// Human-readable error message when `status == Failed`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,

    /// The id of the `AgentSpec` assigned to execute this task (set at dispatch time).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub assigned_agent_id: Option<String>,
}

impl AgentTask {
    /// Create a new root `AgentTask` in `Pending` status.
    pub fn new_root(goal: impl Into<String>, role: AgentRole) -> Self {
        let id = Uuid::new_v4().to_string();
        Self {
            id: id.clone(),
            goal: goal.into(),
            assigned_role: role,
            status: TaskStatus::Pending,
            parent_id: None,
            root_id: id,
            created_at: Utc::now(),
            updated_at: None,
            error: None,
            assigned_agent_id: None,
        }
    }

    /// Create a child `AgentTask` beneath `parent`.
    pub fn new_child(goal: impl Into<String>, role: AgentRole, parent: &AgentTask) -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            goal: goal.into(),
            assigned_role: role,
            status: TaskStatus::Pending,
            parent_id: Some(parent.id.clone()),
            root_id: parent.root_id.clone(),
            created_at: Utc::now(),
            updated_at: None,
            error: None,
            assigned_agent_id: None,
        }
    }

    /// Transition the task to `Running`, recording the assigned agent.
    pub fn mark_running(&mut self, agent_id: impl Into<String>) {
        self.status = TaskStatus::Running;
        self.assigned_agent_id = Some(agent_id.into());
        self.updated_at = Some(Utc::now());
    }

    /// Transition the task to `Done`.
    pub fn mark_done(&mut self) {
        self.status = TaskStatus::Done;
        self.updated_at = Some(Utc::now());
    }

    /// Transition the task to `Failed` with an error message.
    pub fn mark_failed(&mut self, error: impl Into<String>) {
        self.status = TaskStatus::Failed;
        self.error = Some(error.into());
        self.updated_at = Some(Utc::now());
    }

    /// Returns `true` if the task is in a terminal state.
    pub fn is_terminal(&self) -> bool {
        matches!(
            self.status,
            TaskStatus::Done | TaskStatus::Failed | TaskStatus::Cancelled
        )
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::spec::AgentRole;

    #[test]
    fn new_root_is_pending_and_self_rooted() {
        let task = AgentTask::new_root("write the auth module", AgentRole::Coder);
        assert_eq!(task.status, TaskStatus::Pending);
        assert_eq!(task.root_id, task.id);
        assert!(task.parent_id.is_none());
        assert!(!task.is_terminal());
    }

    #[test]
    fn child_inherits_root_id() {
        let parent = AgentTask::new_root("parent goal", AgentRole::Ceo);
        let child = AgentTask::new_child("child goal", AgentRole::Coder, &parent);
        assert_eq!(child.root_id, parent.root_id);
        assert_eq!(child.parent_id, Some(parent.id.clone()));
    }

    #[test]
    fn lifecycle_transitions() {
        let mut task = AgentTask::new_root("do something", AgentRole::Coder);
        task.mark_running("cascade.coder");
        assert_eq!(task.status, TaskStatus::Running);
        assert_eq!(task.assigned_agent_id.as_deref(), Some("cascade.coder"));
        assert!(!task.is_terminal());

        task.mark_done();
        assert_eq!(task.status, TaskStatus::Done);
        assert!(task.is_terminal());
    }

    #[test]
    fn failed_transition_stores_error() {
        let mut task = AgentTask::new_root("failing goal", AgentRole::Coder);
        task.mark_failed("timeout");
        assert_eq!(task.status, TaskStatus::Failed);
        assert_eq!(task.error.as_deref(), Some("timeout"));
        assert!(task.is_terminal());
    }

    #[test]
    fn task_serde_camel_case_round_trip() {
        let mut task = AgentTask::new_root("serde test", AgentRole::Reviewer);
        task.assigned_agent_id = Some("cascade.ceo".into());
        let json = serde_json::to_string(&task).unwrap();
        assert!(
            json.contains("\"assignedRole\""),
            "expected camelCase assignedRole"
        );
        assert!(json.contains("\"rootId\""), "expected camelCase rootId");
        let decoded: AgentTask = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.assigned_role, AgentRole::Reviewer);
    }
}
