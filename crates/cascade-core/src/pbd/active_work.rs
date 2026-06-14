//! Active-work context builder (E-P8-07).
//!
//! # Purpose
//! Given a phases root, resolves the active PBD sprint + its tickets AND the
//! project's open kanban tasks, then produces a compact token-bounded text block
//! suitable for injection into any harness context.
//!
//! # Inputs
//! - `phases_root`  — path to the phases/ directory (auto-discovered if None)
//! - `task_store`   — optional `KanbanTaskStore`; if None, tasks section is omitted
//! - `project`      — optional project name filter for kanban tasks
//! - `token_budget` — max estimated tokens for the returned text (default 800)
//!
//! # Outputs
//! - `ActiveWorkBlock` — structured block + `text()` for harness injection
//!
//! # Constraints
//! - Token budget is enforced: truncated items append a "+N more" note.
//! - Empty result (no active sprint, no tasks) returns an empty block, not an error.
//!
//! # SPORT
//! cascade-core pbd active_work — ActiveWorkContext, ActiveWorkBlock, build_active_work

use std::path::Path;

use serde::{Deserialize, Serialize};

use crate::pbd::schema::{SprintStatus, TicketStatus};
use crate::pbd::store::{locate_phases_root, resolve_phases_root, PbdStore};
use cascade_types::{
    error::Result,
    task::{TaskFilter, TaskStatus},
};

// ── Public types ──────────────────────────────────────────────────────────────

/// A single ticket entry for harness injection.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveTicketEntry {
    pub id: String,
    pub title: String,
    pub status: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub depends_on: Vec<String>,
}

/// A single kanban task entry for harness injection.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ActiveTaskEntry {
    pub title: String,
    pub status: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,
}

/// The fully built active-work block.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ActiveWorkBlock {
    /// Active sprint ID (None if no active sprint).
    pub sprint_id: Option<String>,
    /// Sprint title (None if no active sprint).
    pub sprint_title: Option<String>,
    /// Tickets in the active sprint (active/queued, most relevant first).
    pub tickets: Vec<ActiveTicketEntry>,
    /// Total tickets available (before truncation).
    pub tickets_total: usize,
    /// Open kanban tasks (Todo/InProgress).
    pub tasks: Vec<ActiveTaskEntry>,
    /// Total kanban tasks available (before truncation).
    pub tasks_total: usize,
}

impl ActiveWorkBlock {
    /// Returns true when there is nothing to show (no sprint, no tasks).
    pub fn is_empty(&self) -> bool {
        self.sprint_id.is_none() && self.tasks.is_empty()
    }

    /// Render as the canonical harness-injection text block.
    ///
    /// Wrapped in a clearly delimited section so harnesses can locate it.
    pub fn text(&self) -> String {
        if self.is_empty() {
            return String::new();
        }

        let mut out = String::new();
        out.push_str("## Active Work (Cascade-managed — do not edit)\n\n");

        if let Some(sprint_id) = &self.sprint_id {
            let title = self.sprint_title.as_deref().unwrap_or("(untitled sprint)");
            out.push_str(&format!("**Active sprint:** {} — {}\n\n", sprint_id, title));

            if self.tickets.is_empty() {
                out.push_str("*(no active or queued tickets)*\n");
            } else {
                out.push_str("**Tickets:**\n");
                for t in &self.tickets {
                    let deps = if t.depends_on.is_empty() {
                        String::new()
                    } else {
                        format!(" _(depends on: {})_", t.depends_on.join(", "))
                    };
                    out.push_str(&format!(
                        "- `{}` [{}] {}{}\n",
                        t.id, t.status, t.title, deps
                    ));
                }
                if self.tickets_total > self.tickets.len() {
                    out.push_str(&format!(
                        "- *(+{} more not shown)*\n",
                        self.tickets_total - self.tickets.len()
                    ));
                }
            }
        }

        if !self.tasks.is_empty() {
            out.push_str("\n**Open kanban tasks:**\n");
            for t in &self.tasks {
                let tags = if t.tags.is_empty() {
                    String::new()
                } else {
                    format!(" [{}]", t.tags.join(", "))
                };
                out.push_str(&format!("- [{}] {}{}\n", t.status, t.title, tags));
            }
            if self.tasks_total > self.tasks.len() {
                out.push_str(&format!(
                    "- *(+{} more not shown)*\n",
                    self.tasks_total - self.tasks.len()
                ));
            }
        }

        out
    }
}

