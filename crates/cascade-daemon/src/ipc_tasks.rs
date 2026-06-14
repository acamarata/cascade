//! # cascade-daemon::ipc_tasks — kanban Task IPC handlers
//!
//! Purpose: Wire `task_create`, `task_update`, `task_list`, `task_get`,
//!   `task_delete`, and `task_move` to the `KanbanTaskStore`. These handlers
//!   are called from `try_typed_dispatch` in ipc.rs.
//!
//! Inputs:  JSON `params` values from the typed JSON-RPC dispatch path.
//! Outputs: `Response` (success = JSON task(s); error = -32xxx code + message).
//! Constraints: `KanbanTaskStore` is sync; handlers call `spawn_blocking` so the
//!   async IPC task is never blocked on SQLite I/O.
//! SPORT: cascade-daemon — task_create, task_update, task_list, task_get,
//!        task_delete, task_move IPC methods

use std::sync::Arc;

use cascade_core::tasks::KanbanTaskStore;
use cascade_types::task::{
    Task, TaskCreateParams, TaskDeleteParams, TaskGetParams, TaskListParams, TaskMoveParams,
    TaskUpdateParams,
};
use serde_json::Value;
use tokio::task::spawn_blocking;

use crate::ipc::Response;

// ── Handler functions (async wrappers around the sync store) ─────────────────

/// Handle `task_create`.
pub async fn handle_task_create(store: Arc<KanbanTaskStore>, params: Value) -> Response {
    let p: TaskCreateParams = match serde_json::from_value(params) {
        Ok(v) => v,
        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
    };

    let result = spawn_blocking(move || {
        let mut task = Task::new(&p.title, &p.project);
        if let Some(desc) = p.description {
            task.description = Some(desc);
        }
        if let Some(status) = p.status {
            task.status = status;
        }
        if !p.tags.is_empty() {
            task.tags = p.tags;
        }
        if let Some(assignee) = p.assignee {
            task.assignee = Some(assignee);
        }
        if let Some(priority) = p.priority {
            task.priority = priority;
        }
        if !p.blockers.is_empty() {
            task.blockers = p.blockers;
        }
        if let Some(due) = p.due {
            task.due = Some(due);
        }
        if let Some(order) = p.order {
            task.order = order;
        }
        store.create(&task)
    })
    .await;

    match result {
        Ok(Ok(task)) => Response::ok(serde_json::json!({ "task": task })),
        Ok(Err(e)) => Response::err(-32001, e.to_string()),
        Err(e) => Response::err(-32603, format!("spawn_blocking panic: {e}")),
    }
}

/// Handle `task_get`.
pub async fn handle_task_get(store: Arc<KanbanTaskStore>, params: Value) -> Response {
    let p: TaskGetParams = match serde_json::from_value(params) {
        Ok(v) => v,
        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
    };

    let result = spawn_blocking(move || store.get(p.id)).await;

    match result {
        Ok(Ok(task_opt)) => Response::ok(serde_json::json!({ "task": task_opt })),
        Ok(Err(e)) => Response::err(-32001, e.to_string()),
        Err(e) => Response::err(-32603, format!("spawn_blocking panic: {e}")),
    }
}

/// Handle `task_update`.
pub async fn handle_task_update(store: Arc<KanbanTaskStore>, params: Value) -> Response {
    let p: TaskUpdateParams = match serde_json::from_value(params) {
        Ok(v) => v,
        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
    };

    let result = spawn_blocking(move || store.update(&p)).await;

    match result {
        Ok(Ok(task)) => Response::ok(serde_json::json!({ "task": task })),
        Ok(Err(e)) => Response::err(-32001, e.to_string()),
        Err(e) => Response::err(-32603, format!("spawn_blocking panic: {e}")),
    }
}

/// Handle `task_list`.
pub async fn handle_task_list(store: Arc<KanbanTaskStore>, params: Value) -> Response {
    let p: TaskListParams = match serde_json::from_value(params) {
        Ok(v) => v,
        // Treat missing params as "list all"
        Err(_) => TaskListParams::default(),
    };

    let result = spawn_blocking(move || store.list(&p.filter)).await;

    match result {
        Ok(Ok(tasks)) => {
            let total = tasks.len();
            Response::ok(serde_json::json!({ "tasks": tasks, "total": total }))
        }
        Ok(Err(e)) => Response::err(-32001, e.to_string()),
        Err(e) => Response::err(-32603, format!("spawn_blocking panic: {e}")),
    }
}

/// Handle `task_delete`.
pub async fn handle_task_delete(store: Arc<KanbanTaskStore>, params: Value) -> Response {
    let p: TaskDeleteParams = match serde_json::from_value(params) {
        Ok(v) => v,
        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
    };

    let task_id = p.id;
    let result = spawn_blocking(move || store.delete(p.id)).await;

    match result {
        Ok(Ok(deleted)) => Response::ok(serde_json::json!({ "id": task_id, "deleted": deleted })),
        Ok(Err(e)) => Response::err(-32001, e.to_string()),
        Err(e) => Response::err(-32603, format!("spawn_blocking panic: {e}")),
    }
}

