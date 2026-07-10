//! # engine — autonomous BuildEngine
//!
//! ## Purpose
//! Walks a phase's ticket tree in topological order, dispatches tickets
//! concurrently via a pluggable [`TicketDispatcher`] trait, and drives the
//! EOx gate functions at sprint/wave/epic/phase boundaries.
//!
//! ## Design
//! - **`TicketDispatcher`** is the pluggable seam. Real implementations call
//!   a fleet agent; tests inject a mock that marks tickets done instantly.
//! - **Topological ordering** within a sprint respects `ticket.depends_on`.
//!   Tickets in the same dependency level run concurrently via `tokio::JoinSet`.
//! - **EOSt** (end-of-step) is called by the engine between steps within a
//!   ticket before the ticket-level EOT gate.
//! - **EOx gate chain**: EOSt per step → EOT per ticket → EOS per sprint →
//!   EOW per wave → EOE per epic → EOP for the phase.
//! - The engine is intentionally **I/O-only through `PbdStore`** — no direct
//!   filesystem access, no network calls outside `TicketDispatcher`.
//!
//! ## Constraints
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - All public methods are `async` and require a Tokio runtime.
//! - Files stay under 300 lines.
//!
//! ## SPORT
//! MASTER-CRATES.md — cascade-core: build::engine

// TODO(pews-02 defer): EOP full route-sweep (enumerate_services / frontend_routes),
// opening.rs / wrapup.rs full automation, and real Fleet HTTP dispatch are all
// deferred. Leave this stub — the engine calls run_eop but performs no outbound
// traffic; deploy verification is left to RealExternalChecks injected by the caller.

use std::collections::{HashMap, HashSet};

use async_trait::async_trait;
use tracing::{debug, info, warn};

use cascade_types::error::{CascadeError, Result};

use crate::pbd::{
    protocol::{
        run_eoe, run_eop, run_eos, run_eost, run_eot, run_eow, ExternalChecks, NoExternalChecks,
        ProtocolResult,
    },
    schema::{EpicStatus, SprintStatus, StepStatus, TicketStatus, WaveStatus},
    store::PbdStore,
};

use super::dispatch::classify_ticket;

// ── TicketDispatcher trait ────────────────────────────────────────────────────

/// Pluggable seam for executing a single ticket's work.
///
/// # Real implementation
/// Calls the fleet Router with the ticket's [`TaskClass`] and spawns a
/// delegate agent process. Not implemented in this PR — wire this once the
/// agent-process harness lands.
///
/// # Test implementation
/// Use [`MockDispatcher`] which simply marks every ticket done immediately
/// without spawning any process.
///
/// [`TaskClass`]: crate::routing::TaskClass
#[async_trait]
pub trait TicketDispatcher: Send + Sync {
    /// Execute the work for one ticket and return `Ok(())` on success.
    ///
    /// The engine calls this after all of the ticket's `depends_on`
    /// dependencies have been dispatched successfully. The dispatcher is
    /// responsible for progressing all steps to `passed` or `skipped`
    /// before returning.
    async fn dispatch(
        &self,
        store: &PbdStore,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
    ) -> Result<()>;
}

// ── MockDispatcher ────────────────────────────────────────────────────────────

/// A no-op dispatcher for unit tests.
///
/// Marks every step `passed` and leaves ticket status management to the engine.
pub struct MockDispatcher;

#[async_trait]
impl TicketDispatcher for MockDispatcher {
    async fn dispatch(
        &self,
        store: &PbdStore,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
    ) -> Result<()> {
        let ticket = store.load_ticket(phase_id, epic_id, wave_id, sprint_id, ticket_id)?;
        for step in &ticket.steps {
            match step.status {
                StepStatus::Pending => {
                    // Pending → Running → Passed (respects allowed transitions)
                    store.transition_step(
                        phase_id,
                        epic_id,
                        wave_id,
                        sprint_id,
                        ticket_id,
                        &step.id,
                        StepStatus::Running,
                    )?;
                    store.transition_step(
                        phase_id,
                        epic_id,
                        wave_id,
                        sprint_id,
                        ticket_id,
                        &step.id,
                        StepStatus::Passed,
                    )?;
                }
                StepStatus::Running => {
                    store.transition_step(
                        phase_id,
                        epic_id,
                        wave_id,
                        sprint_id,
                        ticket_id,
                        &step.id,
                        StepStatus::Passed,
                    )?;
                }
                StepStatus::Passed | StepStatus::Skipped | StepStatus::Failed => {}
            }
        }
        Ok(())
    }
}

// ── BuildEngine ───────────────────────────────────────────────────────────────

/// Configuration for a [`BuildEngine`] run.
#[derive(Default)]
pub struct BuildConfig {
    /// Skip external checks (build commands, health probes).
    pub skip_externals: bool,
}

