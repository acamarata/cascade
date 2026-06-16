//! `cascade doctor` — diagnose cascade health and report issues.
//!
//! Runs a series of checks and prints a structured report table:
//!
//! ```text
//! CHECK                          STATUS   DETAIL
//! ─────────────────────────────────────────────────────
//! GCI tier health                PASS
//! PRC tier health                WARN     AGENTS.md is not a symlink
//! Daemon socket                  WARN     daemon not running
//! Global config                  PASS
//! Project config                 FAIL     TOML parse error on line 3
//! Stale derive-files             PASS
//! ```
//!
//! `--fix` auto-repairs safe issues (broken symlinks, stale temp files).

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::Result;
use cascade_types::paths;
use clap::Args;

use super::Command;

/// Arguments for `cascade doctor`.
#[derive(Debug, Args)]
pub struct DoctorArgs {
    /// Attempt to auto-repair safe issues (rebuild symlinks, remove stale files).
    #[arg(long)]
    pub fix: bool,

    /// Treat cross-tier content duplication findings as errors (exit non-zero).
    ///
    /// By default, duplication findings are advisory WARNings.  Pass this flag
    /// in CI to enforce the "reference up, never duplicate" rule strictly.
    #[arg(long)]
    pub strict: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum CheckStatus {
    Pass,
    Warn,
    Fail,
}

impl std::fmt::Display for CheckStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CheckStatus::Pass => write!(f, "PASS"),
            CheckStatus::Warn => write!(f, "WARN"),
            CheckStatus::Fail => write!(f, "FAIL"),
        }
    }
}

struct Check {
    name: &'static str,
    status: CheckStatus,
    detail: String,
}

