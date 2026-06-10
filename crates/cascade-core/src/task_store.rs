#[cfg(test)]
use cascade_types::scheduled_task::ScheduleSpec;
use cascade_types::{scheduled_task::ScheduledTask, CascadeError, Result};
use chrono::{DateTime, Utc};
use rusqlite::{params, Connection, OptionalExtension};
use std::sync::{Arc, Mutex};
use uuid::Uuid;

/// TaskStore manages CRUD operations on scheduled tasks in SQLite.
/// It provides a thread-safe interface to the scheduled_tasks table.
pub struct TaskStore {
    conn: Arc<Mutex<Connection>>,
}

impl TaskStore {
    /// Create a new TaskStore from a shared database connection.
    pub fn new(conn: Arc<Mutex<Connection>>) -> Self {
        Self { conn }
    }

    /// Insert a new scheduled task into the database.
    pub fn insert(&self, task: &ScheduledTask) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let schedule_json = serde_json::to_string(&task.schedule)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize schedule: {}", e)))?;

        let args_json = serde_json::to_string(&task.args)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize args: {}", e)))?;

        let status_json = serde_json::to_string(&task.status)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize status: {}", e)))?;

        conn.execute(
            "INSERT INTO scheduled_tasks (id, name, schedule_json, command, args_json, enabled, created_at, last_run, next_run, status_json)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![
                task.id.to_string(),
                &task.name,
                &schedule_json,
                &task.command,
                &args_json,
                task.enabled as i32,
                task.created_at.to_rfc3339(),
                task.last_run.map(|dt| dt.to_rfc3339()),
                task.next_run.map(|dt| dt.to_rfc3339()),
                &status_json,
            ],
        )
        .map_err(|e| CascadeError::Other(format!("Failed to insert task: {}", e)))?;

        Ok(())
    }

    /// Get a task by ID.
    pub fn get(&self, id: Uuid) -> Result<Option<ScheduledTask>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let mut stmt = conn
            .prepare(
                "SELECT id, name, schedule_json, command, args_json, enabled, created_at, last_run, next_run, status_json
                 FROM scheduled_tasks WHERE id = ?",
            )
            .map_err(|e| CascadeError::Other(format!("Failed to prepare statement: {}", e)))?;

        let task = stmt
            .query_row([id.to_string()], |row| {
                let schedule_json: String = row.get(2)?;
                let args_json: String = row.get(4)?;
                let status_json: String = row.get(9)?;

                Ok(ScheduledTask {
                    id: Uuid::parse_str(&row.get::<_, String>(0)?).unwrap(),
                    name: row.get(1)?,
                    schedule: serde_json::from_str(&schedule_json).unwrap(),
                    command: row.get(3)?,
                    args: serde_json::from_str(&args_json).unwrap(),
                    enabled: row.get::<_, i32>(5)? != 0,
                    created_at: chrono::DateTime::parse_from_rfc3339(&row.get::<_, String>(6)?)
                        .unwrap()
                        .with_timezone(&chrono::Utc),
                    last_run: row.get::<_, Option<String>>(7)?.map(|s| {
                        chrono::DateTime::parse_from_rfc3339(&s)
                            .unwrap()
                            .with_timezone(&chrono::Utc)
                    }),
                    next_run: row.get::<_, Option<String>>(8)?.map(|s| {
                        chrono::DateTime::parse_from_rfc3339(&s)
                            .unwrap()
                            .with_timezone(&chrono::Utc)
                    }),
                    status: serde_json::from_str(&status_json).unwrap(),
                })
            })
            .optional()
            .map_err(|e| CascadeError::Other(format!("Query failed: {}", e)))?;

        Ok(task)
    }

    /// List all scheduled tasks.
    pub fn list(&self) -> Result<Vec<ScheduledTask>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let mut stmt = conn
            .prepare(
                "SELECT id, name, schedule_json, command, args_json, enabled, created_at, last_run, next_run, status_json
                 FROM scheduled_tasks ORDER BY created_at DESC",
            )
            .map_err(|e| CascadeError::Other(format!("Failed to prepare statement: {}", e)))?;

        let tasks = stmt
            .query_map([], |row| {
                let schedule_json: String = row.get(2)?;
                let args_json: String = row.get(4)?;
                let status_json: String = row.get(9)?;

                Ok(ScheduledTask {
                    id: Uuid::parse_str(&row.get::<_, String>(0)?).unwrap(),
                    name: row.get(1)?,
                    schedule: serde_json::from_str(&schedule_json).unwrap(),
                    command: row.get(3)?,
                    args: serde_json::from_str(&args_json).unwrap(),
                    enabled: row.get::<_, i32>(5)? != 0,
                    created_at: chrono::DateTime::parse_from_rfc3339(&row.get::<_, String>(6)?)
                        .unwrap()
                        .with_timezone(&chrono::Utc),
                    last_run: row.get::<_, Option<String>>(7)?.map(|s| {
                        chrono::DateTime::parse_from_rfc3339(&s)
                            .unwrap()
                            .with_timezone(&chrono::Utc)
                    }),
                    next_run: row.get::<_, Option<String>>(8)?.map(|s| {
                        chrono::DateTime::parse_from_rfc3339(&s)
                            .unwrap()
                            .with_timezone(&chrono::Utc)
                    }),
                    status: serde_json::from_str(&status_json).unwrap(),
                })
            })
            .map_err(|e| CascadeError::Other(format!("Query failed: {}", e)))?;

        let result: Result<Vec<_>> = tasks
            .map(|r| {
                r.map_err(|e| CascadeError::Other(format!("Row deserialization failed: {}", e)))
            })
            .collect();

        result
    }

    /// Update an existing task.
    pub fn update(&self, task: &ScheduledTask) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let schedule_json = serde_json::to_string(&task.schedule)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize schedule: {}", e)))?;

        let args_json = serde_json::to_string(&task.args)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize args: {}", e)))?;

        let status_json = serde_json::to_string(&task.status)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize status: {}", e)))?;

        conn.execute(
            "UPDATE scheduled_tasks
             SET name = ?, schedule_json = ?, command = ?, args_json = ?, enabled = ?, last_run = ?, next_run = ?, status_json = ?
             WHERE id = ?",
            params![
                &task.name,
                &schedule_json,
                &task.command,
                &args_json,
                task.enabled as i32,
                task.last_run.map(|dt| dt.to_rfc3339()),
                task.next_run.map(|dt| dt.to_rfc3339()),
                &status_json,
                task.id.to_string(),
            ],
        )
        .map_err(|e| CascadeError::Other(format!("Failed to update task: {}", e)))?;

        Ok(())
    }

    /// Return all enabled tasks whose next_run is <= `now`.
    ///
    /// A task with `next_run = NULL` is also included so that newly-inserted
    /// Once/Interval tasks that have never computed a next_run are picked up
    /// on the first poll.
    pub fn get_due(&self, now: DateTime<Utc>) -> Result<Vec<ScheduledTask>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let now_str = now.to_rfc3339();

        let mut stmt = conn
            .prepare(
                "SELECT id, name, schedule_json, command, args_json, enabled, created_at, last_run, next_run, status_json
                 FROM scheduled_tasks
                 WHERE enabled = 1 AND (next_run IS NULL OR next_run <= ?)
                 ORDER BY next_run ASC NULLS FIRST",
            )
            .map_err(|e| CascadeError::Other(format!("Failed to prepare get_due statement: {}", e)))?;

        let tasks = stmt
            .query_map([&now_str], |row| {
                let schedule_json: String = row.get(2)?;
                let args_json: String = row.get(4)?;
                let status_json: String = row.get(9)?;

                Ok(ScheduledTask {
                    id: Uuid::parse_str(&row.get::<_, String>(0)?).unwrap(),
                    name: row.get(1)?,
                    schedule: serde_json::from_str(&schedule_json).unwrap(),
                    command: row.get(3)?,
                    args: serde_json::from_str(&args_json).unwrap(),
                    enabled: row.get::<_, i32>(5)? != 0,
                    created_at: chrono::DateTime::parse_from_rfc3339(&row.get::<_, String>(6)?)
                        .unwrap()
                        .with_timezone(&chrono::Utc),
                    last_run: row.get::<_, Option<String>>(7)?.map(|s| {
                        chrono::DateTime::parse_from_rfc3339(&s)
                            .unwrap()
                            .with_timezone(&chrono::Utc)
                    }),
                    next_run: row.get::<_, Option<String>>(8)?.map(|s| {
                        chrono::DateTime::parse_from_rfc3339(&s)
                            .unwrap()
                            .with_timezone(&chrono::Utc)
                    }),
                    status: serde_json::from_str(&status_json).unwrap(),
                })
            })
            .map_err(|e| CascadeError::Other(format!("get_due query failed: {}", e)))?;

        let result: Result<Vec<_>> = tasks
            .map(|r| {
                r.map_err(|e| CascadeError::Other(format!("Row deserialization failed: {}", e)))
            })
            .collect();

        result
    }

    /// Delete a task by ID.
    pub fn delete(&self, id: Uuid) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        conn.execute("DELETE FROM scheduled_tasks WHERE id = ?", [id.to_string()])
            .map_err(|e| CascadeError::Other(format!("Failed to delete task: {}", e)))?;

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    fn setup_test_db() -> Result<(Arc<Mutex<Connection>>, TaskStore)> {
        let conn = Connection::open(":memory:")
            .map_err(|e| CascadeError::Other(format!("Failed to open memory db: {}", e)))?;

        // Create the table
        conn.execute(
            "CREATE TABLE scheduled_tasks (
                id TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                schedule_json TEXT NOT NULL,
                command TEXT NOT NULL,
                args_json TEXT NOT NULL,
                enabled INTEGER NOT NULL DEFAULT 1,
                created_at TEXT NOT NULL,
                last_run TEXT,
                next_run TEXT,
                status_json TEXT NOT NULL
            )",
            [],
        )
        .map_err(|e| CascadeError::Other(format!("Failed to create table: {}", e)))?;

        let conn_arc = Arc::new(Mutex::new(conn));
        let store = TaskStore::new(conn_arc.clone());
        Ok((conn_arc, store))
    }

    #[test]
    fn test_insert_and_get() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let task = ScheduledTask::new(
            "test task",
            ScheduleSpec::Interval { secs: 60 },
            "echo",
            vec!["hello".to_string()],
        );

        let task_id = task.id;
        store.insert(&task)?;

        let retrieved = store.get(task_id)?;
        assert!(retrieved.is_some());
        let retrieved_task = retrieved.unwrap();
        assert_eq!(retrieved_task.name, "test task");
        assert_eq!(retrieved_task.command, "echo");

        Ok(())
    }

    #[test]
    fn test_list_tasks() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let task1 = ScheduledTask::new(
            "task 1",
            ScheduleSpec::Cron {
                expression: "0 9 * * *".to_string(),
            },
            "cmd1",
            vec![],
        );

        let task2 = ScheduledTask::new(
            "task 2",
            ScheduleSpec::Interval { secs: 300 },
            "cmd2",
            vec!["arg1".to_string()],
        );

        store.insert(&task1)?;
        store.insert(&task2)?;

        let tasks = store.list()?;
        assert_eq!(tasks.len(), 2);

        Ok(())
    }

    #[test]
    fn test_update_task() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let mut task = ScheduledTask::new(
            "original",
            ScheduleSpec::Interval { secs: 60 },
            "cmd",
            vec![],
        );

        let task_id = task.id;
        store.insert(&task)?;

        // Update the task
        task.name = "updated".to_string();
        store.update(&task)?;

        let retrieved = store.get(task_id)?;
        assert!(retrieved.is_some());
        assert_eq!(retrieved.unwrap().name, "updated");

        Ok(())
    }

    #[test]
    fn test_delete_task() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let task = ScheduledTask::new(
            "to delete",
            ScheduleSpec::Interval { secs: 60 },
            "cmd",
            vec![],
        );

        let task_id = task.id;
        store.insert(&task)?;

        // Verify it exists
        assert!(store.get(task_id)?.is_some());

        // Delete it
        store.delete(task_id)?;

        // Verify it's gone
        assert!(store.get(task_id)?.is_none());

        Ok(())
    }

    #[test]
    fn test_cron_validation() -> Result<()> {
        let spec = ScheduleSpec::Cron {
            expression: "0 9 * * *".to_string(),
        };
        assert!(spec.validate().is_ok());

        let invalid_spec = ScheduleSpec::Cron {
            expression: "not a cron".to_string(),
        };
        assert!(invalid_spec.validate().is_err());

        Ok(())
    }
}
