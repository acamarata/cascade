//! TaskStore — in-memory task state store for the executor.
//!
//! Purpose: thin wrapper over a shared HashMap so tests can inspect task
//!   states without a full database.
//! Inputs: `AgentTask` values.
//! Outputs: cloned task lookups; drained Vec for assertions.
//! Constraints: all access is behind a Mutex; Clone is cheap (Arc-backed).
//! SPORT: cascade-agents / executor / store

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use crate::task::AgentTask;

/// In-memory store for task state (used by the executor).
///
/// A thin wrapper so tests can inspect task states without a full DB.
#[derive(Clone, Default)]
pub struct TaskStore {
    inner: Arc<Mutex<HashMap<String, AgentTask>>>,
}

impl TaskStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// Upsert a task.
    pub fn upsert(&self, task: AgentTask) {
        self.inner.lock().unwrap().insert(task.id.clone(), task);
    }

    /// Get a task by id.
    pub fn get(&self, id: &str) -> Option<AgentTask> {
        self.inner.lock().unwrap().get(id).cloned()
    }

    /// Drain all stored tasks (for test assertions).
    pub fn drain(&self) -> Vec<AgentTask> {
        self.inner.lock().unwrap().values().cloned().collect()
    }
}
