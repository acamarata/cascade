//! `cascade pbd` — Phase-Based Development CLI commands.
//!
//! # Purpose
//! Top-level `pbd` command plus subcommands for managing the full PBD hierarchy:
//! phase / epic / wave / sprint / ticket / step.
//!
//! # Subcommands
//! - `pbd current` — print active pointers (≤200 tok)
//! - `phase list|new|open|close|archive`
//! - `ticket create|list|update|close`
//! - `sprint status [<id>]`
//! - `epic list`
//! - `wave list`
//! - `eot <ticket>` / `eos <sprint>` / `eow <wave>` / `eoe <epic>` / `eop <phase>`

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};

use cascade_core::pbd::{
    self, index_pbd, index_repo,
    schema::{Phase, PhaseStatus, Ticket, TicketStatus},
    store::{resolve_phases_root, PbdStore},
    validate,
};

use super::Command;

// ── Top-level ─────────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct PbdArgs {
    /// Explicit path to the phases/ root directory.
    #[arg(long, global = true, value_name = "DIR")]
    pub phases: Option<PathBuf>,

    /// Output as JSON.
    #[arg(long, global = true)]
    pub json: bool,

    #[command(subcommand)]
    pub subcommand: PbdSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum PbdSubcmd {
    /// Print active pointers (phase/epic/wave/sprint/active tickets).
    Current(PbdCurrentArgs),
    /// Manage phases.
    Phase(PhaseArgs),
    /// Manage tickets.
    Ticket(TicketArgs),
    /// Show sprint status.
    Sprint(SprintArgs),
    /// List epics.
    Epic(EpicArgs),
    /// List waves.
    Wave(WaveArgs),
    /// End-of-ticket: verify steps, mark ticket done.
    Eot(EotArgs),
    /// End-of-sprint: verify tickets, mark sprint done.
    Eos(EosArgs),
    /// End-of-wave: verify sprints, mark wave done.
    Eow(EowArgs),
    /// End-of-epic: verify waves, mark epic done.
    Eoe(EoeArgs),
    /// End-of-phase: verify epics + externals, mark phase shipped.
    Eop(EopArgs),
    /// Validate the phases tree (PEWS linter). Use --repair to auto-fix.
    Validate(ValidateArgs),
    /// Index a source repo, generating masters/{repo}.yaml + MASTER-PHASES/TICKETS.
    Index(IndexArgs),
}

#[async_trait]
impl Command for PbdArgs {
    async fn run(&self) -> Result<()> {
        let phases_root = resolve_phases_root(self.phases.as_deref());
        let store = PbdStore::new(&phases_root);
        store.init()?;
        match &self.subcommand {
            PbdSubcmd::Current(a) => a.run_with(&store, self.json),
            PbdSubcmd::Phase(a) => a.run_with(&store, self.json),
            PbdSubcmd::Ticket(a) => a.run_with(&store, self.json),
            PbdSubcmd::Sprint(a) => a.run_with(&store, self.json),
            PbdSubcmd::Epic(a) => a.run_with(&store, self.json),
            PbdSubcmd::Wave(a) => a.run_with(&store, self.json),
            PbdSubcmd::Eot(a) => a.run_with(&store, self.json),
            PbdSubcmd::Eos(a) => a.run_with(&store, self.json),
            PbdSubcmd::Eow(a) => a.run_with(&store, self.json),
            PbdSubcmd::Eoe(a) => a.run_with(&store, self.json),
            PbdSubcmd::Eop(a) => a.run_with(&store, self.json),
            PbdSubcmd::Validate(a) => a.run_with(&store, self.json),
            PbdSubcmd::Index(a) => a.run_with(&store, self.json),
        }
    }
}

// ── pbd current ───────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct PbdCurrentArgs {}

impl PbdCurrentArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let ptr = store.read_current()?;
        if json {
            println!("{}", serde_json::to_string_pretty(&ptr).unwrap_or_default());
        } else {
            println!(
                "active_phase:  {}",
                ptr.active_phase.as_deref().unwrap_or("—")
            );
            println!(
                "active_epic:   {}",
                ptr.active_epic.as_deref().unwrap_or("—")
            );
            println!(
                "active_wave:   {}",
                ptr.active_wave.as_deref().unwrap_or("—")
            );
            println!(
                "active_sprint: {}",
                ptr.active_sprint.as_deref().unwrap_or("—")
            );
            if ptr.active_tickets.is_empty() {
                println!("active_tickets: (none)");
            } else {
                println!("active_tickets: {}", ptr.active_tickets.join(", "));
            }
        }
        Ok(())
    }
}

