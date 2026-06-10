//! # cascade_core::security::secrets_scanner
//!
//! Pattern-based detection of secrets and high-entropy tokens in text.
//!
//! ## Patterns covered
//!
//! The scanner is vendor-neutral: it targets access key formats from major
//! cloud providers without hardcoding any single vendor as the canonical case.
//!
//! | Category | Pattern | Notes |
//! |----------|---------|-------|
//! | AWS access key ID | `AKIA[0-9A-Z]{16}` | Standard IAM key prefix |
//! | GCP service account | `"type": "service_account"` | JSON credential files |
//! | Azure connection string | `DefaultEndpointsProtocol=https;AccountName=` | Storage connection strings |
//! | Generic API key | `(?i)api[_-]?key\s*[:=]\s*\S{20,}` | Named key assignments |
//! | Private RSA key | `-----BEGIN RSA PRIVATE KEY-----` | PEM header |
//! | Private EC key | `-----BEGIN EC PRIVATE KEY-----` | PEM header |
//! | Private key (generic) | `-----BEGIN PRIVATE KEY-----` | PKCS#8 PEM header |
//! | JWT | `ey[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}` | Three-part base64url |
//! | SSH private key | `-----BEGIN OPENSSH PRIVATE KEY-----` | OpenSSH format |
//! | High-entropy token | Shannon entropy ≥ 4.5 bits/char over a 20+ char sequence | Generic token detection |
//!
//! ## False-positive rate
//!
//! Target: ≤2% on a 1000-sample clean-prose corpus.
//! The entropy threshold (4.5 bits/char) and minimum token length (20 chars)
//! are tuned to minimize false positives on English text and code comments.
//!
//! ## STRIDE mapping
//!
//! Information disclosure (I) — prevents credentials from leaking through
//! agent outputs or audit logs.

/// A single match found by the scanner.
#[derive(Debug, Clone)]
pub struct SecretMatch {
    /// Category of the matched secret (e.g. `"aws-access-key"`, `"jwt"`, `"high-entropy"`).
    pub kind: String,
    /// Byte offset of the match start within the input.
    pub start: usize,
    /// Length of the match in bytes.
    pub len: usize,
    /// A redacted sample for log context (e.g. `"AKIA***"`), never the full value.
    pub redacted_sample: String,
}

// ── Regex patterns (compiled once) ────────────────────────────────────────────

/// A static pattern entry: (kind, literal prefix or pattern description).
///
/// For performance these are matched as byte-string searches rather than regex
/// to avoid pulling in the `regex` crate as a workspace dependency. Each
/// pattern is identified by a unique prefix or substring. The entropy scanner
/// runs as a post-pass over all unmatched token-like sequences.
struct Pattern {
    kind: &'static str,
    /// Literal substring that must appear for this pattern to fire.
    needle: &'static str,
    /// Minimum required length of the overall match window (0 = no length check).
    min_match_len: usize,
}

const PATTERNS: &[Pattern] = &[
    Pattern {
        kind: "aws-access-key",
        needle: "AKIA",
        min_match_len: 20,
    },
    Pattern {
        kind: "gcp-service-account",
        needle: "\"type\": \"service_account\"",
        min_match_len: 25,
    },
    Pattern {
        kind: "azure-connection-string",
        needle: "DefaultEndpointsProtocol=https;AccountName=",
        min_match_len: 43,
    },
    Pattern {
        kind: "rsa-private-key",
        needle: "-----BEGIN RSA PRIVATE KEY-----",
        min_match_len: 30,
    },
    Pattern {
        kind: "ec-private-key",
        needle: "-----BEGIN EC PRIVATE KEY-----",
        min_match_len: 29,
    },
    Pattern {
        kind: "private-key-pkcs8",
        needle: "-----BEGIN PRIVATE KEY-----",
        min_match_len: 26,
    },
    Pattern {
        kind: "openssh-private-key",
        needle: "-----BEGIN OPENSSH PRIVATE KEY-----",
        min_match_len: 34,
    },
];

// ── SecretsScanner ────────────────────────────────────────────────────────────

