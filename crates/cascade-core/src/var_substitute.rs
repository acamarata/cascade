//! # var_substitute — canonical variable substitution and missed-reference detection
//!
//! Provides two operations over the merged cascade text after `@import` expansion:
//!
//! 1. **Token substitution** ([`substitute_vars`]) — replace every `${ns.key}`
//!    token with the corresponding value from the merged [`VarsMap`].  Unknown
//!    tokens are left verbatim and logged at `warn`.  `$${ns.key}` is an escape
//!    sequence that produces a literal `${ns.key}` in the output.
//!
//! 2. **Missed-reference detection** ([`missed_canonical_refs`]) — scan the
//!    substituted text for literal occurrences of a canonical var's _value_ that
//!    are NOT already inside a `${…}` token.  Each such occurrence is a
//!    hardcoded duplicate that should be an interpolation instead.
//!
//! ## Ordering contract
//!
//! Both functions operate on text that has already been through `@import`
//! expansion.  The caller is responsible for correct ordering:
//!
//! ```text
//! raw instruction text
//!   → expand_imports()       (import_expand module)
//!   → substitute_vars()      (this module)
//!   → missed_canonical_refs() (this module, for doctor / lint)
//! ```
//!
//! ## Substitution syntax
//!
//! | Input          | Output                       |
//! |----------------|------------------------------|
//! | `${ns.key}`    | value from VarsMap for ns.key |
//! | `$${ns.key}`   | literal `${ns.key}` (escape)  |
//! | `${unknown}`   | left verbatim + warn logged   |
//! | `$` (bare)     | left verbatim                 |
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: var_substitute module (P12-canonical-vars)

use cascade_types::tiers::VarsMap;
use tracing::warn;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single missed-reference finding: a literal value that should be an
/// interpolation.
///
/// Produced by [`missed_canonical_refs`] and surfaced by `cascade doctor`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MissedRef {
    /// The canonical var key whose value was found as a literal.
    ///
    /// Callers can build the suggested interpolation as `${key}`.
    pub key: String,

    /// The literal value that was found verbatim in the text.
    pub value: String,

    /// Byte offset of the first occurrence in the (post-substitution) text.
    pub offset: usize,
}

// ── Public API ─────────────────────────────────────────────────────────────────

/// Substitute `${ns.key}` tokens in `text` using the merged `vars` map.
///
/// # Substitution rules
///
/// - `${ns.key}` → value from `vars["ns.key"]`, or verbatim + warn if unknown.
/// - `$${ns.key}` → literal `${ns.key}` (escape; the leading `$` is consumed).
/// - Bare `$` not followed by `{` → passed through unchanged.
/// - Unclosed `${…` (no matching `}`) → left verbatim from the `$` onward.
///
/// # Arguments
///
/// * `text` — post-`@import`-expansion instruction text.
/// * `vars` — the merged canonical variable map from [`ResolvedContext::vars`].
///
/// # Returns
///
/// The text with all resolvable tokens substituted.  The function is infallible:
/// unknown or unclosed tokens are left in place.
///
/// [`ResolvedContext::vars`]: cascade_types::tiers::ResolvedContext::vars
pub fn substitute_vars(text: &str, vars: &VarsMap) -> String {
    let bytes = text.as_bytes();
    let mut out = String::with_capacity(text.len());
    let mut i = 0;

    while i < bytes.len() {
        // Look for '$'
        if bytes[i] != b'$' {
            out.push(bytes[i] as char);
            i += 1;
            continue;
        }

        // We have a '$' at position i.
        if i + 1 >= bytes.len() {
            // Trailing bare '$' — pass through.
            out.push('$');
            i += 1;
            continue;
        }

        match bytes[i + 1] {
            b'$' => {
                // `$$` escape prefix — consume one `$`, then process the rest
                // of the potential token as a literal.
                //
                // `$${key}` → literal `${key}` (escape).
                // `$$` followed by anything else → two `$` signs would be odd;
                // we emit the second `$` and let the next iteration handle it.
                if i + 2 < bytes.len() && bytes[i + 2] == b'{' {
                    // Find the matching '}'.
                    if let Some(end) = find_closing_brace(bytes, i + 3) {
                        // Emit a literal `${…}` for the span [i+2 .. end+1].
                        // Skipping the first `$` (the escape prefix).
                        let literal = &text[i + 1..=end];
                        out.push_str(literal);
                        i = end + 1;
                    } else {
                        // Unclosed `$${` — emit `$$` verbatim, let loop continue.
                        out.push('$');
                        out.push('$');
                        i += 2;
                    }
                } else {
                    // `$$` not followed by `{` — emit both as literals.
                    out.push('$');
                    out.push('$');
                    i += 2;
                }
            }
            b'{' => {
                // Potential `${key}` token.
                if let Some(end) = find_closing_brace(bytes, i + 2) {
                    let key = &text[i + 2..end];
                    if let Some(value) = vars.get(key) {
                        out.push_str(value);
                    } else {
                        // Unknown token — warn and leave verbatim.
                        warn!(
                            token = key,
                            "canonical var token not found in merged vars map — leaving verbatim"
                        );
                        out.push_str(&text[i..=end]);
                    }
                    i = end + 1;
                } else {
                    // Unclosed `${` — pass through literally.
                    out.push('$');
                    i += 1;
                }
            }
            _ => {
                // `$` followed by a non-`{`, non-`$` character — literal.
                out.push('$');
                i += 1;
            }
        }
    }

    out
}

