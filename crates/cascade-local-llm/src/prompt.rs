//! Gemma-2 chat-template prompt formatter.
//!
//! # Purpose
//!
//! Gemma-2 uses a specific chat template with `<start_of_turn>` /
//! `<end_of_turn>` tokens.  This module converts a
//! [`cascade_providers::CompletionRequest`] (list of `Message` structs) into
//! the correctly formatted string for the tokenizer.
//!
//! # Inputs / Outputs
//!
//! `format_gemma2_prompt(messages) -> String`
//! — takes a slice of `Message` and returns a UTF-8 prompt string.
//!
//! # Constraints
//!
//! - The formatted prompt ALWAYS ends with `<start_of_turn>model\n` so the
//!   model continues from the assistant turn (not a user turn).
//! - System messages are prepended as a standalone system block before the
//!   first user message.
//! - Only `system`, `user`, and `assistant` roles are handled; unknown roles
//!   are mapped to `user` and a tracing warning is emitted.

use cascade_providers::{Message, MessageRole};
use tracing::warn;

// ── Constants ─────────────────────────────────────────────────────────────────

const BOS: &str = "<bos>";
const START_TURN: &str = "<start_of_turn>";
const END_TURN: &str = "<end_of_turn>";
const USER_ROLE: &str = "user";
const MODEL_ROLE: &str = "model";
const SYSTEM_ROLE: &str = "system"; // Gemma2 system block header

// ── format_gemma2_prompt ──────────────────────────────────────────────────────

/// Convert a conversation history into the Gemma-2 chat template format.
///
/// # Example
///
/// Given `[system("You are helpful."), user("Hello")]` produces:
/// ```text
/// <bos><start_of_turn>system
/// You are helpful.<end_of_turn>
/// <start_of_turn>user
/// Hello<end_of_turn>
/// <start_of_turn>model
/// ```
pub fn format_gemma2_prompt(messages: &[Message]) -> String {
    let mut out = String::from(BOS);

    for msg in messages {
        let role_str = match msg.role {
            MessageRole::System => SYSTEM_ROLE,
            MessageRole::User => USER_ROLE,
            MessageRole::Assistant => MODEL_ROLE,
        };
        // Warn on any role that didn't cleanly map (future-proof for new variants)
        if role_str == USER_ROLE && msg.role != MessageRole::User {
            warn!("unexpected message role mapped to 'user'");
        }

        out.push_str(START_TURN);
        out.push_str(role_str);
        out.push('\n');
        out.push_str(&msg.content);
        out.push_str(END_TURN);
        out.push('\n');
    }

    // Always close with the model-turn opener so the model continues
    out.push_str(START_TURN);
    out.push_str(MODEL_ROLE);
    out.push('\n');

    out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_providers::Message;

    #[test]
    fn single_user_message() {
        let msgs = vec![Message::user("Hello")];
        let prompt = format_gemma2_prompt(&msgs);
        assert!(prompt.starts_with("<bos>"), "missing BOS: {prompt}");
        assert!(
            prompt.contains("<start_of_turn>user\nHello<end_of_turn>"),
            "{prompt}"
        );
        assert!(
            prompt.ends_with("<start_of_turn>model\n"),
            "must end with model turn opener: {prompt}"
        );
    }

    #[test]
    fn system_plus_user_message() {
        let msgs = vec![
            Message::system("You are a helpful assistant."),
            Message::user("What is 2+2?"),
        ];
        let prompt = format_gemma2_prompt(&msgs);
        assert!(
            prompt.contains("<start_of_turn>system\nYou are a helpful assistant.<end_of_turn>"),
            "{prompt}"
        );
        assert!(
            prompt.contains("<start_of_turn>user\nWhat is 2+2?<end_of_turn>"),
            "{prompt}"
        );
        assert!(prompt.ends_with("<start_of_turn>model\n"), "{prompt}");
    }

    #[test]
    fn multi_turn_conversation() {
        let msgs = vec![
            Message::user("Hi"),
            Message::assistant("Hello!"),
            Message::user("How are you?"),
        ];
        let prompt = format_gemma2_prompt(&msgs);
        // Should have: user/Hi, model/Hello!, user/How are you?, then model opener
        assert!(
            prompt.contains("<start_of_turn>model\nHello!<end_of_turn>"),
            "{prompt}"
        );
        assert!(prompt.ends_with("<start_of_turn>model\n"), "{prompt}");
    }

    #[test]
    fn empty_messages_just_bos_and_model_opener() {
        let prompt = format_gemma2_prompt(&[]);
        assert_eq!(prompt, "<bos><start_of_turn>model\n");
    }

    #[test]
    fn bos_appears_exactly_once() {
        let msgs = vec![
            Message::system("s"),
            Message::user("u"),
            Message::assistant("a"),
        ];
        let prompt = format_gemma2_prompt(&msgs);
        assert_eq!(
            prompt.matches("<bos>").count(),
            1,
            "BOS must appear exactly once: {prompt}"
        );
    }
}
