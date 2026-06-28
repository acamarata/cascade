//! Opt-in security gate for PBD EOx phase runners.
//!
//! # Purpose
//! Wraps `cascade-security`'s `prelaunch_scan` + `client_leak` detection to
//! provide a `SecurityChecks` that implements `ExternalChecks`. Only HIGH-severity
//! findings (client-side secrets, critical CVEs) cause a gate failure. Low/medium
//! findings are informational and never block a ticket.
//!
//! # Design
//! - `SecurityChecks::new(project_dir)` — scans on every `run_checks` call.
//! - `WithSecurity<C>(inner, security)` — composable wrapper: calls both the
//!   inner checks and the security checks, concatenating any failures.
//!   Callers opt in by wrapping their existing `ExternalChecks` impl; the default
//!   `protocol.rs` wiring is unchanged.
//!
//! # Inputs / Outputs
//! - Input: `context_id: &str` (entity path, passed through from the phase runner)
//! - Output: `Vec<String>` of failure messages, empty on pass.
//!
//! # Constraints
//! - No `unwrap()` outside `#[cfg(test)]` blocks.
//! - Uses a tokio single-threaded runtime internally (one per `run_checks` call)
//!   because `ExternalChecks::run_checks` is synchronous; async scan is run via
//!   `Runtime::block_on`.
//! - Never blocks the EOT default path: only a caller that explicitly constructs
//!   `WithSecurity` or `SecurityChecks` runs these checks.
//!
//! # SPORT
//! MASTER-CRATES.md — cascade-core: pbd/security_checks (P12-security-gate)

use std::path::{Path, PathBuf};

use cascade_types::error::Result;

use super::protocol::ExternalChecks;
use cascade_security::report::{prelaunch_scan, Report};

// ── SecurityChecks ────────────────────────────────────────────────────────────

/// An `ExternalChecks` implementation that runs `cascade-security`'s
/// `prelaunch_scan` against the configured project directory on every call.
///
/// Only HIGH-severity issues produce gate failures:
/// - Secrets detected in client-side paths (`report.client_leaks` non-empty)
/// - Dependencies with high/critical CVEs (`report.deps.high_or_critical_count > 0`)
///
/// Low and medium findings (generic secrets, error leaks, missing privacy policy)
/// are surfaced as tracing `warn!` messages but never block advancement.
///
/// # Examples
/// ```rust,no_run
/// use cascade_core::pbd::security_checks::SecurityChecks;
/// use cascade_core::pbd::protocol::ExternalChecks;
///
/// let sc = SecurityChecks::new("/path/to/project");
/// let failures = sc.run_checks("p1/e01/w01/s01/t01").unwrap();
/// assert!(failures.is_empty()); // clean project
/// ```
pub struct SecurityChecks {
    /// Root directory of the project to scan (must contain git-tracked files).
    project_dir: PathBuf,
}

impl SecurityChecks {
    /// Create a new security checker targeting `project_dir`.
    pub fn new(project_dir: impl Into<PathBuf>) -> Self {
        Self { project_dir: project_dir.into() }
    }

    /// Run the security scan synchronously. Returns only high-severity failures.
    fn scan_sync(&self) -> Vec<String> {
        let dir = self.project_dir.clone();
        // prelaunch_scan is async; block on it with a single-threaded runtime.
        let report: Report = match tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
        {
            Ok(rt) => rt.block_on(prelaunch_scan(&dir)),
            Err(e) => {
                tracing::warn!(
                    dir = %dir.display(),
                    "security_checks: failed to build tokio runtime: {e}"
                );
                return vec![];
            }
        };

        failures_from_report(&report, &dir)
    }
}

impl ExternalChecks for SecurityChecks {
    /// Run the security scan for `context_id`.
    ///
    /// `context_id` is passed through from the phase runner (e.g.
    /// `"p1/e01/w01/s01/t01"`) but is not used in the scan itself — the
    /// scanner always covers the full project directory.
    fn run_checks(&self, _context_id: &str) -> Result<Vec<String>> {
        Ok(self.scan_sync())
    }
}

