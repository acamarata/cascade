//! SimplePolicyEvaluator — pattern-based evaluator with injection resilience.
//!
//! Purpose: Enforce the default deny-dangerous policy via literal substring
//!   matching against a normalised form of the serialized `PolicyAction`.
//!   Used when wasmtime is unavailable or no WASM policy file is present.
//!   Also serves as the fallback when the `wasm-policy` feature is not compiled.
//! Inputs: `PolicyAction` (serialized to JSON; normalised before matching).
//! Outputs: `PolicyResult::Deny` if any dangerous pattern matches, else Allow.
//! Constraints:
//!   - No regex — literal `contains` checks only (avoids ReDoS).
//!   - Zero new external dependencies — all helpers implemented inline.
//!   - Input is **normalised** before matching: base64 tokens are decoded,
//!     URL-percent-encoded bytes are unescaped, and common unicode homoglyphs
//!     are mapped to their ASCII equivalents.
//!   - Chained commands (`;`, `&&`, `||`, `|`, newlines) are split into
//!     individual segments; each segment is evaluated independently so a
//!     chain cannot smuggle a blocked action past a single-command check.
//!   - `DELETE FROM` without a `WHERE` clause is detected per-segment.
//!   - All SQL tokens are compared case-insensitively (uppercased before match).
//!
//! SPORT: MASTER-POLICIES.md

use cascade_types::policy::{PolicyAction, PolicyResult};

use crate::policy::engine::PolicyEvaluator;
use crate::policy::patterns::{all_patterns, DB_PATTERNS};

/// The policy ID reported by this evaluator.
pub const POLICY_ID: &str = "default-deny-dangerous";

// ── Injection normalisation ───────────────────────────────────────────────────

