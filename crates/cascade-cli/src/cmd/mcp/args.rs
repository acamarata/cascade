//! Top-level CLI argument types for `cascade mcp`.


use clap::{Args, Subcommand, ValueEnum};

// ── Top-level args ────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp`.
#[derive(Debug, Args)]
pub struct McpArgs {
    #[command(subcommand)]
    pub subcommand: McpSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum McpSubcmd {
    /// Print the current MCP auth token for manual client configuration.
    Token(super::token::McpTokenArgs),
    /// Show MCP server transport status and active connections.
    Status(super::status::McpStatusArgs),
    /// Configure an AI client tool to connect to the cascade MCP server.
    Setup(McpSetupArgs),
    /// Start the MCP server in stdio mode (for Codex and other subprocess clients).
    ///
    /// Reads JSON-RPC messages from stdin and writes responses to stdout.
    /// This is the entry point configured by `cascade harness codex install`.
    Stdio(super::stdio::McpStdioArgs),
}

// ── Tool name enum ────────────────────────────────────────────────────────────

/// Which AI client tool to configure.
#[derive(Debug, Clone, ValueEnum)]
pub enum ToolName {
    /// Claude Code CLI
    #[value(name = "claude-code")]
    ClaudeCode,
    /// Claude Desktop app
    #[value(name = "claude-desktop")]
    ClaudeDesktop,
    /// VS Code (+ Continue.dev)
    #[value(name = "vscode")]
    Vscode,
    /// OpenCode
    #[value(name = "opencode")]
    Opencode,
}

// ── Setup args ────────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp setup`.
#[derive(Debug, Args)]
pub struct McpSetupArgs {
    /// The AI client tool to configure.
    #[arg(long, value_enum)]
    pub tool: Option<ToolName>,

    /// Configure all detected clients automatically.
    #[arg(long, conflicts_with = "tool")]
    pub all: bool,

    /// List detected clients without configuring anything.
    #[arg(long, conflicts_with = "tool")]
    pub list: bool,

    /// Remove the cascade entry from the target config instead of adding it.
    #[arg(long)]
    pub remove: bool,

    /// Use HTTP transport instead of stdio (for tools that support it).
    #[arg(long)]
    pub http: bool,

    /// Write to project-local config instead of global (opencode only).
    #[arg(long)]
    pub local: bool,

    /// Write to global user config (vscode: ~/.vscode/mcp.json).
    #[arg(long)]
    pub global: bool,

    /// Print what would be written without modifying any files.
    #[arg(long)]
    pub dry_run: bool,
}
