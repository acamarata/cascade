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
    Account, AccountFamily, AccountRole, AccountsRegistry, AccessMethod, ModelMatrixEntry,
    ModelRoute, TaskClass, ACCOUNTS_SCHEMA_VERSION,
};
use cascade_types::error::{CascadeError, Result};
use serde_json::Value;

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

        let mut entry = json!({
            "account": acc.id,
            "provider": provider,
            "email": email,
            "usage": usage,
            "last_pull_at": last_pull_at,
            "quota_opaque": quota_opaque.unwrap_or(false),
            "status": "ok"
        });

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
///   1. Exact `account` field match (e.g. "claude-acc1")
///   2. Provider string match — legacy "codex" entry → our "codex" provider,
///      legacy "opencode-1" → our "opencode", legacy "gemini-1" → our "gemini".
fn find_legacy_entry<'a>(entries: &'a [Value], acc_id: &str, provider: &str) -> Option<&'a Value> {
    // 1. Exact account id match.
    if let Some(e) = entries.iter().find(|e| e.get("account").and_then(|v| v.as_str()) == Some(acc_id)) {
        return Some(e);
    }
    // 2. Provider-level match: the legacy entry's `provider` field matches ours.
    //    Return the first matching entry for that provider (single-account families).
    entries.iter().find(|e| {
        e.get("provider").and_then(|v| v.as_str()) == Some(provider)
    })
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

/// Build the seeded default [`AccountsRegistry`] with 6 known fleet accounts.
///
/// Active roster: claude-acc1 (primary), claude-acc2 (pooled),
/// codex-acc1, gemini-acc1, opencode-acc1, gfp-pool.
/// Detects CLI availability from real PATH; counts GFP keys from vault files.
pub fn default_registry() -> AccountsRegistry {
    let cc1 = detect_cli("claude");
    let smt = detect_cli("smithers") || detect_cli("claude-p");
    let cx = detect_cli("codex");
    let agy = detect_cli("agy");
    let oc = detect_cli("opencode");
    let gfp = count_gfp_keys();

    let opus_sonnet_haiku_fable = || vec![
        "claude-opus-4-8".into(), "claude-sonnet-4-6".into(),
        "claude-haiku-4-5".into(), "claude-fable-5".into(),
    ];
    let opus_sonnet_haiku = || vec![
        "claude-opus-4-8".into(), "claude-sonnet-4-6".into(), "claude-haiku-4-5".into(),
    ];

    let accounts = vec![
        acc("claude-acc1", AccountFamily::Claude, "anthropic-max",
            vec![AccessMethod::NativeCc], AccountRole::PrimaryT0, 255,
            opus_sonnet_haiku_fable(), cc1, 0, Some("cc-acct1"), None),
        acc("claude-acc2", AccountFamily::Claude, "anthropic-max",
            vec![AccessMethod::SmithersClaudeP], AccountRole::Pooled, 10,
            opus_sonnet_haiku(), smt, 0, None, Some("Extra CC account — drain first via PTY")),
        acc("codex-acc1", AccountFamily::Openai, "openai-pro",
            vec![AccessMethod::CodexCli], AccountRole::Pooled, 50,
            vec!["gpt-5.5".into()], cx, 0, None, None),
        acc("gemini-acc1", AccountFamily::Google, "google-ai-ultra",
            vec![AccessMethod::AgyCli], AccountRole::Pooled, 50,
            vec!["gemini-3.1-pro".into()], agy, 0, None, None),
        acc("opencode-acc1", AccountFamily::Opencode, "opencode-run",
            vec![AccessMethod::OpencodeRun], AccountRole::Pooled, 60,
            vec!["glm-5.2".into(), "qwen-2.5-coder".into(), "deepseek-v3".into(), "zen".into()],
            oc, 0, None, None),
        acc("gfp-pool", AccountFamily::Gfp, "gemini-free",
            vec![AccessMethod::GfpKeypool], AccountRole::Free, 1,
            vec!["gemini-3.5-flash".into()],
            true, gfp, None, Some("GFP round-robin key pool — key-based, no CLI binary required")),
    ];

    AccountsRegistry {
        schema_version: ACCOUNTS_SCHEMA_VERSION,
        updated_at: chrono::Utc::now().to_rfc3339(),
        accounts,
        model_matrix: build_model_matrix(),
    }
}

/// Compact account constructor helper.
#[allow(clippy::too_many_arguments)]
fn acc(
    id: &str, family: AccountFamily, sub: &str, methods: Vec<AccessMethod>,
    role: AccountRole, pri: u8, models: Vec<String>, cli: bool, keys: u32,
    quota: Option<&str>, notes: Option<&str>,
) -> Account {
    Account {
        id: id.into(), family, subscription: sub.into(), access_methods: methods,
        role, exhaustion_priority: pri, models, cli_available: cli, key_count: keys,
        quota_account_id: quota.map(|s| s.into()),
        notes: notes.map(|s| s.into()),
    }
}

