//! `Phi3Runner` — async phi-3-mini inference engine via candle-rs.
//!
//! # Purpose
//!
//! Wraps candle-transformers `Phi3` model (phi-3-mini) loading and
//! forward-pass inference behind the [`LocalLlmRunnerTrait`] interface.
//! All blocking work (model load, tokenization, tensor ops) runs in
//! `tokio::task::spawn_blocking` so the async executor is never stalled.
//!
//! # Chat template
//!
//! Phi-3 uses the `<|user|>` / `<|assistant|>` / `<|system|>` role format:
//! ```text
//! <|system|>
//! {system_content}<|end|>
//! <|user|>
//! {user_content}<|end|>
//! <|assistant|>
//! ```
//!
//! # Constraints
//!
//! - Model weights loaded lazily on first `run()` call.
//! - Tests that require real weights are gated behind `#[ignore]`.

use std::path::PathBuf;
use std::sync::Arc;

use tokio::sync::Mutex;
use tokio_stream::wrappers::ReceiverStream;
use tracing::{debug, info, warn};

use crate::{
    config::LocalLlmConfig,
    error::LocalModelError,
    runner::select_device,
    runner_factory::LocalLlmRunnerTrait,
};

use candle_core::Device;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Canonical model identifier for the phi-3-mini runner.
pub const PHI3_MINI_MODEL_ID: &str = "local:phi-3-mini";

// ── Chat template helpers ─────────────────────────────────────────────────────

/// Format messages into the Phi-3 chat template.
///
/// Template:
/// ```text
/// <|system|>
/// {system_content}<|end|>
/// <|user|>
/// {user_content}<|end|>
/// <|assistant|>
/// ```
pub fn format_phi3_prompt(messages: &[cascade_providers::Message]) -> String {
    use cascade_providers::MessageRole;

    let mut out = String::new();

    for msg in messages {
        let role_tag = match msg.role {
            MessageRole::System => "<|system|>",
            MessageRole::User => "<|user|>",
            MessageRole::Assistant => "<|assistant|>",
        };
        out.push_str(role_tag);
        out.push('\n');
        out.push_str(&msg.content);
        out.push_str("<|end|>\n");
    }

    // Open assistant turn
    out.push_str("<|assistant|>\n");

    out
}

// ── ModelState ────────────────────────────────────────────────────────────────

type ModelState = Arc<Mutex<Option<LoadedModel>>>;

struct LoadedModel {
    /// The phi-3 model (candle type is `Model`, not `Phi3`).
    model: candle_transformers::models::phi3::Model,
    tokenizer: tokenizers::Tokenizer,
    device: Device,
}

// ── Phi3Runner ────────────────────────────────────────────────────────────────

/// Async wrapper around a candle-rs phi-3-mini inference session.
///
/// Implements [`LocalLlmRunnerTrait`] — construct via
/// [`crate::runner_factory::build_runner`] with `ModelFamily::Phi3`.
pub struct Phi3Runner {
    config: LocalLlmConfig,
    state: ModelState,
}

impl std::fmt::Debug for Phi3Runner {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Phi3Runner")
            .field("model_path", &self.config.model_path)
            .field("max_tokens", &self.config.max_tokens)
            .field("loaded", &"<opaque>")
            .finish()
    }
}

