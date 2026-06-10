//! # cascade_core::library::inject
//!
//! Harness injection engine — inserts a [`LibraryItem`] into the appropriate
//! harness config file (Claude Code, OpenCode, or Codex).
//!
//! ## Purpose
//!
//! Appends a formatted block to the target harness configuration file so that
//! the item's content (persona system prompt, prompt text, slash-command, or
//! agent definition) becomes available in that harness immediately.
//!
//! ## Injection targets and strategies
//!
//! | Harness | Persona                  | Prompt                   | SlashCommand             | Agent                               |
//! |---------|--------------------------|--------------------------|--------------------------|-------------------------------------|
//! | CC      | `~/.claude/CLAUDE.md`    | `~/.claude/CLAUDE.md`    | `~/.claude/commands/{slug}.md` | `~/.claude/agents/{slug}.md`   |
//! | OC      | `~/.config/opencode/AGENTS.md` | `~/.config/opencode/AGENTS.md` | `~/.config/opencode/prompts/{slug}.md` | `~/.config/opencode/agents/{slug}.md` |
//! | Codex   | `~/.codex/AGENTS.md`     | `~/.codex/AGENTS.md`     | `~/.codex/AGENTS.md`     | `~/.codex/agents/{slug}.md`         |
//!
//! ## Idempotency
//!
//! Before appending, the engine checks for the presence of
//! `<!-- cascade:{slug} -->` in the target file.  If found, the injection is
//! skipped and `Ok(InjectionOutcome::AlreadyPresent)` is returned.
//!
//! ## Marker format
//!
//! ```text
//! <!-- cascade:{slug} -->
//! ## {header}: {title}
//! {body}
//! <!-- /cascade:{slug} -->
//! ```
//!
//! ## Atomic write
//!
//! All writes use a temp-file + rename sequence to prevent partial writes.
//!
//! ## Constraints
//!
//! - All paths are resolved relative to `$HOME`; no absolute path escaping.
//! - Dry-run mode returns `InjectionOutcome::WouldInject` without modifying files.
//! - HOME-confined: callers supply a `home` path; the engine never reads `$HOME`
//!   directly from the environment — the Tauri command resolves it.
//!
//! ## SPORT
//!
//! MASTER-COMPONENTS.md — inject.rs module (T-P3-E07-07)

use std::fs;
use std::path::{Path, PathBuf};

use cascade_types::error::CascadeError;

use super::types::{LibraryItem, LibraryItemKind};

// ── Public types ──────────────────────────────────────────────────────────────

/// The harness to inject into.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum InjectionTarget {
    /// Claude Code — `~/.claude/`
    CC,
    /// OpenCode — `~/.config/opencode/`
    OC,
    /// Codex (OpenAI CLI) — `~/.codex/`
    Codex,
}

impl InjectionTarget {
    /// Parse from the lowercase string used in the IPC layer.
    pub fn from_ipc_str(s: &str) -> Option<Self> {
        match s {
            "cc" => Some(Self::CC),
            "oc" => Some(Self::OC),
            "codex" => Some(Self::Codex),
            _ => None,
        }
    }
}

/// Result of an injection attempt.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum InjectionOutcome {
    /// Block was appended (or file was created) successfully.
    Injected,
    /// Idempotency marker already present — nothing was written.
    AlreadyPresent,
    /// Dry-run: would have injected but did not write.
    WouldInject,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Inject `item` into the config for `target`, resolving all paths relative
/// to `home`.
///
/// # Errors
///
/// Returns [`CascadeError::Io`] on filesystem failures and
/// [`CascadeError::ConfigParse`] on unexpected path construction errors.
pub fn inject_item(
    item: &LibraryItem,
    target: InjectionTarget,
    home: &Path,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    match item.item_type {
        LibraryItemKind::Agent => inject_agent(item, target, home, dry_run),
        LibraryItemKind::SlashCommand => inject_slash_command(item, target, home, dry_run),
        LibraryItemKind::Persona | LibraryItemKind::Prompt => {
            inject_into_main_file(item, target, home, dry_run)
        }
    }
}

// ── Per-kind injection strategies ────────────────────────────────────────────

