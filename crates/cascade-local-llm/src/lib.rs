//! # cascade-local-llm
//!
//! Local LLM inference crate for Cascade — candle-rs multi-model runner.
//!
//! ## Purpose
//!
//! Provides `LocalLlmAdapter`, a `cascade_providers::ProviderAdapter`
//! implementation backed by candle-rs local models running fully on-device
//! (CPU by default; Metal-accelerated on macOS ARM via the `metal` feature
//! flag).  Supported models: gemma-2-2b, llama-3.2-3b, phi-3-mini.
//!
//! ## Quick start
//!
//! ```rust,ignore
//! use cascade_local_llm::{LocalLlmAdapter, LocalLlmConfig, ModelFamily, build_runner};
//!
//! // 1. Configure — model weights must be downloaded first
//! let cfg = LocalLlmConfig::default_gemma_2_2b();
//!
//! // 2. Construct adapter (validates directory, does NOT load weights yet)
//! let adapter = LocalLlmAdapter::new(cfg)?;
//!
//! // 3. Register with the provider registry
//! registry.register("local:gemma-2-2b", Box::new(adapter));
//!
//! // Or use the factory to get any runner by family:
//! let runner = build_runner(ModelFamily::Llama3, cfg)?;
//! ```
//!
//! ## Feature flags
//!
//! | Flag | Default | Effect |
//! |------|---------|--------|
//! | `metal` | off | Enable Apple Silicon GPU acceleration via candle-core/metal |
//!
//! ## Module map
//!
//! | Module | Purpose |
//! |--------|---------|
//! | [`config`] | `LocalLlmConfig` — model path + generation hyper-params |
//! | [`error`] | `LocalModelError` — all local-inference failure modes |
//! | [`prompt`] | Gemma-2 chat-template formatter |
//! | [`runner`] | `LocalLlmRunner` — async candle-rs Gemma2 inference engine |
//! | [`runner_factory`] | `ModelFamily` enum + `build_runner` factory |
//! | [`runners`] | `Llama3Runner` + `Phi3Runner` implementations |
//! | [`adapter`] | `LocalLlmAdapter` — `ProviderAdapter` bridge |
//! | [`downloader`] | Model weight download helpers |
//!
//! ## Model weights
//!
//! Model weights are NOT bundled with the crate.  Download with
//! `cascade models download <model-id>` (T-P3-E04-18).
//!
//! | Model | Directory | RAM floor |
//! |-------|-----------|-----------|
//! | gemma-2-2b | `~/.cascade/models/gemma-2-2b/` | ~4 GB |
//! | llama-3.2-3b | `~/.cascade/models/llama-3.2-3b/` | ~6 GB (8 GB-floor) |
//! | phi-3-mini | `~/.cascade/models/phi-3-mini/` | ~7 GB |
//!
//! ```text
//! ~/.cascade/models/<model>/
//!   tokenizer.json          ← HuggingFace tokenizer JSON (preferred)
//!   tokenizer.model         ← SentencePiece BPE (fallback)
//!   config.json             ← model architecture config
//!   model.safetensors       ← weight tensor file (or sharded)
//! ```

pub mod adapter;
pub mod config;
pub mod downloader;
pub mod error;
pub mod prompt;
pub mod runner;
pub mod runner_factory;
pub mod runners;

// ── Top-level re-exports ──────────────────────────────────────────────────────

pub use adapter::LocalLlmAdapter;
pub use config::LocalLlmConfig;
pub use downloader::{
    download_model, find_model, model_dir_path, models, DownloadProgress, ModelEntry,
};
pub use error::LocalModelError;
pub use runner::{LocalLlmRunner, GEMMA_2_2B_MODEL_ID};
pub use runner_factory::{build_runner, LocalLlmRunnerTrait, ModelFamily};
pub use runners::llama3::{format_llama3_prompt, Llama3Runner, LLAMA3_2_3B_MODEL_ID};
pub use runners::phi3::{format_phi3_prompt, Phi3Runner, PHI3_MINI_MODEL_ID};

// ── Startup helper ────────────────────────────────────────────────────────────

/// Scan `~/.cascade/models/` and return a list of `(provider_id, LocalLlmAdapter)`
/// pairs for every model directory that is present on disk.
///
/// # Purpose
///
/// Called at daemon startup. For each known model whose directory exists under
/// `~/.cascade/models/<model_id>/`, this function constructs a `LocalLlmAdapter`
/// and returns it paired with the provider ID `local:<model_id>`. The caller
/// (daemon) registers these in the `ProviderRegistry` so the health-check and
/// routing subsystems see them as healthy local providers.
///
/// # Behaviour
///
/// - Uses `$HOME` env var first (test-isolation-safe), then `dirs::home_dir()`.
/// - Iterates the hardcoded model catalog (`downloader::models()`).
/// - Skips models whose directory is absent — no error, no log noise.
/// - Constructs each adapter with `LocalLlmConfig::new_for_model(id)` (defaults).
///
/// # Errors
///
/// Returns `Vec` — per-model errors are logged at WARN and skipped. The function
/// itself is infallible so the daemon never fails to start due to a bad local model.
///
/// # SPORT
///
/// Entity: cascade-local-llm crate — startup registration helper (E-P5-07)
pub fn scan_installed_models() -> Vec<(String, LocalLlmAdapter)> {
    let home = std::env::var_os("HOME")
        .map(std::path::PathBuf::from)
        .or_else(dirs::home_dir);
    let Some(home) = home else {
        return vec![];
    };
    let models_root = home.join(".cascade").join("models");

    let mut out = Vec::new();
    for entry in models() {
        let model_dir = models_root.join(entry.model_id);
        if !model_dir.is_dir() {
            continue;
        }
        let cfg = LocalLlmConfig {
            model_path: model_dir,
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        match LocalLlmAdapter::new(cfg) {
            Ok(adapter) => {
                let provider_id = format!("local:{}", entry.model_id);
                out.push((provider_id, adapter));
            }
            Err(e) => {
                // Log at warn but never abort startup.
                tracing::warn!(model_id = entry.model_id, error = %e,
                    "scan_installed_models: adapter init failed (skipping)");
            }
        }
    }
    out
}