// ── phase ─────────────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct PhaseArgs {
    #[command(subcommand)]
    pub subcommand: PhaseSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum PhaseSubcmd {
    /// List all phases.
    List,
    /// Create a new phase.
    New(PhaseNewArgs),
    /// Open a phase (planning → ready_to_build).
    Open(PhaseIdArg),
    /// Close / ship a phase.
    Close(PhaseIdArg),
    /// Archive a phase.
    Archive(PhaseIdArg),
}

#[derive(Debug, Args)]
pub struct PhaseNewArgs {
    /// Phase ID, e.g. `p2`.
    pub id: String,
    /// Phase title.
    pub title: String,
}

#[derive(Debug, Args)]
pub struct PhaseIdArg {
    /// Phase ID.
    pub id: String,
}

impl PhaseArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        match &self.subcommand {
            PhaseSubcmd::List => {
                let phases = store.list_phases()?;
                if json {
                    println!(
                        "{}",
                        serde_json::to_string_pretty(&phases).unwrap_or_default()
                    );
                } else {
                    for p in &phases {
                        println!("[{}] {} — {:?}", p.id, p.title, p.status);
                    }
                }
            }
            PhaseSubcmd::New(a) => {
                let phase = Phase {
                    id: a.id.clone(),
                    title: a.title.clone(),
                    status: PhaseStatus::Planning,
                    epics: vec![],
                    started_at: Some(chrono::Utc::now()),
                    closed_at: None,
                    note: None,
                };
                store.create_phase(&phase)?;
                println!("Created phase: {}", a.id);
            }
            PhaseSubcmd::Open(a) => {
                store.transition_phase(&a.id, PhaseStatus::ReadyToBuild)?;
                println!("Phase {} → ready_to_build", a.id);
            }
            PhaseSubcmd::Close(a) => {
                store.transition_phase(&a.id, PhaseStatus::Wrapup)?;
                println!("Phase {} → wrapup", a.id);
            }
            PhaseSubcmd::Archive(a) => {
                store.transition_phase(&a.id, PhaseStatus::Archived)?;
                println!("Phase {} → archived", a.id);
            }
        }
        Ok(())
    }
}

// ── ticket ────────────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct TicketArgs {
    #[command(subcommand)]
    pub subcommand: TicketSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum TicketSubcmd {
    /// Create a ticket.
    Create(TicketCreateArgs),
    /// List tickets in a sprint.
    List(TicketListArgs),
    /// Update a ticket's status.
    Update(TicketUpdateArgs),
    /// Close (mark done) a ticket via the EOT checklist.
    Close(TicketCloseArgs),
}

#[derive(Debug, Args)]
pub struct TicketCreateArgs {
    /// Ticket ID, e.g. `T-P1-E01-W01-S01-01`.
    pub id: String,
    /// Ticket title.
    pub title: String,
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    #[arg(long)]
    pub sprint_id: String,
    #[arg(long)]
    pub repo: Option<String>,
    #[arg(long)]
    pub weight: Option<String>,
}

#[derive(Debug, Args)]
pub struct TicketListArgs {
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    #[arg(long)]
    pub sprint_id: String,
}

#[derive(Debug, Args)]
pub struct TicketUpdateArgs {
    pub id: String,
    #[arg(long)]
    pub status: String,
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    #[arg(long)]
    pub sprint_id: String,
    #[arg(long)]
    pub note: Option<String>,
}

#[derive(Debug, Args)]
pub struct TicketCloseArgs {
    pub id: String,
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    #[arg(long)]
    pub sprint_id: String,
    /// Skip external build/health checks (dry-run mode).
    #[arg(long)]
    pub skip_externals: bool,
}

