//! `cascade models` — list, download, and remove local LLM models.
//!
//! # Purpose
//!
//! Provides a self-bootstrapping AI path: a fresh machine can acquire local
//! model weights with zero API keys by running `cascade models download <id>`.
//!
//! # Subcommands
//!
//! | Subcommand | Description |
//! |------------|-------------|
//! | `list` | Print the model catalog with download status and disk footprint |
//! | `download <id>` | Download weights to `~/.cascade/models/<id>/` with progress |
//! | `remove <id>` | Delete `~/.cascade/models/<id>/` and its contents |
//!
//! # Constraints
//!
//! - `download` is idempotent: if the model directory already exists and the
//!   weight file is present and valid, it prints a "already present" message
//!   and exits 0 without re-downloading.
//! - `download` performs a disk-space pre-flight: refuses when free space is
//!   < 2 × the model's known size.
//! - `download` streams to a `.tmp` file and verifies SHA-256 before the
//!   atomic rename; a corrupted `.tmp` is deleted on mismatch.
//! - `remove` asks for confirmation unless `--yes` is passed.
//! - All paths are `$HOME`-confined (`~/.cascade/models/`).
//! - Tests MUST NOT download real weights (gate with `#[ignore]`).
//!
//! # SPORT
//!
//! Entity: cascade-cli / models subcommand (E-P5-07)

#[cfg(feature = "local-llm")]
use std::path::PathBuf;
#[cfg(feature = "local-llm")]
use std::sync::Arc;

use async_trait::async_trait;
#[cfg(feature = "local-llm")]
use cascade_local_llm::downloader::{find_model, models};
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};

use super::Command;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade models`.
#[derive(Debug, Args)]
pub struct ModelsArgs {
    #[command(subcommand)]
    pub command: ModelsCommands,
}

/// Subcommands under `cascade models`.
#[derive(Debug, Subcommand)]
pub enum ModelsCommands {
    /// Print the model catalog with download status and disk footprint.
    List(ListArgs),
    /// Download model weights to `~/.cascade/models/<id>/`.
    Download(DownloadArgs),
    /// Remove a downloaded model from disk.
    Remove(RemoveArgs),
}

/// Arguments for `cascade models list`.
#[derive(Debug, Args)]
pub struct ListArgs {
    /// Output as JSON instead of a human-readable table.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade models download`.
#[derive(Debug, Args)]
pub struct DownloadArgs {
    /// Model identifier (e.g. `gemma-2-2b`, `llama-3.2-3b`, `phi-3-mini`).
    pub id: String,
}

/// Arguments for `cascade models remove`.
#[derive(Debug, Args)]
pub struct RemoveArgs {
    /// Model identifier.
    pub id: String,
    /// Skip confirmation prompt.
    #[arg(long, short = 'y')]
    pub yes: bool,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ModelsArgs {
    async fn run(&self) -> Result<()> {
        match &self.command {
            ModelsCommands::List(args) => args.run().await,
            ModelsCommands::Download(args) => args.run().await,
            ModelsCommands::Remove(args) => args.run().await,
        }
    }
}

// ── list ──────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ListArgs {
    async fn run(&self) -> Result<()> {
        #[cfg(not(feature = "local-llm"))]
        return Err(local_llm_not_built());

        #[cfg(feature = "local-llm")]
        {
            let catalog = models();

            if self.json {
                // JSON output: array of objects with id, size_bytes, downloaded
                let mut rows = Vec::new();
                for entry in catalog {
                    let dir = model_dir_path_home(entry.model_id);
                    let downloaded = dir.map(|d| d.is_dir()).unwrap_or(false);
                    rows.push(serde_json::json!({
                        "id": entry.model_id,
                        "hf_repo": entry.hf_repo,
                        "size_bytes": entry.size_bytes,
                        "downloaded": downloaded,
                    }));
                }
                println!(
                    "{}",
                    serde_json::to_string_pretty(&rows)
                        .map_err(|e| CascadeError::Other(e.to_string()))?
                );
                return Ok(());
            }

            // Human-readable table
            println!("{:<18} {:<10} {:<40} STATUS", "MODEL ID", "SIZE", "HF REPO");
            println!("{}", "-".repeat(80));
            for entry in catalog {
                let size_gb = entry.size_bytes as f64 / 1e9;
                let dir = model_dir_path_home(entry.model_id);
                let status = match dir {
                    Some(d) if d.is_dir() => "downloaded",
                    _ => "not downloaded",
                };
                println!(
                    "{:<18} {:>6.1}GB  {:<40} {}",
                    entry.model_id, size_gb, entry.hf_repo, status
                );
            }

            Ok(())
        }
    }
}

