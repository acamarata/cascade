//! Real external-check provider for PBD protocol runners.
//!
//! # Purpose
//! Implements [`ExternalChecks`] with actual sub-process and HTTP calls so
//! `eot`, `eos`, and `eop` gates can verify the workspace is genuinely healthy
//! before advancing phase state.
//!
//! # Design
//! - **Build / test checks** — spawn a configured shell command (`cargo build`,
//!   `cargo test`, or any custom command) in the project directory, capture
//!   stdout + stderr, and surface the last `TAIL_LINES` lines on failure.
//! - **Health checks** — perform a lightweight HTTP GET to each configured URL
//!   and check the response status code. Uses only [`std::net::TcpStream`] +
//!   raw HTTP/1.0 to keep the dependency footprint at zero.
//! - **Deploy stubs** — deploy and outbound side-effects are intentionally kept
//!   as safe no-ops. All deploy/outbound methods return
//!   `"skipped (not configured — run externally)"` so they never accidentally
//!   trigger a publish or send. Callers that need real deploy verification
//!   should implement [`ExternalChecks`] directly and inject it via the seam.
//!
//! # Constraints
//! - No `unwrap()` in non-test code.
//! - Files stay under 300 lines.
//! - No hardcoded personal paths, IPs, or vendor names.

use std::{
    io::{Read, Write},
    net::TcpStream,
    process::Command,
    time::Duration,
};

use cascade_types::error::Result;

use super::protocol::ExternalChecks;

/// Number of trailing output lines captured from a failing command.
const TAIL_LINES: usize = 20;

/// HTTP connect + read timeout for health checks.
const HTTP_TIMEOUT_SECS: u64 = 10;

// ── CheckResult ───────────────────────────────────────────────────────────────

/// Outcome of a single external check.
#[derive(Debug)]
pub struct CheckResult {
    /// Human-readable label (e.g. `"build"`, `"GET https://…/health"`).
    pub label: String,
    /// `true` = passed, `false` = failed.
    pub passed: bool,
    /// Short tail of captured output or the error message on failure.
    pub detail: String,
}

impl CheckResult {
    fn pass(label: impl Into<String>, detail: impl Into<String>) -> Self {
        Self {
            label: label.into(),
            passed: true,
            detail: detail.into(),
        }
    }

    fn fail(label: impl Into<String>, detail: impl Into<String>) -> Self {
        Self {
            label: label.into(),
            passed: false,
            detail: detail.into(),
        }
    }
}

// ── BuildCheck ────────────────────────────────────────────────────────────────

/// Configuration for a build or test sub-process.
///
/// # Examples
/// ```rust
/// use cascade_core::pbd::external_checks::BuildCheck;
/// let c = BuildCheck::new("build", "cargo build --workspace");
/// ```
#[derive(Debug, Clone)]
pub struct BuildCheck {
    /// Label shown in the report (e.g. `"build"`, `"test"`).
    pub label: String,
    /// Shell command to run (passed to `sh -c`).
    pub command: String,
    /// Working directory for the command. `None` = inherit.
    pub working_dir: Option<std::path::PathBuf>,
}

impl BuildCheck {
    /// Create a new build check with no working-directory override.
    pub fn new(label: impl Into<String>, command: impl Into<String>) -> Self {
        Self {
            label: label.into(),
            command: command.into(),
            working_dir: None,
        }
    }

    /// Set an explicit working directory.
    pub fn with_dir(mut self, dir: impl Into<std::path::PathBuf>) -> Self {
        self.working_dir = Some(dir.into());
        self
    }

    /// Run the check and return a [`CheckResult`].
    pub fn run(&self) -> CheckResult {
        let mut cmd = Command::new("sh");
        cmd.arg("-c").arg(&self.command);
        if let Some(dir) = &self.working_dir {
            cmd.current_dir(dir);
        }

        match cmd.output() {
            Err(e) => CheckResult::fail(&self.label, format!("failed to spawn command: {e}")),
            Ok(out) => {
                if out.status.success() {
                    CheckResult::pass(&self.label, "ok")
                } else {
                    let combined = [out.stdout.as_slice(), out.stderr.as_slice()].concat();
                    let text = String::from_utf8_lossy(&combined);
                    let tail = tail_lines(&text, TAIL_LINES);
                    CheckResult::fail(
                        &self.label,
                        format!("exited {:?}\n{tail}", out.status.code()),
                    )
                }
            }
        }
    }
}

// ── HealthCheck ───────────────────────────────────────────────────────────────

/// Configuration for an HTTP health-endpoint check.
///
/// Uses a raw TCP connection so the crate needs no HTTP client dependency.
/// Supports `http://host[:port]/path` URLs only — TLS termination should
/// be handled upstream (reverse proxy) before the check URL is reached.
///
/// # Examples
/// ```rust
/// use cascade_core::pbd::external_checks::HealthCheck;
/// let h = HealthCheck::new("api", "http://localhost:3000/health");
/// ```
#[derive(Debug, Clone)]
pub struct HealthCheck {
    /// Label shown in the report.
    pub label: String,
    /// URL to GET. Must be `http://` (plain, no TLS).
    pub url: String,
    /// Expected HTTP status code prefix, e.g. `"2"` means any 2xx.
    pub expected_status_prefix: String,
}

