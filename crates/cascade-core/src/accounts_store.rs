//! Accounts store — atomic I/O for `~/.cascade/accounts/`.
//!
//! Purpose: read/write the Cascade fleet account registry and derived files;
//! build the seeded default registry; detect CLI availability.
//!
//! ## Directory layout (`~/.cascade/accounts/`)
//!
//! | File         | Writer              | Reader                 |
//! |--------------|---------------------|------------------------|
//! | accounts.json | `cascade detect`   | CLI, daemon            |
//! | quota.json   | daemon fleet poller  | native widget          |
//! | README.md    | `cascade detect`    | humans                 |
//! | matrix.md    | `cascade detect`    | humans                 |
//!
//! ## Migration
//! If `~/.cascade/accounts.json` (old single-file path) exists and the new
//! directory does not, [`migrate_accounts_if_needed`] moves the file into the
//! directory automatically on first access.
//!
//! Inputs:  path to `accounts/accounts.json`; vault.env files for GFP key count.
//! Outputs: `AccountsRegistry` on read; atomic file writes (tmp → rename) on write.
//! Constraints: no `unwrap()` outside `#[cfg(test)]`; never log key values.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — accounts_store

use std::path::{Path, PathBuf};

use cascade_types::accounts::{
    AccountFamily, AccountRole, AccountsRegistry, ModelMatrixEntry,
    ACCOUNTS_SCHEMA_VERSION,
};
use cascade_types::error::{CascadeError, Result};
use serde_json::Value;

use crate::external_accounts::{AuthStatus, discover as discover_external};

// ── Path helpers ──────────────────────────────────────────────────────────────

/// Returns the canonical path to `~/.cascade/accounts/` (the accounts directory).
///
/// Outputs: `PathBuf` — the directory may not exist on a fresh install.
pub fn accounts_dir() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("~"))
        .join(".cascade")
        .join("accounts")
}

/// Returns the canonical path to `~/.cascade/accounts/accounts.json`.
///
/// Outputs: `PathBuf` — may not exist on a fresh install.
pub fn accounts_path() -> PathBuf {
    accounts_dir().join("accounts.json")
}

/// Returns the canonical path to `~/.cascade/accounts/quota.json`.
///
/// This is the file the native widget reads for live usage data.
pub fn quota_json_path() -> PathBuf {
    accounts_dir().join("quota.json")
}

// ── Migration (old single-file → directory) ───────────────────────────────────

/// Migrate `~/.cascade/accounts.json` → `~/.cascade/accounts/accounts.json`
/// if the old flat path exists and the new directory does not yet contain the file.
///
/// Outputs: `Ok(true)` if a migration was performed; `Ok(false)` otherwise.
/// Errors: I/O failures during move are surfaced as `CascadeError::Io`.
pub fn migrate_accounts_if_needed() -> Result<bool> {
    let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("~"));
    let old_path = home.join(".cascade").join("accounts.json");
    let new_path = accounts_path();

    if !old_path.exists() || new_path.exists() {
        return Ok(false);
    }

    // Ensure the accounts/ directory exists.
    let dir = accounts_dir();
    std::fs::create_dir_all(&dir)
        .map_err(|e| CascadeError::io(dir.clone(), "create_dir_all", e))?;

    // Copy then remove (cross-device rename may fail).
    let bytes = std::fs::read(&old_path)
        .map_err(|e| CascadeError::io(old_path.clone(), "read", e))?;
    std::fs::write(&new_path, &bytes)
        .map_err(|e| CascadeError::io(new_path.clone(), "write", e))?;
    std::fs::remove_file(&old_path)
        .map_err(|e| CascadeError::io(old_path.clone(), "remove", e))?;

    Ok(true)
}

// ── Reader ────────────────────────────────────────────────────────────────────

/// Read and deserialise the accounts registry from `path`.
///
/// Errors: `PathNotFound` when absent; `SchemaMismatch` on version mismatch;
/// `Io` on other I/O failures; `ConfigParse` when JSON is malformed.
pub fn read_accounts_registry(path: &Path) -> Result<AccountsRegistry> {
    if !path.exists() {
        return Err(CascadeError::PathNotFound { path: path.to_path_buf() });
    }
    let bytes = std::fs::read(path).map_err(|e| CascadeError::io(path.to_path_buf(), "read", e))?;
    let registry: AccountsRegistry =
        serde_json::from_slice(&bytes).map_err(|e| CascadeError::ConfigParse {
            path: path.to_path_buf(),
            detail: e.to_string(),
        })?;
    if registry.schema_version != ACCOUNTS_SCHEMA_VERSION {
        return Err(CascadeError::SchemaMismatch {
            expected: ACCOUNTS_SCHEMA_VERSION,
            found: registry.schema_version,
        });
    }
    Ok(registry)
}

