//! Shared helpers for the `cascade template` subcommands.
//!
//! Purpose: Registry loading, target resolution, stamp parsing, variable
//! substitution, tier parsing, and result printing.
//! Inputs: CLI args slices, file paths, registry references.
//! Outputs: Loaded registries, parsed types, formatted terminal output.
//! Constraints: No public API changes — all items re-exported from parent mod.

use cascade_core::templates::registry::TemplateRegistry;
use cascade_types::{
    error::{CascadeError, Result},
    TemplateTier,
};
use std::path::{Path, PathBuf};

// ── Stamp parsing (CLI-layer) ─────────────────────────────────────────────────
// Mirrors the stamp format written by cascade-core::templates::apply.
// Pattern: <!-- cascade:applied { id="...", version="...", applied_at="..." } -->

pub(super) const CLI_STAMP_PREFIX: &str = "<!-- cascade:applied";

/// Extract `(id, version)` pairs from all stamp comments in `content`.
pub(super) fn read_stamps_from_content(content: &str) -> Vec<(String, String)> {
    let mut stamps = Vec::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with(CLI_STAMP_PREFIX) {
            continue;
        }
        if let (Some(id), Some(ver)) = (
            cli_extract_attr(trimmed, "id"),
            cli_extract_attr(trimmed, "version"),
        ) {
            stamps.push((id, ver));
        }
    }
    stamps
}

/// Extract `key="value"` from a stamp comment line.
pub(super) fn cli_extract_attr(line: &str, key: &str) -> Option<String> {
    let needle = format!("{}=\"", key);
    let start = line.find(&needle)? + needle.len();
    let rest = &line[start..];
    let end = rest.find('"')?;
    Some(rest[..end].to_string())
}

// ── Registry + path resolution ────────────────────────────────────────────────

/// Load the template registry from bundled + user template directories.
///
/// Priority: bundled (lower) < `~/.cascade/templates/` (higher).
/// Missing directories are silently skipped by the registry.
pub(super) fn load_registry() -> Result<TemplateRegistry> {
    let bundled = bundled_templates_dir();
    let user = user_templates_dir();

    let mut dirs: Vec<&Path> = Vec::new();
    if let Some(ref b) = bundled {
        dirs.push(b.as_path());
    }
    if let Some(ref u) = user {
        dirs.push(u.as_path());
    }

    Ok(TemplateRegistry::load_dirs(&dirs))
}

/// Return the bundled `data/templates/` directory, or `None` if undiscoverable.
pub(super) fn bundled_templates_dir() -> Option<PathBuf> {
    if let Ok(v) = std::env::var("CASCADE_BUNDLED_TEMPLATES") {
        return Some(PathBuf::from(v));
    }
    if let Ok(exe) = std::env::current_exe() {
        if let Some(parent) = exe.parent() {
            let candidate = parent.join("../share/cascade/templates");
            if candidate.exists() {
                return Some(candidate);
            }
        }
    }
    None
}

/// Return `~/.cascade/templates/`, or `None` if HOME is unset.
pub(super) fn user_templates_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(|h| PathBuf::from(h).join(".cascade").join("templates"))
}

/// Resolve the target path:
/// 1. If `--target` was given, use it.
/// 2. Otherwise, walk up from CWD looking for `.cascade/CASCADE.md`.
/// 3. If not found, fall back to `<cwd>/.cascade/CASCADE.md`.
pub(super) fn resolve_target(explicit: Option<&Path>) -> Result<PathBuf> {
    if let Some(p) = explicit {
        return Ok(p.to_path_buf());
    }
    let cwd = std::env::current_dir()
        .map_err(|e| CascadeError::Other(format!("cannot determine CWD: {e}")))?;
    for ancestor in cwd.ancestors() {
        let candidate = ancestor.join(".cascade").join("CASCADE.md");
        if candidate.exists() {
            return Ok(candidate);
        }
    }
    Ok(cwd.join(".cascade").join("CASCADE.md"))
}

// ── Parsing helpers ───────────────────────────────────────────────────────────

/// Parse `--tier` string into [`TemplateTier`].
pub(super) fn parse_tier(s: &str) -> Result<TemplateTier> {
    match s.to_lowercase().as_str() {
        "gci" => Ok(TemplateTier::Gci),
        "pci" => Ok(TemplateTier::Pci),
        "apc" => Ok(TemplateTier::Apc),
        "ppc" => Ok(TemplateTier::Ppc),
        "prc" => Ok(TemplateTier::Prc),
        "pac" => Ok(TemplateTier::Pac),
        "any" => Ok(TemplateTier::Any),
        other => Err(CascadeError::Other(format!(
            "unknown tier '{other}' — valid values: gci, pci, apc, ppc, prc, pac, any"
        ))),
    }
}

