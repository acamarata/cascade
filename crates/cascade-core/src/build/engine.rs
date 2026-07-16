//! # engine — autonomous BuildEngine
//!
//! Walks a phase's ticket tree in topological order, dispatches tickets through
//! a pluggable [`TicketDispatcher`], and drives the EOx gate functions at each
//! sprint, wave, epic, and phase boundary.
//!
//! The engine performs I/O through [`PbdStore`] and delegates ticket execution
//! to the dispatcher. External verification is injected through
//! [`ExternalChecks`].

// EOP intentionally performs no outbound traffic. Deploy verification remains
// the responsibility of the ExternalChecks implementation injected by the caller.

use std::{any::TypeId, collections::HashSet, time::Duration};

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
use super::{
    dispatch::{classify_ticket, run_fleet_cli, FleetOutcome},
    topo::topological_sort,
};

const STEP_COMPLETION_MARKER: &str = "CASCADE_STEP_COMPLETE:";
const MAX_FLEET_ATTEMPTS: usize = 3;
const FLEET_RETRY_BACKOFF: Duration = Duration::from_millis(100);

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
    uses_fleet_dispatcher: bool,
    checks: Box<dyn ExternalChecks>,
    config: BuildConfig,
}

impl BuildEngine {
    /// Create a new engine.
    ///
    /// `dispatcher` — ticket execution seam.
    /// `checks` — external gate seam (pass [`NoExternalChecks`] for dry runs).
    pub fn new<D, C>(
        store: PbdStore,
        dispatcher: D,
        checks: C,
        config: BuildConfig,
    ) -> Self
    where
        D: TicketDispatcher + 'static,
        C: ExternalChecks + 'static,
    {
        Self {
            store,
            dispatcher: Box::new(dispatcher),
            uses_fleet_dispatcher: TypeId::of::<D>() == TypeId::of::<FleetDispatcher>(),
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

        // Sprints within a wave run sequentially so their on-disk transitions
        // remain deterministic and resumable.
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
            self.dispatch_ticket(phase_id, epic_id, wave_id, sprint_id, &ticket_id)
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

    async fn dispatch_ticket(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
    ) -> Result<()> {
        if !self.uses_fleet_dispatcher {
            return self
                .dispatcher
                .dispatch(
                    self.store_ref(),
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    ticket_id,
                )
                .await;
        }

        let ticket = self
            .store
            .load_ticket(phase_id, epic_id, wave_id, sprint_id, ticket_id)?;
        let active_steps: Vec<_> = ticket
            .steps
            .iter()
            .filter(|step| {
                matches!(
                    step.status,
                    crate::pbd::schema::StepStatus::Pending
                        | crate::pbd::schema::StepStatus::Running
                        | crate::pbd::schema::StepStatus::Failed
                )
            })
            .collect();

        if active_steps.is_empty() {
            return Ok(());
        }

        for step in &active_steps {
            if matches!(
                step.status,
                crate::pbd::schema::StepStatus::Pending
                    | crate::pbd::schema::StepStatus::Failed
            ) {
                self.store.transition_step(
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    ticket_id,
                    &step.id,
                    crate::pbd::schema::StepStatus::Running,
                )?;
            }
        }

        let prompt = fleet_prompt(
            self.store.root(),
            phase_id,
            epic_id,
            wave_id,
            sprint_id,
            &ticket,
            &active_steps.iter().map(|step| step.id.as_str()).collect::<Vec<_>>(),
        );
        let mut attempt = 0;
        let outcome = loop {
            attempt += 1;
            let task_class = classify_ticket(ticket.weight.as_deref());
            let attempt_prompt = prompt.clone();
            let attempt_outcome =
                tokio::task::spawn_blocking(move || run_fleet_cli(task_class, &attempt_prompt))
                    .await
                    .map_err(|error| {
                        CascadeError::Other(format!("fleet dispatch join error: {error}"))
                    })?;

            if matches!(attempt_outcome, FleetOutcome::Failed { .. })
                && attempt < MAX_FLEET_ATTEMPTS
            {
                warn!(
                    ticket = ticket_id,
                    attempt,
                    max_attempts = MAX_FLEET_ATTEMPTS,
                    "transient fleet CLI failure — retrying"
                );
                tokio::time::sleep(FLEET_RETRY_BACKOFF).await;
                continue;
            }
            break attempt_outcome;
        };

        match outcome {
            FleetOutcome::Success { stdout } => {
                let completed = completion_markers(&stdout);
                let gate_context = format!(
                    "{phase_id}/{epic_id}/{wave_id}/{sprint_id}/{ticket_id}/steps"
                );
                let gate_errors = if self.config.skip_externals {
                    Vec::new()
                } else {
                    match self.checks.run_checks(&gate_context) {
                        Ok(errors) => errors,
                        Err(error) => vec![format!("external check error: {error}")],
                    }
                };

                if !gate_errors.is_empty() {
                    self.fail_running_steps(
                        phase_id,
                        epic_id,
                        wave_id,
                        sprint_id,
                        ticket_id,
                        active_steps.iter().map(|step| step.id.as_str()),
                    )?;
                    return Err(CascadeError::Other(format!(
                        "external step gate blocked ticket {ticket_id}: {gate_errors:?}"
                    )));
                }

                let mut missing = Vec::new();
                for step in &active_steps {
                    let status = if completed.contains(&step.id) {
                        crate::pbd::schema::StepStatus::Passed
                    } else {
                        missing.push(step.id.clone());
                        crate::pbd::schema::StepStatus::Failed
                    };
                    self.store.transition_step(
                        phase_id,
                        epic_id,
                        wave_id,
                        sprint_id,
                        ticket_id,
                        &step.id,
                        status,
                    )?;
                }

                if missing.is_empty() {
                    Ok(())
                } else {
                    Err(CascadeError::Other(format!(
                        "FleetDispatcher: successful CLI output omitted completion markers for ticket {ticket_id} steps: {}",
                        missing.join(", ")
                    )))
                }
            }
            FleetOutcome::Unavailable { reason } => {
                self.fail_running_steps(
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    ticket_id,
                    active_steps.iter().map(|step| step.id.as_str()),
                )?;
                Err(CascadeError::Other(format!(
                    "FleetDispatcher: no CLI available for ticket {ticket_id}: {reason}"
                )))
            }
            FleetOutcome::Failed { message } => {
                self.fail_running_steps(
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    ticket_id,
                    active_steps.iter().map(|step| step.id.as_str()),
                )?;
                Err(CascadeError::Other(format!(
                    "FleetDispatcher: CLI failed for ticket {ticket_id} after {attempt} attempts: {message}"
                )))
            }
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn fail_running_steps<'a>(
        &self,
        phase_id: &str,
        epic_id: &str,
        wave_id: &str,
        sprint_id: &str,
        ticket_id: &str,
        step_ids: impl Iterator<Item = &'a str>,
    ) -> Result<()> {
        for step_id in step_ids {
            let ticket = self
                .store
                .load_ticket(phase_id, epic_id, wave_id, sprint_id, ticket_id)?;
            if ticket
                .steps
                .iter()
                .any(|step| step.id == step_id && step.status == crate::pbd::schema::StepStatus::Running)
            {
                self.store.transition_step(
                    phase_id,
                    epic_id,
                    wave_id,
                    sprint_id,
                    ticket_id,
                    step_id,
                    crate::pbd::schema::StepStatus::Failed,
                )?;
            }
        }
        Ok(())
    }
}

fn fleet_prompt(
    phases_root: &std::path::Path,
    phase_id: &str,
    epic_id: &str,
    wave_id: &str,
    sprint_id: &str,
    ticket: &crate::pbd::schema::Ticket,
    step_ids: &[&str],
) -> String {
    let step_list = ticket
        .steps
        .iter()
        .filter(|step| step_ids.contains(&step.id.as_str()))
        .map(|step| format!("- {}: {}", step.id, step.title))
        .collect::<Vec<_>>()
        .join("\n");
    let ticket_note = ticket.note.as_deref().unwrap_or("No additional ticket instructions.");

    format!(
        "Execute the self-contained ticket {ticket_id} ({title}) in phase {phase_id}/{epic_id}/{wave_id}/{sprint_id}.\n\
         The PBD store is {phases_root}.\n\
         Ticket instructions: {ticket_note}\n\
         Complete only these steps:\n{step_list}\n\
         After genuinely completing each step, print an exact line `{marker}<step-id>`.\n\
         Do not print a marker for an incomplete or failed step. These markers and the process exit code are authoritative.",
        ticket_id = ticket.id,
        title = ticket.title,
        phases_root = phases_root.display(),
        marker = STEP_COMPLETION_MARKER,
    )
}

fn completion_markers(stdout: &str) -> HashSet<String> {
    stdout
        .match_indices(STEP_COMPLETION_MARKER)
        .filter_map(|(offset, _)| {
            let marker_start = offset + STEP_COMPLETION_MARKER.len();
            let step_id: String = stdout[marker_start..]
                .chars()
                .take_while(|ch| ch.is_ascii_alphanumeric() || matches!(ch, '-' | '_' | '.'))
                .collect();
            (!step_id.is_empty()).then_some(step_id)
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        path::{Path, PathBuf},
    };

    use serial_test::serial;
    use tempfile::TempDir;

    use crate::{
        build::test_support::{mk_store, seed_phase},
        pbd::{
            protocol::ExternalChecks,
            schema::{Step, StepStatus},
        },
    };

    use super::*;

    struct PathGuard(Option<std::ffi::OsString>);

    impl Drop for PathGuard {
        fn drop(&mut self) {
            match self.0.take() {
                Some(path) => std::env::set_var("PATH", path),
                None => std::env::remove_var("PATH"),
            }
        }
    }

    struct RejectChecks;

    impl ExternalChecks for RejectChecks {
        fn run_checks(&self, _context_id: &str) -> Result<Vec<String>> {
            Ok(vec!["CR/QA rejected output".into()])
        }
    }

    fn install_fake_fleet_cli(directory: &Path, body: &str) -> PathGuard {
        for binary in ["claude", "opencode"] {
            let path = directory.join(binary);
            fs::write(&path, format!("#!/bin/sh\n{body}\n")).expect("write fake fleet CLI");
            let mut permissions = fs::metadata(&path).expect("fake CLI metadata").permissions();
            permissions.set_mode(0o755);
            fs::set_permissions(path, permissions).expect("make fake CLI executable");
        }
        let original = std::env::var_os("PATH");
        std::env::set_var("PATH", directory);
        PathGuard(original)
    }

    fn event_transitions(phases_root: &Path) -> Vec<(String, String)> {
        fs::read_to_string(phases_root.join("events.jsonl"))
            .expect("read event log")
            .lines()
            .map(|line| serde_json::from_str::<serde_json::Value>(line).expect("valid event JSON"))
            .filter(|event| event["level"] == "step")
            .map(|event| {
                (
                    event["from"].as_str().expect("from string").to_string(),
                    event["to"].as_str().expect("to string").to_string(),
                )
            })
            .collect()
    }

    fn first_step_status(phases_root: PathBuf) -> StepStatus {
        PbdStore::new(phases_root)
            .load_ticket("p1", "e01", "w01", "s01", "t01")
            .expect("load first ticket")
            .steps[0]
            .status
            .clone()
    }

    #[test]
    fn completion_marker_parser_accepts_exact_step_ids_in_plain_or_json_output() {
        let markers = completion_markers(
            "done CASCADE_STEP_COMPLETE:step-01\\n{\"result\":\"CASCADE_STEP_COMPLETE:qa_a\"}",
        );
        assert_eq!(markers.len(), 2);
        assert!(markers.contains("step-01"));
        assert!(markers.contains("qa_a"));
    }

    #[test]
    fn completion_marker_parser_ignores_empty_markers() {
        assert!(completion_markers("CASCADE_STEP_COMPLETE: !!!").is_empty());
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn real_dispatch_integration_transitions_pending_running_passed_from_marker() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(
            cli_dir.path(),
            "printf '%s\\n' 'CASCADE_STEP_COMPLETE:step-01'",
        );
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);
        let phases_root = store.root().to_path_buf();

        let result = BuildEngine::new(
            store,
            FleetDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        )
        .run_phase("p1")
        .await
        .expect("real phase run");

        assert!(result.success, "phase gate should pass: {:?}", result.errors);
        assert_eq!(first_step_status(phases_root.clone()), StepStatus::Passed);
        let transitions = event_transitions(&phases_root);
        assert!(transitions.contains(&("pending".into(), "running".into())));
        assert!(transitions.contains(&("running".into(), "passed".into())));
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn real_dispatch_marks_step_failed_when_success_output_has_no_marker() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(cli_dir.path(), "printf '%s\\n' 'work complete'");
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);
        let phases_root = store.root().to_path_buf();

        let error = BuildEngine::new(
            store,
            FleetDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        )
        .run_phase("p1")
        .await
        .expect_err("missing marker must fail the phase run");

        assert!(error.to_string().contains("omitted completion markers"));
        assert_eq!(first_step_status(phases_root), StepStatus::Failed);
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn real_dispatch_marks_step_failed_on_nonzero_exit() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(cli_dir.path(), "printf '%s\\n' 'failure' >&2; exit 17");
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);
        let phases_root = store.root().to_path_buf();

        let error = BuildEngine::new(
            store,
            FleetDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        )
        .run_phase("p1")
        .await
        .expect_err("nonzero exit must fail the phase run");

        assert!(error.to_string().contains("exit 17"));
        assert_eq!(first_step_status(phases_root), StepStatus::Failed);
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn real_dispatch_gate_failure_blocks_step_progression() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(
            cli_dir.path(),
            "printf '%s\\n' 'CASCADE_STEP_COMPLETE:step-01'",
        );
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);
        let phases_root = store.root().to_path_buf();

        let error = BuildEngine::new(
            store,
            FleetDispatcher,
            RejectChecks,
            BuildConfig::default(),
        )
        .run_phase("p1")
        .await
        .expect_err("rejected gate must fail the phase run");

        assert!(error.to_string().contains("external step gate blocked"));
        assert_eq!(first_step_status(phases_root.clone()), StepStatus::Failed);
        assert!(!event_transitions(&phases_root).contains(&("running".into(), "passed".into())));
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn transient_cli_failure_retries_and_passes_on_third_attempt() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(
            cli_dir.path(),
            r#"counter_file="$0.count"
attempt=0
if [ -f "$counter_file" ]; then IFS= read -r attempt < "$counter_file"; fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > "$counter_file"
if [ "$attempt" -lt 3 ]; then printf '%s\n' 'temporary failure' >&2; exit 75; fi
printf '%s\n' 'CASCADE_STEP_COMPLETE:step-01'"#,
        );
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);
        let phases_root = store.root().to_path_buf();
        let engine = BuildEngine::new(
            store,
            FleetDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        );