impl TicketArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        match &self.subcommand {
            TicketSubcmd::Create(a) => {
                let ticket = Ticket {
                    id: a.id.clone(),
                    sprint_id: a.sprint_id.clone(),
                    title: a.title.clone(),
                    status: TicketStatus::Planned,
                    steps: vec![],
                    depends_on: vec![],
                    repo: a.repo.clone(),
                    weight: a.weight.clone(),
                    note: None,
                    blocked_reason: None,
                };
                store.create_ticket(&a.phase_id, &a.epic_id, &a.wave_id, &ticket)?;
                println!("Created ticket: {}", a.id);
            }
            TicketSubcmd::List(a) => {
                let tickets =
                    store.list_tickets(&a.phase_id, &a.epic_id, &a.wave_id, &a.sprint_id)?;
                if json {
                    println!(
                        "{}",
                        serde_json::to_string_pretty(&tickets).unwrap_or_default()
                    );
                } else {
                    for t in &tickets {
                        println!("[{}] {} — {:?}", t.id, t.title, t.status);
                    }
                }
            }
            TicketSubcmd::Update(a) => {
                let new_status = parse_ticket_status(&a.status)?;
                store.transition_ticket(
                    &a.phase_id,
                    &a.epic_id,
                    &a.wave_id,
                    &a.sprint_id,
                    &a.id,
                    new_status,
                    a.note.as_deref(),
                )?;
                println!("Ticket {} → {}", a.id, a.status);
            }
            TicketSubcmd::Close(a) => {
                let checks = make_checks(a.skip_externals);
                let result = pbd::run_eot(
                    store,
                    &a.phase_id,
                    &a.epic_id,
                    &a.wave_id,
                    &a.sprint_id,
                    &a.id,
                    checks.as_ref(),
                )?;
                print_protocol_result(&result, json);
            }
        }
        Ok(())
    }
}

// ── sprint ────────────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct SprintArgs {
    #[command(subcommand)]
    pub subcommand: SprintSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum SprintSubcmd {
    /// Show sprint status (tickets summary).
    Status(SprintStatusArgs),
    /// List sprints in a wave.
    List(SprintListArgs),
}

#[derive(Debug, Args)]
pub struct SprintStatusArgs {
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    #[arg(long)]
    pub sprint_id: String,
}

#[derive(Debug, Args)]
pub struct SprintListArgs {
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
}

impl SprintArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        match &self.subcommand {
            SprintSubcmd::Status(a) => {
                let sprint =
                    store.load_sprint(&a.phase_id, &a.epic_id, &a.wave_id, &a.sprint_id)?;
                let tickets =
                    store.list_tickets(&a.phase_id, &a.epic_id, &a.wave_id, &a.sprint_id)?;
                let done = tickets
                    .iter()
                    .filter(|t| {
                        t.status == TicketStatus::Done || t.status == TicketStatus::Archived
                    })
                    .count();
                if json {
                    let v = serde_json::json!({
                        "sprint": sprint.id,
                        "status": sprint.status,
                        "tickets_total": tickets.len(),
                        "tickets_done": done,
                    });
                    println!("{}", serde_json::to_string_pretty(&v).unwrap_or_default());
                } else {
                    println!(
                        "Sprint: {} [{:?}]  {}/{} tickets done",
                        sprint.id,
                        sprint.status,
                        done,
                        tickets.len()
                    );
                    for t in &tickets {
                        println!("  [{}] {} — {:?}", t.id, t.title, t.status);
                    }
                }
            }
            SprintSubcmd::List(a) => {
                let sprints = store.list_sprints(&a.phase_id, &a.epic_id, &a.wave_id)?;
                if json {
                    println!(
                        "{}",
                        serde_json::to_string_pretty(&sprints).unwrap_or_default()
                    );
                } else {
                    for s in &sprints {
                        println!("[{}] {} — {:?}", s.id, s.title, s.status);
                    }
                }
            }
        }
        Ok(())
    }
}

// ── epic ──────────────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct EpicArgs {
    #[command(subcommand)]
    pub subcommand: EpicSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum EpicSubcmd {
    /// List epics in a phase.
    List(EpicListArgs),
}

#[derive(Debug, Args)]
pub struct EpicListArgs {
    #[arg(long)]
    pub phase_id: String,
}

impl EpicArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        match &self.subcommand {
            EpicSubcmd::List(a) => {
                let epics = store.list_epics(&a.phase_id)?;
                if json {
                    println!(
                        "{}",
                        serde_json::to_string_pretty(&epics).unwrap_or_default()
                    );
                } else {
                    for e in &epics {
                        println!("[{}] {} — {:?}", e.id, e.title, e.status);
                    }
                }
            }
        }
        Ok(())
    }
}

// ── wave ──────────────────────────────────────────────────────────────────────