impl Phi3Runner {
    /// Construct a new runner from `config`.
    ///
    /// Validates that the model directory exists; returns
    /// [`LocalModelError::WeightsNotFound`] if it does not.
    /// Does NOT load model weights (lazy load on first `run()` call).
    ///
    /// # Errors
    ///
    /// - [`LocalModelError::WeightsNotFound`] — model directory missing.
    pub fn new(config: LocalLlmConfig) -> Result<Self, LocalModelError> {
        if !config.model_path.exists() {
            return Err(LocalModelError::WeightsNotFound {
                path: config.model_path.clone(),
            });
        }
        info!(
            path = %config.model_path.display(),
            "cascade-local-llm: Phi3Runner created (weights not loaded yet)"
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

    // ── private helpers ───────────────────────────────────────────────────────

    async fn ensure_loaded(&self) -> Result<(), LocalModelError> {
        let mut guard = self.state.lock().await;
        if guard.is_some() {
            return Ok(());
        }
        let config = self.config.clone();
        let loaded = tokio::task::spawn_blocking(move || load_model(config))
            .await
            .map_err(|e| LocalModelError::StreamInterrupted(e.to_string()))??;
        *guard = Some(loaded);
        info!("cascade-local-llm: Phi3 model loaded successfully");
        Ok(())
    }
}

#[async_trait::async_trait]
impl LocalLlmRunnerTrait for Phi3Runner {
    async fn run(
        &self,
        prompt: &str,
    ) -> Result<ReceiverStream<String>, LocalModelError> {
        self.ensure_loaded().await?;

        let state = Arc::clone(&self.state);
        let prompt = prompt.to_owned();
        let max_tokens = self.config.max_tokens;
        let temperature = self.config.temperature;
        let stop_sequences = self.config.stop_sequences.clone();

        let (tx, rx) = tokio::sync::mpsc::channel::<String>(128);

        tokio::task::spawn_blocking(move || {
            let rt = tokio::runtime::Handle::current();
            let mut guard = rt.block_on(state.lock());
            let loaded = match guard.as_mut() {
                Some(l) => l,
                None => {
                    let _ = tx.blocking_send(String::new());
                    return;
                }
            };
            if let Err(e) = run_inference(loaded, &prompt, max_tokens, temperature, &stop_sequences, &tx) {
                warn!("cascade-local-llm: Phi3 inference error: {e}");
            }
        });

        Ok(ReceiverStream::new(rx))
    }
}

// ── load_model (blocking) ─────────────────────────────────────────────────────

fn load_model(config: LocalLlmConfig) -> Result<LoadedModel, LocalModelError> {
    use candle_core::DType;
    use candle_transformers::models::phi3::{Config as Phi3Config, Model as Phi3Model};
    use std::io::BufReader;

    let device = select_device()?;

    // ── Tokenizer ──────────────────────────────────────────────────────────
    let tok_path = config.model_path.join("tokenizer.json");
    let sp_path = config.model_path.join("tokenizer.model");
    if !tok_path.exists() && !sp_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: tok_path });
    }
    let tok_file = if tok_path.exists() { tok_path.clone() } else { sp_path };
    let tokenizer = tokenizers::Tokenizer::from_file(&tok_file).map_err(|e| {
        LocalModelError::TokenizerLoad {
            path: tok_file.clone(),
            reason: e.to_string(),
        }
    })?;

    // ── Model config ───────────────────────────────────────────────────────
    let config_path = config.model_path.join("config.json");
    if !config_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: config_path });
    }
    let f = std::fs::File::open(&config_path)
        .map_err(|e| LocalModelError::Candle(format!("config.json open: {e}")))?;
    let phi3_config: Phi3Config = serde_json::from_reader(BufReader::new(f))
        .map_err(|e| LocalModelError::Candle(format!("config.json parse: {e}")))?;

    // ── Weights ────────────────────────────────────────────────────────────
    let weights_path = config.model_path.join("model.safetensors");
    if !weights_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: weights_path });
    }

    let vb = unsafe {
        candle_nn::VarBuilder::from_mmaped_safetensors(
            &[weights_path],
            DType::F32,
            &device,
        )
        .map_err(|e| LocalModelError::Candle(e.to_string()))?
    };

    // Phi3 constructor signature: Model::new(&Config, vb: VarBuilder)
    let model = Phi3Model::new(&phi3_config, vb)
        .map_err(|e| LocalModelError::Candle(e.to_string()))?;

    info!("cascade-local-llm: Phi3 weights loaded from {:?}", config.model_path);
    Ok(LoadedModel { model, tokenizer, device })
}

// ── run_inference (blocking) ──────────────────────────────────────────────────