        engine
            .dispatch_ticket("p1", "e01", "w01", "s01", "t01")
            .await
            .expect("third transient attempt should pass");

        assert_eq!(
            fs::read_to_string(cli_dir.path().join("claude.count"))
                .expect("read attempt count")
                .trim(),
            "3"
        );
        assert_eq!(first_step_status(phases_root), StepStatus::Passed);
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn exhausted_cli_retries_mark_step_failed_after_bounded_count() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(
            cli_dir.path(),
            r#"counter_file="$0.count"
attempt=0
if [ -f "$counter_file" ]; then IFS= read -r attempt < "$counter_file"; fi
attempt=$((attempt + 1))
printf '%s\n' "$attempt" > "$counter_file"
printf '%s\n' 'temporary failure' >&2
exit 75"#,
        );
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);
        let phases_root = store.root().to_path_buf();
        let engine = BuildEngine::new(
            store,
            FleetDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        );

        let error = engine
            .dispatch_ticket("p1", "e01", "w01", "s01", "t01")
            .await
            .expect_err("bounded retry exhaustion must fail");

        assert!(error.to_string().contains("after 3 attempts"));
        assert_eq!(
            fs::read_to_string(cli_dir.path().join("claude.count"))
                .expect("read attempt count")
                .trim(),
            "3"
        );
        assert_eq!(first_step_status(phases_root), StepStatus::Failed);
    }

    #[tokio::test]
    #[serial(env_path)]
    async fn interrupted_real_run_resumes_without_redispatching_passed_steps() {
        let cli_dir = TempDir::new().expect("fake CLI directory");
        let _path = install_fake_fleet_cli(
            cli_dir.path(),
            r#"case "$*" in
  *"ticket t01"*)
    case "$*" in
      *"- step-01:"*) printf '%s\n' 'completed step was redispatched' >&2; exit 91 ;;
    esac
    printf '%s\n' 'CASCADE_STEP_COMPLETE:step-02'
    ;;
  *) printf '%s\n' 'CASCADE_STEP_COMPLETE:step-01' ;;
