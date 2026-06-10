//! `Llama3Runner` — async llama-3.2-3b inference engine via candle-rs.
//!
//! # Purpose
//!
//! Wraps candle-transformers `Llama` model (llama-3.2-3b) loading and
//! forward-pass inference behind the [`LocalLlmRunnerTrait`] interface.
//! All blocking work (model load, tokenization, tensor ops) runs in
//! `tokio::task::spawn_blocking` so the async executor is never stalled.
//!
//! # Chat template
//!
//! Llama-3 uses a `<|begin_of_text|>` BOS token and role headers of the form:
//! ```text
//! <|begin_of_text|><|start_header_id|>user<|end_header_id|>
//! {content}<|eot_id|>
//! <|start_header_id|>assistant<|end_header_id|>
//! ```
//!
//! # 8GB-floor note
//!
//! llama-3.2-3b is the low-RAM option for cascade — it fits comfortably in
//! 8 GB of system RAM on CPU (F32 weights ~6 GB; use Metal for GPU offload).
//! This is the default model for machines at the 8 GB floor per repo conventions.
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
    config::LocalLlmConfig, error::LocalModelError, runner::select_device,
    runner_factory::LocalLlmRunnerTrait,
};

use candle_core::Device;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Canonical model identifier for the llama-3.2-3b runner.
pub const LLAMA3_2_3B_MODEL_ID: &str = "local:llama-3.2-3b";

// ── Chat template helpers ─────────────────────────────────────────────────────

/// Format messages into the Llama-3 chat template.
///
/// Template:
/// ```text
/// <|begin_of_text|><|start_header_id|>role<|end_header_id|>
/// content<|eot_id|>
/// ...
/// <|start_header_id|>assistant<|end_header_id|>
/// ```
pub fn format_llama3_prompt(messages: &[cascade_providers::Message]) -> String {
    use cascade_providers::MessageRole;

    let mut out = String::from("<|begin_of_text|>");

    for msg in messages {
        let role = match msg.role {
            MessageRole::System => "system",
            MessageRole::User => "user",
            MessageRole::Assistant => "assistant",
        };
        out.push_str("<|start_header_id|>");
        out.push_str(role);
        out.push_str("<|end_header_id|>\n");
        out.push_str(&msg.content);
        out.push_str("<|eot_id|>\n");
    }

    // Open the assistant turn for the model to continue
    out.push_str("<|start_header_id|>assistant<|end_header_id|>\n");

    out
}

// ── ModelState ────────────────────────────────────────────────────────────────

type ModelState = Arc<Mutex<Option<LoadedModel>>>;

struct LoadedModel {
    model: candle_transformers::models::llama::Llama,
    tokenizer: tokenizers::Tokenizer,
    device: Device,
    /// KV-cache for autoregressive generation — mutated per-token in the inference loop.
    cache: candle_transformers::models::llama::Cache,
}

// ── Llama3Runner ──────────────────────────────────────────────────────────────

/// Async wrapper around a candle-rs llama-3.2-3b inference session.
///
/// Implements [`LocalLlmRunnerTrait`] — construct via
/// [`crate::runner_factory::build_runner`] with `ModelFamily::Llama3`.
pub struct Llama3Runner {
    config: LocalLlmConfig,
    state: ModelState,
}

impl std::fmt::Debug for Llama3Runner {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Llama3Runner")
            .field("model_path", &self.config.model_path)
            .field("max_tokens", &self.config.max_tokens)
            .field("loaded", &"<opaque>")
            .finish()
    }
}

impl Llama3Runner {
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
            "cascade-local-llm: Llama3Runner created (weights not loaded yet)"
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
        info!("cascade-local-llm: Llama3 model loaded successfully");
        Ok(())
    }
}

#[async_trait::async_trait]
impl LocalLlmRunnerTrait for Llama3Runner {
    async fn run(&self, prompt: &str) -> Result<ReceiverStream<String>, LocalModelError> {
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
            if let Err(e) = run_inference(
                loaded,
                &prompt,
                max_tokens,
                temperature,
                &stop_sequences,
                &tx,
            ) {
                warn!("cascade-local-llm: Llama3 inference error: {e}");
            }
        });

        Ok(ReceiverStream::new(rx))
    }
}

// ── load_model (blocking) ─────────────────────────────────────────────────────

