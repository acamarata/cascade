//! [`RedactionPipeline`] — strips PII and secrets from content before indexing.
//!
//! # Purpose
//!
//! Applied to chunk text (and optionally raw document text) **before** embeddings
//! are computed and stored.  Prevents PII (email addresses, US SSNs) and common
//! secrets (API keys, bearer tokens) from entering the vector store or being
//! returned in retrieval results.
//!
//! # Approach
//!
//! Two complementary detection layers:
//!
//! 1. **Regex patterns** — fast, deterministic, zero false-negatives for
//!    well-known formats (email, SSN, bearer tokens, common key prefixes).
//! 2. **Entropy filter** — catches high-entropy strings (≥ 3.5 bits/char over
//!    the printable-ASCII alphabet) that look like random secrets even without a
//!    known prefix.  Applied only to candidate tokens of 20–64 chars.
//!
//! # Inputs / Outputs
//!
//! - `redact(text: &str) → String` — returns a copy of `text` with detected
//!   secrets replaced by a `[REDACTED]` placeholder.  The replacement preserves
//!   the surrounding context so retrieval quality is minimally degraded.
//!
//! # Constraints
//!
//! - All regexes are compiled once at construction via `once_cell::sync::Lazy`
//!   and reused for the lifetime of the pipeline — zero per-call compilation.
//! - The entropy filter is an opt-in field on [`RedactionConfig`]; it defaults
//!   to `true` but can be disabled for environments where false positives matter
//!   more than secret leakage.
//! - This pass is **best-effort**: it catches the most common formats but is not
//!   a replacement for a dedicated DLP (Data Loss Prevention) system.
//!
//! # SPORT
//!
//! MASTER-COMPONENTS.md → cascade-rag::privacy::RedactionPipeline (T-RAG-07)

use once_cell::sync::Lazy;
use regex::Regex;

// ── Compiled patterns (lazy, thread-safe) ────────────────────────────────────

/// US Social Security Numbers: `NNN-NN-NNNN` or `NNN NN NNNN`.
static RE_SSN: Lazy<Regex> =
    Lazy::new(|| Regex::new(r"\b\d{3}[-\s]\d{2}[-\s]\d{4}\b").expect("SSN regex must compile"));

/// Email addresses.
static RE_EMAIL: Lazy<Regex> = Lazy::new(|| {
    Regex::new(r"\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b")
        .expect("email regex must compile")
});

/// Bearer / Authorization header tokens.
/// Matches: `Bearer <token>` or `Authorization: Bearer <token>`.
static RE_BEARER: Lazy<Regex> = Lazy::new(|| {
    Regex::new(r"(?i)\bbearer\s+[A-Za-z0-9\-._~+/]+=*\b").expect("bearer regex must compile")
});

/// Common API key / secret prefixes used by major providers.
///
/// Covers: `sk-`, `pk-`, `rk-`, `ghp_`, `gho_`, `ghs_`, `ghu_`, `github_pat_`,
/// `xoxb-`, `xoxp-` (Slack), `AIza` (Google), `AKIA` (AWS access key ID).
static RE_API_KEY: Lazy<Regex> = Lazy::new(|| {
    Regex::new(
        r"\b(?:sk-|pk-|rk-|ghp_|gho_|ghs_|ghu_|github_pat_|xoxb-|xoxp-|AIza|AKIA)[A-Za-z0-9\-_]{8,}\b",
    )
    .expect("API key regex must compile")
});

// ── Config ────────────────────────────────────────────────────────────────────

/// Configuration for the redaction pipeline.
///
/// # Defaults
///
/// All regex patterns are enabled; entropy filter is enabled with a threshold
/// of 3.5 bits/char (empirically separates random tokens from prose).
#[derive(Debug, Clone)]
pub struct RedactionConfig {
    /// Strip US Social Security Numbers (NNN-NN-NNNN).
    pub redact_ssn: bool,
    /// Strip email addresses.
    pub redact_email: bool,
    /// Strip `Bearer <token>` authorization header values.
    pub redact_bearer: bool,
    /// Strip strings matching common API key prefixes (`sk-`, `ghp_`, `AKIA`, …).
    pub redact_api_keys: bool,
    /// Apply Shannon entropy filter to catch high-entropy random strings.
    pub redact_high_entropy: bool,
    /// Minimum token length (chars) for the entropy filter.
    pub entropy_min_len: usize,
    /// Maximum token length (chars) for the entropy filter.
    pub entropy_max_len: usize,
    /// Shannon entropy threshold (bits/char) above which a token is redacted.
    pub entropy_threshold: f64,
    /// Replacement string inserted in place of detected secrets.
    pub placeholder: String,
}

impl Default for RedactionConfig {
    fn default() -> Self {
        Self {
            redact_ssn: true,
            redact_email: true,
            redact_bearer: true,
            redact_api_keys: true,
            redact_high_entropy: true,
            entropy_min_len: 20,
            entropy_max_len: 64,
            entropy_threshold: 3.5,
            placeholder: "[REDACTED]".to_string(),
        }
    }
}