esac"#,
        );
        let tmp = TempDir::new().expect("phase directory");
        let store = mk_store(&tmp);
        seed_phase(&store);

        let mut ticket = store
            .load_ticket("p1", "e01", "w01", "s01", "t01")
            .expect("load resumable ticket");
        ticket.steps.push(Step {
            id: "step-02".into(),
            title: "Verify".into(),
            status: StepStatus::Pending,
            note: None,
        });
        store
            .save_ticket("p1", "e01", "w01", &ticket)
            .expect("save second step");
        store
            .transition_step(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step-01",
                StepStatus::Running,
            )
            .expect("start completed step before interrupt");
        store
            .transition_step(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step-01",
                StepStatus::Passed,
            )
            .expect("persist completed step before interrupt");
        store
            .transition_step(
                "p1",
                "e01",
                "w01",
                "s01",
                "t01",
                "step-02",
                StepStatus::Running,
            )
            .expect("persist interrupted running step");
        let phases_root = store.root().to_path_buf();

        let result = BuildEngine::new(
            store,
            FleetDispatcher,
            NoExternalChecks,
            BuildConfig::default(),
        )
        .run_phase("p1")
        .await
        .expect("resumed phase run");

        assert!(result.success, "resumed phase should close: {:?}", result.errors);
        let resumed = PbdStore::new(phases_root)
            .load_ticket("p1", "e01", "w01", "s01", "t01")
            .expect("load resumed ticket");
        assert!(
            resumed
                .steps
                .iter()
                .all(|step| step.status == StepStatus::Passed),
            "all resumed steps should pass: {:?}",
            resumed.steps
        );
    }
}
