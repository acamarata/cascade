//! PBD schema types — Phase-Based Development hierarchy.
//!
//! # Purpose
//! Defines the complete type hierarchy for the on-disk PBD tracking system:
//! Phase → Epic → Wave → Sprint → Ticket → Step.
//!
//! # Design
//! - All types are serde-serializable to YAML (git-diffable, human-readable).
//! - Status transitions are validated by this module before writes.
//! - Event log entries are append-only JSONL records emitted on every transition.
//!
//! # Constraints
//! - Status enums are non-exhaustive to allow future additions without breaking semver.
//! - `depends_on` is a flat list of IDs — callers check dependency graph.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

// ── Status enums ─────────────────────────────────────────────────────────────

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum PhaseStatus {
    /// Phase is being set up; prerequisites not yet confirmed.
    Opening,
    /// Active planning; epics/tickets being defined.
    Planning,
    /// Fully planned and gated — all epics+tickets populated.
    #[serde(alias = "ready")]
    ReadyToBuild,
    /// Active execution (Build phase).
    #[serde(alias = "build")]
    Building,
    /// QA / wrap-up after execution (folds legacy Qa + Shipped).
    #[serde(alias = "qa", alias = "shipped")]
    Wrapup,
    /// Terminal: phase archived.
    Archived,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EpicStatus {
    Planned,
    Active,
    Done,
    Archived,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum WaveStatus {
    Queued,
    Active,
    Qa,
    Done,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SprintStatus {
    Queued,
    Active,
    Qa,
    Done,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TicketStatus {
    Planned,
    Queue,
    Active,
    Review,
    Blocked,
    Done,
    Archived,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StepStatus {
    Pending,
    Running,
    Passed,
    Failed,
    Skipped,
}

// ── Hierarchy types ──────────────────────────────────────────────────────────

/// Top-level phase (e.g. P1, P2, P3).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Phase {
    /// Unique identifier, e.g. `p1`.
    pub id: String,
    pub title: String,
    pub status: PhaseStatus,
    #[serde(default)]
    pub epics: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub closed_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

/// Epic — groups waves of parallel sprints.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Epic {
    pub id: String,
    pub phase_id: String,
    pub title: String,
    pub status: EpicStatus,
    #[serde(default)]
    pub waves: Vec<String>,
    #[serde(default)]
    pub depends_on: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

/// Wave — a parallel batch of sprints.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Wave {
    pub id: String,
    pub epic_id: String,
    pub title: String,
    pub status: WaveStatus,
    #[serde(default)]
    pub sprints: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

/// Sprint — a unit of work containing tickets.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Sprint {
    pub id: String,
    pub wave_id: String,
    pub title: String,
    pub status: SprintStatus,
    #[serde(default)]
    pub tickets: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

/// Ticket — a single deliverable work item.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Ticket {
    pub id: String,
    pub sprint_id: String,
    pub title: String,
    pub status: TicketStatus,
    #[serde(default)]
    pub steps: Vec<Step>,
    #[serde(default)]
    pub depends_on: Vec<String>,
    /// Optional repository the ticket belongs to.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub repo: Option<String>,
    /// Optional weight (S/M/L/XL or numeric).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub weight: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub blocked_reason: Option<String>,
}

/// Step — an atomic check or action inside a ticket.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Step {
    pub id: String,
    pub title: String,
    pub status: StepStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

// ── Event log ────────────────────────────────────────────────────────────────

/// Level of the entity that changed.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum EventLevel {
    Phase,
    Epic,
    Wave,
    Sprint,
    Ticket,
    Step,
}

/// A single append-only event record (one line in events.jsonl).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PbdEvent {
    pub ts: DateTime<Utc>,
    pub actor: String,
    pub level: EventLevel,
    pub id: String,
    pub from: String,
    pub to: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

// ── Active-context pointer ────────────────────────────────────────────────────

/// current.yaml — active pointers for session boot (<=200 tok).
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct CurrentPointers {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub active_phase: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub active_epic: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub active_wave: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub active_sprint: Option<String>,
    #[serde(default)]
    pub active_tickets: Vec<String>,
}

// ── INDEX ─────────────────────────────────────────────────────────────────────

/// INDEX.yaml entry — flat view of the entire hierarchy for quick lookup.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IndexEntry {
    pub kind: String,
    pub id: String,
    pub parent: Option<String>,
    pub title: String,
    pub status: String,
}

/// INDEX.yaml root.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct PbdIndex {
    pub entries: Vec<IndexEntry>,
}

// ── Status transition validation ──────────────────────────────────────────────

impl PhaseStatus {
    /// Returns allowed next statuses from the current state.
    ///
    /// Canonical lifecycle: Opening → Planning → ReadyToBuild → Building → Wrapup → Archived.
    /// Back-steps allowed where they make sense (e.g. ReadyToBuild → Planning for re-planning).
    pub fn allowed_transitions(&self) -> &'static [PhaseStatus] {
        use PhaseStatus::*;
        match self {
            Opening => &[Planning, Archived],
            Planning => &[ReadyToBuild, Opening, Archived],
            ReadyToBuild => &[Building, Planning, Archived],
            Building => &[Wrapup, ReadyToBuild, Archived],
            Wrapup => &[Archived],
            Archived => &[],
        }
    }

    pub fn can_transition_to(&self, next: &PhaseStatus) -> bool {
        self.allowed_transitions().contains(next)
    }

    /// Canonical string representation serialized to YAML/JSON.
    pub fn as_str(&self) -> &'static str {
        match self {
            PhaseStatus::Opening => "opening",
            PhaseStatus::Planning => "planning",
            PhaseStatus::ReadyToBuild => "ready_to_build",
            PhaseStatus::Building => "building",
            PhaseStatus::Wrapup => "wrapup",
            PhaseStatus::Archived => "archived",
        }
    }

    /// Returns true when the phase is actively being executed (counts toward active_phase in current.yaml).
    pub fn is_active(&self) -> bool {
        matches!(self, PhaseStatus::Building | PhaseStatus::Wrapup)
    }
}