#[derive(Debug, Args)]
pub struct WaveArgs {
    #[command(subcommand)]
    pub subcommand: WaveSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum WaveSubcmd {
    /// List waves in an epic.
    List(WaveListArgs),
}

#[derive(Debug, Args)]
pub struct WaveListArgs {
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
}

impl WaveArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        match &self.subcommand {
            WaveSubcmd::List(a) => {
                let waves = store.list_waves(&a.phase_id, &a.epic_id)?;
                if json {
                    println!(
                        "{}",
                        serde_json::to_string_pretty(&waves).unwrap_or_default()
                    );
                } else {
                    for w in &waves {
                        println!("[{}] {} — {:?}", w.id, w.title, w.status);
                    }
                }
            }
        }
        Ok(())
    }
}

// ── Protocol runners ──────────────────────────────────────────────────────────

/// Build and return the appropriate [`ExternalChecks`] provider for the CLI.
///
/// When `skip_externals` is `true`, returns the no-op stub so all external
/// gates pass immediately (useful for dry runs and testing). Otherwise returns
/// a [`pbd::RealExternalChecks`] with default settings (no build commands or
/// health probes configured — users extend this by subclassing the provider in
/// their own wrappers or by passing `--skip-externals` for dry runs).
fn make_checks(skip_externals: bool) -> Box<dyn pbd::ExternalChecks> {
    if skip_externals {
        Box::new(pbd::NoExternalChecks)
    } else {
        Box::new(pbd::RealExternalChecks::new())
    }
}

#[derive(Debug, Args)]
pub struct EotArgs {
    pub ticket_id: String,
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    #[arg(long)]
    pub sprint_id: String,
    /// Skip external build/health checks (dry-run mode).
    #[arg(long)]
    pub skip_externals: bool,
}

impl EotArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let checks = make_checks(self.skip_externals);
        let result = pbd::run_eot(
            store,
            &self.phase_id,
            &self.epic_id,
            &self.wave_id,
            &self.sprint_id,
            &self.ticket_id,
            checks.as_ref(),
        )?;
        print_protocol_result(&result, json);
        if !result.success {
            return Err(CascadeError::Other("EOT failed — see errors above".into()));
        }
        Ok(())
    }
}

#[derive(Debug, Args)]
pub struct EosArgs {
    pub sprint_id: String,
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
    #[arg(long)]
    pub wave_id: String,
    /// Skip external build/health checks (dry-run mode).
    #[arg(long)]
    pub skip_externals: bool,
}

impl EosArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let checks = make_checks(self.skip_externals);
        let result = pbd::run_eos(
            store,
            &self.phase_id,
            &self.epic_id,
            &self.wave_id,
            &self.sprint_id,
            checks.as_ref(),
        )?;
        print_protocol_result(&result, json);
        if !result.success {
            return Err(CascadeError::Other("EOS failed — see errors above".into()));
        }
        Ok(())
    }
}

#[derive(Debug, Args)]
pub struct EowArgs {
    pub wave_id: String,
    #[arg(long)]
    pub phase_id: String,
    #[arg(long)]
    pub epic_id: String,
}

impl EowArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let result = pbd::run_eow(store, &self.phase_id, &self.epic_id, &self.wave_id)?;
        print_protocol_result(&result, json);
        if !result.success {
            return Err(CascadeError::Other("EOW failed — see errors above".into()));
        }
        Ok(())
    }
}

#[derive(Debug, Args)]
pub struct EoeArgs {
    pub epic_id: String,
    #[arg(long)]
    pub phase_id: String,
}

impl EoeArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let result = pbd::run_eoe(store, &self.phase_id, &self.epic_id)?;
        print_protocol_result(&result, json);
        if !result.success {
            return Err(CascadeError::Other("EOE failed — see errors above".into()));
        }
        Ok(())
    }
}

#[derive(Debug, Args)]
pub struct EopArgs {
    pub phase_id: String,
    /// Skip external build/health checks (dry-run mode).
    #[arg(long)]
    pub skip_externals: bool,
}

impl EopArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let checks = make_checks(self.skip_externals);
        let result = pbd::run_eop(store, &self.phase_id, checks.as_ref())?;
        print_protocol_result(&result, json);
        if !result.success {
            return Err(CascadeError::Other("EOP failed — see errors above".into()));
        }
        Ok(())
    }
}

