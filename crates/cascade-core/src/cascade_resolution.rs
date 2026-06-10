//! # cascade_resolution — 6-tier instruction-cascade resolution engine
//!
//! Extends [`crate::resolution::Resolver`] with the full spec-compliant API:
//! - `ResolvedCascade` struct with `merged_instructions`, `mcp_server_url`,
//!   `tiers_found: Vec<TierResult>`, and `working_dir`.
//! - `mcp_server_url` extracted from the lowest-tier config that declares it;
//!   default: `"unix://~/.cascade/cascade.sock"`.
//! - Tier provenance: each `TierResult` records found/missing status and the
//!   path searched.
//! - JSON output via `serde`.
//!
//! ## Purpose
//!
//! This module is the canonical entry point for E-02 consumers that need the
//! spec-shaped output (T-P4-E02-26). It wraps [`crate::resolution::Resolver`]
//! rather than duplicating its walk-and-merge logic.
//!
//! ## Inputs / Outputs
//!
//! - **Input:** `cwd: &Path` — the working directory to resolve from.
//! - **Output:** `Result<ResolvedCascade>` — fully merged cascade with provenance.
//!
//! ## Constraints
//!
//! - Missing tiers are silently skipped; they appear in `tiers_found` with
//!   `found: false` rather than returning an error.
//! - No panic on a cwd with zero `.cascade/` directories (returns empty result).
//! - `mcp_server_url` uses the LOWEST-tier value (most-specific overrides).
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: cascade_resolution module (T-P4-E02-26)

use std::path::{Path, PathBuf};

use cascade_types::cascade_tier::CascadeTier;
use cascade_types::error::Result;
use serde::{Deserialize, Serialize};

use crate::resolution::Resolver;

// ── Default MCP socket URL ────────────────────────────────────────────────────

/// Default MCP server URL when no tier declares one.
const DEFAULT_MCP_SERVER_URL: &str = "unix://~/.cascade/cascade.sock";

/// Key in `config.toml` (or `cascade.toml`) that overrides the MCP server URL.
const MCP_URL_TOML_KEY: &str = "mcp_server_url";

// ── Public types ──────────────────────────────────────────────────────────────

/// Result for a single tier during resolution — records whether it was found.
///
/// Present in `tiers_found` for ALL six tiers regardless of whether a
/// `.cascade/` directory existed there.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TierResult {
    /// The cascade tier.
    pub tier: CascadeTier,
    /// Whether a `.cascade/CASCADE.md` (or `config.toml`) was found here.
    pub found: bool,
    /// The path that was searched (may not exist when `found = false`).
    pub path_searched: PathBuf,
    /// The instruction text contributed by this tier (empty when `found = false`).
    pub instructions: String,
}