// ── RedactionPipeline ─────────────────────────────────────────────────────────

/// Stateless (after construction) redaction engine.
///
/// # Purpose
///
/// Strips PII and secrets from chunk text before it is stored in the vector
/// index or returned to callers.  Designed to be constructed once and shared
/// via `Arc<RedactionPipeline>` across threads.
///
/// # Example
///
/// ```rust
/// use cascade_rag::privacy::RedactionPipeline;
///
/// let rp = RedactionPipeline::default();
/// let clean = rp.redact("Contact me at alice@example.com or sk-abc1234567890xyz");
/// assert!(!clean.contains("alice@example.com"));
/// assert!(!clean.contains("sk-abc1234567890xyz"));
/// ```
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::privacy::RedactionPipeline
#[derive(Debug, Clone)]
pub struct RedactionPipeline {
    config: RedactionConfig,
}

impl RedactionPipeline {
    /// Construct with the supplied configuration.
    pub fn new(config: RedactionConfig) -> Self {
        Self { config }
    }

    /// Redact PII and secrets from `text`.
    ///
    /// # Algorithm
    ///
    /// 1. Apply SSN regex (if enabled).
    /// 2. Apply email regex (if enabled).
    /// 3. Apply bearer-token regex (if enabled).
    /// 4. Apply API-key prefix regex (if enabled).
    /// 5. Tokenise on whitespace, apply entropy filter (if enabled).
    ///
    /// Each pass replaces matches with [`RedactionConfig::placeholder`].
    ///
    /// # Inputs
    ///
    /// - `text` — raw chunk or document text.
    ///
    /// # Outputs
    ///
    /// A new `String` with detected PII/secrets replaced by the placeholder.
    pub fn redact(&self, text: &str) -> String {
        let ph = &self.config.placeholder;

        let mut s = text.to_owned();

        // 1. SSN.
        if self.config.redact_ssn {
            s = RE_SSN.replace_all(&s, ph.as_str()).into_owned();
        }

        // 2. Email.
        if self.config.redact_email {
            s = RE_EMAIL.replace_all(&s, ph.as_str()).into_owned();
        }

        // 3. Bearer token (before generic API key — more specific).
        if self.config.redact_bearer {
            s = RE_BEARER.replace_all(&s, ph.as_str()).into_owned();
        }

        // 4. API key prefix patterns.
        if self.config.redact_api_keys {
            s = RE_API_KEY.replace_all(&s, ph.as_str()).into_owned();
        }

        // 5. Entropy filter — word-by-word scan.
        if self.config.redact_high_entropy {
            s = self.redact_by_entropy(&s);
        }

        s
    }

    // ── Private helpers ───────────────────────────────────────────────────────

    /// Replace whitespace-delimited tokens whose Shannon entropy exceeds the
    /// configured threshold with the placeholder.
    ///
    /// WHY word-level (not char-level sliding window): secrets in real text are
    /// typically whitespace-delimited tokens (a password in a config file, a JWT
    /// segment).  Sub-word scanning would produce too many false positives on
    /// technical prose.
    fn redact_by_entropy(&self, text: &str) -> String {
        let cfg = &self.config;
        let ph = &cfg.placeholder;

        // Split on whitespace, process each token.
        // Reconstruct the string preserving separators by working on the
        // char-by-char boundary of whitespace runs.
        let mut result = String::with_capacity(text.len());
        let mut token_buf = String::new();

        // Iterate over words (split_whitespace loses separator info), so we
        // instead scan manually to preserve whitespace.
        let chars: Vec<char> = text.chars().collect();
        let n = chars.len();
        let mut i = 0;

        while i < n {
            if chars[i].is_whitespace() {
                // Flush accumulated non-whitespace token.
                if !token_buf.is_empty() {
                    let token = std::mem::take(&mut token_buf);
                    if self.should_redact_by_entropy(&token) {
                        result.push_str(ph);
                    } else {
                        result.push_str(&token);
                    }
                }
                result.push(chars[i]);
                i += 1;
            } else {
                token_buf.push(chars[i]);
                i += 1;
            }
        }
        // Flush final token.
        if !token_buf.is_empty() {
            if self.should_redact_by_entropy(&token_buf) {
                result.push_str(ph);
            } else {
                result.push_str(&token_buf);
            }
        }

        result
    }

    /// Return `true` if `token` is within the configured length window and its
    /// Shannon entropy (over its character alphabet) meets or exceeds the threshold.
    ///
    /// # Algorithm
    ///
    /// Shannon entropy H = -Σ p(c) log₂ p(c) for each unique character c.
    fn should_redact_by_entropy(&self, token: &str) -> bool {
        let len = token.chars().count();
        if len < self.config.entropy_min_len || len > self.config.entropy_max_len {
            return false;
        }
        shannon_entropy(token) >= self.config.entropy_threshold
    }
}

impl Default for RedactionPipeline {
    fn default() -> Self {
        Self::new(RedactionConfig::default())
    }
}

