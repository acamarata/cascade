//! `cascade security` — security scan subcommands.
//!
//! # Purpose
//! CLI surface for cascade-security scanning. Subcommands:
//! - scan-file   — scan a single file; exit nonzero if client-side secret found
//! - secret-scan — scan all git-tracked files for secrets
//! - audit       — run dep audit for detected ecosystem
//! - prelaunch   — full prelaunch checklist: secrets + deps + error leaks + privacy
//! - scan-hook   — CC PostToolUse hook adapter: reads hook JSON from stdin,
//!   extracts tool_input.file_path, and runs scan-file logic. Always exits 0
//!   so the CC session is never interrupted; warns on findings to stderr
//!   (CC surfaces hook stderr to the user).
//!
//! # Outputs
//! Human-readable table output by default; `--json` for machine-readable JSON.
//!
//! # Constraints
//! - scan-file and prelaunch exit nonzero on high-severity findings (for hook use)
//! - scan-hook always exits 0 to never break CC session; warns to stderr on findings
//! - audit exits 0 when tool is unavailable (graceful)

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};
use std::path::{Path, PathBuf};

use cascade_security::{
    secret_scan::{scan_file, scan_text},
    client_leak::scan_client_leak,
    dep_audit::audit,
    report::prelaunch_scan,
};

use super::Command;

// ── scan-hook: CC PostToolUse stdin parser ────────────────────────────────────

/// Extract `tool_input.file_path` from the CC PostToolUse hook JSON payload.
///
/// CC passes a JSON object on stdin with shape:
/// ```json
/// { "tool_name": "Write", "tool_input": { "file_path": "/abs/path" }, ... }
/// ```
/// Returns None if the field is absent, not a string, or the JSON is invalid.
pub fn extract_file_path_from_hook_json(json_str: &str) -> Option<PathBuf> {
    let val: serde_json::Value = serde_json::from_str(json_str).ok()?;
    val.get("tool_input")
        .and_then(|ti| ti.get("file_path"))
        .and_then(|fp| fp.as_str())
        .map(PathBuf::from)
}

/// Security scanning and advisory commands.
#[derive(Debug, Args)]
pub struct SecurityArgs {
    #[command(subcommand)]
    pub subcommand: SecuritySubcommand,
}

#[derive(Debug, Subcommand)]
pub enum SecuritySubcommand {
    /// Scan a single file for secrets. Exits nonzero if a secret is found in a client-side path.
    ScanFile(ScanFileArgs),
    /// Scan all git-tracked files in a directory for secrets.
    SecretScan(SecretScanArgs),
    /// Run dependency vulnerability audit for the detected ecosystem.
    Audit(AuditArgs),
    /// Full prelaunch security checklist: secrets + deps + error leaks + privacy policy.
    Prelaunch(PrelaunchArgs),
    /// CC PostToolUse hook adapter: reads hook JSON from stdin, scans the written file.
    /// Always exits 0 — never breaks the CC session. Warns to stderr on findings.
    ScanHook(ScanHookArgs),
}

/// `cascade security scan-hook` — CC PostToolUse adapter.
///
/// Reads a CC PostToolUse hook JSON payload from stdin, extracts
/// `tool_input.file_path`, and runs the same scan logic as `scan-file`.
///
/// # Safety contract
/// - Always exits 0, regardless of findings or errors.
/// - Warnings go to stderr (CC surfaces hook stderr to the user).
/// - If `cascade` is not on PATH, the hook command itself won't run; this
///   subcommand simply handles the case where the file is missing gracefully.
#[derive(Debug, Args)]
pub struct ScanHookArgs {}

