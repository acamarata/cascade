//! Security tool handlers for cascade-mcp.
//!
//! ## Tools
//! - `cascade.security.secret_scan` — regex-based secret detection in git-tracked files or a
//!   single file; flags client-side findings as high severity.
//! - `cascade.security.audit`       — dep audit (cargo/npm/pip) via cascade-security::dep_audit.
//!
//! ## Purpose
//! Expose cascade-security scanning to MCP callers without session overhead (schema-only
//! until called — the Cascade "deferred" pattern).
//!
//! ## Inputs
//! Both handlers accept an optional `path` (string). Defaults to the process cwd.
//!
//! ## Outputs
//! Both return JSON values shaped for direct MCP tool result consumption.
//!
//! ## Constraints
//! - No shell interpolation: all subprocess invocations use arg arrays.
//! - Blocking FS work runs in `spawn_blocking` to avoid blocking the async executor.
//! - Subprocess work (audit) uses `tokio::process::Command` (already async).

use std::path::{Path, PathBuf};

use serde_json::Value;

use cascade_security::client_leak::is_client_side_path;
use cascade_security::dep_audit;
use cascade_security::secret_scan::{scan_file, SecretFinding, Severity};

use crate::server::JsonRpcError;

// ── cascade.security.secret_scan ─────────────────────────────────────────────

/// `cascade.security.secret_scan` handler.
///
/// Scans git-tracked files under `path` (or the path itself if it is a file).
/// Returns a JSON object with `findings` (array) and `summary` counts.
///
/// Client-side paths (browser-visible code) have their findings upgraded to
/// `high` severity when the pattern severity is below `high`.
pub(super) async fn handle_secret_scan(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let root = resolve_path_arg(args)?;

    let findings: Vec<SecretFinding> = tokio::task::spawn_blocking(move || {
        scan_path(&root)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?;

    let high_or_critical: usize = findings
        .iter()
        .filter(|f| matches!(f.severity, Severity::High | Severity::Critical))
        .count();

    let findings_json: Vec<Value> = findings
        .iter()
        .map(|f| {
            serde_json::json!({
                "kind": f.kind,
                "file": f.path,
                "line": f.line,
                "column": f.column,
                "redacted_preview": f.redacted_preview,
                "severity": format!("{:?}", f.severity).to_lowercase(),
                "client_side": is_client_side_path(Path::new(&f.path)),
            })
        })
        .collect();

    let summary_text = if findings_json.is_empty() {
        "No secrets found.".to_string()
    } else {
        format!(
            "{} finding(s); {} high/critical.",
            findings_json.len(),
            high_or_critical
        )
    };

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": summary_text }],
        "findings": findings_json,
        "total": findings_json.len(),
        "high_or_critical": high_or_critical,
    }))
}

/// Scan a file or directory (git-tracked files only for dirs).
///
/// For a single file: scans it directly.
/// For a directory: lists git-tracked files via `git ls-files`, scans each.
fn scan_path(root: &Path) -> Vec<SecretFinding> {
    if root.is_file() {
        return scan_file(root);
    }

    // Directory: list git-tracked files to avoid scanning .git/ or binaries.
    let output = std::process::Command::new("git")
        .args(["ls-files", "--cached", "--others", "--exclude-standard"])
        .current_dir(root)
        .output();

    match output {
        Err(_) => {
            // git unavailable — fall back to a shallow recursive scan.
            scan_dir_shallow(root)
        }
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout);
            let mut all: Vec<SecretFinding> = Vec::new();
            for rel in stdout.lines() {
                let full = root.join(rel);
                if full.is_file() {
                    let mut findings = scan_file(&full);
                    // Upgrade severity for client-side leaks if below High.
                    if is_client_side_path(&full) {
                        for f in &mut findings {
                            if matches!(f.severity, Severity::Low | Severity::Medium) {
                                f.severity = Severity::High;
                            }
                        }
                    }
                    all.extend(findings);
                }
            }
            all
        }
    }
}

/// Fallback: scan non-hidden files in `dir` (non-recursive, skips node_modules/target).
fn scan_dir_shallow(dir: &Path) -> Vec<SecretFinding> {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return vec![];
    };
    let mut all = Vec::new();
    for entry in entries.flatten() {
        let p = entry.path();
        let name = p.file_name().and_then(|n| n.to_str()).unwrap_or("");
        // Skip hidden dirs, build dirs, and large asset dirs.
        if name.starts_with('.') || matches!(name, "node_modules" | "target" | "dist" | "build") {
            continue;
        }
        if p.is_file() {
            all.extend(scan_file(&p));
        }
    }
    all
}

// ── cascade.security.audit ────────────────────────────────────────────────────

/// `cascade.security.audit` handler.
///
/// Runs `cargo audit --json` / `pnpm audit --json` / `pip-audit -f json`
/// (ecosystem auto-detected from directory contents). Returns a JSON object
/// with `tool_available`, `ecosystem`, `advisories`, and `high_or_critical_count`.
pub(super) async fn handle_security_audit(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let root = resolve_path_arg(args)?;

    // dep_audit::audit is already async (tokio::process::Command internally).
    let report = dep_audit::audit(&root).await;

    let advisories_json: Vec<Value> = report
        .advisories
        .iter()
        .map(|a| {
            serde_json::json!({
                "id": a.id,
                "package": a.package,
                "severity": a.severity,
                "title": a.title,
            })
        })
        .collect();

    let summary_text = if !report.tool_available {
        format!(
            "Dep audit tool not available for ecosystem '{:?}'.",
            report.ecosystem
        )
    } else if report.advisories.is_empty() {
        "No advisories found.".to_string()
    } else {
        format!(
            "{} advisory/advisories; {} high/critical.",
            report.advisories.len(),
            report.high_or_critical_count
        )
    };

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": summary_text }],
        "tool_available": report.tool_available,
        "ecosystem": format!("{:?}", report.ecosystem).to_lowercase(),
        "advisories": advisories_json,
        "high_or_critical_count": report.high_or_critical_count,
        "error": report.error,
    }))
}

// ── Shared arg helpers ────────────────────────────────────────────────────────

/// Extract the `path` arg from tool arguments.
///
/// Falls back to the process cwd if `path` is absent or empty.
fn resolve_path_arg(args: &Value) -> std::result::Result<PathBuf, JsonRpcError> {
    let path = args
        .get("path")
        .and_then(|v| v.as_str())
        .filter(|s| !s.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."))
        });

    Ok(path)
}
