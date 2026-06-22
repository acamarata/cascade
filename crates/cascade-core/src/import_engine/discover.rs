//! # discover — source tree walker for the import engine
//!
//! ## Purpose
//! Walk a source `.claude/`, `.opencode/`, or `.codex/` directory tree and
//! classify every file into a [`SourceKind`] category. Detect `@import` include
//! lines and pointer references so the tier mapper and coverage ledger can
//! account for every atomic instruction.
//!
//! ## Inputs
//! `source_root: &Path` — the root of the legacy harness directory to walk.
//!
//! ## Outputs
//! `Vec<DiscoveredFile>` — one entry per file, with classification and detected
//! imports/references.
//!
//! ## Constraints
//! - Does not cross symlinks into parent directories (loop guard).
//! - Skips hidden files except those explicitly expected (CLAUDE.md, etc.).
//! - Never errors on unreadable files — records them with `SourceKind::Unknown`.
//!
//! ## Errors
//! Returns `CascadeError::Io` only if the root directory itself cannot be read.
//!
//! ## Notes
//! Classification is heuristic-based on path prefix and content markers.

use std::path::{Path, PathBuf};

use cascade_types::error::{CascadeError, Result};
use serde::{Deserialize, Serialize};

// ── Source kind ───────────────────────────────────────────────────────────────

/// Classification of a discovered source file.
///
/// Used by the tier mapper and coverage ledger to decide how each file
/// is handled during migration.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum SourceKind {
    /// Root instruction file (e.g. `CLAUDE.md`, `GLOBAL.md`, `AGENTS.md`).
    InstructionRoot,
    /// Always-loaded behavioral rule (lives under `rules/`).
    AlwaysLoadedRule,
    /// On-demand reference or doctrine (lives under `references-rules/` or `references/`).
    OnDemandReference,
    /// Per-language or per-domain coding standard (lives under `standards/`).
    Standard,
    /// Reusable template file (lives under `templates/`).
    Template,
    /// Executable helper script (lives under `scripts/` or `bin/`).
    Script,
    /// Memory file: decisions, lessons, patterns, feedback (lives under `memory/`).
    Memory,
    /// Inbox message file (lives under `inbox/`).
    Inbox,
    /// Idea capture file (lives under `ideas/`).
    Idea,
    /// Task or planning file (lives under `tasks/` or `planning/` or `phases/`).
    Task,
    /// Site or project-group tier instruction (lives under `sites/` or a
    /// project subdirectory with its own `CLAUDE.md`).
    SiteTier,
    /// File whose kind could not be determined from path or content.
    Unknown,
}

// ── Discovered file ───────────────────────────────────────────────────────────

/// A single file discovered during the source tree walk.
///
/// Contains the file's absolute path, its classification, a list of detected
/// `@import` directives, and a list of pointer references (`-> path` patterns).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DiscoveredFile {
    /// Absolute path to the source file.
    pub path: PathBuf,
    /// Path relative to the source root (for display and tier mapping).
    pub relative_path: PathBuf,
    /// Heuristic classification of this file's role.
    pub kind: SourceKind,
    /// `@import` lines detected in the file content (resolved paths).
    pub imports: Vec<PathBuf>,
    /// `-> ref` pointer lines detected in the file content (raw strings).
    pub pointers: Vec<String>,
    /// Whether the file was readable. If false, content analysis was skipped.
    pub readable: bool,
    /// Raw UTF-8 content of the file (empty if not readable or binary).
    pub content: String,
}

// ── Walker ────────────────────────────────────────────────────────────────────

/// Walk `source_root` and classify every file.
///
/// # Errors
/// Returns [`CascadeError::Io`] if `source_root` cannot be read at all.
pub fn discover_source_tree(source_root: &Path) -> Result<Vec<DiscoveredFile>> {
    if !source_root.exists() {
        return Err(CascadeError::PathNotFound {
            path: source_root.to_path_buf(),
        });
    }

    let mut files = Vec::new();
    walk_dir(source_root, source_root, &mut files)?;
    Ok(files)
}

/// Recursive directory walker (skips symlinks that escape the root).
fn walk_dir(
    root: &Path,
    dir: &Path,
    files: &mut Vec<DiscoveredFile>,
) -> Result<()> {
    let entries = std::fs::read_dir(dir).map_err(|e| CascadeError::Io {
        path: dir.to_path_buf(),
        operation: "read directory",
        source: e,
    })?;

    for entry in entries {
        let entry = match entry {
            Ok(e) => e,
            Err(_) => continue,
        };
        let path = entry.path();

        // Skip git/OS metadata and harness-runtime directories that are not part
        // of the instruction cascade. A real `~/.claude` holds gigabytes of
        // session transcripts + per-project data under `projects/` (and other
        // runtime dirs) that are NOT importable global instruction content;
        // recursing into them makes import hang on tens of thousands of files.
        let name = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("");
        const SKIP_DIRS: &[&str] = &[
            ".git", ".DS_Store", "node_modules", "target",
            "projects", "todos", "shell-snapshots", "statsig", "ide",
            ".cache", "temp", "tmp", "logs", "backups", "downloads",
        ];
        if SKIP_DIRS.contains(&name) {
            continue;
        }

        // Symlink guard: only follow if target stays within or at root.
        if path.is_symlink() {
            if let Ok(target) = std::fs::canonicalize(&path) {
                if !target.starts_with(root) {
                    tracing::warn!(
                        path = %path.display(),
                        target = %target.display(),
                        "skipping symlink that escapes source root"
                    );
                    continue;
                }
            }
        }

        if path.is_dir() {
            walk_dir(root, &path, files)?;
        } else {
            // Skip non-instruction files: session transcripts (`.jsonl`), logs,
            // lockfiles, binaries, and anything larger than 1 MiB. A real
            // `~/.claude` can hold gigabytes of session transcripts that are not
            // instruction-cascade content; reading them all makes import hang.
            if is_noise_file(&path, name) {
                continue;
            }
            let relative_path = path
                .strip_prefix(root)
                .unwrap_or(&path)
                .to_path_buf();
            let discovered = classify_file(root, &path, &relative_path);
            files.push(discovered);
        }
    }

    Ok(())
}

