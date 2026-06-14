// commands/pbd.rs — Tauri IPC commands for the PBD (Phase-Based Development) tree.
//
// Purpose: Expose the full PEWS hierarchy (Phase→Epic→Wave→Sprint→Ticket) to the
//   React frontend for visualization and mutation. All logic delegates to
//   cascade_core::pbd::store::PbdStore — no daemon IPC required (same pattern as
//   commands/maps.rs for the project-graph/DAG views).
//
// IPC contract: camelCase JSON payloads via Tauri serde rename. Snake_case param
//   names here map to camelCase on the JS side (Tauri auto-transform disabled for
//   explicit Value params; applied for typed params).
//
// File-watch: pbd_watch installs a notify watcher on the phases/ directory, emitting
//   "pbd://changed" events whenever YAML or JSONL files are written. Watcher is
//   Box-leaked (same pattern as vault_watch) for process lifetime.
//
// SPORT: MASTER-COMMANDS.md — pbd_get_tree, pbd_get_current, pbd_update_ticket_status,
//        pbd_list_tickets, pbd_watch (E-P8-05)

use std::path::PathBuf;

use notify::{Config, EventKind, RecommendedWatcher, RecursiveMode, Watcher};
use serde::{Deserialize, Serialize};
use serde_json::Value;

use cascade_core::pbd::{
    schema::{Ticket, TicketStatus},
    store::{PbdStore, resolve_phases_root},
};
use tauri::Emitter;
use cascade_types::error::CascadeError;

use crate::error::CascadeError as AppError;

// ── Wire types ────────────────────────────────────────────────────────────────

/// Serialized ticket for the frontend tree view.
///
/// Mirrors cascade_core::pbd::schema::Ticket with camelCase keys.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TicketView {
    pub id: String,
    pub sprint_id: String,
    pub title: String,
    pub status: String,
    pub depends_on: Vec<String>,
    pub repo: Option<String>,
    pub weight: Option<String>,
    pub note: Option<String>,
    pub blocked_reason: Option<String>,
}

impl From<Ticket> for TicketView {
    fn from(t: Ticket) -> Self {
        Self {
            id: t.id,
            sprint_id: t.sprint_id,
            title: t.title,
            status: t.status.as_str().to_string(),
            depends_on: t.depends_on,
            repo: t.repo,
            weight: t.weight,
            note: t.note,
            blocked_reason: t.blocked_reason,
        }
    }
}

/// Serialized sprint with its ticket list.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SprintView {
    pub id: String,
    pub wave_id: String,
    pub title: String,
    pub status: String,
    pub tickets: Vec<TicketView>,
    pub note: Option<String>,
}

/// Serialized wave with its sprint list.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WaveView {
    pub id: String,
    pub epic_id: String,
    pub title: String,
    pub status: String,
    pub sprints: Vec<SprintView>,
    pub note: Option<String>,
}

/// Serialized epic with its wave list.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EpicView {
    pub id: String,
    pub phase_id: String,
    pub title: String,
    pub status: String,
    pub depends_on: Vec<String>,
    pub waves: Vec<WaveView>,
    pub note: Option<String>,
}

/// Serialized phase with its epic list.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PhaseView {
    pub id: String,
    pub title: String,
    pub status: String,
    pub epics: Vec<EpicView>,
    pub note: Option<String>,
}

/// Full PEWS tree returned by pbd_get_tree.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PewsTree {
    /// All phases in the store, past/current/future, sorted by id.
    pub phases: Vec<PhaseView>,
    /// Current active pointers (from current.yaml).
    pub current_phase: Option<String>,
    pub current_epic: Option<String>,
    pub current_wave: Option<String>,
    pub current_sprint: Option<String>,
    pub active_tickets: Vec<String>,
}

/// Result returned by pbd_update_ticket_status.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TicketStatusUpdateResult {
    pub ticket_id: String,
    pub old_status: String,
    pub new_status: String,
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn app_err(e: CascadeError) -> AppError {
    AppError::Custom(e.to_string())
}

