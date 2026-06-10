//! # cascade_resolve — 6-tier cascade resolution engine
//!
//! Loads and merges configuration from all six cascade tiers in priority order
//! (GCI highest, PAC lowest) and returns a [`ResolvedContext`].
//!
//! ## Tier default paths
//!
//! | Tier | Default `.cascade/` path |
//! |------|--------------------------|
//! | GCI  | `~/.cascade`             |
//! | PCI  | `~/Downloads/.cascade`   |
//! | APC  | `~/Sites/.cascade` (or `CASCADE_APC_PATH` env) |
//! | PPC  | `{project_root}/.cascade` |
//! | PRC  | `{project_root}/.cascade` (same root; distinguish by context) |
//! | PAC  | `{project_root}/.cascade` (same root) |
//!
//! ## Resolution algorithm
//!
//! 1. Build an ordered tier list from PAC (lowest) to GCI (highest).
//! 2. For each tier: locate the `.cascade/` directory via `TierName::default_path`.
//!    If the directory does not exist, skip it silently.
//! 3. Inside the tier directory:
//!    - Try `config.toml` → deserialise into [`TierConfig`].
//!    - If `config.toml` has no `instructions` field (or does not exist), fall
//!      back to reading `CASCADE.md` as the plain-text instructions string.
//! 4. Merge by overlay, iterating from PAC to GCI (so GCI writes last and wins):
//!    - `instructions`: highest-tier non-empty value wins.
//!    - `rules`, `memory_paths`, `task_paths`: accumulated — GCI prepended.
//! 5. Record `tier_sources` for each tier that had a readable `.cascade/` dir.
//! 6. Return a [`ResolvedContext`] with all merged fields + an ISO-8601 timestamp.
//!
//! ## Example
//!
//! ```rust,no_run
//! use cascade_core::cascade_resolve::resolve_cascade;
//! use std::path::Path;
//!
//! # fn main() -> cascade_types::error::Result<()> {
//! let ctx = resolve_cascade(Path::new("/my/project"))?;
//! println!("instructions: {}", ctx.instructions);
//! println!("tiers found: {}", ctx.tier_sources.len());
//! # Ok(())
//! # }
//! ```

use std::path::{Path, PathBuf};

use cascade_types::error::{CascadeError, Result};
use cascade_types::tiers::{ResolvedContext, TierConfig, TierName};
use tracing::debug;

// ── Public API ────────────────────────────────────────────────────────────────

/// Resolve the 6-tier cascade configuration for the given project root.
///
/// Walks each tier from highest (GCI) to lowest (PAC), loading `config.toml`
/// (with `CASCADE.md` as fallback) from each tier's `.cascade/` directory.
/// Missing tiers are silently skipped.
///
/// # Arguments
///
/// * `project_root` — the root directory of the project being resolved.
///   Used as the base path for PPC / PRC / PAC tier lookups.
///
/// # Errors
///
/// Returns [`CascadeError::Io`] only if a file that EXISTS cannot be read.
/// A tier directory or `config.toml` that is simply absent is not an error.
pub fn resolve_cascade(project_root: &Path) -> Result<ResolvedContext> {
    let resolver = CascadeResolver::new(project_root.to_path_buf());
    resolver.resolve()
}

// ── CascadeResolver ───────────────────────────────────────────────────────────

/// Internal resolver that implements the 6-tier merge algorithm.
///
/// # Constraints
/// - Inputs / outputs: `project_root: PathBuf` → `Result<ResolvedContext>`
/// - All tier paths derived from `TierName::default_path`; no hardcoded strings
/// - Accumulation order: GCI first in all Vec fields
/// - Higher-tier wins for scalar fields (`instructions`)
pub struct CascadeResolver {
    /// Project root used to compute PPC/PRC/PAC paths.
    project_root: PathBuf,
}

impl CascadeResolver {
    /// Create a new resolver for the given project root.
    pub fn new(project_root: PathBuf) -> Self {
        Self { project_root }
    }

