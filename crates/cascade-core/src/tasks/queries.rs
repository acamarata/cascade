//! # cascade-core::tasks::queries — KanbanTaskStore CRUD
//!
//! Purpose: All database operations for the kanban Task entity. Wraps a
//!   `Arc<Mutex<Connection>>` so it is safe to clone and share across tokio tasks.
//!
//! Inputs:  `Task`, `TaskFilter`, `TaskUpdateParams`, `TaskMoveParams`
//! Outputs: `Result<Task>` / `Result<Vec<Task>>` / `Result<bool>`
//! Constraints: sync API; callers in async context should use `tokio::task::spawn_blocking`.
//! SPORT: cascade-core tasks queries — KanbanTaskStore methods

use std::sync::{Arc, Mutex};

use cascade_types::{
    task::{Task, TaskFilter, TaskMoveParams, TaskPriority, TaskStatus, TaskUpdateParams},
    CascadeError, Result,
};
use chrono::{DateTime, Utc};
use rusqlite::{params, Connection, OptionalExtension};
use uuid::Uuid;

// ── Store ─────────────────────────────────────────────────────────────────────

/// Thread-safe SQLite-backed store for kanban Tasks.
#[derive(Clone)]
pub struct KanbanTaskStore {
    conn: Arc<Mutex<Connection>>,
}

impl KanbanTaskStore {
    /// Construct from a shared (already-migrated) connection.
    pub fn new(conn: Arc<Mutex<Connection>>) -> Self {
        Self { conn }
    }

    // ── Write operations ──────────────────────────────────────────────────────

    /// Insert a new task. Returns the inserted task (with server-assigned id/timestamps).
    pub fn create(&self, task: &Task) -> Result<Task> {
        let conn = self.lock()?;
        let tags_json = serde_json::to_string(&task.tags)
            .map_err(|e| CascadeError::Other(format!("serialize tags: {e}")))?;
        let blockers_json = serde_json::to_string(
            &task
                .blockers
                .iter()
                .map(|u| u.to_string())
                .collect::<Vec<_>>(),
        )
        .map_err(|e| CascadeError::Other(format!("serialize blockers: {e}")))?;

        conn.execute(
            "INSERT INTO kanban_tasks
             (id, title, description, status, project, tags_json, assignee, priority,
              blockers_json, created_at, updated_at, due, ord)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![
                task.id.to_string(),
                &task.title,
                &task.description,
                task.status.to_string(),
                &task.project,
                &tags_json,
                &task.assignee,
                priority_to_str(&task.priority),
                &blockers_json,
                task.created_at.to_rfc3339(),
                task.updated_at.to_rfc3339(),
                task.due.map(|d| d.to_rfc3339()),
                task.order,
            ],
        )
        .map_err(|e| CascadeError::Other(format!("insert task: {e}")))?;

        Ok(task.clone())
    }

    /// Retrieve a task by ID. Returns `None` if not found.
    pub fn get(&self, id: Uuid) -> Result<Option<Task>> {
        let conn = self.lock()?;
        let mut stmt = conn
            .prepare(
                "SELECT id, title, description, status, project, tags_json, assignee, priority,
                        blockers_json, created_at, updated_at, due, ord
                 FROM kanban_tasks WHERE id = ?",
            )
            .map_err(|e| CascadeError::Other(format!("prepare get: {e}")))?;

        stmt.query_row([id.to_string()], row_to_task)
            .optional()
            .map_err(|e| CascadeError::Other(format!("get task: {e}")))
    }