impl HealthCheck {
    /// Create a health check that accepts any 2xx response.
    pub fn new(label: impl Into<String>, url: impl Into<String>) -> Self {
        Self {
            label: label.into(),
            url: url.into(),
            expected_status_prefix: "2".to_string(),
        }
    }

    /// Override the expected status prefix (default `"2"` for 2xx).
    pub fn with_expected_prefix(mut self, prefix: impl Into<String>) -> Self {
        self.expected_status_prefix = prefix.into();
        self
    }

    /// Run the check and return a [`CheckResult`].
    pub fn run(&self) -> CheckResult {
        match self.run_inner() {
            Ok(status_line) => {
                if status_line
                    .split_whitespace()
                    .nth(1)
                    .map(|code| code.starts_with(&self.expected_status_prefix))
                    .unwrap_or(false)
                {
                    CheckResult::pass(&self.label, status_line)
                } else {
                    CheckResult::fail(&self.label, format!("unexpected response: {status_line}"))
                }
            }
            Err(e) => CheckResult::fail(&self.label, e),
        }
    }

    /// Returns the HTTP status line (`"HTTP/1.0 200 OK"`) or an error string.
    fn run_inner(&self) -> std::result::Result<String, String> {
        let (host, port, path) =
            parse_http_url(&self.url).map_err(|e| format!("bad URL {}: {e}", self.url))?;

        let addr = format!("{host}:{port}");
        let timeout = Duration::from_secs(HTTP_TIMEOUT_SECS);

        let mut stream = TcpStream::connect_timeout(
            &addr.parse().map_err(|e| format!("addr parse: {e}"))?,
            timeout,
        )
        .map_err(|e| format!("connect {addr}: {e}"))?;

        stream
            .set_read_timeout(Some(timeout))
            .map_err(|e| format!("set timeout: {e}"))?;

        let request = format!("GET {path} HTTP/1.0\r\nHost: {host}\r\nConnection: close\r\n\r\n");
        stream
            .write_all(request.as_bytes())
            .map_err(|e| format!("write: {e}"))?;

        let mut response = String::new();
        stream
            .read_to_string(&mut response)
            .map_err(|e| format!("read: {e}"))?;

        let status_line = response.lines().next().unwrap_or("").to_string();
        Ok(status_line)
    }
}

/// Parse `http://host[:port]/path` into `(host, port, path)`.
fn parse_http_url(url: &str) -> std::result::Result<(String, u16, String), String> {
    let rest = url
        .strip_prefix("http://")
        .ok_or_else(|| "only http:// URLs are supported".to_string())?;

    let (authority, path) = if let Some(idx) = rest.find('/') {
        (&rest[..idx], rest[idx..].to_string())
    } else {
        (rest, "/".to_string())
    };

    let (host, port) = if let Some(idx) = authority.rfind(':') {
        let port: u16 = authority[idx + 1..]
            .parse()
            .map_err(|_| format!("invalid port in {authority}"))?;
        (authority[..idx].to_string(), port)
    } else {
        (authority.to_string(), 80u16)
    };

    Ok((host, port, path))
}

// ── RealExternalChecks ────────────────────────────────────────────────────────

/// Concrete [`ExternalChecks`] provider that runs real build, test, and HTTP
/// health checks before advancing a PBD phase gate.
///
/// # Deploy / outbound safety
/// Deploy, publish, and outbound-messaging methods are intentionally no-ops.
/// They return `"skipped (not configured — run externally)"` and never trigger
/// a real deploy or send. If you need deploy-gate verification, implement
/// [`ExternalChecks`] directly in your project-specific runner and pass it
/// through the seam.
///
/// # Examples
/// ```rust,no_run
/// use cascade_core::pbd::external_checks::{BuildCheck, RealExternalChecks};
///
/// let checks = RealExternalChecks::new()
///     .with_build(BuildCheck::new("build", "cargo build --workspace"));
/// ```
#[derive(Debug, Default)]
pub struct RealExternalChecks {
    /// Build and test commands to run.
    pub build_checks: Vec<BuildCheck>,
    /// Health endpoints to probe.
    pub health_checks: Vec<HealthCheck>,
}

impl RealExternalChecks {
    /// Create an empty provider (no checks configured).
    pub fn new() -> Self {
        Self::default()
    }

    /// Add a build / test command check.
    pub fn with_build(mut self, check: BuildCheck) -> Self {
        self.build_checks.push(check);
        self
    }

    /// Add an HTTP health-check probe.
    pub fn with_health(mut self, check: HealthCheck) -> Self {
        self.health_checks.push(check);
        self
    }

