//! Gemini SSE parsing and Gemini → Anthropic streaming event translation.
//!
//! Purpose: pure, unit-testable translation layer between Gemini's
//! `streamGenerateContent?alt=sse` chunk format and the Anthropic Messages
//! API streaming event sequence. Kept free of any I/O so the translation
//! logic can be exercised with synthetic byte sequences in tests.
//!
//! Inputs:  raw SSE bytes from the Gemini upstream (`data: {...}\n\n` frames).
//! Outputs: a `Vec<(event_name, json_data)>` of Anthropic SSE events per
//!          Gemini chunk, formatted to wire bytes via `format_event`.
//!
//! Constraints: `GeminiSseParser` buffers partial frames across chunk
//!              boundaries (network reads rarely align on `\n\n`).
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy/anthropic_compat/sse

use serde_json::{json, Value};
use uuid::Uuid;

// ── Gemini SSE frame parser ───────────────────────────────────────────────────

/// Incremental parser for Gemini's `data: {...}\n\n` SSE framing.
///
/// Feed raw upstream bytes via [`GeminiSseParser::push`]; complete frames are
/// returned as parsed [`Value`]s. Partial frames are buffered until the next
/// `push` call completes them.
#[derive(Default)]
pub struct GeminiSseParser {
    buf: String,
}

impl GeminiSseParser {
    /// Construct an empty parser.
    pub fn new() -> Self {
        Self::default()
    }

    /// Feed a chunk of raw bytes; returns any complete `data:` JSON frames
    /// found. Non-UTF8 bytes are lossily converted (Gemini SSE is always
    /// UTF-8 JSON text).
    pub fn push(&mut self, bytes: &[u8]) -> Vec<Value> {
        self.buf.push_str(&String::from_utf8_lossy(bytes));
        self.drain_complete_frames()
    }

    /// Extract and parse every complete `\n\n`-terminated frame currently
    /// buffered, leaving any trailing partial frame in `self.buf`.
    fn drain_complete_frames(&mut self) -> Vec<Value> {
        let mut out = Vec::new();
        while let Some(pos) = self.buf.find("\n\n") {
            let frame = self.buf[..pos].to_string();
            self.buf.drain(..pos + 2);

            for line in frame.lines() {
                let line = line.trim_end_matches('\r');
                if let Some(data) = line.strip_prefix("data:") {
                    let data = data.trim();
                    if data.is_empty() || data == "[DONE]" {
                        continue;
                    }
                    if let Ok(v) = serde_json::from_str::<Value>(data) {
                        out.push(v);
                    }
                }
            }
        }
        out
    }
}

// ── Anthropic event formatting ────────────────────────────────────────────────

/// Format one Anthropic SSE event as wire bytes: `event: <name>\ndata: <json>\n\n`.
pub fn format_event(event_name: &str, data: &Value) -> Vec<u8> {
    format!("event: {event_name}\ndata: {data}\n\n").into_bytes()
}

// ── Streaming translator (Gemini chunk → Anthropic events) ───────────────────

/// Stateful translator that converts a sequence of parsed Gemini
/// `streamGenerateContent` chunks into the Anthropic Messages API streaming
/// event sequence.
///
/// Call [`StreamTranslator::start`] once, [`StreamTranslator::on_chunk`] for
/// each parsed Gemini JSON value, and [`StreamTranslator::finish`] when the
/// upstream stream ends normally (no error).
pub struct StreamTranslator {
    model: String,
    message_id: String,
    content_block_open: bool,
    output_tokens: u64,
    finish_reason: Option<String>,
}

impl StreamTranslator {
    /// Construct a new translator for a response using `model` (the Gemini
    /// model name echoed back as the Anthropic `model` field).
    pub fn new(model: impl Into<String>) -> Self {
        Self {
            model: model.into(),
            message_id: format!("msg_gp_{}", Uuid::new_v4().simple()),
            content_block_open: false,
            output_tokens: 0,
            finish_reason: None,
        }
    }

    /// Build the initial `message_start` event. Must be emitted first.
    pub fn start(&self) -> (&'static str, Value) {
        (
            "message_start",
            json!({
                "type": "message_start",
                "message": {
                    "id": self.message_id,
                    "type": "message",
                    "role": "assistant",
                    "content": [],
                    "model": self.model,
                    "stop_reason": Value::Null,
                    "stop_sequence": Value::Null,
                    "usage": { "input_tokens": 0, "output_tokens": 0 }
                }
            }),
        )
    }