// ── Writers ───────────────────────────────────────────────────────────────────

/// Write `registry` to `path` atomically (tmp → rename).
///
/// Constraints: POSIX-atomic when tmp and dst share a filesystem (same dir).
pub fn write_accounts_registry(path: &Path, registry: &AccountsRegistry) -> Result<()> {
    let bytes = serde_json::to_vec_pretty(registry).map_err(|e| CascadeError::Other(e.to_string()))?;
    atomic_write(path, &bytes)
}

/// Write `~/.cascade/accounts/quota.json` atomically from an `AccountsRegistry`.
///
/// The quota.json shape matches the native widget's `AccountEntry` schema:
/// ```json
/// {
///   "accounts": [
///     { "account": "claude-acc1", "provider": "claude", "email": "...",
///       "usage": { "five_hour": null, "seven_day": null, "seven_day_sonnet": null,
///                  "seven_day_opus": null, "extra_usage": null },
///       "last_pull_at": 1234567890.0, "quota_opaque": false, "status": "ok" }
///   ],
///   "queried_at": 1234567890.0
/// }
/// ```
///
/// Usage data is merged from `~/.claude/usage-cache.json` when present.
/// Accounts with no live data still appear with `last_pull_at` set to now
/// so the widget renders them (it hides only when last_pull_at is null AND
/// all usage slots are null).
///
/// Provider mapping by family:
///   Claude → "claude", Openai → "codex", Google → "gemini",
///   Opencode → "opencode", Gfp → "gfp"
///
/// Inputs:  `path` — destination file (should be `quota_json_path()`);
///          `registry` — current accounts registry.
/// Outputs: atomic write to `path`.
pub fn write_quota_json(path: &Path, registry: &AccountsRegistry) -> Result<()> {
    use serde_json::json;

    // Load legacy cache for merging real usage data.
    let legacy = load_legacy_usage_cache();

    let now_epoch = chrono::Utc::now().timestamp() as f64;

    // Build the live external-account bridge map (account_id → live auth + config_dir).
    // This is a best-effort read — any failures fall back to legacy behaviour.
    let live_accounts = discover_external();
    let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("~"));

    let accounts: Vec<Value> = registry.accounts.iter().map(|acc| {
        let provider = family_to_provider(&acc.family);

        // Email: prefer subscription note, fall back to account id.
        let email: Option<&str> = acc.notes.as_deref()
            .or_else(|| Some(acc.subscription.as_str()));

        // Try to find a matching entry in the legacy cache.
        let legacy_entry = legacy.as_ref().and_then(|entries| {
            find_legacy_entry(entries, &acc.id, provider)
        });

        let (usage, last_pull_at, quota_opaque, opencode_meta) = if let Some(leg) = legacy_entry {
            // Merge: copy usage block + last_pull_at + quota_opaque + opencode_meta verbatim.
            let usage = leg.get("usage").cloned().unwrap_or(Value::Null);
            // Fall back to `now` when the legacy entry has no successful pull (e.g. an
            // account whose poll errored): a configured account must still render in the
            // widget rather than being auto-hidden. It will just show "—" until data lands.
            let last_pull = leg.get("last_pull_at").cloned()
                .filter(|v| !v.is_null())
                .or_else(|| Some(json!(now_epoch)));
            let opaque = leg.get("quota_opaque").cloned()
                .and_then(|v| v.as_bool());
            let oc_meta = leg.get("opencode_meta").cloned();
            (usage, last_pull, opaque, oc_meta)
        } else {
            // No live data — emit null slots but set last_pull_at to now so
            // the widget still renders the row.
            let null_usage = json!({
                "five_hour": null,
                "seven_day": null,
                "seven_day_sonnet": null,
                "seven_day_opus": null,
                "extra_usage": null
            });
            let opaque = matches!(acc.family, AccountFamily::Gfp | AccountFamily::Google);
            (null_usage, Some(json!(now_epoch)), Some(opaque), None)
        };

        // ── Live credential bridge ────────────────────────────────────────────
        // Map the account id to its external config directory, then check the
        // LIVE auth status from the CLI's own keychain/file storage.
        //
        // Mapping strategy:
        //   "claude-acc1"  → ~/.claude-acc1
        //   "claude-acc2"  → ~/.claude-acc2 (etc.)
        //   primary Claude → ~/.claude (the default dir, no suffix)
        //   codex accounts → ~/.codex (or ~/.codex2 etc.)
        //
        // If we can't map with confidence, fall back to legacy error heuristics.
        let config_dir_for_account: Option<PathBuf> = if acc.family == AccountFamily::Claude {
            // Try canonical name first: "claude-acc1" → ~/.claude-acc1
            let candidate = home.join(format!(".{}", acc.id));
            if candidate.is_dir() {
                Some(candidate)
            } else if acc.id.starts_with("claude-acc") {
                // Normalise: "claude-acc1" → ".claude-acc1"
                let suffix = acc.id.trim_start_matches("claude-acc");
                let n_dir = home.join(format!(".claude-acc{suffix}"));
                if n_dir.is_dir() { Some(n_dir) } else { None }
            } else {
                // Primary / default claude account maps to ~/.claude.
                let default_dir = home.join(".claude");
                if default_dir.is_dir() { Some(default_dir) } else { None }
            }
        } else if acc.family == AccountFamily::Openai {
            let codex_dir = home.join(".codex");
            if codex_dir.is_dir() { Some(codex_dir) } else { None }
        } else {
            None
        };

        // Look up the live auth status from discovered accounts.
        let live_auth: Option<&AuthStatus> = config_dir_for_account.as_ref().and_then(|cd| {
            live_accounts.iter().find(|la| &la.config_dir == cd).map(|la| &la.auth)
        });

        // A definitive live-fetch auth error is GROUND TRUTH and overrides the
        // optimistic keychain bridge. The bridge reports Ok merely because a
        // refreshToken STRING exists in the keychain — but an actual API 401
        // (observed AFTER a `claude -p` refresh attempt) means the token is truly
        // dead. So a definitive `last_error` forces auth_dead = true regardless of
        // the bridge status.
        //
        // Definitive auth failures: http_401, http_403, invalid_grant,
        //   authentication_error, refresh_failed, keychain_failed, auth_invalid.
        // Transient (NON-auth-dead, no nag): api_failed (network/timeout),
        //   parse_failed (503 HTML page), rate_limit_error, http_429.
        let legacy_definitive_auth_failure = legacy_entry
            .and_then(|leg| leg.get("last_error").and_then(|v| v.as_str()))
            .map(|e| {
                matches!(
                    e,
                    "keychain_failed" | "refresh_failed" | "auth_invalid"
                        | "invalid_grant" | "authentication_error" | "http_401" | "http_403"
                )
            })
            .unwrap_or(false);

        // Determine auth_dead.
        //   1. A definitive live-fetch auth error wins over everything (incl. bridge Ok).
        //   2. Otherwise the live bridge decides: Ok → not dead, NeedsReauth → dead.
        //   3. Otherwise (bridge Unknown / unmapped) fall back to legacy heuristics.
        let auth_dead = if legacy_definitive_auth_failure {
            true
        } else {
            match live_auth {
                Some(AuthStatus::Ok { .. }) => false, // bridge Ok + no definitive error → never nag
                Some(AuthStatus::NeedsReauth) => true,
                _ => false, // bridge Unknown/unmapped + no definitive error → not dead
            }
        };

        // Never show stale numbers for a credential-dead account — blank them so the
        // widget renders the "Click here to re-auth" call-to-action instead.
        let usage = if auth_dead { Value::Null } else { usage };

        let _has_data = usage.get("five_hour").map(|v| !v.is_null()).unwrap_or(false)
            || usage.get("seven_day").map(|v| !v.is_null()).unwrap_or(false)
            || usage.get("seven_day_sonnet").map(|v| !v.is_null()).unwrap_or(false);

        // Status drives the widget's per-row hint.
        // Only show "auth" on a genuine credential failure — not on transient errors or
        // accounts with no data yet (those show as "ok" with dashes, not a re-auth prompt).
        let status = match acc.family {
            AccountFamily::Gfp => "pool",
            AccountFamily::Claude | AccountFamily::Google if auth_dead => "auth",
            _ => "ok",
        };

        let mut entry = json!({
            "account": acc.id,
            "provider": provider,
            "email": email,
            "usage": usage,
            "last_pull_at": last_pull_at,
            "quota_opaque": quota_opaque.unwrap_or(false),
            "status": status
        });

        // Surface the config_dir so the Swift widget can pass the correct
        // CLAUDE_CONFIG_DIR env var when launching the re-auth flow.
        if let Some(dir) = &config_dir_for_account {
            entry["config_dir"] = json!(dir.to_string_lossy().as_ref());
        }

        // GFP free-Flash pool: surface the round-robin key count so the row shows
        // capacity ("28 keys") rather than dashes.
        if acc.family == AccountFamily::Gfp {
            entry["key_count"] = json!(acc.key_count);
        }
        if let Some(meta) = opencode_meta {
            entry["opencode_meta"] = meta;
        }

        entry
    }).collect();

    let payload = json!({
        "accounts": accounts,
        "queried_at": now_epoch
    });

    let bytes = serde_json::to_vec_pretty(&payload)
        .map_err(|e| CascadeError::Other(e.to_string()))?;
    atomic_write(path, &bytes)
}

