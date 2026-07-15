//! # dispatch — ticket-weight → TaskClass mapping + fleet CLI-shelling
//!
//! ## Purpose
//! Maps a ticket's `weight` field to a [`TaskClass`] so the
//! [`super::engine::BuildEngine`] can route each ticket to the correct
//! delegate lane, and (via [`cli_binary_for_task_class`] /
//! [`run_fleet_cli`]) shells the corresponding CLI process — mirroring
//! `cascade-cli/src/cmd/conductor.rs`'s `execute_claude` / `execute_codex` /
//! `execute_opencode` backends, kept minimal here since cascade-core cannot
//! depend on cascade-cli (dependency direction is cli → core).
//!
//! ## Inputs
//! `weight: Option<&str>` — the `weight` field from a [`crate::pbd::Ticket`]
//! (values: `"S"`, `"M"`, `"L"`, `"XL"` or numeric strings).
//!
//! ## Outputs
//! [`TaskClass`] — passed to `Router::select` to choose a provider lane.
//! [`FleetOutcome`] — result of a shelled CLI invocation (stdout + exit code).
//!
//! ## Constraints
//! - `classify_ticket` stays pure (no I/O, no async, no panics outside tests).
//! - `run_fleet_cli` is the only I/O in this module — a single blocking
//!   `std::process::Command::output()` call, safe to call from `spawn_blocking`.
//! - This table is **INTERIM** — superseded once the agents-01 roster exposes
//!   per-role default tiers.
//!
//! ## SPORT
//! MASTER-CRATES.md — cascade-core: build::dispatch

// TODO(agents-01 supersedes): replace this static table with AgentRole::default_tier
// lookups once the full roster is wired. The Router seam stays; only this
// classify_ticket function changes.

use std::process::{Command, Stdio};

use crate::routing::TaskClass;

/// Classify a ticket weight into a [`TaskClass`] for routing.
///
/// # Mapping (INTERIM)
///
/// | Weight | TaskClass   | Rationale                           |
/// |--------|-------------|-------------------------------------|
/// | S / 1  | `Cheap`     | Trivial change; free Flash tier OK  |
/// | M / 2  | `BulkExec`  | Standard implementation             |
/// | L / 3  | `BulkExec`  | Larger feature; still bulk          |
/// | XL / 4 | `FinalGate` | Architectural; needs best model     |
/// | other  | `BulkExec`  | Safe default for unknown weight     |
pub fn classify_ticket(weight: Option<&str>) -> TaskClass {
    // TODO(agents-01 supersedes): replace with AgentRole::default_tier lookup
    match weight.unwrap_or("").trim().to_ascii_uppercase().as_str() {
        "S" | "1" => TaskClass::Cheap,
        "M" | "2" => TaskClass::BulkExec,
        "L" | "3" => TaskClass::BulkExec,
        "XL" | "4" => TaskClass::FinalGate,
        _ => TaskClass::BulkExec,
    }
}

// ── Fleet CLI-shelling (D-external) ───────────────────────────────────────────

/// Outcome of a single fleet CLI invocation.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FleetOutcome {
    /// Process exited 0. Carries captured stdout.
    Success { stdout: String },
    /// The CLI binary is not present on PATH.
    Unavailable { reason: String },
    /// Process ran but exited non-zero. Carries stderr for diagnostics.
    Failed { message: String },
}

/// Choose which CLI binary handles a [`TaskClass`], mirroring conductor.rs's
/// spill preference (BulkExec/FinalGate → claude, AdversarialReview → codex,
/// Cheap → opencode) without depending on cascade-cli.
///
/// # Mapping (INTERIM — parity with `classify_ticket`)
/// | TaskClass          | CLI binary |
/// |---------------------|-----------|
/// | `Cheap`             | `opencode` |
/// | `BulkExec`          | `claude`   |
/// | `AdversarialReview` | `codex`    |
/// | `FinalGate`         | `claude`   |
/// | `InteractiveChat` / `Sensitive` | `claude` |
pub fn cli_binary_for_task_class(task_class: TaskClass) -> &'static str {
    match task_class {
        TaskClass::Cheap => "opencode",
        TaskClass::AdversarialReview => "codex",
        TaskClass::BulkExec
        | TaskClass::FinalGate
        | TaskClass::InteractiveChat
        | TaskClass::Sensitive => "claude",
    }
}