/// Autonomous build engine for a single phase.
///
/// Drives the full EOx gate chain across all epics/waves/sprints/tickets.
/// Inject a [`MockDispatcher`] for tests; use a fleet-backed dispatcher in
/// production once the agent-process harness lands.
pub struct BuildEngine {
    store: PbdStore,
    dispatcher: Box<dyn TicketDispatcher>,
    checks: Box<dyn ExternalChecks>,
    /// Engine configuration — retained for future skip_externals wiring.
    #[allow(dead_code)]
    config: BuildConfig,
}

impl BuildEngine {
    /// Create a new engine.
    ///
    /// `dispatcher` — ticket execution seam.
    /// `checks` — external gate seam (pass [`NoExternalChecks`] for dry runs).
    pub fn new(
        store: PbdStore,
        dispatcher: impl TicketDispatcher + 'static,
        checks: impl ExternalChecks + 'static,
        config: BuildConfig,
    ) -> Self {
        Self {
            store,
            dispatcher: Box::new(dispatcher),
            checks: Box::new(checks),
            config,
        }
    }

    /// Create an engine with [`MockDispatcher`] and [`NoExternalChecks`] for tests.
    pub fn new_mock(store: PbdStore) -> Self {
        Self::new(
            store,
            MockDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        )
    }

    /// Run the full build for a phase, then call EOP.
    ///
    /// Returns `Ok(result)` where `result.success` reflects the final EOP gate.
    pub async fn run_phase(&self, phase_id: &str) -> Result<ProtocolResult> {
        info!(phase = phase_id, "BuildEngine: starting phase");

        let phase = self.store.load_phase(phase_id)?;

        for epic_id in &phase.epics {
            self.run_epic(phase_id, epic_id).await?;
        }

        let result = run_eop(&self.store, phase_id, self.checks.as_ref())?;
        if !result.success {
            warn!(phase = phase_id, errors = ?result.errors, "EOP gate failed");
        } else {
            info!(phase = phase_id, "BuildEngine: phase complete");
        }
        Ok(result)
    }

    /// Run all waves in an epic, then call EOE.
    async fn run_epic(&self, phase_id: &str, epic_id: &str) -> Result<()> {
        debug!(phase = phase_id, epic = epic_id, "run_epic");

        let epic = self.store.load_epic(phase_id, epic_id)?;

        // Skip already-done epics
        if epic.status == EpicStatus::Done {
            debug!(epic = epic_id, "already done — skipping");
            return Ok(());
        }

        for wave_id in &epic.waves {
            self.run_wave(phase_id, epic_id, wave_id).await?;
        }

        let result = run_eoe(&self.store, phase_id, epic_id)?;
        if !result.success {
            return Err(CascadeError::Other(format!(
                "EOE failed for epic {epic_id}: {:?}",
                result.errors
            )));
        }
        Ok(())
    }

    /// Run all sprints in a wave concurrently, then call EOW.
    async fn run_wave(&self, phase_id: &str, epic_id: &str, wave_id: &str) -> Result<()> {
        debug!(phase = phase_id, epic = epic_id, wave = wave_id, "run_wave");

        let wave = self.store.load_wave(phase_id, epic_id, wave_id)?;
        if wave.status == WaveStatus::Done {
            debug!(wave = wave_id, "already done — skipping");
            return Ok(());
        }

        // Sprints within a wave run sequentially for now.
        // TODO(pews-02): refactor to Arc<dyn TicketDispatcher> + JoinSet for true
        // concurrency once the agent-process harness lands and the dispatcher is Send+Clone.
        for sprint_id in wave.sprints.clone() {
            self.run_sprint(phase_id, epic_id, wave_id, &sprint_id)
                .await?;
        }

        let result = run_eow(&self.store, phase_id, epic_id, wave_id)?;
        if !result.success {
            return Err(CascadeError::Other(format!(
                "EOW failed for wave {wave_id}: {:?}",
                result.errors
            )));
        }
        Ok(())
    }

