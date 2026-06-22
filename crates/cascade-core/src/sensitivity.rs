//! # sensitivity — content-sensitivity classifier and routing policy
//!
//! ## Purpose
//!
//! Classifies user-visible text as [`ContentSensitivity::Public`] or
//! [`ContentSensitivity::Sensitive`] and provides a routing policy
//! ([`SensitivityPolicy`]) that enforces the v1.2 firewall rule:
//!
//! > **Sensitive content may only be dispatched to Claude or a local model.
//! > It must never reach an external provider (Gemini / OpenAI / OC-Go / GFP)
//! > and must not be synced.**
//!
//! ## Classifier categories
//!
//! | Category | Examples | Conservative direction |
//! |---|---|---|
//! | PII — SSN | `123-45-6789`, `ssn:` | false-positive → Sensitive |
//! | PII — DOB | `date of birth`, ISO dates in PII context | Sensitive |
//! | PII — name+address | co-occurrence of name-like + street address | Sensitive |
//! | VA / disability | `va claim`, `disability rating`, `service-connected` | Sensitive |
//! | Custody / family court | `custody`, `guardian ad litem`, `parenting plan` | Sensitive |
//! | Health / medical | `diagnosis`, `prescription`, `medical record` | Sensitive |
//! | Financial / account | `account number`, `routing number`, `credit card` | Sensitive |
//! | Personal scope path | path starts with `~/Downloads` or home `Downloads` | Sensitive |
//!
//! The classifier is **conservative**: when uncertain it leans toward
//! `Sensitive` to prevent accidental leakage of private data.
//!
//! ## Constraints
//!
//! - Pure, deterministic, no I/O, no async, no external dependencies.
//! - No `unwrap()` outside `#[cfg(test)]`.
//! - File ≤ 300 lines.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: sensitivity module (v1.2)

use std::path::Path;

// ── ContentSensitivity ────────────────────────────────────────────────────────

/// Sensitivity classification of a piece of content.
///
/// The classifier is conservative: it prefers `Sensitive` on ambiguous inputs
/// to prevent accidental data leakage to external providers.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ContentSensitivity {
    /// Content appears to be non-personal and safe to route to any provider.
    Public,
    /// Content contains (or likely contains) personal, medical, legal, financial,
    /// or VA-related data. Must only be dispatched to Claude or a local model.
    Sensitive,
}

impl std::fmt::Display for ContentSensitivity {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            ContentSensitivity::Public => write!(f, "Public"),
            ContentSensitivity::Sensitive => write!(f, "Sensitive"),
        }
    }
}

// ── RoutingVerdict ────────────────────────────────────────────────────────────

/// Decision returned by [`SensitivityPolicy::check`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RoutingVerdict {
    /// Provider is approved for this content.
    Allow,
    /// Provider is blocked for this content. `reason` explains why.
    DenyExternal {
        /// Human-readable explanation shown to the caller.
        reason: String,
    },
}

impl RoutingVerdict {
    /// Returns `true` when the verdict is `Allow`.
    pub fn is_allow(&self) -> bool {
        matches!(self, RoutingVerdict::Allow)
    }

    /// Returns `true` when the verdict is `DenyExternal`.
    pub fn is_deny(&self) -> bool {
        matches!(self, RoutingVerdict::DenyExternal { .. })
    }
}

// ── SensitivityPolicy ─────────────────────────────────────────────────────────

/// Routing policy that enforces the sensitive-data firewall.
///
/// Call [`SensitivityPolicy::check`] before dispatching to any provider.
/// If the verdict is [`RoutingVerdict::DenyExternal`], the caller MUST NOT
/// send the payload to the external provider.
#[derive(Debug, Default)]
pub struct SensitivityPolicy;

impl SensitivityPolicy {
    /// Create a new policy instance.
    pub fn new() -> Self {
        Self
    }

    /// Check whether `provider_id` may receive content classified as `sensitivity`.
    ///
    /// Returns [`RoutingVerdict::Allow`] when the provider is safe for the
    /// given sensitivity level, or [`RoutingVerdict::DenyExternal`] when the
    /// firewall rule must block the dispatch.
    pub fn check(&self, sensitivity: ContentSensitivity, provider_id: &str) -> RoutingVerdict {
        if sensitivity == ContentSensitivity::Public {
            return RoutingVerdict::Allow;
        }

        // Sensitive: only trusted providers are allowed.
        if provider_is_trusted_for_sensitive(provider_id) {
            RoutingVerdict::Allow
        } else {
            RoutingVerdict::DenyExternal {
                reason: format!(
                    "Sensitive content (PII / VA / health / personal) cannot be sent to \
                     external provider '{}'. Route to Claude or a local model instead.",
                    provider_id
                ),
            }
        }
    }
}