#[derive(Debug, Args)]
pub struct ScanFileArgs {
    /// Path to the file to scan.
    pub path: PathBuf,
    /// Output findings as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Debug, Args)]
pub struct SecretScanArgs {
    /// Directory to scan (defaults to current directory).
    #[arg(default_value = ".")]
    pub dir: PathBuf,
    /// Output findings as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Debug, Args)]
pub struct AuditArgs {
    /// Directory to audit (defaults to current directory).
    #[arg(default_value = ".")]
    pub dir: PathBuf,
    /// Output report as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Debug, Args)]
pub struct PrelaunchArgs {
    /// Directory to scan (defaults to current directory).
    #[arg(default_value = ".")]
    pub dir: PathBuf,
    /// Output checklist as JSON.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for SecurityArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            SecuritySubcommand::ScanFile(args) => args.run().await,
            SecuritySubcommand::SecretScan(args) => args.run().await,
            SecuritySubcommand::Audit(args) => args.run().await,
            SecuritySubcommand::Prelaunch(args) => args.run().await,
            SecuritySubcommand::ScanHook(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for ScanFileArgs {
    async fn run(&self) -> Result<()> {
        let path = &self.path;
        let text = match std::fs::read_to_string(path) {
            Ok(t) => t,
            Err(e) => {
                eprintln!("Error reading {}: {e}", path.display());
                std::process::exit(1);
            }
        };

        let findings = scan_text(&text, &path.display().to_string());
        let client_findings = scan_client_leak(path, &text);

        if self.json {
            let out = serde_json::json!({
                "path": path.display().to_string(),
                "findings": findings,
                "client_leaks": client_findings,
            });
            println!("{}", serde_json::to_string_pretty(&out).unwrap());
        } else {
            if findings.is_empty() {
                println!("✓ No secrets found in {}", path.display());
            } else {
                println!("Secrets found in {}:", path.display());
                for f in &findings {
                    println!("  [{}] line {} col {} — {} ({})", f.severity_str(), f.line, f.column, f.kind, f.redacted_preview);
                }
            }
            if !client_findings.is_empty() {
                println!("⚠ CLIENT-SIDE LEAKS (high severity):");
                for f in &client_findings {
                    println!("  line {} — {} in client-side path", f.line, f.kind);
                }
            }
        }

        // Exit nonzero if a secret is found in a client-side path
        if !client_findings.is_empty() {
            std::process::exit(1);
        }
        Ok(())
    }
}

#[async_trait]
impl Command for ScanHookArgs {
    async fn run(&self) -> Result<()> {
        use std::io::Read;

        // Read the full stdin payload. On error, exit 0 silently.
        let mut buf = String::new();
        if std::io::stdin().read_to_string(&mut buf).is_err() {
            return Ok(());
        }

        let path = match extract_file_path_from_hook_json(&buf) {
            Some(p) => p,
            None => {
                // No file_path in payload — nothing to scan.
                return Ok(());
            }
        };

        // File must exist (Write hook fires before rename in some editors; tolerate absence).
        if !path.exists() {
            return Ok(());
        }

        let text = match std::fs::read_to_string(&path) {
            Ok(t) => t,
            Err(_) => return Ok(()),
        };

        let findings = scan_text(&text, &path.display().to_string());
        let client_findings = scan_client_leak(&path, &text);

        // Warn to stderr — CC surfaces hook stderr to the user.
        // Never hard-block (always return Ok / exit 0).
        if !findings.is_empty() || !client_findings.is_empty() {
            eprintln!(
                "[cascade security] WARNING: potential secret(s) detected in {}",
                path.display()
            );
            for f in &findings {
                eprintln!(
                    "  [{}] line {} — {} ({})",
                    f.severity_str(), f.line, f.kind, f.redacted_preview
                );
            }
            for f in &client_findings {
                eprintln!(
                    "  [CLIENT-SIDE LEAK] line {} — {} — review before committing",
                    f.line, f.kind
                );
            }
        }

        // Always exit 0 — never interrupt CC.
        Ok(())
    }
}

#[async_trait]
impl Command for SecretScanArgs {
    async fn run(&self) -> Result<()> {
        let dir = &self.dir;
        let files = git_tracked_files(dir);

        let mut all_findings = vec![];
        for file_path in &files {
            let findings = scan_file(file_path);
            all_findings.extend(findings);
        }

        if self.json {
            println!("{}", serde_json::to_string_pretty(&all_findings).unwrap());
        } else {
            if all_findings.is_empty() {
                println!("✓ No secrets found in git-tracked files");
            } else {
                println!("{} secret(s) found:", all_findings.len());
                for f in &all_findings {
                    println!("  {} line {} — {} ({})", f.path, f.line, f.kind, f.redacted_preview);
                }
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for AuditArgs {
    async fn run(&self) -> Result<()> {
        let report = audit(&self.dir).await;

        if self.json {
            println!("{}", serde_json::to_string_pretty(&report).unwrap());
        } else {
            if !report.tool_available {
                println!("⚠ Audit tool not available for {:?} ecosystem", report.ecosystem);
                return Ok(());
            }
            if report.advisories.is_empty() {
                println!("✓ No advisories found");
            } else {
                println!("{} advisory/advisories ({} high/critical):",
                    report.advisories.len(), report.high_or_critical_count);
                for a in &report.advisories {
                    println!("  [{}] {} — {} — {}", a.severity, a.id, a.package, a.title);
                }
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for PrelaunchArgs {
    async fn run(&self) -> Result<()> {
        let report = prelaunch_scan(&self.dir).await;

        if self.json {
            println!("{}", serde_json::to_string_pretty(&report).unwrap());
        } else {
            println!("Cascade Security Prelaunch Checklist");
            println!("=====================================");

            if report.secrets.is_empty() {
                println!("✓ PASS  Secrets: no secrets in tracked files");
            } else {
                println!("✗ WARN  Secrets: {} finding(s)", report.secrets.len());
                for s in report.secrets.iter().take(5) {
                    println!("        {} line {} — {}", s.path, s.line, s.kind);
                }
                if report.secrets.len() > 5 {
                    println!("        ...and {} more", report.secrets.len() - 5);
                }
            }

            if report.client_leaks.is_empty() {
                println!("✓ PASS  Client leaks: no secrets in client-side paths");
            } else {
                println!("✗ FAIL  Client leaks: {} HIGH-SEVERITY finding(s)", report.client_leaks.len());
                for s in &report.client_leaks {
                    println!("        {} line {} — {}", s.path, s.line, s.kind);
                }
            }

            if !report.deps.tool_available {
                println!("- SKIP  Deps: audit tool not available");
            } else if report.deps.high_or_critical_count == 0 {
                println!("✓ PASS  Deps: no high/critical advisories");
            } else {
                println!("✗ FAIL  Deps: {} high/critical advisory/advisories", report.deps.high_or_critical_count);
            }

            if report.error_leaks.is_empty() {
                println!("✓ PASS  Error leaks: no debug strings detected");
            } else {
                println!("- WARN  Error leaks: {} potential leak(s) (low severity)", report.error_leaks.len());
            }

            if report.has_privacy_policy {
                println!("✓ PASS  Privacy: policy document found");
            } else {
                println!("- WARN  Privacy: no PRIVACY.md or privacy route detected");
            }

            println!();
            if report.has_high_severity() {
                println!("RESULT: FAIL — high-severity issues require attention before launch");
                std::process::exit(1);
            } else {
                println!("RESULT: PASS — no blocking issues");
            }
        }

        if report.has_high_severity() {
            std::process::exit(1);
        }
        Ok(())
    }
}

/// Get git-tracked files in a directory (shared helper).
fn git_tracked_files(dir: &Path) -> Vec<PathBuf> {
    let output = std::process::Command::new("git")
        .args(["ls-files"])
        .current_dir(dir)
        .output();

    match output {
        Ok(out) if out.status.success() => {
            String::from_utf8_lossy(&out.stdout)
                .lines()
                .map(|l| dir.join(l))
                .collect()
        }
        _ => vec![],
    }
}

/// Helper trait for human-readable severity in CLI output.
trait SeverityStr {
    fn severity_str(&self) -> &str;
}

impl SeverityStr for cascade_security::secret_scan::SecretFinding {
    fn severity_str(&self) -> &str {
        use cascade_security::secret_scan::Severity;
        match self.severity {
            Severity::Low => "low",
            Severity::Medium => "medium",
            Severity::High => "high",
            Severity::Critical => "critical",
        }
    }
}

#[cfg(test)]
mod tests {
    use clap::Parser;
    use super::*;
    use crate::cmd::{Cli, Commands};

    #[test]
    fn parse_security_scan_file() {
        let cli = Cli::try_parse_from(["cascade", "security", "scan-file", "/tmp/test.txt"]).unwrap();
        assert!(matches!(cli.command, Commands::Security(_)));
    }

    #[test]
    fn parse_security_secret_scan_default_dir() {
        let cli = Cli::try_parse_from(["cascade", "security", "secret-scan"]).unwrap();
        assert!(matches!(cli.command, Commands::Security(_)));
    }

    #[test]
    fn parse_security_audit() {
        let cli = Cli::try_parse_from(["cascade", "security", "audit", "--json"]).unwrap();
        assert!(matches!(cli.command, Commands::Security(_)));
    }

    #[test]
    fn parse_security_prelaunch() {
        let cli = Cli::try_parse_from(["cascade", "security", "prelaunch"]).unwrap();
        assert!(matches!(cli.command, Commands::Security(_)));
    }

    #[test]
    fn parse_security_scan_hook() {
        let cli = Cli::try_parse_from(["cascade", "security", "scan-hook"]).unwrap();
        assert!(matches!(cli.command, Commands::Security(_)));
    }

    // ── extract_file_path_from_hook_json unit tests ───────────────────────────

    #[test]
    fn hook_json_extracts_file_path() {
        let json = r#"{"tool_name":"Write","tool_input":{"file_path":"/tmp/foo.rs","content":"x"}}"#;
        let result = extract_file_path_from_hook_json(json);
        assert_eq!(result, Some(PathBuf::from("/tmp/foo.rs")));
    }

    #[test]
    fn hook_json_extracts_edit_path() {
        let json = r#"{"tool_name":"Edit","tool_input":{"file_path":"/home/user/project/src/lib.rs","old_string":"a","new_string":"b"}}"#;
        let result = extract_file_path_from_hook_json(json);
        assert_eq!(result, Some(PathBuf::from("/home/user/project/src/lib.rs")));
    }

    #[test]
    fn hook_json_returns_none_for_missing_tool_input() {
        let json = r#"{"tool_name":"Bash","tool_input":{"command":"echo hi"}}"#;
        let result = extract_file_path_from_hook_json(json);
        assert_eq!(result, None);
    }

    #[test]
    fn hook_json_returns_none_for_invalid_json() {
        let result = extract_file_path_from_hook_json("not-json");
        assert_eq!(result, None);
    }

    #[test]
    fn hook_json_returns_none_for_empty_string() {
        let result = extract_file_path_from_hook_json("");
        assert_eq!(result, None);
    }
}
