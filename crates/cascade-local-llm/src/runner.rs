//! `LocalLlmRunner` — async gemma-2-2b inference engine via candle-rs.
//!
//! # Purpose
//!
//! Wraps candle-transformers Gemma2 model loading and forward-pass inference
//! behind an async streaming interface.  All blocking work (model load,
//! tokenization, tensor ops) runs in `tokio::task::spawn_blocking` so the
//! async executor is never stalled.
//!
//! # Inputs / Outputs
//!
//! - `LocalLlmRunner::new(config) -> Result<Self, LocalModelError>`
//!   Validates the model directory, loads the tokenizer (fast, CPU, no
//!   weights), stores config.  Does NOT load the model weights — they are
//!   loaded lazily on first call to `run()`.
//!
//! - `runner.run(prompt) -> impl Stream<Item = String>`
//!   Tokenizes the prompt, runs the forward pass in spawn_blocking, and
//!   streams decoded tokens back through a tokio mpsc channel.
//!
//! # Constraints
//!
//! - Model weights are loaded once and shared via `Arc<Mutex<Option<Model>>>`.
//!   This avoids re-loading ~1.5 GB on every call.
//! - `spawn_blocking` is mandatory — candle tensor ops are CPU-bound and
//!   would block the async executor.
//! - Tests that require real model weights are gated behind `#[ignore]` and
//!   document the reason.  Unit tests only validate config/path/error logic.

use std::path::PathBuf;
use std::sync::Arc;

use tokio::sync::Mutex;
use tokio_stream::wrappers::ReceiverStream;
use tracing::{debug, info, warn};

use crate::{config::LocalLlmConfig, error::LocalModelError};

// ── Candle imports (compiled-out at the type level until model is loaded) ──────
use candle_core::Device;
use serde_json;

// ── MODEL_ID constant ─────────────────────────────────────────────────────────

/// The canonical model identifier reported to the provider registry.
pub const GEMMA_2_2B_MODEL_ID: &str = "local:gemma-2-2b";

// ── Device selection ──────────────────────────────────────────────────────────

/// Select the best available compute device.
///
/// Priority: Metal (if `metal` feature + macOS ARM) > CPU.
/// The `metal` feature is off by default so CI stays CPU-only.
pub fn select_device() -> Result<Device, LocalModelError> {
    #[cfg(feature = "metal")]
    {
        match Device::new_metal(0) {
            Ok(d) => {
                info!("cascade-local-llm: using Metal device (Apple Silicon)");
                return Ok(d);
            }
            Err(e) => {
                warn!("cascade-local-llm: Metal init failed ({e}), falling back to CPU");
            }
        }
    }
    debug!("cascade-local-llm: using CPU device");
    Ok(Device::Cpu)
}

// ── ModelState ────────────────────────────────────────────────────────────────

/// Lazily-loaded model state.
///
/// `None` means the model has not been loaded yet.  The first call to
/// `LocalLlmRunner::run()` triggers loading.  Subsequent calls reuse the
/// resident weights.
///
/// Wrapped in `Arc<Mutex<...>>` so the runner can be shared via `Arc<Self>`
/// across async tasks (required for `ProviderAdapter: Send + Sync`).
type ModelState = Arc<Mutex<Option<LoadedModel>>>;

/// Resident model + tokenizer after the first load.
struct LoadedModel {
    /// Gemma-2 model (weights in candle tensors on the selected device).
    model: candle_transformers::models::gemma2::Model,
    /// Tokenizer loaded from `tokenizer.json` (or `tokenizer.model`).
    tokenizer: tokenizers::Tokenizer,
    /// Device the tensors are on.
    device: Device,
}

// ── LocalLlmRunner ────────────────────────────────────────────────────────────

/// Async wrapper around a candle-rs Gemma-2-2b inference session.
///
/// Construct via [`LocalLlmRunner::new`].  The underlying model is loaded
/// lazily; the first call to [`LocalLlmRunner::run`] incurs a ~3–10 s
/// one-time load delay.
pub struct LocalLlmRunner {
    config: LocalLlmConfig,
    state: ModelState,
}

impl std::fmt::Debug for LocalLlmRunner {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("LocalLlmRunner")
            .field("model_path", &self.config.model_path)
            .field("max_tokens", &self.config.max_tokens)
            .field("loaded", &"<opaque>")
            .finish()
    }
}