/// Returns `true` when the provider is trusted for sensitive content.
///
/// Trusted: `claude` (and sub-variants: `claude-*`, `acc2`, `acc3`, …) and
/// anything tagged `local` or `local-llm`.  All external providers (gemini,
/// openai, codex, oc-go, gfp) are untrusted.
///
/// The comparison is case-insensitive to avoid footguns from provider-ID
/// formatting differences across the codebase.
pub fn provider_is_trusted_for_sensitive(provider_id: &str) -> bool {
    let lower = provider_id.to_lowercase();
    // Trusted families: claude (any variant), local models.
    lower == "claude"
        || lower.starts_with("claude-")
        || lower.starts_with("acc")           // acc2, acc3, extra Claude accounts
        || lower == "local"
        || lower.starts_with("local-")
        || lower == "local-llm"
        || lower.starts_with("local-llm-")
}

// ── classify_sensitivity ──────────────────────────────────────────────────────

/// Classify the sensitivity of `text`.
///
/// The classifier is **conservative**: on uncertainty it returns `Sensitive`.
/// It never makes network calls; all detection is pattern-based.
///
/// Callers should also check [`path_is_personal_scope`] and OR the results
/// together when the content originates from a specific filesystem path.
pub fn classify_sensitivity(text: &str) -> ContentSensitivity {
    let lower = text.to_lowercase();

    if matches_any_sensitive_pattern(&lower) {
        ContentSensitivity::Sensitive
    } else {
        ContentSensitivity::Public
    }
}

/// Returns `true` if `path` is within the personal scope (`~/Downloads` or
/// equivalent).  Content from personal-scope paths is always treated as
/// Sensitive regardless of its text content.
pub fn path_is_personal_scope(path: &Path) -> bool {
    let path_str = path.to_string_lossy();
    // Match ~/Downloads (home-relative) or absolute paths that include
    // /Downloads as a component at the home level.
    if path_str.starts_with("~/Downloads") {
        return true;
    }
    // Check for OS-level expansion: /Users/<user>/Downloads or /home/<user>/Downloads
    let home = std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .unwrap_or_default();
    if !home.is_empty() {
        let personal = format!("{}/Downloads", home.trim_end_matches('/'));
        if path_str.starts_with(personal.as_str()) {
            return true;
        }
    }
    false
}

/// Classify content that came from a specific filesystem path.
///
/// Equivalent to `classify_sensitivity(text)` OR personal-scope path check:
/// returns `Sensitive` if either the text or the path indicates sensitivity.
pub fn classify_sensitivity_with_path(text: &str, path: &Path) -> ContentSensitivity {
    if path_is_personal_scope(path) || classify_sensitivity(text) == ContentSensitivity::Sensitive {
        ContentSensitivity::Sensitive
    } else {
        ContentSensitivity::Public
    }
}

// ── Pattern matching internals ────────────────────────────────────────────────

/// All sensitive-pattern categories combined.
fn matches_any_sensitive_pattern(lower: &str) -> bool {
    matches_pii_ssn(lower)
        || matches_pii_dob(lower)
        || matches_pii_name_address(lower)
        || matches_va_disability(lower)
        || matches_custody_family(lower)
        || matches_health_medical(lower)
        || matches_financial_account(lower)
}

/// SSN patterns: `123-45-6789`, `ssn:`, `social security number`.
fn matches_pii_ssn(lower: &str) -> bool {
    if lower.contains("ssn") || lower.contains("social security") {
        return true;
    }
    // Numeric SSN pattern: NNN-NN-NNNN (with surrounding non-digit context).
    // Simple heuristic: look for a substring matching ddd-dd-dddd.
    contains_ssn_pattern(lower)
}