    /// Run all configured checks and collect failure messages.
    ///
    /// Returns an empty `Vec` if every check passed.
    pub fn run_all(&self) -> Vec<String> {
        let mut failures = Vec::new();

        for bc in &self.build_checks {
            let r = bc.run();
            if !r.passed {
                failures.push(format!("[{}] {}", r.label, r.detail));
            }
        }

        for hc in &self.health_checks {
            let r = hc.run();
            if !r.passed {
                failures.push(format!("[{}] {}", r.label, r.detail));
            }
        }

        failures
    }
}

impl ExternalChecks for RealExternalChecks {
    /// Run all build and health checks.
    ///
    /// Deploy and outbound side-effects are never triggered here — those gates
    /// must be performed externally and this seam is not the right place for
    /// them. Pass `NoExternalChecks` or a custom impl if you need deploy gating.
    fn run_checks(&self, _phase_id: &str) -> Result<Vec<String>> {
        Ok(self.run_all())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Return the last `n` lines of `text` as a single `String`.
fn tail_lines(text: &str, n: usize) -> String {
    let lines: Vec<&str> = text.lines().collect();
    let start = lines.len().saturating_sub(n);
    lines[start..].join("\n")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::pbd::protocol::{ExternalChecks, NoExternalChecks};

    // ── BuildCheck ────────────────────────────────────────────────────────────

    #[test]
    fn build_check_passes_on_true_command() {
        let check = BuildCheck::new("pass", "true");
        let result = check.run();
        assert!(result.passed, "expected pass, got: {}", result.detail);
        assert_eq!(result.label, "pass");
    }

    #[test]
    fn build_check_fails_on_false_command() {
        let check = BuildCheck::new("fail", "false");
        let result = check.run();
        assert!(!result.passed, "expected fail");
        assert_eq!(result.label, "fail");
    }

    #[test]
    fn build_check_captures_stderr_tail_on_failure() {
        let check = BuildCheck::new("stderr", "echo error-marker >&2 && exit 1");
        let result = check.run();
        assert!(!result.passed);
        assert!(
            result.detail.contains("error-marker"),
            "stderr not captured: {}",
            result.detail
        );
    }

    // ── RealExternalChecks ────────────────────────────────────────────────────

    #[test]
    fn real_checks_empty_is_clean() {
        let checks = RealExternalChecks::new();
        let failures = checks.run_all();
        assert!(failures.is_empty());
    }

    #[test]
    fn real_checks_collects_failures() {
        let checks = RealExternalChecks::new()
            .with_build(BuildCheck::new("ok", "true"))
            .with_build(BuildCheck::new("bad", "false"));
        let failures = checks.run_all();
        assert_eq!(failures.len(), 1);
        assert!(failures[0].contains("bad"));
    }

    #[test]
    fn real_checks_trait_impl_returns_failures() {
        let checks = RealExternalChecks::new().with_build(BuildCheck::new("broken", "false"));
        let result = checks.run_checks("p1").expect("no error");
        assert_eq!(result.len(), 1);
    }

    // ── NoExternalChecks (stub safety) ────────────────────────────────────────

    #[test]
    fn no_external_checks_returns_empty() {
        let stub = NoExternalChecks;
        let result = stub.run_checks("p1").expect("no error");
        assert!(result.is_empty(), "stub should return no errors");
    }

    // ── parse_http_url ────────────────────────────────────────────────────────

    #[test]
    fn parse_http_url_with_port() {
        let (host, port, path) = parse_http_url("http://localhost:3000/health").unwrap();
        assert_eq!(host, "localhost");
        assert_eq!(port, 3000);
        assert_eq!(path, "/health");
    }

    #[test]
    fn parse_http_url_default_port() {
        let (host, port, path) = parse_http_url("http://example.com/api").unwrap();
        assert_eq!(host, "example.com");
        assert_eq!(port, 80);
        assert_eq!(path, "/api");
    }

    #[test]
    fn parse_http_url_no_path() {
        let (host, port, path) = parse_http_url("http://localhost:8080").unwrap();
        assert_eq!(host, "localhost");
        assert_eq!(port, 8080);
        assert_eq!(path, "/");
    }

    #[test]
    fn parse_http_url_rejects_https() {
        let result = parse_http_url("https://example.com/health");
        assert!(result.is_err());
    }

    // ── tail_lines ────────────────────────────────────────────────────────────

    #[test]
    fn tail_lines_shorter_than_n() {
        let text = "a\nb\nc";
        assert_eq!(tail_lines(text, 10), "a\nb\nc");
    }

    #[test]
    fn tail_lines_exactly_n() {
        let text = "1\n2\n3";
        assert_eq!(tail_lines(text, 3), "1\n2\n3");
    }

    #[test]
    fn tail_lines_longer_than_n() {
        let text = "1\n2\n3\n4\n5";
        assert_eq!(tail_lines(text, 3), "3\n4\n5");
    }
}
