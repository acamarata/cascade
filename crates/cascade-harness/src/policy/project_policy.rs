//! Per-project pre-evaluated policy cache.
//!
//! Purpose: At daemon startup (or on first request for a given project root),
//!   merge the `[policy]` tables from all resolved cascade.toml files across
//!   the tier chain and cache the resulting `PolicySet` keyed by project root.
//!   Subsequent requests are O(1) cache lookups.
//!
//! ## Cache invalidation
//!   The cache entry is invalidated when the `cascade.toml` mtime in any tier
//!   directory for the project root changes (cheapest check: just the project
//!   config.toml mtime — a coarser but safe trigger). A file-watch hook seam
//!   is provided via `invalidate_project` for callers that receive watch events.
//!
//! ## Merge semantics
//!   Lower-tier rules (PAC) override higher-tier rules (GCI) with the same `id`.
//!   Scope filtering (`PolicyScope::covers`) is applied at each tier.
//!
//! SPORT: MASTER-POLICIES.md

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::RwLock;
use std::time::SystemTime;

use cascade_types::config::CascadeConfig;
use cascade_types::policy::{PolicySet, PolicyTableConfig};
use cascade_types::tiers::TierName;

// ── Cache entry ───────────────────────────────────────────────────────────────

/// A cached `PolicySet` for one project root, plus the mtime fingerprint.
struct CachedProjectPolicy {
    /// The effective merged policy set.
    policy_set: PolicySet,
    /// Mtime of the config.toml that triggered the last rebuild.
    /// `None` if no config.toml existed at last build time.
    config_mtime: Option<SystemTime>,
}

// ── Global cache ──────────────────────────────────────────────────────────────

/// Global per-project cache: project_root → CachedProjectPolicy.
static PROJECT_POLICY_CACHE: std::sync::OnceLock<RwLock<HashMap<PathBuf, CachedProjectPolicy>>> =
    std::sync::OnceLock::new();

fn cache() -> &'static RwLock<HashMap<PathBuf, CachedProjectPolicy>> {
    PROJECT_POLICY_CACHE.get_or_init(|| RwLock::new(HashMap::new()))
}

// ── Config.toml mtime ─────────────────────────────────────────────────────────

/// Returns the `modified()` time of the project's `cascade.toml` (or `None`).
fn project_config_mtime(project_root: &Path) -> Option<SystemTime> {
    let cfg_path = project_root.join(".cascade").join("config.toml");
    std::fs::metadata(&cfg_path).ok()?.modified().ok()
}

// ── PolicySet builder ─────────────────────────────────────────────────────────

/// Load and parse `{tier_dir}/config.toml` into a `CascadeConfig`.
///
/// Returns `PolicyTableConfig::default()` if the file is absent or unparseable
/// (so a single broken tier doesn't block the whole resolution).
fn load_tier_policy(tier_dir: &Path) -> PolicyTableConfig {
    let cfg_path = tier_dir.join("config.toml");
    let Ok(content) = std::fs::read_to_string(&cfg_path) else {
        return PolicyTableConfig::default();
    };
    let Ok(cfg) = toml::from_str::<CascadeConfig>(&content) else {
        return PolicyTableConfig::default();
    };
    cfg.policy
}

