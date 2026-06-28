//! Client-side leak detection — secrets in browser-visible code paths are critical.
//!
//! # Purpose
//! A secret in server-side code is bad; a secret in a file shipped to browsers is worse.
//! This module classifies paths as client-side and re-applies secret scanning with that
//! context so callers can apply higher severity rules.
//!
//! # Inputs
//! - `is_client_side_path(path)` — true for paths that reach the browser
//! - `scan_client_leak(path, text)` — secret findings only for client-side paths
//!
//! # Outputs
//! - `bool` / `Vec<SecretFinding>`
//!
//! # Constraints
//! - Paths under /api/, /server/, /lib/server/, or named *.server.* are excluded
//! - Client extensions: .tsx .jsx .vue .svelte .html .astro (plus public/ dist/ build/ static/)

use std::path::Path;
use crate::secret_scan::{scan_text, SecretFinding};

/// Return true if `path` is likely to be served to browsers.
pub fn is_client_side_path(path: &Path) -> bool {
    let path_str = path.display().to_string();
    let path_lower = path_str.to_lowercase();

    // Exclusion check: server-side paths are not client leaks
    let server_patterns = ["/api/", "/server/", "/lib/server/", ".server."];
    if server_patterns.iter().any(|p| path_lower.contains(p)) {
        return false;
    }

    // Client-side directory prefixes
    let client_dirs = ["public/", "static/", "assets/", "dist/", "build/"];
    if client_dirs.iter().any(|d| path_lower.contains(d)) {
        return true;
    }

    // Frontend source file extensions
    let client_exts = [".tsx", ".jsx", ".vue", ".svelte", ".html", ".astro"];
    if let Some(ext) = path.extension().and_then(|e| e.to_str()) {
        let ext_lower = format!(".{}", ext.to_lowercase());
        if client_exts.iter().any(|e| *e == ext_lower) {
            return true;
        }
    }

    false
}

/// Scan `text` (at `path`) and return secret findings ONLY when the path is client-side.
pub fn scan_client_leak(path: &Path, text: &str) -> Vec<SecretFinding> {
    if !is_client_side_path(path) {
        return vec![];
    }
    scan_text(text, &path.display().to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn public_dir_is_client_side() {
        assert!(is_client_side_path(Path::new("src/public/app.js")));
    }

    #[test]
    fn tsx_file_is_client_side() {
        assert!(is_client_side_path(Path::new("src/components/Button.tsx")));
    }

    #[test]
    fn api_route_is_not_client_side() {
        assert!(!is_client_side_path(Path::new("src/api/auth.ts")));
    }

    #[test]
    fn server_file_is_excluded() {
        assert!(!is_client_side_path(Path::new("src/lib/db.server.ts")));
    }

    #[test]
    fn dist_dir_is_client_side() {
        assert!(is_client_side_path(Path::new("dist/index.js")));
    }

    #[test]
    fn rust_file_is_not_client_side() {
        assert!(!is_client_side_path(Path::new("src/main.rs")));
    }

    #[test]
    fn finds_secret_in_tsx_file() {
        let text = "const key = AKIAZ3ABCDEFGHIJKLMN;";
        let findings = scan_client_leak(Path::new("src/App.tsx"), text);
        assert!(!findings.is_empty(), "AWS key in .tsx should be flagged");
    }

    #[test]
    fn no_finding_in_server_tsx() {
        let text = "const key = AKIAZ3ABCDEFGHIJKLMN;";
        let findings = scan_client_leak(Path::new("src/api/handler.ts"), text);
        assert!(findings.is_empty(), "server-side .ts should return no client leak");
    }
}