/// Decode a standard base64-encoded string (alphabet A-Z a-z 0-9 + /).
///
/// Returns the decoded UTF-8 text, or `None` if the input is not valid base64
/// or the decoded bytes are not valid UTF-8.  Only attempts decode when the
/// token is >= 8 chars and every char (ignoring `=` padding) is a valid
/// base64 character.
fn try_b64_decode(s: &str) -> Option<String> {
    /// Map a base64 character to its 6-bit value, or `None` if invalid.
    fn b64_val(b: u8) -> Option<u8> {
        match b {
            b'A'..=b'Z' => Some(b - b'A'),
            b'a'..=b'z' => Some(b - b'a' + 26),
            b'0'..=b'9' => Some(b - b'0' + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    }

    // Strip trailing padding; reject very short tokens.
    let stripped = s.trim_end_matches('=');
    if stripped.len() < 8 {
        return None;
    }
    // All remaining characters must be valid base64.
    if !stripped.bytes().all(|b| b64_val(b).is_some()) {
        return None;
    }

    // Process every complete 4-char group and any short tail.
    let chars: Vec<u8> = stripped.bytes().map(|b| b64_val(b).unwrap_or(0)).collect();
    let mut out: Vec<u8> = Vec::with_capacity(chars.len() * 3 / 4 + 1);

    let mut i = 0;
    while i + 3 < chars.len() {
        // Full 4-symbol group → 3 bytes.
        out.push((chars[i] << 2) | (chars[i + 1] >> 4));
        out.push((chars[i + 1] << 4) | (chars[i + 2] >> 2));
        out.push((chars[i + 2] << 6) | chars[i + 3]);
        i += 4;
    }
    // Tail: 2 remaining symbols → 1 byte; 3 symbols → 2 bytes.
    match chars.len() - i {
        2 => {
            out.push((chars[i] << 2) | (chars[i + 1] >> 4));
        }
        3 => {
            out.push((chars[i] << 2) | (chars[i + 1] >> 4));
            out.push((chars[i + 1] << 4) | (chars[i + 2] >> 2));
        }
        _ => {}
    }

    String::from_utf8(out).ok()
}

/// Expand any base64-encoded tokens embedded in `text`.
///
/// Splits on whitespace, attempts to decode each token individually, and
/// reassembles.  Catches `eval $(echo "cm0gLXJmIC8=" | base64 -d)` style
/// injections where a dangerous command is base64-encoded inline.
fn expand_base64(text: &str) -> String {
    text.split_whitespace()
        .map(|token| {
            // Strip common command-substitution wrappers before decode.
            let inner = token
                .trim_start_matches("$(echo")
                .trim_start_matches("$(")
                .trim_end_matches(')')
                .trim_matches('`')
                .trim();
            try_b64_decode(inner).unwrap_or_else(|| token.to_string())
        })
        .collect::<Vec<_>>()
        .join(" ")
}

/// Decode URL percent-encoding in `text` (e.g. `%20` → space, `%2F` → `/`).
///
/// Unrecognised or malformed sequences are passed through as-is so that the
/// normalisation pipeline is always infallible.
fn expand_url_encoding(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            let hi = hex_digit(bytes[i + 1]);
            let lo = hex_digit(bytes[i + 2]);
            if let (Some(h), Some(l)) = (hi, lo) {
                out.push((h << 4) | l);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8(out).unwrap_or_else(|_| text.to_string())
}

/// Convert an ASCII hex digit byte to its numeric value (0–15), or `None`.
fn hex_digit(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'A'..=b'F' => Some(b - b'A' + 10),
        b'a'..=b'f' => Some(b - b'a' + 10),
        _ => None,
    }
}

/// Map common unicode homoglyphs to their ASCII equivalents.
///
/// Covers the most common substitutions used to evade keyword filters:
/// - En/em dashes → ASCII hyphen.
/// - Full-width slash variants → `/`.
/// - Full-width tilde → `~`.
/// - Full-width space → regular space.
/// - Full-width ASCII letters/digits → half-width.
fn normalise_homoglyphs(text: &str) -> String {
    text.chars()
        .map(|c| match c {
            // En dash, em dash → hyphen.
            '\u{2013}' | '\u{2014}' => '-',
            // Unicode slash variants → ASCII slash.
            '\u{FF0F}' | '\u{29F8}' | '\u{2044}' => '/',
            // Full-width tilde → ~
            '\u{FF5E}' => '~',
            // Full-width space → regular space.
            '\u{3000}' => ' ',
            // Full-width ASCII letters A-Z (Ａ-Ｚ) → half-width.
            '\u{FF21}'..='\u{FF3A}' => {
                char::from_u32('A' as u32 + (c as u32 - 0xFF21)).unwrap_or(c)
            }
            // Full-width ASCII letters a-z (ａ-ｚ) → half-width.
            '\u{FF41}'..='\u{FF5A}' => {
                char::from_u32('a' as u32 + (c as u32 - 0xFF41)).unwrap_or(c)
            }
            // Full-width ASCII digits 0-9 (０-９) → half-width.
            '\u{FF10}'..='\u{FF19}' => {
                char::from_u32('0' as u32 + (c as u32 - 0xFF10)).unwrap_or(c)
            }
            other => other,
        })
        .collect()
}

/// Full normalisation pipeline applied to the serialised JSON before matching.
///
/// Order: homoglyph → URL-decode → base64-expand.
/// The pipeline is deterministic and infallible (never panics).
pub(crate) fn normalise(text: &str) -> String {
    let step1 = normalise_homoglyphs(text);
    let step2 = expand_url_encoding(&step1);
    expand_base64(&step2)
}

// ── Chained-command splitting ─────────────────────────────────────────────────

/// Split a command string on shell chaining operators: `;`, `&&`, `||`, `|`,
/// and newlines.  Returns individual segments with surrounding whitespace
/// trimmed.  Empty segments are discarded.
///
/// This prevents smuggling a blocked action in a longer chain:
///   `cargo build && rm -rf /`  →  `["cargo build", "rm -rf /"]`
pub(crate) fn split_chain(text: &str) -> Vec<&str> {
    let bytes = text.as_bytes();
    let len = bytes.len();
    let mut segments: Vec<&str> = Vec::new();
    let mut start = 0usize;
    let mut i = 0usize;

    while i < len {
        let split_at: Option<usize> = if i + 1 < len
            && ((bytes[i] == b'&' && bytes[i + 1] == b'&')
                || (bytes[i] == b'|' && bytes[i + 1] == b'|'))
        {
            Some(2)
        } else if bytes[i] == b';' || bytes[i] == b'|' || bytes[i] == b'\n' || bytes[i] == b'\r' {
            Some(1)
        } else {
            None
        };

        if let Some(op_len) = split_at {
            let seg = text[start..i].trim();
            if !seg.is_empty() {
                segments.push(seg);
            }
            start = i + op_len;
            i = start;
        } else {
            i += 1;
        }
    }
    // Tail segment.
    let tail = text[start..].trim();
    if !tail.is_empty() {
        segments.push(tail);
    }
    segments
}

// ── DELETE FROM without WHERE detection ──────────────────────────────────────

/// Returns true when a segment contains `DELETE FROM` (case-insensitive) and
/// does NOT contain `WHERE` (case-insensitive).
///
/// Safe invocations (`DELETE FROM sessions WHERE expires_at < NOW()`) pass
/// through; unbounded deletes (`DELETE FROM users`) are blocked.
fn is_delete_without_where(segment: &str) -> bool {
    let upper = segment.to_uppercase();
    upper.contains("DELETE FROM") && !upper.contains("WHERE")
}

// ── SimplePolicyEvaluator ─────────────────────────────────────────────────────

/// Default deny-dangerous evaluator using literal substring matching.
///
/// Evaluation proceeds in three stages:
///
/// 1. **Normalise**: apply homoglyph mapping, URL-decode, and base64-expand to
///    the full serialised `PolicyAction` JSON string.
/// 2. **Split**: divide the normalised string on shell chaining operators
///    (`;`, `&&`, `||`, `|`, newlines) into individual command segments.
/// 3. **Match**: test each segment against every pattern in the deny-list.
///    The first match produces a `Deny`; all segments must pass for an `Allow`.
///
/// # Example
///
/// ```
/// use cascade_harness::policy::simple::SimplePolicyEvaluator;
/// use cascade_harness::policy::engine::PolicyEvaluator;
/// use cascade_types::policy::{Decision, PolicyAction};
/// use serde_json::json;
///
/// let ev = SimplePolicyEvaluator::default();
/// let deny = ev.evaluate(&PolicyAction::new("bash", json!({"cmd": "rm -rf /"})));
/// assert_eq!(deny.decision, Decision::Deny);
///
/// let allow = ev.evaluate(&PolicyAction::new("bash", json!({"cmd": "cargo build"})));
/// assert_eq!(allow.decision, Decision::Allow);
/// ```
#[derive(Debug, Default, Clone)]
pub struct SimplePolicyEvaluator;

impl PolicyEvaluator for SimplePolicyEvaluator {
    fn evaluate(&self, action: &PolicyAction) -> PolicyResult {
        let serialized = action.to_json_string();
        let normalised = normalise(&serialized);

        // Evaluate each chained segment independently.
        for segment in split_chain(&normalised) {
            // Special case: DELETE FROM without WHERE.
            if is_delete_without_where(segment) {
                return PolicyResult::deny(
                    POLICY_ID,
                    "DELETE FROM without WHERE clause is forbidden",
                );
            }

            // Uppercase once per segment for case-insensitive DB pattern matching.
            let segment_upper = segment.to_uppercase();

            for (pattern, reason) in all_patterns() {
                // DB_PATTERNS are matched case-insensitively via the uppercased segment.
                let is_db = DB_PATTERNS.iter().any(|(p, _)| *p == pattern);
                let haystack: &str = if is_db { &segment_upper } else { segment };

                if haystack.contains(pattern) {
                    return PolicyResult::deny(POLICY_ID, reason);
                }
            }
        }

        PolicyResult::allow(POLICY_ID)
    }

    fn policy_id(&self) -> &str {
        POLICY_ID
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyAction};
    use serde_json::json;

    fn ev() -> SimplePolicyEvaluator {
        SimplePolicyEvaluator
    }

    fn bash(cmd: &str) -> PolicyAction {
        PolicyAction::new("bash", json!({"cmd": cmd}))
    }

    fn sql(query: &str) -> PolicyAction {
        PolicyAction::new("sql", json!({"query": query}))
    }

    // ── Filesystem patterns ───────────────────────────────────────────────────

    #[test]
    fn denies_rm_rf_root() {
        assert_eq!(ev().evaluate(&bash("rm -rf /")).decision, Decision::Deny);
    }

    #[test]
    fn denies_rm_rf_home_tilde() {
        assert_eq!(ev().evaluate(&bash("rm -rf ~")).decision, Decision::Deny);
    }

    #[test]
    fn denies_rm_rf_home_var() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf $HOME")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_rm_rf_volumes() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf /Volumes/MyDisk")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_rm_rf_dot_git() {
        let action = PolicyAction::new("bash", json!({"cmd": "rm -rf .git"}));
        let serialized = action.to_json_string();
        assert!(
            serialized.contains("rm -rf .git"),
            "serialized: {serialized}"
        );
        assert_eq!(ev().evaluate(&action).decision, Decision::Deny);
    }

    #[test]
    fn denies_dd_device() {
        assert_eq!(
            ev().evaluate(&bash("dd if=/dev/zero of=/dev/sda")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_mkfs() {
        assert_eq!(
            ev().evaluate(&bash("mkfs.ext4 /dev/sdb")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_diskutil_erase() {
        assert_eq!(
            ev().evaluate(&bash("diskutil eraseDisk APFS MyDisk /dev/disk2"))
                .decision,
            Decision::Deny
        );
    }

    /// Safe variant: rm -rf on well-known ephemeral directories must NOT be blocked.
    #[test]
    fn allows_rm_rf_node_modules() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf node_modules")).decision,
            Decision::Allow
        );
    }

    #[test]
    fn allows_rm_rf_next() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf .next")).decision,
            Decision::Allow
        );
    }

    #[test]
    fn allows_rm_rf_target() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf target")).decision,
            Decision::Allow
        );
    }

    #[test]
    fn allows_rm_rf_dist() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf dist")).decision,
            Decision::Allow
        );
    }

    #[test]
    fn allows_rm_rf_build() {
        assert_eq!(
            ev().evaluate(&bash("rm -rf build")).decision,
            Decision::Allow
        );
    }

    // ── Git patterns ──────────────────────────────────────────────────────────

    #[test]
    fn denies_force_push_main() {
        assert_eq!(
            ev().evaluate(&bash("git push --force origin main"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_force_push_master() {
        assert_eq!(
            ev().evaluate(&bash("git push --force origin master"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_force_with_lease_main() {
        assert_eq!(
            ev().evaluate(&bash("git push --force-with-lease origin main"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_delete_remote_main() {
        assert_eq!(
            ev().evaluate(&bash("git push --delete origin main"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_hard_reset_origin_main() {
        assert_eq!(
            ev().evaluate(&bash("git reset --hard origin/main"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_branch_delete_main() {
        assert_eq!(
            ev().evaluate(&bash("git branch -D main")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_filter_branch() {
        assert_eq!(
            ev().evaluate(&bash(
                "git filter-branch --tree-filter 'rm secrets.txt' HEAD"
            ))
            .decision,
            Decision::Deny
        );
    }

    /// Safe variant: force-push to a feature branch is allowed.
    #[test]
    fn allows_force_push_feature_branch() {
        assert_eq!(
            ev().evaluate(&bash("git push --force origin feature/cleanup"))
                .decision,
            Decision::Allow
        );
    }

    /// Safe variant: delete a local feature branch is allowed.
    #[test]
    fn allows_branch_delete_feature() {
        assert_eq!(
            ev().evaluate(&bash("git branch -D feature/old-experiment"))
                .decision,
            Decision::Allow
        );
    }

    // ── Database patterns ─────────────────────────────────────────────────────

    #[test]
    fn denies_drop_table() {
        assert_eq!(
            ev().evaluate(&sql("DROP TABLE users")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_drop_table_lowercase() {
        // Case-insensitive matching via normalisation.
        assert_eq!(
            ev().evaluate(&sql("drop table users")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_drop_database() {
        assert_eq!(
            ev().evaluate(&sql("DROP DATABASE prod")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_drop_schema() {
        assert_eq!(
            ev().evaluate(&sql("DROP SCHEMA public")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_truncate() {
        assert_eq!(
            ev().evaluate(&sql("TRUNCATE users")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_delete_from_without_where() {
        assert_eq!(
            ev().evaluate(&sql("DELETE FROM users")).decision,
            Decision::Deny
        );
    }

    /// Safe variant: DELETE FROM with WHERE is allowed.
    #[test]
    fn allows_delete_from_with_where() {
        assert_eq!(
            ev().evaluate(&sql("DELETE FROM sessions WHERE expires_at < NOW()"))
                .decision,
            Decision::Allow
        );
    }

    // ── Infrastructure patterns ───────────────────────────────────────────────

    #[test]
    fn denies_terraform_destroy() {
        assert_eq!(
            ev().evaluate(&bash("terraform destroy")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_kubectl_delete_namespace() {
        assert_eq!(
            ev().evaluate(&bash("kubectl delete namespace production"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_kubectl_delete_pvc() {
        assert_eq!(
            ev().evaluate(&bash("kubectl delete pvc data-volume-0"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_docker_volume_rm() {
        assert_eq!(
            ev().evaluate(&bash("docker volume rm postgres-data"))
                .decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_docker_system_prune() {
        assert_eq!(
            ev().evaluate(&bash("docker system prune -f")).decision,
            Decision::Deny
        );
    }

    // ── Publishing patterns ───────────────────────────────────────────────────

    #[test]
    fn denies_npm_publish() {
        assert_eq!(ev().evaluate(&bash("npm publish")).decision, Decision::Deny);
    }

    #[test]
    fn denies_cargo_publish() {
        assert_eq!(
            ev().evaluate(&bash("cargo publish")).decision,
            Decision::Deny
        );
    }

    #[test]
    fn denies_gh_release_create() {
        assert_eq!(
            ev().evaluate(&bash("gh release create v1.2.3")).decision,
            Decision::Deny
        );
    }

    // ── Secret patterns ───────────────────────────────────────────────────────

    #[test]
    fn denies_cat_env() {
        assert_eq!(ev().evaluate(&bash("cat .env")).decision, Decision::Deny);
    }

    #[test]
    fn denies_git_add_env() {
        assert_eq!(
            ev().evaluate(&bash("git add .env")).decision,
            Decision::Deny
        );
    }

    // ── Benign commands ───────────────────────────────────────────────────────

    #[test]
    fn allows_cargo_build() {
        assert_eq!(
            ev().evaluate(&bash("cargo build")).decision,
            Decision::Allow
        );
    }

    #[test]
    fn allows_cargo_test() {
        assert_eq!(ev().evaluate(&bash("cargo test")).decision, Decision::Allow);
    }

    #[test]
    fn allows_ls() {
        assert_eq!(ev().evaluate(&bash("ls /tmp")).decision, Decision::Allow);
    }

    #[test]
    fn allows_read_action() {
        let action = PolicyAction::new("read", json!({"path": "/tmp/foo.txt"}));
        assert_eq!(ev().evaluate(&action).decision, Decision::Allow);
    }

    #[test]
    fn allows_mcp_tool() {
        let action = PolicyAction::new("mcp_tool", json!({"tool": "list_files"}));
        assert_eq!(ev().evaluate(&action).decision, Decision::Allow);
    }

    // ── Injection resilience: base64 ──────────────────────────────────────────

    /// `echo "cm0gLXJmIC8=" | base64 -d | bash` → base64("rm -rf /") detected.
    #[test]
    fn denies_base64_encoded_rm_rf_root() {
        // "rm -rf /" base64-encodes to "cm0gLXJmIC8="
        let action = bash("echo cm0gLXJmIC8= | base64 -d | bash");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "base64-encoded 'rm -rf /' must be detected"
        );
    }

    /// Verify our inline base64 decoder works correctly for the test vector.
    #[test]
    fn b64_decode_rm_rf_root() {
        let decoded = try_b64_decode("cm0gLXJmIC8=");
        assert_eq!(decoded.as_deref(), Some("rm -rf /"));
    }

    // ── Injection resilience: URL encoding ────────────────────────────────────

    /// `rm%20-rf%20/` → URL-decoded to `rm -rf /`.
    #[test]
    fn denies_url_encoded_rm_rf_root() {
        let action = bash("rm%20-rf%20/");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "URL-encoded 'rm -rf /' must be detected"
        );
    }

    /// `DROP%20TABLE%20users` → detected after URL-decoding.
    #[test]
    fn denies_url_encoded_drop_table() {
        let action = sql("DROP%20TABLE%20users");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "URL-encoded 'DROP TABLE users' must be detected"
        );
    }

    // ── Injection resilience: homoglyphs ──────────────────────────────────────

    /// `rm -rf /` with an en-dash instead of a hyphen.
    #[test]
    fn denies_homoglyph_en_dash() {
        // U+2013 EN DASH used instead of U+002D HYPHEN-MINUS.
        let action = bash("rm \u{2013}rf /");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "en-dash homoglyph must be normalised to '-'"
        );
    }

    /// Full-width slash in path.
    #[test]
    fn denies_homoglyph_fullwidth_slash() {
        // U+FF0F FULLWIDTH SOLIDUS used instead of U+002F SOLIDUS.
        let action = bash("rm -rf \u{FF0F}");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "full-width slash homoglyph must be normalised to '/'"
        );
    }

    // ── Injection resilience: chained commands ────────────────────────────────

    /// Dangerous action chained after a safe one via `&&`.
    #[test]
    fn denies_chained_and_blocked_tail() {
        let action = bash("cargo build && rm -rf /");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "dangerous tail in && chain must be detected"
        );
    }

    /// Dangerous action chained after a safe one via `;`.
    #[test]
    fn denies_chained_semicolon_blocked_tail() {
        let action = bash("ls /tmp ; rm -rf ~");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "dangerous tail in ; chain must be detected"
        );
    }

    /// Dangerous action in the middle of a pipe chain.
    #[test]
    fn denies_chained_pipe_blocked_segment() {
        let action = bash("cat file.txt | mkfs.ext4 /dev/sdb | grep ok");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Deny,
            "dangerous segment in pipe chain must be detected"
        );
    }

    /// A benign chain must NOT be flagged.
    #[test]
    fn allows_benign_chain() {
        let action = bash("cargo fmt && cargo clippy && cargo test");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Allow,
            "benign && chain must be allowed"
        );
    }

    /// Another benign chain: ls then echo.
    #[test]
    fn allows_benign_semicolon_chain() {
        let action = bash("cd /tmp ; ls -la ; echo done");
        assert_eq!(
            ev().evaluate(&action).decision,
            Decision::Allow,
            "benign ; chain must be allowed"
        );
    }

    // ── split_chain unit tests ────────────────────────────────────────────────

    #[test]
    fn split_chain_and() {
        let segs = split_chain("a && b");
        assert_eq!(segs, vec!["a", "b"]);
    }

    #[test]
    fn split_chain_or() {
        let segs = split_chain("a || b");
        assert_eq!(segs, vec!["a", "b"]);
    }

    #[test]
    fn split_chain_semicolon() {
        let segs = split_chain("a ; b ; c");
        assert_eq!(segs, vec!["a", "b", "c"]);
    }

    #[test]
    fn split_chain_pipe() {
        let segs = split_chain("a | b");
        assert_eq!(segs, vec!["a", "b"]);
    }

    #[test]
    fn split_chain_newline() {
        let segs = split_chain("a\nb");
        assert_eq!(segs, vec!["a", "b"]);
    }

    #[test]
    fn split_chain_single() {
        let segs = split_chain("cargo build");
        assert_eq!(segs, vec!["cargo build"]);
    }

    // ── normalise unit tests ──────────────────────────────────────────────────

    #[test]
    fn normalise_url_decodes_spaces() {
        assert!(normalise("rm%20-rf%20/").contains("rm -rf /"));
    }

    #[test]
    fn normalise_maps_en_dash_to_hyphen() {
        assert!(normalise("rm \u{2013}rf /").contains("rm -rf /"));
    }

    #[test]
    fn normalise_expands_b64_token() {
        // "rm -rf /" base64 = "cm0gLXJmIC8="
        let out = normalise("cm0gLXJmIC8=");
        assert!(out.contains("rm -rf /"), "got: {out}");
    }
}