    /// Translate one parsed Gemini chunk into zero or more Anthropic events.
    ///
    /// The first chunk carrying text triggers `content_block_start`. Every
    /// chunk carrying non-empty text produces a `content_block_delta`.
    /// `usageMetadata` (if present) updates the running output-token count
    /// (Anthropic's `message_delta` event only ever reports `output_tokens`;
    /// `input_tokens` is reported once, in `message_start`).
    /// `finishReason` (if present) is recorded for `finish()`.
    pub fn on_chunk(&mut self, chunk: &Value) -> Vec<(&'static str, Value)> {
        let mut events = Vec::new();

        if let Some(usage) = chunk.get("usageMetadata") {
            if let Some(n) = usage.get("candidatesTokenCount").and_then(Value::as_u64) {
                self.output_tokens = n;
            }
        }

        let candidate = chunk
            .get("candidates")
            .and_then(Value::as_array)
            .and_then(|c| c.first());

        if let Some(reason) = candidate
            .and_then(|c| c.get("finishReason"))
            .and_then(Value::as_str)
        {
            self.finish_reason = Some(reason.to_string());
        }

        let text = candidate
            .and_then(|c| c.get("content"))
            .and_then(|c| c.get("parts"))
            .and_then(Value::as_array)
            .map(|parts| {
                parts
                    .iter()
                    .filter_map(|p| p.get("text").and_then(Value::as_str))
                    .collect::<String>()
            })
            .unwrap_or_default();

        if !text.is_empty() {
            if !self.content_block_open {
                events.push((
                    "content_block_start",
                    json!({
                        "type": "content_block_start",
                        "index": 0,
                        "content_block": { "type": "text", "text": "" }
                    }),
                ));
                self.content_block_open = true;
            }
            events.push((
                "content_block_delta",
                json!({
                    "type": "content_block_delta",
                    "index": 0,
                    "delta": { "type": "text_delta", "text": text }
                }),
            ));
        }

        events
    }