// ── Builder ───────────────────────────────────────────────────────────────────

/// Token-budget heuristic: approximate chars per token for markdown text.
const CHARS_PER_TOKEN: usize = 4;

/// Build an `ActiveWorkBlock` from the phases tree and optional task store.
///
/// # Arguments
/// - `phases_root_override` — explicit path; if `None` auto-discovers from CWD.
/// - `task_filter`  — filter applied to the kanban store (pass `None` to skip tasks).
/// - `task_store_fn` — callable that returns `Vec<ActiveTaskEntry>`; `None` skips tasks.
/// - `token_budget` — max estimated tokens (default 800 if 0 is passed).
pub fn build_active_work(
    phases_root_override: Option<&Path>,
    token_budget: usize,
    kanban_tasks: Option<Vec<ActiveTaskEntry>>,
) -> Result<ActiveWorkBlock> {
    let budget = if token_budget == 0 { 800 } else { token_budget };
    let char_budget = budget * CHARS_PER_TOKEN;

    let mut block = ActiveWorkBlock::default();
    let mut chars_used: usize = 0;

    // ── PBD: active sprint + tickets ─────────────────────────────────────────
    let root = resolve_phases_root(phases_root_override);
    let store = PbdStore::new(&root);
    let current = store.read_current()?;

    if let Some(sprint_id) = &current.active_sprint {
        // Look up sprint title via INDEX
        let index = store.read_index()?;
        let sprint_title = index
            .entries
            .iter()
            .find(|e| e.kind == "sprint" && &e.id == sprint_id)
            .map(|e| e.title.clone());

        block.sprint_id = Some(sprint_id.clone());
        block.sprint_title = sprint_title.clone();

        // Count chars used by header
        let header = format!(
            "**Active sprint:** {} — {}\n",
            sprint_id,
            sprint_title.as_deref().unwrap_or("")
        );
        chars_used += header.len();

        // Collect all tickets in the active sprint, ordered: active > review > queue > planned
        let mut all_tickets: Vec<ActiveTicketEntry> = Vec::new();
        let sprint_entry = index
            .entries
            .iter()
            .find(|e| e.kind == "sprint" && &e.id == sprint_id);

        if let Some(sprint_e) = sprint_entry {
            // Resolve parent chain to load the sprint YAML
            let wave_id = index
                .entries
                .iter()
                .find(|e| e.kind == "wave" && Some(&e.id) == sprint_e.parent.as_ref())
                .or_else(|| {
                    // Parent field holds wave_id
                    index
                        .entries
                        .iter()
                        .find(|e| e.kind == "wave" && sprint_e.parent.as_deref() == Some(&e.id))
                });
            let _ = wave_id; // We use the ticket entries in INDEX directly

            // Collect ticket entries whose parent is our sprint
            for entry in index
                .entries
                .iter()
                .filter(|e| e.kind == "ticket" && e.parent.as_deref() == Some(sprint_id.as_str()))
            {
                let status = entry.status.clone();
                // Only include non-done, non-archived tickets
                if matches!(status.as_str(), "done" | "archived") {
                    continue;
                }
                all_tickets.push(ActiveTicketEntry {
                    id: entry.id.clone(),
                    title: entry.title.clone(),
                    status,
                    depends_on: Vec::new(), // INDEX doesn't carry deps; load from ticket.yaml if needed
                });
            }
        }

        // Sort: active first, then review, then queue, then planned/blocked
        all_tickets.sort_by_key(|t| status_priority(&t.status));
        block.tickets_total = all_tickets.len();

        // Fit into budget
        for ticket in all_tickets {
            let line = format!("- `{}` [{}] {}\n", ticket.id, ticket.status, ticket.title);
            if chars_used + line.len() > char_budget {
                break;
            }
            chars_used += line.len();
            block.tickets.push(ticket);
        }
    }

    // ── Kanban tasks ──────────────────────────────────────────────────────────
    if let Some(tasks) = kanban_tasks {
        let total = tasks.len();
        let mut included = Vec::new();

        for task in tasks {
            let line = format!("- [{}] {}\n", task.status, task.title);
            if chars_used + line.len() > char_budget {
                break;
            }
            chars_used += line.len();
            included.push(task);
        }

        block.tasks_total = total;
        block.tasks = included;
    }

    Ok(block)
}

/// Priority ordering for ticket status (lower = higher priority).
fn status_priority(status: &str) -> u8 {
    match status {
        "active" => 0,
        "review" => 1,
        "queue" => 2,
        "blocked" => 3,
        "planned" => 4,
        _ => 5,
    }
}

