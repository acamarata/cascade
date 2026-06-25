//! `HarnessKind` — all supported harness identifiers.

use std::path::Path;

use super::detect::harness_is_installed;

/// All harnesses that Cascade supports for instruction-file generation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HarnessKind {
    ClaudeCode,
    OpenCode,
    Codex,
    Cursor,
    Aider,
    /// Antigravity AI coding tool — detect+config mode (E-P7-06).
    Antigravity,
}

impl HarnessKind {
    pub const ALL: &'static [Self] = &[
        Self::ClaudeCode,
        Self::OpenCode,
        Self::Codex,
        Self::Cursor,
        Self::Aider,
        Self::Antigravity,
    ];

    /// Lowercase identifier used in CLI args and MCP tool args.
    pub fn id(&self) -> &'static str {
        match self {
            Self::ClaudeCode => "claude-code",
            Self::OpenCode => "opencode",
            Self::Codex => "codex",
            Self::Cursor => "cursor",
            Self::Aider => "aider",
            Self::Antigravity => "antigravity",
        }
    }

    /// Preferred output filename relative to the workspace root.
    pub fn output_filename(&self) -> &'static str {
        match self {
            Self::ClaudeCode => "CLAUDE.md",
            Self::OpenCode => "AGENTS.md",
            Self::Codex => "AGENTS.md",
            Self::Cursor => ".cursor/rules/cascade.mdc",
            Self::Aider => "CONVENTIONS.md",
            Self::Antigravity => ".antigravity/cascade-rules.md",
        }
    }

    /// Returns true if a harness binary / config is present on disk.
    pub fn is_installed(&self, workspace: &Path) -> bool {
        harness_is_installed(self, workspace)
    }
}