fn build_pews_tree(root: &std::path::Path) -> Result<PewsTree, AppError> {
    let store = PbdStore::new(root);

    // Read current pointers (may not exist yet — return empty pointers)
    let current = store.read_current().unwrap_or_default();

    let phases = store.list_phases().map_err(app_err)?;
    let mut phase_views = Vec::new();

    for phase in phases {
        let epics = store.list_epics(&phase.id).map_err(app_err)?;
        let mut epic_views = Vec::new();

        for epic in epics {
            let waves = store.list_waves(&phase.id, &epic.id).map_err(app_err)?;
            let mut wave_views = Vec::new();

            for wave in waves {
                let sprints = store
                    .list_sprints(&phase.id, &epic.id, &wave.id)
                    .map_err(app_err)?;
                let mut sprint_views = Vec::new();

                for sprint in sprints {
                    let tickets = store
                        .list_tickets(&phase.id, &epic.id, &wave.id, &sprint.id)
                        .map_err(app_err)?;
                    sprint_views.push(SprintView {
                        id: sprint.id.clone(),
                        wave_id: sprint.wave_id.clone(),
                        title: sprint.title.clone(),
                        status: sprint.status.as_str().to_string(),
                        tickets: tickets.into_iter().map(TicketView::from).collect(),
                        note: sprint.note.clone(),
                    });
                }

                wave_views.push(WaveView {
                    id: wave.id.clone(),
                    epic_id: wave.epic_id.clone(),
                    title: wave.title.clone(),
                    status: wave.status.as_str().to_string(),
                    sprints: sprint_views,
                    note: wave.note.clone(),
                });
            }

            epic_views.push(EpicView {
                id: epic.id.clone(),
                phase_id: epic.phase_id.clone(),
                title: epic.title.clone(),
                status: epic.status.as_str().to_string(),
                depends_on: epic.depends_on.clone(),
                waves: wave_views,
                note: epic.note.clone(),
            });
        }

        phase_views.push(PhaseView {
            id: phase.id.clone(),
            title: phase.title.clone(),
            status: phase.status.as_str().to_string(),
            epics: epic_views,
            note: phase.note.clone(),
        });
    }

    Ok(PewsTree {
        phases: phase_views,
        current_phase: current.active_phase,
        current_epic: current.active_epic,
        current_wave: current.active_wave,
        current_sprint: current.active_sprint,
        active_tickets: current.active_tickets,
    })
}

/// Parse a string into the TicketStatus enum.
fn parse_ticket_status(s: &str) -> Option<TicketStatus> {
    match s {
        "planned" => Some(TicketStatus::Planned),
        "queue" => Some(TicketStatus::Queue),
        "active" => Some(TicketStatus::Active),
        "review" => Some(TicketStatus::Review),
        "blocked" => Some(TicketStatus::Blocked),
        "done" => Some(TicketStatus::Done),
        "archived" => Some(TicketStatus::Archived),
        _ => None,
    }
}

// ── Tauri commands ────────────────────────────────────────────────────────────

/// Return the full PEWS tree for the given phase root.
///
/// # Purpose
/// Powers the PEWS mutation UI — returns all phases (past/current/future) with
/// their full Epic→Wave→Sprint→Ticket hierarchy and status coloring metadata.
///
/// # JS
/// `invoke("pbd_get_tree", { phaseRoot })`
///
/// # Inputs
/// - `phase_root`: Absolute path to the project root whose `.claude/phases/` to read.
///
/// # Outputs
/// `PewsTree` JSON: `{ phases, currentPhase, currentEpic, currentWave, currentSprint, activeTickets }`
/// # SPORT
/// MASTER-COMMANDS.md — pbd_get_tree (E-P8-05)
#[tauri::command]
pub fn pbd_get_tree(phase_root: String) -> Result<PewsTree, AppError> {
    let root = resolve_phases_root(Some(std::path::Path::new(&phase_root)));
    build_pews_tree(&root)
}