// ── download ──────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for DownloadArgs {
    async fn run(&self) -> Result<()> {
        #[cfg(not(feature = "local-llm"))]
        return Err(local_llm_not_built());

        #[cfg(feature = "local-llm")]
        {
            let id = self.id.as_str();

            // Validate model id against catalog.
            let entry = find_model(id).ok_or_else(|| {
                CascadeError::Other(format!(
                    "unknown model id '{}'. Run 'cascade models list' to see available models.",
                    id
                ))
            })?;

            // Check if already present (idempotent).
            let model_dir = model_dir_path_home(id).ok_or_else(|| {
                CascadeError::Other("cannot determine HOME directory".to_string())
            })?;
            let weight_file = model_dir.join(entry.filename);

            if weight_file.exists() {
                println!("✓ {} already downloaded at {}", id, weight_file.display());
                return Ok(());
            }

            println!(
                "Downloading {} ({:.1}GB) from {}",
                id,
                entry.size_bytes as f64 / 1e9,
                entry.hf_url()
            );
            println!("Destination: {}", model_dir.display());
            println!();

            // Progress tracking with a simple inline bar.
            let last_pct = Arc::new(std::sync::Mutex::new(-1i32));
            let last_pct_clone = last_pct.clone();

            let result = cascade_local_llm::download_model(id, move |progress| {
                let pct = progress.pct as i32;
                let mut last = last_pct_clone.lock().unwrap();
                // Only reprint when percentage changes to avoid spamming.
                if pct > *last {
                    *last = pct;
                    let filled = (pct as usize * 40) / 100;
                    let bar = format!(
                        "[{}{}] {}%  ({:.1}/{:.1}GB)",
                        "#".repeat(filled),
                        "-".repeat(40 - filled),
                        pct,
                        progress.bytes_received as f64 / 1e9,
                        progress.total_bytes as f64 / 1e9,
                    );
                    eprint!("\r{}", bar);
                }
            })
            .await;

            eprintln!(); // newline after progress bar

            match result {
                Ok(path) => {
                    println!("✓ Download complete: {}", path.display());
                    println!(
                        "  Register with daemon: restart 'cascade daemon' to pick up the new model."
                    );
                    Ok(())
                }
                Err(e) => Err(CascadeError::Other(e.to_string())),
            }
        }
    }
}

// ── remove ────────────────────────────────────────────────────────────────────

#[async_trait]
impl Command for RemoveArgs {
    async fn run(&self) -> Result<()> {
        #[cfg(not(feature = "local-llm"))]
        return Err(local_llm_not_built());

        #[cfg(feature = "local-llm")]
        {
            let id = self.id.as_str();

            // Validate model id.
            if find_model(id).is_none() {
                return Err(CascadeError::Other(format!(
                    "unknown model id '{}'. Run 'cascade models list' to see available models.",
                    id
                )));
            }

            let model_dir = model_dir_path_home(id).ok_or_else(|| {
                CascadeError::Other("cannot determine HOME directory".to_string())
            })?;

            if !model_dir.is_dir() {
                println!("Model '{}' is not downloaded (nothing to remove).", id);
                return Ok(());
            }

            if !self.yes {
                // Ask for confirmation.
                eprint!("Remove {} at {}? [y/N] ", id, model_dir.display());
                let mut input = String::new();
                std::io::stdin()
                    .read_line(&mut input)
                    .map_err(|e| CascadeError::Other(e.to_string()))?;
                if !input.trim().eq_ignore_ascii_case("y") {
                    println!("Aborted.");
                    return Ok(());
                }
            }

            tokio::fs::remove_dir_all(&model_dir)
                .await
                .map_err(|e| CascadeError::Other(e.to_string()))?;

            println!("✓ Removed {} from disk.", id);
            Ok(())
        }
    }
}

