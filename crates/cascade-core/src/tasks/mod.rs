//! # cascade-core::tasks — SQLite-backed kanban TaskStore
//!
//! Purpose: Persistent, thread-safe CRUD store for user-facing kanban Tasks.
//!   Opens (or creates) `tasks.db` inside the ai_folder (`~/.cascade/`).
//!   Schema migrations are idempotent (CREATE TABLE IF NOT EXISTS + ALTER TABLE
//!   IF NOT EXISTS guards). Survives daemon restarts.
//!
//! Inputs:  `Task`, `TaskFilter`, `TaskUpdateParams`, `TaskMoveParams` from cascade-types.
//! Outputs: `Result<T>` where T is the relevant cascade-types IPC result type.
//! Constraints: `rusqlite` sync API wrapped in `Arc<Mutex<Connection>>`; safe to
//!   share across tokio tasks via `Arc<KanbanTaskStore>`.
//! SPORT: cascade-core tasks module — KanbanTaskStore, open_tasks_db

mod migrations;
mod queries;

pub use migrations::open_tasks_db;
pub use queries::KanbanTaskStore;