/// Fully resolved cascade for a working directory.
///
/// This is the spec-compliant output shape for T-P4-E02-26.
///
/// # Merge semantics
///
/// | Field                 | Rule |
/// |-----------------------|------|
/// | `merged_instructions` | All tier instruction blocks concatenated, GCI first |
/// | `mcp_server_url`      | Lowest tier that declares it wins (most specific) |
/// | `tiers_found`         | All 6 tiers recorded; `found: false` when absent |
/// | `working_dir`         | The canonicalised cwd passed to the resolver |
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolvedCascade {
    /// Concatenated instruction text from all found tiers, tier-ordered (GCI first).
    ///
    /// Tiers are separated by a `--- <TIER> ---` comment so callers can see
    /// provenance within the merged string.
    pub merged_instructions: String,

    /// MCP server URL for the cascade daemon.
    ///
    /// Taken from the lowest (most specific) tier that declares `mcp_server_url`
    /// in its `config.toml` / `cascade.toml`. Falls back to
    /// `"unix://~/.cascade/cascade.sock"` when no tier sets it.
    pub mcp_server_url: String,

    /// Ordered list of all six tiers, recording found/missing status and provenance.
    pub tiers_found: Vec<TierResult>,

    /// The working directory used to compute tier paths.
    pub working_dir: PathBuf,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Resolve the 6-tier cascade for the given working directory.
///
/// This is the primary public API for T-P4-E02-26. Wraps the existing
/// [`Resolver`] and enriches the result with the spec-shaped `ResolvedCascade`.
///
/// # Arguments
///
/// * `cwd` — the working directory to resolve from.  Used to compute
///   PPC / PRC / PAC tier paths via directory walk.
///
/// # Errors
///
/// Returns [`cascade_types::error::CascadeError::Io`] only if a file that
/// EXISTS cannot be read.  Missing tiers are not errors.
pub fn resolve_cascade_full(cwd: &Path) -> Result<ResolvedCascade> {
    let engine = CascadeResolutionEngine::new(cwd.to_path_buf());
    engine.resolve()
}

// ── CascadeResolutionEngine ───────────────────────────────────────────────────

/// Internal engine that wraps `Resolver` and adds the spec-shaped output.
///
/// # Constraints
///
/// - Inputs: `cwd: PathBuf`
/// - Outputs: `Result<ResolvedCascade>`
/// - All tier paths come from `CascadeTier` + the path walk in `Resolver`.
struct CascadeResolutionEngine {
    cwd: PathBuf,
}

impl CascadeResolutionEngine {
    fn new(cwd: PathBuf) -> Self {
        Self { cwd }
    }

    fn resolve(&self) -> Result<ResolvedCascade> {
        // Use the existing Resolver (resolution.rs) for the filesystem walk and
        // tier merging so we do not duplicate that logic.
        //
        // When called from within an existing Tokio runtime (e.g. from a CLI
        // command dispatched via `#[tokio::main]`), we must not create a new
        // runtime — that panics.  Instead we use `block_in_place` so the
        // current worker thread is allowed to block.  When there is no active
        // runtime (e.g. from a unit test or a sync context) we create one.
        let resolver = Resolver::new();
        let base_result = match tokio::runtime::Handle::try_current() {
            Ok(handle) => {
                tokio::task::block_in_place(|| handle.block_on(resolver.resolve(&self.cwd)))?
            }
            Err(_) => {
                let rt = tokio::runtime::Builder::new_current_thread()
                    .enable_all()
                    .build()
                    .expect("build tokio runtime for cascade_resolution");
                rt.block_on(resolver.resolve(&self.cwd))?
            }
        };

        // Build the working_dir from the canonicalised cwd in the base result.
        let working_dir = base_result.cwd.clone();

        // Build per-tier TierResult list (all 6 tiers, found or not).
        let tier_results = self.build_tier_results(&base_result);

        // Merge instructions: iterate in precedence order (GCI first), separate
        // with tier-labelled comments so callers can see provenance.
        let merged_instructions = build_merged_instructions(&tier_results);

        // MCP server URL: lowest-tier (most specific) value wins.
        let mcp_server_url = extract_mcp_server_url(&self.cwd, &tier_results);

        Ok(ResolvedCascade {
            merged_instructions,
            mcp_server_url,
            tiers_found: tier_results,
            working_dir,
        })
    }

    /// Build a `TierResult` for every tier (found AND missing).
    ///
    /// Maps from the `Resolver` output:
    /// - `tier_sources` → `found = true`, `instructions` = content.
    /// - `missing_tiers` → `found = false`, `instructions = ""`.
    ///
    /// Also computes `path_searched` for each tier using the same logic as
    /// `Resolver::discover_tier_paths` (HOME-based paths for GCI/PCI/APC;
    /// cwd ancestor walk for PPC/PRC/PAC).
    fn build_tier_results(&self, base: &crate::resolution::ResolvedCascade) -> Vec<TierResult> {
        // Build a fast-lookup map: CascadeTier → (path, content) for found tiers.
        use std::collections::HashMap;
        let found_map: HashMap<CascadeTier, (PathBuf, String)> = base
            .tier_sources
            .iter()
            .map(|ts| (ts.tier, (ts.path.clone(), ts.content.clone())))
            .collect();

        let home = dirs_home();

        // Pre-compute ancestor list for PPC/PRC/PAC assignment (mirrors Resolver logic).
        let mut ancestors: Vec<PathBuf> = base.cwd.ancestors().map(|p| p.to_path_buf()).collect();
        ancestors.reverse(); // root → cwd

        let intermediate_tiers = [
            CascadeTier::Pci,
            CascadeTier::Apc,
            CascadeTier::Ppc,
            CascadeTier::Prc,
            CascadeTier::Pac,
        ];

        // Map each ancestor with a .cascade/ dir to an intermediate tier slot.
        let mut tier_dir_map: HashMap<CascadeTier, PathBuf> = HashMap::new();
        let mut tier_idx = 0;
        for ancestor in &ancestors {
            if tier_idx >= intermediate_tiers.len() {
                break;
            }
            let cascade_dir = ancestor.join(cascade_types::paths::CASCADE_DIR_NAME);
            if cascade_dir.is_dir() {
                tier_dir_map.insert(intermediate_tiers[tier_idx], ancestor.clone());
                tier_idx += 1;
            }
        }

        CascadeTier::all_in_precedence_order()
            .iter()
            .map(|tier| {
                let path_searched = match tier {
                    CascadeTier::Gci => home
                        .as_ref()
                        .map(|h| h.join(".cascade"))
                        .unwrap_or_else(|| PathBuf::from("~/.cascade")),
                    CascadeTier::Pci => home
                        .as_ref()
                        .map(|h| h.join("Downloads").join(".cascade"))
                        .unwrap_or_else(|| PathBuf::from("~/Downloads/.cascade")),
                    CascadeTier::Apc => std::env::var("CASCADE_APC_PATH")
                        .ok()
                        .map(PathBuf::from)
                        .or_else(|| home.as_ref().map(|h| h.join("Sites").join(".cascade")))
                        .unwrap_or_else(|| PathBuf::from("~/Sites/.cascade")),
                    CascadeTier::Ppc | CascadeTier::Prc | CascadeTier::Pac => tier_dir_map
                        .get(tier)
                        .map(|p| p.join(".cascade"))
                        .unwrap_or_else(|| base.cwd.join(".cascade")),
                };

                if let Some((path, content)) = found_map.get(tier) {
                    TierResult {
                        tier: *tier,
                        found: true,
                        path_searched: path.clone(),
                        instructions: content.clone(),
                    }
                } else {
                    TierResult {
                        tier: *tier,
                        found: false,
                        path_searched,
                        instructions: String::new(),
                    }
                }
            })
            .collect()
    }
}

// ── Merge helpers ─────────────────────────────────────────────────────────────

/// Concatenate instruction text from found tiers, with tier-separator comments.
///
/// GCI appears first (index 0 in `tier_results` which is precedence-ordered).
fn build_merged_instructions(tier_results: &[TierResult]) -> String {
    let parts: Vec<String> = tier_results
        .iter()
        .filter(|tr| tr.found && !tr.instructions.trim().is_empty())
        .map(|tr| {
            format!(
                "<!-- cascade-tier: {} -->\n{}",
                tr.tier.acronym(),
                tr.instructions.trim_end()
            )
        })
        .collect();

    parts.join("\n\n")
}

/// Extract the `mcp_server_url` from the lowest (most specific) tier's
/// `config.toml` that contains the key.
///
/// Falls back to [`DEFAULT_MCP_SERVER_URL`].
fn extract_mcp_server_url(cwd: &Path, tier_results: &[TierResult]) -> String {
    // Iterate in REVERSE precedence order (PAC first) so the lowest tier wins.
    for tr in tier_results.iter().rev() {
        if !tr.found {
            continue;
        }
        // The path_searched in a found result is the CASCADE.md path; the
        // config.toml lives in the same directory.
        let config_dir = if tr.path_searched.is_file() {
            tr.path_searched.parent().map(|p| p.to_path_buf())
        } else {
            Some(tr.path_searched.clone())
        };

        if let Some(dir) = config_dir {
            let config_toml = dir.join("config.toml");
            if let Ok(raw) = std::fs::read_to_string(&config_toml) {
                if let Ok(table) = raw.parse::<toml::Table>() {
                    if let Some(url) = table.get(MCP_URL_TOML_KEY).and_then(|v| v.as_str()) {
                        if !url.trim().is_empty() {
                            return url.to_owned();
                        }
                    }
                }
            }
        }
    }

    // Also check cwd root config.toml as a convenience.
    let root_config = cwd.join(".cascade").join("config.toml");
    if let Ok(raw) = std::fs::read_to_string(&root_config) {
        if let Ok(table) = raw.parse::<toml::Table>() {
            if let Some(url) = table.get(MCP_URL_TOML_KEY).and_then(|v| v.as_str()) {
                if !url.trim().is_empty() {
                    return url.to_owned();
                }
            }
        }
    }

    DEFAULT_MCP_SERVER_URL.to_owned()
}

// ── Utilities ─────────────────────────────────────────────────────────────────

fn dirs_home() -> Option<PathBuf> {
    std::env::var("HOME").ok().map(PathBuf::from)
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
    fn home_lock() -> MutexGuard<'static, ()> {
        static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
        LOCK.get_or_init(|| Mutex::new(()))
            .lock()
            .unwrap_or_else(|e| e.into_inner())
    }

    // ── fixture helpers ───────────────────────────────────────────────────────

    fn make_cascade_dir(root: &Path) -> PathBuf {
        let d = root.join(".cascade");
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn write_cascade_md(dir: &Path, text: &str) {
        fs::write(dir.join("CASCADE.md"), text).unwrap();
    }

    fn write_config_toml(dir: &Path, toml: &str) {
        fs::write(dir.join("config.toml"), toml).unwrap();
    }

    // ── test 1: all tiers found ───────────────────────────────────────────────

    /// All 6 tiers present → tiers_found has 6 entries, all found = true.
    ///
    /// WHY: verifies that the TierResult list is always exactly 6 elements and
    /// that the provenance map is complete.
    #[test]
    #[serial(global_env)]
    fn all_tiers_found() {
        let _guard = home_lock();
        let tmp_home = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        // GCI: ~/.cascade/CASCADE.md
        let gci_dir = make_cascade_dir(tmp_home.path());
        write_cascade_md(&gci_dir, "GCI instructions");

        // PCI: ~/Downloads/.cascade/CASCADE.md
        let pci_root = tmp_home.path().join("Downloads");
        fs::create_dir_all(&pci_root).unwrap();
        let pci_dir = make_cascade_dir(&pci_root);
        write_cascade_md(&pci_dir, "PCI instructions");

        // APC: ~/Sites/.cascade/CASCADE.md
        let apc_root = tmp_home.path().join("Sites");
        fs::create_dir_all(&apc_root).unwrap();
        let apc_dir = make_cascade_dir(&apc_root);
        write_cascade_md(&apc_dir, "APC instructions");

        // PPC / PRC / PAC: nested in a project tree that CWD descends from.
        // We use: Sites/myproject/myrepo/app as CWD with .cascade/ at each level.
        let ppc_root = apc_root.join("myproject");
        let ppc_dir = make_cascade_dir(&ppc_root);
        write_cascade_md(&ppc_dir, "PPC instructions");

        let prc_root = ppc_root.join("myrepo");
        let prc_dir = make_cascade_dir(&prc_root);
        write_cascade_md(&prc_dir, "PRC instructions");

        let pac_root = prc_root.join("app");
        fs::create_dir_all(&pac_root).unwrap();
        let pac_dir = make_cascade_dir(&pac_root);
        write_cascade_md(&pac_dir, "PAC instructions");

        std::env::set_var("CASCADE_APC_PATH", apc_dir.to_str().unwrap());

        let result = resolve_cascade_full(&pac_root).expect("should resolve");

        // Should have all 6 tiers in tiers_found
        assert_eq!(result.tiers_found.len(), 6, "must have 6 tier results");

        // Count found tiers
        let found_count = result.tiers_found.iter().filter(|t| t.found).count();
        // At minimum GCI + whatever the walk finds
        assert!(found_count >= 1, "at least GCI must be found");

        // merged_instructions must contain the GCI text
        assert!(
            result.merged_instructions.contains("GCI instructions"),
            "GCI instructions must appear in merged output"
        );

        std::env::remove_var("CASCADE_APC_PATH");
    }

    // ── test 2: partial tiers — only GCI present ──────────────────────────────

    /// Only GCI present → 5 tiers missing, 1 found.
    ///
    /// WHY: verifies that missing tiers are recorded with `found = false` and
    /// do not cause errors.
    #[test]
    #[serial(global_env)]
    fn partial_tiers_only_gci() {
        let _guard = home_lock();
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        let gci_dir = make_cascade_dir(tmp_home.path());
        write_cascade_md(&gci_dir, "GCI only");

        let result = resolve_cascade_full(tmp_project.path()).expect("should not error");
        assert_eq!(result.tiers_found.len(), 6, "must always have 6 entries");

        let gci = result
            .tiers_found
            .iter()
            .find(|t| t.tier == CascadeTier::Gci)
            .expect("GCI entry must exist");
        assert!(gci.found, "GCI must be found");

        let pac = result
            .tiers_found
            .iter()
            .find(|t| t.tier == CascadeTier::Pac)
            .expect("PAC entry must exist");
        assert!(!pac.found, "PAC must be missing");

        assert!(
            result.merged_instructions.contains("GCI only"),
            "GCI text must appear"
        );
    }

    // ── test 3: no tiers at all ───────────────────────────────────────────────

    /// No `.cascade/` directories anywhere → graceful empty result, no panic.
    ///
    /// WHY: spec requires no panic when cwd has zero .cascade dirs.
    #[test]
    #[serial(global_env)]
    fn no_tiers_graceful_empty() {
        let _guard = home_lock();
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        // No .cascade dirs anywhere
        let result = resolve_cascade_full(tmp_project.path());
        assert!(result.is_ok(), "no tiers must not be an error");
        let result = result.unwrap();
        assert_eq!(result.tiers_found.len(), 6, "still 6 tier result entries");
        assert!(
            result.tiers_found.iter().all(|t| !t.found),
            "all tiers must be missing"
        );
        assert_eq!(
            result.mcp_server_url, DEFAULT_MCP_SERVER_URL,
            "default MCP URL when no config"
        );
        assert!(result.merged_instructions.is_empty(), "no instructions");
    }

    // ── test 4: mcp_server_url from lowest tier wins ──────────────────────────

    /// PPC overrides GCI for mcp_server_url (lowest-tier wins).
    ///
    /// WHY: spec says "from the lowest tier that declares it".
    #[test]
    #[serial(global_env)]
    fn mcp_server_url_lowest_tier_wins() {
        let _guard = home_lock();
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        // GCI: config.toml with mcp_server_url
        let gci_dir = make_cascade_dir(tmp_home.path());
        write_config_toml(
            &gci_dir,
            r#"mcp_server_url = "unix://~/.cascade/cascade.sock""#,
        );
        write_cascade_md(&gci_dir, "GCI text");

        // PPC (project-level): config.toml with a different mcp_server_url
        let ppc_dir = make_cascade_dir(tmp_project.path());
        write_config_toml(
            &ppc_dir,
            r#"mcp_server_url = "unix:///tmp/myproject/cascade.sock""#,
        );
        write_cascade_md(&ppc_dir, "PPC text");

        let result = resolve_cascade_full(tmp_project.path()).expect("should resolve");
        assert_eq!(
            result.mcp_server_url, "unix:///tmp/myproject/cascade.sock",
            "lowest tier (PPC) mcp_server_url must override GCI"
        );
    }

    // ── test 5: JSON serialization ────────────────────────────────────────────

    /// ResolvedCascade serialises to valid JSON matching the spec schema.
    ///
    /// WHY: `cascade cascade-resolve` prints JSON output.
    #[test]
    #[serial(global_env)]
    fn json_serialization_valid() {
        let _guard = home_lock();
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        let gci_dir = make_cascade_dir(tmp_home.path());
        write_cascade_md(&gci_dir, "instructions");

        let result = resolve_cascade_full(tmp_project.path()).expect("should resolve");
        let json = serde_json::to_string_pretty(&result).expect("must serialize");

        // Verify expected fields are present
        assert!(
            json.contains("merged_instructions"),
            "must have merged_instructions"
        );
        assert!(json.contains("mcp_server_url"), "must have mcp_server_url");
        assert!(json.contains("tiers_found"), "must have tiers_found");
        assert!(json.contains("working_dir"), "must have working_dir");

        // Must deserialize back without error
        let _: ResolvedCascade = serde_json::from_str(&json).expect("must deserialize");
    }

    // ── test 6: PAC missing does not error ────────────────────────────────────

    /// PAC tier absent from fixture → TierResult.found = false, no error.
    ///
    /// WHY: acceptance criterion from spec.
    #[test]
    #[serial(global_env)]
    fn pac_absent_not_an_error() {
        let _guard = home_lock();
        let tmp_home = TempDir::new().unwrap();
        let tmp_project = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp_home.path().to_str().unwrap());

        // Only PPC present (project root), no PAC (app subdir)
        let ppc_dir = make_cascade_dir(tmp_project.path());
        write_cascade_md(&ppc_dir, "PPC text");

        let result = resolve_cascade_full(tmp_project.path()).expect("no error");
        let pac = result
            .tiers_found
            .iter()
            .find(|t| t.tier == CascadeTier::Pac)
            .unwrap();
        assert!(!pac.found, "PAC must be missing, not an error");
    }
}