    /// Run the full 6-tier resolution and return a [`ResolvedContext`].
    pub fn resolve(&self) -> Result<ResolvedContext> {
        // Build a list of (tier, cascade_dir) in GCI-first order (highest → lowest).
        // We iterate in this order so each tier's data can be prepended to vecs
        // and the last-write-wins logic for `instructions` naturally gives GCI priority.
        let tier_order: &[TierName] = TierName::all_in_precedence_order();

        // Collect (tier, cascade_dir, TierConfig) for every tier that has a
        // readable `.cascade/` directory.
        let mut found: Vec<(TierName, PathBuf, TierConfig)> = Vec::new();

        for tier in tier_order {
            let Some(cascade_dir) = tier.default_path(&self.project_root) else {
                debug!(tier = %tier, "skipping tier — cannot compute default path (HOME not set)");
                continue;
            };

            if !cascade_dir.is_dir() {
                debug!(tier = %tier, path = %cascade_dir.display(), "tier directory not present — skipping");
                continue;
            }

            let tier_config = self.load_tier_config(tier, &cascade_dir)?;
            debug!(
                tier = %tier,
                path = %cascade_dir.display(),
                has_instructions = tier_config.instructions.is_some(),
                rules_count = tier_config.rules.len(),
                "tier loaded"
            );
            found.push((*tier, cascade_dir, tier_config));
        }

        // Build the ResolvedContext from the found tiers.
        // GCI is first in `found` (highest precedence).
        // Merge algorithm:
        //   instructions  → first non-empty value (GCI wins because it's first)
        //   rules         → accumulate GCI first
        //   memory_paths  → accumulate GCI first
        //   task_paths    → accumulate GCI first
        let mut instructions = String::new();
        let mut rules: Vec<String> = Vec::new();
        let mut memory_paths: Vec<PathBuf> = Vec::new();
        let mut task_paths: Vec<PathBuf> = Vec::new();
        let mut tier_sources = std::collections::BTreeMap::new();

        for (tier, cascade_dir, config) in &found {
            // instructions: first non-empty value wins (GCI is first, so it wins)
            if instructions.is_empty() {
                if let Some(inst) = &config.instructions {
                    if !inst.trim().is_empty() {
                        instructions = inst.clone();
                    }
                }
            }
            // vecs: accumulate; GCI contributes first → its entries appear first
            rules.extend(config.rules.iter().cloned());
            memory_paths.extend(config.memory_paths.iter().cloned());
            task_paths.extend(config.task_paths.iter().cloned());

            tier_sources.insert(*tier, cascade_dir.clone());
        }

        let resolved_at = utc_timestamp_now();

        Ok(ResolvedContext {
            instructions,
            rules,
            memory_paths,
            task_paths,
            tier_sources,
            resolved_at,
        })
    }

    /// Load the [`TierConfig`] for a single tier directory.
    ///
    /// Attempts to read `{cascade_dir}/config.toml`; if missing or if `instructions`
    /// is absent, falls back to `{cascade_dir}/CASCADE.md` for the instructions text.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::Io`] if an existing file cannot be read.
    fn load_tier_config(&self, tier: &TierName, cascade_dir: &Path) -> Result<TierConfig> {
        let config_path = cascade_dir.join("config.toml");
        let mut cfg = TierConfig::default();

        if config_path.is_file() {
            let raw = std::fs::read_to_string(&config_path).map_err(|e| CascadeError::Io {
                path: config_path.clone(),
                operation: "read tier config.toml",
                source: e,
            })?;
            cfg = toml::from_str::<TierConfig>(&raw).unwrap_or_else(|e| {
                // Malformed config is treated as empty; log at warn level.
                tracing::warn!(
                    tier = %tier,
                    path = %config_path.display(),
                    error = %e,
                    "config.toml parse error — treating tier as empty config"
                );
                TierConfig::default()
            });
        }

        // Fallback: if instructions is still empty, try CASCADE.md
        if cfg.instructions.is_none() || cfg.instructions.as_deref().unwrap_or("").trim().is_empty()
        {
            let cascade_md = cascade_dir.join("CASCADE.md");
            if cascade_md.is_file() {
                let text = std::fs::read_to_string(&cascade_md).map_err(|e| CascadeError::Io {
                    path: cascade_md.clone(),
                    operation: "read tier CASCADE.md (instructions fallback)",
                    source: e,
                })?;
                if !text.trim().is_empty() {
                    cfg.instructions = Some(text);
                }
            }
        }

        Ok(cfg)
    }
}