/// Parse `["key=value", ...]` into a `HashMap<String, String>`.
pub(super) fn parse_vars(vars: &[String]) -> Result<std::collections::HashMap<String, String>> {
    let mut map = std::collections::HashMap::new();
    for entry in vars {
        let (k, v) = entry.split_once('=').ok_or_else(|| {
            CascadeError::Other(format!(
                "invalid --var '{entry}': expected key=value format"
            ))
        })?;
        map.insert(k.to_string(), v.to_string());
    }
    Ok(map)
}

/// Substitute `{{key}}` placeholders in `body` using `vars`.
///
/// Returns `Err` if any `{{key}}` placeholder has no corresponding entry in
/// `vars` and no inline default (supports `{{key:default}}` syntax).
pub(super) fn substitute_vars(
    body: &str,
    vars: &std::collections::HashMap<String, String>,
) -> Result<String> {
    let mut result = body.to_string();
    let mut output = String::with_capacity(result.len());
    let bytes = result.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if i + 1 < bytes.len() && bytes[i] == b'{' && bytes[i + 1] == b'{' {
            if let Some(close) = result[i + 2..].find("}}") {
                let key = &result[i + 2..i + 2 + close];
                let key = key.trim();
                if let Some(val) = vars.get(key) {
                    output.push_str(val);
                } else if key.contains(':') {
                    let (var_name, default) = key.split_once(':').unwrap();
                    let var_name = var_name.trim();
                    if let Some(val) = vars.get(var_name) {
                        output.push_str(val);
                    } else {
                        output.push_str(default.trim());
                    }
                } else {
                    return Err(CascadeError::Other(format!(
                        "missing variable '{{{{{}}}}}' — provide it with --var {key}=<value>",
                        key
                    )));
                }
                i += 2 + close + 2;
                continue;
            }
        }
        output.push(bytes[i] as char);
        i += 1;
    }
    result = output;
    Ok(result)
}

/// Build the "not found" error message with a helpful hint.
pub(super) fn not_found_error(id: &str) -> CascadeError {
    CascadeError::Other(format!(
        "Template '{id}' not found. Run `cascade template list` to see available templates."
    ))
}

// ── Result printers ───────────────────────────────────────────────────────────

/// Print a human-readable summary of an [`cascade_types::UpgradeResult`].
pub(super) fn print_upgrade_result(result: &cascade_types::UpgradeResult, dry_run: bool) {
    let prefix = if dry_run { "[dry-run] " } else { "" };
    if !result.upgraded.is_empty() {
        println!("{prefix}Updated sections ({}):", result.upgraded.len());
        for s in &result.upgraded {
            println!("  ~ {s}");
        }
    }
    if !result.added.is_empty() {
        println!("{prefix}Newly added sections ({}):", result.added.len());
        for s in &result.added {
            println!("  + {s}");
        }
    }
    if !result.deprecated.is_empty() {
        println!(
            "{prefix}Deprecated sections ({}) — preserved with deprecation notice:",
            result.deprecated.len()
        );
        for s in &result.deprecated {
            println!("  ! {s}");
        }
    }
    if result.upgraded.is_empty() && result.added.is_empty() && result.deprecated.is_empty() {
        println!("{prefix}Already up to date — no changes needed.");
    }
}

/// Print a human-readable summary of an [`cascade_types::ApplyResult`].
pub(super) fn print_apply_result(result: &cascade_types::ApplyResult, dry_run: bool) {
    let prefix = if dry_run { "[dry-run] " } else { "" };
    if !result.applied.is_empty() {
        println!("{prefix}Applied sections ({}):", result.applied.len());
        for s in &result.applied {
            println!("  + {s}");
        }
    }
    if !result.conflicts.is_empty() {
        println!(
            "{prefix}Conflicts ({}): existing content preserved:",
            result.conflicts.len()
        );
        for s in &result.conflicts {
            println!("  ! {s}");
        }
        if !dry_run {
            eprintln!("Tip: use --force to overwrite conflicting sections.");
        }
    }
    if !result.skipped.is_empty() {
        println!(
            "{prefix}Already present ({}): no change needed:",
            result.skipped.len()
        );
        for s in &result.skipped {
            println!("  = {s}");
        }
    }
    if result.applied.is_empty() && result.conflicts.is_empty() && result.skipped.is_empty() {
        println!("{prefix}Nothing to change (template already fully applied).");
    }
}