/// Handle `task_move`.
pub async fn handle_task_move(store: Arc<KanbanTaskStore>, params: Value) -> Response {
    let p: TaskMoveParams = match serde_json::from_value(params) {
        Ok(v) => v,
        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
    };

    let result = spawn_blocking(move || store.move_task(&p)).await;

    match result {
        Ok(Ok(task)) => Response::ok(serde_json::json!({ "task": task })),
        Ok(Err(e)) => Response::err(-32001, e.to_string()),
        Err(e) => Response::err(-32603, format!("spawn_blocking panic: {e}")),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::tasks::open_tasks_db;
    use tempfile::tempdir;
    use uuid::Uuid;

    fn make_store() -> Arc<KanbanTaskStore> {
        let dir = tempdir().unwrap();
        let conn = open_tasks_db(&dir.path().join("tasks.db")).unwrap();
        std::mem::forget(dir);
        Arc::new(KanbanTaskStore::new(conn))
    }

    // ── IPC handler dispatch tests ────────────────────────────────────────────

    #[tokio::test]
    async fn ipc_task_create_and_get() {
        let store = make_store();

        let params = serde_json::json!({
            "title": "IPC task",
            "project": "test-proj",
            "tags": ["ipc", "test"],
            "priority": "high"
        });
        let resp = handle_task_create(store.clone(), params).await;
        let val = assert_ok_json(resp);
        let task_id: Uuid = serde_json::from_value(val["task"]["id"].clone()).unwrap();

        // task_get
        let get_params = serde_json::json!({ "id": task_id });
        let get_resp = handle_task_get(store.clone(), get_params).await;
        let get_val = assert_ok_json(get_resp);
        assert_eq!(get_val["task"]["title"], "IPC task");
    }

    #[tokio::test]
    async fn ipc_task_list_returns_all() {
        let store = make_store();

        for i in 0..3 {
            let params = serde_json::json!({ "title": format!("Task {i}"), "project": "proj" });
            handle_task_create(store.clone(), params).await;
        }

        let list_resp = handle_task_list(store.clone(), serde_json::json!({})).await;
        let val = assert_ok_json(list_resp);
        assert_eq!(val["total"].as_u64().unwrap(), 3);
    }

    #[tokio::test]
    async fn ipc_task_update() {
        let store = make_store();
        let create_params = serde_json::json!({ "title": "Old title", "project": "proj" });
        let create_resp = handle_task_create(store.clone(), create_params).await;
        let val = assert_ok_json(create_resp);
        let task_id: Uuid = serde_json::from_value(val["task"]["id"].clone()).unwrap();

        let update_params = serde_json::json!({
            "id": task_id,
            "title": "New title",
            "status": "inProgress"
        });
        let update_resp = handle_task_update(store.clone(), update_params).await;
        let updated = assert_ok_json(update_resp);
        assert_eq!(updated["task"]["title"], "New title");
        assert_eq!(updated["task"]["status"], "inProgress");
    }

    #[tokio::test]
    async fn ipc_task_delete() {
        let store = make_store();
        let create_params = serde_json::json!({ "title": "To delete", "project": "proj" });
        let create_resp = handle_task_create(store.clone(), create_params).await;
        let val = assert_ok_json(create_resp);
        let task_id: Uuid = serde_json::from_value(val["task"]["id"].clone()).unwrap();

        let delete_params = serde_json::json!({ "id": task_id });
        let delete_resp = handle_task_delete(store.clone(), delete_params).await;
        let del_val = assert_ok_json(delete_resp);
        assert_eq!(del_val["deleted"], true);

        // Second delete returns false (not found)
        let delete_again =
            handle_task_delete(store.clone(), serde_json::json!({ "id": task_id })).await;
        let del_val2 = assert_ok_json(delete_again);
        assert_eq!(del_val2["deleted"], false);
    }

    #[tokio::test]
    async fn ipc_task_move() {
        let store = make_store();
        let create_params = serde_json::json!({ "title": "Movable", "project": "proj" });
        let create_resp = handle_task_create(store.clone(), create_params).await;
        let val = assert_ok_json(create_resp);
        let task_id: Uuid = serde_json::from_value(val["task"]["id"].clone()).unwrap();

        let move_params = serde_json::json!({
            "id": task_id,
            "status": "done",
            "order": 5
        });
        let move_resp = handle_task_move(store.clone(), move_params).await;
        let moved = assert_ok_json(move_resp);
        assert_eq!(moved["task"]["status"], "done");
        assert_eq!(moved["task"]["order"].as_i64().unwrap(), 5);
    }

    #[tokio::test]
    async fn ipc_task_create_invalid_params_returns_error() {
        let store = make_store();
        // Missing required `title`
        let resp = handle_task_create(store, serde_json::json!({ "project": "p" })).await;
        match resp {
            Response::Error { code, .. } => assert_eq!(code, -32602),
            _ => panic!("expected error response"),
        }
    }

    // ── Helper ────────────────────────────────────────────────────────────────

    fn assert_ok_json(resp: Response) -> Value {
        match resp {
            Response::Ok(v) => v,
            Response::Error { code, message } => {
                panic!("expected Ok response, got Error {code}: {message}")
            }
        }
    }
}