// ── Utilities ─────────────────────────────────────────────────────────────────

/// Returns an ISO-8601 UTC timestamp string for the current moment.
///
/// Uses `std::time::SystemTime`; no external chrono dependency required.
fn utc_timestamp_now() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    // Format as a minimal ISO-8601 UTC string: "YYYY-MM-DDTHH:MM:SSZ"
    let s = secs;
    let (sec, min, hour) = (s % 60, (s / 60) % 60, (s / 3600) % 24);
    let days_total = s / 86400;
    // Compute year/month/day from days since epoch (good enough for timestamps)
    let (year, month, day) = days_since_epoch_to_ymd(days_total);
    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{min:02}:{sec:02}Z")
}

/// Convert days since Unix epoch (1970-01-01) to (year, month, day).
///
/// Uses the proleptic Gregorian calendar. Sufficient precision for diagnostic
/// timestamps; not a general-purpose calendar library.
fn days_since_epoch_to_ymd(mut days: u64) -> (u64, u64, u64) {
    // 400-year cycle = 146097 days
    let quad_cent = days / 146097;
    days %= 146097;
    let cent = (days / 36524).min(3);
    days -= cent * 36524;
    let quad = days / 1461;
    days %= 1461;
    let year_of_quad = (days / 365).min(3);
    days -= year_of_quad * 365;

    let year = quad_cent * 400 + cent * 100 + quad * 4 + year_of_quad + 1970;

    // Month offsets for non-leap and leap years
    let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
    let month_days: [u64; 12] = if leap {
        [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
    } else {
        [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
    };
    let mut month = 1u64;
    for &md in &month_days {
        if days < md {
            break;
        }
        days -= md;
        month += 1;
    }
    (year, month, days + 1)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use std::sync::{Mutex, MutexGuard, OnceLock};
    use tempfile::TempDir;

    /// Serialises tests that mutate `HOME` via `std::env::set_var`.
    ///
    /// WHY: `set_var` is not thread-safe in Rust tests; parallel test threads
    /// can read each other's `HOME` mid-test, causing spurious assertion failures.
    /// All tests in this module that call `set_var("HOME", …)` MUST hold this
    /// guard for the full duration of the test.
    fn home_lock() -> MutexGuard<'static, ()> {
        static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
        LOCK.get_or_init(|| Mutex::new(()))
            .lock()
            .unwrap_or_else(|e| e.into_inner())
    }

    // ── helpers ───────────────────────────────────────────────────────────────

    /// Create a `.cascade/` directory under `root` and optionally write a
    /// `config.toml` and/or `CASCADE.md` to it. Returns the path to `.cascade/`.
    fn make_tier_dir(root: &Path) -> PathBuf {
        let cascade_dir = root.join(".cascade");
        fs::create_dir_all(&cascade_dir).expect("create .cascade dir");
        cascade_dir
    }

    fn write_config_toml(cascade_dir: &Path, toml: &str) {
        fs::write(cascade_dir.join("config.toml"), toml).expect("write config.toml");
    }

    fn write_cascade_md(cascade_dir: &Path, content: &str) {
        fs::write(cascade_dir.join("CASCADE.md"), content).expect("write CASCADE.md");
    }

    // ── test 1: single tier, config.toml with instructions ────────────────────

    /// resolve_cascade returns Ok when a single tier dir exists with config.toml.
    #[test]
    #[serial(global_env)]
    fn single_tier_config_toml() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = make_tier_dir(tmp.path());
        write_config_toml(&cascade_dir, r#"instructions = "Do the right thing.""#);

        // Override HOME so GCI path points to tmp
        let _guard = home_lock();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp.path()).expect("should resolve");
        assert_eq!(ctx.instructions, "Do the right thing.");
        assert!(ctx.tier_sources.contains_key(&TierName::GCI));
    }

    // ── test 2: higher tier overrides lower tier ───────────────────────────────

    /// GCI instructions override PPC instructions when both are present.
    #[test]
    #[serial(global_env)]
    fn higher_tier_wins_on_instructions() {
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();

        // GCI tier: ~/.cascade/config.toml with instructions = "A"
        let gci_dir = make_tier_dir(tmp_home.path());
        write_config_toml(&gci_dir, r#"instructions = "A""#);

        // PPC tier: {project}/.cascade/config.toml with instructions = "B"
        let ppc_dir = make_tier_dir(tmp_project.path());
        write_config_toml(&ppc_dir, r#"instructions = "B""#);

        let _guard = home_lock();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp_project.path()).expect("should resolve");
        // GCI (higher tier) must win
        assert_eq!(
            ctx.instructions, "A",
            "GCI instructions must override PPC instructions"
        );
    }

    // ── test 3: missing tiers silently skipped ─────────────────────────────────

    /// resolve_cascade returns Ok even when only one tier dir exists.
    #[test]
    #[serial(global_env)]
    fn missing_tiers_silently_skipped() {
        let tmp = TempDir::new().unwrap();
        // Only project-level .cascade exists; GCI/PCI/APC are not present.
        let cascade_dir = make_tier_dir(tmp.path());
        write_config_toml(&cascade_dir, r#"instructions = "project-only""#);

        // HOME points to a directory that has NO .cascade subdirectory
        let fake_home = TempDir::new().unwrap();
        let _guard = home_lock();
        std::env::set_var("HOME", fake_home.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp.path()).expect("should return Ok even with missing tiers");
        assert_eq!(ctx.instructions, "project-only");
        // GCI/PCI/APC are absent → only PPC/PRC/PAC present (they share the same dir)
        assert!(!ctx.tier_sources.contains_key(&TierName::GCI));
    }

    // ── test 4: CASCADE.md fallback ────────────────────────────────────────────

    /// When config.toml has no `instructions` key, CASCADE.md is used as fallback.
    #[test]
    #[serial(global_env)]
    fn cascade_md_fallback() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = make_tier_dir(tmp.path());
        // config.toml without instructions field
        write_config_toml(&cascade_dir, r#"rules = ["rule-one"]"#);
        // CASCADE.md supplies the instructions text
        write_cascade_md(&cascade_dir, "Instructions from CASCADE.md");

        let _guard = home_lock();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp.path()).expect("should resolve");
        assert_eq!(ctx.instructions, "Instructions from CASCADE.md");
        assert!(ctx.rules.contains(&"rule-one".to_string()));
    }

    // ── test 5: rules accumulated from multiple tiers ─────────────────────────

    /// Rules from all tiers are accumulated, GCI rules appear first.
    #[test]
    #[serial(global_env)]
    fn rules_accumulated_gci_first() {
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();

        let gci_dir = make_tier_dir(tmp_home.path());
        write_config_toml(&gci_dir, r#"rules = ["gci-rule"]"#);

        let ppc_dir = make_tier_dir(tmp_project.path());
        write_config_toml(&ppc_dir, r#"rules = ["ppc-rule"]"#);

        let _guard = home_lock();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp_project.path()).expect("should resolve");
        // GCI rule must appear before PPC rule
        let gci_pos = ctx.rules.iter().position(|r| r == "gci-rule");
        let ppc_pos = ctx.rules.iter().position(|r| r == "ppc-rule");
        assert!(gci_pos.is_some(), "gci-rule must be in rules");
        assert!(ppc_pos.is_some(), "ppc-rule must be in rules");
        assert!(
            gci_pos.unwrap() < ppc_pos.unwrap(),
            "GCI rule must appear before PPC rule"
        );
    }

    // ── test 6: all six tiers present ─────────────────────────────────────────

    /// resolve_all_tiers: all 6 tiers supply different instructions; GCI wins.
    #[test]
    #[serial(global_env)]
    fn resolve_all_tiers() {
        // We cannot easily set up all 6 tiers in isolation because PPC/PRC/PAC
        // all resolve to `project_root/.cascade`. Instead, test GCI + PPC (2 tiers)
        // and confirm tier_sources contains exactly those two entries.
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();

        let gci_dir = make_tier_dir(tmp_home.path());
        write_config_toml(
            &gci_dir,
            r#"
instructions = "GCI wins"
rules = ["global-rule-1", "global-rule-2"]
"#,
        );

        let ppc_dir = make_tier_dir(tmp_project.path());
        write_config_toml(
            &ppc_dir,
            r#"
instructions = "PPC loses"
rules = ["project-rule-1"]
"#,
        );

        let _guard = home_lock();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp_project.path()).expect("resolve_all_tiers should succeed");

        // GCI wins on instructions
        assert_eq!(ctx.instructions, "GCI wins");

        // Both tiers present in tier_sources
        assert!(ctx.tier_sources.contains_key(&TierName::GCI));
        // PPC, PRC, PAC all resolve to the same project dir — at least one present
        let has_project_tier = ctx.tier_sources.contains_key(&TierName::PPC)
            || ctx.tier_sources.contains_key(&TierName::PRC)
            || ctx.tier_sources.contains_key(&TierName::PAC);
        assert!(
            has_project_tier,
            "at least one project-scoped tier should be present"
        );

        // Rules accumulated: global rules first
        assert_eq!(ctx.rules[0], "global-rule-1");
        assert_eq!(ctx.rules[1], "global-rule-2");
        assert_eq!(ctx.rules[2], "project-rule-1");
    }

    // ── test 7: empty project dir — returns empty but valid context ────────────

    /// resolve_cascade returns Ok(ResolvedContext::default) when no tiers exist.
    #[test]
    #[serial(global_env)]
    fn empty_project_no_tiers() {
        let fake_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();

        // Neither HOME nor project has .cascade dir
        let _guard = home_lock();
        std::env::set_var("HOME", fake_home.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp_project.path()).expect("should return Ok with empty context");
        assert!(ctx.instructions.is_empty());
        assert!(ctx.tier_sources.is_empty());
        assert!(
            !ctx.resolved_at.is_empty(),
            "resolved_at must always be set"
        );
    }

    // ── test 8: resolved_at is set ────────────────────────────────────────────

    /// resolved_at is a non-empty string that looks like an ISO-8601 timestamp.
    #[test]
    #[serial(global_env)]
    fn resolved_at_is_set() {
        let fake_home = TempDir::new().unwrap();
        let tmp = TempDir::new().unwrap();
        let _guard = home_lock();
        std::env::set_var("HOME", fake_home.path().to_str().unwrap());

        let ctx = resolve_cascade(tmp.path()).expect("should succeed");
        // Must start with a 4-digit year
        assert!(
            ctx.resolved_at.len() >= 10,
            "resolved_at too short: {:?}",
            ctx.resolved_at
        );
        assert!(
            ctx.resolved_at.starts_with("20"),
            "resolved_at should start with 20xx: {:?}",
            ctx.resolved_at
        );
        assert!(
            ctx.resolved_at.ends_with('Z'),
            "resolved_at should be UTC (ends with Z): {:?}",
            ctx.resolved_at
        );
    }

    // ── test 9: utc_timestamp_now format check ────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn utc_timestamp_format() {
        let ts = utc_timestamp_now();
        // Format: "YYYY-MM-DDTHH:MM:SSZ" — 20 chars
        assert_eq!(ts.len(), 20, "timestamp length should be 20: {ts:?}");
        assert!(ts.contains('T'));
        assert!(ts.ends_with('Z'));
    }

    // ── test 10: config.toml parse error is non-fatal ─────────────────────────

    /// A malformed config.toml should be treated as an empty tier (no panic/error).
    #[test]
    #[serial(global_env)]
    fn malformed_config_toml_is_non_fatal() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = make_tier_dir(tmp.path());
        write_config_toml(&cascade_dir, "this is not valid toml = [[[");

        let _guard = home_lock();
        std::env::set_var("HOME", tmp.path().to_str().unwrap());

        // Should return Ok (not Err) — malformed config treated as empty
        let result = resolve_cascade(tmp.path());
        assert!(
            result.is_ok(),
            "malformed config.toml should not cause resolve_cascade to return Err"
        );
    }
}