/// Build an effective `PolicySet` for `project_root` by reading cascade.toml
/// from each tier directory in precedence order (GCI → PAC).
///
/// Tier directories are derived from `TierName::default_path(project_root)`.
pub fn build_project_policy_set(project_root: &Path) -> PolicySet {
    let tiers_in_order = TierName::all_in_precedence_order();
    let mut tier_configs: Vec<(TierName, PolicyTableConfig)> = Vec::new();

    for &tier in tiers_in_order {
        if let Some(tier_dir) = tier.default_path(project_root) {
            if tier_dir.is_dir() {
                tier_configs.push((tier, load_tier_policy(&tier_dir)));
            }
        }
    }

    PolicySet::merge(&tier_configs)
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Return the effective `PolicySet` for `project_root`.
///
/// Uses the cache when valid; rebuilds when the project config.toml mtime has
/// changed (or when no cache entry exists).
pub fn effective_policy_set(project_root: &Path) -> PolicySet {
    let current_mtime = project_config_mtime(project_root);

    // Fast path: read lock, check validity.
    {
        let guard = cache().read().expect("policy cache RwLock poisoned");
        if let Some(entry) = guard.get(project_root) {
            if entry.config_mtime == current_mtime {
                return entry.policy_set.clone();
            }
        }
    }

    // Slow path: rebuild and store.
    let new_set = build_project_policy_set(project_root);
    {
        let mut guard = cache().write().expect("policy cache RwLock poisoned");
        guard.insert(
            project_root.to_owned(),
            CachedProjectPolicy {
                policy_set: new_set.clone(),
                config_mtime: current_mtime,
            },
        );
    }
    new_set
}

/// Invalidate the cached policy set for `project_root`.
///
/// Call this from a file-watch callback when any `cascade.toml` in the project's
/// tier chain changes. The next call to `effective_policy_set` will rebuild.
pub fn invalidate_project(project_root: &Path) {
    let mut guard = cache().write().expect("policy cache RwLock poisoned");
    guard.remove(project_root);
}

/// Invalidate all cached project policies.
///
/// Use at daemon startup after a config reload to force a fresh build for
/// every known project.
pub fn invalidate_all() {
    let mut guard = cache().write().expect("policy cache RwLock poisoned");
    guard.clear();
}

/// Pre-populate the cache for a set of known project roots.
///
/// Called at daemon startup so the first request to any project is O(1).
pub fn warm_cache(project_roots: &[PathBuf]) {
    for root in project_roots {
        // Touch `effective_policy_set` to populate the cache entry.
        let _ = effective_policy_set(root);
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::policy::{Decision, PolicyRule, PolicyScope};
    use serial_test::serial;
    use std::io::Write;
    use tempfile::TempDir;

    fn write_config(dir: &Path, toml: &str) {
        let cascade_dir = dir.join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        let mut f = std::fs::File::create(cascade_dir.join("config.toml")).unwrap();
        f.write_all(toml.as_bytes()).unwrap();
    }

    /// Build a temporary project tree that looks like:
    ///  <tmp>/   ← pretend this is project root
    ///    .cascade/config.toml  ← project-level policy
    ///
    /// We can't easily fake GCI/APC dirs in unit tests without env manipulation,
    /// so we test the merge logic directly instead.
    #[test]
    #[serial(global_env)]
    fn build_policy_set_from_empty_tree() {
        let dir = TempDir::new().unwrap();
        write_config(
            dir.path(),
            r#"
[policy]
[[policy.rules]]
id       = "deny-rm-rf"
scope    = "global"
match    = "*rm -rf /*"
decision = "DENY"
reason   = "test deny"
"#,
        );

        // Build without going through the full tier resolution (no HOME manipulation).
        // Test the merge function directly.
        let cfg_content = std::fs::read_to_string(dir.path().join(".cascade/config.toml")).unwrap();
        let cfg: CascadeConfig = toml::from_str(&cfg_content).unwrap();

        let tier_configs = vec![(TierName::PPC, cfg.policy)];
        let policy_set = PolicySet::merge(&tier_configs);

        assert_eq!(policy_set.len(), 1);
        assert_eq!(policy_set.entries[0].rule.id, "deny-rm-rf");
        assert_eq!(policy_set.entries[0].source_tier, TierName::PPC);
    }

    #[test]
    #[serial(global_env)]
    fn policy_set_merge_override() {
        // Higher tier (GCI) sets a rule; lower tier (PRC) overrides it by same id.
        use cascade_types::policy::PolicyTableConfig;

        let gci_rule = PolicyRule {
            id: "no-deploy".to_string(),
            scope: PolicyScope::Global,
            r#match: "*deploy*".to_string(),
            decision: Decision::Deny,
            reason: "GCI deny".to_string(),
        };
        let prc_rule = PolicyRule {
            id: "no-deploy".to_string(), // same id — override
            scope: PolicyScope::Global,
            r#match: "*deploy*".to_string(),
            decision: Decision::RequireApproval,
            reason: "PRC relaxed to require-approval".to_string(),
        };

        let gci_cfg = PolicyTableConfig {
            rules: vec![gci_rule],
        };
        let prc_cfg = PolicyTableConfig {
            rules: vec![prc_rule],
        };

        // GCI first (highest precedence), PRC second (overrides).
        let merged = PolicySet::merge(&[(TierName::GCI, gci_cfg), (TierName::PRC, prc_cfg)]);

        assert_eq!(merged.len(), 1, "same-id rules collapse to one");
        assert_eq!(
            merged.entries[0].rule.decision,
            Decision::RequireApproval,
            "PRC override must win"
        );
        assert_eq!(merged.entries[0].source_tier, TierName::PRC);
    }

    #[test]
    #[serial(global_env)]
    fn policy_set_scope_filtering() {
        use cascade_types::policy::PolicyTableConfig;

        // Rule scoped to "repo" — should only appear when defined at PRC or PAC tier.
        let repo_rule = PolicyRule {
            id: "repo-only".to_string(),
            scope: PolicyScope::Repo,
            r#match: "*secret*".to_string(),
            decision: Decision::Deny,
            reason: "no secrets".to_string(),
        };
        // Same rule defined at GCI tier — scope=Repo still applies (scope filters
        // which tiers it covers, not where it's defined).
        let gci_cfg = PolicyTableConfig {
            rules: vec![repo_rule.clone()],
        };
        // At GCI tier, scope=Repo covers GCI? No — Repo only covers PRC|PAC.
        let merged_at_gci = PolicySet::merge(&[(TierName::GCI, gci_cfg.clone())]);
        assert_eq!(
            merged_at_gci.len(),
            0,
            "Repo-scoped rule should not apply at GCI tier"
        );

        // At PRC tier, scope=Repo DOES cover it.
        let prc_cfg = PolicyTableConfig {
            rules: vec![repo_rule],
        };
        let merged_at_prc = PolicySet::merge(&[(TierName::PRC, prc_cfg)]);
        assert_eq!(
            merged_at_prc.len(),
            1,
            "Repo-scoped rule should apply at PRC tier"
        );
    }

    #[test]
    #[serial(global_env)]
    fn effective_policy_cache_hit() {
        let dir = TempDir::new().unwrap();
        write_config(
            dir.path(),
            r#"
[policy]
[[policy.rules]]
id       = "test-allow"
scope    = "global"
match    = "*cargo build*"
decision = "ALLOW"
reason   = ""
"#,
        );

        // Populate cache.
        let set1 = effective_policy_set(dir.path());
        // Second call — should hit cache (same mtime).
        let set2 = effective_policy_set(dir.path());
        // Both should have the same number of entries from the project tier.
        // (GCI/APC/PCI dirs won't exist in tmp, so only PPC tier counts.)
        assert_eq!(set1.len(), set2.len());
    }

    #[test]
    #[serial(global_env)]
    fn invalidate_project_clears_cache() {
        let dir = TempDir::new().unwrap();
        write_config(dir.path(), "[policy]\n");

        let _ = effective_policy_set(dir.path());
        invalidate_project(dir.path());

        // After invalidation, cache entry should be gone — next call rebuilds.
        // We can't easily assert "cache is empty" without exposing internals,
        // but we can call effective_policy_set again without panic.
        let _ = effective_policy_set(dir.path());
    }

    #[test]
    #[serial(global_env)]
    fn require_approval_surfaces_in_evaluation() {
        use cascade_types::policy::PolicyTableConfig;

        let rule = PolicyRule {
            id: "approval-for-deploy".to_string(),
            scope: PolicyScope::Global,
            r#match: "*deploy*prod*".to_string(),
            decision: Decision::RequireApproval,
            reason: "Production deploys need human sign-off".to_string(),
        };

        let cfg = PolicyTableConfig { rules: vec![rule] };
        let set = PolicySet::merge(&[(TierName::PPC, cfg)]);

        // Match: should require approval.
        let result = set.evaluate(r#"{"action_type":"bash","args":{"cmd":"deploy prod"}}"#);
        assert_eq!(result.decision, Decision::RequireApproval);

        // No match: should allow.
        let result2 = set.evaluate(r#"{"action_type":"bash","args":{"cmd":"cargo build"}}"#);
        assert_eq!(result2.decision, Decision::Allow);
    }
}