/// True for files that are not part of an instruction cascade and should be
/// skipped during import discovery: session transcripts (`.jsonl`), logs,
/// lockfiles, archives, media, binaries, or anything larger than 1 MiB
/// (instruction files are small). Keeps import fast and focused on real
/// instruction content (CLAUDE.md, rules, references, memory, etc.).
fn is_noise_file(path: &Path, name: &str) -> bool {
    const SKIP_EXT: &[&str] = &[
        "jsonl", "log", "lock", "tmp", "bak", "zip", "gz", "tgz", "tar", "png",
        "jpg", "jpeg", "gif", "webp", "ico", "icns", "pdf", "wasm", "bin", "db",
        "sqlite", "sqlite3", "so", "dylib", "node",
    ];
    if let Some(ext) = name.rsplit('.').next() {
        if SKIP_EXT.contains(&ext.to_ascii_lowercase().as_str()) {
            return true;
        }
    }
    // Oversized files are not instruction content (cap 1 MiB).
    matches!(std::fs::metadata(path), Ok(m) if m.len() > 1_048_576)
}

/// Read a file and classify it into a [`SourceKind`].
fn classify_file(root: &Path, path: &Path, relative: &Path) -> DiscoveredFile {
    let (content, readable) = match std::fs::read_to_string(path) {
        Ok(c) => (c, true),
        Err(_) => (String::new(), false),
    };

    let kind = classify_kind(relative, &content);

    let (imports, pointers) = if readable {
        parse_references(root, path, &content)
    } else {
        (Vec::new(), Vec::new())
    };

    DiscoveredFile {
        path: path.to_path_buf(),
        relative_path: relative.to_path_buf(),
        kind,
        imports,
        pointers,
        readable,
        content,
    }
}

/// Classify a file based on its path relative to the source root.
fn classify_kind(relative: &Path, content: &str) -> SourceKind {
    let parts: Vec<&str> = relative
        .components()
        .filter_map(|c| c.as_os_str().to_str())
        .collect();

    let file_name = relative
        .file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("");

    // Root-level instruction files.
    if parts.len() == 1 {
        let name_lower = file_name.to_lowercase();
        if name_lower == "claude.md"
            || name_lower == "global.md"
            || name_lower == "agents.md"
            || name_lower == "cascade.md"
            || name_lower == "rtk.md"
        {
            return SourceKind::InstructionRoot;
        }
    }

    let dir0 = parts.first().copied().unwrap_or("");
    let dir1 = parts.get(1).copied().unwrap_or("");

    match dir0 {
        "rules" => SourceKind::AlwaysLoadedRule,
        "references-rules" => SourceKind::OnDemandReference,
        "references" => SourceKind::OnDemandReference,
        "standards" => SourceKind::Standard,
        "templates" => SourceKind::Template,
        "scripts" | "bin" => SourceKind::Script,
        "memory" => SourceKind::Memory,
        "inbox" => SourceKind::Inbox,
        "ideas" => SourceKind::Idea,
        "tasks" | "planning" | "phases" => SourceKind::Task,
        "sites" => SourceKind::SiteTier,
        "global" => {
            // global/CLAUDE.md, global/GLOBAL.md, etc.
            let name_lower = file_name.to_lowercase();
            if name_lower == "claude.md"
                || name_lower == "global.md"
                || name_lower == "agents.md"
                || name_lower == "rtk.md"
            {
                SourceKind::InstructionRoot
            } else {
                SourceKind::OnDemandReference
            }
        }
        _ => {
            // Heuristic: if inside a sub-dir named after a project that has its
            // own CLAUDE.md / GLOBAL.md, treat as SiteTier.
            if dir1 == "claude.md" || dir1 == "global.md" || dir1 == "cascade.md" {
                return SourceKind::SiteTier;
            }
            // Fall back to content-based heuristics.
            classify_by_content(file_name, content)
        }
    }
}

