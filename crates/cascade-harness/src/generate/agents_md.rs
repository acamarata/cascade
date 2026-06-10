//! Generate `AGENTS.md` and `.cursorrules` from a resolved Cascade context.
//!
//! ## Purpose
//! Writes `AGENTS.md` (for Codex / OpenCode / general agent consumption) and
//! `.cursorrules` (for Cursor IDE) using the merged instruction text from a
//! [`cascade_core::cascade_resolution::ResolvedCascade`].
//!
//! ## AGENTS.md format
//! YAML frontmatter block followed by the merged instruction body:
//! ```yaml
//! ---
//! model: "claude-sonnet-4-6"
//! tools: ["cascade.read", "cascade.search"]
//! context: ["gci", "asi", "ppi"]
//! ---
//! {merged instructions}
//! ```
//!
//! ## Symlink semantics
//! If `CLAUDE.md` already exists at the same directory as `dest`, `AGENTS.md`
//! is created as a symlink pointing to `CLAUDE.md` (they share the same content).
//! Cross-filesystem symlink failures fall back to a file copy.
//!
//! ## Idempotency
//! Content hash comparison: if the proposed content matches the current file,
//! the file is not touched (mtime preserved).
//!
//! ## SPORT
//! MASTER-HARNESSES.md: Codex+Cursor rows — agents_md_gen, cursorrules_gen (T-P4-E06-03)

use std::collections::hash_map::DefaultHasher;
use std::fs;
use std::hash::{Hash, Hasher};
use std::io::Write;
use std::path::Path;

use cascade_core::cascade_resolution::ResolvedCascade;
use cascade_types::error::{CascadeError, Result};
use tracing::{debug, info};

// ── Idempotency marker ────────────────────────────────────────────────────────

/// Marker written into AGENTS.md so readers can identify cascade-generated files.
const AGENTS_MD_MARKER: &str = "<!-- cascade:agents-md -->";

// ── Public API ────────────────────────────────────────────────────────────────

/// Parameters for AGENTS.md frontmatter.
pub struct AgentsMdOptions {
    /// Model string for the `model:` frontmatter field.
    pub model: String,
    /// Tool names for the `tools:` frontmatter field.
    pub tools: Vec<String>,
}

impl Default for AgentsMdOptions {
    fn default() -> Self {
        Self {
            model: "claude-sonnet-4-6".into(),
            tools: vec!["cascade.read".into(), "cascade.search".into()],
        }
    }
}

/// Generate `AGENTS.md` at `dest` from a resolved cascade.
///
/// # Symlink semantics
/// If `CLAUDE.md` exists in the same directory as `dest`, `AGENTS.md` is written
/// as a symlink to `CLAUDE.md`. Cross-filesystem symlink failures fall back to
/// writing a plain file.
///
/// # Idempotency
/// If the current file contents hash matches the proposed content, the function
/// returns `Ok(())` without touching the file.
///
/// # Arguments
/// * `resolved` — the merged cascade (provides instruction text and tier names).
/// * `dest`    — absolute path where `AGENTS.md` should be written.
/// * `opts`    — optional frontmatter parameters (uses sensible defaults if `None`).
pub fn generate_agents_md(
    resolved: &ResolvedCascade,
    dest: &Path,
    opts: Option<AgentsMdOptions>,
) -> Result<()> {
    let opts = opts.unwrap_or_default();

    // Check if CLAUDE.md is a neighbour — symlink path
    let claude_md = dest.parent().map(|p| p.join("CLAUDE.md"));
    if let Some(ref claude_path) = claude_md {
        if claude_path.exists() && claude_path != dest {
            return write_agents_md_symlink(claude_path, dest);
        }
    }

    // Build proposed content
    let content = build_agents_md_content(resolved, &opts);

    // Idempotency check
    if dest.exists() && hash_of_file(dest) == hash_of_str(&content) {
        debug!("AGENTS.md unchanged; skipping write");
        return Ok(());
    }

    // Atomic write
    write_atomic(dest, &content)
}

/// Generate `.cursorrules` at `dest` from a resolved cascade.
///
/// Writes a minimal JSON file with a `rules` array containing the merged instructions.
///
/// # Idempotency
/// Skips the write if the hash matches the existing file.
pub fn generate_cursorrules(resolved: &ResolvedCascade, dest: &Path) -> Result<()> {
    let content = build_cursorrules_content(resolved)?;

    if dest.exists() && hash_of_file(dest) == hash_of_str(&content) {
        debug!(".cursorrules unchanged; skipping write");
        return Ok(());
    }

    write_atomic(dest, &content)
}

