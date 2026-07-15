//! # engine — autonomous BuildEngine
//!
//! Walks a phase's ticket tree in topological order, dispatches tickets through
//! a pluggable [`TicketDispatcher`], and drives the EOx gate functions at each
//! sprint, wave, epic, and phase boundary.
//!
//! The engine performs I/O through [`PbdStore`] and delegates ticket execution
//! to the dispatcher. External verification is injected through
//! [`ExternalChecks`].

// TODO(pews-02 defer): EOP full route-sweep (enumerate_services / frontend_routes),
// opening.rs / wrapup.rs full automation, and real Fleet HTTP dispatch are all
// deferred. Leave this stub — the engine calls run_eop but performs no outbound
// traffic; deploy verification is left to RealExternalChecks injected by the caller.

use tracing::{debug, info, warn};

use cascade_types::error::{CascadeError, Result};

use crate::pbd::{
    protocol::{
        run_eoe, run_eop, run_eos, run_eost, run_eot, run_eow, ExternalChecks, NoExternalChecks,
        ProtocolResult,
    },
    schema::{EpicStatus, SprintStatus, TicketStatus, WaveStatus},
    store::PbdStore,
};

pub use super::dispatchers::{FleetDispatcher, MockDispatcher, TicketDispatcher};
use super::{dispatch::classify_ticket, topo::topological_sort};

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
