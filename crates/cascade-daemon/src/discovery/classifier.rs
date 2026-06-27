//! Project-type classifier — pure, marker-file-based detection.
//!
//! Purpose: given a directory path, probe for well-known marker files and
//! return a (ProjectType, language, framework, package_manager) tuple.
//! This function is pure (no I/O side-effects beyond `Path::exists` / `read_dir`)
//! and is synchronous so it can run cheaply inside a directory walker.
//!
//! Inputs:  `&Path` — must be an existing directory
//! Outputs: `(ProjectType, Option<String>, Option<String>, Option<String>)`
//!          → (type, language, framework, package_manager)
//! Constraints:
//!   - Never panics on missing files; all probes use `.exists()` / `read_dir`.
//!   - Framework precedence (highest → lowest): Next.js → Astro → Vite → plain Node.
//!   - Package-manager precedence: pnpm-lock.yaml → yarn.lock → package-lock.json.
//!
//! SPORT: cascade-daemon / discovery / classifier (rag-13)

use cascade_types::project::ProjectType;
use std::path::Path;

// ── Public API ────────────────────────────────────────────────────────────────

/// Classify a single directory by inspecting marker files.
///
/// Returns `(project_type, language, framework, package_manager)`.
///
/// WHY pure function: the scanner calls this in a tight loop over hundreds of
/// directories. Keeping it pure (no async, no DB, no allocation beyond the
/// returned strings) lets the scanner parallelise cheaply with `rayon` later
/// without tokio overhead.
pub fn classify(dir: &Path) -> (ProjectType, Option<String>, Option<String>, Option<String>) {
    // ── Rust ──────────────────────────────────────────────────────────────────
    if dir.join("Cargo.toml").exists() {
        return (
            ProjectType::RustCrate,
            Some("rust".into()),
            None,
            None,
        );
    }

    // ── Python ────────────────────────────────────────────────────────────────
    if dir.join("pyproject.toml").exists()
        || dir.join("setup.py").exists()
        || dir.join("setup.cfg").exists()
    {
        return (
            ProjectType::PythonPkg,
            Some("python".into()),
            None,
            None,
        );
    }

    // ── Dart / Flutter ────────────────────────────────────────────────────────
    if dir.join("pubspec.yaml").exists() {
        return (
            ProjectType::DartFlutter,
            Some("dart".into()),
            Some("flutter".into()),
            None,
        );
    }

    // ── JavaScript / TypeScript (package.json present) ────────────────────────
    if dir.join("package.json").exists() {
        let pm = detect_package_manager(dir);
        let lang = Some("typescript".into()); // treat all JS/TS projects as TS by default

        // Framework detection — highest precedence first.
        if has_config_prefix(dir, "next.config") {
            return (ProjectType::NextApp, lang, Some("next".into()), pm);
        }
        if has_config_prefix(dir, "astro.config") {
            return (ProjectType::AstroSite, lang, Some("astro".into()), pm);
        }
        if has_config_prefix(dir, "vite.config") {
            return (ProjectType::ViteApp, lang, Some("vite".into()), pm);
        }

        return (ProjectType::NodeApp, lang, None, pm);
    }

    // ── Fallback ──────────────────────────────────────────────────────────────
    (ProjectType::Unknown, None, None, None)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Detect the JavaScript package manager from lock-file presence.
///
/// Precedence: pnpm-lock.yaml > yarn.lock > package-lock.json > None.
fn detect_package_manager(dir: &Path) -> Option<String> {
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

/// Return `true` if any file in `dir` whose name starts with `prefix` exists.
///
/// Used to detect framework configs like `next.config.js`, `next.config.ts`,
/// `vite.config.mts`, etc., without hardcoding every possible extension.
fn has_config_prefix(dir: &Path, prefix: &str) -> bool {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return false;
    };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name_str = name.to_string_lossy();
        if name_str.starts_with(prefix) {
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

    fn tempdir() -> TempDir {
        tempfile::tempdir().expect("create tempdir")
    }

    fn touch(dir: &Path, name: &str) {
        fs::write(dir.join(name), b"").expect("create marker file");
    }

    #[test]
    fn rust_crate_detected() {
        let d = tempdir();
        touch(d.path(), "Cargo.toml");
        let (pt, lang, fw, pm) = classify(d.path());
        assert_eq!(pt, ProjectType::RustCrate);
        assert_eq!(lang.as_deref(), Some("rust"));
        assert!(fw.is_none());
        assert!(pm.is_none());
    }

    #[test]
    fn next_app_detected() {
        let d = tempdir();
        touch(d.path(), "package.json");
        touch(d.path(), "next.config.ts");
        touch(d.path(), "pnpm-lock.yaml");
        let (pt, lang, fw, pm) = classify(d.path());
        assert_eq!(pt, ProjectType::NextApp);
        assert_eq!(fw.as_deref(), Some("next"));
        assert_eq!(pm.as_deref(), Some("pnpm"));
        assert_eq!(lang.as_deref(), Some("typescript"));
    }

    #[test]
    fn astro_site_detected() {
        let d = tempdir();
        touch(d.path(), "package.json");
        touch(d.path(), "astro.config.mjs");
        touch(d.path(), "package-lock.json");
        let (pt, _, fw, pm) = classify(d.path());
        assert_eq!(pt, ProjectType::AstroSite);
        assert_eq!(fw.as_deref(), Some("astro"));
        assert_eq!(pm.as_deref(), Some("npm"));
    }

    #[test]
    fn vite_app_detected() {
        let d = tempdir();
        touch(d.path(), "package.json");
        touch(d.path(), "vite.config.ts");
        touch(d.path(), "yarn.lock");
        let (pt, _, fw, pm) = classify(d.path());
        assert_eq!(pt, ProjectType::ViteApp);
        assert_eq!(fw.as_deref(), Some("vite"));
        assert_eq!(pm.as_deref(), Some("yarn"));
    }

    #[test]
    fn node_app_no_framework() {
        let d = tempdir();
        touch(d.path(), "package.json");
        let (pt, _, fw, _) = classify(d.path());
        assert_eq!(pt, ProjectType::NodeApp);
        assert!(fw.is_none());
    }

    #[test]
    fn python_pkg_detected() {
        let d = tempdir();
        touch(d.path(), "pyproject.toml");
        let (pt, lang, _, _) = classify(d.path());
        assert_eq!(pt, ProjectType::PythonPkg);
        assert_eq!(lang.as_deref(), Some("python"));
    }

    #[test]
    fn python_setup_py_detected() {
        let d = tempdir();
        touch(d.path(), "setup.py");
        let (pt, lang, _, _) = classify(d.path());
        assert_eq!(pt, ProjectType::PythonPkg);
        assert_eq!(lang.as_deref(), Some("python"));
    }

    #[test]
    fn dart_flutter_detected() {
        let d = tempdir();
        touch(d.path(), "pubspec.yaml");
        let (pt, lang, fw, _) = classify(d.path());
        assert_eq!(pt, ProjectType::DartFlutter);
        assert_eq!(lang.as_deref(), Some("dart"));
        assert_eq!(fw.as_deref(), Some("flutter"));
    }

    #[test]
    fn unknown_empty_dir() {
        let d = tempdir();
        let (pt, lang, fw, pm) = classify(d.path());
        assert_eq!(pt, ProjectType::Unknown);
        assert!(lang.is_none());
        assert!(fw.is_none());
        assert!(pm.is_none());
    }

    #[test]
    fn next_takes_precedence_over_vite() {
        // next.config wins even when vite.config is also present.
        let d = tempdir();
        touch(d.path(), "package.json");
        touch(d.path(), "next.config.js");
        touch(d.path(), "vite.config.ts");
        let (pt, _, fw, _) = classify(d.path());
        assert_eq!(pt, ProjectType::NextApp);
        assert_eq!(fw.as_deref(), Some("next"));
    }

    #[test]
    fn pnpm_lock_wins_over_npm() {
        let d = tempdir();
        touch(d.path(), "package.json");
        touch(d.path(), "pnpm-lock.yaml");
        touch(d.path(), "package-lock.json");
        let (_, _, _, pm) = classify(d.path());
        assert_eq!(pm.as_deref(), Some("pnpm"));
    }
}