// ── pbd validate ─────────────────────────────────────────────────────────────

/// Arguments for `cascade pbd validate`.
#[derive(Debug, Args)]
pub struct ValidateArgs {
    /// Auto-repair fixable issues (recompute counts, regen INDEX, normalize status).
    #[arg(long)]
    pub repair: bool,
}

impl ValidateArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let result = validate(store, self.repair)?;
        if json {
            println!(
                "{}",
                serde_json::to_string_pretty(&result).unwrap_or_default()
            );
        } else {
            if result.issues.is_empty() {
                println!("OK  phases tree is clean");
            } else {
                for issue in &result.issues {
                    let level = match issue.level {
                        cascade_core::pbd::IssueLevel::Error => "ERROR",
                        cascade_core::pbd::IssueLevel::Warning => "WARN ",
                    };
                    let path_suffix = issue
                        .path
                        .as_ref()
                        .map(|p| format!("  [{}]", p.display()))
                        .unwrap_or_default();
                    println!(
                        "[{}] [{}] {}{}",
                        level, issue.check, issue.message, path_suffix
                    );
                }
            }
            if !result.repaired.is_empty() {
                println!("\nRepaired:");
                for r in &result.repaired {
                    println!("  + {r}");
                }
            }
        }
        let error_count = result.errors().len();
        if error_count > 0 {
            return Err(CascadeError::Other(format!(
                "Validation failed with {error_count} error(s)"
            )));
        }
        Ok(())
    }
}

// ── pbd index ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade pbd index`.
#[derive(Debug, Args)]
pub struct IndexArgs {
    /// Path to the source repository to index.
    pub repo_path: PathBuf,
    /// Directory to write master YAML files into (default: .cascade/phases/masters/).
    #[arg(long, value_name = "DIR")]
    pub masters_dir: Option<PathBuf>,
    /// Also regenerate MASTER-PHASES.yaml and MASTER-TICKETS.yaml from the PBD tree.
    #[arg(long)]
    pub pbd: bool,
}

impl IndexArgs {
    fn run_with(&self, store: &PbdStore, json: bool) -> Result<()> {
        let masters_dir = self
            .masters_dir
            .clone()
            .unwrap_or_else(|| store.root().join("masters"));

        // Index the source repo
        let list = index_repo(&self.repo_path, &masters_dir)?;
        if json {
            println!(
                "{}",
                serde_json::to_string_pretty(&list).unwrap_or_default()
            );
        } else {
            println!(
                "Indexed '{}': {} entities → {}",
                list.repo,
                list.entities.len(),
                masters_dir.join(format!("{}.yaml", list.repo)).display()
            );
        }

        // Optionally also regen PBD master lists
        if self.pbd {
            let (phases, tickets) = index_pbd(store, &masters_dir)?;
            if !json {
                println!("MASTER-PHASES.yaml: {} phases", phases.phases.len());
                println!("MASTER-TICKETS.yaml: {} tickets", tickets.tickets.len());
            }
        }

        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_ticket_status(s: &str) -> Result<TicketStatus> {
    match s {
        "planned" => Ok(TicketStatus::Planned),
        "queue" => Ok(TicketStatus::Queue),
        "active" => Ok(TicketStatus::Active),
        "review" => Ok(TicketStatus::Review),
        "blocked" => Ok(TicketStatus::Blocked),
        "done" => Ok(TicketStatus::Done),
        "archived" => Ok(TicketStatus::Archived),
        _ => Err(CascadeError::Other(format!("Unknown ticket status: {s}"))),
    }
}

fn print_protocol_result(result: &pbd::ProtocolResult, json: bool) {
    if json {
        let v = serde_json::json!({
            "level": result.level,
            "id": result.id,
            "success": result.success,
            "errors": result.errors,
            "sidecar": result.sidecar_path.as_ref().map(|p| p.display().to_string()),
        });
        println!("{}", serde_json::to_string_pretty(&v).unwrap_or_default());
    } else if result.success {
        println!(
            "OK {} {} → done. Sidecar: {}",
            result.level,
            result.id,
            result
                .sidecar_path
                .as_ref()
                .map(|p| p.display().to_string())
                .unwrap_or_else(|| "—".into())
        );
    } else {
        println!("FAIL {} {}", result.level, result.id);
        for e in &result.errors {
            println!("  ERROR: {e}");
        }
    }
}