impl EpicStatus {
    pub fn allowed_transitions(&self) -> &'static [EpicStatus] {
        use EpicStatus::*;
        match self {
            Planned => &[Active, Archived],
            Active => &[Done, Planned, Archived],
            Done => &[Archived],
            Archived => &[],
        }
    }

    pub fn can_transition_to(&self, next: &EpicStatus) -> bool {
        self.allowed_transitions().contains(next)
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            EpicStatus::Planned => "planned",
            EpicStatus::Active => "active",
            EpicStatus::Done => "done",
            EpicStatus::Archived => "archived",
        }
    }
}

impl WaveStatus {
    pub fn allowed_transitions(&self) -> &'static [WaveStatus] {
        use WaveStatus::*;
        match self {
            Queued => &[Active, Done],
            Active => &[Qa, Queued, Done],
            Qa => &[Done, Active],
            Done => &[],
        }
    }

    pub fn can_transition_to(&self, next: &WaveStatus) -> bool {
        self.allowed_transitions().contains(next)
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            WaveStatus::Queued => "queued",
            WaveStatus::Active => "active",
            WaveStatus::Qa => "qa",
            WaveStatus::Done => "done",
        }
    }
}

impl SprintStatus {
    pub fn allowed_transitions(&self) -> &'static [SprintStatus] {
        use SprintStatus::*;
        match self {
            Queued => &[Active, Done],
            Active => &[Qa, Queued, Done],
            Qa => &[Done, Active],
            Done => &[],
        }
    }

    pub fn can_transition_to(&self, next: &SprintStatus) -> bool {
        self.allowed_transitions().contains(next)
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            SprintStatus::Queued => "queued",
            SprintStatus::Active => "active",
            SprintStatus::Qa => "qa",
            SprintStatus::Done => "done",
        }
    }
}

impl TicketStatus {
    pub fn allowed_transitions(&self) -> &'static [TicketStatus] {
        use TicketStatus::*;
        match self {
            Planned => &[Queue, Archived],
            Queue => &[Active, Planned, Archived],
            Active => &[Review, Blocked, Done, Queue, Archived],
            Review => &[Done, Active, Blocked, Archived],
            Blocked => &[Active, Queue, Archived],
            Done => &[Archived],
            Archived => &[],
        }
    }

    pub fn can_transition_to(&self, next: &TicketStatus) -> bool {
        self.allowed_transitions().contains(next)
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            TicketStatus::Planned => "planned",
            TicketStatus::Queue => "queue",
            TicketStatus::Active => "active",
            TicketStatus::Review => "review",
            TicketStatus::Blocked => "blocked",
            TicketStatus::Done => "done",
            TicketStatus::Archived => "archived",
        }
    }
}

impl StepStatus {
    pub fn allowed_transitions(&self) -> &'static [StepStatus] {
        use StepStatus::*;
        match self {
            Pending => &[Running, Skipped],
            Running => &[Passed, Failed],
            Passed => &[],
            Failed => &[Running, Skipped],
            Skipped => &[Pending],
        }
    }

    pub fn can_transition_to(&self, next: &StepStatus) -> bool {
        self.allowed_transitions().contains(next)
    }

    pub fn as_str(&self) -> &'static str {
        match self {
            StepStatus::Pending => "pending",
            StepStatus::Running => "running",
            StepStatus::Passed => "passed",
            StepStatus::Failed => "failed",
            StepStatus::Skipped => "skipped",
        }
    }
}
