//! PBD protocol runners — eot / eos / eow / eoe / eop lifecycle gates.
//!
//! # Purpose
//! Each `run_*` function enforces the "end-of-X" checklist:
//! - Verify all children are in a terminal state before closing the parent.
//! - Update the parent YAML + emit an event.
//! - Write a sidecar `.done` marker file.
//! - Run external verification checks via the [`ExternalChecks`] seam.
//! - Return a structured report.
//!
//! # External checks
//! Real build, test, and health-endpoint verification lives in
//! [`crate::pbd::external_checks::RealExternalChecks`]. Pass
//! [`NoExternalChecks`] (or the CLI `--skip-externals` flag) for dry runs.
//! Deploy and outbound side-effects are never triggered by this module.

use cascade_types::error::{CascadeError, Result};

use super::{
    schema::{EpicStatus, PhaseStatus, SprintStatus, StepStatus, TicketStatus, WaveStatus},
    store::PbdStore,
};

// ── Seam types ────────────────────────────────────────────────────────────────

/// Result of a protocol run.
#[derive(Debug)]
pub struct ProtocolResult {
    pub level: &'static str,
    pub id: String,
    pub success: bool,
    pub errors: Vec<String>,
    pub sidecar_path: Option<std::path::PathBuf>,
}

/// Injectable seam for external verification at every PBD phase gate.
///
/// Implement this trait to plug build commands, test runners, and HTTP health
/// probes into `run_eot`, `run_eos`, and `run_eop`. Return an empty `Vec` when
/// all checks pass; any string in the returned `Vec` is treated as a gate
/// failure and surfaces in the [`ProtocolResult`] error list.
///
/// Deploy and outbound side-effects **must not** be triggered inside
/// implementations of this trait. Those actions must be performed externally
/// and verified by their own CI/CD gate. See
/// [`crate::pbd::external_checks::RealExternalChecks`] for the built-in
/// provider; pass [`NoExternalChecks`] for dry runs.
pub trait ExternalChecks: Send + Sync {
    /// Run all configured checks for the given entity context.
    ///
    /// `context_id` is a slash-separated entity path, e.g.:
    /// - `"p1/e01/w01/s01/t01"` from `run_eot`
    /// - `"p1/e01/w01/s01"` from `run_eos`
    /// - `"p1"` from `run_eop`
    ///
    /// Return an empty `Vec` if every check passed, or one message per failure.
    fn run_checks(&self, context_id: &str) -> Result<Vec<String>>;
}

/// No-op implementation used for dry runs and when no external checks are
/// configured. All gates pass immediately.
pub struct NoExternalChecks;

impl ExternalChecks for NoExternalChecks {
    fn run_checks(&self, _context_id: &str) -> Result<Vec<String>> {
        Ok(vec![])
    }
}

// ── eot — end of ticket ───────────────────────────────────────────────────────

/// Run the EOT (end-of-ticket) checklist.
///
/// 1. Verifies all steps are `passed` or `skipped` (none may be `pending` or
///    `failed`).
/// 2. Runs external checks supplied via the `checks` seam (build commands,
///    health probes, etc.). Pass [`NoExternalChecks`] for a dry run.
/// 3. If all gates pass, transitions the ticket to `done` and writes a sidecar.
pub fn run_eot(
    store: &PbdStore,
    phase_id: &str,
    epic_id: &str,
    wave_id: &str,
    sprint_id: &str,
    ticket_id: &str,
    checks: &dyn ExternalChecks,
) -> Result<ProtocolResult> {
    let ticket = store.load_ticket(phase_id, epic_id, wave_id, sprint_id, ticket_id)?;
    let mut errors: Vec<String> = Vec::new();

    // Verify no step is pending or failed
    for step in &ticket.steps {
        match step.status {
            StepStatus::Pending => {
                errors.push(format!("Step '{}' is still pending", step.id));
            }
            StepStatus::Failed => {
                errors.push(format!("Step '{}' failed", step.id));
            }
            StepStatus::Running => {
                errors.push(format!("Step '{}' is still running", step.id));
            }
            StepStatus::Passed | StepStatus::Skipped => {}
        }
    }

    // Run external checks
    let context_id = format!(
        "{}/{}/{}/{}/{}",
        phase_id, epic_id, wave_id, sprint_id, ticket_id
    );
    match checks.run_checks(&context_id) {
        Ok(ext_errors) => errors.extend(ext_errors),
        Err(e) => errors.push(format!("External check error: {e}")),
    }

    if !errors.is_empty() {
        return Ok(ProtocolResult {
            level: "ticket",
            id: ticket_id.to_string(),
            success: false,
            errors,
            sidecar_path: None,
        });
    }

    // Transition to done
    store.transition_ticket(
        phase_id,
        epic_id,
        wave_id,
        sprint_id,
        ticket_id,
        TicketStatus::Done,
        Some("eot: all steps passed"),
    )?;

    // Write sidecar
    let sidecar = write_sidecar(store, &context_id, "ticket", ticket_id, "done")?;

    Ok(ProtocolResult {
        level: "ticket",
        id: ticket_id.to_string(),
        success: true,
        errors: vec![],
        sidecar_path: Some(sidecar),
    })
}

