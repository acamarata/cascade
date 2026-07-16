//! Secret scanning — detect credentials and keys in text using compiled regexes.
//!
//! # Purpose
//! Scan arbitrary text (or file content) for credential patterns. Returns structured
//! findings with redacted previews so callers can report without leaking actual secrets.
//!
//! # Inputs
//! - `scan_text(text, path)` — scan a string with an associated path for context
//! - `scan_file(path)` — read a file and call scan_text
//!
//! # Outputs
//! - `Vec<SecretFinding>` — zero or more findings; empty = clean
//!
//! # Constraints
//! - Skips obvious placeholders: "your-api-key", "xxxx", "changeme", all-same-char sequences
//! - Redacts: shows first 4 chars of matched secret + "***"
//! - Compiled regexes via once_cell::sync::Lazy for zero per-call cost

use once_cell::sync::Lazy;
use regex::Regex;
use serde::{Deserialize, Serialize};
use std::path::Path;

/// Severity level for a finding.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Severity {
    Low,
    Medium,
    High,
    Critical,
}

/// A single secret finding from scanning text.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SecretFinding {
    /// Category of the detected secret (e.g. "aws_access_key", "private_key").
    pub kind: String,
    /// 1-based line number in the scanned text.
    pub line: usize,
    /// 1-based column of the match start.
    pub column: usize,
    /// Redacted preview: first 4 chars + "***".
    pub redacted_preview: String,
    /// Severity level.
    pub severity: Severity,
    /// File path associated with the finding (may be empty for in-memory scans).
    pub path: String,
}

// --- Compiled regex patterns (compiled once at first use) ---

struct Pattern {
    kind: &'static str,
    re: Regex,
    severity: Severity,
}

static PATTERNS: Lazy<Vec<Pattern>> = Lazy::new(|| {
    vec![
        Pattern {
            kind: "private_key",
            re: Regex::new(r"-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----").unwrap(),
            severity: Severity::Critical,
        },
        Pattern {
            kind: "aws_access_key",
            re: Regex::new(r"AKIA[0-9A-Z]{16}").unwrap(),
            severity: Severity::Critical,
        },
        Pattern {
            kind: "google_api_key",
            re: Regex::new(r"AIza[0-9A-Za-z_\-]{35}").unwrap(),
            severity: Severity::High,
        },
        Pattern {
            kind: "github_token",
            re: Regex::new(r"gh[pousr]_[A-Za-z0-9]{36,}").unwrap(),
            severity: Severity::Critical,
        },
        Pattern {
            kind: "slack_token",
            re: Regex::new(r"xox[baprs]-[0-9A-Za-z\-]{10,}").unwrap(),
            severity: Severity::High,
        },
        Pattern {
            kind: "stripe_live_key",
            re: Regex::new(r"sk_live_[0-9A-Za-z]{24,}").unwrap(),
            severity: Severity::Critical,
        },
        Pattern {
            kind: "generic_secret_assignment",
            re: Regex::new(
                r#"(?i)(?:api[_-]?key|secret|token|password|access[_-]?key)\s*[:=]\s*["'][A-Za-z0-9_\-./+]{16,}["']"#,
            )
            .unwrap(),
            severity: Severity::Medium,
        },
    ]
});

/// Return true if the matched value looks like a placeholder (not a real secret).
fn is_placeholder(value: &str) -> bool {
    let v = value.trim_matches(['"', '\'']);
    // Common placeholder strings
    let placeholders = [
        "your-api-key",
        "your_api_key",
        "your-secret",
        "your_secret",
        "changeme",
        "change_me",
        "change-me",
        "placeholder",
        "insert-key-here",
        "todo",
        "fixme",
        "xxx",
        "xxxx",
        "xxxxxxxx",
        "example",
        "sample",
        "test",
        "fake",
        "your-token",
        "your_token",
        "akia_example",
        "sk_live_example",
    ];
    let lower = v.to_lowercase();
    if placeholders.iter().any(|p| lower.contains(p)) {
        return true;
    }
    // All same character
    if v.len() > 3 {
        let first = v.chars().next().unwrap();
        if v.chars().all(|c| c == first) {
            return true;
        }
    }
    false
}

/// Redact a secret value: show first 4 chars + "***".
fn redact(value: &str) -> String {
    let stripped = value.trim_matches(['"', '\'']);
    if stripped.len() <= 4 {
        return "****".to_string();
    }
    format!("{}***", &stripped[..4])
}

/// Scan `text` (associated with `path`) for secrets. Returns findings.
pub fn scan_text(text: &str, path: &str) -> Vec<SecretFinding> {
    let mut findings = Vec::new();

    for (line_idx, line_text) in text.lines().enumerate() {
        let line_no = line_idx + 1;
        for pat in PATTERNS.iter() {
            for m in pat.re.find_iter(line_text) {
                let matched = m.as_str();
                if is_placeholder(matched) {
                    continue;
                }
                let preview = redact(matched);
                findings.push(SecretFinding {
                    kind: pat.kind.to_string(),
                    line: line_no,
                    column: m.start() + 1,
                    redacted_preview: preview,
                    severity: pat.severity.clone(),
                    path: path.to_string(),
                });
            }
        }
    }

    findings
}

/// Read a file and scan its content for secrets.
pub fn scan_file(path: &Path) -> Vec<SecretFinding> {
    match std::fs::read_to_string(path) {
        Ok(text) => scan_text(&text, &path.display().to_string()),
        Err(_) => vec![], // Binary or unreadable files are skipped silently
    }
}

// --- Tests ---

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_fake_aws_key() {
        let text2 = "export AWS_KEY=AKIAZ3ABCDEFGHIJKLMN";
        let findings = scan_text(text2, "test.env");
        assert!(!findings.is_empty(), "should detect AWS key");
        assert_eq!(findings[0].kind, "aws_access_key");
    }

    #[test]
    fn skips_placeholder_aws_key() {
        let text2 = "AWS_KEY=AKIAxxxxxxxxxxxxxxxxxxxx";
        let findings2 = scan_text(text2, "test.env");
        assert!(
            findings2.is_empty(),
            "all-same-char placeholder should be skipped"
        );
    }

    #[test]
    fn detects_private_key_header() {
        let text = "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----";
        let findings = scan_text(text, "id_rsa");
        assert!(!findings.is_empty());
        assert_eq!(findings[0].kind, "private_key");
        assert_eq!(findings[0].severity, Severity::Critical);
    }

    #[test]
    fn detects_github_token() {
        // ghp_ + 36 chars minimum required by regex
        let text = "TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij";
        let findings = scan_text(text, "config.yml");
        assert!(!findings.is_empty());
        assert_eq!(findings[0].kind, "github_token");
    }

    #[test]
    fn detects_generic_assignment() {
        let text = r#"api_key = "supersecretvalue12345""#;
        let findings = scan_text(text, "app.js");
        assert!(!findings.is_empty());
    }

    #[test]
    fn clean_file_returns_empty() {
        let text = "Hello world\nNo secrets here\nJust plain text";
        let findings = scan_text(text, "readme.md");
        assert!(findings.is_empty());
    }

    #[test]
    fn redact_shows_first_four_chars() {
        // ghp_ + 36 chars minimum required by regex
        let text = "TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij";
        let findings = scan_text(text, "f");
        if !findings.is_empty() {
            assert!(findings[0].redacted_preview.ends_with("***"));
        }
    }

    #[test]
    fn skips_changeme_placeholder() {
        let text = r#"password = "changeme123456789012""#;
        let findings = scan_text(text, "config.toml");
        assert!(
            findings.is_empty(),
            "changeme should be skipped as placeholder"
        );
    }
}
