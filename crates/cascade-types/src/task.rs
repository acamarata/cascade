//! # cascade_types::task — Kanban Task (user-facing card)
//!
//! Purpose: Shared wire/storage type for the Cascade Kanban board. This is the
//!   **user-visible card** (title, status, tags, assignee, priority, blockers,
//!   due date, board order). It is distinct from:
//!   - `cascade_agents::AgentTask` — internal agent work unit
//!   - PBD tickets — phase planning artefacts
//!
//! Inputs:  serde-compatible JSON (camelCase on the wire, snake_case in Rust)
//! Outputs: type shared by cascade-types, cascade-core (TaskStore), cascade-daemon (IPC)
//! Constraints: no circular deps; cascade-types is the DAG root. No rusqlite here.
//! SPORT: cascade-types module task — Task, TaskStatus, TaskPriority, TaskFilter, IPC params/results

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

// ── Core enumerations ────────────────────────────────────────────────────────

/// Board column / lifecycle state for a kanban Task.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum TaskStatus {
    /// Not started; sits in the backlog.
    #[default]
    Backlog,
    /// Committed to the current cycle but not started.
    Todo,
    /// Actively being worked on.
    InProgress,
    /// Awaiting review/approval.
    Review,
    /// Completed.
    Done,
    /// Removed from the active board; kept for history.
    Archived,
}

impl std::fmt::Display for TaskStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            TaskStatus::Backlog => "backlog",
            TaskStatus::Todo => "todo",
            TaskStatus::InProgress => "inProgress",
            TaskStatus::Review => "review",
            TaskStatus::Done => "done",
            TaskStatus::Archived => "archived",
        };
        write!(f, "{}", s)
    }
}

/// Priority level for a kanban Task.
#[derive(Debug, Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum TaskPriority {
    Low,
    #[default]
    Med,
    High,
    Urgent,
}

// ── Core type ────────────────────────────────────────────────────────────────

/// A single kanban card on a Cascade board.
///
/// Serialises with camelCase keys for JSON-RPC wire transport and the app UI.
/// All IDs are UUID v4 strings.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Task {
    /// Unique identifier (UUID v4).
    pub id: Uuid,

    /// Short card title (required, non-empty).
    pub title: String,

    /// Optional longer description / markdown body.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,

    /// Current board column.
    pub status: TaskStatus,

    /// Project path or slug this task belongs to.
    /// Empty string means "global / unassigned".
    pub project: String,

    /// Taxonomy tags for filtering and grouping.
    #[serde(default)]
    pub tags: Vec<String>,

    /// Optional assignee (email or username).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub assignee: Option<String>,

    /// Priority level.
    pub priority: TaskPriority,

    /// IDs of tasks that block this one (must be completed first).
    #[serde(default)]
    pub blockers: Vec<Uuid>,

    /// ISO 8601 creation timestamp (UTC).
    pub created_at: DateTime<Utc>,

    /// ISO 8601 last-update timestamp (UTC).
    pub updated_at: DateTime<Utc>,

    /// Optional due date (ISO 8601, UTC).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub due: Option<DateTime<Utc>>,

    /// Board display order within its column (lower = higher on board).
    /// Defaults to `i64::MAX` (new cards appear at the bottom).
    pub order: i64,
}

impl Task {
    /// Construct a minimal Task with required fields.
    ///
    /// # Arguments
    /// - `title` — Card title (non-empty string).
    /// - `project` — Project path or slug; pass `""` for global.
    ///
    /// All optional fields default to `None`/empty; status defaults to `Backlog`;
    /// priority defaults to `Med`; order defaults to `i64::MAX` (bottom of column).
    pub fn new(title: impl Into<String>, project: impl Into<String>) -> Self {
        let now = Utc::now();
        Self {
            id: Uuid::new_v4(),
            title: title.into(),
            description: None,
            status: TaskStatus::default(),
            project: project.into(),
            tags: Vec::new(),
            assignee: None,
            priority: TaskPriority::default(),
            blockers: Vec::new(),
            created_at: now,
            updated_at: now,
            due: None,
            order: i64::MAX,
        }
    }
}