/// Inject an Agent item — writes a standalone `{slug}.md` in the harness agents dir.
fn inject_agent(
    item: &LibraryItem,
    target: InjectionTarget,
    home: &Path,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    let agents_dir = match target {
        InjectionTarget::CC => home.join(".claude").join("agents"),
        InjectionTarget::OC => home.join(".config").join("opencode").join("agents"),
        InjectionTarget::Codex => home.join(".codex").join("agents"),
    };

    let config_path = agents_dir.join(format!("{}.md", item.id));
    let block = format_block(item, "Agent");

    write_to_standalone_file(&item.id, &config_path, &block, dry_run)
}

/// Inject a SlashCommand item.
///
/// - CC: dedicated `~/.claude/commands/{slug}.md` file.
/// - OC: dedicated `~/.config/opencode/prompts/{slug}.md` file.
/// - Codex: appended to `~/.codex/AGENTS.md` (no dedicated dir yet).
fn inject_slash_command(
    item: &LibraryItem,
    target: InjectionTarget,
    home: &Path,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    match target {
        InjectionTarget::CC => {
            let commands_dir = home.join(".claude").join("commands");
            let config_path = commands_dir.join(format!("{}.md", item.id));
            let block = format_block(item, "Command");
            write_to_standalone_file(&item.id, &config_path, &block, dry_run)
        }
        InjectionTarget::OC => {
            let prompts_dir = home.join(".config").join("opencode").join("prompts");
            let config_path = prompts_dir.join(format!("{}.md", item.id));
            let block = format_block(item, "Command");
            write_to_standalone_file(&item.id, &config_path, &block, dry_run)
        }
        InjectionTarget::Codex => {
            let config_path = home.join(".codex").join("AGENTS.md");
            let block = format_block(item, "Command");
            append_to_file(&item.id, &config_path, &block, dry_run)
        }
    }
}

/// Inject Persona or Prompt items by appending to the harness main config file.
fn inject_into_main_file(
    item: &LibraryItem,
    target: InjectionTarget,
    home: &Path,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    let (config_path, header) = match (&item.item_type, target) {
        // CC: always CLAUDE.md for persona + prompt
        (_, InjectionTarget::CC) => {
            let p = home.join(".claude").join("CLAUDE.md");
            let h = match item.item_type {
                LibraryItemKind::Persona => "Persona",
                _ => "Prompt",
            };
            (p, h)
        }
        // OC: AGENTS.md for persona + prompt
        (_, InjectionTarget::OC) => {
            let p = home.join(".config").join("opencode").join("AGENTS.md");
            let h = match item.item_type {
                LibraryItemKind::Persona => "Persona",
                _ => "Prompt",
            };
            (p, h)
        }
        // Codex: AGENTS.md for persona + prompt
        (_, InjectionTarget::Codex) => {
            let p = home.join(".codex").join("AGENTS.md");
            let h = match item.item_type {
                LibraryItemKind::Persona => "Persona",
                _ => "Prompt",
            };
            (p, h)
        }
    };

    let block = format_block(item, header);
    append_to_file(&item.id, &config_path, &block, dry_run)
}

// ── File operations ───────────────────────────────────────────────────────────

/// Write `block` to `path` as a standalone file.
///
/// Idempotency: if `path` already exists and contains the cascade marker for
/// `slug`, returns `AlreadyPresent`.  Otherwise creates or overwrites the file.
fn write_to_standalone_file(
    slug: &str,
    path: &Path,
    block: &str,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    // Idempotency check
    if path.exists() {
        let existing = fs::read_to_string(path).map_err(|e| CascadeError::Io {
            path: path.to_path_buf(),
            operation: "read",
            source: e,
        })?;
        if existing.contains(&open_marker(slug)) {
            return Ok(InjectionOutcome::AlreadyPresent);
        }
    }

    if dry_run {
        return Ok(InjectionOutcome::WouldInject);
    }

    // Create parent dirs
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir_all",
            source: e,
        })?;
    }

    atomic_write(path, block)?;
    Ok(InjectionOutcome::Injected)
}

