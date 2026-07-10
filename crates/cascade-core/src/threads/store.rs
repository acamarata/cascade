//! Purpose: ThreadStore — CRUD for threads, thread_tasks, topics, thread_topics.
//!   Also manages on-disk directories and stage markdown files (write-through).
//!   Filesystem structure:
//!     {threads_root}/{thread_id}/README.md        — thread overview
//!     {threads_root}/{thread_id}/todo.md          — todo tasks
//!     {threads_root}/{thread_id}/in_progress.md  — in-progress tasks
//!     {threads_root}/{thread_id}/done.md          — done tasks
//!     {threads_root}/archive/{thread_id}/        — archived threads (never deleted)
//! Inputs:  Arc<Mutex<Connection>>, threads_root PathBuf
//! Outputs: Result<T>
//! Constraints: all DB ops sync; disk writes via std::fs.
//! SPORT: cascade-core threads module — ThreadStore

use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use cascade_types::{CascadeError, Result};
use chrono::Utc;
use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

// ── RagSink trait ────────────────────────────────────────────────────────────

/// Abstracts writing an episodic memory entry to the personal RAG namespace.
///
/// Purpose: breaks the dep-cycle between cascade-core (no cascade-rag dep) and
///   the actual rag-08 memory engine in cascade-rag. cascade-daemon, which
///   depends on both, injects a real implementation at call sites.
/// Inputs:  episode content string
/// Outputs: Ok(()) or CascadeError::Other
/// Constraints: must not be called for sensitivity='locked' threads (enforced
///   by push_to_rag before calling this).
pub trait RagSink: Send + Sync {
    /// Append one episode to the personal RAG namespace.
    fn push_episode(&self, content: &str) -> Result<()>;
}

/// No-op implementation — used when no real sink has been injected.
pub struct NoopRagSink;

impl RagSink for NoopRagSink {
    fn push_episode(&self, _content: &str) -> Result<()> {
        Ok(())
    }
}