fn load_model(config: LocalLlmConfig) -> Result<LoadedModel, LocalModelError> {
    use candle_core::DType;
    use candle_transformers::models::llama::{Cache, Llama, LlamaConfig};
    use std::io::BufReader;

    let device = select_device()?;

    // ── Tokenizer ──────────────────────────────────────────────────────────
    let tok_path = config.model_path.join("tokenizer.json");
    let sp_path = config.model_path.join("tokenizer.model");
    if !tok_path.exists() && !sp_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: tok_path });
    }
    let tok_file = if tok_path.exists() {
        tok_path.clone()
    } else {
        sp_path
    };
    let tokenizer = tokenizers::Tokenizer::from_file(&tok_file).map_err(|e| {
        LocalModelError::TokenizerLoad {
            path: tok_file.clone(),
            reason: e.to_string(),
        }
    })?;

    // ── Model config ───────────────────────────────────────────────────────
    // LlamaConfig (Deserializable) → Config (internal, no Deserialize)
    let config_path = config.model_path.join("config.json");
    if !config_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: config_path });
    }
    let f = std::fs::File::open(&config_path)
        .map_err(|e| LocalModelError::Candle(format!("config.json open: {e}")))?;
    let llama_config: LlamaConfig = serde_json::from_reader(BufReader::new(f))
        .map_err(|e| LocalModelError::Candle(format!("config.json parse: {e}")))?;
    let model_config = llama_config.into_config(false); // use_flash_attn = false

    // ── Weights ────────────────────────────────────────────────────────────
    let weights_path = config.model_path.join("model.safetensors");
    if !weights_path.exists() {
        return Err(LocalModelError::WeightsNotFound { path: weights_path });
    }

    let vb = unsafe {
        candle_nn::VarBuilder::from_mmaped_safetensors(&[weights_path], DType::F32, &device)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?
    };

    let cache = Cache::new(false, DType::F32, &model_config, &device)
        .map_err(|e| LocalModelError::Candle(e.to_string()))?;

    let model =
        Llama::load(vb, &model_config).map_err(|e| LocalModelError::Candle(e.to_string()))?;

    info!(
        "cascade-local-llm: Llama3 weights loaded from {:?}",
        config.model_path
    );
    Ok(LoadedModel {
        model,
        tokenizer,
        device,
        cache,
    })
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
    use crate::runner::sample_token;
    use candle_core::Tensor;

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

    // EOS token — llama3 uses <|eot_id|> (128009) or <|end_of_text|> (128001)
    let eos_id: u32 = tokenizer
        .token_to_id("<|eot_id|>")
        .or_else(|| tokenizer.token_to_id("<|end_of_text|>"))
        .or_else(|| tokenizer.token_to_id("</s>"))
        .unwrap_or(128009);

    for step in 0..max_tokens {
        let logits = loaded
            .model
            .forward(&input_tensor, step as usize, &mut loaded.cache)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        let logits = logits
            .squeeze(0)
            .map_err(|e| LocalModelError::Candle(e.to_string()))?;

        // Take logits for last position
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

        if stop_sequences
            .iter()
            .any(|s| generated.ends_with(s.as_str()))
        {
            break;
        }

        // Single-token step
        let pos = seq_len + step as usize;
        debug!("llama3: step {step}, pos {pos}");
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
            model_path: PathBuf::from("/tmp/__cascade_llama3_nonexistent__"),
            max_tokens: 512,
            temperature: 0.7,
            stop_sequences: vec![],
        }
    }

    #[test]
    fn missing_model_dir_returns_weights_not_found_err() {
        let result = Llama3Runner::new(nonexistent_config());
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
        let result = Llama3Runner::new(cfg);
        assert!(
            result.is_ok(),
            "expected Ok for existing dir, got: {result:?}"
        );
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
        let runner = Llama3Runner::new(cfg).expect("runner");
        assert_eq!(runner.model_path(), &path);
    }

    #[test]
    fn llama3_chat_template_starts_with_bos() {
        use cascade_providers::Message;
        let msgs = vec![Message::user("Hello")];
        let prompt = format_llama3_prompt(&msgs);
        assert!(
            prompt.starts_with("<|begin_of_text|>"),
            "must start with BOS token: {prompt}"
        );
    }

    #[test]
    fn llama3_chat_template_contains_header_format() {
        use cascade_providers::Message;
        let msgs = vec![Message::user("Hi there")];
        let prompt = format_llama3_prompt(&msgs);
        assert!(
            prompt.contains("<|start_header_id|>user<|end_header_id|>"),
            "must contain user header: {prompt}"
        );
        assert!(
            prompt.contains("Hi there"),
            "must contain message content: {prompt}"
        );
        assert!(
            prompt.contains("<|eot_id|>"),
            "must contain eot_id: {prompt}"
        );
        assert!(
            prompt.ends_with("<|start_header_id|>assistant<|end_header_id|>\n"),
            "must end with assistant turn opener: {prompt}"
        );
    }

    #[test]
    fn llama3_chat_template_multi_turn() {
        use cascade_providers::Message;
        let msgs = vec![
            Message::system("You are helpful."),
            Message::user("What is Rust?"),
            Message::assistant("Rust is a systems language."),
            Message::user("Tell me more."),
        ];
        let prompt = format_llama3_prompt(&msgs);
        assert!(
            prompt.contains("<|start_header_id|>system<|end_header_id|>"),
            "{prompt}"
        );
        assert!(
            prompt.contains("<|start_header_id|>assistant<|end_header_id|>"),
            "{prompt}"
        );
        assert!(
            prompt.ends_with("<|start_header_id|>assistant<|end_header_id|>\n"),
            "{prompt}"
        );
    }

    /// Real model inference — requires llama-3.2-3b weights.
    /// Ignored in CI. Run with: `cargo test -p cascade-local-llm -- --ignored`
    #[tokio::test]
    #[ignore = "requires real llama-3.2-3b weights (~6 GB F32); run with --ignored locally"]
    async fn real_model_generates_tokens() {
        let cfg = LocalLlmConfig {
            model_path: std::path::Path::new(&std::env::var("HOME").unwrap())
                .join(".cascade/models/llama-3.2-3b"),
            max_tokens: 20,
            temperature: 0.0,
            stop_sequences: vec![],
        };
        let runner = Llama3Runner::new(cfg).expect("runner (weights must exist)");
        use cascade_providers::Message;
        let prompt = format_llama3_prompt(&[Message::user("Hello!")]);
        let stream = runner.run(&prompt).await.expect("run");
        use tokio_stream::StreamExt;
        let tokens: Vec<String> = stream.collect().await;
        assert!(!tokens.is_empty(), "should generate at least one token");
    }
}