// ── Filter type (used by TaskStore::list) ────────────────────────────────────

/// Optional filter set for `task_list` / `TaskStore::list`.
///
/// All fields are `Option`; `None` means "no filter on this dimension".
/// Multiple non-None fields are ANDed together.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct TaskFilter {
    /// Restrict to tasks in this project.
    pub project: Option<String>,

    /// Restrict to tasks with this status.
    pub status: Option<TaskStatus>,

    /// Restrict to tasks that include this tag.
    pub tag: Option<String>,

    /// Restrict to tasks assigned to this user.
    pub assignee: Option<String>,
}

// ── IPC wire types ────────────────────────────────────────────────────────────
// These structs correspond to the six task_* IPC methods exposed by the daemon.

// --- task_create ---

/// Params for `task_create`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskCreateParams {
    /// Card title (required).
    pub title: String,
    /// Optional description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// Project path or slug (pass `""` for global).
    #[serde(default)]
    pub project: String,
    /// Initial status (defaults to Backlog if omitted).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<TaskStatus>,
    /// Tags.
    #[serde(default)]
    pub tags: Vec<String>,
    /// Assignee.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub assignee: Option<String>,
    /// Priority (defaults to Med if omitted).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub priority: Option<TaskPriority>,
    /// Blocking task IDs.
    #[serde(default)]
    pub blockers: Vec<Uuid>,
    /// Due date.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub due: Option<DateTime<Utc>>,
    /// Board order hint (defaults to i64::MAX if omitted).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub order: Option<i64>,
}

/// Result for `task_create`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskCreateResult {
    /// The newly created task.
    pub task: Task,
}

// --- task_get ---

/// Params for `task_get`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskGetParams {
    /// Task ID to fetch.
    pub id: Uuid,
}

/// Result for `task_get`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskGetResult {
    /// The task, or null if not found.
    pub task: Option<Task>,
}

// --- task_update ---

/// Params for `task_update`.
///
/// All fields except `id` are `Option`. Only provided fields are updated;
/// absent fields leave the existing values unchanged (PATCH semantics).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskUpdateParams {
    /// ID of the task to update.
    pub id: Uuid,
    /// New title, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    /// New description, if changing. Pass `null` JSON to clear.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<Option<String>>,
    /// New status, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub status: Option<TaskStatus>,
    /// New project, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub project: Option<String>,
    /// Replacement tag list, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tags: Option<Vec<String>>,
    /// New assignee, if changing. Pass `null` JSON to clear.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub assignee: Option<Option<String>>,
    /// New priority, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub priority: Option<TaskPriority>,
    /// Replacement blocker list, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub blockers: Option<Vec<Uuid>>,
    /// New due date, if changing. Pass `null` JSON to clear.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub due: Option<Option<DateTime<Utc>>>,
    /// New board order, if changing.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub order: Option<i64>,
}

/// Result for `task_update`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskUpdateResult {
    /// The task after the update.
    pub task: Task,
}

// --- task_list ---

/// Params for `task_list`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct TaskListParams {
    /// Filter options (all optional).
    pub filter: TaskFilter,
}

/// Result for `task_list`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskListResult {
    /// Matching tasks, ordered by (project, status, order, created_at).
    pub tasks: Vec<Task>,
    /// Total count (useful for pagination stubs).
    pub total: usize,
}

// --- task_delete ---

/// Params for `task_delete`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskDeleteParams {
    /// ID of the task to delete.
    pub id: Uuid,
}

/// Result for `task_delete`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskDeleteResult {
    /// ID of the deleted task.
    pub id: Uuid,
    /// Whether the task existed and was deleted (`false` = already absent).
    pub deleted: bool,
}

// --- task_move ---

/// Params for `task_move` — change status column and/or board order.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskMoveParams {
    /// Task ID to move.
    pub id: Uuid,
    /// New status column.
    pub status: TaskStatus,
    /// New board order within the destination column.
    pub order: i64,
}

/// Result for `task_move`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskMoveResult {
    /// The task after the move.
    pub task: Task,
}