    /// Update an existing task with PATCH semantics.
    ///
    /// Only fields set to `Some(...)` in `params` are written. The `updated_at`
    /// timestamp is always bumped to `Utc::now()`.
    pub fn update(&self, params: &TaskUpdateParams) -> Result<Task> {
        // Fetch the current task first so we can apply partial updates.
        let mut task = self
            .get(params.id)?
            .ok_or_else(|| CascadeError::Other(format!("task not found: {}", params.id)))?;

        if let Some(title) = &params.title {
            task.title = title.clone();
        }
        if let Some(desc_opt) = &params.description {
            task.description = desc_opt.clone();
        }
        if let Some(status) = &params.status {
            task.status = status.clone();
        }
        if let Some(project) = &params.project {
            task.project = project.clone();
        }
        if let Some(tags) = &params.tags {
            task.tags = tags.clone();
        }
        if let Some(assignee_opt) = &params.assignee {
            task.assignee = assignee_opt.clone();
        }
        if let Some(priority) = &params.priority {
            task.priority = priority.clone();
        }
        if let Some(blockers) = &params.blockers {
            task.blockers = blockers.clone();
        }
        if let Some(due_opt) = &params.due {
            task.due = *due_opt;
        }
        if let Some(order) = params.order {
            task.order = order;
        }
        task.updated_at = Utc::now();

        self.write_full(&task)?;
        Ok(task)
    }

    /// Move a task to a different column (status) and/or board position.
    pub fn move_task(&self, params: &TaskMoveParams) -> Result<Task> {
        let mut task = self
            .get(params.id)?
            .ok_or_else(|| CascadeError::Other(format!("task not found: {}", params.id)))?;

        task.status = params.status.clone();
        task.order = params.order;
        task.updated_at = Utc::now();

        self.write_full(&task)?;
        Ok(task)
    }

    /// Delete a task by ID. Returns `true` if deleted, `false` if not found.
    pub fn delete(&self, id: Uuid) -> Result<bool> {
        let conn = self.lock()?;
        let rows = conn
            .execute("DELETE FROM kanban_tasks WHERE id = ?", [id.to_string()])
            .map_err(|e| CascadeError::Other(format!("delete task: {e}")))?;
        Ok(rows > 0)
    }

    // ── Read operations ───────────────────────────────────────────────────────

    /// List tasks with optional filtering.
    ///
    /// Results are ordered by `(project, status, ord, created_at)`.
    /// When `filter` is all-None, returns every task (cross-project dashboard).
    pub fn list(&self, filter: &TaskFilter) -> Result<Vec<Task>> {
        let conn = self.lock()?;

        // Build query dynamically from filter fields.
        let mut conditions: Vec<String> = Vec::new();
        let mut bind_values: Vec<String> = Vec::new();

        if let Some(project) = &filter.project {
            conditions.push("project = ?".to_string());
            bind_values.push(project.clone());
        }
        if let Some(status) = &filter.status {
            conditions.push("status = ?".to_string());
            bind_values.push(status.to_string());
        }
        if let Some(tag) = &filter.tag {
            // Simple LIKE search within the JSON array; fast enough for typical board sizes.
            conditions.push("tags_json LIKE ?".to_string());
            bind_values.push(format!("%{}%", tag.replace('%', "\\%").replace('_', "\\_")));
        }
        if let Some(assignee) = &filter.assignee {
            conditions.push("assignee = ?".to_string());
            bind_values.push(assignee.clone());
        }

        let where_clause = if conditions.is_empty() {
            String::new()
        } else {
            format!("WHERE {}", conditions.join(" AND "))
        };

        let sql = format!(
            "SELECT id, title, description, status, project, tags_json, assignee, priority,
                    blockers_json, created_at, updated_at, due, ord
             FROM kanban_tasks
             {}
             ORDER BY project ASC, status ASC, ord ASC, created_at ASC",
            where_clause
        );

        let mut stmt = conn
            .prepare(&sql)
            .map_err(|e| CascadeError::Other(format!("prepare list: {e}")))?;

        let tasks = stmt
            .query_map(rusqlite::params_from_iter(bind_values.iter()), row_to_task)
            .map_err(|e| CascadeError::Other(format!("list query: {e}")))?
            .map(|r| r.map_err(|e| CascadeError::Other(format!("row deserialization: {e}"))))
            .collect::<Result<Vec<_>>>()?;

        Ok(tasks)
    }

    // ── Internal helpers ──────────────────────────────────────────────────────