#[async_trait]
impl Command for DoctorArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let mut checks = Vec::<Check>::new();
        let mut any_fail = false;

        // ── Tier health ────────────────────────────────────────────────────
        // WHY: DiscoveredTier exposes `cascade_dir` (the `.cascade/` path) and
        // `is_readable()`. `root` and `is_present` are not fields on the struct —
        // we derive them from `cascade_dir`.
        let tiers = cascade_core::discovery::TierDiscovery::new().discover(&cwd)?;
        for t in &tiers {
            let name: &'static str =
                Box::leak(format!("{:?} tier health", t.tier).into_boxed_str());
            let issues = cascade_core::symlinks::verify_siblings(&t.cascade_dir);
            if !t.is_readable() {
                checks.push(Check {
                    name,
                    status: CheckStatus::Warn,
                    detail: "CASCADE.md not found or not readable".into(),
                });
            } else if !issues.is_empty() {
                checks.push(Check {
                    name,
                    status: CheckStatus::Warn,
                    detail: issues.join("; "),
                });
            } else {
                checks.push(Check {
                    name,
                    status: CheckStatus::Pass,
                    detail: String::new(),
                });
            }
        }

        // ── Daemon socket ──────────────────────────────────────────────────
        {
            let sock = paths::daemon_socket();
            let (status, detail) = if sock.exists() {
                (CheckStatus::Pass, String::new())
            } else {
                (
                    CheckStatus::Warn,
                    format!("socket not found at {}", sock.display()),
                )
            };
            checks.push(Check {
                name: "Daemon socket",
                status,
                detail,
            });
        }

        // ── Config integrity ───────────────────────────────────────────────
        for (label, config_path) in config_paths(&cwd) {
            if !config_path.exists() {
                checks.push(Check {
                    name: Box::leak(label.into_boxed_str()),
                    status: CheckStatus::Pass,
                    detail: "(not present — using defaults)".into(),
                });
                continue;
            }
            let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
            match raw.parse::<toml::Value>() {
                Ok(_) => checks.push(Check {
                    name: Box::leak(label.into_boxed_str()),
                    status: CheckStatus::Pass,
                    detail: String::new(),
                }),
                Err(e) => {
                    any_fail = true;
                    checks.push(Check {
                        name: Box::leak(label.into_boxed_str()),
                        status: CheckStatus::Fail,
                        detail: e.to_string(),
                    });
                }
            }
        }

        // ── Stale derive-files ─────────────────────────────────────────────
        {
            let mut stale: Vec<String> = Vec::new();
            for t in &tiers {
                let issues = cascade_core::symlinks::verify_siblings(&t.cascade_dir);
                for issue in issues {
                    stale.push(format!("[{:?}] {}", t.tier, issue));
                }
            }
            let (status, detail) = if stale.is_empty() {
                (CheckStatus::Pass, String::new())
            } else {
                (CheckStatus::Warn, stale.join("; "))
            };
            checks.push(Check {
                name: "Stale derive-files",
                status,
                detail,
            });
        }

        // ── Cross-tier duplication lint ────────────────────────────────────
        // Advisory by default; --strict makes findings a FAIL.
        {
            let resolved =
                cascade_core::cascade_resolution::resolve_cascade_full(&cwd);
            match resolved {
                Ok(ref rc) => {
                    let findings = cascade_core::lint_duplication::lint_duplication(rc);
                    if findings.is_empty() {
                        checks.push(Check {
                            name: "Cross-tier duplication",
                            status: CheckStatus::Pass,
                            detail: String::new(),
                        });
                    } else {
                        let dup_status = if self.strict {
                            any_fail = true;
                            CheckStatus::Fail
                        } else {
                            CheckStatus::Warn
                        };
                        for finding in &findings {
                            let name: &'static str = Box::leak(
                                format!(
                                    "Cross-tier duplication ({:?} → {:?})",
                                    finding.lower_tier, finding.higher_tier
                                )
                                .into_boxed_str(),
                            );
                            checks.push(Check {
                                name,
                                status: dup_status,
                                detail: format!(
                                    "'{}' — reference up instead",
                                    finding.snippet
                                ),
                            });
                        }
                    }
                }
                Err(e) => {
                    // Resolution failure is non-fatal for the duplication check.
                    checks.push(Check {
                        name: "Cross-tier duplication",
                        status: CheckStatus::Warn,
                        detail: format!("could not resolve cascade: {e}"),
                    });
                }
            }
        }

        // ── Dangling / broken pointers ─────────────────────────────────────
        // Advisory by default; --strict escalates to FAIL.
        {
            let resolved = cascade_core::cascade_resolution::resolve_cascade_full(&cwd);
            match resolved {
                Ok(ref rc) => {
                    let findings = cascade_core::lint_dangling::lint_dangling(rc);
                    if findings.is_empty() {
                        checks.push(Check {
                            name: "Dangling references",
                            status: CheckStatus::Pass,
                            detail: String::new(),
                        });
                    } else {
                        let dangle_status = if self.strict {
                            any_fail = true;
                            CheckStatus::Fail
                        } else {
                            CheckStatus::Warn
                        };
                        for finding in &findings {
                            let name: &'static str = Box::leak(
                                format!("Dangling reference ({:?})", finding.tier)
                                    .into_boxed_str(),
                            );
                            checks.push(Check {
                                name,
                                status: dangle_status,
                                detail: format!(
                                    "'{}' → {} (not found)",
                                    finding.raw_ref,
                                    finding.resolved_path.display()
                                ),
                            });
                        }
                    }
                }
                Err(e) => {
                    checks.push(Check {
                        name: "Dangling references",
                        status: CheckStatus::Warn,
                        detail: format!("could not resolve cascade: {e}"),
                    });
                }
            }
        }

        // ── Hand-edited generated files ────────────────────────────────────
        {
            let scan_dirs: Vec<PathBuf> = tiers
                .iter()
                .map(|t| t.cascade_dir.clone())
                .collect();
            let findings = cascade_core::lint_generated::lint_generated(&scan_dirs);
            if findings.is_empty() {
                checks.push(Check {
                    name: "Generated files integrity",
                    status: CheckStatus::Pass,
                    detail: String::new(),
                });
            } else {
                for finding in &findings {
                    checks.push(Check {
                        name: "Generated file edited by hand",
                        status: CheckStatus::Warn,
                        detail: format!(
                            "{} — edits will be overwritten on next resolve",
                            finding.path.display()
                        ),
                    });
                }
            }
        }

        // ── Cross-tier value conflicts ─────────────────────────────────────
        // Advisory by default; --strict escalates to FAIL.
        {
            let resolved = cascade_core::cascade_resolution::resolve_cascade_full(&cwd);
            match resolved {
                Ok(ref rc) => {
                    let findings = cascade_core::lint_conflicts::lint_conflicts(rc);
                    if findings.is_empty() {
                        checks.push(Check {
                            name: "Cross-tier value conflicts",
                            status: CheckStatus::Pass,
                            detail: String::new(),
                        });
                    } else {
                        let conflict_status = if self.strict {
                            any_fail = true;
                            CheckStatus::Fail
                        } else {
                            CheckStatus::Warn
                        };
                        for finding in &findings {
                            let name: &'static str = Box::leak(
                                format!(
                                    "Cross-tier conflict ({:?} vs {:?})",
                                    finding.tier_a, finding.tier_b
                                )
                                .into_boxed_str(),
                            );
                            checks.push(Check {
                                name,
                                status: conflict_status,
                                detail: format!(
                                    "'{}' = '{}' vs '{}' — resolve conflict or document the override",
                                    finding.key, finding.value_a, finding.value_b
                                ),
                            });
                        }
                    }
                }
                Err(e) => {
                    checks.push(Check {
                        name: "Cross-tier value conflicts",
                        status: CheckStatus::Warn,
                        detail: format!("could not resolve cascade: {e}"),
                    });
                }
            }
        }

        // ── Audit log + security tokens (T-P2-E07-18) ─────────────────────
        if let Some(home) = std::env::var_os("HOME").map(PathBuf::from) {
            let cascade_dir = home.join(".cascade");
            checks.extend(check_audit_log(&cascade_dir.join("audit.log")));
            checks.extend(check_security_tokens(&cascade_dir));
        }

        // ── Auto-fix pass ──────────────────────────────────────────────────
        if self.fix {
            for t in &tiers {
                if t.is_readable() {
                    if let Err(e) = cascade_core::symlinks::create_siblings(&t.cascade_dir, false) {
                        eprintln!("fix failed for {:?}: {}", t.tier, e);
                    }
                }
            }
        }

        // ── Print report ───────────────────────────────────────────────────
        println!("{:<40} {:<8} DETAIL", "CHECK", "STATUS");
        println!("{}", "-".repeat(80));
        for c in &checks {
            if c.status == CheckStatus::Fail {
                any_fail = true;
            }
            println!("{:<40} {:<8} {}", c.name, c.status, c.detail);
        }

        if any_fail {
            std::process::exit(1);
        }
        Ok(())
    }
}

