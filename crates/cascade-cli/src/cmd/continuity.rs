//! `cascade continuity` — manage Cascade Continuity intents.
//!
//! Purpose: Continuity lets a user leave a "continuation intent" on disk when
//! they hit a usage-cap window mid-session. The daemon's continuity watcher
//! (`cascade-daemon::continuity`) polls `~/.cascade/accounts/quota.json` and,
//! once the named account's 5-hour utilization drops back under 100%, either
//! headlessly resumes the Claude Code session (`mode: resume`) or fires a
//! local notification (`mode: notify`).
//!
//! This module owns only the CLI surface: `add` writes an intent file,
//! `list`/`status` read intents for display, `rm` deletes one. All fire
//! logic lives in the daemon.
//!
//! Constraints:
//!   - Intentionally self-contained (no dependency on the `cascade-daemon`
//!     crate) — mirrors the `cascade ram` pattern so the CLI never needs to
//!     link the daemon binary's dependency graph. Keep the `ContinuityIntent`
//!     shape in sync with `crates/cascade-daemon/src/continuity.rs` if either
//!     changes (both are additive/serde-defaulted so drift is low-risk).
//!   - Never panics: I/O failures print a friendly message and return `Ok(())`
//!     rather than propagating an error the user can't act on.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-cli, continuity module (E2-S1)

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};
use serde::{Deserialize, Serialize};

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// `cascade continuity` — manage session-resume intents fired on quota reset.
#[derive(Debug, Args)]
pub struct ContinuityArgs {
    #[command(subcommand)]
    pub subcommand: ContinuityCommands,
}

/// Subcommands under `cascade continuity`.
#[derive(Debug, Subcommand)]
pub enum ContinuityCommands {
    /// Add a new continuation intent.
    Add {
        /// Claude Code session id to resume (`claude --resume <id>`).
        #[arg(long)]
        session: String,
        /// Prompt text passed via `-p` on resume. Default: "continue".
        #[arg(long, default_value = "continue")]
        prompt: String,
        /// Fleet account id to watch (e.g. "claude", "claude2"). Default: "claude".
        #[arg(long, default_value = "claude")]
        account: String,
        /// `CLAUDE_CONFIG_DIR` to run the resume under. Default: ~/.claude.
        #[arg(long)]
        config_dir: Option<PathBuf>,
        /// Delivery mode: "resume" (headless) or "notify" (OS notification only).
        #[arg(long, default_value = "resume")]
        mode: String,
        /// Free-form note (e.g. reminder of what the session was doing).
        #[arg(long)]
        note: Option<String>,
    },
    /// List all intents (pending and fired).
    List,
    /// Remove an intent by id.
    Rm {
        /// Intent id to remove.
        id: String,
    },
    /// Show watcher config and pending/fired intent counts.
    Status,
}

#[async_trait]
impl Command for ContinuityArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            ContinuityCommands::Add {
                session,
                prompt,
                account,
                config_dir,
                mode,
                note,
            } => run_add(
                session,
                prompt,
                account,
                config_dir.as_deref(),
                mode,
                note.as_deref(),
            ),
            ContinuityCommands::List => run_list(),
            ContinuityCommands::Rm { id } => run_rm(id),
            ContinuityCommands::Status => run_status(),
        }
    }
}

// ── Intent schema (mirrors cascade-daemon::continuity::ContinuityIntent) ─────

/// Delivery mode — see module docs. Kept in sync with the daemon's
/// `ContinuityMode` (same serde shape: lowercase string).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum ContinuityMode {
    Resume,
    Notify,
}

impl std::fmt::Display for ContinuityMode {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ContinuityMode::Resume => write!(f, "resume"),
            ContinuityMode::Notify => write!(f, "notify"),
        }
    }
}