    fn lock(&self) -> Result<std::sync::MutexGuard<'_, Connection>> {
        self.conn
            .lock()
            .map_err(|_| CascadeError::Other("tasks db mutex poisoned".into()))
    }

    /// Full-row UPDATE (used by update/move_task after computing the new state in Rust).
    fn write_full(&self, task: &Task) -> Result<()> {
        let conn = self.lock()?;
        let tags_json = serde_json::to_string(&task.tags)
            .map_err(|e| CascadeError::Other(format!("serialize tags: {e}")))?;
        let blockers_json = serde_json::to_string(
            &task
                .blockers
                .iter()
                .map(|u| u.to_string())
                .collect::<Vec<_>>(),
        )
        .map_err(|e| CascadeError::Other(format!("serialize blockers: {e}")))?;

        let rows = conn
            .execute(
                "UPDATE kanban_tasks
                 SET title = ?, description = ?, status = ?, project = ?, tags_json = ?,
                     assignee = ?, priority = ?, blockers_json = ?,
                     updated_at = ?, due = ?, ord = ?
                 WHERE id = ?",
                params![
                    &task.title,
                    &task.description,
                    task.status.to_string(),
                    &task.project,
                    &tags_json,
                    &task.assignee,
                    priority_to_str(&task.priority),
                    &blockers_json,
                    task.updated_at.to_rfc3339(),
                    task.due.map(|d| d.to_rfc3339()),
                    task.order,
                    task.id.to_string(),
                ],
            )
            .map_err(|e| CascadeError::Other(format!("update task: {e}")))?;

        if rows == 0 {
            return Err(CascadeError::Other(format!(
                "task not found for update: {}",
                task.id
            )));
        }
        Ok(())
    }
}

// ── Row mapping ───────────────────────────────────────────────────────────────

fn row_to_task(row: &rusqlite::Row<'_>) -> rusqlite::Result<Task> {
    let id_str: String = row.get(0)?;
    let tags_json: String = row.get(5)?;
    let priority_str: String = row.get(7)?;
    let blockers_json: String = row.get(8)?;
    let status_str: String = row.get(3)?;
    let created_str: String = row.get(9)?;
    let updated_str: String = row.get(10)?;
    let due_str: Option<String> = row.get(11)?;

    let id = Uuid::parse_str(&id_str).unwrap_or_else(|_| Uuid::nil());
    let tags: Vec<String> = serde_json::from_str(&tags_json).unwrap_or_default();
    let blocker_strs: Vec<String> = serde_json::from_str(&blockers_json).unwrap_or_default();
    let blockers: Vec<Uuid> = blocker_strs
        .iter()
        .filter_map(|s| Uuid::parse_str(s).ok())
        .collect();
    let priority = str_to_priority(&priority_str);
    let status = str_to_status(&status_str);
    let created_at = DateTime::parse_from_rfc3339(&created_str)
        .map(|dt| dt.with_timezone(&Utc))
        .unwrap_or_else(|_| Utc::now());
    let updated_at = DateTime::parse_from_rfc3339(&updated_str)
        .map(|dt| dt.with_timezone(&Utc))
        .unwrap_or_else(|_| Utc::now());
    let due = due_str.and_then(|s| {
        DateTime::parse_from_rfc3339(&s)
            .ok()
            .map(|dt| dt.with_timezone(&Utc))
    });

    Ok(Task {
        id,
        title: row.get(1)?,
        description: row.get(2)?,
        status,
        project: row.get(4)?,
        tags,
        assignee: row.get(6)?,
        priority,
        blockers,
        created_at,
        updated_at,
        due,
        order: row.get(12)?,
    })
}

fn priority_to_str(p: &TaskPriority) -> &'static str {
    match p {
        TaskPriority::Low => "low",
        TaskPriority::Med => "med",
        TaskPriority::High => "high",
        TaskPriority::Urgent => "urgent",
    }
}