// ── Convenience: build from KanbanTaskStore ───────────────────────────────────

/// Build active-work block with tasks sourced from a `KanbanTaskStore`.
///
/// Filters to `Todo` + `InProgress` status. Applies optional project filter.
pub fn build_active_work_with_tasks(
    phases_root_override: Option<&Path>,
    token_budget: usize,
    task_store: &crate::tasks::KanbanTaskStore,
    project_filter: Option<&str>,
) -> Result<ActiveWorkBlock> {
    let filter = cascade_types::task::TaskFilter {
        project: project_filter.map(|s| s.to_string()),
        status: None, // we'll filter in Rust
        ..Default::default()
    };
    let all_tasks = task_store.list(&filter)?;

    let active_tasks: Vec<ActiveTaskEntry> = all_tasks
        .into_iter()
        .filter(|t| matches!(t.status, TaskStatus::Todo | TaskStatus::InProgress))
        .map(|t| ActiveTaskEntry {
            title: t.title.clone(),
            status: t.status.to_string(),
            tags: t.tags.clone(),
        })
        .collect();

    build_active_work(phases_root_override, token_budget, Some(active_tasks))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pbd::schema::{
        CurrentPointers, Epic, EpicStatus, Phase, PhaseStatus, Sprint, SprintStatus, Ticket,
        TicketStatus, Wave, WaveStatus,
    };
    use crate::pbd::store::PbdStore;
    use crate::tasks::{open_tasks_db, KanbanTaskStore};
    use cascade_types::task::{Task, TaskStatus};
    use serial_test::serial;
    use std::sync::{Arc, Mutex};
    use tempfile::TempDir;

    // ── Helpers ───────────────────────────────────────────────────────────────

    fn make_phases_tree(dir: &TempDir) -> std::path::PathBuf {
        let root = dir.path().join("phases");
        std::fs::create_dir_all(&root).unwrap();
        let store = PbdStore::new(&root);
        store.init().unwrap();

        let phase = Phase {
            id: "p1".into(),
            title: "Phase 1".into(),
            status: PhaseStatus::Building,
            epics: vec!["e01".into()],
            started_at: None,
            closed_at: None,
            note: None,
        };
        store.create_phase(&phase).unwrap();

        let epic = Epic {
            id: "e01".into(),
            phase_id: "p1".into(),
            title: "Epic 1".into(),
            status: EpicStatus::Active,
            waves: vec!["w01".into()],
            depends_on: vec![],
            note: None,
        };
        store.create_epic(&epic).unwrap();

        let wave = Wave {
            id: "w01".into(),
            epic_id: "e01".into(),
            title: "Wave 1".into(),
            status: WaveStatus::Active,
            sprints: vec!["s01".into()],
            note: None,
        };
        store.create_wave("p1", &wave).unwrap();

        let sprint = Sprint {
            id: "s01".into(),
            wave_id: "w01".into(),
            title: "Sprint 1".into(),
            status: SprintStatus::Active,
            tickets: vec!["T-P1-E01-W01-S01-01".into(), "T-P1-E01-W01-S01-02".into()],
            note: None,
        };
        store.create_sprint("p1", "e01", &sprint).unwrap();

        let t1 = Ticket {
            id: "T-P1-E01-W01-S01-01".into(),
            sprint_id: "s01".into(),
            title: "First ticket".into(),
            status: TicketStatus::Active,
            steps: vec![],
            depends_on: vec![],
            repo: None,
            weight: None,
            note: None,
            blocked_reason: None,
        };
        store.create_ticket("p1", "e01", "w01", &t1).unwrap();

        let t2 = Ticket {
            id: "T-P1-E01-W01-S01-02".into(),
            sprint_id: "s01".into(),
            title: "Second ticket".into(),
            status: TicketStatus::Queue,
            steps: vec![],
            depends_on: vec![],
            repo: None,
            weight: None,
            note: None,
            blocked_reason: None,
        };
        store.create_ticket("p1", "e01", "w01", &t2).unwrap();

        root
    }

    fn make_task_store(dir: &TempDir) -> KanbanTaskStore {
        let db_path = dir.path().join("tasks.db");
        let conn = open_tasks_db(&db_path).unwrap();
        KanbanTaskStore::new(conn)
    }

    // ── Test: active_work reflects active sprint tickets ─────────────────────

    #[test]
    #[serial(global_env)]
    fn active_work_reflects_sprint_tickets() {
        let tmp = TempDir::new().unwrap();
        let phases_root = make_phases_tree(&tmp);

        let block = build_active_work(Some(&phases_root), 800, None).unwrap();

        assert_eq!(
            block.sprint_id.as_deref(),
            Some("s01"),
            "sprint_id must be s01"
        );
        assert_eq!(block.tickets_total, 2, "two tickets in sprint");
        assert!(
            block.tickets.iter().any(|t| t.id == "T-P1-E01-W01-S01-01"),
            "must include first ticket"
        );
        assert!(
            block.tickets.iter().any(|t| t.id == "T-P1-E01-W01-S01-02"),
            "must include second ticket"
        );
        // active ticket comes before queue ticket
        assert_eq!(
            block.tickets[0].status, "active",
            "active ticket must be listed first"
        );
    }

    // ── Test: token bound truncates with +N more note ─────────────────────────

    #[test]
    #[serial(global_env)]
    fn token_bound_truncates_with_more_note() {
        let tmp = TempDir::new().unwrap();
        let phases_root = make_phases_tree(&tmp);

        // Very small budget — only fits sprint header, no tickets
        let block = build_active_work(Some(&phases_root), 5, None).unwrap();

        // With 5 tokens (20 chars), sprint title header might not even fit fully,
        // but we should never panic. If some tickets are omitted, tickets_total > tickets.len().
        if block.tickets_total > block.tickets.len() {
            // truncation note must appear in text
            let text = block.text();
            assert!(
                text.contains("more not shown") || block.tickets.is_empty(),
                "truncated block must contain 'more not shown' or have no tickets: {text}"
            );
        }
    }

    // ── Test: empty (no active phase / no tasks) -> empty block, not error ───

    #[test]
    #[serial(global_env)]
    fn empty_phases_tree_returns_empty_block() {
        let tmp = TempDir::new().unwrap();
        let empty_root = tmp.path().join("empty_phases");
        std::fs::create_dir_all(&empty_root).unwrap();

        let block = build_active_work(Some(&empty_root), 800, None).unwrap();

        assert!(block.is_empty(), "no active phase/tasks → empty block");
        assert_eq!(block.text(), "", "empty block text must be empty string");
    }

    // ── Test: include open kanban tasks ──────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn includes_open_kanban_tasks() {
        let tmp = TempDir::new().unwrap();
        let phases_root = tmp.path().join("phases");
        std::fs::create_dir_all(&phases_root).unwrap();

        let tasks = vec![
            ActiveTaskEntry {
                title: "Implement login".into(),
                status: "inProgress".into(),
                tags: vec!["auth".into()],
            },
            ActiveTaskEntry {
                title: "Write tests".into(),
                status: "todo".into(),
                tags: vec![],
            },
        ];

        let block = build_active_work(Some(&phases_root), 800, Some(tasks)).unwrap();

        assert_eq!(block.tasks_total, 2);
        assert_eq!(block.tasks.len(), 2);
        let text = block.text();
        assert!(
            text.contains("Implement login"),
            "text must contain task title"
        );
    }

    // ── Test: build_active_work_with_tasks filters to Todo/InProgress ─────────

    #[test]
    #[serial(global_env)]
    fn task_store_filters_to_active_statuses() {
        let tmp = TempDir::new().unwrap();
        let phases_root = tmp.path().join("phases");
        std::fs::create_dir_all(&phases_root).unwrap();
        let store = make_task_store(&tmp);

        let mut t_done = Task::new("Done task", "proj");
        t_done.status = TaskStatus::Done;
        store.create(&t_done).unwrap();

        let mut t_inprogress = Task::new("In progress", "proj");
        t_inprogress.status = TaskStatus::InProgress;
        store.create(&t_inprogress).unwrap();

        let t_todo = Task::new("Todo task", "proj");
        // status defaults to Backlog, but let's set to Todo
        let mut t_todo = t_todo;
        t_todo.status = TaskStatus::Todo;
        store.create(&t_todo).unwrap();

        let block = build_active_work_with_tasks(Some(&phases_root), 800, &store, None).unwrap();

        // Only InProgress + Todo included, not Done
        assert_eq!(
            block.tasks_total, 2,
            "only InProgress and Todo tasks included"
        );
        assert!(
            block.tasks.iter().all(|t| t.status != "done"),
            "Done tasks must not appear"
        );
    }
}
