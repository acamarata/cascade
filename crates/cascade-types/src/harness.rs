//! Harness integration types for Cascade's augmenting meta-harness layer.
//!
//! Purpose: Shared request/response types for the GCI harness-status and
//!   harness-regenerate API endpoints (GET /api/gci/harness-status,
//!   POST /api/gci/harness-regenerate). Used by cascade-daemon's HTTP handlers
//!   and serialised over the wire to the dashboard frontend.
//!
//! Inputs:  None (type definitions only; no I/O in this module).
//! Outputs: Serialisable/Deserialisable structs consumed by cascade-daemon
//!   HTTP handlers and the React dashboard panel.
//!
//! Constraints:
//!   - No I/O or filesystem operations in this module.
//!   - `serde(rename_all = "camelCase")` ensures consistent JSON field names
//!     that match the TypeScript frontend conventions.
//!   - All types implement `Debug` and `Clone` for test ergonomics.
//!
//! SPORT: T-P3-E02-28 — MASTER-ROUTES.md: /api/gci/harness-status,
//!   /api/gci/harness-regenerate; MASTER-COMPONENTS.md: HarnessPanel

use std::path::PathBuf;

use serde::{Deserialize, Serialize};

// ── Codex harness types ───────────────────────────────────────────────────────

/// Detection result for the OpenAI Codex CLI harness.
///
/// Purpose: Carries the resolved binary path, version string, and config
///   directory for the Codex CLI when it is detected on the host system.
///   Importable from cascade-types without depending on cascade-harness.
///
/// Inputs:  populated by `cascade_harness::codex::detect::detect_codex()`.
/// Outputs: consumed by scaffold_codex_config, CLI status, and the daemon.
///
/// Constraints:
///   - `path` is an absolute, already-confirmed-executable path.
///   - `version` is the first line of `codex --version` output.
///   - `config_dir` is `$XDG_CONFIG_HOME/codex` or `~/.config/codex`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CodexInfo {
    /// Absolute path to the `codex` binary.
    pub path: PathBuf,
    /// Version string from `codex --version` (first line).
    pub version: String,
    /// Path to the Codex config directory.
    pub config_dir: PathBuf,
}

/// A single Codex session snapshot captured by the session monitor.
///
/// Purpose: Records a live Codex process observed by `CodexMonitor::snapshot()`.
///
/// Constraints:
///   - `pid` matches the OS process ID at snapshot time (may recycle).
///   - `started_at` is a Unix epoch second timestamp.
///   - `workspace` is the process working-directory, if determinable.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct CodexSession {
    /// OS process ID.
    pub pid: u32,
    /// Unix epoch seconds when the process started (as reported by sysinfo).
    pub started_at: i64,
    /// Working directory of the process, if readable.
    pub workspace: Option<PathBuf>,
}

/// A single harness entry in the status response.
///
/// Purpose: Describes the detection state and instruction-file linkage for
///   one AI coding harness (CC = Claude Code, OC = OpenCode).
///
/// Inputs:  Populated by the daemon's harness detector at request time.
/// Outputs: Serialised to JSON in GET /api/gci/harness-status response.
///
/// Constraints:
///   - `name` MUST be "cc" or "oc" (lower-case, matching harness identifiers).
///   - `instruction_file_path` is `None` when no instruction file is found;
///     `Some(abs_path_string)` when the file (symlink or regular) exists.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct HarnessEntry {
    /// Harness identifier: "cc" (Claude Code) or "oc" (OpenCode).
    pub name: String,
    /// Whether the harness is detected on disk (settings file present).
    pub detected: bool,
    /// Absolute path of the instruction file (CLAUDE.md / AGENTS.md), if any.
    pub instruction_file_path: Option<String>,
    /// Whether the instruction file exists and is linked to CASCADE.md.
    pub instruction_file_linked: bool,
}

/// Response payload for GET /api/gci/harness-status.
///
/// Purpose: Returns status for all known harnesses (cc, oc) in a fixed-order
///   list so the frontend can render two deterministic harness cards.
///
/// Outputs: Serialised as `{"harnesses":[...]}` JSON object.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct HarnessStatus {
    /// Ordered list of harness entries; always contains exactly two entries:
    /// index 0 = cc, index 1 = oc.
    pub harnesses: Vec<HarnessEntry>,
}

/// Request body for POST /api/gci/harness-regenerate.
///
/// Purpose: Identifies which harness to regenerate instruction-file links for.
/// Inputs:  JSON body `{"harness":"cc"}` or `{"harness":"oc"}`.
/// Constraints: `harness` MUST be "cc" or "oc"; any other value → HTTP 400.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct RegenerateRequest {
    /// Target harness: "cc" or "oc".
    pub harness: String,
}

/// Response payload for POST /api/gci/harness-regenerate.
///
/// Purpose: Reports how many instruction-file links were created or updated.
/// Outputs: Serialised as `{"regeneratedCount":N}` JSON object.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct RegenerateResponse {
    /// Number of instruction-file symlinks created or updated (≥ 0).
    pub regenerated_count: u32,
}
