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
    build::{BuildConfig, BuildEngine, MockDispatcher},
    pbd::{store::resolve_phases_root, NoExternalChecks, PbdStore},
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
    /// TODO(pews-02): wire real fleet dispatcher once agent-process harness lands.
    /// Currently uses MockDispatcher which marks tickets done without agent calls.
    Run(BuildRunArgs),
}

#[derive(Debug, Args)]
pub struct BuildRunArgs {
    /// Phase ID to run (e.g. `p2`).
    pub phase: String,
}

#[async_trait]
impl Command for BuildArgs {
    async fn run(&self) -> Result<()> {
        let phases_root = resolve_phases_root(self.phases.as_deref());
        let store = PbdStore::new(&phases_root);
        store.init()?;

        match &self.subcommand {
            BuildSubcmd::Run(args) => {
                let config = BuildConfig {
                    skip_externals: self.skip_externals,
                };

                // TODO(pews-02): replace MockDispatcher with a real fleet-backed
                // dispatcher once the agent-process harness lands (pews-03).
                let checks = NoExternalChecks;
                let engine = BuildEngine::new(store, MockDispatcher, checks, config);

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