impl LocalLlmRunner {
    /// Construct a new runner from `config`.
    ///
    /// Validates that the model directory exists and returns a typed error
    /// if it does not.  Does NOT load the model weights (lazy load on first
    /// `run()` call).
    ///
    /// # Errors
    ///
    /// - [`LocalModelError::WeightsNotFound`] — model directory is missing.
    pub fn new(config: LocalLlmConfig) -> Result<Self, LocalModelError> {
        // Validate the model directory exists.  We check the directory, not
        // individual files, so partial downloads show a clear error message.
        if !config.model_path.exists() {
            return Err(LocalModelError::WeightsNotFound {
                path: config.model_path.clone(),
            });
        }

        info!(
            path = %config.model_path.display(),
            "cascade-local-llm: runner created (weights not loaded yet)"
        );

        Ok(Self {
            config,
            state: Arc::new(Mutex::new(None)),
        })
    }

    /// Return the model path this runner is configured to use.
    pub fn model_path(&self) -> &PathBuf {
        &self.config.model_path
    }

    /// Return the device the runner would use (does not load the model).
    pub fn device() -> Result<Device, LocalModelError> {
        select_device()
    }

    /// Run text generation on `prompt`, returning an async stream of decoded
    /// token strings.
    ///
    /// The model is loaded on the first call.  Subsequent calls reuse the
    /// resident weights.  All tensor ops run in `tokio::task::spawn_blocking`
    /// so the async executor is never stalled.
    ///
    /// # Stream behaviour
    ///
    /// Each yielded `String` is a single decoded token (may be partial UTF-8
    /// bytes merged by the tokenizer decoder).  The stream ends when:
    /// - `max_tokens` tokens have been generated, or
    /// - An EOS token is produced, or
    /// - Any `stop_sequence` is matched.
    ///
    /// # Errors
    ///
    /// Errors are logged and the channel is dropped, causing the stream to
    /// end early.  The caller can detect premature end via stream length.
    pub async fn run(&self, prompt: &str) -> Result<ReceiverStream<String>, LocalModelError> {
        // Ensure the model is loaded (lazy init).
        self.ensure_loaded().await?;

        let state = Arc::clone(&self.state);
        let prompt = prompt.to_owned();
        let max_tokens = self.config.max_tokens;
        let temperature = self.config.temperature;
        let stop_sequences = self.config.stop_sequences.clone();

        let (tx, rx) = tokio::sync::mpsc::channel::<String>(128);

        // All candle ops run in a dedicated blocking thread so we don't stall
        // the Tokio executor on CPU-bound tensor work.
        tokio::task::spawn_blocking(move || {
            let rt = tokio::runtime::Handle::current();
            // Lock the model for the duration of inference.
            let mut guard = rt.block_on(state.lock());
            let loaded = match guard.as_mut() {
                Some(l) => l,
                None => {
                    // Should not happen (ensure_loaded ran above), but be safe.
                    let _ = tx.blocking_send(String::new());
                    return;
                }
            };

            if let Err(e) = run_inference(
                loaded,
                &prompt,
                max_tokens,
                temperature,
                &stop_sequences,
                &tx,
            ) {
                warn!("cascade-local-llm: inference error: {e}");
            }
        });

        Ok(ReceiverStream::new(rx))
    }

    // ── private helpers ───────────────────────────────────────────────────────

    /// Lazily load model weights into `self.state` if not already loaded.
    async fn ensure_loaded(&self) -> Result<(), LocalModelError> {
        let mut guard = self.state.lock().await;
        if guard.is_some() {
            return Ok(()); // already loaded
        }

        let config = self.config.clone();

        // Run the heavy weight loading in a blocking thread.
        let loaded = tokio::task::spawn_blocking(move || load_model(config))
            .await
            .map_err(|e| LocalModelError::StreamInterrupted(e.to_string()))??;

        *guard = Some(loaded);
        info!("cascade-local-llm: model loaded successfully");
        Ok(())
    }
}

// ── load_model (blocking) ─────────────────────────────────────────────────────