// ── eos — end of sprint ───────────────────────────────────────────────────────

/// Run the EOS (end-of-sprint) checklist.
///
/// 1. Verifies all tickets are `done` or `archived`.
/// 2. Runs external checks via the `checks` seam.
/// 3. Transitions sprint to `done`.
pub fn run_eos(
    store: &PbdStore,
    phase_id: &str,
    epic_id: &str,
    wave_id: &str,
    sprint_id: &str,
    checks: &dyn ExternalChecks,
) -> Result<ProtocolResult> {
    let sprint = store.load_sprint(phase_id, epic_id, wave_id, sprint_id)?;
    let mut errors: Vec<String> = Vec::new();

    for ticket_id in &sprint.tickets {
        let ticket_path_exists = {
            let tickets = store.list_tickets(phase_id, epic_id, wave_id, sprint_id)?;
            tickets.iter().any(|t| {
                t.id == *ticket_id
                    && (t.status == TicketStatus::Done || t.status == TicketStatus::Archived)
            })
        };
        if !ticket_path_exists {
            errors.push(format!("Ticket '{}' is not done/archived", ticket_id));
        }
    }

    // Run external checks
    let context_id = format!("{}/{}/{}/{}", phase_id, epic_id, wave_id, sprint_id);
    match checks.run_checks(&context_id) {
        Ok(ext_errors) => errors.extend(ext_errors),
        Err(e) => errors.push(format!("External check error: {e}")),
    }

    if !errors.is_empty() {
        return Ok(ProtocolResult {
            level: "sprint",
            id: sprint_id.to_string(),
            success: false,
            errors,
            sidecar_path: None,
        });
    }

    store.transition_sprint(phase_id, epic_id, wave_id, sprint_id, SprintStatus::Done)?;

    let sidecar = write_sidecar(store, &context_id, "sprint", sprint_id, "done")?;

    Ok(ProtocolResult {
        level: "sprint",
        id: sprint_id.to_string(),
        success: true,
        errors: vec![],
        sidecar_path: Some(sidecar),
    })
}

// ── eow — end of wave ─────────────────────────────────────────────────────────

/// Run the EOW (end-of-wave) checklist.
///
/// Verifies all sprints in the wave are `done`. Transitions wave to `done`.
pub fn run_eow(
    store: &PbdStore,
    phase_id: &str,
    epic_id: &str,
    wave_id: &str,
) -> Result<ProtocolResult> {
    let wave = store.load_wave(phase_id, epic_id, wave_id)?;
    let mut errors: Vec<String> = Vec::new();

    for sprint_id in &wave.sprints {
        let sprints = store.list_sprints(phase_id, epic_id, wave_id)?;
        let ok = sprints
            .iter()
            .any(|s| s.id == *sprint_id && s.status == SprintStatus::Done);
        if !ok {
            errors.push(format!("Sprint '{}' is not done", sprint_id));
        }
    }

    if !errors.is_empty() {
        return Ok(ProtocolResult {
            level: "wave",
            id: wave_id.to_string(),
            success: false,
            errors,
            sidecar_path: None,
        });
    }

    store.transition_wave(phase_id, epic_id, wave_id, WaveStatus::Done)?;

    let sidecar = write_sidecar(
        store,
        &format!("{}/{}/{}", phase_id, epic_id, wave_id),
        "wave",
        wave_id,
        "done",
    )?;

    Ok(ProtocolResult {
        level: "wave",
        id: wave_id.to_string(),
        success: true,
        errors: vec![],
        sidecar_path: Some(sidecar),
    })
}

