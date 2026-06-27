//! Tech-stack detector — marker-file-based, pure, synchronous.
//!
//! # Purpose
//!
//! Given a directory path, probe for well-known marker files and return a
//! [`TechStack`] describing the language, framework, and build tool detected.
//!
//! Reuses the same marker-file pattern established by the rag-13 project
//! classifier in `cascade-daemon/src/discovery/classifier.rs` but is
//! self-contained here so `cascade-rag` does not gain a daemon dependency.
//!
//! # Inputs
//!
//! `&Path` — a project root directory (need not exist; missing paths yield
//! `TechStack::unknown()`).
//!
//! # Outputs
//!
//! [`TechStack`] struct with optional `language`, `framework`, and
//! `build_tool` fields.
//!
//! # Constraints
//!
//! - Pure: no async, no network, no DB access. Only `Path::exists()`.
//! - Framework precedence (highest → lowest): Next.js → Astro → Vite → Node.
//! - Build-tool precedence: pnpm → yarn → npm.
//!
//! # Ticket
//!
//! rag-14
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::study::stack

use std::path::Path;

// ── Types ─────────────────────────────────────────────────────────────────────

/// The detected technology stack for a project directory.
///
/// All fields are `Option<String>` — any field that cannot be determined
/// is `None`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TechStack {
    /// Primary language, e.g. `"rust"`, `"typescript"`, `"python"`, `"dart"`.
    pub language: Option<String>,
    /// Framework, e.g. `"next"`, `"astro"`, `"vite"`, `"flutter"`.
    pub framework: Option<String>,
    /// Build / package tool, e.g. `"cargo"`, `"pnpm"`, `"yarn"`, `"npm"`.
    pub build_tool: Option<String>,
}

impl TechStack {
    /// Construct a fully unknown stack (all fields `None`).
    pub fn unknown() -> Self {
        Self {
            language: None,
            framework: None,
            build_tool: None,
        }
    }

    /// Return `true` if no field was detected.
    pub fn is_unknown(&self) -> bool {
        self.language.is_none() && self.framework.is_none() && self.build_tool.is_none()
    }
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Classify a project directory by inspecting marker files.
///
/// Returns a [`TechStack`]. Unknown components are `None`.
pub fn detect(dir: &Path) -> TechStack {
    // ── Rust ──────────────────────────────────────────────────────────────────
    if dir.join("Cargo.toml").exists() {
        return TechStack {
            language: Some("rust".into()),
            framework: None,
            build_tool: Some("cargo".into()),
        };
    }

    // ── Python ────────────────────────────────────────────────────────────────
    if dir.join("pyproject.toml").exists()
        || dir.join("setup.py").exists()
        || dir.join("setup.cfg").exists()
    {
        let build_tool = if dir.join("pyproject.toml").exists() {
            Some("uv".into()) // modern default; caller can override
        } else {
            Some("pip".into())
        };
        return TechStack {
            language: Some("python".into()),
            framework: None,
            build_tool,
        };
    }

    // ── Dart / Flutter ────────────────────────────────────────────────────────
    if dir.join("pubspec.yaml").exists() {
        return TechStack {
            language: Some("dart".into()),
            framework: Some("flutter".into()),
            build_tool: Some("pub".into()),
        };
    }

    // ── JavaScript / TypeScript ───────────────────────────────────────────────
    if dir.join("package.json").exists() {
        let build_tool = detect_js_package_manager(dir);
        let framework = detect_js_framework(dir);
        return TechStack {
            language: Some("typescript".into()),
            framework,
            build_tool,
        };
    }

    TechStack::unknown()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn detect_js_package_manager(dir: &Path) -> Option<String> {
    if dir.join("pnpm-lock.yaml").exists() {
        return Some("pnpm".into());
    }
    if dir.join("yarn.lock").exists() {
        return Some("yarn".into());
    }
    if dir.join("package-lock.json").exists() {
        return Some("npm".into());
    }
    None
}

fn detect_js_framework(dir: &Path) -> Option<String> {
    // Highest precedence first.
    if has_config_prefix(dir, "next.config") {
        return Some("next".into());
    }
    if has_config_prefix(dir, "astro.config") {
        return Some("astro".into());
    }
    if has_config_prefix(dir, "vite.config") {
        return Some("vite".into());
    }
    None
}

/// Return `true` if a file with the given base prefix (any extension) exists.
fn has_config_prefix(dir: &Path, prefix: &str) -> bool {
    // Try the most common extensions.
    for ext in &["js", "mjs", "ts", "cjs"] {
        if dir.join(format!("{prefix}.{ext}")).exists() {
            return true;
        }
    }
    false
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn tmpdir() -> TempDir {
        tempfile::tempdir().unwrap()
    }

    #[test]
    fn detects_rust_project() {
        let dir = tmpdir();
        fs::write(dir.path().join("Cargo.toml"), "[package]").unwrap();
        let stack = detect(dir.path());
        assert_eq!(stack.language.as_deref(), Some("rust"));
        assert_eq!(stack.build_tool.as_deref(), Some("cargo"));
        assert!(stack.framework.is_none());
    }

    #[test]
    fn detects_python_pyproject() {
        let dir = tmpdir();
        fs::write(dir.path().join("pyproject.toml"), "[tool.uv]").unwrap();
        let stack = detect(dir.path());
        assert_eq!(stack.language.as_deref(), Some("python"));
        assert_eq!(stack.build_tool.as_deref(), Some("uv"));
    }

    #[test]
    fn detects_flutter() {
        let dir = tmpdir();
        fs::write(dir.path().join("pubspec.yaml"), "name: myapp").unwrap();
        let stack = detect(dir.path());
        assert_eq!(stack.language.as_deref(), Some("dart"));
        assert_eq!(stack.framework.as_deref(), Some("flutter"));
        assert_eq!(stack.build_tool.as_deref(), Some("pub"));
    }

    #[test]
    fn detects_vite_with_pnpm() {
        let dir = tmpdir();
        fs::write(dir.path().join("package.json"), "{}").unwrap();
        fs::write(dir.path().join("vite.config.ts"), "").unwrap();
        fs::write(dir.path().join("pnpm-lock.yaml"), "").unwrap();
        let stack = detect(dir.path());
        assert_eq!(stack.language.as_deref(), Some("typescript"));
        assert_eq!(stack.framework.as_deref(), Some("vite"));
        assert_eq!(stack.build_tool.as_deref(), Some("pnpm"));
    }

    #[test]
    fn detects_next_over_vite() {
        let dir = tmpdir();
        fs::write(dir.path().join("package.json"), "{}").unwrap();
        fs::write(dir.path().join("next.config.js"), "").unwrap();
        fs::write(dir.path().join("vite.config.ts"), "").unwrap();
        let stack = detect(dir.path());
        assert_eq!(stack.framework.as_deref(), Some("next")); // next wins
    }

    #[test]
    fn unknown_dir_returns_unknown() {
        let dir = tmpdir();
        let stack = detect(dir.path());
        assert!(stack.is_unknown());
    }
}