fn str_to_priority(s: &str) -> TaskPriority {
    match s {
        "low" => TaskPriority::Low,
        "high" => TaskPriority::High,
        "urgent" => TaskPriority::Urgent,
        _ => TaskPriority::Med,
    }
}

fn str_to_status(s: &str) -> TaskStatus {
    match s {
        "todo" => TaskStatus::Todo,
        "inProgress" => TaskStatus::InProgress,
        "review" => TaskStatus::Review,
        "done" => TaskStatus::Done,
        "archived" => TaskStatus::Archived,
        _ => TaskStatus::Backlog,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tasks::open_tasks_db;
    use cascade_types::task::{TaskFilter, TaskMoveParams, TaskStatus, TaskUpdateParams};
    use tempfile::tempdir;

    fn make_store() -> KanbanTaskStore {
        let dir = tempdir().unwrap();
        let conn = open_tasks_db(&dir.path().join("tasks.db")).unwrap();
        // Keep `dir` alive by leaking it into the test
        std::mem::forget(dir);
        KanbanTaskStore::new(conn)
    }

    fn make_store_with_dir() -> (KanbanTaskStore, tempfile::TempDir) {
        let dir = tempdir().unwrap();
        let conn = open_tasks_db(&dir.path().join("tasks.db")).unwrap();
        (KanbanTaskStore::new(conn), dir)
    }

    // ── CRUD round-trip ───────────────────────────────────────────────────────

    #[test]
    fn crud_round_trip() {
        let store = make_store();
        let task = Task::new("Buy milk", "personal");
        let created = store.create(&task).unwrap();
        assert_eq!(created.id, task.id);
        assert_eq!(created.title, "Buy milk");
        assert_eq!(created.project, "personal");

        let fetched = store.get(task.id).unwrap().expect("task should exist");
        assert_eq!(fetched.title, "Buy milk");
        assert_eq!(fetched.status, TaskStatus::Backlog);

        let delete_result = store.delete(task.id).unwrap();
        assert!(delete_result);
        assert!(store.get(task.id).unwrap().is_none());
    }

    #[test]
    fn delete_returns_false_when_absent() {
        let store = make_store();
        let missing = Uuid::new_v4();
        let result = store.delete(missing).unwrap();
        assert!(!result);
    }

    // ── Update ────────────────────────────────────────────────────────────────

    #[test]
    fn update_partial_fields() {
        let store = make_store();
        let task = Task::new("Original title", "proj-a");
        store.create(&task).unwrap();

        let params = TaskUpdateParams {
            id: task.id,
            title: Some("Updated title".to_string()),
            description: None,
            status: Some(TaskStatus::InProgress),
            project: None,
            tags: Some(vec!["rust".to_string(), "core".to_string()]),
            assignee: None,
            priority: None,
            blockers: None,
            due: None,
            order: None,
        };
        let updated = store.update(&params).unwrap();
        assert_eq!(updated.title, "Updated title");
        assert_eq!(updated.status, TaskStatus::InProgress);
        assert_eq!(updated.tags, vec!["rust", "core"]);
        // Unchanged fields preserved
        assert_eq!(updated.project, "proj-a");
    }

    // ── List filters ─────────────────────────────────────────────────────────

    #[test]
    fn list_filter_by_project() {
        let store = make_store();
        let t1 = Task::new("Task in A", "proj-a");
        let t2 = Task::new("Task in B", "proj-b");
        let t3 = Task::new("Another A", "proj-a");
        store.create(&t1).unwrap();
        store.create(&t2).unwrap();
        store.create(&t3).unwrap();

        let filter = TaskFilter {
            project: Some("proj-a".to_string()),
            ..Default::default()
        };
        let results = store.list(&filter).unwrap();
        assert_eq!(results.len(), 2);
        assert!(results.iter().all(|t| t.project == "proj-a"));
    }

    #[test]
    fn list_filter_by_status() {
        let store = make_store();
        let mut t_done = Task::new("Done task", "proj");
        t_done.status = TaskStatus::Done;
        let t_backlog = Task::new("Backlog task", "proj");
        store.create(&t_done).unwrap();
        store.create(&t_backlog).unwrap();

        let filter = TaskFilter {
            status: Some(TaskStatus::Done),
            ..Default::default()
        };
        let results = store.list(&filter).unwrap();
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].status, TaskStatus::Done);
    }

    #[test]
    fn list_filter_by_tag() {
        let store = make_store();
        let mut t_tagged = Task::new("Tagged", "proj");
        t_tagged.tags = vec!["urgent".to_string()];
        let t_plain = Task::new("Plain", "proj");
        store.create(&t_tagged).unwrap();
        store.create(&t_plain).unwrap();

        let filter = TaskFilter {
            tag: Some("urgent".to_string()),
            ..Default::default()
        };
        let results = store.list(&filter).unwrap();
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].title, "Tagged");
    }

    #[test]
    fn list_filter_by_assignee() {
        let store = make_store();
        let mut t_assigned = Task::new("Assigned", "proj");
        t_assigned.assignee = Some("alice@example.com".to_string());
        let t_unassigned = Task::new("Unassigned", "proj");
        store.create(&t_assigned).unwrap();
        store.create(&t_unassigned).unwrap();

        let filter = TaskFilter {
            assignee: Some("alice@example.com".to_string()),
            ..Default::default()
        };
        let results = store.list(&filter).unwrap();
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].title, "Assigned");
    }

    // ── Cross-project aggregation ─────────────────────────────────────────────

    #[test]
    fn cross_project_list_no_filter() {
        let store = make_store();
        store.create(&Task::new("T1", "alpha")).unwrap();
        store.create(&Task::new("T2", "beta")).unwrap();
        store.create(&Task::new("T3", "gamma")).unwrap();

        let all = store.list(&TaskFilter::default()).unwrap();
        assert_eq!(all.len(), 3);
        let projects: Vec<&str> = all.iter().map(|t| t.project.as_str()).collect();
        assert!(projects.contains(&"alpha"));
        assert!(projects.contains(&"beta"));
        assert!(projects.contains(&"gamma"));
    }

    // ── Reorder / move ────────────────────────────────────────────────────────

    #[test]
    fn reorder_and_move() {
        let store = make_store();
        let task = Task::new("Movable", "proj");
        store.create(&task).unwrap();

        let move_params = TaskMoveParams {
            id: task.id,
            status: TaskStatus::InProgress,
            order: 10,
        };
        let moved = store.move_task(&move_params).unwrap();
        assert_eq!(moved.status, TaskStatus::InProgress);
        assert_eq!(moved.order, 10);

        let fetched = store.get(task.id).unwrap().unwrap();
        assert_eq!(fetched.order, 10);
        assert_eq!(fetched.status, TaskStatus::InProgress);
    }

    // ── Blockers ──────────────────────────────────────────────────────────────

    #[test]
    fn blockers_stored_and_retrieved() {
        let store = make_store();
        let blocker1 = Uuid::new_v4();
        let blocker2 = Uuid::new_v4();
        let mut task = Task::new("Blocked task", "proj");
        task.blockers = vec![blocker1, blocker2];
        store.create(&task).unwrap();

        let fetched = store.get(task.id).unwrap().unwrap();
        assert_eq!(fetched.blockers.len(), 2);
        assert!(fetched.blockers.contains(&blocker1));
        assert!(fetched.blockers.contains(&blocker2));
    }

    // ── Migration idempotency ─────────────────────────────────────────────────

    #[test]
    fn migration_idempotent_with_data() {
        let (store, dir) = make_store_with_dir();
        store.create(&Task::new("Persisted", "proj")).unwrap();

        // Re-open — migrations run again, data must survive.
        let conn2 = open_tasks_db(&dir.path().join("tasks.db")).unwrap();
        let store2 = KanbanTaskStore::new(conn2);
        let all = store2.list(&TaskFilter::default()).unwrap();
        assert_eq!(all.len(), 1);
        assert_eq!(all[0].title, "Persisted");
    }
}