// ── eoe — end of epic ─────────────────────────────────────────────────────────

/// Run the EOE (end-of-epic) checklist.
///
/// Verifies all waves are `done`. Transitions epic to `done`.
pub fn run_eoe(store: &PbdStore, phase_id: &str, epic_id: &str) -> Result<ProtocolResult> {
    let epic = store.load_epic(phase_id, epic_id)?;
    let mut errors: Vec<String> = Vec::new();

    for wave_id in &epic.waves {
        let waves = store.list_waves(phase_id, epic_id)?;
        let ok = waves
            .iter()
            .any(|w| w.id == *wave_id && w.status == WaveStatus::Done);
        if !ok {
            errors.push(format!("Wave '{}' is not done", wave_id));
        }
    }

    if !errors.is_empty() {
        return Ok(ProtocolResult {
            level: "epic",
            id: epic_id.to_string(),
            success: false,
            errors,
            sidecar_path: None,
        });
    }

    store.transition_epic(phase_id, epic_id, EpicStatus::Done)?;

    let sidecar = write_sidecar(
        store,
        &format!("{}/{}", phase_id, epic_id),
        "epic",
        epic_id,
        "done",
    )?;

    Ok(ProtocolResult {
        level: "epic",
        id: epic_id.to_string(),
        success: true,
        errors: vec![],
        sidecar_path: Some(sidecar),
    })
}

// ── eop — end of phase ────────────────────────────────────────────────────────

/// Run the EOP (end-of-phase) YAML lifecycle + verification.
///
/// 1. Verifies all epics are `done`.
/// 2. Runs external checks (build, health probes) via the `checks` seam.
/// 3. Transitions phase to `shipped` and writes a sidecar.
///
/// Pass [`NoExternalChecks`] or use `--skip-externals` on the CLI for a dry
/// run that skips all external gates.
pub fn run_eop(
    store: &PbdStore,
    phase_id: &str,
    checks: &dyn ExternalChecks,
) -> Result<ProtocolResult> {
    let phase = store.load_phase(phase_id)?;
    let mut errors: Vec<String> = Vec::new();

    // Verify all epics are done
    for epic_id in &phase.epics {
        let epics = store.list_epics(phase_id)?;
        let ok = epics
            .iter()
            .any(|e| e.id == *epic_id && e.status == EpicStatus::Done);
        if !ok {
            errors.push(format!("Epic '{}' is not done", epic_id));
        }
    }

    // Run external checks (E-P8-04 seam)
    match checks.run_checks(phase_id) {
        Ok(ext_errors) => errors.extend(ext_errors),
        Err(e) => errors.push(format!("External check error: {e}")),
    }

    if !errors.is_empty() {
        return Ok(ProtocolResult {
            level: "phase",
            id: phase_id.to_string(),
            success: false,
            errors,
            sidecar_path: None,
        });
    }

    store.transition_phase(phase_id, PhaseStatus::Shipped)?;

    let sidecar = write_sidecar(store, phase_id, "phase", phase_id, "shipped")?;

    Ok(ProtocolResult {
        level: "phase",
        id: phase_id.to_string(),
        success: true,
        errors: vec![],
        sidecar_path: Some(sidecar),
    })
}

// ── Sidecar writer ────────────────────────────────────────────────────────────

fn write_sidecar(
    store: &PbdStore,
    rel_path: &str,
    level: &str,
    id: &str,
    status: &str,
) -> Result<std::path::PathBuf> {
    // sidecar goes beside the entity dir as <rel_path>.done
    let sidecar_path = store.root().join(rel_path).with_extension("done");
    let content = format!(
        "level: {level}\nid: {id}\nstatus: {status}\nts: {}\n",
        chrono::Utc::now().to_rfc3339()
    );
    std::fs::write(&sidecar_path, &content).map_err(|e| CascadeError::Io {
        path: sidecar_path.clone(),
        operation: "write sidecar",
        source: e,
    })?;
    Ok(sidecar_path)
}
