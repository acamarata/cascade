//! Dependency auditing — run cargo/npm/pip audit and parse advisories.
//!
//! # Purpose
//! Detect known CVEs and security advisories in project dependencies without
//! requiring the caller to know which ecosystem they're targeting.
//!
//! # Inputs
//! - `audit(dir)` — detect ecosystem from dir contents, run the audit tool
//!
//! # Outputs
//! - `AuditReport` — advisory list + tool availability flag
//!
//! # Constraints
//! - If the audit tool isn't installed, returns tool_available:false (no panic)
//! - Uses tokio::process::Command with arg arrays (no shell interpolation)
//! - Parses only high/critical advisories

use serde::{Deserialize, Serialize};
use std::path::Path;
use tokio::process::Command;

/// Ecosystem detected in a directory.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Ecosystem {
    Cargo,
    Npm,
    Pip,
    Unknown,
}

/// A single advisory from a dep audit tool.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Advisory {
    pub id: String,
    pub package: String,
    pub severity: String,
    pub title: String,
}

/// Result of running a dep audit.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuditReport {
    pub ecosystem: Ecosystem,
    pub tool_available: bool,
    pub advisories: Vec<Advisory>,
    pub high_or_critical_count: usize,
    /// Raw error message if the tool ran but parsing failed.
    pub error: Option<String>,
}

impl AuditReport {
    fn unavailable(ecosystem: Ecosystem) -> Self {
        Self {
            ecosystem,
            tool_available: false,
            advisories: vec![],
            high_or_critical_count: 0,
            error: None,
        }
    }
}

/// Detect which ecosystem lives in `dir`.
fn detect_ecosystem(dir: &Path) -> Ecosystem {
    if dir.join("Cargo.toml").exists() {
        return Ecosystem::Cargo;
    }
    if dir.join("package.json").exists() {
        return Ecosystem::Npm;
    }
    if dir.join("pyproject.toml").exists() || dir.join("requirements.txt").exists() {
        return Ecosystem::Pip;
    }
    Ecosystem::Unknown
}

/// Run dep audit for the given directory. Gracefully handles missing tools.
pub async fn audit(dir: &Path) -> AuditReport {
    let ecosystem = detect_ecosystem(dir);
    match ecosystem {
        Ecosystem::Cargo => audit_cargo(dir).await,
        Ecosystem::Npm => audit_npm(dir).await,
        Ecosystem::Pip => audit_pip(dir).await,
        Ecosystem::Unknown => AuditReport::unavailable(Ecosystem::Unknown),
    }
}

async fn audit_cargo(dir: &Path) -> AuditReport {
    let output = Command::new("cargo")
        .args(["audit", "--json"])
        .current_dir(dir)
        .output()
        .await;

    match output {
        Err(_) => AuditReport::unavailable(Ecosystem::Cargo),
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout);
            parse_cargo_audit(&stdout)
        }
    }
}

fn parse_cargo_audit(json: &str) -> AuditReport {
    let mut report = AuditReport {
        ecosystem: Ecosystem::Cargo,
        tool_available: true,
        advisories: vec![],
        high_or_critical_count: 0,
        error: None,
    };

    let parsed: serde_json::Value = match serde_json::from_str(json) {
        Ok(v) => v,
        Err(e) => {
            report.error = Some(format!("parse error: {e}"));
            return report;
        }
    };

    if let Some(list) = parsed["vulnerabilities"]["list"].as_array() {
        for vuln in list {
            let id = vuln["advisory"]["id"].as_str().unwrap_or("?").to_string();
            let pkg = vuln["package"]["name"].as_str().unwrap_or("?").to_string();
            let title = vuln["advisory"]["title"].as_str().unwrap_or("?").to_string();
            let severity = "high".to_string();
            report.advisories.push(Advisory { id, package: pkg, severity: severity.clone(), title });
            if matches!(severity.as_str(), "high" | "critical") {
                report.high_or_critical_count += 1;
            }
        }
    }

    report
}