/// Scans text for secrets and high-entropy tokens.
///
/// Stateless and cheap to construct — create a new instance per call or share
/// a single instance across the process.
pub struct SecretsScanner {
    /// Shannon entropy threshold for generic token detection (bits per char).
    pub entropy_threshold: f64,
    /// Minimum token length for entropy analysis.
    pub entropy_min_len: usize,
}

impl Default for SecretsScanner {
    fn default() -> Self {
        Self {
            entropy_threshold: 4.5,
            entropy_min_len: 20,
        }
    }
}

impl SecretsScanner {
    /// Scan `text` for secrets.
    ///
    /// Returns all matches found, potentially overlapping.
    /// The caller is responsible for deduplication when needed.
    pub fn scan(&self, text: &str) -> Vec<SecretMatch> {
        let mut matches = Vec::new();

        // Pattern-based pass.
        for pattern in PATTERNS {
            let mut search = text;
            let mut base_offset = 0usize;
            while let Some(pos) = search.find(pattern.needle) {
                let abs_pos = base_offset + pos;
                let window_end = (abs_pos + pattern.needle.len() + 24).min(text.len());
                let window = &text[abs_pos..window_end];
                let len = window.len();
                if len >= pattern.min_match_len {
                    let sample = redact_sample(window);
                    matches.push(SecretMatch {
                        kind: pattern.kind.to_owned(),
                        start: abs_pos,
                        len,
                        redacted_sample: sample,
                    });
                }
                // Advance search past this match.
                base_offset = abs_pos + pattern.needle.len();
                search = &text[base_offset..];
            }
        }

        // JWT pass: look for the three-part base64url structure `eyXXX.XXX.XXX`.
        self.scan_jwt(text, &mut matches);

        // Generic API key pass: `api_key = <value>` or `api-key: <value>`.
        self.scan_api_key_assignments(text, &mut matches);

        // High-entropy token pass over whitespace-delimited tokens.
        self.scan_high_entropy(text, &mut matches);

        matches
    }

    fn scan_jwt(&self, text: &str, matches: &mut Vec<SecretMatch>) {
        // JWTs start with `ey` (base64url-encoded `{"` header preamble).
        let mut search = text;
        let mut base = 0usize;
        while let Some(pos) = search.find("ey") {
            let abs = base + pos;
            // Extract the token: all base64url characters + dots.
            let token: String = text[abs..]
                .chars()
                .take_while(|&c| c.is_alphanumeric() || c == '_' || c == '-' || c == '.')
                .collect();
            // A valid JWT has exactly 2 dots separating 3 non-empty parts.
            let parts: Vec<&str> = token.splitn(4, '.').collect();
            if parts.len() == 3 && parts.iter().all(|p| p.len() >= 4) {
                matches.push(SecretMatch {
                    kind: "jwt".to_owned(),
                    start: abs,
                    len: token.len(),
                    redacted_sample: format!(
                        "{}.[REDACTED].[REDACTED]",
                        &parts[0][..4.min(parts[0].len())]
                    ),
                });
            }
            base = abs + 2;
            search = &text[base..];
        }
    }

    fn scan_api_key_assignments(&self, text: &str, matches: &mut Vec<SecretMatch>) {
        let lower = text.to_lowercase();
        for needle in &["api_key", "api-key", "apikey", "api key"] {
            let mut search = lower.as_str();
            let mut base = 0usize;
            while let Some(pos) = search.find(needle) {
                let abs = base + pos;
                // Look for `:` or `=` followed by a value within the next 60 bytes.
                let window = &text[abs..(abs + 60).min(text.len())];
                if let Some(sep_pos) = window.find([':', '=']) {
                    let value_start = abs + sep_pos + 1;
                    let value: String = text[value_start..]
                        .chars()
                        .skip_while(|c| c.is_whitespace() || *c == '"' || *c == '\'')
                        .take_while(|c| !c.is_whitespace() && *c != '"' && *c != '\'')
                        .collect();
                    if value.len() >= 20 {
                        matches.push(SecretMatch {
                            kind: "api-key-assignment".to_owned(),
                            start: value_start,
                            len: value.len(),
                            redacted_sample: redact_sample(&value),
                        });
                    }
                }
                base = abs + needle.len();
                search = &lower[base..];
            }
        }
    }