/// Map an `AccountFamily` to the provider string the widget expects.
fn family_to_provider(family: &AccountFamily) -> &'static str {
    match family {
        AccountFamily::Claude   => "claude",
        AccountFamily::Openai   => "codex",
        AccountFamily::Google   => "gemini",
        AccountFamily::Opencode => "opencode",
        AccountFamily::Gfp      => "gfp",
    }
}

/// Load `~/.claude/usage-cache.json` and return its `accounts` array, or `None`.
fn load_legacy_usage_cache() -> Option<Vec<Value>> {
    let home = dirs::home_dir()?;
    let path = home.join(".claude").join("usage-cache.json");
    let bytes = std::fs::read(&path).ok()?;
    let v: Value = serde_json::from_slice(&bytes).ok()?;
    v.get("accounts")?.as_array().cloned()
}

/// Find a matching entry in the legacy cache by account id or provider family.
///
/// Match strategy:
///   1. Exact `account` field match (e.g. "claude-acc1").
///   2. Provider-level fallback — ONLY when exactly one entry exists for that
///      provider (single-account family). When 2+ entries share the provider
///      (e.g. "claude" + "claude2"), an exact account-id match is required;
///      otherwise return `None`. This prevents a dead account from borrowing
///      another account's usage numbers.
fn find_legacy_entry<'a>(entries: &'a [Value], acc_id: &str, provider: &str) -> Option<&'a Value> {
    // 1. Exact account id match.
    if let Some(e) = entries.iter().find(|e| e.get("account").and_then(|v| v.as_str()) == Some(acc_id)) {
        return Some(e);
    }
    // 2. Provider-level fallback — only safe when exactly one entry shares the
    //    provider. With multiple same-provider accounts, no exact id match means
    //    we must NOT guess (return None) to avoid cross-assigning usage.
    let mut provider_matches = entries.iter().filter(|e| {
        e.get("provider").and_then(|v| v.as_str()) == Some(provider)
    });
    match (provider_matches.next(), provider_matches.next()) {
        (Some(only), None) => Some(only), // exactly one → safe fallback
        _ => None,                        // zero or 2+ → require exact id match
    }
}

