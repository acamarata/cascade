//! Error leak detection — stack traces and DB errors in HTTP response contexts.
//!
//! # Purpose
//! Best-effort heuristic scan for obvious development/debug information that
//! might be returned to HTTP clients in production (stack traces, raw SQL errors,
//! Python tracebacks, etc.).
//!
//! # Severity: Low — these are heuristics, not guarantees.

use serde::{Deserialize, Serialize};

/// A potential error-information leak finding.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ErrorLeakFinding {
    pub pattern: String,
    pub line: usize,
    pub snippet: String,
    pub severity: String,
}

/// Patterns that suggest debug/error output in response context.
const ERROR_PATTERNS: &[(&str, &str)] = &[
    ("raw_sql", "SELECT * FROM"),
    ("traceback", "Traceback (most recent call last)"),
    ("js_stacktrace", "at Object.<anonymous>"),
    ("java_stacktrace", "at java."),
    ("ruby_stacktrace", "app/controllers/"),
    ("php_error", "Fatal error:"),
    ("node_error", "Error: Cannot find module"),
    ("django_debug", "Django Version:"),
    ("rails_error", "ActionController::"),
];

/// Scan `text` for patterns suggesting error/debug info leaks in HTTP response context.
pub fn scan_error_leak(text: &str) -> Vec<ErrorLeakFinding> {
    let mut findings = Vec::new();

    for (line_idx, line_text) in text.lines().enumerate() {
        let line_no = line_idx + 1;
        for (pattern_name, pattern) in ERROR_PATTERNS {
            if line_text.contains(pattern) {
                let snippet = if line_text.len() > 80 {
                    format!("{}...", &line_text[..80])
                } else {
                    line_text.to_string()
                };
                findings.push(ErrorLeakFinding {
                    pattern: pattern_name.to_string(),
                    line: line_no,
                    snippet,
                    severity: "low".to_string(),
                });
            }
        }
    }

    findings
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_raw_sql() {
        let text = "response.send('SELECT * FROM users WHERE id=' + id)";
        let findings = scan_error_leak(text);
        assert!(!findings.is_empty());
        assert_eq!(findings[0].pattern, "raw_sql");
    }

    #[test]
    fn detects_python_traceback() {
        let text = "Traceback (most recent call last):\n  File 'app.py'";
        let findings = scan_error_leak(text);
        assert!(!findings.is_empty());
    }

    #[test]
    fn clean_text_returns_empty() {
        let text = "Hello world\nreturn res.json({ ok: true })";
        let findings = scan_error_leak(text);
        assert!(findings.is_empty());
    }
}