// ── internal helpers ──────────────────────────────────────────────────────────

/// Return a `CascadeError` explaining that local LLM support was not compiled in.
///
/// Only reachable in builds WITHOUT the `local-llm` feature; with the feature
/// on, every caller takes the real path instead.
#[cfg(not(feature = "local-llm"))]
fn local_llm_not_built() -> cascade_types::error::CascadeError {
    CascadeError::Other(
        "local LLM support is not built in — reinstall or build with `--features local-llm`"
            .to_string(),
    )
}

/// Return `~/.cascade/models/<model_id>` using `$HOME` env var first
/// (test-isolation-safe), falling back to `dirs::home_dir()`.
#[cfg(feature = "local-llm")]
fn model_dir_path_home(model_id: &str) -> Option<PathBuf> {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(dirs::home_dir)?;
    Some(home.join(".cascade").join("models").join(model_id))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(all(test, feature = "local-llm"))]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    // ── helpers ───────────────────────────────────────────────────────────────

    /// Override $HOME to a temp dir and return the dir guard.
    /// Call `std::env::set_var("HOME", dir.path())` — must use #[serial(global_env)].
    fn with_fake_home() -> TempDir {
        let dir = TempDir::new().expect("tempdir");
        std::env::set_var("HOME", dir.path());
        dir
    }

    // ── catalog list ──────────────────────────────────────────────────────────

    #[test]
    fn models_catalog_has_three_entries() {
        let catalog = models();
        assert_eq!(catalog.len(), 3, "catalog must have 3 entries");
    }

    #[test]
    fn models_catalog_ids_match_expected() {
        let ids: Vec<&str> = models().iter().map(|e| e.model_id).collect();
        assert!(
            ids.contains(&"gemma-2-2b"),
            "catalog must contain gemma-2-2b"
        );
        assert!(
            ids.contains(&"llama-3.2-3b"),
            "catalog must contain llama-3.2-3b"
        );
        assert!(
            ids.contains(&"phi-3-mini"),
            "catalog must contain phi-3-mini"
        );
    }

    // ── list command (text output) ────────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    #[allow(clippy::await_holding_lock)] // Deliberate: env-mutation test lock spans await to keep process-env writes serialized.
    async fn list_runs_without_panic() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let _home = with_fake_home();
        // ListArgs::run() prints to stdout; we just ensure it returns Ok.
        let args = ListArgs { json: false };
        args.run().await.expect("list should not error");
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn list_json_runs_without_panic() {
        let _home = with_fake_home();
        let args = ListArgs { json: true };
        args.run().await.expect("list --json should not error");
    }

    // ── download — unknown model ──────────────────────────────────────────────

    #[tokio::test]
    async fn download_unknown_model_returns_err() {
        let args = DownloadArgs {
            id: "totally-nonexistent-model-xyzzy".to_string(),
        };
        let result = args.run().await;
        assert!(result.is_err(), "unknown model must return Err");
        let err_str = format!("{:?}", result.unwrap_err());
        assert!(
            err_str.contains("unknown model id"),
            "error must mention 'unknown model id'; got: {err_str}"
        );
    }

    // ── download — already present (idempotent) ───────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn download_skips_when_model_already_present() {
        let home = with_fake_home();
        // Pre-create the expected model directory and weight file.
        let model_dir = home
            .path()
            .join(".cascade")
            .join("models")
            .join("gemma-2-2b");
        tokio::fs::create_dir_all(&model_dir).await.unwrap();
        tokio::fs::write(model_dir.join("model.safetensors"), b"fake-weights")
            .await
            .unwrap();

        let args = DownloadArgs {
            id: "gemma-2-2b".to_string(),
        };
        // Should return Ok immediately without hitting network.
        let result = args.run().await;
        assert!(
            result.is_ok(),
            "should skip download when file present: {result:?}"
        );
    }

    // ── remove — not downloaded ───────────────────────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn remove_not_downloaded_exits_cleanly() {
        let _home = with_fake_home();
        let args = RemoveArgs {
            id: "gemma-2-2b".to_string(),
            yes: true,
        };
        // Dir doesn't exist → should print and return Ok (not Err).
        let result = args.run().await;
        assert!(
            result.is_ok(),
            "remove when not present must not error: {result:?}"
        );
    }

    // ── remove — unknown model ────────────────────────────────────────────────

    #[tokio::test]
    async fn remove_unknown_model_returns_err() {
        let args = RemoveArgs {
            id: "bogus-model-zzz".to_string(),
            yes: true,
        };
        let result = args.run().await;
        assert!(result.is_err(), "remove unknown model must return Err");
    }

    // ── remove — with fake home, directory exists ─────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn remove_existing_model_dir_succeeds() {
        let home = with_fake_home();
        let model_dir = home
            .path()
            .join(".cascade")
            .join("models")
            .join("phi-3-mini");
        tokio::fs::create_dir_all(&model_dir).await.unwrap();
        tokio::fs::write(model_dir.join("model.safetensors"), b"fake")
            .await
            .unwrap();

        let args = RemoveArgs {
            id: "phi-3-mini".to_string(),
            yes: true,
        };
        let result = args.run().await;
        assert!(result.is_ok(), "remove should succeed: {result:?}");
        assert!(!model_dir.exists(), "model dir must be deleted");
    }

    // ── scan_installed_models from local-llm crate ────────────────────────────

    #[test]
    #[serial(global_env)]
    fn scan_installed_models_finds_dirs_under_home() {
        use cascade_local_llm::scan_installed_models;

        let home = with_fake_home();
        // Create two fake model dirs.
        let models_root = home.path().join(".cascade").join("models");
        std::fs::create_dir_all(models_root.join("gemma-2-2b")).unwrap();
        std::fs::create_dir_all(models_root.join("phi-3-mini")).unwrap();

        let found = scan_installed_models();
        let ids: Vec<&str> = found.iter().map(|(id, _)| id.as_str()).collect();
        assert!(
            ids.contains(&"local:gemma-2-2b"),
            "must find gemma-2-2b; got: {ids:?}"
        );
        assert!(
            ids.contains(&"local:phi-3-mini"),
            "must find phi-3-mini; got: {ids:?}"
        );
    }

    #[test]
    #[serial(global_env)]
    fn scan_installed_models_returns_empty_when_no_dirs() {
        use cascade_local_llm::scan_installed_models;

        let _home = with_fake_home();
        // No model dirs created.
        let found = scan_installed_models();
        assert!(
            found.is_empty(),
            "must be empty when no models are installed"
        );
    }

    // ── disk-space guard (unit test of error enum) ────────────────────────────

    #[test]
    fn disk_space_guard_error_variant_is_recognizable() {
        use cascade_local_llm::LocalModelError;
        let err = LocalModelError::InsufficientDiskSpace {
            required: 10_000_000_000,
            available: 1_000_000_000,
        };
        let s = err.to_string();
        assert!(
            s.contains("disk") || s.contains("space") || s.contains("Insufficient"),
            "error string must mention disk/space; got: {s}"
        );
    }

    // ── real-weight download: gated behind #[ignore] ──────────────────────────

    #[tokio::test]
    #[ignore = "requires real network + HuggingFace access + ~5.2GB free disk"]
    async fn download_gemma_2_2b_real_weights() {
        let args = DownloadArgs {
            id: "gemma-2-2b".to_string(),
        };
        args.run()
            .await
            .expect("should download and verify gemma-2-2b");
    }
}