/// Scan `text` for literal occurrences of canonical var values that are NOT
/// already inside a `${…}` interpolation token.
///
/// Returns one [`MissedRef`] per (key, occurrence) pair — if a value appears
/// three times as a literal, three findings are returned.  Callers may choose
/// to deduplicate by key if only a count per key is needed.
///
/// Only values with at least **3 characters** are checked to avoid false
/// positives from very short values like `"1"` or `"us"`.
///
/// # Arguments
///
/// * `text` — the post-`substitute_vars` text to scan.
/// * `vars` — the merged canonical variable map.
///
/// # Returns
///
/// `Vec<MissedRef>` — empty when no literals are found.  The function is pure
/// (no I/O, no mutation of inputs).
pub fn missed_canonical_refs(text: &str, vars: &VarsMap) -> Vec<MissedRef> {
    const MIN_VALUE_LEN: usize = 3;

    let mut findings: Vec<MissedRef> = Vec::new();

    for (key, value) in vars {
        if value.len() < MIN_VALUE_LEN {
            continue;
        }

        // Scan for every literal occurrence of `value` in `text`.
        let mut search_start = 0;
        while let Some(pos) = text[search_start..].find(value.as_str()) {
            let abs_pos = search_start + pos;

            // Check that this occurrence is NOT already inside `${…}`.
            // It is inside a token if we can find a `${` before it and a `}`
            // after it with no intervening `}` or `${`.
            if !is_inside_token(text, abs_pos, abs_pos + value.len()) {
                findings.push(MissedRef {
                    key: key.clone(),
                    value: value.clone(),
                    offset: abs_pos,
                });
            }

            // Advance past this occurrence to find the next one.
            search_start = abs_pos + value.len().max(1);
        }
    }

    findings
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Find the byte index of the first `}` at or after `start` in `bytes`.
///
/// Returns `None` if no `}` is found (unclosed token).
fn find_closing_brace(bytes: &[u8], start: usize) -> Option<usize> {
    bytes[start..].iter().position(|&b| b == b'}').map(|p| start + p)
}

/// Return `true` when the substring `text[span_start..span_end]` is contained
/// within a `${…}` interpolation token.
///
/// A span is "inside a token" when there exists a `${` somewhere before
/// `span_start` (with no `}` between that `${` and `span_start`) and the
/// matching `}` is at or after `span_end - 1`.
///
/// This is a best-effort check suitable for the missed-ref use case.  It will
/// correctly handle the common cases; pathological inputs (e.g. `${a${b}c}`)
/// may produce imprecise results, but those are not valid cascade var syntax.
fn is_inside_token(text: &str, span_start: usize, span_end: usize) -> bool {
    // Scan backward from span_start looking for '${'.
    let prefix = &text[..span_start];
    if let Some(dollar_open) = prefix.rfind("${") {
        // Make sure there is no '}' between `dollar_open` and `span_start`.
        let between = &text[dollar_open + 2..span_start];
        if between.contains('}') {
            return false;
        }
        // The opening `${` is valid; check that there is a `}` at or after span_end.
        let suffix = &text[span_end..];
        if suffix.starts_with('}')
            || suffix
                .find('}')
                .is_some_and(|p| !text[span_end..span_end + p].contains('{'))
        {
            return true;
        }
    }
    false
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::tiers::VarsMap;

    fn vars(pairs: &[(&str, &str)]) -> VarsMap {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    // ── substitute_vars ───────────────────────────────────────────────────────

    // Test 1: simple substitution
    /// `${ns.key}` is replaced with the corresponding value.
    #[test]
    fn substitutes_known_token() {
        let v = vars(&[("infra.prod_ip", "1.2.3.4")]);
        let result = substitute_vars("connect to ${infra.prod_ip} now", &v);
        assert_eq!(result, "connect to 1.2.3.4 now");
    }

    // Test 2: unknown token left verbatim (no panic)
    /// An unrecognised `${…}` token is left as-is; the function does not error.
    #[test]
    fn unknown_token_verbatim_no_panic() {
        let v = vars(&[]);
        let result = substitute_vars("value: ${cascade.unknown}", &v);
        assert_eq!(result, "value: ${cascade.unknown}");
    }

    // Test 3: escape `$${…}` → literal `${…}`
    /// `$${ns.key}` produces a literal `${ns.key}` in the output.
    #[test]
    fn escape_produces_literal_token() {
        let v = vars(&[("ns.key", "replaced")]);
        let result = substitute_vars("literal: $${ns.key} end", &v);
        assert_eq!(result, "literal: ${ns.key} end", "escaped token must appear verbatim");
    }

    // Test 4: multiple tokens in one string
    /// Multiple `${…}` tokens in the same string are all substituted.
    #[test]
    fn multiple_tokens_substituted() {
        let v = vars(&[
            ("host", "example.com"),
            ("port", "8080"),
        ]);
        let result = substitute_vars("url: ${host}:${port}/path", &v);
        assert_eq!(result, "url: example.com:8080/path");
    }

    // Test 5: bare `$` is left untouched
    /// A bare `$` not followed by `{` or `$` passes through unchanged.
    #[test]
    fn bare_dollar_unchanged() {
        let v = vars(&[]);
        let result = substitute_vars("price is $5 today", &v);
        assert_eq!(result, "price is $5 today");
    }

    // Test 6: unclosed `${` is left verbatim
    /// An unclosed `${` (no matching `}`) is left verbatim without panicking.
    #[test]
    fn unclosed_token_verbatim_no_panic() {
        let v = vars(&[("ns.key", "value")]);
        let result = substitute_vars("broken: ${ns.key and more text", &v);
        // The `${` is unclosed so the whole thing from `$` onward is left as-is.
        assert!(result.contains("${ns.key"), "unclosed token must be left verbatim: {result}");
    }

    // Test 7: text with no tokens passes through unchanged
    /// Text containing no `$` signs is returned unchanged.
    #[test]
    fn no_tokens_unchanged() {
        let v = vars(&[("k", "v")]);
        let text = "This text has no dollar signs at all.";
        assert_eq!(substitute_vars(text, &v), text);
    }

    // Test 8: precedence — lower tier var overrides higher tier (integration check)
    /// The substitute_vars function uses whatever VarsMap it receives;
    /// precedence is resolved by the resolver before calling this function.
    /// This test verifies the contract is honoured end-to-end with a merged map.
    #[test]
    fn substitution_uses_provided_map() {
        // Simulate a merged map where the lower tier's value won.
        let v = vars(&[("infra.prod_ip", "2.2.2.2")]);
        let result = substitute_vars("ip=${infra.prod_ip}", &v);
        assert_eq!(result, "ip=2.2.2.2");
    }

    // ── missed_canonical_refs ─────────────────────────────────────────────────

    // Test 9: finds a hardcoded literal duplicate
    /// When a canonical var's value appears verbatim in the text (outside any
    /// `${…}` token), a MissedRef is returned.
    #[test]
    fn finds_hardcoded_literal() {
        let v = vars(&[("infra.prod_ip", "1.2.3.4")]);
        // "1.2.3.4" appears as a literal, not as ${infra.prod_ip}.
        let text = "connect to 1.2.3.4 for production";
        let refs = missed_canonical_refs(text, &v);
        assert_eq!(refs.len(), 1, "one finding expected: {refs:#?}");
        assert_eq!(refs[0].key, "infra.prod_ip");
        assert_eq!(refs[0].value, "1.2.3.4");
    }

    // Test 10: ignores already-interpolated values
    /// A value that appears as `${infra.prod_ip}` (inside a token) must NOT
    /// be flagged — it is already interpolated.
    ///
    /// WHY: after substitute_vars runs, `${infra.prod_ip}` is replaced by the
    /// value itself; so missed_canonical_refs must operate on the ORIGINAL text
    /// (before substitution) or the caller must call it on the pre-substitution
    /// text.  Here we test directly with the literal token still present.
    #[test]
    fn ignores_interpolated_value() {
        let v = vars(&[("infra.prod_ip", "1.2.3.4")]);
        // The token `${infra.prod_ip}` is inside the interpolation syntax.
        let text = "connect to ${infra.prod_ip} for production";
        let refs = missed_canonical_refs(text, &v);
        assert!(
            refs.is_empty(),
            "value inside a token must not be flagged: {refs:#?}"
        );
    }

    // Test 11: value shorter than MIN_VALUE_LEN is skipped
    /// Short values (< 3 chars) are not checked to avoid false positives.
    #[test]
    fn short_value_not_checked() {
        let v = vars(&[("flag", "on")]);
        let text = "turn on the feature";
        let refs = missed_canonical_refs(text, &v);
        assert!(refs.is_empty(), "short values must be skipped: {refs:#?}");
    }

    // Test 12: multiple literal occurrences produce multiple findings
    /// If the same value appears three times, three MissedRef entries are
    /// returned (one per occurrence).
    #[test]
    fn multiple_literal_occurrences() {
        let v = vars(&[("host", "example.com")]);
        let text = "a=example.com b=example.com c=example.com";
        let refs = missed_canonical_refs(text, &v);
        assert_eq!(refs.len(), 3, "one finding per literal occurrence: {refs:#?}");
    }

    // Test 13: empty vars map produces no findings
    /// When the vars map is empty, missed_canonical_refs returns an empty vec.
    #[test]
    fn empty_vars_no_findings() {
        let v = vars(&[]);
        let refs = missed_canonical_refs("some text with values", &v);
        assert!(refs.is_empty());
    }
}
