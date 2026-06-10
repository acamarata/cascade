//! Configuration types for the local LLM runner.
//!
//! # Purpose
//!
//! `LocalLlmConfig` is the sole input to [`crate::runner::LocalLlmRunner`].
//! It captures the model path, generation hyper-parameters, and optional
//! device preferences.
//!
//! # Inputs / Outputs
//!
//! `LocalLlmConfig` is `Clone` + `Debug` + `Serialize` + `Deserialize` so it
//! can be embedded in the Cascade settings file and round-tripped over IPC.
//!
//! # Constraints
//!
//! - `model_path` must point to a directory containing at minimum:
//!   - `tokenizer.model` — SentencePiece BPE vocabulary
//!   - `model.safetensors` — (or sharded `model-0000X-of-Y.safetensors`)
//! - Default values follow the spec guide: `max_tokens = 512`,
//!   `temperature = 0.7`.
//! - `stop_sequences` is empty by default (generation runs until `max_tokens`
//!   or EOS token).

use std::path::PathBuf;

use serde::{Deserialize, Serialize};

// ── LocalLlmConfig ────────────────────────────────────────────────────────────

/// Runtime configuration for the gemma-2-2b local runner.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LocalLlmConfig {
    /// Path to the model directory (e.g. `~/.cascade/models/gemma-2-2b/`).
    ///
    /// The directory must contain:
    /// - `tokenizer.model` — SentencePiece tokenizer vocabulary
    /// - `model.safetensors` — weight file(s) for the model
    pub model_path: PathBuf,

    /// Maximum number of new tokens to generate per request.
    ///
    /// Defaults to `512`.  Requests may override this via
    /// `CompletionRequest::max_tokens`.
    #[serde(default = "default_max_tokens")]
    pub max_tokens: u32,

    /// Sampling temperature in `[0.0, 2.0]`.
    ///
    /// `0.0` = greedy (deterministic), `1.0` = default softmax sampling,
    /// `>1.0` = high entropy / creative output.
    /// Defaults to `0.7`.
    #[serde(default = "default_temperature")]
    pub temperature: f32,

    /// Sequences that stop generation early (in addition to the EOS token).
    ///
    /// Defaults to `[]` (only the EOS token stops generation).
    #[serde(default)]
    pub stop_sequences: Vec<String>,
}

impl LocalLlmConfig {
    /// Convenience constructor: uses the canonical Cascade model directory
    /// (`~/.cascade/models/gemma-2-2b/`) with default hyper-parameters.
    pub fn default_gemma_2_2b() -> Self {
        let mut model_path = dirs::home_dir().unwrap_or_else(|| PathBuf::from("~"));
        model_path.push(".cascade");
        model_path.push("models");
        model_path.push("gemma-2-2b");
        Self {
            model_path,
            max_tokens: default_max_tokens(),
            temperature: default_temperature(),
            stop_sequences: vec![],
        }
    }

    /// Return the expected path to the SentencePiece tokenizer file.
    pub fn tokenizer_path(&self) -> PathBuf {
        self.model_path.join("tokenizer.model")
    }

    /// Return the expected path to the primary safetensors weight file.
    pub fn weights_path(&self) -> PathBuf {
        self.model_path.join("model.safetensors")
    }
}

// ── Serde defaults ────────────────────────────────────────────────────────────

fn default_max_tokens() -> u32 {
    512
}

fn default_temperature() -> f32 {
    0.7
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_gemma_2_2b_paths_are_correct() {
        let cfg = LocalLlmConfig::default_gemma_2_2b();
        let tok = cfg.tokenizer_path();
        let wts = cfg.weights_path();
        assert!(tok.ends_with("tokenizer.model"), "tok path: {tok:?}");
        assert!(wts.ends_with("model.safetensors"), "wts path: {wts:?}");
        // Both should be under .cascade/models/gemma-2-2b/
        assert!(
            cfg.model_path.to_string_lossy().contains("gemma-2-2b"),
            "model_path: {:?}",
            cfg.model_path
        );
    }

    #[test]
    fn defaults_match_spec() {
        let cfg = LocalLlmConfig::default_gemma_2_2b();
        assert_eq!(cfg.max_tokens, 512, "spec: max_tokens default = 512");
        assert!(
            (cfg.temperature - 0.7_f32).abs() < 1e-6,
            "spec: temperature default = 0.7"
        );
        assert!(cfg.stop_sequences.is_empty());
    }

    #[test]
    fn round_trip_serde() {
        let cfg = LocalLlmConfig {
            model_path: PathBuf::from("/tmp/gemma"),
            max_tokens: 1024,
            temperature: 0.5,
            stop_sequences: vec!["<end>".into()],
        };
        let json = serde_json::to_string(&cfg).expect("serialize");
        let back: LocalLlmConfig = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(back.max_tokens, 1024);
        assert!((back.temperature - 0.5_f32).abs() < 1e-6);
        assert_eq!(back.stop_sequences, vec!["<end>"]);
    }
}
