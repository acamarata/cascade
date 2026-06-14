//! PBD store — reads and writes the on-disk phases/ tree.
//!
//! # Purpose
//! Provides atomic load/save operations for every level of the PBD hierarchy.
//! All mutations update the YAML file, append to events.jsonl, and regenerate INDEX.yaml.
//!
//! # Filesystem layout (under <phases_root>/)
//! ```text
//! phases/
//!   INDEX.yaml
//!   events.jsonl          (append-only; one JSON object per line)
//!   current.yaml          (active pointers — regenerated on mutation)
//!   p1/
//!     phase.yaml
//!     e01/
//!       epic.yaml
//!       w01/
//!         wave.yaml
//!         s01/
//!           sprint.yaml
//!           T-P1-E01-W01-S01-01/
//!             ticket.yaml
//! ```
//!
//! # Constraints
//! - All writes are atomic: write to a `.tmp` sibling, then rename.
//! - events.jsonl is append-only and fsync'd per write.
//! - INDEX.yaml is regenerated in full after each mutation.

use std::{
    fs::{self, OpenOptions},
    io::Write as IoWrite,
    path::{Path, PathBuf},
};

use chrono::Utc;
use serde_yaml;

use cascade_types::error::{CascadeError, Result};

use super::schema::{
    CurrentPointers, Epic, EpicStatus, EventLevel, IndexEntry, PbdEvent, PbdIndex, Phase,
    PhaseStatus, Sprint, SprintStatus, StepStatus, Ticket, TicketStatus, Wave, WaveStatus,
};

// ── PbdStore ──────────────────────────────────────────────────────────────────

pub struct PbdStore {
    /// Root of the phases/ directory.
    root: PathBuf,
}

impl PbdStore {
    /// Create a store pointing at `phases_root`. The directory need not exist yet.
    pub fn new(phases_root: impl Into<PathBuf>) -> Self {
        Self {
            root: phases_root.into(),
        }
    }

    /// Returns the phases root directory.
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Ensure the phases directory exists.
    pub fn init(&self) -> Result<()> {
        fs::create_dir_all(&self.root).map_err(|e| CascadeError::Io {
            path: self.root.clone(),
            operation: "create phases dir",
            source: e,
        })
    }

    // ── Path helpers ─────────────────────────────────────────────────────────

    fn index_path(&self) -> PathBuf {
        self.root.join("INDEX.yaml")
    }

    fn events_path(&self) -> PathBuf {
        self.root.join("events.jsonl")
    }

    fn current_path(&self) -> PathBuf {
        self.root.join("current.yaml")
    }

    fn phase_dir(&self, phase_id: &str) -> PathBuf {
        self.root.join(phase_id)
    }

    fn phase_path(&self, phase_id: &str) -> PathBuf {
        self.phase_dir(phase_id).join("phase.yaml")
    }

    fn epic_dir(&self, phase_id: &str, epic_id: &str) -> PathBuf {
        self.phase_dir(phase_id).join(epic_id)
    }

    fn epic_path(&self, phase_id: &str, epic_id: &str) -> PathBuf {
        self.epic_dir(phase_id, epic_id).join("epic.yaml")
    }

    fn wave_dir(&self, phase_id: &str, epic_id: &str, wave_id: &str) -> PathBuf {
        self.epic_dir(phase_id, epic_id).join(wave_id)
    }

    fn wave_path(&self, phase_id: &str, epic_id: &str, wave_id: &str) -> PathBuf {
        self.wave_dir(phase_id, epic_id, wave_id).join("wave.yaml")
    }

    fn sprint_dir(&self, phase_id: &str, epic_id: &str, wave_id: &str, sprint_id: &str) -> PathBuf {
        self.wave_dir(phase_id, epic_id, wave_id).join(sprint_id)
    }

    fn sprint_path(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
    ) -> PathBuf {
        self.sprint_dir(phase_id, epic_id, wave_id, sprint_id)
            .join("sprint.yaml")
    }