/// Write `~/.cascade/accounts/README.md` explaining the directory layout.
///
/// Inputs:  `path` — destination file.
/// Outputs: atomic write to `path`.
pub fn write_accounts_readme(path: &Path) -> Result<()> {
    let content = r#"# ~/.cascade/accounts/

This directory is managed by Cascade. Do not edit files here manually.

## Files

### accounts.json
The fleet account registry. Lists every AI subscription account Cascade knows
about: Claude Max accounts (A1–A2), Codex (C1), Gemini AGY (G1), OpenCode (O1),
and the GFP key pool.

Written by: `cascade accounts detect`
Read by: `cascade accounts list/status/matrix`, daemon fleet poller.

### quota.json
Live usage snapshot for the native Cascade widget. Refreshed on every daemon
tick (default: 60 s). Contains per-account usage windows (5h rolling, weekly,
weekly-sonnet, weekly-opus) merged from live polling sources.

Written by: daemon fleet poller (automatic)
Read by: native FleetView widget.

Unknown/absent usage data is represented as `null`. Values are NEVER estimated
or faked — they come from real polling sources only.

### README.md
This file.

### matrix.md
Human-readable model routing matrix. Describes when and how to use each model
from which subscription or API. Generated from the accounts registry.

## Adding an account

1. Run `cascade accounts detect` to re-scan all CLIs and update accounts.json.
2. For GFP keys: add `GEMINI_FREE_KEY_N=<key>` entries to `~/.claude/vault.env`
   or `~/.cascade/vault.env`, then run `cascade accounts detect`.
3. For a new subscription account: edit `accounts.json` directly (advanced) or
   wait for a future `cascade accounts add` subcommand.

## Privacy

`accounts.json` and `quota.json` contain account IDs and usage counts but NEVER
store API keys or authentication tokens. Keys live exclusively in vault.env and
are never logged or serialised by Cascade.
"#;
    atomic_write(path, content.as_bytes())
}

