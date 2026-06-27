//! Skill suite installation — copy skill markdown files from the data bundle
//! into the active cascade skills/ directory.
//!
//! Purpose: At init time, populate skills/ with the right skill set for the
//! user's workflow (pews for phased coding, personal for general use).
//!
//! Inputs:  `SkillSuite` enum, target skills dir path.
//! Outputs: Markdown files written to skills/; idempotent (skip if file exists).
//! Constraints:
//!   - Never overwrites a user-modified file (check existence only; skip if present).
//!   - Never panics; returns errors.
//!   - Skills are embedded via `include_str!` from data/skills/ at compile time.
//!
//! SPORT: frame-01

use std::fs;
use std::path::Path;

use cascade_types::error::{CascadeError, Result};

/// Which skill set to install into the skills/ directory.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SkillSuite {
    /// PEWS (Plan/Build/Eot/Eos/Eow/Eoe/Eop) — phased engineering workflow.
    Pews,
    /// Personal productivity skills (threads, topics, recall, archive).
    Personal,
}

impl SkillSuite {
    /// Return the (filename, content) pairs for this suite.
    pub fn files(self) -> &'static [(&'static str, &'static str)] {
        match self {
            SkillSuite::Pews => PEWS_FILES,
            SkillSuite::Personal => PERSONAL_FILES,
        }
    }
}

// Embedded skill files — compiled into the binary.
static PEWS_FILES: &[(&str, &str)] = &[
    ("plan.md",  include_str!("../../../data/skills/pews/plan.md")),
    ("build.md", include_str!("../../../data/skills/pews/build.md")),
    ("eot.md",   include_str!("../../../data/skills/pews/eot.md")),
    ("eos.md",   include_str!("../../../data/skills/pews/eos.md")),
    ("eow.md",   include_str!("../../../data/skills/pews/eow.md")),
    ("eoe.md",   include_str!("../../../data/skills/pews/eoe.md")),
    ("eop.md",   include_str!("../../../data/skills/pews/eop.md")),
];

static PERSONAL_FILES: &[(&str, &str)] = &[
    ("threads.md", include_str!("../../../data/skills/personal/threads.md")),
    ("topics.md",  include_str!("../../../data/skills/personal/topics.md")),
    ("recall.md",  include_str!("../../../data/skills/personal/recall.md")),
    ("archive.md", include_str!("../../../data/skills/personal/archive.md")),
];

/// Install skill files from the suite into `skills_dir`.
///
/// Idempotent: files that already exist are skipped (never overwritten).
/// Missing parent directories are created automatically.
pub fn install_suite(suite: SkillSuite, skills_dir: &Path) -> Result<Vec<String>> {
    fs::create_dir_all(skills_dir).map_err(|e| CascadeError::Io {
        path: skills_dir.to_path_buf(),
        operation: "create skills_dir",
        source: e,
    })?;

    let mut installed = Vec::new();
    for (name, content) in suite.files() {
        let dest = skills_dir.join(name);
        if dest.exists() {
            continue; // skip user-modified files
        }
        fs::write(&dest, content).map_err(|e| CascadeError::Io {
            path: dest.clone(),
            operation: "write skill",
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
    fn test_install_pews_copies_expected_files() {
        let dir = TempDir::new().unwrap();
        let installed = install_suite(SkillSuite::Pews, dir.path()).unwrap();
        assert_eq!(installed.len(), 7, "pews suite has 7 files");
        for name in &["plan.md", "build.md", "eot.md", "eos.md", "eow.md", "eoe.md", "eop.md"] {
            assert!(dir.path().join(name).exists(), "{name} must be installed");
        }
    }

    #[test]
    fn test_install_personal_copies_expected_files() {
        let dir = TempDir::new().unwrap();
        let installed = install_suite(SkillSuite::Personal, dir.path()).unwrap();
        assert_eq!(installed.len(), 4, "personal suite has 4 files");
    }

    #[test]
    fn test_install_idempotent() {
        let dir = TempDir::new().unwrap();
        install_suite(SkillSuite::Pews, dir.path()).unwrap();
        // Second call — nothing new installed (files already exist).
        let installed2 = install_suite(SkillSuite::Pews, dir.path()).unwrap();
        assert!(installed2.is_empty(), "second install must skip existing files");
    }

    #[test]
    fn test_install_does_not_overwrite_user_file() {
        let dir = TempDir::new().unwrap();
        let plan = dir.path().join("plan.md");
        fs::write(&plan, "# user-modified content").unwrap();
        install_suite(SkillSuite::Pews, dir.path()).unwrap();
        let content = fs::read_to_string(&plan).unwrap();
        assert_eq!(content, "# user-modified content", "user file must not be overwritten");
    }
}