    fn ticket_path(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
    ) -> PathBuf {
        self.sprint_dir(phase_id, epic_id, wave_id, sprint_id)
            .join(ticket_id)
            .join("ticket.yaml")
    }

    // ── YAML helpers ─────────────────────────────────────────────────────────

    fn write_yaml<T: serde::Serialize>(&self, path: &Path, value: &T) -> Result<()> {
        let dir = path.parent().unwrap_or(path);
        fs::create_dir_all(dir).map_err(|e| CascadeError::Io {
            path: dir.to_path_buf(),
            operation: "create dir",
            source: e,
        })?;
        let yaml = serde_yaml::to_string(value)
            .map_err(|e| CascadeError::Other(format!("yaml serialize: {e}")))?;
        let tmp = path.with_extension("tmp");
        fs::write(&tmp, &yaml).map_err(|e| CascadeError::Io {
            path: tmp.clone(),
            operation: "write yaml tmp",
            source: e,
        })?;
        fs::rename(&tmp, path).map_err(|e| CascadeError::Io {
            path: path.to_path_buf(),
            operation: "rename yaml tmp",
            source: e,
        })?;
        Ok(())
    }

    fn read_yaml<T: for<'de> serde::Deserialize<'de>>(&self, path: &Path) -> Result<T> {
        let content = fs::read_to_string(path).map_err(|e| CascadeError::Io {
            path: path.to_path_buf(),
            operation: "read yaml",
            source: e,
        })?;
        serde_yaml::from_str(&content)
            .map_err(|e| CascadeError::Other(format!("yaml parse {}: {e}", path.display())))
    }

    // ── Event log ─────────────────────────────────────────────────────────────

    /// Append one event to events.jsonl and fsync.
    pub fn append_event(&self, event: &PbdEvent) -> Result<()> {
        let line = serde_json::to_string(event)
            .map_err(|e| CascadeError::Other(format!("json event: {e}")))?;
        let mut file = OpenOptions::new()
            .create(true)
            .append(true)
            .open(self.events_path())
            .map_err(|e| CascadeError::Io {
                path: self.events_path(),
                operation: "open events.jsonl",
                source: e,
            })?;
        writeln!(file, "{}", line).map_err(|e| CascadeError::Io {
            path: self.events_path(),
            operation: "append event",
            source: e,
        })?;
        file.flush().map_err(|e| CascadeError::Io {
            path: self.events_path(),
            operation: "flush events.jsonl",
            source: e,
        })?;
        // fsync for crash safety
        file.sync_all().map_err(|e| CascadeError::Io {
            path: self.events_path(),
            operation: "fsync events.jsonl",
            source: e,
        })?;
        Ok(())
    }

    /// Read all events from events.jsonl.
    pub fn read_events(&self) -> Result<Vec<PbdEvent>> {
        let path = self.events_path();
        if !path.exists() {
            return Ok(vec![]);
        }
        let content = fs::read_to_string(&path).map_err(|e| CascadeError::Io {
            path,
            operation: "read events.jsonl",
            source: e,
        })?;
        let mut events = Vec::new();
        for line in content.lines() {
            let line = line.trim();
            if line.is_empty() {
                continue;
            }
            let ev: PbdEvent = serde_json::from_str(line)
                .map_err(|e| CascadeError::Other(format!("parse event line: {e}")))?;
            events.push(ev);
        }
        Ok(events)
    }

    // ── INDEX.yaml ────────────────────────────────────────────────────────────