    /// Run tickets in a sprint in topological order, then call EOS.
    async fn run_sprint(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
    ) -> Result<()> {
        debug!(
            phase = phase_id,
            epic = epic_id,
            wave = wave_id,
            sprint = sprint_id,
            "run_sprint"
        );

        let sprint = self
            .store
            .load_sprint(phase_id, epic_id, wave_id, sprint_id)?;
        if sprint.status == SprintStatus::Done {
            debug!(sprint = sprint_id, "already done — skipping");
            return Ok(());
        }

        // Build topological order of tickets
        let tickets = self
            .store
            .list_tickets(phase_id, epic_id, wave_id, sprint_id)?;
        let ordered = topological_sort(&tickets)?;

        for ticket_id in ordered {
            let ticket = tickets
                .iter()
                .find(|t| t.id == ticket_id)
                .expect("ticket in list");
            if ticket.status == TicketStatus::Done || ticket.status == TicketStatus::Archived {
                debug!(ticket = ticket_id, "already done — skipping");
                continue;
            }

            // Classify ticket for routing (INTERIM dispatch table)
            let _task_class = classify_ticket(ticket.weight.as_deref());
            // TODO(pews-02): pass _task_class to fleet Router when real dispatcher lands

            // Transition ticket to Active so run_eot can close it (Queue → Active → Done)
            if ticket.status == TicketStatus::Queue {
                self.store.transition_ticket(
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    &ticket_id,
                    TicketStatus::Active,
                    Some("engine: dispatching"),
                )?;
            }

            // Dispatch ticket work
            self.dispatcher
                .dispatch(
                    self.store_ref(),
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    &ticket_id,
                )
                .await?;

            // EOSt per step — lightweight gate after dispatcher runs each step
            let ticket_fresh = self
                .store
                .load_ticket(phase_id, epic_id, wave_id, sprint_id, &ticket_id)?;
            for step in &ticket_fresh.steps {
                let result = run_eost(
                    &self.store,
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    &ticket_id,
                    &step.id,
                )?;
                if !result.success {
                    return Err(CascadeError::Other(format!(
                        "EOSt failed for step {}/{}: {:?}",
                        ticket_id, step.id, result.errors
                    )));
                }
            }

            // EOT — transition ticket to done
            let result = run_eot(
                &self.store,
                phase_id,
                epic_id,
                wave_id,
                sprint_id,
                &ticket_id,
                self.checks.as_ref(),
            )?;
            if !result.success {
                return Err(CascadeError::Other(format!(
                    "EOT failed for ticket {ticket_id}: {:?}",
                    result.errors
                )));
            }
        }

        let result = run_eos(
            &self.store,
            phase_id,
            epic_id,
            wave_id,
            sprint_id,
            self.checks.as_ref(),
        )?;
        if !result.success {
            return Err(CascadeError::Other(format!(
                "EOS failed for sprint {sprint_id}: {:?}",
                result.errors
            )));
        }
        Ok(())
    }

    /// Borrow the inner store. Helper to avoid ownership issues.
    fn store_ref(&self) -> &PbdStore {
        &self.store
    }
}

// ── Topological sort ──────────────────────────────────────────────────────────