/// Load the Gemma-2 model and tokenizer from disk.
///
/// This is a blocking function — always call via `spawn_blocking`.
///
/// # Errors
///
/// - [`LocalModelError::WeightsNotFound`] if safetensors file is missing.
/// - [`LocalModelError::TokenizerLoad`] if the tokenizer file fails to load.
/// - [`LocalModelError::Candle`] for any candle tensor load error.
fn load_model(config: LocalLlmConfig) -> Result<LoadedModel, LocalModelError> {
    use candle_core::DType;
    use candle_transformers::models::gemma2::{Config as Gemma2Config, Model};
    use std::io::BufReader;

    let device = select_device()?;

    // ── Tokenizer ──────────────────────────────────────────────────────────
    let tok_path = config.tokenizer_path();
    if !tok_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: tok_path });
    }

    let tokenizer = tokenizers::Tokenizer::from_file(&tok_path).map_err(|e| {
        LocalModelError::TokenizerLoad {
            path: tok_path.clone(),
            reason: e.to_string(),
        }
    })?;

    // ── Model config ───────────────────────────────────────────────────────
    // Load config.json from the model directory.
    // config.json is required — it is always bundled with HuggingFace model
    // repos.  If absent, return a clear error rather than using hard-coded
    // defaults (which would diverge from actual weights).
    let config_path = config.model_path.join("config.json");
    if !config_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: config_path });
    }
    let f = std::fs::File::open(&config_path)
        .map_err(|e| LocalModelError::Candle(format!("config.json open: {e}")))?;
    let gemma_config: Gemma2Config = serde_json::from_reader(BufReader::new(f))
        .map_err(|e| LocalModelError::Candle(format!("config.json parse: {e}")))?;

    // ── Weights ────────────────────────────────────────────────────────────
    let weights_path = config.weights_path();
    if !weights_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: weights_path });
    }

    let vb = unsafe {
        candle_nn::VarBuilder::from_mmaped_safetensors(&[weights_path], DType::F32, &device)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?
    };

    let model =
        Model::new(false, &gemma_config, vb).map_err(|e| LocalModelError::Candle(e.to_string()))?;

    info!(
        "cascade-local-llm: weights loaded from {:?}",
        config.model_path
    );

    Ok(LoadedModel {
        model,
        tokenizer,
        device,
    })
}

// ── run_inference (blocking) ───────────────────────────────────────────────────

/// Execute the token-generation loop.
///
/// This is a blocking function — always called from `spawn_blocking`.
///
/// # SPORT: Inference algorithm
///
/// 1. Tokenize `prompt` → token id vec.
/// 2. Build input tensor `[1, seq_len]` on `device`.
/// 3. Loop up to `max_tokens`:
///    a. `model.forward(input, pos)` → logits `[1, 1, vocab]`.
///    b. Sample next token (greedy if temperature ≈ 0, else softmax sample).
///    c. Decode token id → string fragment.
///    d. Send fragment to `tx`.
///    e. Check EOS and stop_sequences.
///    f. Slide input to `[[next_token_id]]` (single-token step).
fn run_inference(
    loaded: &mut LoadedModel,
    prompt: &str,
    max_tokens: u32,
    temperature: f32,
    stop_sequences: &[String],
    tx: &tokio::sync::mpsc::Sender<String>,
) -> Result<(), LocalModelError> {
    use candle_core::Tensor;

    let device = &loaded.device;
    let tokenizer = &loaded.tokenizer;

    // ── Tokenize prompt ────────────────────────────────────────────────────
    let encoding = tokenizer
        .encode(prompt, false)
        .map_err(|e| LocalModelError::TokenizerLoad {
            path: PathBuf::from("<runtime>"),
            reason: format!("tokenize: {e}"),
        })?;
    let input_ids: Vec<u32> = encoding.get_ids().to_vec();
    if input_ids.is_empty() {
        return Ok(());
    }

    let seq_len = input_ids.len();
    let mut input_tensor = Tensor::new(input_ids.as_slice(), device)
        .map_err(|e| LocalModelError::Candle(e.to_string()))?
        .unsqueeze(0)
        .map_err(|e| LocalModelError::Candle(e.to_string()))?;

    let mut position: usize = 0;
    let mut generated = String::new();

    // EOS token id — tokenizers should have this; fall back to 1 (Gemma EOS)
    let eos_id: u32 = tokenizer
        .token_to_id("<eos>")
        .or_else(|| tokenizer.token_to_id("</s>"))
        .unwrap_or(1);

    for _ in 0..max_tokens {
        // Forward pass — returns logits [batch=1, seq, vocab]
        let logits = loaded
            .model
            .forward(&input_tensor, position)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        // Take logits for the last token position: [1, vocab]
        let logits = logits
            .squeeze(0)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?
            .get(logits.dims()[1].saturating_sub(1))
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        // Sample next token
        let next_token = sample_token(&logits, temperature)?;

        if next_token == eos_id {
            break;
        }

        // Decode token id → string fragment
        let fragment = tokenizer
            .decode(&[next_token], false)
            .map_err(|e| LocalModelError::StreamInterrupted(e.to_string()))?;

        generated.push_str(&fragment);

        // Send fragment to stream
        if tx.blocking_send(fragment.clone()).is_err() {
            // Receiver dropped — stop silently
            break;
        }

        // Check stop sequences
        if stop_sequences
            .iter()
            .any(|s| generated.ends_with(s.as_str()))
        {
            break;
        }

        // Slide input to single-token step
        input_tensor = Tensor::new(&[next_token][..], device)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?
            .unsqueeze(0)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        position += seq_len + (position > 0) as usize;
        // After first step position advances by 1 per token
        if position > seq_len {
            position = seq_len + (position - seq_len);
        }
    }

    Ok(())
}

// ── sample_token (blocking helper) ────────────────────────────────────────────