/// Secondary classification using file name and content patterns.
fn classify_by_content(file_name: &str, content: &str) -> SourceKind {
    let name_lower = file_name.to_lowercase();
    if name_lower == "manifest.md" || name_lower == "readme.md" {
        return SourceKind::OnDemandReference;
    }
    // Content markers.
    let content_lower = content.to_lowercase();
    if content_lower.contains("# hard rule")
        || content_lower.contains("## deny patterns")
        || content_lower.contains("## the rule")
    {
        return SourceKind::AlwaysLoadedRule;
    }
    if content_lower.contains("# instructions\n")
        || content_lower.contains("## operating posture")
        || content_lower.contains("## instruction cascade")
    {
        return SourceKind::InstructionRoot;
    }
    SourceKind::Unknown
}

/// Parse `@import` directives and `-> ref` pointer lines from file content.
fn parse_references(
    root: &Path,
    file_path: &Path,
    content: &str,
) -> (Vec<PathBuf>, Vec<String>) {
    let base = file_path.parent().unwrap_or(root);
    let mut imports = Vec::new();
    let mut pointers = Vec::new();

    for line in content.lines() {
        let trimmed = line.trim();

        // @import directives: lines starting with `@` followed by a path.
        if let Some(rest) = trimmed.strip_prefix('@') {
            let candidate = rest.split_whitespace().next().unwrap_or("").trim();
            if !candidate.is_empty() && !candidate.contains("://") {
                let resolved = base.join(candidate);
                if resolved.exists() {
                    imports.push(resolved);
                }
            }
        }

        // Pointer references: `-> path` or `-> path (...)`.
        if let Some(rest) = trimmed.strip_prefix("-> ") {
            // Extract the path portion (up to first space or parenthesis).
            let path_part = rest
                .split([' ', '(', '\n'])
                .next()
                .unwrap_or("")
                .trim();
            if !path_part.is_empty() {
                pointers.push(path_part.to_string());
            }
        }
    }

    (imports, pointers)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn make_file(dir: &Path, name: &str, content: &str) {
        fs::write(dir.join(name), content).unwrap();
    }

    fn make_dir(parent: &Path, name: &str) -> PathBuf {
        let d = parent.join(name);
        fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn classifies_root_claude_md() {
        let tmp = TempDir::new().unwrap();
        make_file(tmp.path(), "CLAUDE.md", "# Global Instructions\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        let f = files.iter().find(|f| f.relative_path == *"CLAUDE.md").unwrap();
        assert_eq!(f.kind, SourceKind::InstructionRoot);
    }

    #[test]
    fn classifies_rules_dir() {
        let tmp = TempDir::new().unwrap();
        let rules_dir = make_dir(tmp.path(), "rules");
        make_file(&rules_dir, "destructive-deny-list.md", "# Hard Rule\n## Deny patterns\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        let f = files.iter().find(|f| f.relative_path.starts_with("rules")).unwrap();
        assert_eq!(f.kind, SourceKind::AlwaysLoadedRule);
    }

    #[test]
    fn classifies_standards_dir() {
        let tmp = TempDir::new().unwrap();
        let std_dir = make_dir(tmp.path(), "standards");
        make_file(&std_dir, "rust.md", "# Rust Standard\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        let f = files.iter().find(|f| f.relative_path.starts_with("standards")).unwrap();
        assert_eq!(f.kind, SourceKind::Standard);
    }

    #[test]
    fn classifies_templates_dir() {
        let tmp = TempDir::new().unwrap();
        let t_dir = make_dir(tmp.path(), "templates");
        make_file(&t_dir, "PPI-template.md", "# PPI Template\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        let f = files.iter().find(|f| f.relative_path.starts_with("templates")).unwrap();
        assert_eq!(f.kind, SourceKind::Template);
    }

    #[test]
    fn classifies_scripts_dir() {
        let tmp = TempDir::new().unwrap();
        let s_dir = make_dir(tmp.path(), "scripts");
        make_file(&s_dir, "claude-recall", "#!/usr/bin/env python3\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        let f = files.iter().find(|f| f.relative_path.starts_with("scripts")).unwrap();
        assert_eq!(f.kind, SourceKind::Script);
    }

    #[test]
    fn detects_at_imports() {
        let tmp = TempDir::new().unwrap();
        make_file(tmp.path(), "RTK.md", "# RTK\n");
        make_file(tmp.path(), "CLAUDE.md", "# Global\n@RTK.md\n-> some/path.md\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        let f = files.iter().find(|f| f.relative_path == *"CLAUDE.md").unwrap();
        assert!(!f.imports.is_empty(), "should detect @RTK.md import");
        assert!(!f.pointers.is_empty(), "should detect -> pointer");
    }

    #[test]
    fn missing_root_returns_error() {
        let result = discover_source_tree(Path::new("/tmp/cascade-import-engine-nonexistent-123"));
        assert!(result.is_err());
    }

    #[test]
    fn skips_git_dir() {
        let tmp = TempDir::new().unwrap();
        let git = make_dir(tmp.path(), ".git");
        make_file(&git, "config", "[core]\n");
        make_file(tmp.path(), "CLAUDE.md", "# Instructions\n");
        let files = discover_source_tree(tmp.path()).unwrap();
        assert!(!files.iter().any(|f| f.relative_path.starts_with(".git")));
    }
}