// ── Domain types ─────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Thread {
    pub id: String,
    pub parent_id: Option<String>,
    pub title: String,
    pub sensitivity: String,
    pub created_at: String,
    pub archived: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ThreadTask {
    pub id: String,
    pub thread_id: String,
    pub title: String,
    pub stage: String,
    pub notes: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct Topic {
    pub id: String,
    pub name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateThreadParams {
    pub title: String,
    pub parent_id: Option<String>,
    pub sensitivity: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateTaskParams {
    pub thread_id: String,
    pub title: String,
    pub stage: Option<String>,
    pub notes: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MoveTaskParams {
    pub task_id: String,
    pub new_stage: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchParams {
    pub query: String,
    pub topic: Option<String>,
    pub include_archived: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResult {
    pub threads: Vec<Thread>,
    pub tasks: Vec<ThreadTask>,
}

// ── Store ────────────────────────────────────────────────────────────────────

/// SQLite + filesystem CRUD for threads, tasks, and topics.
pub struct ThreadStore {
    conn: Arc<Mutex<Connection>>,
    /// Root directory for thread dirs (e.g. `~/.cascade/threads/`).
    threads_root: PathBuf,
}

impl ThreadStore {
    pub fn new(conn: Arc<Mutex<Connection>>, threads_root: PathBuf) -> Self {
        Self { conn, threads_root }
    }

    // ── threads ──────────────────────────────────────────────────────────────

    /// Create a new thread: insert into DB + create on-disk directory with stage files.
    pub fn create_thread(&self, params: CreateThreadParams) -> Result<Thread> {
        let id = Uuid::new_v4().to_string();
        let created_at = Utc::now().to_rfc3339();
        let sensitivity = params.sensitivity.unwrap_or_else(|| "normal".to_string());

        {
            let conn = self
                .conn
                .lock()
                .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
            conn.execute(
                "INSERT INTO threads (id, parent_id, title, sensitivity, created_at, archived)
                 VALUES (?1, ?2, ?3, ?4, ?5, 0)",
                params![id, params.parent_id, params.title, sensitivity, created_at],
            )
            .map_err(|e| CascadeError::Other(format!("create_thread insert: {e}")))?;
        }

        let thread = Thread {
            id: id.clone(),
            parent_id: params.parent_id,
            title: params.title.clone(),
            sensitivity: sensitivity.clone(),
            created_at: created_at.clone(),
            archived: false,
        };

        // Write-through: create on-disk structure.
        self.init_thread_dir(&self.threads_root.join(&id), &params.title)?;

        Ok(thread)
    }

    /// List all active (non-archived) threads, ordered by created_at DESC.
    pub fn list_threads(&self, include_archived: bool) -> Result<Vec<Thread>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
        let mut threads = Vec::new();
        if include_archived {
            let mut stmt = conn
                .prepare(
                    "SELECT id, parent_id, title, sensitivity, created_at, archived
                 FROM threads ORDER BY created_at DESC",
                )
                .map_err(|e| CascadeError::Other(format!("list_threads prepare: {e}")))?;
            let rows = stmt
                .query_map([], |row| {
                    Ok(Thread {
                        id: row.get(0)?,
                        parent_id: row.get(1)?,
                        title: row.get(2)?,
                        sensitivity: row.get(3)?,
                        created_at: row.get(4)?,
                        archived: row.get::<_, i64>(5)? != 0,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("list_threads query: {e}")))?;
            for r in rows.flatten() {
                threads.push(r);
            }
        } else {
            let mut stmt = conn
                .prepare(
                    "SELECT id, parent_id, title, sensitivity, created_at, archived
                 FROM threads WHERE archived = 0 ORDER BY created_at DESC",
                )
                .map_err(|e| CascadeError::Other(format!("list_threads prepare: {e}")))?;
            let rows = stmt
                .query_map([], |row| {
                    Ok(Thread {
                        id: row.get(0)?,
                        parent_id: row.get(1)?,
                        title: row.get(2)?,
                        sensitivity: row.get(3)?,
                        created_at: row.get(4)?,
                        archived: row.get::<_, i64>(5)? != 0,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("list_threads query: {e}")))?;
            for r in rows.flatten() {
                threads.push(r);
            }
        }
        Ok(threads)
    }

    /// Get a single thread by ID.
    pub fn get_thread(&self, id: &str) -> Result<Option<Thread>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
        let mut stmt = conn
            .prepare(
                "SELECT id, parent_id, title, sensitivity, created_at, archived
                 FROM threads WHERE id = ?1",
            )
            .map_err(|e| CascadeError::Other(format!("get_thread prepare: {e}")))?;
        let mut rows = stmt
            .query_map([id], |row| {
                Ok(Thread {
                    id: row.get(0)?,
                    parent_id: row.get(1)?,
                    title: row.get(2)?,
                    sensitivity: row.get(3)?,
                    created_at: row.get(4)?,
                    archived: row.get::<_, i64>(5)? != 0,
                })
            })
            .map_err(|e| CascadeError::Other(format!("get_thread query: {e}")))?;
        Ok(rows.next().and_then(|r| r.ok()))
    }

    /// Toggle archive state. Archive moves dir to threads/archive/{id}/ (never deletes).
    pub fn set_archived(&self, id: &str, archived: bool) -> Result<()> {
        {
            let conn = self
                .conn
                .lock()
                .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
            conn.execute(
                "UPDATE threads SET archived = ?1 WHERE id = ?2",
                params![archived as i64, id],
            )
            .map_err(|e| CascadeError::Other(format!("set_archived update: {e}")))?;
        }

        // Move directory: active ↔ archive.
        let active_dir = self.threads_root.join(id);
        let archive_root = self.threads_root.join("archive");
        let archive_dir = archive_root.join(id);

        if archived {
            if active_dir.exists() {
                std::fs::create_dir_all(&archive_root)
                    .map_err(|e| CascadeError::Other(format!("archive mkdir: {e}")))?;
                std::fs::rename(&active_dir, &archive_dir)
                    .map_err(|e| CascadeError::Other(format!("archive rename: {e}")))?;
            }
        } else if archive_dir.exists() {
            std::fs::rename(&archive_dir, &active_dir)
                .map_err(|e| CascadeError::Other(format!("unarchive rename: {e}")))?;
        }

        Ok(())
    }

    // ── tasks ────────────────────────────────────────────────────────────────

    /// Add a task to a thread. Updates the stage markdown file.
    pub fn add_task(&self, params: CreateTaskParams) -> Result<ThreadTask> {
        let id = Uuid::new_v4().to_string();
        let stage = params.stage.unwrap_or_else(|| "todo".to_string());
        let now = Utc::now().to_rfc3339();

        {
            let conn = self
                .conn
                .lock()
                .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
            conn.execute(
                "INSERT INTO thread_tasks (id, thread_id, title, stage, notes, created_at, updated_at)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?6)",
                params![id, params.thread_id, params.title, stage, params.notes, now],
            )
            .map_err(|e| CascadeError::Other(format!("add_task insert: {e}")))?;
        }

        let task = ThreadTask {
            id: id.clone(),
            thread_id: params.thread_id.clone(),
            title: params.title.clone(),
            stage: stage.clone(),
            notes: params.notes.clone(),
            created_at: now.clone(),
            updated_at: now,
        };

        self.sync_stage_file(&params.thread_id, &stage)?;
        Ok(task)
    }

    /// Move a task to a new stage. Updates both old and new stage markdown files.
    pub fn move_task(&self, params: MoveTaskParams) -> Result<ThreadTask> {
        let now = Utc::now().to_rfc3339();
        let (old_stage, updated_task) = {
            let conn = self
                .conn
                .lock()
                .map_err(|_| CascadeError::Other("lock poisoned".into()))?;

            // Fetch current stage before update.
            let old_stage: String = conn
                .query_row(
                    "SELECT stage FROM thread_tasks WHERE id = ?1",
                    [&params.task_id],
                    |r| r.get(0),
                )
                .map_err(|e| CascadeError::Other(format!("move_task fetch: {e}")))?;

            conn.execute(
                "UPDATE thread_tasks SET stage = ?1, updated_at = ?2 WHERE id = ?3",
                params![params.new_stage, now, params.task_id],
            )
            .map_err(|e| CascadeError::Other(format!("move_task update: {e}")))?;

            let task: ThreadTask = conn
                .query_row(
                    "SELECT id, thread_id, title, stage, notes, created_at, updated_at
                     FROM thread_tasks WHERE id = ?1",
                    [&params.task_id],
                    |r| {
                        Ok(ThreadTask {
                            id: r.get(0)?,
                            thread_id: r.get(1)?,
                            title: r.get(2)?,
                            stage: r.get(3)?,
                            notes: r.get(4)?,
                            created_at: r.get(5)?,
                            updated_at: r.get(6)?,
                        })
                    },
                )
                .map_err(|e| CascadeError::Other(format!("move_task re-fetch: {e}")))?;

            (old_stage, task)
        };

        self.sync_stage_file(&updated_task.thread_id, &old_stage)?;
        self.sync_stage_file(&updated_task.thread_id, &params.new_stage)?;
        Ok(updated_task)
    }

    /// List tasks for a thread, optionally filtered by stage.
    pub fn list_tasks(&self, thread_id: &str, stage: Option<&str>) -> Result<Vec<ThreadTask>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
        let tasks: Vec<ThreadTask> = if let Some(s) = stage {
            let mut stmt = conn
                .prepare(
                    "SELECT id, thread_id, title, stage, notes, created_at, updated_at
                     FROM thread_tasks WHERE thread_id = ?1 AND stage = ?2 ORDER BY created_at",
                )
                .map_err(|e| CascadeError::Other(format!("list_tasks prepare: {e}")))?;
            let rows = stmt
                .query_map(params![thread_id, s], |r| {
                    Ok(ThreadTask {
                        id: r.get(0)?,
                        thread_id: r.get(1)?,
                        title: r.get(2)?,
                        stage: r.get(3)?,
                        notes: r.get(4)?,
                        created_at: r.get(5)?,
                        updated_at: r.get(6)?,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("list_tasks query: {e}")))?;
            let result: Vec<ThreadTask> = rows.filter_map(|r| r.ok()).collect();
            result
        } else {
            let mut stmt = conn
                .prepare(
                    "SELECT id, thread_id, title, stage, notes, created_at, updated_at
                     FROM thread_tasks WHERE thread_id = ?1 ORDER BY stage, created_at",
                )
                .map_err(|e| CascadeError::Other(format!("list_tasks prepare: {e}")))?;
            let rows = stmt
                .query_map([thread_id], |r| {
                    Ok(ThreadTask {
                        id: r.get(0)?,
                        thread_id: r.get(1)?,
                        title: r.get(2)?,
                        stage: r.get(3)?,
                        notes: r.get(4)?,
                        created_at: r.get(5)?,
                        updated_at: r.get(6)?,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("list_tasks query: {e}")))?;
            let result: Vec<ThreadTask> = rows.filter_map(|r| r.ok()).collect();
            result
        };
        Ok(tasks)
    }

    // ── topics ───────────────────────────────────────────────────────────────

    /// Get or create a topic by name.
    pub fn upsert_topic(&self, name: &str) -> Result<Topic> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;

        // Try to find existing.
        if let Ok(id) =
            conn.query_row::<String, _, _>("SELECT id FROM topics WHERE name = ?1", [name], |r| {
                r.get(0)
            })
        {
            return Ok(Topic {
                id,
                name: name.to_string(),
            });
        }

        // Create new.
        let id = Uuid::new_v4().to_string();
        conn.execute(
            "INSERT INTO topics (id, name) VALUES (?1, ?2)",
            params![id, name],
        )
        .map_err(|e| CascadeError::Other(format!("upsert_topic insert: {e}")))?;
        Ok(Topic {
            id,
            name: name.to_string(),
        })
    }

    /// Add a topic to a thread (idempotent).
    pub fn add_topic_to_thread(&self, thread_id: &str, topic_name: &str) -> Result<Topic> {
        let topic = self.upsert_topic(topic_name)?;
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
        conn.execute(
            "INSERT OR IGNORE INTO thread_topics (thread_id, topic_id) VALUES (?1, ?2)",
            params![thread_id, topic.id],
        )
        .map_err(|e| CascadeError::Other(format!("add_topic_to_thread: {e}")))?;
        Ok(topic)
    }

    /// List all topics for a thread.
    pub fn list_topics_for_thread(&self, thread_id: &str) -> Result<Vec<Topic>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
        let mut stmt = conn
            .prepare(
                "SELECT t.id, t.name FROM topics t
                 JOIN thread_topics tt ON tt.topic_id = t.id
                 WHERE tt.thread_id = ?1 ORDER BY t.name",
            )
            .map_err(|e| CascadeError::Other(format!("list_topics prepare: {e}")))?;
        let rows = stmt
            .query_map([thread_id], |r| {
                Ok(Topic {
                    id: r.get(0)?,
                    name: r.get(1)?,
                })
            })
            .map_err(|e| CascadeError::Other(format!("list_topics query: {e}")))?;
        let topics: Vec<Topic> = rows.filter_map(|r| r.ok()).collect();
        Ok(topics)
    }

    // ── search ───────────────────────────────────────────────────────────────

    /// Full-text keyword search across thread titles + task titles.
    /// Optionally filter by topic name. Skips locked threads.
    pub fn search(&self, params: SearchParams) -> Result<SearchResult> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
        let pattern = format!("%{}%", params.query.to_lowercase());
        let archived_filter: i64 = if params.include_archived { -1 } else { 0 };

        let threads: Vec<Thread> = if let Some(ref topic_name) = params.topic {
            let q = "SELECT DISTINCT th.id, th.parent_id, th.title, th.sensitivity, th.created_at, th.archived
                     FROM threads th
                     JOIN thread_topics tt ON tt.thread_id = th.id
                     JOIN topics tp ON tp.id = tt.topic_id
                     WHERE th.sensitivity != 'locked'
                       AND (?2 = -1 OR th.archived = ?2)
                       AND (LOWER(th.title) LIKE ?1 OR LOWER(tp.name) = ?3)
                     ORDER BY th.created_at DESC";
            let mut stmt = conn
                .prepare(q)
                .map_err(|e| CascadeError::Other(format!("search threads prepare: {e}")))?;
            let rows = stmt
                .query_map(
                    params![pattern, archived_filter, topic_name.to_lowercase()],
                    |r| {
                        Ok(Thread {
                            id: r.get(0)?,
                            parent_id: r.get(1)?,
                            title: r.get(2)?,
                            sensitivity: r.get(3)?,
                            created_at: r.get(4)?,
                            archived: r.get::<_, i64>(5)? != 0,
                        })
                    },
                )
                .map_err(|e| CascadeError::Other(format!("search threads query: {e}")))?;
            let result: Vec<Thread> = rows.filter_map(|r| r.ok()).collect();
            result
        } else {
            let mut stmt = conn
                .prepare(
                    "SELECT id, parent_id, title, sensitivity, created_at, archived
                     FROM threads
                     WHERE sensitivity != 'locked'
                       AND (?2 = -1 OR archived = ?2)
                       AND LOWER(title) LIKE ?1
                     ORDER BY created_at DESC",
                )
                .map_err(|e| CascadeError::Other(format!("search threads prepare: {e}")))?;
            let rows = stmt
                .query_map(params![pattern, archived_filter], |r| {
                    Ok(Thread {
                        id: r.get(0)?,
                        parent_id: r.get(1)?,
                        title: r.get(2)?,
                        sensitivity: r.get(3)?,
                        created_at: r.get(4)?,
                        archived: r.get::<_, i64>(5)? != 0,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("search threads query: {e}")))?;
            let result: Vec<Thread> = rows.filter_map(|r| r.ok()).collect();
            result
        };

        // Search tasks (skip tasks from locked threads).
        let tasks: Vec<ThreadTask> = {
            let mut stmt = conn
                .prepare(
                    "SELECT tt.id, tt.thread_id, tt.title, tt.stage, tt.notes, tt.created_at, tt.updated_at
                     FROM thread_tasks tt
                     JOIN threads th ON th.id = tt.thread_id
                     WHERE th.sensitivity != 'locked'
                       AND (?2 = -1 OR th.archived = ?2)
                       AND LOWER(tt.title) LIKE ?1
                     ORDER BY tt.created_at DESC",
                )
                .map_err(|e| CascadeError::Other(format!("search tasks prepare: {e}")))?;
            let rows = stmt
                .query_map(params![pattern, archived_filter], |r| {
                    Ok(ThreadTask {
                        id: r.get(0)?,
                        thread_id: r.get(1)?,
                        title: r.get(2)?,
                        stage: r.get(3)?,
                        notes: r.get(4)?,
                        created_at: r.get(5)?,
                        updated_at: r.get(6)?,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("search tasks query: {e}")))?;
            rows.filter_map(|r| r.ok()).collect()
        };

        Ok(SearchResult { threads, tasks })
    }

    // ── RAG push ─────────────────────────────────────────────────────────────

    /// Push thread content (title + README + open/review task summaries) to the
    /// personal RAG namespace via `sink`.
    ///
    /// Skips threads with sensitivity = 'locked' — those are never surfaced to RAG.
    /// One episode per non-locked thread is written; empty threads (no tasks, empty
    /// README) are still pushed so the thread title is discoverable.
    ///
    /// The caller supplies a [`RagSink`] implementation. Use [`NoopRagSink`] in
    /// tests or contexts without a memory DB; the daemon injects the real rag-08
    /// sink backed by cascade-rag's `insert_episode`.
    pub fn push_to_rag(&self, sink: &dyn RagSink) -> Result<()> {
        // Gather all threads (active + archived), skip locked.
        let threads = {
            let conn = self
                .conn
                .lock()
                .map_err(|_| CascadeError::Other("lock poisoned".into()))?;
            let mut stmt = conn
                .prepare(
                    "SELECT id, parent_id, title, sensitivity, created_at, archived
                     FROM threads
                     WHERE sensitivity != 'locked'
                     ORDER BY created_at DESC",
                )
                .map_err(|e| CascadeError::Other(format!("push_to_rag prepare: {e}")))?;
            let rows = stmt
                .query_map([], |row| {
                    Ok(Thread {
                        id: row.get(0)?,
                        parent_id: row.get(1)?,
                        title: row.get(2)?,
                        sensitivity: row.get(3)?,
                        created_at: row.get(4)?,
                        archived: row.get::<_, i64>(5)? != 0,
                    })
                })
                .map_err(|e| CascadeError::Other(format!("push_to_rag query: {e}")))?;
            rows.filter_map(|r| r.ok()).collect::<Vec<_>>()
        };

        for thread in &threads {
            // Build episode content: title + README body + open/in-review tasks.
            let readme_body = self.read_readme_content(&thread.id);
            let open_tasks = self.list_tasks(&thread.id, Some("todo"))?;
            let review_tasks = self.list_tasks(&thread.id, Some("in_progress"))?;

            let mut content = format!("Thread: {}\n", thread.title);
            if !readme_body.is_empty() {
                content.push_str(&format!("Overview: {readme_body}\n"));
            }
            if !open_tasks.is_empty() {
                content.push_str("Open tasks:");
                for t in &open_tasks {
                    content.push_str(&format!(" [{title}]", title = t.title));
                }
                content.push('\n');
            }
            if !review_tasks.is_empty() {
                content.push_str("In-progress tasks:");
                for t in &review_tasks {
                    content.push_str(&format!(" [{title}]", title = t.title));
                }
                content.push('\n');
            }

            sink.push_episode(content.trim_end())?;
            tracing::debug!("push_to_rag: pushed thread '{}'", thread.title);
        }

        tracing::debug!(
            "push_to_rag: pushed {} thread(s) to personal namespace",
            threads.len()
        );
        Ok(())
    }

    /// Read the README.md for a thread; returns empty string if absent/unreadable.
    fn read_readme_content(&self, thread_id: &str) -> String {
        let dir = self.thread_dir(thread_id);
        let path = dir.join("README.md");
        std::fs::read_to_string(&path)
            .unwrap_or_default()
            // Strip markdown heading line (e.g. "# Thread title\n") and comments.
            .lines()
            .filter(|l| !l.starts_with('#') && !l.trim_start().starts_with("<!--"))
            .collect::<Vec<_>>()
            .join(" ")
            .trim()
            .to_string()
    }

    // ── disk helpers ─────────────────────────────────────────────────────────

    /// Create the on-disk thread directory with README.md and empty stage files.
    fn init_thread_dir(&self, dir: &Path, title: &str) -> Result<()> {
        std::fs::create_dir_all(dir)
            .map_err(|e| CascadeError::Other(format!("init_thread_dir mkdir: {e}")))?;

        let readme = dir.join("README.md");
        if !readme.exists() {
            std::fs::write(&readme, format!("# {title}\n\n<!-- thread overview -->\n"))
                .map_err(|e| CascadeError::Other(format!("init_thread_dir README: {e}")))?;
        }
        for stage in &["todo", "in_progress", "done"] {
            let f = dir.join(format!("{stage}.md"));
            if !f.exists() {
                std::fs::write(&f, format!("# {stage}\n\n")).map_err(|e| {
                    CascadeError::Other(format!("init_thread_dir stage {stage}: {e}"))
                })?;
            }
        }
        Ok(())
    }

    /// Determine the on-disk directory for a thread (active or archived).
    fn thread_dir(&self, thread_id: &str) -> PathBuf {
        let active = self.threads_root.join(thread_id);
        if active.exists() {
            return active;
        }
        self.threads_root.join("archive").join(thread_id)
    }

    /// Regenerate one stage markdown file from DB for a thread.
    fn sync_stage_file(&self, thread_id: &str, stage: &str) -> Result<()> {
        let tasks = self.list_tasks(thread_id, Some(stage))?;
        let dir = self.thread_dir(thread_id);
        if !dir.exists() {
            return Ok(()); // thread dir not yet created (shouldn't happen but guard)
        }
        let file_name = format!("{stage}.md");
        let path = dir.join(&file_name);
        let mut content = format!("# {stage}\n\n");
        for task in &tasks {
            content.push_str(&format!("- [ ] {}\n", task.title));
            if let Some(notes) = &task.notes {
                if !notes.is_empty() {
                    content.push_str(&format!("  <!-- {} -->\n", notes.replace('\n', " ")));
                }
            }
        }
        std::fs::write(&path, content)
            .map_err(|e| CascadeError::Other(format!("sync_stage_file {stage}: {e}")))?;
        Ok(())
    }
}

// ── Tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tasks::open_tasks_db;
    use tempfile::tempdir;

    fn make_store() -> (ThreadStore, tempfile::TempDir) {
        let tmp = tempdir().unwrap();
        let db_path = tmp.path().join("tasks.db");
        let conn = open_tasks_db(&db_path).expect("open_tasks_db");
        let threads_root = tmp.path().join("threads");
        std::fs::create_dir_all(&threads_root).unwrap();
        (ThreadStore::new(conn, threads_root), tmp)
    }

    #[test]
    fn create_thread_creates_dirs_and_files() {
        let (store, tmp) = make_store();
        let t = store
            .create_thread(CreateThreadParams {
                title: "My Thread".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();

        let dir = tmp.path().join("threads").join(&t.id);
        assert!(dir.exists(), "thread dir must be created");
        assert!(dir.join("README.md").exists(), "README.md must exist");
        assert!(dir.join("todo.md").exists(), "todo.md must exist");
        assert!(
            dir.join("in_progress.md").exists(),
            "in_progress.md must exist"
        );
        assert!(dir.join("done.md").exists(), "done.md must exist");

        let readme = std::fs::read_to_string(dir.join("README.md")).unwrap();
        assert!(readme.contains("My Thread"), "README must contain title");
    }

    #[test]
    fn list_threads_returns_active_only() {
        let (store, _tmp) = make_store();
        store
            .create_thread(CreateThreadParams {
                title: "Active".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();
        let archived = store
            .create_thread(CreateThreadParams {
                title: "Archived".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();
        store.set_archived(&archived.id, true).unwrap();

        let active_list = store.list_threads(false).unwrap();
        assert_eq!(active_list.len(), 1);
        assert_eq!(active_list[0].title, "Active");

        let all = store.list_threads(true).unwrap();
        assert_eq!(all.len(), 2);
    }

    #[test]
    fn task_stage_transition_updates_markdown() {
        let (store, tmp) = make_store();
        let t = store
            .create_thread(CreateThreadParams {
                title: "T".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();
        let task = store
            .add_task(CreateTaskParams {
                thread_id: t.id.clone(),
                title: "do the thing".to_string(),
                stage: Some("todo".to_string()),
                notes: None,
            })
            .unwrap();

        let todo_content =
            std::fs::read_to_string(tmp.path().join("threads").join(&t.id).join("todo.md"))
                .unwrap();
        assert!(todo_content.contains("do the thing"));

        store
            .move_task(MoveTaskParams {
                task_id: task.id.clone(),
                new_stage: "done".to_string(),
            })
            .unwrap();

        let done_content =
            std::fs::read_to_string(tmp.path().join("threads").join(&t.id).join("done.md"))
                .unwrap();
        assert!(done_content.contains("do the thing"));

        let todo_after =
            std::fs::read_to_string(tmp.path().join("threads").join(&t.id).join("todo.md"))
                .unwrap();
        assert!(
            !todo_after.contains("do the thing"),
            "task must be removed from todo.md after move"
        );
    }

    #[test]
    fn archive_moves_dir_not_deletes() {
        let (store, tmp) = make_store();
        let t = store
            .create_thread(CreateThreadParams {
                title: "Will Archive".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();

        store.set_archived(&t.id, true).unwrap();

        let active_dir = tmp.path().join("threads").join(&t.id);
        let archive_dir = tmp.path().join("threads").join("archive").join(&t.id);
        assert!(
            !active_dir.exists(),
            "active dir must be gone after archive"
        );
        assert!(archive_dir.exists(), "archive dir must exist");
        assert!(
            archive_dir.join("README.md").exists(),
            "README must survive archival"
        );
    }

    #[test]
    fn topic_search_and_cross_thread() {
        let (store, _tmp) = make_store();
        let t1 = store
            .create_thread(CreateThreadParams {
                title: "Rust threads".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();
        let t2 = store
            .create_thread(CreateThreadParams {
                title: "Python ideas".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();

        store.add_topic_to_thread(&t1.id, "rust").unwrap();
        store.add_topic_to_thread(&t2.id, "python").unwrap();
        store
            .add_task(CreateTaskParams {
                thread_id: t1.id.clone(),
                title: "Write a parser".to_string(),
                stage: None,
                notes: None,
            })
            .unwrap();

        let results = store
            .search(SearchParams {
                query: "parser".to_string(),
                topic: None,
                include_archived: false,
            })
            .unwrap();
        assert_eq!(results.tasks.len(), 1);
        assert_eq!(results.tasks[0].title, "Write a parser");

        let topic_results = store
            .search(SearchParams {
                query: "".to_string(),
                topic: Some("rust".to_string()),
                include_archived: false,
            })
            .unwrap();
        assert!(topic_results.threads.iter().any(|th| th.id == t1.id));
    }

    #[test]
    fn markdown_readable_without_db() {
        let (store, tmp) = make_store();
        let t = store
            .create_thread(CreateThreadParams {
                title: "Standalone".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();
        store
            .add_task(CreateTaskParams {
                thread_id: t.id.clone(),
                title: "standalone task".to_string(),
                stage: Some("todo".to_string()),
                notes: None,
            })
            .unwrap();

        // Read the markdown file directly (no DB access) and verify it's meaningful.
        let md = std::fs::read_to_string(tmp.path().join("threads").join(&t.id).join("todo.md"))
            .unwrap();
        assert!(
            md.contains("standalone task"),
            "markdown must be readable without DB"
        );
    }

    // ── push_to_rag tests ────────────────────────────────────────────────────

    /// Collecting sink: records every episode pushed.
    struct CollectingSink(std::sync::Mutex<Vec<String>>);

    impl CollectingSink {
        fn new() -> Self {
            Self(std::sync::Mutex::new(vec![]))
        }
        fn episodes(&self) -> Vec<String> {
            self.0.lock().unwrap().clone()
        }
    }

    impl RagSink for CollectingSink {
        fn push_episode(&self, content: &str) -> Result<()> {
            self.0.lock().unwrap().push(content.to_string());
            Ok(())
        }
    }

    #[test]
    fn push_to_rag_sends_normal_thread_content() {
        let (store, _tmp) = make_store();
        store
            .create_thread(CreateThreadParams {
                title: "Normal Thread".to_string(),
                parent_id: None,
                sensitivity: Some("normal".to_string()),
            })
            .unwrap();
        store
            .create_thread(CreateThreadParams {
                title: "Default Sensitivity Thread".to_string(),
                parent_id: None,
                sensitivity: None, // defaults to "normal"
            })
            .unwrap();

        let sink = CollectingSink::new();
        store.push_to_rag(&sink).unwrap();

        let episodes = sink.episodes();
        assert_eq!(
            episodes.len(),
            2,
            "both normal-sensitivity threads must be pushed"
        );
        assert!(
            episodes.iter().any(|e| e.contains("Normal Thread")),
            "episode must include thread title"
        );
        assert!(episodes
            .iter()
            .any(|e| e.contains("Default Sensitivity Thread")));
    }

    #[test]
    fn push_to_rag_skips_locked_threads() {
        let (store, _tmp) = make_store();
        store
            .create_thread(CreateThreadParams {
                title: "Public Thread".to_string(),
                parent_id: None,
                sensitivity: Some("normal".to_string()),
            })
            .unwrap();
        store
            .create_thread(CreateThreadParams {
                title: "Secret Thread".to_string(),
                parent_id: None,
                sensitivity: Some("locked".to_string()),
            })
            .unwrap();

        let sink = CollectingSink::new();
        store.push_to_rag(&sink).unwrap();

        let episodes = sink.episodes();
        assert_eq!(episodes.len(), 1, "locked thread must be excluded");
        assert!(
            episodes[0].contains("Public Thread"),
            "only public thread must be pushed"
        );
        assert!(
            !episodes.iter().any(|e| e.contains("Secret Thread")),
            "locked thread must never appear"
        );
    }

    #[test]
    fn push_to_rag_noop_sink_returns_ok() {
        let (store, _tmp) = make_store();
        store
            .create_thread(CreateThreadParams {
                title: "Any Thread".to_string(),
                parent_id: None,
                sensitivity: None,
            })
            .unwrap();
        let sink = NoopRagSink;
        assert!(
            store.push_to_rag(&sink).is_ok(),
            "NoopRagSink must never error"
        );
    }
}