/// Append `block` to `path` (creating parent dirs + file if missing).
///
/// Idempotency: reads existing content first; skips append if the marker is
/// already present.
fn append_to_file(
    slug: &str,
    path: &Path,
    block: &str,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    // Read existing content (empty string if file doesn't exist yet)
    let existing = if path.exists() {
        fs::read_to_string(path).map_err(|e| CascadeError::Io {
            path: path.to_path_buf(),
            operation: "read",
            source: e,
        })?
    } else {
        String::new()
    };

    // Idempotency check
    if existing.contains(&open_marker(slug)) {
        return Ok(InjectionOutcome::AlreadyPresent);
    }

    if dry_run {
        return Ok(InjectionOutcome::WouldInject);
    }

    // Create parent dirs
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir_all",
            source: e,
        })?;
    }

    // Build new content: existing + separator + block
    let separator = if existing.is_empty() || existing.ends_with('\n') {
        "\n"
    } else {
        "\n\n"
    };
    let new_content = format!("{existing}{separator}{block}\n");

    atomic_write(path, &new_content)?;
    Ok(InjectionOutcome::Injected)
}

// ── Eject (remove) ────────────────────────────────────────────────────────────

/// Remove a previously injected block from a file.
///
/// Finds the region between `<!-- cascade:{slug} -->` and
/// `<!-- /cascade:{slug} -->` (inclusive) and removes it.  If the block is not
/// found, returns `Ok(InjectionOutcome::AlreadyPresent)` (idempotent eject).
///
/// For standalone files (agent / slash-command in CC/OC), the caller should
/// simply delete the file; this function handles the append-to-main-file case.
pub fn eject_item(
    slug: &str,
    path: &Path,
    dry_run: bool,
) -> Result<InjectionOutcome, CascadeError> {
    if !path.exists() {
        // Nothing to eject
        return Ok(InjectionOutcome::AlreadyPresent);
    }

    let content = fs::read_to_string(path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "read",
        source: e,
    })?;

    let open = open_marker(slug);
    let close = close_marker(slug);

    let start = match content.find(&open) {
        Some(i) => i,
        None => return Ok(InjectionOutcome::AlreadyPresent),
    };

    let end = match content.find(&close) {
        Some(i) => i + close.len(),
        None => {
            // Malformed — open marker without close; remove from open to EOF
            content.len()
        }
    };

    if dry_run {
        return Ok(InjectionOutcome::WouldInject);
    }

    // Trim surrounding blank lines to keep the file clean
    let before = content[..start].trim_end_matches('\n');
    let after = content[end..].trim_start_matches('\n');

    let new_content = if before.is_empty() && after.is_empty() {
        String::new()
    } else if before.is_empty() {
        format!("{after}\n")
    } else if after.is_empty() {
        format!("{before}\n")
    } else {
        format!("{before}\n\n{after}\n")
    };

    atomic_write(path, &new_content)?;
    Ok(InjectionOutcome::Injected)
}

// ── Marker helpers ────────────────────────────────────────────────────────────

fn open_marker(slug: &str) -> String {
    format!("<!-- cascade:{slug} -->")
}

fn close_marker(slug: &str) -> String {
    format!("<!-- /cascade:{slug} -->")
}

/// Format a complete injection block with markers.
fn format_block(item: &LibraryItem, header: &str) -> String {
    let slug = &item.id;
    let title = &item.title;

    // For persona, prefer system_prompt from metadata if present; fall back to content
    let body = if item.item_type == LibraryItemKind::Persona {
        item.metadata
            .get("system_prompt")
            .and_then(|v| v.as_str())
            .unwrap_or(&item.content)
    } else {
        &item.content
    };

    format!(
        "{open}\n## {header}: {title}\n\n{body}\n{close}",
        open = open_marker(slug),
        close = close_marker(slug),
    )
}

// ── Atomic write ──────────────────────────────────────────────────────────────

/// Write `content` to `path` atomically via a temp file + rename.
fn atomic_write(path: &Path, content: &str) -> Result<(), CascadeError> {
    let tmp_path = tmp_path_for(path);

    fs::write(&tmp_path, content).map_err(|e| CascadeError::Io {
        path: tmp_path.clone(),
        operation: "write_tmp",
        source: e,
    })?;

    fs::rename(&tmp_path, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename",
        source: e,
    })?;

    Ok(())
}