fn parse_mode(s: &str) -> Option<ContinuityMode> {
    match s.to_ascii_lowercase().as_str() {
        "resume" => Some(ContinuityMode::Resume),
        "notify" => Some(ContinuityMode::Notify),
        _ => None,
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct ContinuityIntent {
    id: String,
    session_id: String,
    #[serde(default = "default_config_dir")]
    config_dir: String,
    #[serde(default = "default_prompt")]
    prompt: String,
    #[serde(default = "default_account_id")]
    account_id: String,
    #[serde(default = "default_mode")]
    mode: ContinuityMode,
    created_at: i64,
    #[serde(default)]
    fired_at: Option<i64>,
    #[serde(default)]
    note: Option<String>,
    #[serde(default)]
    last_error: Option<String>,
}

fn default_config_dir() -> String {
    dirs::home_dir()
        .map(|h| h.join(".claude").to_string_lossy().into_owned())
        .unwrap_or_else(|| ".claude".to_owned())
}
fn default_prompt() -> String {
    "continue".to_owned()
}
fn default_account_id() -> String {
    "claude".to_owned()
}
fn default_mode() -> ContinuityMode {
    ContinuityMode::Resume
}

// ── Paths ─────────────────────────────────────────────────────────────────────

fn continuity_dir() -> PathBuf {
    let home = std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(dirs::home_dir)
        .unwrap_or_else(|| PathBuf::from("~"));
    home.join(".cascade").join("continuity")
}

fn intent_path(dir: &Path, id: &str) -> PathBuf {
    dir.join(format!("{id}.json"))
}

fn write_intent(dir: &Path, intent: &ContinuityIntent) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    let path = intent_path(dir, &intent.id);
    let bytes = serde_json::to_vec_pretty(intent)?;
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, &bytes)?;
    #[cfg(unix)]
    {
        use std::fs::Permissions;
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, Permissions::from_mode(0o600))?;
    }
    std::fs::rename(&tmp, &path)?;
    Ok(())
}

fn list_intents(dir: &Path) -> Vec<ContinuityIntent> {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return Vec::new(),
    };
    let mut out: Vec<ContinuityIntent> = entries
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().and_then(|s| s.to_str()) == Some("json"))
        .filter_map(|p| {
            let bytes = std::fs::read(&p).ok()?;
            serde_json::from_slice(&bytes).ok()
        })
        .collect();
    out.sort_by_key(|i: &ContinuityIntent| i.created_at);
    out
}

// ── add ───────────────────────────────────────────────────────────────────────

fn run_add(
    session: &str,
    prompt: &str,
    account: &str,
    config_dir: Option<&Path>,
    mode: &str,
    note: Option<&str>,
) -> Result<()> {
    let Some(mode) = parse_mode(mode) else {
        println!("error: --mode must be \"resume\" or \"notify\" (got {mode:?})");
        return Ok(());
    };

    let id = uuid::Uuid::new_v4().simple().to_string();
    let config_dir = config_dir
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_else(default_config_dir);
    let created_at = chrono::Utc::now().timestamp();

    let intent = ContinuityIntent {
        id: id.clone(),
        session_id: session.to_owned(),
        config_dir,
        prompt: prompt.to_owned(),
        account_id: account.to_owned(),
        mode,
        created_at,
        fired_at: None,
        note: note.map(|s| s.to_owned()),
        last_error: None,
    };

    if let Err(e) = write_intent(&continuity_dir(), &intent) {
        println!("error: failed to write continuity intent: {e}");
        return Ok(());
    }

    println!("added continuity intent: {id}");
    println!("  session:  {}", intent.session_id);
    println!("  account:  {}", intent.account_id);
    println!("  mode:     {}", intent.mode);
    println!(
        "  the daemon will fire this once {}'s 5-hour window drops under 100% utilization",
        intent.account_id
    );
    Ok(())
}

// ── list ──────────────────────────────────────────────────────────────────────

fn run_list() -> Result<()> {
    let intents = list_intents(&continuity_dir());
    if intents.is_empty() {
        println!(
            "No continuity intents found ({})",
            continuity_dir().display()
        );
        return Ok(());
    }

    println!(
        "{:<34} {:<10} {:<8} {:<20} {:<8} NOTE",
        "ID", "ACCOUNT", "MODE", "CREATED", "FIRED"
    );
    println!("{}", "-".repeat(100));
    for intent in &intents {
        println!(
            "{:<34} {:<10} {:<8} {:<20} {:<8} {}",
            intent.id,
            intent.account_id,
            intent.mode.to_string(),
            format_ts(intent.created_at),
            if intent.fired_at.is_some() {
                "yes"
            } else {
                "no"
            },
            intent.note.as_deref().unwrap_or("-"),
        );
    }
    Ok(())
}

