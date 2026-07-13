//! models/models.yaml drift check — daemon boot-time provider-set sanity.
//!
//! # Purpose
//!
//! `models/models.yaml` (repo root) is the canonical "which sub has which
//! model" source of truth referenced by ASI/PPI docs, but it is a docs-first
//! file. To make it load-bearing at runtime (rather than a doc that silently
//! rots), this module embeds the file at compile time via `include_str!`,
//! prefers a valid refreshed cache at `~/.cascade/models.yaml`, and, on daemon
//! boot, compares its canonical provider-family set against the live provider
//! set the daemon actually knows about (from `providers.json`, harness values
//! mapped to provider families). Any drift is logged as a single concise WARN
//! — never fatal, never blocking.
//!
//! # Inputs / Outputs
//!
//! - `CANONICAL_MODELS_YAML` — the embedded fallback contents of
//!   `models/models.yaml`, resolved at compile time relative to this crate's
//!   `src/` directory.
//! - `~/.cascade/models.yaml` — optional refreshed cache written by
//!   `cascade update models`; used only when present and parseable.
//! - `canonical_provider_families()` — parses the cached or embedded YAML and
//!   returns the set of top-level `providers[].name` values (e.g. "anthropic",
//!   "openai", "google", "opencode").
//! - `live_provider_families(harnesses)` — maps the harness values seen in
//!   `providers.json` (`"cc" | "oc" | "codex" | "cursor" | "gemini"`) to the
//!   same provider-family vocabulary used by models.yaml.
//! - `diff_provider_families(canonical, live)` — pure set comparison, returns
//!   the symmetric-ish diff used for the warn message (see [`DriftReport`]).
//! - `check_and_warn(harnesses)` — the entry point wired into `main.rs`;
//!   performs the full parse + compare + log-if-drifted flow.
//!
//! # Constraints
//!
//! - Non-fatal: a cache read/parse failure, embedded parse failure, or empty
//!   live set never panics or blocks startup — it is logged where actionable
//!   (or silently skipped for an empty live set, which is the common
//!   default-install case) and the daemon proceeds.
//! - Cheap: runs once at startup; the only runtime I/O is one optional cache
//!   read from `~/.cascade/models.yaml`.
//! - `cursor` has no corresponding models.yaml entry today (no known model
//!   catalog is tracked for it) — it is intentionally mapped to `None` and
//!   excluded from the comparison rather than flagged as permanent drift.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — cascade-daemon: models.yaml drift check (A1)

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use tracing::warn;

// ── Embedded canonical source ─────────────────────────────────────────────────

/// The full contents of `models/models.yaml`, embedded at compile time.
///
/// Path is relative to this file (`crates/cascade-daemon/src/model_drift.rs`):
/// three `../` hops reach the repo root, then into `models/models.yaml`.
const CANONICAL_MODELS_YAML: &str = include_str!("../../../models/models.yaml");

/// Minimal shape for parsing `models/models.yaml` — only the top-level
/// provider family names are needed for drift detection; per-model fields
/// (tier, context_tokens, etc.) are irrelevant here and deliberately omitted
/// so this parser never breaks when models.yaml gains new per-model fields.
#[derive(Debug, serde::Deserialize)]
struct ModelsYaml {
    providers: Vec<ProviderBlock>,
}

#[derive(Debug, serde::Deserialize)]
struct ProviderBlock {
    name: String,
}

// ── Canonical set ──────────────────────────────────────────────────────────────

/// Parse the refreshed `~/.cascade/models.yaml` cache when it exists and is
/// valid, otherwise parse [`CANONICAL_MODELS_YAML`], then return the set of
/// provider-family names it declares (e.g. `{"anthropic", "openai", "google",
/// "opencode"}`).
///
/// Returns `None` only if the embedded fallback YAML fails to parse — this
/// should never happen in practice (the file is committed and CI-verified),
/// but a defensive `None` keeps this module non-fatal rather than panicking on
/// a malformed build.
fn canonical_provider_families() -> Option<BTreeSet<String>> {
    canonical_provider_families_from(&models_yaml_cache_path())
}

fn canonical_provider_families_from(cache_path: &Path) -> Option<BTreeSet<String>> {
    if let Ok(cached) = std::fs::read_to_string(cache_path) {
        if let Some(families) = parse_provider_families(&cached) {
            return Some(families);
        }
    }

    parse_provider_families(CANONICAL_MODELS_YAML)
}

fn parse_provider_families(contents: &str) -> Option<BTreeSet<String>> {
    let parsed: ModelsYaml = serde_yaml::from_str(contents).ok()?;
    let families: BTreeSet<String> = parsed
        .providers
        .into_iter()
        .map(|p| p.name.trim().to_string())
        .filter(|name| !name.is_empty())
        .collect();
    (!families.is_empty()).then_some(families)
}