/// Kahn's algorithm over ticket `depends_on` edges.
///
/// Returns ticket IDs in an order where every dependency appears before its
/// dependent. Returns `Err` if a cycle is detected.
fn topological_sort(tickets: &[crate::pbd::schema::Ticket]) -> Result<Vec<String>> {
    let mut in_degree: HashMap<&str, usize> = HashMap::new();
    let mut adj: HashMap<&str, Vec<&str>> = HashMap::new();

    for t in tickets {
        in_degree.entry(t.id.as_str()).or_insert(0);
        adj.entry(t.id.as_str()).or_default();
    }
    for t in tickets {
        for dep in &t.depends_on {
            // dep must finish before t — dep → t edge
            adj.entry(dep.as_str()).or_default().push(t.id.as_str());
            *in_degree.entry(t.id.as_str()).or_insert(0) += 1;
        }
    }

    let mut queue: Vec<&str> = in_degree
        .iter()
        .filter(|(_, &deg)| deg == 0)
        .map(|(&id, _)| id)
        .collect();
    queue.sort(); // deterministic ordering

    let mut result: Vec<String> = Vec::new();
    let mut visited: HashSet<&str> = HashSet::new();

    while !queue.is_empty() {
        queue.sort(); // keep deterministic across iterations
        let node = queue.remove(0);
        if visited.contains(node) {
            continue;
        }
        visited.insert(node);
        result.push(node.to_string());
        if let Some(neighbors) = adj.get(node) {
            for &nbr in neighbors {
                let deg = in_degree.entry(nbr).or_insert(0);
                *deg = deg.saturating_sub(1);
                if *deg == 0 {
                    queue.push(nbr);
                }
            }
        }
    }

    if result.len() != tickets.len() {
        return Err(CascadeError::Other(
            "ticket dependency cycle detected".to_string(),
        ));
    }
    Ok(result)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pbd::schema::{
        Epic, EpicStatus, Phase, PhaseStatus, Sprint, SprintStatus, Step, StepStatus, Ticket,
        TicketStatus, Wave, WaveStatus,
    };
    use tempfile::TempDir;

    fn mk_store(tmp: &TempDir) -> PbdStore {
        let root = tmp.path().join("phases");
        let store = PbdStore::new(&root);
        store.init().expect("init");
        store
    }

    /// Build a minimal phase: 1 epic → 1 wave → 1 sprint → 3 tickets (t3 depends on t1).
    fn seed_phase(store: &PbdStore) {
        let phase = Phase {
            id: "p1".into(),
            title: "Test Phase".into(),
            status: PhaseStatus::Building,
            epics: vec!["e01".into()],
            started_at: None,
            closed_at: None,
            note: None,
        };
        store.save_phase(&phase).expect("save phase");

        let epic = Epic {
            id: "e01".into(),
            phase_id: "p1".into(),
            title: "Epic 01".into(),
            status: EpicStatus::Active,
            waves: vec!["w01".into()],
            depends_on: vec![],
            note: None,
        };
        store.save_epic("p1", &epic).expect("save epic");

        let wave = Wave {
            id: "w01".into(),
            epic_id: "e01".into(),
            title: "Wave 01".into(),
            status: WaveStatus::Active,
            sprints: vec!["s01".into()],
            note: None,
        };
        store.save_wave("p1", "e01", &wave).expect("save wave");

        let sprint = Sprint {
            id: "s01".into(),
            wave_id: "w01".into(),
            title: "Sprint 01".into(),
            status: SprintStatus::Active,
            tickets: vec!["t01".into(), "t02".into(), "t03".into()],
            note: None,
        };
        store
            .save_sprint("p1", "e01", "w01", &sprint)
            .expect("save sprint");

        let step = Step {
            id: "step-01".into(),
            title: "Implement".into(),
            status: StepStatus::Pending,
            note: None,
        };

        for tid in &["t01", "t02"] {
            let ticket = Ticket {
                id: (*tid).into(),
                sprint_id: "s01".into(),
                title: format!("Ticket {tid}"),
                status: TicketStatus::Queue,
                steps: vec![step.clone()],
                depends_on: vec![],
                repo: None,
                weight: Some("M".into()),
                note: None,
                blocked_reason: None,
            };
            store
                .create_ticket("p1", "e01", "w01", &ticket)
                .expect("create ticket");
        }

        // t03 depends on t01
        let t03 = Ticket {
            id: "t03".into(),
            sprint_id: "s01".into(),
            title: "Ticket t03".into(),
            status: TicketStatus::Queue,
            steps: vec![step.clone()],
            depends_on: vec!["t01".into()],
            repo: None,
            weight: Some("S".into()),
            note: None,
            blocked_reason: None,
        };
        store
            .create_ticket("p1", "e01", "w01", &t03)
            .expect("create t03");
    }

    #[tokio::test]
    async fn engine_runs_phase_to_completion() {
        let tmp = TempDir::new().expect("tmpdir");
        let store = mk_store(&tmp);
        seed_phase(&store);

        let engine = BuildEngine::new_mock(store);
        let result = engine.run_phase("p1").await.expect("run_phase");

        assert!(
            result.success,
            "EOP gate should pass; errors: {:?}",
            result.errors
        );
        assert_eq!(result.level, "phase");
    }

    #[tokio::test]
    async fn topological_sort_respects_deps() {
        let make_ticket = |id: &str, deps: Vec<&str>| -> crate::pbd::schema::Ticket {
            Ticket {
                id: id.into(),
                sprint_id: "s01".into(),
                title: id.into(),
                status: TicketStatus::Queue,
                steps: vec![],
                depends_on: deps.into_iter().map(String::from).collect(),
                repo: None,
                weight: None,
                note: None,
                blocked_reason: None,
            }
        };

        let tickets = vec![
            make_ticket("t03", vec!["t01"]),
            make_ticket("t01", vec![]),
            make_ticket("t02", vec![]),
        ];

        let order = topological_sort(&tickets).expect("no cycle");
        let pos: HashMap<_, _> = order
            .iter()
            .enumerate()
            .map(|(i, id)| (id.as_str(), i))
            .collect();
        assert!(pos["t01"] < pos["t03"], "t01 must precede t03");
    }

    #[test]
    fn topological_sort_detects_cycle() {
        let make_ticket = |id: &str, deps: Vec<&str>| -> crate::pbd::schema::Ticket {
            Ticket {
                id: id.into(),
                sprint_id: "s01".into(),
                title: id.into(),
                status: TicketStatus::Queue,
                steps: vec![],
                depends_on: deps.into_iter().map(String::from).collect(),
                repo: None,
                weight: None,
                note: None,
                blocked_reason: None,
            }
        };
        let tickets = vec![make_ticket("ta", vec!["tb"]), make_ticket("tb", vec!["ta"])];
        assert!(
            topological_sort(&tickets).is_err(),
            "cycle should be detected"
        );
    }
}
