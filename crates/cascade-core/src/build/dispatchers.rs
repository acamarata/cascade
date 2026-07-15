//! Ticket execution seam and built-in dispatchers for the build engine.

use async_trait::async_trait;

use cascade_types::error::{CascadeError, Result};

use crate::pbd::{schema::StepStatus, store::PbdStore};

use super::dispatch::{classify_ticket, run_fleet_cli, FleetOutcome};

/// Pluggable seam for executing a single ticket's work.
///
/// The engine invokes the dispatcher after a ticket's dependencies have
/// completed. Implementations must progress all steps to `passed` or `skipped`
/// before returning successfully.
#[async_trait]
pub trait TicketDispatcher: Send + Sync {
    /// Execute the work for one ticket and return `Ok(())` on success.
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

/// Production [`TicketDispatcher`] that classifies each ticket's weight and
/// shells the corresponding fleet CLI process (`claude`, `codex`, or
/// `opencode`). The blocking call runs inside `tokio::task::spawn_blocking`.
pub struct FleetDispatcher;

#[async_trait]
impl TicketDispatcher for FleetDispatcher {
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
        let task_class = classify_ticket(ticket.weight.as_deref());

        let prompt = format!(
            "Execute ticket {ticket_id} ({title}) in phase {phase_id}/{epic_id}/{wave_id}/{sprint_id}.",
            title = ticket.title
        );

        // Blocking CLI shell-out — never run directly on the async executor.
        let outcome = tokio::task::spawn_blocking(move || run_fleet_cli(task_class, &prompt))
            .await
            .map_err(|e| CascadeError::Other(format!("fleet dispatch join error: {e}")))?;

        match outcome {
            FleetOutcome::Success { .. } => {
                for step in &ticket.steps {
                    match step.status {
                        StepStatus::Pending => {
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
            FleetOutcome::Unavailable { reason } => Err(CascadeError::Other(format!(
                "FleetDispatcher: no CLI available for ticket {ticket_id}: {reason}"
            ))),
            FleetOutcome::Failed { message } => Err(CascadeError::Other(format!(
                "FleetDispatcher: CLI failed for ticket {ticket_id}: {message}"
            ))),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use super::super::{
        dispatch::cli_binary_for_task_class,
        test_support::{mk_store, seed_phase},
    };
    use serial_test::serial;
    use tempfile::TempDir;

    #[tokio::test]
    #[serial(env_path)]
    async fn fleet_dispatcher_errs_when_cli_binary_unavailable() {
        // Force PATH to a dir with none of claude/codex/opencode so dispatch
        // deterministically hits FleetOutcome::Unavailable, regardless of
        // what's installed on the machine running this test suite.
        // #[serial(env_path)]: this mutates the process-global PATH env var,
        // which nsentry_config.rs also reads at runtime — without this guard,
        // concurrent test execution (multiple threads in this binary) carries
        // a narrow flakiness risk (CR finding, T-P7-E03-04).
        let empty_path_dir = TempDir::new().expect("tmpdir for empty PATH");
        let original_path = std::env::var("PATH").unwrap_or_default();
        // SAFETY: single-threaded-per-test env mutation is standard in Rust
        // test suites; restored immediately after the assertion below.
        std::env::set_var("PATH", empty_path_dir.path());

        let tmp = TempDir::new().expect("tmpdir");
        let store = mk_store(&tmp);
        seed_phase(&store);

        let dispatcher = FleetDispatcher;
        let result = dispatcher
            .dispatch(&store, "p1", "e01", "w01", "s01", "t01")
            .await;

        std::env::set_var("PATH", original_path);

        assert!(
            result.is_err(),
            "dispatch should fail when no fleet CLI binary is on PATH"
        );
    }

    #[test]
    fn fleet_dispatcher_classifies_ticket_weight_to_expected_cli() {
        // Parity check: FleetDispatcher's classify_ticket → cli_binary_for_task_class
        // chain must route M-weight tickets to claude (BulkExec) and S-weight to
        // opencode (Cheap), matching the ticket fixtures in seed_phase().
        assert_eq!(
            cli_binary_for_task_class(classify_ticket(Some("M"))),
            "claude"
        );
        assert_eq!(
            cli_binary_for_task_class(classify_ticket(Some("S"))),
            "opencode"
        );
    }
}