/// Audit-log health checks (T-P2-E07-18): existence, 0600 permissions, hash-chain
/// integrity, and an entry-count/last-timestamp summary.
///
/// Purpose: surface tamper-evidence status of `~/.cascade/audit.log` in `cascade
///   doctor`. Read-only: never repairs or creates the log.
/// Inputs: `audit_path` — the audit log file path.
/// Outputs: a `Vec<Check>`; a missing log yields a single FAIL check (no panic).
/// Constraints: a broken chain is FAIL; permission drift is WARN.
/// SPORT: MASTER-CLI.md — cascade doctor audit section.
fn check_audit_log(audit_path: &std::path::Path) -> Vec<Check> {
    let mut checks = Vec::new();

    if !audit_path.exists() {
        checks.push(Check {
            name: "audit.log exists",
            status: CheckStatus::Fail,
            detail: format!("not found at {}", audit_path.display()),
        });
        return checks;
    }
    checks.push(Check {
        name: "audit.log exists",
        status: CheckStatus::Pass,
        detail: String::new(),
    });

    // Permissions: must be 0600 (owner read/write only). Unix only.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        match std::fs::metadata(audit_path) {
            Ok(meta) => {
                let mode = meta.permissions().mode() & 0o777;
                if mode == 0o600 {
                    checks.push(Check {
                        name: "audit.log 0600",
                        status: CheckStatus::Pass,
                        detail: String::new(),
                    });
                } else {
                    checks.push(Check {
                        name: "audit.log 0600",
                        status: CheckStatus::Warn,
                        detail: format!("mode is 0o{mode:o}, expected 0o600"),
                    });
                }
            }
            Err(e) => checks.push(Check {
                name: "audit.log 0600",
                status: CheckStatus::Warn,
                detail: format!("cannot stat: {e}"),
            }),
        }
    }

    // Chain integrity via the cascade-audit verifier.
    match cascade_audit::AuditLog::open(audit_path) {
        Ok(log) => match log.verify_chain() {
            Ok(violations) if violations.is_empty() => checks.push(Check {
                name: "audit chain integrity",
                status: CheckStatus::Pass,
                detail: String::new(),
            }),
            Ok(violations) => {
                let lines: Vec<String> = violations.iter().map(|v| v.line.to_string()).collect();
                checks.push(Check {
                    name: "audit chain integrity",
                    status: CheckStatus::Fail,
                    detail: format!(
                        "{} violation(s) at line(s) {}",
                        violations.len(),
                        lines.join(", ")
                    ),
                });
            }
            Err(e) => checks.push(Check {
                name: "audit chain integrity",
                status: CheckStatus::Fail,
                detail: format!("verify failed: {e}"),
            }),
        },
        Err(e) => checks.push(Check {
            name: "audit chain integrity",
            status: CheckStatus::Fail,
            detail: format!("cannot open log: {e}"),
        }),
    }

    // Entry count + last timestamp (informational).
    if let Ok(content) = std::fs::read_to_string(audit_path) {
        let count = content.lines().filter(|l| !l.trim().is_empty()).count();
        let last_ts = content
            .lines()
            .rfind(|l| !l.trim().is_empty())
            .and_then(|l| serde_json::from_str::<serde_json::Value>(l).ok())
            .and_then(|v| v.get("ts").and_then(|t| t.as_str()).map(String::from))
            .unwrap_or_else(|| "unknown".to_string());
        checks.push(Check {
            name: "audit entry count",
            status: CheckStatus::Pass,
            detail: format!("{count} entries, last {last_ts}"),
        });
    }

    checks
}