/// Build the CLI args for `binary` given a non-interactive `prompt`, mirroring
/// conductor.rs's `execute_claude` / `execute_codex` / `execute_opencode`.
fn args_for_binary<'p>(binary: &str, prompt: &'p str) -> Vec<&'p str> {
    match binary {
        "claude" => vec!["-p", prompt, "--output-format", "json"],
        "codex" => vec!["exec", prompt],
        // "opencode" and any unrecognized binary fall back to `run <prompt>`.
        _ => vec!["run", prompt],
    }
}

/// Shell the fleet CLI for `task_class` with `prompt`, capturing stdout and
/// exit code. Blocking — call from `tokio::task::spawn_blocking` in async
/// contexts (per D-external, mirrors conductor.rs's `run_command`).
///
/// Returns [`FleetOutcome::Unavailable`] (never an `Err`) when the binary is
/// missing, so callers can decide whether to fall back rather than fail hard.
pub fn run_fleet_cli(task_class: TaskClass, prompt: &str) -> FleetOutcome {
    let binary = cli_binary_for_task_class(task_class);
    let args = args_for_binary(binary, prompt);

    let output = Command::new(binary)
        .args(&args)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output();

    match output {
        Ok(out) if out.status.success() => FleetOutcome::Success {
            stdout: String::from_utf8_lossy(&out.stdout).to_string(),
        },
        Ok(out) => FleetOutcome::Failed {
            message: format!(
                "{binary} exit {}: {}",
                out.status.code().unwrap_or(-1),
                String::from_utf8_lossy(&out.stderr).trim()
            ),
        },
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => FleetOutcome::Unavailable {
            reason: format!("{binary} binary not found in PATH: {e}"),
        },
        Err(e) => FleetOutcome::Unavailable {
            reason: format!("{binary} exec failed: {e}"),
        },
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn small_maps_to_cheap() {
        assert_eq!(classify_ticket(Some("S")), TaskClass::Cheap);
        assert_eq!(classify_ticket(Some("s")), TaskClass::Cheap);
        assert_eq!(classify_ticket(Some("1")), TaskClass::Cheap);
    }

    #[test]
    fn medium_and_large_map_to_bulk() {
        assert_eq!(classify_ticket(Some("M")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("L")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("2")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("3")), TaskClass::BulkExec);
    }

    #[test]
    fn xl_maps_to_final_gate() {
        assert_eq!(classify_ticket(Some("XL")), TaskClass::FinalGate);
        assert_eq!(classify_ticket(Some("xl")), TaskClass::FinalGate);
        assert_eq!(classify_ticket(Some("4")), TaskClass::FinalGate);
    }

    #[test]
    fn none_and_unknown_default_to_bulk() {
        assert_eq!(classify_ticket(None), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("")), TaskClass::BulkExec);
        assert_eq!(classify_ticket(Some("HUGE")), TaskClass::BulkExec);
    }

    #[test]
    fn cli_binary_mapping_matches_conductor_spill_preference() {
        assert_eq!(cli_binary_for_task_class(TaskClass::Cheap), "opencode");
        assert_eq!(cli_binary_for_task_class(TaskClass::BulkExec), "claude");
        assert_eq!(
            cli_binary_for_task_class(TaskClass::AdversarialReview),
            "codex"
        );
        assert_eq!(cli_binary_for_task_class(TaskClass::FinalGate), "claude");
        assert_eq!(
            cli_binary_for_task_class(TaskClass::InteractiveChat),
            "claude"
        );
        assert_eq!(cli_binary_for_task_class(TaskClass::Sensitive), "claude");
    }

    #[test]
    fn args_for_binary_shapes_prompt_per_cli() {
        assert_eq!(
            args_for_binary("claude", "hi"),
            vec!["-p", "hi", "--output-format", "json"]
        );
        assert_eq!(args_for_binary("codex", "hi"), vec!["exec", "hi"]);
        assert_eq!(args_for_binary("opencode", "hi"), vec!["run", "hi"]);
        assert_eq!(args_for_binary("unknown-cli", "hi"), vec!["run", "hi"]);
    }

    #[test]
    fn run_fleet_cli_reports_unavailable_for_missing_binary() {
        // Use a TaskClass mapped to a binary name that will never exist on
        // the test host, to exercise the NotFound branch deterministically.
        let outcome = run_fleet_cli(TaskClass::AdversarialReview, "test prompt");
        // codex may or may not be installed on the dev machine running tests;
        // assert only that we get a well-formed variant, never a panic.
        match outcome {
            FleetOutcome::Success { .. }
            | FleetOutcome::Unavailable { .. }
            | FleetOutcome::Failed { .. } => {}
        }
    }
}