fn tmp_path_for(path: &Path) -> PathBuf {
    let mut s = path.as_os_str().to_owned();
    s.push(".cascade-inject.tmp");
    PathBuf::from(s)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    fn persona_item(slug: &str) -> LibraryItem {
        LibraryItem {
            id: slug.to_string(),
            item_type: LibraryItemKind::Persona,
            title: format!("Test Persona {slug}"),
            description: "A test persona".to_string(),
            content: "You are a helpful assistant.".to_string(),
            tags: vec![],
            version: "1.0.0".to_string(),
            created_at: "2026-06-10T00:00:00Z".to_string(),
            updated_at: "2026-06-10T00:00:00Z".to_string(),
            harness_targets: vec!["cc".to_string()],
            metadata: Default::default(),
        }
    }

    fn prompt_item(slug: &str) -> LibraryItem {
        LibraryItem {
            id: slug.to_string(),
            item_type: LibraryItemKind::Prompt,
            title: format!("Test Prompt {slug}"),
            description: "A test prompt".to_string(),
            content: "Summarise this document in 3 bullets.".to_string(),
            tags: vec![],
            version: "1.0.0".to_string(),
            created_at: "2026-06-10T00:00:00Z".to_string(),
            updated_at: "2026-06-10T00:00:00Z".to_string(),
            harness_targets: vec!["cc".to_string()],
            metadata: Default::default(),
        }
    }

    fn slash_command_item(slug: &str) -> LibraryItem {
        LibraryItem {
            id: slug.to_string(),
            item_type: LibraryItemKind::SlashCommand,
            title: format!("Test Command /{slug}"),
            description: "A test slash command".to_string(),
            content: "Run tests and report failures.".to_string(),
            tags: vec![],
            version: "1.0.0".to_string(),
            created_at: "2026-06-10T00:00:00Z".to_string(),
            updated_at: "2026-06-10T00:00:00Z".to_string(),
            harness_targets: vec!["cc".to_string(), "oc".to_string()],
            metadata: Default::default(),
        }
    }

    fn agent_item(slug: &str) -> LibraryItem {
        LibraryItem {
            id: slug.to_string(),
            item_type: LibraryItemKind::Agent,
            title: format!("Test Agent {slug}"),
            description: "A test agent".to_string(),
            content: "You are a code-review agent.".to_string(),
            tags: vec![],
            version: "1.0.0".to_string(),
            created_at: "2026-06-10T00:00:00Z".to_string(),
            updated_at: "2026-06-10T00:00:00Z".to_string(),
            harness_targets: vec!["cc".to_string()],
            metadata: Default::default(),
        }
    }

    // ── CC injection ─────────────────────────────────────────────────────────

    /// Injecting a persona into CC appends the block to ~/.claude/CLAUDE.md
    #[test]
    #[serial(global_env)]
    fn cc_persona_inject_appends_to_claude_md() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        // Pre-create ~/.claude/CLAUDE.md with some existing content
        let claude_md = home.join(".claude").join("CLAUDE.md");
        fs::create_dir_all(claude_md.parent().unwrap()).unwrap();
        fs::write(&claude_md, "# Existing CLAUDE.md\n").unwrap();

        let item = persona_item("test-persona");
        let result = inject_item(&item, InjectionTarget::CC, home, false).unwrap();

        assert_eq!(result, InjectionOutcome::Injected);

        let content = fs::read_to_string(&claude_md).unwrap();
        assert!(
            content.contains("<!-- cascade:test-persona -->"),
            "open marker missing"
        );
        assert!(
            content.contains("<!-- /cascade:test-persona -->"),
            "close marker missing"
        );
        assert!(
            content.contains("## Persona: Test Persona test-persona"),
            "header missing"
        );
        assert!(
            content.contains("You are a helpful assistant."),
            "body missing"
        );
        // Existing content preserved
        assert!(
            content.contains("# Existing CLAUDE.md"),
            "existing content lost"
        );
    }

    /// Injecting a prompt into CC appends the block to ~/.claude/CLAUDE.md
    #[test]
    #[serial(global_env)]
    fn cc_prompt_inject_appends_to_claude_md() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = prompt_item("sum-doc");
        let result = inject_item(&item, InjectionTarget::CC, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let claude_md = home.join(".claude").join("CLAUDE.md");
        let content = fs::read_to_string(&claude_md).unwrap();
        assert!(content.contains("## Prompt: Test Prompt sum-doc"));
    }

    /// Injecting a slash-command into CC creates ~/.claude/commands/{slug}.md
    #[test]
    #[serial(global_env)]
    fn cc_slash_command_inject_creates_command_file() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = slash_command_item("run-tests");
        let result = inject_item(&item, InjectionTarget::CC, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let cmd_file = home.join(".claude").join("commands").join("run-tests.md");
        assert!(cmd_file.exists(), "command file not created");
        let content = fs::read_to_string(&cmd_file).unwrap();
        assert!(content.contains("<!-- cascade:run-tests -->"));
        assert!(content.contains("## Command: Test Command /run-tests"));
    }

    /// Injecting an agent into CC creates ~/.claude/agents/{slug}.md
    #[test]
    #[serial(global_env)]
    fn cc_agent_inject_creates_agent_file() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = agent_item("code-reviewer");
        let result = inject_item(&item, InjectionTarget::CC, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let agent_file = home.join(".claude").join("agents").join("code-reviewer.md");
        assert!(agent_file.exists(), "agent file not created");
        let content = fs::read_to_string(&agent_file).unwrap();
        assert!(content.contains("## Agent: Test Agent code-reviewer"));
    }

    // ── OC injection ─────────────────────────────────────────────────────────

    /// Injecting a persona into OC appends to ~/.config/opencode/AGENTS.md
    #[test]
    #[serial(global_env)]
    fn oc_persona_inject_appends_to_agents_md() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = persona_item("oc-persona");
        let result = inject_item(&item, InjectionTarget::OC, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let agents_md = home.join(".config").join("opencode").join("AGENTS.md");
        assert!(agents_md.exists());
        let content = fs::read_to_string(&agents_md).unwrap();
        assert!(content.contains("<!-- cascade:oc-persona -->"));
        assert!(content.contains("## Persona: Test Persona oc-persona"));
    }

    /// Injecting a slash-command into OC creates ~/.config/opencode/prompts/{slug}.md
    #[test]
    #[serial(global_env)]
    fn oc_slash_command_inject_creates_prompt_file() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = slash_command_item("oc-cmd");
        let result = inject_item(&item, InjectionTarget::OC, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let prompt_file = home
            .join(".config")
            .join("opencode")
            .join("prompts")
            .join("oc-cmd.md");
        assert!(prompt_file.exists());
        let content = fs::read_to_string(&prompt_file).unwrap();
        assert!(content.contains("<!-- cascade:oc-cmd -->"));
    }

    /// Injecting an agent into OC creates ~/.config/opencode/agents/{slug}.md
    #[test]
    #[serial(global_env)]
    fn oc_agent_inject_creates_agent_file() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = agent_item("oc-reviewer");
        let result = inject_item(&item, InjectionTarget::OC, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let agent_file = home
            .join(".config")
            .join("opencode")
            .join("agents")
            .join("oc-reviewer.md");
        assert!(agent_file.exists());
    }

    // ── Codex injection ───────────────────────────────────────────────────────

    /// Injecting a persona into Codex appends to ~/.codex/AGENTS.md
    #[test]
    #[serial(global_env)]
    fn codex_persona_inject_appends_to_agents_md() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = persona_item("codex-persona");
        let result = inject_item(&item, InjectionTarget::Codex, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let agents_md = home.join(".codex").join("AGENTS.md");
        assert!(agents_md.exists());
        let content = fs::read_to_string(&agents_md).unwrap();
        assert!(content.contains("<!-- cascade:codex-persona -->"));
    }

    /// Injecting an agent into Codex creates ~/.codex/agents/{slug}.md
    #[test]
    #[serial(global_env)]
    fn codex_agent_inject_creates_agent_file() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = agent_item("codex-reviewer");
        let result = inject_item(&item, InjectionTarget::Codex, home, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let agent_file = home.join(".codex").join("agents").join("codex-reviewer.md");
        assert!(agent_file.exists());
    }

    // ── Idempotency ───────────────────────────────────────────────────────────

    /// Injecting the same item twice returns AlreadyPresent on the second call.
    #[test]
    #[serial(global_env)]
    fn idempotent_inject_returns_already_present() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();
        let item = persona_item("idempotent-test");

        let r1 = inject_item(&item, InjectionTarget::CC, home, false).unwrap();
        let r2 = inject_item(&item, InjectionTarget::CC, home, false).unwrap();

        assert_eq!(r1, InjectionOutcome::Injected);
        assert_eq!(r2, InjectionOutcome::AlreadyPresent);

        // File should have exactly one occurrence of the marker
        let claude_md = home.join(".claude").join("CLAUDE.md");
        let content = fs::read_to_string(&claude_md).unwrap();
        let count = content.matches("<!-- cascade:idempotent-test -->").count();
        assert_eq!(count, 1, "marker appeared more than once");
    }

    /// Injecting two different items into the same file preserves both blocks.
    #[test]
    #[serial(global_env)]
    fn two_items_into_same_file_both_present() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let p = persona_item("persona-a");
        let q = prompt_item("prompt-b");

        inject_item(&p, InjectionTarget::CC, home, false).unwrap();
        inject_item(&q, InjectionTarget::CC, home, false).unwrap();

        let claude_md = home.join(".claude").join("CLAUDE.md");
        let content = fs::read_to_string(&claude_md).unwrap();
        assert!(content.contains("<!-- cascade:persona-a -->"));
        assert!(content.contains("<!-- cascade:prompt-b -->"));
    }

    // ── Eject ─────────────────────────────────────────────────────────────────

    /// Eject removes the injected block, leaving surrounding content intact.
    #[test]
    #[serial(global_env)]
    fn eject_removes_block_cleanly() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let claude_md = home.join(".claude").join("CLAUDE.md");
        fs::create_dir_all(claude_md.parent().unwrap()).unwrap();
        fs::write(&claude_md, "# Header\n").unwrap();

        let item = persona_item("eject-me");
        inject_item(&item, InjectionTarget::CC, home, false).unwrap();

        let before = fs::read_to_string(&claude_md).unwrap();
        assert!(before.contains("<!-- cascade:eject-me -->"));

        let result = eject_item("eject-me", &claude_md, false).unwrap();
        assert_eq!(result, InjectionOutcome::Injected);

        let after = fs::read_to_string(&claude_md).unwrap();
        assert!(
            !after.contains("<!-- cascade:eject-me -->"),
            "marker still present after eject"
        );
        assert!(after.contains("# Header"), "surrounding content lost");
    }

    /// Ejecting a non-present slug is idempotent.
    #[test]
    #[serial(global_env)]
    fn eject_nonexistent_slug_returns_already_present() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();
        let claude_md = home.join(".claude").join("CLAUDE.md");
        fs::create_dir_all(claude_md.parent().unwrap()).unwrap();
        fs::write(&claude_md, "# Content\n").unwrap();

        let result = eject_item("not-injected", &claude_md, false).unwrap();
        assert_eq!(result, InjectionOutcome::AlreadyPresent);
    }

    // ── Dry-run ───────────────────────────────────────────────────────────────

    /// Dry-run returns WouldInject and does not modify the filesystem.
    #[test]
    #[serial(global_env)]
    fn dry_run_does_not_write() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path();

        let item = persona_item("dry-run-persona");
        let result = inject_item(&item, InjectionTarget::CC, home, true).unwrap();
        assert_eq!(result, InjectionOutcome::WouldInject);

        let claude_md = home.join(".claude").join("CLAUDE.md");
        assert!(!claude_md.exists(), "dry-run must not create files");
    }

    // ── Marker integrity ──────────────────────────────────────────────────────

    /// Verify the formatted block has both open and close markers and the header.
    #[test]
    fn format_block_contains_markers_and_header() {
        let item = persona_item("marker-test");
        let block = format_block(&item, "Persona");
        assert!(
            block.starts_with("<!-- cascade:marker-test -->"),
            "block must start with open marker"
        );
        assert!(
            block.ends_with("<!-- /cascade:marker-test -->"),
            "block must end with close marker"
        );
        assert!(block.contains("## Persona: Test Persona marker-test"));
        assert!(block.contains("You are a helpful assistant."));
    }

    /// Verify that the block for a persona with a system_prompt metadata field
    /// uses the system_prompt value, not the content field.
    #[test]
    fn persona_with_system_prompt_metadata_uses_metadata_value() {
        let mut item = persona_item("sys-prompt-test");
        item.metadata.insert(
            "system_prompt".to_string(),
            serde_json::Value::String("Custom system prompt from metadata.".to_string()),
        );
        let block = format_block(&item, "Persona");
        assert!(block.contains("Custom system prompt from metadata."));
        assert!(!block.contains("You are a helpful assistant."));
    }
}