// ── Shannon entropy helper ────────────────────────────────────────────────────

/// Compute the Shannon entropy of `s` in bits per character.
///
/// Returns the character-level entropy: H = -Σ p(c) log₂ p(c) where p(c) is
/// the relative frequency of character c in `s`.
///
/// Returns 0.0 for empty strings.
pub(crate) fn shannon_entropy(s: &str) -> f64 {
    if s.is_empty() {
        return 0.0;
    }

    let mut freq = std::collections::HashMap::<char, usize>::new();
    let mut total = 0usize;

    for c in s.chars() {
        *freq.entry(c).or_insert(0) += 1;
        total += 1;
    }

    let n = total as f64;
    freq.values().fold(0.0_f64, |acc, &count| {
        let p = count as f64 / n;
        acc - p * p.log2()
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn rp() -> RedactionPipeline {
        RedactionPipeline::default()
    }

    // ── redaction::ssn ────────────────────────────────────────────────────────

    #[test]
    fn redact_ssn_hyphen() {
        let out = rp().redact("Patient SSN: 123-45-6789 is confidential.");
        assert!(
            !out.contains("123-45-6789"),
            "hyphenated SSN must be redacted"
        );
        assert!(out.contains("[REDACTED]"), "placeholder must appear");
    }

    #[test]
    fn redact_ssn_space() {
        let out = rp().redact("SSN 123 45 6789");
        assert!(
            !out.contains("123 45 6789"),
            "space-delimited SSN must be redacted"
        );
    }

    // ── redaction::email ──────────────────────────────────────────────────────

    #[test]
    fn redact_email_address() {
        let out = rp().redact("Send invoice to billing@acme.com please.");
        assert!(!out.contains("billing@acme.com"), "email must be redacted");
        assert!(out.contains("[REDACTED]"));
    }

    #[test]
    fn redact_email_preserves_surrounding_text() {
        let out = rp().redact("Contact alice@example.org for details.");
        assert!(!out.contains("alice@example.org"));
        // Surrounding words must be untouched.
        assert!(out.contains("Contact"), "surrounding text preserved");
        assert!(out.contains("for details."), "trailing text preserved");
    }

    // ── redaction::api_key ────────────────────────────────────────────────────

    #[test]
    fn redact_openai_sk_key() {
        let secret = "sk-abc1234567890xyzABCDEF";
        let out = rp().redact(&format!("Use key {secret} for requests."));
        assert!(!out.contains(secret), "sk- API key must be redacted");
    }

    #[test]
    fn redact_github_token() {
        let secret = "ghp_abcdefABCDEF1234567890xyz";
        let out = rp().redact(&format!("GITHUB_TOKEN={secret}"));
        assert!(!out.contains(secret), "GitHub token must be redacted");
    }

    #[test]
    fn redact_aws_access_key() {
        let secret = "AKIAIOSFODNN7EXAMPLE";
        let out = rp().redact(&format!("AWS access key: {secret}"));
        assert!(!out.contains(secret), "AWS AKIA key must be redacted");
    }

    // ── redaction::bearer ─────────────────────────────────────────────────────

    #[test]
    fn redact_bearer_token() {
        let out = rp().redact("Authorization: Bearer eyJhbGciOiJSUzI1NiJ9.payload.sig");
        assert!(
            !out.contains("eyJhbGciOiJSUzI1NiJ9"),
            "bearer token must be redacted"
        );
    }

    // ── redaction::entropy ────────────────────────────────────────────────────

    #[test]
    fn known_secret_absent_after_redaction() {
        // High-entropy 32-char random string — should be caught by entropy filter.
        let secret = "xK9mP2vQnR7sT4wY1uZeAhBcDfGjLo8";
        let out = rp().redact(&format!("password={secret} ok"));
        // Either the entropy filter or prefix detection caught it.
        assert!(
            !out.contains(secret),
            "high-entropy secret must be absent post-redaction; got: {out}"
        );
    }

    #[test]
    fn normal_prose_not_redacted() {
        let prose = "The quick brown fox jumps over the lazy dog and the sun shines bright today.";
        let out = rp().redact(prose);
        // No SSNs, emails, keys, or high-entropy tokens in this string.
        assert_eq!(out, prose, "plain prose must pass through unchanged");
    }

    // ── redaction::shannon_entropy ────────────────────────────────────────────

    #[test]
    fn entropy_empty_string_zero() {
        assert_eq!(shannon_entropy(""), 0.0);
    }

    #[test]
    fn entropy_uniform_string_high() {
        // 32 completely random-looking chars should have high entropy.
        let h = shannon_entropy("aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpP");
        assert!(
            h >= 3.5,
            "high-entropy string expected >= 3.5 bits/char, got {h}"
        );
    }

    #[test]
    fn entropy_repeated_char_low() {
        let h = shannon_entropy("aaaaaaaaaaaaaaaaaaaaaa");
        assert!(h < 0.5, "repeated char entropy must be near 0, got {h}");
    }
}