fn run_inference(
    loaded: &mut LoadedModel,
    prompt: &str,
    max_tokens: u32,
    temperature: f32,
    stop_sequences: &[String],
    tx: &tokio::sync::mpsc::Sender<String>,
) -> Result<(), LocalModelError> {
    use candle_core::Tensor;
    use crate::runner::sample_token;

    let device = &loaded.device;
    let tokenizer = &loaded.tokenizer;

    // ── Tokenize ───────────────────────────────────────────────────────────
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

    let mut generated = String::new();

    // EOS token — phi-3 uses <|end|> (32007) or <|endoftext|>
    let eos_id: u32 = tokenizer
        .token_to_id("<|end|>")
        .or_else(|| tokenizer.token_to_id("<|endoftext|>"))
        .or_else(|| tokenizer.token_to_id("</s>"))
        .unwrap_or(32007);

    for step in 0..max_tokens {
        // phi3::Model::forward(&Tensor, seqlen_offset: usize)
        let logits = loaded
            .model
            .forward(&input_tensor, step as usize)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        // logits shape: [batch=1, seq, vocab] → squeeze batch → [seq, vocab]
        let logits = logits
            .squeeze(0)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        // Take logits for last position → [vocab]
        let last_logits = logits
            .get(logits.dims()[0].saturating_sub(1))
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        let next_token = sample_token(&last_logits, temperature)?;

        if next_token == eos_id {
            break;
        }

        let fragment = tokenizer
            .decode(&[next_token], false)
            .map_err(|e| LocalModelError::StreamInterrupted(e.to_string()))?;

        generated.push_str(&fragment);

        if tx.blocking_send(fragment.clone()).is_err() {
            break;
        }

        if stop_sequences.iter().any(|s| generated.ends_with(s.as_str())) {
            break;
        }

        let pos = seq_len + step as usize;
        debug!("phi3: step {step}, pos {pos}");
        input_tensor = Tensor::new(&[next_token][..], device)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?
            .unsqueeze(0)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;
    }

    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn nonexistent_config() -> LocalLlmConfig {
        LocalLlmConfig {
            model_path: PathBuf::from("/tmp/__cascade_phi3_nonexistent__"),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        }
    }

    #[test]
    fn missing_model_dir_returns_weights_not_found_err() {
        let result = Phi3Runner::new(nonexistent_config());
        assert!(
            matches!(result, Err(LocalModelError::WeightsNotFound { .. })),
            "expected WeightsNotFound, got: {result:?}"
        );
    }

    #[test]
    fn empty_model_dir_passes_constructor() {
        let dir = TempDir::new().expect("tempdir");
        let cfg = LocalLlmConfig {
            model_path: dir.path().to_path_buf(),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        };
        let result = Phi3Runner::new(cfg);
        assert!(result.is_ok(), "expected Ok for existing dir, got: {result:?}");
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
        let runner = Phi3Runner::new(cfg).expect("runner");
        assert_eq!(runner.model_path(), &path);
    }

    #[test]
    fn phi3_chat_template_user_role_tag() {
        use cascade_providers::Message;
        let msgs = vec![Message::user("Hello")];
        let prompt = format_phi3_prompt(&msgs);
        assert!(
            prompt.contains("<|user|>\nHello<|end|>"),
            "must contain user block: {prompt}"
        );
        assert!(
            prompt.ends_with("<|assistant|>\n"),
            "must end with assistant turn opener: {prompt}"
        );
    }

    #[test]
    fn phi3_chat_template_system_role() {
        use cascade_providers::Message;
        let msgs = vec![
            Message::system("You are helpful."),
            Message::user("Hi"),
        ];
        let prompt = format_phi3_prompt(&msgs);
        assert!(
            prompt.contains("<|system|>\nYou are helpful.<|end|>"),
            "must contain system block: {prompt}"
        );
        assert!(
            prompt.contains("<|user|>\nHi<|end|>"),
            "must contain user block: {prompt}"
        );
        assert!(
            prompt.ends_with("<|assistant|>\n"),
            "must end with assistant opener: {prompt}"
        );
    }

    #[test]
    fn phi3_chat_template_multi_turn() {
        use cascade_providers::Message;
        let msgs = vec![
            Message::user("What is Rust?"),
            Message::assistant("Rust is a systems language."),
            Message::user("Tell me more."),
        ];
        let prompt = format_phi3_prompt(&msgs);
        assert!(prompt.contains("<|assistant|>\nRust is a systems language.<|end|>"), "{prompt}");
        assert!(prompt.ends_with("<|assistant|>\n"), "{prompt}");
    }

    /// Real model inference — requires phi-3-mini weights.
    /// Ignored in CI. Run with: `cargo test -p cascade-local-llm -- --ignored`
    #[tokio::test]
    #[ignore = "requires real phi-3-mini weights (~7 GB F32); run with --ignored locally"]
    async fn real_model_generates_tokens() {
        let cfg = LocalLlmConfig {
            model_path: std::path::Path::new(&std::env::var("HOME").unwrap())
                .join(".cascade/models/phi-3-mini"),
            max_tokens: 20,
            temperature: 0.0,
            stop_sequences: vec![],
        };
        let runner = Phi3Runner::new(cfg).expect("runner (weights must exist)");
        use cascade_providers::Message;
        let prompt = format_phi3_prompt(&[Message::user("Hello!")]);
        let stream = runner.run(&prompt).await.expect("run");
        use tokio_stream::StreamExt;
        let tokens: Vec<String> = stream.collect().await;
        assert!(!tokens.is_empty(), "should generate at least one token");
    }
}