fn models_yaml_cache_path() -> PathBuf {
    cascade_types::paths::global_cascade_dir().join("models.yaml")
}

// ── Live set ───────────────────────────────────────────────────────────────────

/// Map a `providers.json` harness value to the models.yaml provider-family
/// vocabulary. `providers.json` harnesses are per-account
/// (`"cc" | "oc" | "codex" | "cursor" | "gemini"`); models.yaml groups by
/// provider family (`"anthropic" | "opencode" | "openai" | "google"`).
///
/// Returns `None` for harnesses with no tracked models.yaml family (currently
/// `"cursor"` and any future/unknown harness) — callers must exclude `None`
/// from the comparison rather than treat it as drift.
fn harness_to_family(harness: &str) -> Option<&'static str> {
    match harness {
        "cc" => Some("anthropic"),
        "oc" => Some("opencode"),
        "codex" => Some("openai"),
        "gemini" => Some("google"),
        _ => None,
    }
}

/// Map the harness values present in the live `providers.json` entries to the
/// models.yaml provider-family set. Unknown/unmapped harnesses (e.g.
/// `"cursor"`) are silently dropped — see [`harness_to_family`].
fn live_provider_families<'a>(harnesses: impl IntoIterator<Item = &'a str>) -> BTreeSet<String> {
    harnesses
        .into_iter()
        .filter_map(harness_to_family)
        .map(str::to_string)
        .collect()
}

// ── Diff ───────────────────────────────────────────────────────────────────────

/// Result of comparing the canonical (models.yaml) and live (providers.json)
/// provider-family sets.
#[derive(Debug, Default, PartialEq, Eq)]
pub struct DriftReport {
    /// Families in models.yaml but not seen live (e.g. tracked in docs but no
    /// account of that family is currently configured).
    pub only_in_canonical: Vec<String>,
    /// Families seen live but absent from models.yaml (a new harness family
    /// was wired up without updating the docs).
    pub only_in_live: Vec<String>,
}

impl DriftReport {
    /// `true` when both sides agree — nothing to warn about.
    pub fn is_empty(&self) -> bool {
        self.only_in_canonical.is_empty() && self.only_in_live.is_empty()
    }
}

/// Pure set comparison between the canonical and live provider-family sets.
///
/// `canonical` is always models.yaml's declared families; `live` is whatever
/// the daemon currently sees configured. Order-independent; returns sorted
/// `Vec`s (via `BTreeSet` iteration) for stable, deterministic warn messages.
fn diff_provider_families(canonical: &BTreeSet<String>, live: &BTreeSet<String>) -> DriftReport {
    DriftReport {
        only_in_canonical: canonical.difference(live).cloned().collect(),
        only_in_live: live.difference(canonical).cloned().collect(),
    }
}

// ── Entry point ────────────────────────────────────────────────────────────────

/// Run the full models.yaml drift check and log a single WARN if the live
/// provider-family set (derived from the given `providers.json` harness
/// values) disagrees with the canonical set embedded from models.yaml.
///
/// Non-fatal by design:
/// - If models.yaml fails to parse, logs at WARN and returns (no drift check
///   possible, but the daemon must still boot).
/// - If `harnesses` is empty (the common default/fresh-install case — no
///   providers configured yet), skips silently. An empty live set is not
///   drift; it just means nothing is configured yet.
/// - Otherwise, compares and logs at most one concise WARN line listing any
///   families present in one set but not the other.
pub fn check_and_warn<'a>(harnesses: impl IntoIterator<Item = &'a str>) {
    let harnesses: Vec<&str> = harnesses.into_iter().collect();
    if harnesses.is_empty() {
        // Default/fresh install with no providers configured yet — nothing
        // to compare against, and warning here would just be startup noise.
        return;
    }

    let Some(canonical) = canonical_provider_families() else {
        warn!("model_drift: failed to parse cached or embedded models.yaml — skipping drift check");
        return;
    };

    let live = live_provider_families(harnesses);
    let report = diff_provider_families(&canonical, &live);

    if !report.is_empty() {
        warn!(
            only_in_models_yaml = ?report.only_in_canonical,
            only_in_live_providers = ?report.only_in_live,
            "model_drift: live provider set disagrees with models/models.yaml"
        );
    }
}

// ── Tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// The embedded models.yaml must always parse — this is a build-time
    /// guarantee check, not just a unit test: if this ever fails it means the
    /// committed models.yaml is malformed relative to what this parser
    /// expects (e.g. `providers[].name` field renamed).
    #[test]
    fn embedded_models_yaml_parses_and_has_known_families() {
        let families =
            parse_provider_families(CANONICAL_MODELS_YAML).expect("models.yaml must parse");
        // These four are the current canonical set (verified 2026-07-06,
        // models.yaml header). This assertion pins the expectation so a
        // silent schema change (e.g. renaming `name` to `provider_name`) is
        // caught immediately rather than degrading into a silent no-op check.
        for expected in ["anthropic", "openai", "google", "opencode"] {
            assert!(
                families.contains(expected),
                "expected family {expected:?} missing from parsed models.yaml: {families:?}"
            );
        }
    }

    #[test]
    fn no_cache_file_uses_embedded_default() {
        let tmp = tempfile::tempdir().unwrap();
        let cache_path = tmp.path().join("models.yaml");
        let expected = parse_provider_families(CANONICAL_MODELS_YAML).unwrap();
        let families = canonical_provider_families_from(&cache_path).unwrap();
        assert_eq!(families, expected);
    }

    #[test]
    fn valid_cache_file_uses_cache_content() {
        let tmp = tempfile::tempdir().unwrap();
        let cache_path = tmp.path().join("models.yaml");
        std::fs::write(
            &cache_path,
            "providers:\n  - name: cached-provider\n  - name: cached-second\n",
        )
        .unwrap();

        let families = canonical_provider_families_from(&cache_path).unwrap();
        assert_eq!(
            families,
            BTreeSet::from(["cached-provider".to_string(), "cached-second".to_string(),])
        );
    }

    #[test]
    fn corrupt_cache_file_falls_back_to_embedded_default() {
        let tmp = tempfile::tempdir().unwrap();
        let cache_path = tmp.path().join("models.yaml");
        std::fs::write(&cache_path, "providers: [").unwrap();

        let expected = parse_provider_families(CANONICAL_MODELS_YAML).unwrap();
        let families = canonical_provider_families_from(&cache_path).unwrap();
        assert_eq!(families, expected);
    }

    #[test]
    fn harness_to_family_maps_known_harnesses() {
        assert_eq!(harness_to_family("cc"), Some("anthropic"));
        assert_eq!(harness_to_family("oc"), Some("opencode"));
        assert_eq!(harness_to_family("codex"), Some("openai"));
        assert_eq!(harness_to_family("gemini"), Some("google"));
        assert_eq!(harness_to_family("cursor"), None);
        assert_eq!(harness_to_family("something-unknown"), None);
    }

    #[test]
    fn diff_returns_empty_when_sets_match() {
        let canonical: BTreeSet<String> = ["anthropic", "google"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let live = canonical.clone();
        let report = diff_provider_families(&canonical, &live);
        assert!(report.is_empty());
    }

    #[test]
    fn diff_detects_family_missing_from_live() {
        let canonical: BTreeSet<String> = ["anthropic", "google", "openai"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let live: BTreeSet<String> = ["anthropic", "google"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let report = diff_provider_families(&canonical, &live);
        assert_eq!(report.only_in_canonical, vec!["openai".to_string()]);
        assert!(report.only_in_live.is_empty());
    }

    #[test]
    fn diff_detects_family_missing_from_canonical() {
        let canonical: BTreeSet<String> = ["anthropic"].iter().map(|s| s.to_string()).collect();
        let live: BTreeSet<String> = ["anthropic", "google"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let report = diff_provider_families(&canonical, &live);
        assert!(report.only_in_canonical.is_empty());
        assert_eq!(report.only_in_live, vec!["google".to_string()]);
    }

    #[test]
    fn diff_detects_both_directions_simultaneously() {
        let canonical: BTreeSet<String> = ["anthropic", "openai"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let live: BTreeSet<String> = ["anthropic", "google"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        let report = diff_provider_families(&canonical, &live);
        assert_eq!(report.only_in_canonical, vec!["openai".to_string()]);
        assert_eq!(report.only_in_live, vec!["google".to_string()]);
    }

    #[test]
    fn live_provider_families_dedupes_and_drops_unmapped_harnesses() {
        // Multiple "cc" accounts + one unmapped "cursor" harness should
        // collapse to just {"anthropic"} — cursor is dropped, not drift.
        let harnesses = ["cc", "cc", "cursor"];
        let live = live_provider_families(harnesses);
        assert_eq!(live, BTreeSet::from(["anthropic".to_string()]));
    }

    #[test]
    fn check_and_warn_skips_silently_on_empty_live_set() {
        // No providers configured (fresh install) — must not warn, must not
        // panic. Nothing to assert on the log output here (no test hook for
        // captured tracing output in this module); the meaningful assertion
        // is simply that this does not panic.
        check_and_warn(std::iter::empty());
    }
}