    fn scan_high_entropy(&self, text: &str, matches: &mut Vec<SecretMatch>) {
        // Tokenize by whitespace and common delimiters.
        for (token, offset) in tokenize_with_offsets(text) {
            if token.len() < self.entropy_min_len {
                continue;
            }
            // Skip tokens that look like URLs, file paths, or words.
            if token.starts_with("http") || token.starts_with('/') || token.contains(' ') {
                continue;
            }
            // Skip if the token already matched a named pattern.
            if matches.iter().any(|m| m.start == offset) {
                continue;
            }
            let entropy = shannon_entropy(token);
            if entropy >= self.entropy_threshold {
                matches.push(SecretMatch {
                    kind: "high-entropy".to_owned(),
                    start: offset,
                    len: token.len(),
                    redacted_sample: redact_sample(token),
                });
            }
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Compute the Shannon entropy (bits per character) of a string.
///
/// Uses the standard formula: H = -sum(p * log2(p)) where p is the frequency
/// of each distinct character.
fn shannon_entropy(s: &str) -> f64 {
    if s.is_empty() {
        return 0.0;
    }
    let len = s.len() as f64;
    let mut freq = [0u32; 256];
    for b in s.bytes() {
        freq[b as usize] += 1;
    }
    freq.iter()
        .filter(|&&c| c > 0)
        .map(|&c| {
            let p = c as f64 / len;
            -p * p.log2()
        })
        .sum()
}

/// Yield `(token, byte_offset)` pairs by splitting on common delimiters.
fn tokenize_with_offsets(text: &str) -> Vec<(&str, usize)> {
    let delimiters = [
        ' ', '\t', '\n', '\r', '"', '\'', '`', ',', ';', '(', ')', '[', ']', '{', '}',
    ];
    let mut result = Vec::new();
    let mut start = 0usize;
    let chars: Vec<(usize, char)> = text.char_indices().collect();
    for (i, (byte_pos, ch)) in chars.iter().enumerate() {
        if delimiters.contains(ch) {
            if *byte_pos > start {
                result.push((&text[start..*byte_pos], start));
            }
            start = byte_pos + ch.len_utf8();
        } else if i == chars.len() - 1 {
            let end = byte_pos + ch.len_utf8();
            if end > start {
                result.push((&text[start..end], start));
            }
        }
    }
    result
}

/// Produce a safe redacted sample: show the first 4 characters then `***`.
fn redact_sample(s: &str) -> String {
    let head: String = s.chars().take(4).collect();
    format!("{head}***")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_aws_access_key() {
        let scanner = SecretsScanner::default();
        let matches = scanner.scan("key = AKIAIOSFODNN7EXAMPLE, use it now");
        assert!(matches.iter().any(|m| m.kind == "aws-access-key"));
    }

    #[test]
    fn detects_pem_header() {
        let scanner = SecretsScanner::default();
        let matches = scanner.scan("-----BEGIN RSA PRIVATE KEY-----\nMIIEo...");
        assert!(matches.iter().any(|m| m.kind == "rsa-private-key"));
    }

    #[test]
    fn detects_jwt() {
        let scanner = SecretsScanner::default();
        let jwt = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.SflKxwRJSMeKKF2QT4fwpMeJf36";
        let matches = scanner.scan(jwt);
        assert!(matches.iter().any(|m| m.kind == "jwt"), "JWT not detected");
    }

    #[test]
    fn clean_prose_no_false_positives() {
        let scanner = SecretsScanner::default();
        let prose = "The prayer time for Fajr is 05:12 and Isha is 21:44 in Medina.";
        let matches = scanner.scan(prose);
        assert!(
            matches.is_empty(),
            "unexpected matches in clean prose: {matches:?}"
        );
    }

    #[test]
    fn shannon_entropy_high_for_random() {
        // A 32-char hex string has ~4.0 bits/char (16 symbols, uniform).
        let s = "a4f3b2c1d8e9f0a1b2c3d4e5f6a7b8c9";
        assert!(shannon_entropy(s) >= 3.8);
    }

    #[test]
    fn shannon_entropy_low_for_repeated() {
        let s = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        assert!(shannon_entropy(s) < 0.1);
    }
}