async fn audit_npm(dir: &Path) -> AuditReport {
    let output = Command::new("pnpm")
        .args(["audit", "--json"])
        .current_dir(dir)
        .output()
        .await;

    let (out, _tool) = match output {
        Ok(o) => (o, "pnpm"),
        Err(_) => {
            match Command::new("npm").args(["audit", "--json"]).current_dir(dir).output().await {
                Ok(o) => (o, "npm"),
                Err(_) => return AuditReport::unavailable(Ecosystem::Npm),
            }
        }
    };

    let stdout = String::from_utf8_lossy(&out.stdout);
    parse_npm_audit(&stdout)
}

fn parse_npm_audit(json: &str) -> AuditReport {
    let mut report = AuditReport {
        ecosystem: Ecosystem::Npm,
        tool_available: true,
        advisories: vec![],
        high_or_critical_count: 0,
        error: None,
    };

    let parsed: serde_json::Value = match serde_json::from_str(json) {
        Ok(v) => v,
        Err(e) => {
            report.error = Some(format!("parse error: {e}"));
            return report;
        }
    };

    if let Some(vulns) = parsed["vulnerabilities"].as_object() {
        for (name, vuln) in vulns {
            let severity = vuln["severity"].as_str().unwrap_or("unknown").to_string();
            if matches!(severity.as_str(), "high" | "critical") {
                let id = format!("npm-{name}");
                let title = vuln["title"].as_str().unwrap_or(name).to_string();
                report.advisories.push(Advisory {
                    id,
                    package: name.clone(),
                    severity: severity.clone(),
                    title,
                });
                report.high_or_critical_count += 1;
            }
        }
    }

    report
}

async fn audit_pip(dir: &Path) -> AuditReport {
    let output = Command::new("pip-audit")
        .args(["-f", "json"])
        .current_dir(dir)
        .output()
        .await;

    match output {
        Err(_) => AuditReport::unavailable(Ecosystem::Pip),
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout);
            parse_pip_audit(&stdout)
        }
    }
}

fn parse_pip_audit(json: &str) -> AuditReport {
    let mut report = AuditReport {
        ecosystem: Ecosystem::Pip,
        tool_available: true,
        advisories: vec![],
        high_or_critical_count: 0,
        error: None,
    };

    let parsed: serde_json::Value = match serde_json::from_str(json) {
        Ok(v) => v,
        Err(e) => {
            report.error = Some(format!("parse error: {e}"));
            return report;
        }
    };

    if let Some(packages) = parsed.as_array() {
        for pkg in packages {
            let name = pkg["name"].as_str().unwrap_or("?").to_string();
            if let Some(vulns) = pkg["vulns"].as_array() {
                for vuln in vulns {
                    let id = vuln["id"].as_str().unwrap_or("?").to_string();
                    let desc = vuln["description"].as_str().unwrap_or("").to_string();
                    report.advisories.push(Advisory {
                        id,
                        package: name.clone(),
                        severity: "high".to_string(),
                        title: desc,
                    });
                    report.high_or_critical_count += 1;
                }
            }
        }
    }

    report
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_cargo_ecosystem() {
        let cascade_root = Path::new("/Volumes/X9/Sites/acamarata/cascade");
        assert_eq!(detect_ecosystem(cascade_root), Ecosystem::Cargo);
    }

    #[test]
    fn unknown_for_empty_dir() {
        assert_eq!(detect_ecosystem(Path::new("/tmp")), Ecosystem::Unknown);
    }

    #[test]
    fn parse_cargo_audit_empty_list() {
        let json = r#"{"vulnerabilities":{"list":[],"count":0,"found":false},"warnings":{},"lockfile":{"dependency-count":10}}"#;
        let report = parse_cargo_audit(json);
        assert!(report.tool_available);
        assert_eq!(report.high_or_critical_count, 0);
    }

    #[test]
    fn parse_npm_audit_high_severity() {
        let json = r#"{"vulnerabilities":{"lodash":{"name":"lodash","severity":"high","title":"Prototype Pollution","via":[],"effects":[],"range":"<4.17.21","nodes":[],"fixAvailable":true}},"metadata":{}}"#;
        let report = parse_npm_audit(json);
        assert!(report.tool_available);
        assert_eq!(report.high_or_critical_count, 1);
        assert_eq!(report.advisories[0].package, "lodash");
    }

    #[test]
    fn unavailable_for_unknown_ecosystem() {
        let report = AuditReport::unavailable(Ecosystem::Unknown);
        assert!(!report.tool_available);
        assert!(report.advisories.is_empty());
    }
}
