//! # cascade-core::threads — personal thread/topic/archive CRUD
//!
//! Purpose: SQLite-backed CRUD for personal threads (collections of tasks with a
//!   directory + stage files on disk). Filesystem is canonical on startup;
//!   writes go to both DB and disk (write-through). fswatch live-sync is TODO.
//! Inputs:  Arc<Mutex<rusqlite::Connection>> from open_tasks_db.
//! Outputs: Result<T> from cascade-types.
//! Constraints: sync rusqlite wrapped in tokio::task::spawn_blocking for async callers.
//! SPORT: cascade-core threads module

mod store;
pub use store::ThreadStore;
pub use store::{Thread, ThreadTask, Topic, CreateThreadParams, CreateTaskParams,
                MoveTaskParams, SearchParams, SearchResult};
pub use store::{RagSink, NoopRagSink};