fn contains_ssn_pattern(text: &str) -> bool {
    // Walk the text looking for d{3}-d{2}-d{4}.
    let bytes = text.as_bytes();
    if bytes.len() < 11 {
        return false;
    }
    for i in 0..=(bytes.len() - 11) {
        if bytes[i].is_ascii_digit()
            && bytes[i + 1].is_ascii_digit()
            && bytes[i + 2].is_ascii_digit()
            && bytes[i + 3] == b'-'
            && bytes[i + 4].is_ascii_digit()
            && bytes[i + 5].is_ascii_digit()
            && bytes[i + 6] == b'-'
            && bytes[i + 7].is_ascii_digit()
            && bytes[i + 8].is_ascii_digit()
            && bytes[i + 9].is_ascii_digit()
            && bytes[i + 10].is_ascii_digit()
        {
            return true;
        }
    }
    false
}

/// DOB patterns: `date of birth`, `dob:`, `born on`, `birthdate`.
fn matches_pii_dob(lower: &str) -> bool {
    lower.contains("date of birth")
        || lower.contains(" dob ")
        || lower.contains("dob:")
        || lower.contains("born on")
        || lower.contains("birthdate")
        || lower.contains("birth date")
}

/// Name + address co-occurrence: when name-like tokens AND address tokens appear.
fn matches_pii_name_address(lower: &str) -> bool {
    let has_address_token = lower.contains(" street")
        || lower.contains(" avenue")
        || lower.contains(" blvd")
        || lower.contains(" boulevard")
        || lower.contains(" apt ")
        || lower.contains(" suite ")
        || lower.contains(" rd ")
        || lower.contains(" lane")
        || lower.contains(" drive")
        || lower.contains("zip code")
        || lower.contains("postal code")
        || lower.contains("mailing address")
        || lower.contains("home address");
    let has_name_token = lower.contains("full name")
        || lower.contains("legal name")
        || lower.contains("first name")
        || lower.contains("last name")
        || lower.contains("middle name")
        || lower.contains("my name is")
        || lower.contains("patient name")
        || lower.contains("claimant name")
        || lower.contains("veteran name");
    has_address_token && has_name_token
}

/// VA / disability claim patterns.
fn matches_va_disability(lower: &str) -> bool {
    lower.contains("va claim")
        || lower.contains("va disability")
        || lower.contains("veterans affairs")
        || lower.contains("service-connected")
        || lower.contains("service connected")
        || lower.contains("disability rating")
        || lower.contains("disability compensation")
        || lower.contains("c&p exam")
        || lower.contains("compensation and pension")
        || lower.contains("va file number")
        || lower.contains("va rating")
        || lower.contains("nexus letter")
        || lower.contains("dbq ")          // disability benefits questionnaire
        || lower.contains("cfr 38")        // title 38 CFR (VA regulations)
        || lower.contains("38 cfr")
        || lower.contains("board of veterans")
        || lower.contains("cavc ")         // Court of Appeals for Veterans Claims
        || lower.contains("vso ")          // Veterans Service Organization
}

/// Custody / family-court patterns.
fn matches_custody_family(lower: &str) -> bool {
    lower.contains("custody")
        || lower.contains("guardian ad litem")
        || lower.contains("parenting plan")
        || lower.contains("child support")
        || lower.contains("visitation schedule")
        || lower.contains("family court")
        || lower.contains("divorce decree")
        || lower.contains("marital settlement")
        || lower.contains("custody agreement")
        || lower.contains("restraining order")
        || lower.contains("protective order")
}

/// Health / medical patterns.
fn matches_health_medical(lower: &str) -> bool {
    lower.contains("diagnosis")
        || lower.contains("prescription")
        || lower.contains("medical record")
        || lower.contains("health record")
        || lower.contains("patient id")
        || lower.contains("hipaa")
        || lower.contains("icd-")           // ICD diagnosis codes
        || lower.contains("cpt code")       // procedure codes
        || lower.contains("lab result")
        || lower.contains("blood pressure")
        || lower.contains("medication")
        || lower.contains("mental health")
        || lower.contains("psychiatric")
        || lower.contains("therapy note")
        || lower.contains("treatment plan")
        || lower.contains("insurance claim")
        || lower.contains("prior authorization")
        || lower.contains("medical history")
}

