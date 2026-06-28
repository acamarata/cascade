//! Agent TOML install — copy built-in agent definitions into the agents/ dir.
//!
//! Purpose: At init time, seed the agents/ subdirectory with cascade's built-in
//! agent definitions. Users may modify or delete these files; init never
//! overwrites existing files (hierarchy-overridable by design).
//!
//! Inputs:  target agents dir path.
//! Outputs: TOML files written to agents/; idempotent (skip if file exists).
//! Constraints:
//!   - Never overwrites a user-modified file (existence check only).
//!   - Agent TOMLs are embedded via include_str! at compile time.
//!
//! SPORT: frame-01

use std::fs;
use std::path::Path;

use cascade_types::error::{CascadeError, Result};

/// Built-in agent definitions embedded at compile time.
static AGENT_FILES: &[(&str, &str)] = &[
    ("code-reviewer.toml",    include_str!("../../../data/agents/code-reviewer.toml")),
    ("doc-writer.toml",       include_str!("../../../data/agents/doc-writer.toml")),
    ("drift-detector.toml",   include_str!("../../../data/agents/drift-detector.toml")),
    ("security-reviewer.toml", include_str!("../../../data/agents/security-reviewer.toml")),
];

/// Install built-in agent TOML files into `agents_dir`.
///
/// Idempotent: existing files (including user-modified agents) are skipped.
/// Missing parent directories are created automatically.
pub fn install_agents(agents_dir: &Path) -> Result<Vec<String>> {
    fs::create_dir_all(agents_dir).map_err(|e| CascadeError::Io {
        path: agents_dir.to_path_buf(),
        operation: "create agents_dir",
        source: e,
    })?;

    let mut installed = Vec::new();
    for (name, content) in AGENT_FILES {
        let dest = agents_dir.join(name);
        if dest.exists() {
            continue; // never overwrite user-modified files
        }
        fs::write(&dest, content).map_err(|e| CascadeError::Io {
            path: dest.clone(),
            operation: "write agent",
            source: e,
        })?;
        installed.push(name.to_string());
    }
    Ok(installed)
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn test_install_agents_copies_all_builtins() {
        let dir = TempDir::new().unwrap();
        let installed = install_agents(dir.path()).unwrap();
        assert_eq!(installed.len(), 4);
        for name in &[
            "code-reviewer.toml",
            "doc-writer.toml",
            "drift-detector.toml",
            "security-reviewer.toml",
        ] {
            assert!(dir.path().join(name).exists(), "{name} must be installed");
        }
    }

    #[test]
    fn test_install_agents_idempotent() {
        let dir = TempDir::new().unwrap();
        install_agents(dir.path()).unwrap();
        let installed2 = install_agents(dir.path()).unwrap();
        assert!(installed2.is_empty(), "second install must skip existing files");
    }

    #[test]
    fn test_install_agents_does_not_overwrite_user_file() {
        let dir = TempDir::new().unwrap();
        let f = dir.path().join("code-reviewer.toml");
        fs::write(&f, "# user custom agent").unwrap();
        install_agents(dir.path()).unwrap();
        let content = fs::read_to_string(&f).unwrap();
        assert_eq!(content, "# user custom agent");
    }
}