    /// Map a Gemini `finishReason` to an Anthropic `stop_reason`.
    fn stop_reason(&self) -> &'static str {
        match self.finish_reason.as_deref() {
            Some("MAX_TOKENS") => "max_tokens",
            _ => "end_turn",
        }
    }

    /// Build the closing event sequence: `content_block_stop` (if a block was
    /// opened) → `message_delta` → `message_stop`.
    pub fn finish(&self) -> Vec<(&'static str, Value)> {
        let mut events = Vec::new();

        if self.content_block_open {
            events.push((
                "content_block_stop",
                json!({ "type": "content_block_stop", "index": 0 }),
            ));
        }

        events.push((
            "message_delta",
            json!({
                "type": "message_delta",
                "delta": {
                    "stop_reason": self.stop_reason(),
                    "stop_sequence": Value::Null
                },
                "usage": { "output_tokens": self.output_tokens }
            }),
        ));

        events.push(("message_stop", json!({ "type": "message_stop" })));

        events
    }

    /// Build an Anthropic `error` SSE event for a mid-stream upstream failure.
    pub fn error_event(message: &str) -> (&'static str, Value) {
        (
            "error",
            json!({
                "type": "error",
                "error": {
                    "type": "api_error",
                    "message": message
                }
            }),
        )
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    // Test-only: the canonical id is referenced by fixtures, not by the
    // translator itself, so it must be imported HERE — a module-level import
    // is dead in the non-test build and fails clippy -D warnings.
    use cascade_types::model_ids::MODEL_GEMINI_FLASH_LATEST;

    fn event_names(events: &[Vec<u8>]) -> Vec<String> {
        events
            .iter()
            .map(|bytes| {
                let s = String::from_utf8_lossy(bytes);
                s.lines()
                    .find_map(|l| l.strip_prefix("event: "))
                    .unwrap_or("")
                    .to_string()
            })
            .collect()
    }

    /// Simulate the full adapter loop over a sequence of raw Gemini SSE byte
    /// chunks, returning the formatted Anthropic wire-format events.
    fn run_translation(model: &str, raw_chunks: &[&str]) -> Vec<Vec<u8>> {
        let mut parser = GeminiSseParser::new();
        let mut translator = StreamTranslator::new(model);
        let mut out = Vec::new();

        let (name, data) = translator.start();
        out.push(format_event(name, &data));

        for raw in raw_chunks {
            for gemini_chunk in parser.push(raw.as_bytes()) {
                for (name, data) in translator.on_chunk(&gemini_chunk) {
                    out.push(format_event(name, &data));
                }
            }
        }

        for (name, data) in translator.finish() {
            out.push(format_event(name, &data));
        }

        out
    }

    #[test]
    fn translates_single_chunk_stream_to_full_anthropic_sequence() {
        let raw = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}}\n\n";
        let events = run_translation(MODEL_GEMINI_FLASH_LATEST, &[raw]);

        assert_eq!(
            event_names(&events),
            vec![
                "message_start",
                "content_block_start",
                "content_block_delta",
                "content_block_stop",
                "message_delta",
                "message_stop",
            ]
        );

        // message_start carries model + zeroed usage.
        let start_json: Value = serde_json::from_str(
            String::from_utf8_lossy(&events[0])
                .lines()
                .nth(1)
                .unwrap()
                .trim_start_matches("data: "),
        )
        .unwrap();
        assert_eq!(start_json["message"]["model"], MODEL_GEMINI_FLASH_LATEST);
        assert_eq!(start_json["message"]["usage"]["input_tokens"], 0);

        // content_block_delta carries the text.
        let delta_json: Value = serde_json::from_str(
            String::from_utf8_lossy(&events[2])
                .lines()
                .nth(1)
                .unwrap()
                .trim_start_matches("data: "),
        )
        .unwrap();
        assert_eq!(delta_json["delta"]["text"], "Hello");
        assert_eq!(delta_json["delta"]["type"], "text_delta");

        // message_delta carries stop_reason + output_tokens.
        let mdelta_json: Value = serde_json::from_str(
            String::from_utf8_lossy(&events[4])
                .lines()
                .nth(1)
                .unwrap()
                .trim_start_matches("data: "),
        )
        .unwrap();
        assert_eq!(mdelta_json["delta"]["stop_reason"], "end_turn");
        assert_eq!(mdelta_json["usage"]["output_tokens"], 2);
    }

    #[test]
    fn translates_multi_chunk_stream_with_repeated_deltas() {
        let chunks = [
            "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hel\"}]}}]}\n\n",
            "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"lo \"}]}}]}\n\n",
            "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"world\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":3}}\n\n",
        ];
        let events = run_translation(MODEL_GEMINI_FLASH_LATEST, &chunks);

        assert_eq!(
            event_names(&events),
            vec![
                "message_start",
                "content_block_start",
                "content_block_delta",
                "content_block_delta",
                "content_block_delta",
                "content_block_stop",
                "message_delta",
                "message_stop",
            ]
        );
    }

    #[test]
    fn parser_buffers_partial_frame_across_chunk_boundary() {
        let mut parser = GeminiSseParser::new();
        // Split a single frame mid-way through the JSON payload.
        let full = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"split\"}]}}]}\n\n";
        let (part1, part2) = full.split_at(30);

        let first = parser.push(part1.as_bytes());
        assert!(first.is_empty(), "no complete frame yet");

        let second = parser.push(part2.as_bytes());
        assert_eq!(second.len(), 1);
        assert_eq!(
            second[0]["candidates"][0]["content"]["parts"][0]["text"],
            "split"
        );
    }

    #[test]
    fn parser_ignores_done_and_empty_data_lines() {
        let mut parser = GeminiSseParser::new();
        let out = parser.push(b"data: [DONE]\n\ndata: \n\n");
        assert!(out.is_empty());
    }

    #[test]
    fn empty_text_chunk_produces_no_delta_events() {
        let mut translator = StreamTranslator::new(MODEL_GEMINI_FLASH_LATEST);
        let chunk: Value =
            serde_json::from_str("{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"\"}]}}]}")
                .unwrap();
        let events = translator.on_chunk(&chunk);
        assert!(events.is_empty());
    }

    #[test]
    fn max_tokens_finish_reason_maps_to_anthropic_max_tokens() {
        let mut translator = StreamTranslator::new(MODEL_GEMINI_FLASH_LATEST);
        let chunk: Value = serde_json::from_str(
            "{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"x\"}]},\"finishReason\":\"MAX_TOKENS\"}]}",
        )
        .unwrap();
        translator.on_chunk(&chunk);
        let finish_events = translator.finish();
        let (name, data) = &finish_events[1]; // content_block_stop, message_delta, message_stop
        assert_eq!(*name, "message_delta");
        assert_eq!(data["delta"]["stop_reason"], "max_tokens");
    }

    #[test]
    fn error_event_has_correct_anthropic_shape() {
        let (name, data) = StreamTranslator::error_event("all Gemini providers exhausted");
        assert_eq!(name, "error");
        assert_eq!(data["type"], "error");
        assert_eq!(data["error"]["type"], "api_error");
        assert_eq!(data["error"]["message"], "all Gemini providers exhausted");
    }

    #[test]
    fn format_event_produces_correct_sse_wire_framing() {
        let bytes = format_event("message_stop", &json!({"type": "message_stop"}));
        let s = String::from_utf8(bytes).unwrap();
        assert_eq!(
            s,
            "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
        );
    }

    #[test]
    fn no_content_block_when_stream_has_only_finish_reason() {
        // Some terminal chunks carry only finishReason + usageMetadata, no text.
        let mut translator = StreamTranslator::new(MODEL_GEMINI_FLASH_LATEST);
        let chunk: Value = serde_json::from_str(
            "{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":0}}",
        )
        .unwrap();
        let events = translator.on_chunk(&chunk);
        assert!(events.is_empty(), "no text => no content_block events");

        let finish_events = translator.finish();
        // content_block_open is false, so no content_block_stop.
        assert_eq!(finish_events.len(), 2);
        assert_eq!(finish_events[0].0, "message_delta");
        assert_eq!(finish_events[1].0, "message_stop");
    }
}