/// Financial / account-number patterns.
fn matches_financial_account(lower: &str) -> bool {
    lower.contains("account number")
        || lower.contains("routing number")
        || lower.contains("credit card")
        || lower.contains("card number")
        || lower.contains("bank account")
        || lower.contains("wire transfer")
        || lower.contains("iban")
        || lower.contains("swift code")
        || lower.contains("tax id")
        || lower.contains("ein:")
        || lower.contains("taxpayer id")
        || lower.contains("w-2")
        || lower.contains("1099")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    // Shorthand helpers.
    fn sensitive(text: &str) -> bool {
        classify_sensitivity(text) == ContentSensitivity::Sensitive
    }

    fn public_text(text: &str) -> bool {
        classify_sensitivity(text) == ContentSensitivity::Public
    }

    // ── Negative (clean/public) ───────────────────────────────────────────────

    #[test]
    fn benign_coding_request_is_public() {
        assert!(public_text("refactor the auth module to use JWT"));
    }

    #[test]
    fn benign_docs_request_is_public() {
        assert!(public_text("write rustdoc for the sensitivity module"));
    }

    #[test]
    fn benign_question_is_public() {
        assert!(public_text("how does the cascade resolution algorithm work?"));
    }

    // ── SSN ───────────────────────────────────────────────────────────────────

    #[test]
    fn ssn_keyword_triggers_sensitive() {
        assert!(sensitive("my ssn is 123-45-6789"));
    }

    #[test]
    fn social_security_keyword_triggers_sensitive() {
        assert!(sensitive("social security number on file"));
    }

    #[test]
    fn ssn_pattern_numeric_triggers_sensitive() {
        assert!(sensitive("SSN: 987-65-4321 for verification"));
    }

    // ── DOB ───────────────────────────────────────────────────────────────────

    #[test]
    fn date_of_birth_triggers_sensitive() {
        assert!(sensitive("date of birth: 1980-04-15"));
    }

    #[test]
    fn dob_colon_triggers_sensitive() {
        assert!(sensitive("dob: 15 april 1985"));
    }

    #[test]
    fn born_on_triggers_sensitive() {
        assert!(sensitive("born on march 3rd 1990"));
    }

    // ── Name + address ────────────────────────────────────────────────────────

    #[test]
    fn name_and_address_co_occurrence_triggers_sensitive() {
        assert!(sensitive(
            "full name: John Smith, home address: 123 Main Street"
        ));
    }

    #[test]
    fn address_alone_is_not_sensitive() {
        // Just an address mention without a name context should not trigger
        // (avoids false-positive on "turn left on Main Street").
        assert!(public_text("turn left on Main Street heading toward the park"));
    }

    // ── VA / disability ───────────────────────────────────────────────────────

    #[test]
    fn va_claim_triggers_sensitive() {
        assert!(sensitive("my va claim number is C12345678"));
    }

    #[test]
    fn disability_rating_triggers_sensitive() {
        assert!(sensitive("my disability rating was increased to 70%"));
    }

    #[test]
    fn service_connected_triggers_sensitive() {
        assert!(sensitive("this is a service-connected condition"));
    }

    #[test]
    fn cp_exam_triggers_sensitive() {
        assert!(sensitive("I have a c&p exam scheduled next week"));
    }

    #[test]
    fn nexus_letter_triggers_sensitive() {
        assert!(sensitive("the nexus letter links my condition to service"));
    }

    // ── Custody / family court ────────────────────────────────────────────────

    #[test]
    fn custody_triggers_sensitive() {
        assert!(sensitive("shared custody arrangement for the children"));
    }

    #[test]
    fn parenting_plan_triggers_sensitive() {
        assert!(sensitive("update the parenting plan for summer vacation"));
    }

    #[test]
    fn child_support_triggers_sensitive() {
        assert!(sensitive("child support payments are due on the 1st"));
    }

    // ── Health / medical ──────────────────────────────────────────────────────

    #[test]
    fn diagnosis_triggers_sensitive() {
        assert!(sensitive("my diagnosis is PTSD and depression"));
    }

    #[test]
    fn prescription_triggers_sensitive() {
        assert!(sensitive("my prescription expires next month"));
    }

    #[test]
    fn medical_record_triggers_sensitive() {
        assert!(sensitive("attach the medical record from 2023"));
    }

    #[test]
    fn hipaa_triggers_sensitive() {
        assert!(sensitive("this data is protected under HIPAA"));
    }

    // ── Financial ─────────────────────────────────────────────────────────────

    #[test]
    fn account_number_triggers_sensitive() {
        assert!(sensitive("bank account number 123456789"));
    }

    #[test]
    fn routing_number_triggers_sensitive() {
        assert!(sensitive("routing number for the wire transfer"));
    }

    #[test]
    fn credit_card_triggers_sensitive() {
        assert!(sensitive("credit card ending in 4242"));
    }

    // ── Personal-scope path ───────────────────────────────────────────────────

    #[test]
    fn downloads_path_is_personal_scope() {
        assert!(path_is_personal_scope(Path::new("~/Downloads/notes.md")));
    }

    #[test]
    fn downloads_absolute_with_home_env() {
        // Use the real HOME env to build the expected path.
        if let Ok(home) = std::env::var("HOME") {
            let p = PathBuf::from(format!("{}/Downloads/thread.md", home));
            assert!(path_is_personal_scope(&p));
        }
    }

    #[test]
    fn sites_path_is_not_personal_scope() {
        assert!(!path_is_personal_scope(Path::new("~/Sites/myproject/src/main.rs")));
    }

    #[test]
    fn classify_with_path_personal_scope_overrides_public_text() {
        // Even benign text from ~/Downloads is treated as Sensitive.
        let result = classify_sensitivity_with_path(
            "refactor the auth module",
            Path::new("~/Downloads/personal/notes.md"),
        );
        assert_eq!(result, ContentSensitivity::Sensitive);
    }

    // ── SensitivityPolicy ────────────────────────────────────────────────────

    #[test]
    fn policy_allows_claude_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "claude");
        assert!(v.is_allow(), "claude should be allowed for sensitive content");
    }

    #[test]
    fn policy_allows_claude_variant_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "claude-opus-4-5");
        assert!(v.is_allow());
    }

    #[test]
    fn policy_allows_extra_claude_account_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "acc2");
        assert!(v.is_allow());
    }

    #[test]
    fn policy_allows_local_llm_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "local-llm");
        assert!(v.is_allow());
    }

    #[test]
    fn policy_blocks_gemini_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "gemini");
        assert!(v.is_deny(), "gemini must be blocked for sensitive content");
    }

    #[test]
    fn policy_blocks_openai_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "openai");
        assert!(v.is_deny());
    }

    #[test]
    fn policy_blocks_gfp_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "gfp");
        assert!(v.is_deny(), "gfp (Gemini Free Pool) must be blocked for sensitive content");
    }

    #[test]
    fn policy_blocks_oc_go_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "oc-go");
        assert!(v.is_deny());
    }

    #[test]
    fn policy_blocks_codex_for_sensitive() {
        let policy = SensitivityPolicy::new();
        let v = policy.check(ContentSensitivity::Sensitive, "codex");
        assert!(v.is_deny());
    }

    #[test]
    fn policy_allows_any_provider_for_public() {
        let policy = SensitivityPolicy::new();
        // Public content is unrestricted.
        for provider in &["gemini", "openai", "gfp", "oc-go", "codex", "claude"] {
            let v = policy.check(ContentSensitivity::Public, provider);
            assert!(v.is_allow(), "public content should be allowed on {}", provider);
        }
    }

    // ── provider_is_trusted_for_sensitive ────────────────────────────────────

    #[test]
    fn trusted_providers_list() {
        assert!(provider_is_trusted_for_sensitive("claude"));
        assert!(provider_is_trusted_for_sensitive("claude-sonnet-4-6"));
        assert!(provider_is_trusted_for_sensitive("acc2"));
        assert!(provider_is_trusted_for_sensitive("acc3"));
        assert!(provider_is_trusted_for_sensitive("local"));
        assert!(provider_is_trusted_for_sensitive("local-llm"));
        assert!(provider_is_trusted_for_sensitive("local-llm-llama"));
    }

    #[test]
    fn untrusted_providers_list() {
        assert!(!provider_is_trusted_for_sensitive("gemini"));
        assert!(!provider_is_trusted_for_sensitive("openai"));
        assert!(!provider_is_trusted_for_sensitive("gfp"));
        assert!(!provider_is_trusted_for_sensitive("oc-go"));
        assert!(!provider_is_trusted_for_sensitive("codex"));
        assert!(!provider_is_trusted_for_sensitive("opencode"));
    }
}