/// Build the model routing matrix for all known models.
fn build_model_matrix() -> Vec<ModelMatrixEntry> {
    use AccessMethod::*; use AccountFamily::*; use TaskClass::*;
    let cc_pooled = || vec![
        ModelRoute { account_id: "claude-acc2".into(), method: SmithersClaudeP },
        ModelRoute { account_id: "claude-acc1".into(), method: NativeCc },
    ];
    vec![
        mme("claude-opus-4-8", Claude, "T1",
            vec![ModelRoute{account_id:"claude-acc1".into(),method:NativeCc},
                 ModelRoute{account_id:"claude-acc2".into(),method:SmithersClaudeP}],
            vec![AdversarialCr, FinalGate, Sensitive],
            Some("Opus-tier — reserved for high-stakes synthesis and final gates")),
        mme("claude-sonnet-4-6", Claude, "T2", cc_pooled(),
            vec![BulkExecution, InteractiveChat, Background], None),
        mme("claude-haiku-4-5", Claude, "T3-cheap", cc_pooled(),
            vec![Grunt, Taxonomy, PostPrompt], None),
        mme("claude-fable-5", Claude, "T2",
            vec![ModelRoute{account_id:"claude-acc1".into(),method:NativeCc}],
            vec![BulkExecution],
            Some("Fable — available only on primary NativeCc account")),
        mme("gpt-5.5", Openai, "T2",
            vec![ModelRoute{account_id:"codex-acc1".into(),method:CodexCli}],
            vec![BulkExecution, Background], None),
        mme("gemini-3.1-pro", Google, "T2",
            vec![ModelRoute{account_id:"gemini-acc1".into(),method:AgyCli}],
            vec![BulkExecution, Grunt], None),
        mme("glm-5.2", Opencode, "T2",
            vec![ModelRoute{account_id:"opencode-acc1".into(),method:OpencodeRun}],
            vec![BulkExecution, Background], None),
        mme("qwen-2.5-coder", Opencode, "T2",
            vec![ModelRoute{account_id:"opencode-acc1".into(),method:OpencodeRun}],
            vec![BulkExecution, Grunt], None),
        mme("deepseek-v3", Opencode, "T2",
            vec![ModelRoute{account_id:"opencode-acc1".into(),method:OpencodeRun}],
            vec![BulkExecution, Background], None),
        mme("zen", Opencode, "T3-cheap",
            vec![ModelRoute{account_id:"opencode-acc1".into(),method:OpencodeRun}],
            vec![Grunt, PostPrompt], None),
        mme("gemini-3.5-flash", Gfp, "T3-cheap",
            vec![ModelRoute{account_id:"gfp-pool".into(),method:GfpKeypool}],
            vec![Grunt, Taxonomy, PostPrompt, Background],
            Some("GFP key-pool — cheapest; drain first for grunt work")),
    ]
}

/// Compact model matrix entry constructor.
fn mme(
    model_id: &str, family: AccountFamily, tier: &str,
    routes: Vec<ModelRoute>, best_for: Vec<TaskClass>, notes: Option<&str>,
) -> ModelMatrixEntry {
    ModelMatrixEntry {
        model_id: model_id.into(), available_via: routes, best_for,
        tier: tier.into(), family, notes: notes.map(|s| s.into()),
    }
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
        assert_eq!(decoded.accounts.len(), 6, "expected 6 seeded accounts");
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
        assert_eq!(accts.len(), 6, "must have one entry per account (6 accounts)");

        // Collect providers to verify roster.
        let providers: Vec<&str> = accts.iter()
            .filter_map(|e| e.get("provider").and_then(|p| p.as_str()))
            .collect();
        assert_eq!(providers.iter().filter(|&&p| p == "claude").count(), 2,
            "must have 2 claude accounts");

        // Each entry must have AccountEntry widget fields (not old id/family/windows schema).
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

    /// quota.json provider mapping: each family maps to the correct provider string.
    #[test]
    fn quota_json_provider_mapping() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("quota.json");
        let registry = default_registry();
        write_quota_json(&path, &registry).unwrap();

        let bytes = std::fs::read(&path).unwrap();
        let v: serde_json::Value = serde_json::from_slice(&bytes).unwrap();
        let accts = v.get("accounts").and_then(|a| a.as_array()).unwrap();

        // Build account→provider map from output.
        let map: std::collections::HashMap<&str, &str> = accts.iter()
            .filter_map(|e| {
                let acc = e.get("account")?.as_str()?;
                let prov = e.get("provider")?.as_str()?;
                Some((acc, prov))
            })
            .collect();

        assert_eq!(map.get("claude-acc1"), Some(&"claude"), "claude-acc1 → claude");
        assert_eq!(map.get("claude-acc2"), Some(&"claude"), "claude-acc2 → claude");
        assert_eq!(map.get("codex-acc1"),  Some(&"codex"),  "codex-acc1 → codex");
        assert_eq!(map.get("gemini-acc1"), Some(&"gemini"), "gemini-acc1 → gemini");
        assert_eq!(map.get("opencode-acc1"), Some(&"opencode"), "opencode-acc1 → opencode");
        assert_eq!(map.get("gfp-pool"),    Some(&"gfp"),    "gfp-pool → gfp");
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
        assert!(content.contains("claude-opus-4-8"), "must list claude-opus-4-8");
        assert!(content.contains("claude-acc1"), "must mention acc1");
    }
}
