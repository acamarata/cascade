//! `cascade build` — autonomous Build engine CLI.
//!
//! # Purpose
//! Drives a phase's full EOx gate chain (topological ticket dispatch → EOSt →
//! EOT → EOS → EOW → EOE → EOP) via the [`BuildEngine`].
//!
//! # Subcommands
//! - `build run <phase>` — run a phase to completion (mock or real dispatcher)
//!
//! # SPORT
//! MASTER-CRATES.md — cascade-cli: cmd::build

use std::path::PathBuf;

use async_trait::async_trait;
use clap::{Args, Subcommand};

use cascade_core::{
    build::{BuildConfig, BuildEngine, FleetDispatcher, MockDispatcher},
    pbd::{
        store::resolve_phases_root, BuildCheck, NoExternalChecks, PbdStore, RealExternalChecks,
    },
};
use cascade_types::error::Result;

use super::Command;

// ── Top-level ─────────────────────────────────────────────────────────────────

/// `cascade build` — autonomous Build engine.
#[derive(Debug, Args)]
pub struct BuildArgs {
    /// Explicit path to the phases/ root directory.
    #[arg(long, global = true, value_name = "DIR")]
    pub phases: Option<PathBuf>,

    /// Skip external checks (build commands, health probes). Useful for dry runs.
    #[arg(long, global = true)]
    pub skip_externals: bool,

    #[command(subcommand)]
    pub subcommand: BuildSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum BuildSubcmd {
    /// Run a phase to completion via the autonomous Build engine.
    ///
    /// Dispatches tickets in topological order, runs EOSt per step, EOT per
    /// ticket, EOS per sprint, EOW per wave, EOE per epic, and finally EOP.
    ///
    /// Pass `--mock` for a dry run (MockDispatcher + NoExternalChecks) or
    /// `--real` to dispatch actual fleet agents gated by RealExternalChecks
    /// (unless `--skip-externals` is also passed).
    Run(BuildRunArgs),
}

#[derive(Debug, Args)]
pub struct BuildRunArgs {
    /// Phase ID to run (e.g. `p2`).
    pub phase: String,

    /// Use the mock dispatcher (marks tickets done without agent calls, for tests/dry-runs).
    #[arg(long)]
    pub mock: bool,

    /// Use the real fleet dispatcher (dispatches agents to run actual tickets).
    #[arg(long)]
    pub real: bool,
}

#[async_trait]
impl Command for BuildArgs {
    async fn run(&self) -> Result<()> {
        let phases_root = resolve_phases_root(self.phases.as_deref());
        let store = PbdStore::new(&phases_root);
        store.init()?;

        match &self.subcommand {
            BuildSubcmd::Run(args) => {
                // Ensure exactly one dispatcher is selected
                match (args.mock, args.real) {
                    (false, false) => {
                        return Err(cascade_types::error::CascadeError::Other(
                            "must specify a dispatcher: use --mock (marks tickets done without agents, for tests/dry-runs) \
                             or --real (dispatches agents to run actual tickets)"
                                .into(),
                        ));
                    }
                    (true, true) => {
                        return Err(cascade_types::error::CascadeError::Other(
                            "cannot use both --mock and --real; choose one dispatcher"
                                .into(),
                        ));
                    }
                    _ => {}
                }

                let config = BuildConfig {
                    skip_externals: self.skip_externals,
                };

                // External checks: real build/test verification gates `--real` runs
                // unless explicitly skipped; `--mock` and `--skip-externals` always
                // get the no-op provider so tests/dry-runs stay side-effect-free.
                let use_real_checks = args.real && !self.skip_externals;

                let engine = if args.real {
                    if use_real_checks {
                        let checks = RealExternalChecks::new().with_build(BuildCheck::new(
                            "workspace-build",
                            "cargo build --workspace",
                        ));
                        BuildEngine::new(store, FleetDispatcher, checks, config)
                    } else {
                        BuildEngine::new(store, FleetDispatcher, NoExternalChecks, config)
                    }
                } else {
                    BuildEngine::new(store, MockDispatcher, NoExternalChecks, config)
                };

                eprintln!("cascade build: running phase {} …", args.phase);
                let result = engine.run_phase(&args.phase).await?;

                if result.success {
                    println!("phase {} complete", args.phase);
                } else {
                    eprintln!("phase {} FAILED:", args.phase);
                    for err in &result.errors {
                        eprintln!("  - {err}");
                    }
                    return Err(cascade_types::error::CascadeError::Other(format!(
                        "build failed for phase {}",
                        args.phase
                    )));
                }
                Ok(())
            }
        }
    }
}
