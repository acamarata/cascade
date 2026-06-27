//! # memory::namespace
//!
//! Purpose: Namespace validation and personal-firewall enforcement for the RAG-08
//!   memory module. Namespaces partition all memory tables into isolated scopes;
//!   no cross-namespace leakage is permitted.
//!
//! ## Namespace format
//! - `"personal"` — private, local-only. Blocked from external exposure unless
//!   the caller passes `opt_in: true`.
//! - `"dev-<slug>"` — per-project developer RAG namespace (e.g. "dev-nself").
//!   `<slug>` must be lowercase alphanumeric + hyphens, 1–64 chars.
//! - `"meta"` — cascade-meta scope (cascade's own knowledge about itself).
//!
//! Inputs:  raw namespace string + optional opt_in flag
//! Outputs: `Ok(ValidatedNamespace)` or `NamespaceError`
//! Constraints: validation is pure; no I/O.

use thiserror::Error;

// ── Error types ───────────────────────────────────────────────────────────────

/// Errors produced by namespace validation and the personal firewall.
#[derive(Debug, Error, PartialEq, Eq)]
pub enum NamespaceError {
    /// The "personal" namespace was accessed without `opt_in: true`.
    #[error(
        "personal namespace requires explicit opt_in — pass opt_in: true to allow this access"
    )]
    PersonalFirewall,

    /// The namespace string does not match any known format.
    #[error("invalid namespace '{0}': must be 'personal', 'meta', or 'dev-<slug>'")]
    InvalidNamespace(String),

    /// The slug in a `dev-<slug>` namespace contains forbidden characters.
    #[error(
        "invalid dev namespace slug '{0}': must be lowercase alphanumeric + hyphens, 1–64 chars"
    )]
    InvalidSlug(String),
}

// ── Validated namespace ────────────────────────────────────────────────────────

/// A validated, type-safe namespace string.
///
/// Constructed only via [`validate`] or [`validate_with_firewall`].
/// Derefs to `&str` for direct use in SQL `WHERE namespace = ?` bindings.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct ValidatedNamespace(String);

impl ValidatedNamespace {
    /// Return the inner namespace string.
    pub fn as_str(&self) -> &str {
        &self.0
    }

    /// Return `true` if this is the personal namespace.
    pub fn is_personal(&self) -> bool {
        self.0 == "personal"
    }
}

impl std::fmt::Display for ValidatedNamespace {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl AsRef<str> for ValidatedNamespace {
    fn as_ref(&self) -> &str {
        &self.0
    }
}

// ── Validation ────────────────────────────────────────────────────────────────

/// Validate a namespace string without the personal firewall.
///
/// Use this for internal/trusted callers (e.g. background consolidation) that
/// are allowed to operate on any namespace including "personal".
pub fn validate(ns: &str) -> Result<ValidatedNamespace, NamespaceError> {
    match ns {
        "personal" | "meta" => Ok(ValidatedNamespace(ns.to_owned())),
        s if s.starts_with("dev-") => {
            let slug = &s[4..];
            validate_slug(slug, s)?;
            Ok(ValidatedNamespace(s.to_owned()))
        }
        other => Err(NamespaceError::InvalidNamespace(other.to_owned())),
    }
}

/// Validate a namespace string AND enforce the personal firewall.
///
/// External callers (MCP tools, HTTP handlers) MUST use this function.
/// Access to the "personal" namespace is blocked unless `opt_in` is `true`.
pub fn validate_with_firewall(ns: &str, opt_in: bool) -> Result<ValidatedNamespace, NamespaceError> {
    let validated = validate(ns)?;
    if validated.is_personal() && !opt_in {
        return Err(NamespaceError::PersonalFirewall);
    }
    Ok(validated)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

fn validate_slug(slug: &str, full: &str) -> Result<(), NamespaceError> {
    if slug.is_empty() || slug.len() > 64 {
        return Err(NamespaceError::InvalidSlug(full.to_owned()));
    }
    // Allow lowercase letters, digits, and hyphens.
    if !slug
        .chars()
        .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
    {
        return Err(NamespaceError::InvalidSlug(full.to_owned()));
    }
    // Must not start or end with a hyphen.
    if slug.starts_with('-') || slug.ends_with('-') {
        return Err(NamespaceError::InvalidSlug(full.to_owned()));
    }
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_namespaces_pass() {
        for ns in &["personal", "meta", "dev-nself", "dev-my-project", "dev-x1"] {
            assert!(
                validate(ns).is_ok(),
                "expected '{ns}' to be valid"
            );
        }
    }

    #[test]
    fn invalid_namespaces_rejected() {
        for ns in &["", "dev-", "dev-BAD", "global", "dev-a_b", "dev-" , "unknown"] {
            assert!(
                validate(ns).is_err(),
                "expected '{ns}' to be invalid"
            );
        }
    }

    #[test]
    fn personal_firewall_blocks_without_opt_in() {
        let err = validate_with_firewall("personal", false).unwrap_err();
        assert_eq!(err, NamespaceError::PersonalFirewall);
    }

    #[test]
    fn personal_firewall_allows_with_opt_in() {
        let ns = validate_with_firewall("personal", true).unwrap();
        assert!(ns.is_personal());
    }

    #[test]
    fn meta_and_dev_allowed_without_opt_in() {
        assert!(validate_with_firewall("meta", false).is_ok());
        assert!(validate_with_firewall("dev-nself", false).is_ok());
    }

    #[test]
    fn dev_slug_length_limits() {
        // 64-char slug — valid
        let long_slug = format!("dev-{}", "a".repeat(64));
        assert!(validate(&long_slug).is_ok());

        // 65-char slug — invalid
        let too_long = format!("dev-{}", "a".repeat(65));
        assert!(validate(&too_long).is_err());
    }

    #[test]
    fn dev_slug_no_leading_trailing_hyphen() {
        assert!(validate("dev--bad").is_err());
        assert!(validate("dev-bad-").is_err());
    }
}