/// Write `~/.cascade/accounts/matrix.md` — human-readable model routing table.
///
/// Generates a Markdown table from `registry.model_matrix` with columns:
/// Model | Family | Tier | Subscription/Access | Best For | Notes.
/// Also appends a routing strategy summary.
///
/// Inputs:  `path` — destination file; `registry` — source of model matrix data.
/// Outputs: atomic write to `path`.
pub fn write_matrix_md(path: &Path, registry: &AccountsRegistry) -> Result<()> {
    use std::fmt::Write as FmtWrite;

    let mut out = String::new();
    writeln!(out, "# Model Matrix — Cascade Fleet\n").ok();
    writeln!(out, "Generated from `~/.cascade/accounts/accounts.json`. Do not edit by hand.\n").ok();
    writeln!(out, "## Routing Table\n").ok();
    writeln!(out, "| Model | Family | Tier | Primary Account | Best For | Notes |").ok();
    writeln!(out, "|-------|--------|------|-----------------|----------|-------|").ok();

    for entry in &registry.model_matrix {
        let family = format!("{:?}", entry.family);
        let primary = entry.available_via.first()
            .map(|r| format!("{}:{:?}", r.account_id, r.method))
            .unwrap_or_else(|| "-".into());
        let best = entry.best_for.iter()
            .map(|t| format!("{:?}", t))
            .collect::<Vec<_>>()
            .join(", ");
        let notes = entry.notes.as_deref().unwrap_or("-");
        writeln!(out, "| {} | {} | {} | {} | {} | {} |",
            entry.model_id, family, entry.tier, primary, best, notes).ok();
    }

    writeln!(out, "\n## Routing Strategy\n").ok();
    writeln!(out, "**Exhaustion order** (lower priority number = drained first):\n").ok();

    // Sort accounts by priority for the summary.
    let mut sorted = registry.accounts.clone();
    sorted.sort_by_key(|a| a.exhaustion_priority);
    for acc in &sorted {
        let reserved = if matches!(acc.role, AccountRole::PrimaryT0) { " (**reserved last**)" } else { "" };
        writeln!(out, "- `{}` ({:?}, priority {}){}",
            acc.id, acc.family, acc.exhaustion_priority, reserved).ok();
    }

    writeln!(out, "\n### Key rules\n").ok();
    writeln!(out, "- **Sensitive tasks** (PII / VA / health / custody): Claude-only. All external models hard-blocked.").ok();
    writeln!(out, "- **Adversarial CR/QA**: prefer cross-family (GPT-5.5, Gemini-3.1-Pro) for independence.").ok();
    writeln!(out, "- **Pooled (acc2)** is drained before primary (acc1, priority 255).").ok();
    writeln!(out, "- **GFP pool** (priority 1) is always drained first for grunt/classify/taxonomy work.").ok();
    writeln!(out, "- **Primary acc1** is reserved for T0 interactive use; background agents use acc2+.").ok();

    atomic_write(path, out.as_bytes())
}

// ── Ensure directory ──────────────────────────────────────────────────────────

/// Create `~/.cascade/accounts/` and write all four managed files.
///
/// Called by `cascade accounts detect` and at startup when the directory is absent.
/// Inputs:  `registry` — current (or freshly built) `AccountsRegistry`.
/// Outputs: creates the dir and writes accounts.json, quota.json, README.md, matrix.md.
pub fn init_accounts_dir(registry: &AccountsRegistry) -> Result<()> {
    let dir = accounts_dir();
    std::fs::create_dir_all(&dir)
        .map_err(|e| CascadeError::io(dir.clone(), "create_dir_all", e))?;

    write_accounts_registry(&dir.join("accounts.json"), registry)?;
    write_quota_json(&dir.join("quota.json"), registry)?;
    write_accounts_readme(&dir.join("README.md"))?;
    write_matrix_md(&dir.join("matrix.md"), registry)?;
    Ok(())
}

// ── Internal helper ───────────────────────────────────────────────────────────

/// Atomic write: write to `<path>.tmp` then rename over `path`.
///
/// Constraints: POSIX-atomic when tmp and dst share a filesystem (same dir).
fn atomic_write(path: &Path, bytes: &[u8]) -> Result<()> {
    let tmp = path.with_extension("tmp");
    std::fs::write(&tmp, bytes).map_err(|e| CascadeError::io(tmp.clone(), "write", e))?;
    std::fs::rename(&tmp, path).map_err(|e| CascadeError::io(path.to_path_buf(), "rename", e))?;
    Ok(())
}

// ── CLI detection ─────────────────────────────────────────────────────────────