/// Sample the next token id from logits.
///
/// `temperature ≈ 0` → greedy argmax.
/// `temperature > 0` → softmax sampling.
///
/// `pub(crate)` so llama3 and phi3 runners can reuse the same sampling logic.
pub(crate) fn sample_token(
    logits: &candle_core::Tensor,
    temperature: f32,
) -> Result<u32, LocalModelError> {
    use candle_core::D;

    if temperature < 1e-4 {
        // Greedy — argmax
        let id = logits
            .argmax(D::Minus1)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?
            .to_scalar::<u32>()
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;
        return Ok(id);
    }

    // Softmax sampling
    let scaled =
        (logits / temperature as f64).map_err(|e| LocalModelError::Candle(e.to_string()))?;
    let probs = candle_nn::ops::softmax(&scaled, D::Minus1)
        .map_err(|e| LocalModelError::Candle(e.to_string()))?;

    // Convert to Vec<f32> for sampling
    let probs_vec: Vec<f32> = probs
        .to_vec1()
        .map_err(|e| LocalModelError::Candle(e.to_string()))?;

    // Simple cumulative sampling (no external rand dep)
    let sum: f32 = probs_vec.iter().sum();
    // Use a deterministic pseudo-random seed based on probs (avoids rand dep)
    // For production: replace with a seeded Rng.
    let threshold = (probs_vec.iter().copied().fold(0_f32, |a, b| a * 1.1 + b) % sum)
        .abs()
        .min(sum - f32::EPSILON);
    let mut cumulative = 0.0_f32;
    for (i, p) in probs_vec.iter().enumerate() {
        cumulative += p;
        if cumulative >= threshold {
            return Ok(i as u32);
        }
    }

    // Fallback: argmax
    let id = probs
        .argmax(D::Minus1)
        .map_err(|e| LocalModelError::Candle(e.to_string()))?
        .to_scalar::<u32>()
        .map_err(|e| LocalModelError::Candle(e.to_string()))?;
    Ok(id)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;
    use tempfile::TempDir;

    fn nonexistent_config() -> LocalLlmConfig {
        LocalLlmConfig {
            model_path: PathBuf::from("/tmp/__cascade_nonexistent_model_path__"),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        }
    }

    /// Non-existent model directory returns `WeightsNotFound`, never panics.
    ///
    /// This validates the core acceptance criterion from the spec.
    #[test]
    fn missing_model_dir_returns_weights_not_found_err() {
        let result = LocalLlmRunner::new(nonexistent_config());
        assert!(
            matches!(result, Err(LocalModelError::WeightsNotFound { .. })),
            "expected WeightsNotFound, got: {result:?}"
        );
    }

    /// An existing but empty directory passes the directory-existence check
    /// (weights check happens at load time).
    #[test]
    fn empty_model_dir_passes_constructor() {
        let dir = TempDir::new().expect("tempdir");
        let cfg = LocalLlmConfig {
            model_path: dir.path().to_path_buf(),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        // Should succeed — directory exists; weights missing is a load-time error
        let result = LocalLlmRunner::new(cfg);
        assert!(result.is_ok(), "expected Ok, got: {result:?}");
    }

    #[test]
    fn model_path_accessor_returns_configured_path() {
        let dir = TempDir::new().expect("tempdir");
        let path = dir.path().to_path_buf();
        let cfg = LocalLlmConfig {
            model_path: path.clone(),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        let runner = LocalLlmRunner::new(cfg).expect("runner");
        assert_eq!(runner.model_path(), &path);
    }

    #[test]
    fn device_selection_returns_ok() {
        // Device selection must not panic on any supported platform.
        let d = LocalLlmRunner::device();
        assert!(d.is_ok(), "device() failed: {d:?}");
    }

    /// Real model inference — requires gemma-2-2b weights at
    /// ~/.cascade/models/gemma-2-2b/.
    ///
    /// Ignored in CI because model weights are ~1.5 GB and are not present.
    /// Run locally with: `cargo test -p cascade-local-llm -- --ignored`
    #[tokio::test]
    #[ignore = "requires real gemma-2-2b weights (~1.5 GB); run with --ignored locally"]
    async fn real_model_generates_tokens() {
        let cfg = LocalLlmConfig::default_gemma_2_2b();
        let runner = LocalLlmRunner::new(cfg).expect("runner (weights must exist)");
        use crate::prompt::format_gemma2_prompt;
        use cascade_providers::Message;
        let prompt = format_gemma2_prompt(&[Message::user("Hello!")]);
        let stream = runner.run(&prompt).await.expect("run");
        use tokio_stream::StreamExt;
        let tokens: Vec<String> = stream.collect().await;
        assert!(!tokens.is_empty(), "should generate at least one token");
    }
}