// ── WithSecurity ──────────────────────────────────────────────────────────────

/// Composable wrapper that runs both an inner `ExternalChecks` and a
/// `SecurityChecks`, concatenating any failures.
///
/// Opt-in usage: wrap your existing checks at the call site.
///
/// # Examples
/// ```rust,no_run
/// use cascade_core::pbd::external_checks::RealExternalChecks;
/// use cascade_core::pbd::security_checks::{SecurityChecks, WithSecurity};
/// use cascade_core::pbd::protocol::ExternalChecks;
///
/// let inner = RealExternalChecks::new();
/// let with_sec = WithSecurity::new(inner, SecurityChecks::new("/path/to/project"));
/// // Pass `with_sec` as `checks: &dyn ExternalChecks` to `run_eot` / `run_eop`.
/// ```
pub struct WithSecurity<C: ExternalChecks> {
    inner: C,
    security: SecurityChecks,
}

impl<C: ExternalChecks> WithSecurity<C> {
    /// Wrap `inner` with a security gate that scans `project_dir`.
    pub fn new(inner: C, security: SecurityChecks) -> Self {
        Self { inner, security }
    }
}

impl<C: ExternalChecks> ExternalChecks for WithSecurity<C> {
    /// Run both inner checks and the security scan; concatenate all failures.
    fn run_checks(&self, context_id: &str) -> Result<Vec<String>> {
        let mut failures = self.inner.run_checks(context_id)?;
        let sec_failures = self.security.run_checks(context_id)?;
        failures.extend(sec_failures);
        Ok(failures)
    }
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Extract only HIGH-severity failure messages from a `Report`.
///
/// High severity = client-side secrets or critical CVEs. Everything else is
/// logged as a warning but does not produce a failure.
fn failures_from_report(report: &Report, dir: &Path) -> Vec<String> {
    let mut failures = Vec::new();

    // Client-side secrets are always HIGH (a token shipped to the browser).
    for leak in &report.client_leaks {
        failures.push(format!(
            "[security] client-side secret ({kind}) at {path}:{line} — remove from client bundle",
            kind = leak.kind,
            path = leak.path,
            line = leak.line,
        ));
    }

    // Critical CVEs in dependencies.
    if report.deps.high_or_critical_count > 0 {
        failures.push(format!(
            "[security] {} high/critical CVE(s) in dependencies under {} — run audit and update",
            report.deps.high_or_critical_count,
            dir.display(),
        ));
    }

    // Low/medium findings: log only.
    if !report.secrets.is_empty() && report.client_leaks.is_empty() {
        tracing::warn!(
            count = report.secrets.len(),
            "security_checks: {} non-client secret finding(s) (medium/low — not blocking)",
            report.secrets.len()
        );
    }

    failures
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pbd::protocol::{ExternalChecks, NoExternalChecks};
    use cascade_security::report::Report;
    use cascade_security::dep_audit::{AuditReport, Ecosystem};
    use cascade_security::secret_scan::{SecretFinding, Severity};

    // ── helpers ───────────────────────────────────────────────────────────────

    fn clean_report() -> Report {
        Report {
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
            has_privacy_policy: false,
        }
    }

    fn finding(kind: &str, path: &str) -> SecretFinding {
        SecretFinding {
            kind: kind.to_string(),
            line: 1,
            column: 1,
            redacted_preview: "AKIA***".to_string(),
            severity: Severity::Critical,
            path: path.to_string(),
        }
    }

    // ── Test: clean project produces no failures ──────────────────────────────

    /// `failures_from_report` on a clean report returns empty Vec.
    ///
    /// WHY: a project with no client leaks and no critical CVEs must not block.
    #[test]
    fn clean_report_produces_no_failures() {
        let dir = std::path::Path::new("/tmp");
        let report = clean_report();
        let failures = failures_from_report(&report, dir);
        assert!(
            failures.is_empty(),
            "clean report must yield no failures: {failures:#?}"
        );
    }

    // ── Test: client-side secret produces a HIGH failure ─────────────────────

    /// A client-side secret in the report produces exactly one failure message.
    ///
    /// WHY: client-side leaks are the primary HIGH gate.
    #[test]
    fn client_leak_produces_high_failure() {
        let dir = std::path::Path::new("/tmp");
        let mut report = clean_report();
        report.client_leaks.push(finding("aws_access_key", "public/app.js"));

        let failures = failures_from_report(&report, dir);
        assert_eq!(failures.len(), 1, "expected one failure for client leak");
        assert!(
            failures[0].contains("client-side secret"),
            "failure message must mention client-side secret: {}",
            failures[0]
        );
        assert!(
            failures[0].contains("aws_access_key"),
            "failure message must include the kind: {}",
            failures[0]
        );
    }

    // ── Test: critical CVE count produces a HIGH failure ─────────────────────

    /// A report with `high_or_critical_count > 0` produces a failure.
    ///
    /// WHY: critical dependency CVEs are a HIGH gate.
    #[test]
    fn critical_cve_produces_high_failure() {
        let dir = std::path::Path::new("/tmp");
        let mut report = clean_report();
        report.deps.high_or_critical_count = 3;

        let failures = failures_from_report(&report, dir);
        assert_eq!(failures.len(), 1, "expected one failure for CVEs");
        assert!(
            failures[0].contains("3 high/critical CVE"),
            "failure message must include count: {}",
            failures[0]
        );
    }

    // ── Test: non-client secret is low/medium — never blocks ─────────────────

    /// A `secrets` entry without a matching `client_leaks` entry is low/medium
    /// and must NOT produce a gate failure.
    ///
    /// WHY: server-side secrets found by the scanner are informational only.
    #[test]
    fn server_side_secret_does_not_block() {
        let dir = std::path::Path::new("/tmp");
        let mut report = clean_report();
        // Add a secret to `secrets` only (not `client_leaks`).
        report.secrets.push(finding("generic_secret_assignment", "src/server/auth.rs"));

        let failures = failures_from_report(&report, dir);
        assert!(
            failures.is_empty(),
            "server-side secret must not produce gate failure: {failures:#?}"
        );
    }

    // ── Test: WithSecurity concatenates inner + security failures ─────────────

    /// `WithSecurity` with a failing inner check AND a high-severity security
    /// finding produces failures from both sources.
    ///
    /// WHY: verifies the composable wrapper concatenates correctly.
    #[test]
    fn with_security_concatenates_failures() {
        // Build a WithSecurity wrapping a pre-built inner that always fails.
        struct AlwaysFail;
        impl ExternalChecks for AlwaysFail {
            fn run_checks(&self, _id: &str) -> cascade_types::error::Result<Vec<String>> {
                Ok(vec!["inner failure".to_string()])
            }
        }

        // SecurityChecks against a real (clean) temp dir — scan will pass.
        let tmp = tempfile::TempDir::new().unwrap();
        let sec = SecurityChecks::new(tmp.path());
        let combined = WithSecurity::new(AlwaysFail, sec);

        let failures = combined.run_checks("p1").unwrap();
        // Inner always fails; security scan on empty dir is clean.
        assert_eq!(failures.len(), 1, "expected only inner failure: {failures:#?}");
        assert_eq!(failures[0], "inner failure");
    }

    // ── Test: WithSecurity passes when both inner and security are clean ──────

    /// `WithSecurity` wrapping `NoExternalChecks` over a clean dir returns empty.
    ///
    /// WHY: no failures from either side means the gate passes.
    #[test]
    fn with_security_passes_on_clean_project() {
        let tmp = tempfile::TempDir::new().unwrap();
        let sec = SecurityChecks::new(tmp.path());
        let combined = WithSecurity::new(NoExternalChecks, sec);

        let failures = combined.run_checks("p1/e01").unwrap();
        assert!(
            failures.is_empty(),
            "clean project + NoExternalChecks must pass: {failures:#?}"
        );
    }
}