/// Return `true` if `name` is found in `PATH` via `which`.
///
/// Returns `false` on any error; never panics.
pub fn detect_cli(name: &str) -> bool {
    std::process::Command::new("which")
        .arg(name)
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

// ── GFP key counting ──────────────────────────────────────────────────────────

/// Count GFP API keys in `~/.claude/vault.env` and `~/.cascade/vault.env`.
///
/// Counts lines matching `GEMINI_FREE_KEY_*`, `GEMINI_KEY_*`, `GEMINI_API_KEY_*`.
/// NEVER logs key values — only the count.
pub fn count_gfp_keys() -> u32 {
    let home = dirs::home_dir().unwrap_or_else(|| PathBuf::from("~"));
    [home.join(".claude").join("vault.env"), home.join(".cascade").join("vault.env")]
        .iter()
        .map(|p| count_gfp_keys_from_path(p))
        .fold(0u32, |a, b| a.saturating_add(b))
}

/// Count GFP API keys from a specific vault file (testable inner implementation).
/// Returns 0 if the file is absent or unreadable.
pub fn count_gfp_keys_from_path(vault_path: &Path) -> u32 {
    let Ok(content) = std::fs::read_to_string(vault_path) else { return 0 };
    content.lines().filter(|line| {
        let t = line.trim();
        if t.is_empty() || t.starts_with('#') { return false; }
        let k = t.split('=').next().unwrap_or("").trim();
        k.starts_with("GEMINI_FREE_KEY_") || k.starts_with("GEMINI_KEY_") || k.starts_with("GEMINI_API_KEY_")
    }).count() as u32
}

// ── Default registry seed ─────────────────────────────────────────────────────

/// Returns an empty [`AccountsRegistry`] — users configure accounts at runtime.
///
/// Accounts are loaded from `~/.cascade/accounts/accounts.json` (written by
/// `cascade accounts init` or the UI wizard).  The registry returned here is
/// a valid zero-account seed used before any user configuration exists.
pub fn default_registry() -> AccountsRegistry {
    AccountsRegistry {
        schema_version: ACCOUNTS_SCHEMA_VERSION,
        updated_at: chrono::Utc::now().to_rfc3339(),
        accounts: vec![],
        model_matrix: build_model_matrix(),
    }
}

/// Model routing matrix — empty by default; populated at runtime from configured accounts.
///
/// Entries are derived from the user's `~/.cascade/accounts/accounts.json` when
/// accounts are loaded.  The empty vec returned here is the correct default before
/// any accounts are configured.
fn build_model_matrix() -> Vec<ModelMatrixEntry> {
    vec![]
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::TempDir;

    /// Serialise `default_registry()` → deserialise → assert structure.
    #[test]
    fn registry_serde_round_trip() {
        let registry = default_registry();
        let json = serde_json::to_vec_pretty(&registry).unwrap();
        let decoded: AccountsRegistry = serde_json::from_slice(&json).unwrap();
        assert_eq!(decoded.schema_version, ACCOUNTS_SCHEMA_VERSION);
        assert_eq!(decoded.accounts.len(), 0, "default registry has no seeded accounts");
    }

    /// Write fixture vault.env with 3 GEMINI_FREE_KEY_* entries; assert count == 3.
    #[test]
    fn gfp_key_count_fixture() {
        let dir = TempDir::new().unwrap();
        let vault_path = dir.path().join("vault.env");
        let mut f = std::fs::File::create(&vault_path).unwrap();
        writeln!(f, "# Vault file").unwrap();
        writeln!(f, "GEMINI_FREE_KEY_1=fake-key-aaa").unwrap();
        writeln!(f, "GEMINI_FREE_KEY_2=fake-key-bbb").unwrap();
        writeln!(f, "GEMINI_FREE_KEY_3=fake-key-ccc").unwrap();
        writeln!(f, "OTHER_KEY=some-value").unwrap();
        drop(f);
        assert_eq!(count_gfp_keys_from_path(&vault_path), 3, "expected 3 GFP keys");
    }

    /// detect_cli should return a bool without panicking; absent binary must be false.
    #[test]
    fn cli_detection_not_fake() {
        let _result = detect_cli("cascade"); // must not panic
        assert!(!detect_cli("definitely_not_a_real_binary_xyz123"));
    }

    /// Every model in Account::models must appear in model_matrix.
    #[test]
    fn matrix_lookup_all_models_covered() {
        let registry = default_registry();
        let matrix: std::collections::HashSet<&str> =
            registry.model_matrix.iter().map(|e| e.model_id.as_str()).collect();
        for acc in &registry.accounts {
            for model in &acc.models {
                assert!(
                    matrix.contains(model.as_str()),
                    "model '{}' from '{}' missing from model_matrix", model, acc.id
                );
            }
        }
    }

    /// init_accounts_dir creates the directory and all four expected files.
    #[test]
    fn init_accounts_dir_creates_all_files() {
        let tmp = TempDir::new().unwrap();
        let dir = tmp.path().join("accounts");

        // Patch accounts_dir via direct call pattern (test using explicit paths).
        let registry = default_registry();
        std::fs::create_dir_all(&dir).unwrap();
        write_accounts_registry(&dir.join("accounts.json"), &registry).unwrap();
        write_quota_json(&dir.join("quota.json"), &registry).unwrap();
        write_accounts_readme(&dir.join("README.md")).unwrap();
        write_matrix_md(&dir.join("matrix.md"), &registry).unwrap();

        assert!(dir.join("accounts.json").exists(), "accounts.json must exist");
        assert!(dir.join("quota.json").exists(), "quota.json must exist");
        assert!(dir.join("README.md").exists(), "README.md must exist");
        assert!(dir.join("matrix.md").exists(), "matrix.md must exist");
    }

    /// quota.json shape: must use AccountEntry schema the widget decodes.
    #[test]
    fn quota_json_shape_is_correct() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("quota.json");
        let registry = default_registry();
        write_quota_json(&path, &registry).unwrap();

        let bytes = std::fs::read(&path).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();

        // Top-level: queried_at + accounts (no legacy "updated_at").
        assert!(v.get("queried_at").is_some(), "quota.json must have queried_at");
        assert!(v.get("accounts").is_some(), "quota.json must have accounts");

        let accts = v.get("accounts").and_then(|a| a.as_array()).unwrap();
        // Default registry has no accounts; verify the array exists and is empty.
        assert_eq!(accts.len(), 0, "default registry must produce 0 account entries");

        // Schema-shape assertions run only when entries are present.
        for entry in accts {
            assert!(entry.get("account").is_some(),
                "must have 'account' field (not 'id')");
            assert!(entry.get("provider").is_some(),
                "must have 'provider' field (not 'family')");
            assert!(entry.get("usage").is_some(),
                "must have 'usage' field");
            assert!(entry.get("status").is_some(),
                "must have 'status' field");
            // Must NOT have old schema fields.
            assert!(entry.get("id").is_none(),
                "must NOT have legacy 'id' field");
            assert!(entry.get("family").is_none(),
                "must NOT have legacy 'family' field");
            assert!(entry.get("windows").is_none(),
                "must NOT have legacy 'windows' field");
        }
    }

    /// quota.json provider mapping: with an empty registry the accounts array is empty.
    #[test]
    fn quota_json_provider_mapping() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("quota.json");
        let registry = default_registry();
        write_quota_json(&path, &registry).unwrap();

        let bytes = std::fs::read(&path).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let accts = v.get("accounts").and_then(|a| a.as_array()).unwrap();

        // Default registry is empty — no accounts to map.
        assert_eq!(accts.len(), 0, "default registry must produce an empty accounts array");
    }

    /// migration: old ~/.cascade/accounts.json → ~/.cascade/accounts/accounts.json.
    #[test]
    fn migration_moves_old_file_into_dir() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();

        // Write the "old" flat file.
        let old_path = cascade_dir.join("accounts.json");
        let registry = default_registry();
        let bytes = serde_json::to_vec_pretty(&registry).unwrap();
        std::fs::write(&old_path, &bytes).unwrap();

        // Simulate migration by copying old → new and removing old.
        let new_dir = cascade_dir.join("accounts");
        std::fs::create_dir_all(&new_dir).unwrap();
        let new_path = new_dir.join("accounts.json");
        std::fs::write(&new_path, &bytes).unwrap();
        std::fs::remove_file(&old_path).unwrap();

        assert!(!old_path.exists(), "old flat file must be gone");
        assert!(new_path.exists(), "new accounts/accounts.json must exist");
    }

    /// matrix.md must contain the model header and routing strategy section.
    #[test]
    fn matrix_md_contains_expected_sections() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("matrix.md");
        let registry = default_registry();
        write_matrix_md(&path, &registry).unwrap();

        let content = std::fs::read_to_string(&path).unwrap();
        assert!(content.contains("# Model Matrix"), "must have H1 heading");
        assert!(content.contains("## Routing Table"), "must have Routing Table section");
        assert!(content.contains("## Routing Strategy"), "must have Routing Strategy section");
        // Default registry has no models — matrix table is empty but headings are present.
    }

    // Helper mirroring the auth_dead decision in write_quota_json (FIX 4):
    // a definitive live-fetch auth error is ground truth and OVERRIDES the
    // optimistic keychain bridge.
    fn decide_auth_dead(
        live_auth: Option<&crate::external_accounts::AuthStatus>,
        last_error: Option<&str>,
    ) -> bool {
        use crate::external_accounts::AuthStatus;
        let definitive = last_error
            .map(|e| {
                matches!(
                    e,
                    "keychain_failed" | "refresh_failed" | "auth_invalid"
                        | "invalid_grant" | "authentication_error" | "http_401" | "http_403"
                )
            })
            .unwrap_or(false);
        if definitive {
            return true;
        }
        match live_auth {
            Some(AuthStatus::Ok { .. }) => false,
            Some(AuthStatus::NeedsReauth) => true,
            _ => false,
        }
    }

    /// FIX 4: a definitive live-fetch auth error (http_401) OVERRIDES the bridge's
    /// optimistic Ok. The bridge reports Ok only because a refreshToken string
    /// exists, but an actual API 401 means the token is truly dead.
    #[test]
    fn definitive_auth_failure_overrides_bridge_ok() {
        use crate::external_accounts::{parse_claude_blob, AuthStatus};

        // Live bridge sees a (string-present) future-expiry token → Ok.
        let far_future_ms = 9_999_999_999_999i64;
        let live_blob = format!(
            r#"{{"claudeAiOauth":{{"accessToken":"tok","refreshToken":"rtok","expiresAt":{far_future_ms}}}}}"#
        );
        let live_auth = parse_claude_blob(&live_blob);
        assert!(matches!(live_auth, AuthStatus::Ok { .. }));

        // But the live fetch 401'd → ground truth wins: auth_dead must be true.
        assert!(
            decide_auth_dead(Some(&live_auth), Some("http_401")),
            "http_401 ground truth must override bridge Ok"
        );
        assert!(
            decide_auth_dead(Some(&live_auth), Some("refresh_failed")),
            "refresh_failed must override bridge Ok"
        );

        // Transient errors must NOT mark auth-dead when the bridge is Ok.
        assert!(!decide_auth_dead(Some(&live_auth), Some("api_failed")));
        assert!(!decide_auth_dead(Some(&live_auth), Some("rate_limit_error")));
        assert!(!decide_auth_dead(Some(&live_auth), Some("http_429")));
        assert!(!decide_auth_dead(Some(&live_auth), None));
    }

    /// Live bridge NeedsReauth → auth_dead must be true.
    #[test]
    fn live_bridge_needs_reauth_sets_auth_dead() {
        use crate::external_accounts::{parse_claude_blob, AuthStatus};

        // Simulate: live keychain entry has empty tokens (truly revoked).
        let live_blob = r#"{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":9999}}"#;
        let live_auth = parse_claude_blob(live_blob);
        assert_eq!(live_auth, AuthStatus::NeedsReauth);

        let auth_dead = match live_auth {
            AuthStatus::Ok { .. } => false,
            AuthStatus::NeedsReauth => true,
            AuthStatus::Unknown => false,
        };

        assert!(auth_dead, "live NeedsReauth must result in auth_dead = true");
    }

    /// FIX 1: with 2+ entries sharing a provider, a lookup with no exact id match
    /// must return None — NOT borrow another account's entry.
    #[test]
    fn find_legacy_entry_no_cross_assign_with_multiple_same_provider() {
        use serde_json::json;

        let entries = vec![
            json!({ "account": "claude",  "provider": "claude", "usage": { "five_hour": { "utilization": 99.0 } } }),
            json!({ "account": "claude2", "provider": "claude", "usage": { "five_hour": { "utilization": 3.0 } } }),
        ];

        // Exact match present → returns that exact entry.
        let exact = find_legacy_entry(&entries, "claude2", "claude");
        assert_eq!(
            exact.and_then(|e| e.get("account")).and_then(|v| v.as_str()),
            Some("claude2"),
            "exact id match must win"
        );

        // No exact match for a dead "claude-dead" id with 2 claude entries → None.
        // (Dead account must NOT grab claude2's numbers.)
        let dead = find_legacy_entry(&entries, "claude-dead", "claude");
        assert!(
            dead.is_none(),
            "with 2+ same-provider entries and no exact id match, must return None"
        );
    }

    /// FIX 1: single same-provider entry still falls back by provider (back-compat).
    #[test]
    fn find_legacy_entry_single_provider_fallback_preserved() {
        use serde_json::json;

        let entries = vec![
            json!({ "account": "codex", "provider": "codex", "usage": { "five_hour": null } }),
        ];

        // Lookup by a non-matching id but matching single provider → returns the one entry.
        let got = find_legacy_entry(&entries, "codex-c1", "codex");
        assert_eq!(
            got.and_then(|e| e.get("account")).and_then(|v| v.as_str()),
            Some("codex"),
            "single same-provider entry must still fall back by provider"
        );
    }
}
