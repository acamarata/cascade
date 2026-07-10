//! Aggregated security report + prelaunch_scan entry point.
//!
//! # Purpose
//! Combine all security sub-scans into one `Report` and provide the `prelaunch_scan`
//! convenience function that walks git-tracked files and produces a full report.
//!
//! # Inputs
//! - `prelaunch_scan(dir)` — run all scans against a project directory
//!
//! # Outputs
//! - `Report` — secrets, client_leaks, dep audit, error leaks, privacy check

use serde::{Deserialize, Serialize};
use std::path::Path;
use std::process::Command as StdCommand;

use crate::client_leak::{is_client_side_path, scan_client_leak};
use crate::dep_audit::{audit, AuditReport};
use crate::error_leak::{scan_error_leak, ErrorLeakFinding};
use crate::secret_scan::{scan_text, SecretFinding};

/// Full security report for a project.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Report {
    /// Secrets found in any tracked file.
    pub secrets: Vec<SecretFinding>,
    /// Secrets specifically in client-side paths (high severity subset).
    pub client_leaks: Vec<SecretFinding>,
    /// Dependency audit result.
    pub deps: AuditReport,
    /// Error/debug strings in response contexts.
    pub error_leaks: Vec<ErrorLeakFinding>,
    /// Whether a privacy policy document was found.
    pub has_privacy_policy: bool,
}

impl Report {
    /// True if there are any high-severity issues (client leaks or critical CVEs).
    pub fn has_high_severity(&self) -> bool {
        !self.client_leaks.is_empty() || self.deps.high_or_critical_count > 0
    }
}

/// Get the list of git-tracked files in `dir`.
fn git_tracked_files(dir: &Path) -> Vec<std::path::PathBuf> {
    let output = StdCommand::new("git")
        .args(["ls-files"])
        .current_dir(dir)
        .output();

    match output {
        Ok(out) if out.status.success() => String::from_utf8_lossy(&out.stdout)
            .lines()
            .map(|l| dir.join(l))
            .collect(),
        _ => vec![],
    }
}

/// Check whether `dir` has a privacy policy document.
fn has_privacy_policy(dir: &Path) -> bool {
    let candidates = [
        "PRIVACY.md",
        "privacy.md",
        "PRIVACY_POLICY.md",
        "privacy-policy.md",
        "docs/privacy.md",
        "public/privacy",
        "src/pages/privacy",
        "pages/privacy",
    ];
    candidates.iter().any(|c| dir.join(c).exists())
}

/// Run all security scans against `dir` and return an aggregated `Report`.
pub async fn prelaunch_scan(dir: &Path) -> Report {
    let files = git_tracked_files(dir);

    let mut secrets: Vec<SecretFinding> = vec![];
    let mut client_leaks: Vec<SecretFinding> = vec![];
    let mut error_leaks: Vec<ErrorLeakFinding> = vec![];

    for file_path in &files {
        // Skip binary-ish extensions
        if let Some(ext) = file_path.extension().and_then(|e| e.to_str()) {
            if matches!(
                ext,
                "png"
                    | "jpg"
                    | "jpeg"
                    | "gif"
                    | "ico"
                    | "woff"
                    | "woff2"
                    | "ttf"
                    | "eot"
                    | "svg"
                    | "pdf"
                    | "zip"
                    | "gz"
                    | "tar"
                    | "lock"
            ) {
                continue;
            }
        }

        let text = match std::fs::read_to_string(file_path) {
            Ok(t) => t,
            Err(_) => continue,
        };

        let file_secrets = scan_text(&text, &file_path.display().to_string());
        secrets.extend(file_secrets);

        if is_client_side_path(file_path) {
            let cl = scan_client_leak(file_path, &text);
            client_leaks.extend(cl);
        }

        let el = scan_error_leak(&text);
        error_leaks.extend(el);
    }

    let deps = audit(dir).await;
    let privacy = has_privacy_policy(dir);

    Report {
        secrets,
        client_leaks,
        deps,
        error_leaks,
        has_privacy_policy: privacy,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn has_high_severity_with_client_leak() {
        use crate::dep_audit::{AuditReport, Ecosystem};
        use crate::secret_scan::{SecretFinding, Severity};

        let finding = SecretFinding {
            kind: "aws_access_key".to_string(),
            line: 1,
            column: 1,
            redacted_preview: "AKIA***".to_string(),
            severity: Severity::Critical,
            path: "public/app.js".to_string(),
        };
        let report = Report {
            secrets: vec![finding.clone()],
            client_leaks: vec![finding],
            deps: AuditReport {
                ecosystem: Ecosystem::Unknown,
                tool_available: false,
                advisories: vec![],
                high_or_critical_count: 0,
                error: None,
            },
            error_leaks: vec![],
            has_privacy_policy: false,
        };
        assert!(report.has_high_severity());
    }

    #[test]
    fn clean_report_not_high_severity() {
        use crate::dep_audit::{AuditReport, Ecosystem};

        let report = Report {
            secrets: vec![],
            client_leaks: vec![],
            deps: AuditReport {
                ecosystem: Ecosystem::Unknown,
                tool_available: false,
                advisories: vec![],
                high_or_critical_count: 0,
                error: None,
            },
            error_leaks: vec![],
            has_privacy_policy: true,
        };
        assert!(!report.has_high_severity());
    }
}