/// Return the active pointers from current.yaml.
///
/// # Purpose
/// Powers the sidebar active-pointers display. Called on mount + on pbd://changed events.
///
/// # JS
/// `invoke("pbd_get_current", { phaseRoot? })`
/// # SPORT
/// MASTER-COMMANDS.md — pbd_get_current (E-P8-05)
#[tauri::command]
pub fn pbd_get_current(phase_root: Option<String>) -> Result<Value, AppError> {
    let root = resolve_phases_root(phase_root.as_deref().map(std::path::Path::new));
    let store = PbdStore::new(&root);
    let current = store.read_current().unwrap_or_default();
    serde_json::to_value(&current).map_err(|e| AppError::Custom(e.to_string()))
}

/// Transition a ticket status, writing the YAML and emitting an event.
///
/// # Purpose
/// Handles the ticket-status dropdown in the PEWS mutation UI. Optimistic UI
/// pattern: the React layer applies the change immediately, then calls this to
/// persist it; on error, rolls back to the previous status.
///
/// # JS
/// `invoke("pbd_update_ticket_status", { phaseRoot, ticketId, status, note? })`
/// # SPORT
/// MASTER-COMMANDS.md — pbd_update_ticket_status (E-P8-05)
#[tauri::command]
pub fn pbd_update_ticket_status(
    phase_root: String,
    ticket_id: String,
    status: String,
    note: Option<String>,
) -> Result<TicketStatusUpdateResult, AppError> {
    let new_status = parse_ticket_status(&status)
        .ok_or_else(|| AppError::Custom(format!("invalid ticket status: '{status}'")))?;

    let root = resolve_phases_root(Some(std::path::Path::new(&phase_root)));
    let store = PbdStore::new(&root);

    // Locate ticket coords via INDEX
    let index = store.read_index().map_err(app_err)?;
    let entry = index
        .entries
        .iter()
        .find(|e| e.kind == "ticket" && e.id == ticket_id)
        .ok_or_else(|| AppError::Custom(format!("ticket '{ticket_id}' not found in INDEX")))?;

    // Resolve parent chain: ticket → sprint → wave → epic → phase
    let coords = resolve_ticket_coords(&store, &entry.id, entry.parent.as_deref())
        .map_err(app_err)?;

    // Read old status for the response
    let ticket = store
        .load_ticket(&coords.phase_id, &coords.epic_id, &coords.wave_id, &coords.sprint_id, &ticket_id)
        .map_err(app_err)?;
    let old_status = ticket.status.as_str().to_string();

    store
        .transition_ticket(
            &coords.phase_id,
            &coords.epic_id,
            &coords.wave_id,
            &coords.sprint_id,
            &ticket_id,
            new_status.clone(),
            note.as_deref(),
        )
        .map_err(app_err)?;

    Ok(TicketStatusUpdateResult {
        ticket_id,
        old_status,
        new_status: new_status.as_str().to_string(),
    })
}

/// List tickets with optional status filter.
///
/// # JS
/// `invoke("pbd_list_tickets", { phaseRoot, status? })`
/// # SPORT
/// MASTER-COMMANDS.md — pbd_list_tickets (E-P8-05)
#[tauri::command]
pub fn pbd_list_tickets(
    phase_root: String,
    status: Option<String>,
) -> Result<Vec<TicketView>, AppError> {
    let root = resolve_phases_root(Some(std::path::Path::new(&phase_root)));
    let store = PbdStore::new(&root);

    let filter_status: Option<TicketStatus> = status.as_deref().and_then(parse_ticket_status);

    let phases = store.list_phases().map_err(app_err)?;
    let mut result = Vec::new();

    for phase in phases {
        let epics = store.list_epics(&phase.id).map_err(app_err)?;
        for epic in epics {
            let waves = store.list_waves(&phase.id, &epic.id).map_err(app_err)?;
            for wave in waves {
                let sprints = store.list_sprints(&phase.id, &epic.id, &wave.id).map_err(app_err)?;
                for sprint in sprints {
                    let tickets = store
                        .list_tickets(&phase.id, &epic.id, &wave.id, &sprint.id)
                        .map_err(app_err)?;
                    for ticket in tickets {
                        if let Some(ref fs) = filter_status {
                            if &ticket.status != fs {
                                continue;
                            }
                        }
                        result.push(TicketView::from(ticket));
                    }
                }
            }
        }
    }

    Ok(result)
}