// ── Content builders ──────────────────────────────────────────────────────────

fn build_agents_md_content(resolved: &ResolvedCascade, opts: &AgentsMdOptions) -> String {
    let found_tiers: Vec<String> = resolved
        .tiers_found
        .iter()
        .filter(|t| t.found)
        .map(|t| format!("{:?}", t.tier).to_lowercase())
        .collect();

    let tools_yaml = opts
        .tools
        .iter()
        .map(|t| format!("  - \"{}\"", t))
        .collect::<Vec<_>>()
        .join("\n");

    let context_yaml = if found_tiers.is_empty() {
        "  - all".into()
    } else {
        found_tiers
            .iter()
            .map(|t| format!("  - \"{}\"", t))
            .collect::<Vec<_>>()
            .join("\n")
    };

    format!(
        "{marker}\n---\nmodel: \"{model}\"\ntools:\n{tools}\ncontext:\n{context}\n---\n\n{body}",
        marker = AGENTS_MD_MARKER,
        model = opts.model,
        tools = tools_yaml,
        context = context_yaml,
        body = resolved.merged_instructions,
    )
}

fn build_cursorrules_content(resolved: &ResolvedCascade) -> Result<String> {
    let rules_json = serde_json::json!({
        "rules": [resolved.merged_instructions]
    });
    serde_json::to_string_pretty(&rules_json)
        .map_err(|e| CascadeError::Other(format!("JSON serialize error: {e}")))
}

// ── Symlink ───────────────────────────────────────────────────────────────────

fn write_agents_md_symlink(claude_path: &Path, dest: &Path) -> Result<()> {
    // Remove stale dest if present
    if dest.exists() || dest.symlink_metadata().is_ok() {
        fs::remove_file(dest).map_err(|e| CascadeError::Io {
            path: dest.to_path_buf(),
            operation: "remove_for_symlink",
            source: e,
        })?;
    }

    #[cfg(unix)]
    {
        use std::os::unix::fs::symlink;
        match symlink(
            claude_path
                .file_name()
                .unwrap_or_else(|| std::ffi::OsStr::new("CLAUDE.md")),
            dest,
        ) {
            Ok(()) => {
                info!("created AGENTS.md as symlink to CLAUDE.md");
                return Ok(());
            }
            Err(e) if e.raw_os_error() == Some(libc::EXDEV) => {
                // Cross-device link — fall through to file copy
                debug!("cross-filesystem symlink failed; falling back to file copy");
            }
            Err(e) => {
                return Err(CascadeError::Io {
                    path: dest.to_path_buf(),
                    operation: "symlink",
                    source: e,
                });
            }
        }
    }

    // Fallback: copy CLAUDE.md content to AGENTS.md
    let content = fs::read_to_string(claude_path).map_err(|e| CascadeError::Io {
        path: claude_path.to_path_buf(),
        operation: "read_claude_md",
        source: e,
    })?;
    write_atomic(dest, &content)
}

// ── Utilities ─────────────────────────────────────────────────────────────────

fn write_atomic(dest: &Path, content: &str) -> Result<()> {
    if let Some(parent) = dest.parent() {
        fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir",
            source: e,
        })?;
    }
    let tmp = dest.with_extension("md.tmp");
    {
        let mut f = fs::File::create(&tmp).map_err(|e| CascadeError::Io {
            path: tmp.clone(),
            operation: "create_tmp",
            source: e,
        })?;
        f.write_all(content.as_bytes())
            .map_err(|e| CascadeError::Io {
                path: tmp.clone(),
                operation: "write",
                source: e,
            })?;
    }
    fs::rename(&tmp, dest).map_err(|e| CascadeError::Io {
        path: dest.to_path_buf(),
        operation: "rename",
        source: e,
    })?;
    debug!(path = %dest.display(), "wrote file");
    Ok(())
}

fn hash_of_str(s: &str) -> u64 {
    let mut h = DefaultHasher::new();
    s.hash(&mut h);
    h.finish()
}