    /// Regenerate INDEX.yaml by walking the phases tree.
    pub fn regen_index(&self) -> Result<()> {
        let mut entries: Vec<IndexEntry> = Vec::new();

        // Walk phase directories
        let phases = self.list_phases()?;
        for phase in &phases {
            entries.push(IndexEntry {
                kind: "phase".into(),
                id: phase.id.clone(),
                parent: None,
                title: phase.title.clone(),
                status: phase.status.as_str().into(),
            });
            for epic_id in &phase.epics {
                let epic_path = self.epic_path(&phase.id, epic_id);
                if epic_path.exists() {
                    let epic: Epic = self.read_yaml(&epic_path)?;
                    entries.push(IndexEntry {
                        kind: "epic".into(),
                        id: epic.id.clone(),
                        parent: Some(phase.id.clone()),
                        title: epic.title.clone(),
                        status: epic.status.as_str().into(),
                    });
                    for wave_id in &epic.waves {
                        let wave_path = self.wave_path(&phase.id, epic_id, wave_id);
                        if wave_path.exists() {
                            let wave: Wave = self.read_yaml(&wave_path)?;
                            entries.push(IndexEntry {
                                kind: "wave".into(),
                                id: wave.id.clone(),
                                parent: Some(epic.id.clone()),
                                title: wave.title.clone(),
                                status: wave.status.as_str().into(),
                            });
                            for sprint_id in &wave.sprints {
                                let sprint_path =
                                    self.sprint_path(&phase.id, epic_id, wave_id, sprint_id);
                                if sprint_path.exists() {
                                    let sprint: Sprint = self.read_yaml(&sprint_path)?;
                                    entries.push(IndexEntry {
                                        kind: "sprint".into(),
                                        id: sprint.id.clone(),
                                        parent: Some(wave.id.clone()),
                                        title: sprint.title.clone(),
                                        status: sprint.status.as_str().into(),
                                    });
                                    for ticket_id in &sprint.tickets {
                                        let t_path = self.ticket_path(
                                            &phase.id, epic_id, wave_id, sprint_id, ticket_id,
                                        );
                                        if t_path.exists() {
                                            let ticket: Ticket = self.read_yaml(&t_path)?;
                                            entries.push(IndexEntry {
                                                kind: "ticket".into(),
                                                id: ticket.id.clone(),
                                                parent: Some(sprint.id.clone()),
                                                title: ticket.title.clone(),
                                                status: ticket.status.as_str().into(),
                                            });
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }

        let index = PbdIndex { entries };
        self.write_yaml(&self.index_path(), &index)
    }

    pub fn read_index(&self) -> Result<PbdIndex> {
        let path = self.index_path();
        if !path.exists() {
            return Ok(PbdIndex::default());
        }
        self.read_yaml(&path)
    }

    // ── current.yaml ─────────────────────────────────────────────────────────

    pub fn read_current(&self) -> Result<CurrentPointers> {
        let path = self.current_path();
        if !path.exists() {
            return Ok(CurrentPointers::default());
        }
        self.read_yaml(&path)
    }

    pub fn write_current(&self, current: &CurrentPointers) -> Result<()> {
        self.write_yaml(&self.current_path(), current)
    }

    /// Rebuild current.yaml from the tree: find first active phase/epic/wave/sprint
    /// and collect active tickets.
    pub fn regen_current(&self) -> Result<()> {
        let mut ptr = CurrentPointers::default();
        let phases = self.list_phases()?;
        for phase in &phases {
            if phase.status == PhaseStatus::Building || phase.status == PhaseStatus::Qa {
                ptr.active_phase = Some(phase.id.clone());
                for epic_id in &phase.epics {
                    let epic_path = self.epic_path(&phase.id, epic_id);
                    if !epic_path.exists() {
                        continue;
                    }
                    let epic: Epic = self.read_yaml(&epic_path)?;
                    if epic.status == EpicStatus::Active {
                        ptr.active_epic = Some(epic.id.clone());
                        for wave_id in &epic.waves {
                            let wave_path = self.wave_path(&phase.id, epic_id, wave_id);
                            if !wave_path.exists() {
                                continue;
                            }
                            let wave: Wave = self.read_yaml(&wave_path)?;
                            if wave.status == WaveStatus::Active || wave.status == WaveStatus::Qa {
                                ptr.active_wave = Some(wave.id.clone());
                                for sprint_id in &wave.sprints {
                                    let sprint_path = self.sprint_path(
                                        &phase.id, epic_id, wave_id, sprint_id,
                                    );
                                    if !sprint_path.exists() {
                                        continue;
                                    }
                                    let sprint: Sprint = self.read_yaml(&sprint_path)?;
                                    if sprint.status == SprintStatus::Active
                                        || sprint.status == SprintStatus::Qa
                                    {
                                        ptr.active_sprint = Some(sprint.id.clone());
                                        for ticket_id in &sprint.tickets {
                                            let t_path = self.ticket_path(
                                                &phase.id,
                                                epic_id,
                                                wave_id,
                                                sprint_id,
                                                ticket_id,
                                            );
                                            if !t_path.exists() {
                                                continue;
                                            }
                                            let ticket: Ticket = self.read_yaml(&t_path)?;
                                            if ticket.status == TicketStatus::Active
                                                || ticket.status == TicketStatus::Review
                                            {
                                                ptr.active_tickets.push(ticket.id.clone());
                                            }
                                        }
                                    }
                                }
                                break; // first active wave
                            }
                        }
                        break; // first active epic
                    }
                }
                break; // first active phase
            }
        }
        self.write_current(&ptr)
    }

    // ── Phase CRUD ────────────────────────────────────────────────────────────

    pub fn create_phase(&self, phase: &Phase) -> Result<()> {
        let path = self.phase_path(&phase.id);
        self.write_yaml(&path, phase)?;
        self.emit_event(
            EventLevel::Phase,
            &phase.id,
            "none",
            phase.status.as_str(),
            Some("created"),
        )?;
        self.regen_index()?;
        self.regen_current()
    }

    pub fn load_phase(&self, phase_id: &str) -> Result<Phase> {
        self.read_yaml(&self.phase_path(phase_id))
    }

    pub fn save_phase(&self, phase: &Phase) -> Result<()> {
        self.write_yaml(&self.phase_path(&phase.id), phase)
    }

    pub fn list_phases(&self) -> Result<Vec<Phase>> {
        let mut phases = Vec::new();
        let read = match fs::read_dir(&self.root) {
            Ok(r) => r,
            Err(_) => return Ok(phases),
        };
        for entry in read.flatten() {
            let p = entry.path().join("phase.yaml");
            if p.exists() {
                let phase: Phase = self.read_yaml(&p)?;
                phases.push(phase);
            }
        }
        phases.sort_by(|a, b| a.id.cmp(&b.id));
        Ok(phases)
    }

    pub fn transition_phase(
        &self,
        phase_id: &str,
        new_status: PhaseStatus,
    ) -> Result<()> {
        let mut phase = self.load_phase(phase_id)?;
        if !phase.status.can_transition_to(&new_status) {
            return Err(CascadeError::Other(format!(
                "Invalid phase transition: {} -> {}",
                phase.status.as_str(),
                new_status.as_str()
            )));
        }
        let from = phase.status.as_str().to_string();
        let to = new_status.as_str().to_string();
        if new_status == PhaseStatus::Shipped || new_status == PhaseStatus::Archived {
            phase.closed_at = Some(Utc::now());
        }
        phase.status = new_status;
        self.save_phase(&phase)?;
        self.emit_event(EventLevel::Phase, phase_id, &from, &to, None)?;
        self.regen_index()?;
        self.regen_current()
    }

    // ── Epic CRUD ─────────────────────────────────────────────────────────────

    pub fn create_epic(&self, epic: &Epic) -> Result<()> {
        let path = self.epic_path(&epic.phase_id, &epic.id);
        self.write_yaml(&path, epic)?;
        // Register epic in phase
        let mut phase = self.load_phase(&epic.phase_id)?;
        if !phase.epics.contains(&epic.id) {
            phase.epics.push(epic.id.clone());
            self.save_phase(&phase)?;
        }
        self.emit_event(
            EventLevel::Epic,
            &epic.id,
            "none",
            epic.status.as_str(),
            Some("created"),
        )?;
        self.regen_index()?;
        self.regen_current()
    }

    pub fn load_epic(&self, phase_id: &str, epic_id: &str) -> Result<Epic> {
        self.read_yaml(&self.epic_path(phase_id, epic_id))
    }

    pub fn save_epic(&self, phase_id: &str, epic: &Epic) -> Result<()> {
        self.write_yaml(&self.epic_path(phase_id, &epic.id), epic)
    }

    pub fn list_epics(&self, phase_id: &str) -> Result<Vec<Epic>> {
        let phase = self.load_phase(phase_id)?;
        let mut epics = Vec::new();
        for epic_id in &phase.epics {
            let p = self.epic_path(phase_id, epic_id);
            if p.exists() {
                let epic: Epic = self.read_yaml(&p)?;
                epics.push(epic);
            }
        }
        Ok(epics)
    }

    pub fn transition_epic(
        &self,
        phase_id: &str,
        epic_id: &str,
        new_status: EpicStatus,
    ) -> Result<()> {
        let mut epic = self.load_epic(phase_id, epic_id)?;
        if !epic.status.can_transition_to(&new_status) {
            return Err(CascadeError::Other(format!(
                "Invalid epic transition: {} -> {}",
                epic.status.as_str(),
                new_status.as_str()
            )));
        }
        let from = epic.status.as_str().to_string();
        let to = new_status.as_str().to_string();
        epic.status = new_status;
        self.save_epic(phase_id, &epic)?;
        self.emit_event(EventLevel::Epic, epic_id, &from, &to, None)?;
        self.regen_index()?;
        self.regen_current()
    }

    // ── Wave CRUD ─────────────────────────────────────────────────────────────

    pub fn create_wave(&self, phase_id: &str, wave: &Wave) -> Result<()> {
        let path = self.wave_path(phase_id, &wave.epic_id, &wave.id);
        self.write_yaml(&path, wave)?;
        let mut epic = self.load_epic(phase_id, &wave.epic_id)?;
        if !epic.waves.contains(&wave.id) {
            epic.waves.push(wave.id.clone());
            self.save_epic(phase_id, &epic)?;
        }
        self.emit_event(
            EventLevel::Wave,
            &wave.id,
            "none",
            wave.status.as_str(),
            Some("created"),
        )?;
        self.regen_index()?;
        self.regen_current()
    }

    pub fn load_wave(&self, phase_id: &str, epic_id: &str, wave_id: &str) -> Result<Wave> {
        self.read_yaml(&self.wave_path(phase_id, epic_id, wave_id))
    }

    pub fn save_wave(&self, phase_id: &str, epic_id: &str, wave: &Wave) -> Result<()> {
        self.write_yaml(&self.wave_path(phase_id, epic_id, &wave.id), wave)
    }

    pub fn list_waves(&self, phase_id: &str, epic_id: &str) -> Result<Vec<Wave>> {
        let epic = self.load_epic(phase_id, epic_id)?;
        let mut waves = Vec::new();
        for wave_id in &epic.waves {
            let p = self.wave_path(phase_id, epic_id, wave_id);
            if p.exists() {
                let wave: Wave = self.read_yaml(&p)?;
                waves.push(wave);
            }
        }
        Ok(waves)
    }

    pub fn transition_wave(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        new_status: WaveStatus,
    ) -> Result<()> {
        let mut wave = self.load_wave(phase_id, epic_id, wave_id)?;
        if !wave.status.can_transition_to(&new_status) {
            return Err(CascadeError::Other(format!(
                "Invalid wave transition: {} -> {}",
                wave.status.as_str(),
                new_status.as_str()
            )));
        }
        let from = wave.status.as_str().to_string();
        let to = new_status.as_str().to_string();
        wave.status = new_status;
        self.save_wave(phase_id, epic_id, &wave)?;
        self.emit_event(EventLevel::Wave, wave_id, &from, &to, None)?;
        self.regen_index()?;
        self.regen_current()
    }

    // ── Sprint CRUD ───────────────────────────────────────────────────────────

    pub fn create_sprint(&self, phase_id: &str, epic_id: &str, sprint: &Sprint) -> Result<()> {
        let path = self.sprint_path(phase_id, epic_id, &sprint.wave_id, &sprint.id);
        self.write_yaml(&path, sprint)?;
        let mut wave = self.load_wave(phase_id, epic_id, &sprint.wave_id)?;
        if !wave.sprints.contains(&sprint.id) {
            wave.sprints.push(sprint.id.clone());
            self.save_wave(phase_id, epic_id, &wave)?;
        }
        self.emit_event(
            EventLevel::Sprint,
            &sprint.id,
            "none",
            sprint.status.as_str(),
            Some("created"),
        )?;
        self.regen_index()?;
        self.regen_current()
    }

    pub fn load_sprint(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
    ) -> Result<Sprint> {
        self.read_yaml(&self.sprint_path(phase_id, epic_id, wave_id, sprint_id))
    }

    pub fn save_sprint(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint: &Sprint,
    ) -> Result<()> {
        self.write_yaml(
            &self.sprint_path(phase_id, epic_id, wave_id, &sprint.id),
            sprint,
        )
    }

    pub fn list_sprints(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
    ) -> Result<Vec<Sprint>> {
        let wave = self.load_wave(phase_id, epic_id, wave_id)?;
        let mut sprints = Vec::new();
        for sprint_id in &wave.sprints {
            let p = self.sprint_path(phase_id, epic_id, wave_id, sprint_id);
            if p.exists() {
                let sprint: Sprint = self.read_yaml(&p)?;
                sprints.push(sprint);
            }
        }
        Ok(sprints)
    }

    pub fn transition_sprint(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        new_status: SprintStatus,
    ) -> Result<()> {
        let mut sprint = self.load_sprint(phase_id, epic_id, wave_id, sprint_id)?;
        if !sprint.status.can_transition_to(&new_status) {
            return Err(CascadeError::Other(format!(
                "Invalid sprint transition: {} -> {}",
                sprint.status.as_str(),
                new_status.as_str()
            )));
        }
        let from = sprint.status.as_str().to_string();
        let to = new_status.as_str().to_string();
        sprint.status = new_status;
        self.save_sprint(phase_id, epic_id, wave_id, &sprint)?;
        self.emit_event(EventLevel::Sprint, sprint_id, &from, &to, None)?;
        self.regen_index()?;
        self.regen_current()
    }

    // ── Ticket CRUD ───────────────────────────────────────────────────────────

    pub fn create_ticket(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        ticket: &Ticket,
    ) -> Result<()> {
        let path = self.ticket_path(phase_id, epic_id, wave_id, &ticket.sprint_id, &ticket.id);
        self.write_yaml(&path, ticket)?;
        let mut sprint = self.load_sprint(phase_id, epic_id, wave_id, &ticket.sprint_id)?;
        if !sprint.tickets.contains(&ticket.id) {
            sprint.tickets.push(ticket.id.clone());
            self.save_sprint(phase_id, epic_id, wave_id, &sprint)?;
        }
        self.emit_event(
            EventLevel::Ticket,
            &ticket.id,
            "none",
            ticket.status.as_str(),
            Some("created"),
        )?;
        self.regen_index()?;
        self.regen_current()
    }

    pub fn load_ticket(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
    ) -> Result<Ticket> {
        self.read_yaml(&self.ticket_path(phase_id, epic_id, wave_id, sprint_id, ticket_id))
    }

    pub fn save_ticket(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        ticket: &Ticket,
    ) -> Result<()> {
        self.write_yaml(
            &self.ticket_path(phase_id, epic_id, wave_id, &ticket.sprint_id, &ticket.id),
            ticket,
        )
    }

    pub fn list_tickets(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
    ) -> Result<Vec<Ticket>> {
        let sprint = self.load_sprint(phase_id, epic_id, wave_id, sprint_id)?;
        let mut tickets = Vec::new();
        for ticket_id in &sprint.tickets {
            let p = self.ticket_path(phase_id, epic_id, wave_id, sprint_id, ticket_id);
            if p.exists() {
                let ticket: Ticket = self.read_yaml(&p)?;
                tickets.push(ticket);
            }
        }
        Ok(tickets)
    }

    pub fn transition_ticket(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
        new_status: TicketStatus,
        note: Option<&str>,
    ) -> Result<()> {
        let mut ticket = self.load_ticket(phase_id, epic_id, wave_id, sprint_id, ticket_id)?;
        if !ticket.status.can_transition_to(&new_status) {
            return Err(CascadeError::Other(format!(
                "Invalid ticket transition: {} -> {}",
                ticket.status.as_str(),
                new_status.as_str()
            )));
        }
        let from = ticket.status.as_str().to_string();
        let to = new_status.as_str().to_string();
        if new_status == TicketStatus::Blocked {
            ticket.blocked_reason = note.map(|s| s.to_string());
        }
        ticket.status = new_status;
        self.save_ticket(phase_id, epic_id, wave_id, &ticket)?;
        self.emit_event(
            EventLevel::Ticket,
            ticket_id,
            &from,
            &to,
            note,
        )?;
        self.regen_index()?;
        self.regen_current()
    }

    // ── Step helpers ──────────────────────────────────────────────────────────

    pub fn transition_step(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
        step_id: &str,
        new_status: StepStatus,
    ) -> Result<()> {
        let mut ticket = self.load_ticket(phase_id, epic_id, wave_id, sprint_id, ticket_id)?;
        let step = ticket
            .steps
            .iter_mut()
            .find(|s| s.id == step_id)
            .ok_or_else(|| CascadeError::Other(format!("Step {step_id} not found")))?;
        if !step.status.can_transition_to(&new_status) {
            return Err(CascadeError::Other(format!(
                "Invalid step transition: {} -> {}",
                step.status.as_str(),
                new_status.as_str()
            )));
        }
        let from = step.status.as_str().to_string();
        let to = new_status.as_str().to_string();
        step.status = new_status;
        self.save_ticket(phase_id, epic_id, wave_id, &ticket)?;
        self.emit_event(EventLevel::Step, step_id, &from, &to, None)?;
        Ok(())
    }

    // ── Internal helpers ──────────────────────────────────────────────────────

    fn emit_event(
        &self,
        level: EventLevel,
        id: &str,
        from: &str,
        to: &str,
        note: Option<&str>,
    ) -> Result<()> {
        let event = PbdEvent {
            ts: Utc::now(),
            actor: "cascade-cli".into(),
            level,
            id: id.to_string(),
            from: from.to_string(),
            to: to.to_string(),
            note: note.map(|s| s.to_string()),
        };
        self.append_event(&event)
    }
}

// ── Locate phases root from CWD ───────────────────────────────────────────────

/// Walk upward from `start` looking for a `.cascade/phases/` or `phases/` directory.
/// Returns None if not found within 8 ancestors.
pub fn locate_phases_root(start: &Path) -> Option<PathBuf> {
    let mut dir = start.to_path_buf();
    for _ in 0..8 {
        // Check .cascade/phases/
        let candidate = dir.join(".cascade").join("phases");
        if candidate.is_dir() {
            return Some(candidate);
        }
        // Also accept a bare phases/ at repo root
        let bare = dir.join("phases");
        if bare.is_dir() && bare.join("INDEX.yaml").exists() {
            return Some(bare);
        }
        if !dir.pop() {
            break;
        }
    }
    None
}

/// Resolve the phases root: use explicit override, else walk from CWD, else
/// create under CWD/.cascade/phases.
pub fn resolve_phases_root(explicit: Option<&Path>) -> PathBuf {
    if let Some(p) = explicit {
        return p.to_path_buf();
    }
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
    locate_phases_root(&cwd)
        .unwrap_or_else(|| cwd.join(".cascade").join("phases"))
}
