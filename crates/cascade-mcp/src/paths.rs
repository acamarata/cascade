//! Path helpers for cascade-mcp: resolve tier files, memory files, inbox dirs.
//!
//! These supplement cascade-types::paths with MCP-specific resolution logic.
//! All paths are derived from the user's home directory and follow the
//! standard `.claude/` layout documented in GCI.
//!
//! ## Cascade tier file resolution
//!
//! | Tier | Path |
//! |------|------|
//! | `gci` | `~/.claude/CLAUDE.md` |
//! | `asi` | `~/Sites/.claude/CLAUDE.md` |
//! | `ppc` | `~/Sites/{project}/.claude/CLAUDE.md` |
//! | `prc` | `~/Sites/{project}/{repo}/.claude/CLAUDE.md` (best-effort) |
//! | `pac` | falls back to `prc` |

use std::path::PathBuf;

use cascade_types::paths::home_dir;

// ── Tier file ─────────────────────────────────────────────────────────────────

/// Resolve the CASCADE.md / CLAUDE.md path for the named tier.
///
/// `tier` is one of `gci`, `asi`, `ppc`, `prc`, `pac`, or a free-form
/// project name (treated as `ppc`).
///
/// Returns an absolute path. The file may not exist; callers must handle
/// `io::Error` on read.
pub fn tier_file(tier: &str) -> PathBuf {
    let home = home_dir();
    match tier {
        "gci" => home.join(".claude").join("CLAUDE.md"),
        "asi" => home.join("Sites").join(".claude").join("CLAUDE.md"),
        name => home
            .join("Sites")
            .join(name)
            .join(".claude")
            .join("CLAUDE.md"),
    }
}

// ── Memory file ───────────────────────────────────────────────────────────────

/// Resolve a project memory file path.
///
/// `project` is the project name (e.g. `nself`, `acamarata`).
/// `file` is the memory filename (e.g. `decisions.md`).
///
/// Resolved to: `~/Sites/{project}/.claude/memory/{file}`
pub fn memory_file(project: &str, file: &str) -> PathBuf {
    home_dir()
        .join("Sites")
        .join(project)
        .join(".claude")
        .join("memory")
        .join(file)
}

// ── Inbox dir ─────────────────────────────────────────────────────────────────

/// Resolve a project's PCI inbox directory.
///
/// `project` is the project name. For `nclaw` / `downloads` the path is
/// `~/Downloads/.claude/inbox/`; all others are
/// `~/Sites/{project}/.claude/inbox/`.
pub fn inbox_dir(project: &str) -> PathBuf {
    match project {
        "nclaw" | "downloads" => home_dir().join("Downloads").join(".claude").join("inbox"),
        "*" | "all" => home_dir()
            .join("Sites")
            .join("nself")
            .join(".claude")
            .join("inbox"),
        name => home_dir()
            .join("Sites")
            .join(name)
            .join(".claude")
            .join("inbox"),
    }
}

// ── Docs / master lists ───────────────────────────────────────────────────────

/// Resolve a project docs file path (used for master lists).
///
/// `file` is e.g. `MASTER-ROUTES.md`.
/// Resolved to: `~/Sites/{project}/.claude/docs/{file}`
pub fn docs_file(project: &str, file: &str) -> PathBuf {
    home_dir()
        .join("Sites")
        .join(project)
        .join(".claude")
        .join("docs")
        .join(file)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn gci_tier_resolves_to_claude_dir() {
        let p = tier_file("gci");
        assert!(p.to_str().unwrap().contains(".claude/CLAUDE.md"));
    }

    #[test]
    fn memory_file_under_sites() {
        let p = memory_file("nself", "decisions.md");
        let s = p.to_str().unwrap();
        assert!(s.contains("nself"), "path should contain project name: {s}");
        assert!(
            s.ends_with("decisions.md"),
            "path should end with filename: {s}"
        );
    }

    #[test]
    fn inbox_dir_under_sites() {
        let p = inbox_dir("nself");
        let s = p.to_str().unwrap();
        assert!(
            s.contains("nself/.claude/inbox"),
            "unexpected inbox path: {s}"
        );
    }
}