/// Security-token permission checks (T-P2-E07-18): `proxy.token` and
/// `dashboard.token` must be 0600 when present.
///
/// A missing token file is WARN (not yet generated), not FAIL.
fn check_security_tokens(cascade_dir: &std::path::Path) -> Vec<Check> {
    let mut checks = Vec::new();
    for (label, file) in [
        ("proxy.token 0600", "proxy.token"),
        ("dashboard.token 0600", "dashboard.token"),
    ] {
        let path = cascade_dir.join(file);
        if !path.exists() {
            checks.push(Check {
                name: label,
                status: CheckStatus::Warn,
                detail: "not present (not yet generated)".to_string(),
            });
            continue;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let status_detail = match std::fs::metadata(&path) {
                Ok(meta) => {
                    let mode = meta.permissions().mode() & 0o777;
                    if mode == 0o600 {
                        (CheckStatus::Pass, String::new())
                    } else {
                        (
                            CheckStatus::Warn,
                            format!("mode is 0o{mode:o}, expected 0o600"),
                        )
                    }
                }
                Err(e) => (CheckStatus::Warn, format!("cannot stat: {e}")),
            };
            checks.push(Check {
                name: label,
                status: status_detail.0,
                detail: status_detail.1,
            });
        }
        #[cfg(not(unix))]
        {
            checks.push(Check {
                name: label,
                status: CheckStatus::Pass,
                detail: "permission check skipped (non-unix)".to_string(),
            });
        }
    }
    checks
}

fn config_paths(cwd: &std::path::Path) -> Vec<(String, PathBuf)> {
    let mut paths_list = Vec::new();
    // Global config.
    if let Ok(home) = std::env::var("HOME") {
        paths_list.push((
            "Global config".into(),
            PathBuf::from(home).join(".cascade").join("config.toml"),
        ));
    }
    // Project config (nearest ancestor).
    if let Some(p) = cwd.ancestors().find_map(|a| {
        let c = a.join(".cascade").join("config.toml");
        if c.exists() {
            Some(c)
        } else {
            None
        }
    }) {
        paths_list.push(("Project config".into(), p));
    }
    paths_list
}

#[cfg(test)]
mod audit_checks_tests {
    use super::*;
    use cascade_audit::{AuditEntry, AuditLog, AuditOp};

    #[test]
    fn missing_audit_log_reports_fail_not_panic() {
        let dir = tempfile::tempdir().unwrap();
        let checks = check_audit_log(&dir.path().join("audit.log"));
        assert_eq!(checks.len(), 1);
        assert_eq!(checks[0].status, CheckStatus::Fail);
    }

    #[test]
    fn valid_chain_reports_pass() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("audit.log");
        let log = AuditLog::open(&path).unwrap();
        log.append(AuditEntry {
            actor: "cascaded".into(),
            op: AuditOp::DaemonStart,
            target: "cascaded".into(),
            detail: "v0.1.0".into(),
        })
        .unwrap();
        let checks = check_audit_log(&path);
        let chain = checks
            .iter()
            .find(|c| c.name == "audit chain integrity")
            .expect("chain check present");
        assert_eq!(chain.status, CheckStatus::Pass);
    }

    #[test]
    fn tampered_chain_reports_fail() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("audit.log");
        let log = AuditLog::open(&path).unwrap();
        for i in 0..3 {
            log.append(AuditEntry {
                actor: "cascaded".into(),
                op: AuditOp::DaemonStart,
                target: "cascaded".into(),
                detail: format!("entry {i}"),
            })
            .unwrap();
        }
        // Tamper: rewrite the middle record's detail without fixing its hash.
        let content = std::fs::read_to_string(&path).unwrap();
        let mut lines: Vec<String> = content.lines().map(String::from).collect();
        lines[1] = lines[1].replace("entry 1", "TAMPERED");
        std::fs::write(&path, lines.join("\n") + "\n").unwrap();

        let checks = check_audit_log(&path);
        let chain = checks
            .iter()
            .find(|c| c.name == "audit chain integrity")
            .expect("chain check present");
        assert_eq!(chain.status, CheckStatus::Fail, "detail: {}", chain.detail);
    }
}