/// Install a file-system watcher on the phases/ directory.
/// Emits `"pbd://changed"` Tauri events on any YAML or JSONL write.
///
/// # Purpose
/// Enables live sync: when the CLI or an agent edits status.yaml / ticket files,
/// the React layer receives the event, re-fetches pbd_get_tree, and reconciles.
///
/// # JS
/// `invoke("pbd_watch", { phaseRoot })`
/// # SPORT
/// MASTER-COMMANDS.md — pbd_watch (E-P8-05)
#[tauri::command]
pub fn pbd_watch(phase_root: String, window: tauri::Window) -> Result<(), AppError> {
    let watch_root: PathBuf = resolve_phases_root(Some(std::path::Path::new(&phase_root)));

    if !watch_root.exists() {
        // Phases dir may not exist yet — install watcher on the project root .claude/ dir
        // and let notify handle the case where the path is created later.
        // For now, silently succeed (no data = no events needed yet).
        return Ok(());
    }

    let watch_root_clone = watch_root.clone();

    let mut watcher = RecommendedWatcher::new(
        move |res: notify::Result<notify::Event>| {
            if let Ok(event) = res {
                let is_write = matches!(
                    event.kind,
                    EventKind::Create(_) | EventKind::Modify(_) | EventKind::Remove(_)
                );
                if !is_write {
                    return;
                }
                for path in &event.paths {
                    let ext = path.extension().and_then(|e| e.to_str());
                    let is_pbd_file = matches!(ext, Some("yaml") | Some("jsonl"));
                    if is_pbd_file && path.starts_with(&watch_root_clone) {
                        let _ = window.emit(
                            "pbd://changed",
                            serde_json::json!({ "path": path.to_string_lossy() }),
                        );
                        return; // one event per OS batch is enough
                    }
                }
            }
        },
        Config::default(),
    )
    .map_err(|e| AppError::Custom(format!("pbd_watch: failed to create watcher: {e}")))?;

    watcher
        .watch(&watch_root, RecursiveMode::Recursive)
        .map_err(|e| AppError::Custom(format!("pbd_watch: failed to watch path: {e}")))?;

    // Leak the watcher so it lives for the window's lifetime (same as vault_watch).
    Box::leak(Box::new(watcher));
    Ok(())
}

// ── Coord resolution (mirrors cascade-mcp tool.rs) ───────────────────────────

/// Resolved parent-chain coordinates for a ticket.
struct TicketCoords {
    phase_id: String,
    epic_id: String,
    wave_id: String,
    sprint_id: String,
}

fn resolve_ticket_coords(
    store: &PbdStore,
    ticket_id: &str,
    sprint_id: Option<&str>,
) -> Result<TicketCoords, CascadeError> {
    let sprint_id = sprint_id.ok_or_else(|| {
        CascadeError::Other(format!("ticket '{ticket_id}' has no parent sprint in INDEX"))
    })?;

    let index = store.read_index()?;

    let sprint_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "sprint" && e.id == sprint_id)
        .ok_or_else(|| CascadeError::Other(format!("sprint '{sprint_id}' not found in INDEX")))?;

    let wave_id = sprint_entry
        .parent
        .as_deref()
        .ok_or_else(|| CascadeError::Other(format!("sprint '{sprint_id}' has no parent wave")))?;

    let wave_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "wave" && e.id == wave_id)
        .ok_or_else(|| CascadeError::Other(format!("wave '{wave_id}' not found in INDEX")))?;

    let epic_id = wave_entry
        .parent
        .as_deref()
        .ok_or_else(|| CascadeError::Other(format!("wave '{wave_id}' has no parent epic")))?;

    let epic_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "epic" && e.id == epic_id)
        .ok_or_else(|| CascadeError::Other(format!("epic '{epic_id}' not found in INDEX")))?;

    let phase_id = epic_entry
        .parent
        .as_deref()
        .ok_or_else(|| CascadeError::Other(format!("epic '{epic_id}' has no parent phase")))?;

    Ok(TicketCoords {
        phase_id: phase_id.to_string(),
        epic_id: epic_id.to_string(),
        wave_id: wave_id.to_string(),
        sprint_id: sprint_id.to_string(),
    })
}