fn format_ts(epoch: i64) -> String {
    chrono::DateTime::from_timestamp(epoch, 0)
        .map(|dt| dt.format("%Y-%m-%d %H:%M").to_string())
        .unwrap_or_else(|| epoch.to_string())
}

// ── rm ────────────────────────────────────────────────────────────────────────

fn run_rm(id: &str) -> Result<()> {
    let path = intent_path(&continuity_dir(), id);
    if !path.exists() {
        println!("no continuity intent found with id {id}");
        return Ok(());
    }
    match std::fs::remove_file(&path) {
        Ok(()) => println!("removed continuity intent {id}"),
        Err(e) => println!("error: failed to remove {id}: {e}"),
    }
    Ok(())
}

// ── status ────────────────────────────────────────────────────────────────────

fn run_status() -> Result<()> {
    let intents = list_intents(&continuity_dir());
    let pending = intents.iter().filter(|i| i.fired_at.is_none()).count();
    let fired = intents.iter().filter(|i| i.fired_at.is_some()).count();
    let failed = intents
        .iter()
        .filter(|i| i.fired_at.is_some() && i.last_error.is_some())
        .count();

    println!("Cascade Continuity");
    println!("  intents dir:      {}", continuity_dir().display());
    println!("  watch interval:   60s (daemon-owned)");
    println!("  pending:          {pending}");
    println!("  fired:            {fired} ({failed} with errors)");
    if pending > 0 {
        println!();
        println!("Run `cascade continuity list` to see pending intents.");
    }
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn make_intent(id: &str) -> ContinuityIntent {
        ContinuityIntent {
            id: id.to_owned(),
            session_id: "sess-1".to_owned(),
            config_dir: default_config_dir(),
            prompt: default_prompt(),
            account_id: default_account_id(),
            mode: ContinuityMode::Resume,
            created_at: 1_700_000_000,
            fired_at: None,
            note: None,
            last_error: None,
        }
    }

    #[test]
    fn write_and_list_round_trip() {
        let tmp = TempDir::new().unwrap();
        write_intent(tmp.path(), &make_intent("a")).unwrap();
        write_intent(tmp.path(), &make_intent("b")).unwrap();
        let listed = list_intents(tmp.path());
        assert_eq!(listed.len(), 2);
    }

    #[cfg(unix)]
    #[test]
    fn write_intent_sets_0600_permissions() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = TempDir::new().unwrap();
        let intent = make_intent("perm");
        write_intent(tmp.path(), &intent).unwrap();
        let meta = std::fs::metadata(intent_path(tmp.path(), "perm")).unwrap();
        assert_eq!(meta.permissions().mode() & 0o777, 0o600);
    }

    #[test]
    fn list_intents_skips_malformed_files() {
        let tmp = TempDir::new().unwrap();
        write_intent(tmp.path(), &make_intent("good")).unwrap();
        std::fs::write(tmp.path().join("bad.json"), b"not json").unwrap();
        let listed = list_intents(tmp.path());
        assert_eq!(listed.len(), 1);
        assert_eq!(listed[0].id, "good");
    }

    #[test]
    fn parse_mode_accepts_known_values_case_insensitively() {
        assert_eq!(parse_mode("resume"), Some(ContinuityMode::Resume));
        assert_eq!(parse_mode("NOTIFY"), Some(ContinuityMode::Notify));
        assert_eq!(parse_mode("bogus"), None);
    }

    #[test]
    fn continuity_args_help_builds_without_panic() {
        use clap::Args as ClapArgs;
        let _ = ContinuityArgs::augment_args(clap::Command::new("continuity"));
    }

    #[test]
    fn format_ts_never_panics_on_edge_values() {
        let _ = format_ts(0);
        let _ = format_ts(i64::MAX / 2);
    }
}