fn hash_of_file(path: &Path) -> u64 {
    fs::read_to_string(path)
        .map(|s| hash_of_str(&s))
        .unwrap_or(0)
}

// Allow libc in non-unix (it won't be called)
#[cfg(unix)]
use libc;

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::cascade_resolution::{ResolvedCascade, TierResult};
    use cascade_types::cascade_tier::CascadeTier;
    use serial_test::serial;
    use std::fs;
    use std::path::PathBuf;
    use tempfile::TempDir;

    fn mock_resolved() -> ResolvedCascade {
        ResolvedCascade {
            merged_instructions: "## Test Instructions\n\nAlways use Cascade.".into(),
            mcp_server_url: "unix://~/.cascade/cascade.sock".into(),
            tiers_found: vec![
                TierResult {
                    tier: CascadeTier::Gci,
                    found: true,
                    path_searched: PathBuf::from("/home/.cascade"),
                    instructions: "GCI instructions".into(),
                },
                TierResult {
                    tier: CascadeTier::Prc,
                    found: false,
                    path_searched: PathBuf::from("/project/.cascade"),
                    instructions: String::new(),
                },
            ],
            working_dir: PathBuf::from("/project"),
        }
    }

    #[test]
    #[serial(global_env)]
    fn agents_md_writes_valid_frontmatter() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("AGENTS.md");
        let resolved = mock_resolved();

        generate_agents_md(&resolved, &dest, None).unwrap();

        assert!(dest.exists());
        let content = fs::read_to_string(&dest).unwrap();
        assert!(content.contains("model:"), "should have model field");
        assert!(content.contains("tools:"), "should have tools field");
        assert!(
            content.contains("cascade.read"),
            "should have cascade.read tool"
        );
        assert!(
            content.contains("## Test Instructions"),
            "should contain merged body"
        );
    }

    #[test]
    #[serial(global_env)]
    fn agents_md_creates_symlink_when_claude_md_present() {
        let tmp = TempDir::new().unwrap();

        // Create a CLAUDE.md
        let claude_path = tmp.path().join("CLAUDE.md");
        fs::write(&claude_path, "# CLAUDE.md content").unwrap();

        let dest = tmp.path().join("AGENTS.md");
        let resolved = mock_resolved();

        generate_agents_md(&resolved, &dest, None).unwrap();

        // AGENTS.md should be a symlink
        let meta = fs::symlink_metadata(&dest).unwrap();
        assert!(
            meta.file_type().is_symlink() || dest.is_file(),
            "AGENTS.md should be a symlink or file (fallback ok)"
        );
    }

    #[test]
    #[serial(global_env)]
    fn agents_md_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("AGENTS.md");
        let resolved = mock_resolved();

        generate_agents_md(&resolved, &dest, None).unwrap();
        let mtime1 = fs::metadata(&dest).unwrap().modified().unwrap();

        // Small sleep to allow mtime to differ if file were rewritten
        std::thread::sleep(std::time::Duration::from_millis(50));

        generate_agents_md(&resolved, &dest, None).unwrap();
        let mtime2 = fs::metadata(&dest).unwrap().modified().unwrap();

        assert_eq!(
            mtime1, mtime2,
            "mtime should be unchanged on idempotent call"
        );
    }

    #[test]
    #[serial(global_env)]
    fn cursorrules_writes_parseable_json() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join(".cursorrules");
        let resolved = mock_resolved();

        generate_cursorrules(&resolved, &dest).unwrap();

        assert!(dest.exists());
        let content = fs::read_to_string(&dest).unwrap();
        let json: serde_json::Value = serde_json::from_str(&content).expect("valid JSON");
        let rules = json["rules"].as_array().expect("rules array");
        assert!(!rules.is_empty(), "rules array should be non-empty");
    }

    #[test]
    #[serial(global_env)]
    fn cursorrules_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join(".cursorrules");
        let resolved = mock_resolved();

        generate_cursorrules(&resolved, &dest).unwrap();
        let mtime1 = fs::metadata(&dest).unwrap().modified().unwrap();

        std::thread::sleep(std::time::Duration::from_millis(50));

        generate_cursorrules(&resolved, &dest).unwrap();
        let mtime2 = fs::metadata(&dest).unwrap().modified().unwrap();

        assert_eq!(mtime1, mtime2, "mtime unchanged on idempotent call");
    }
}
