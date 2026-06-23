//! Accounts store — atomic I/O for `~/.cascade/accounts.json`.
//!
//! Purpose: read/write the Cascade fleet account registry; build the seeded
//! default registry from known subscription accounts; detect CLI availability.
//!
//! Inputs:  path to `accounts.json`; vault.env files for GFP key count.
//! Outputs: `AccountsRegistry` on read; atomic file write (tmp → rename) on write.
//! Constraints: no `unwrap()` outside `#[cfg(test)]`; never log key values.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — accounts_store

use std::path::{Path, PathBuf};

use cascade_types::accounts::{
    Account, AccountFamily, AccountRole, AccountsRegistry, AccessMethod, ModelMatrixEntry,
    ModelRoute, TaskClass, ACCOUNTS_SCHEMA_VERSION,
};
use cascade_types::error::{CascadeError, Result};

// ── Path helper ───────────────────────────────────────────────────────────────

/// Returns the canonical path to `~/.cascade/accounts.json`.
///
/// Outputs: `PathBuf` — may not exist on a fresh install.
pub fn accounts_path() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("~"))
        .join(".cascade")
        .join("accounts.json")
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

// ── Writer ────────────────────────────────────────────────────────────────────

/// Write `registry` to `path` atomically (tmp → rename).
///
/// Constraints: POSIX-atomic when tmp and dst share a filesystem (same dir).
pub fn write_accounts_registry(path: &Path, registry: &AccountsRegistry) -> Result<()> {
    let bytes = serde_json::to_vec_pretty(registry).map_err(|e| CascadeError::Other(e.to_string()))?;
    let tmp = path.with_extension("tmp");
    std::fs::write(&tmp, &bytes).map_err(|e| CascadeError::io(tmp.clone(), "write", e))?;
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

/// Build the seeded default [`AccountsRegistry`] with 8 known fleet accounts.
///
/// Detects CLI availability from real PATH; counts GFP keys from vault files.
pub fn default_registry() -> AccountsRegistry {
    let cc1 = detect_cli("claude");
    let smt = detect_cli("smithers") || detect_cli("claude-p");
    let cx = detect_cli("codex");
    let agy = detect_cli("agy");
    let oc = detect_cli("opencode");
    let gfp = count_gfp_keys();

    let opus_sonnet_haiku_fable = || vec![
        "claude-opus-4-5".into(), "claude-sonnet-4-6".into(),
        "claude-haiku-4-5".into(), "claude-fable-5".into(),
    ];
    let opus_sonnet_haiku = || vec![
        "claude-opus-4-5".into(), "claude-sonnet-4-6".into(), "claude-haiku-4-5".into(),
    ];

    let accounts = vec![
        acc("claude-acc1", AccountFamily::Claude, "anthropic-max",
            vec![AccessMethod::NativeCc], AccountRole::PrimaryT0, 255,
            opus_sonnet_haiku_fable(), cc1, 0, Some("cc-acct1"), None),
        acc("claude-acc2", AccountFamily::Claude, "anthropic-max",
            vec![AccessMethod::SmithersClaudeP], AccountRole::Pooled, 10,
            opus_sonnet_haiku(), smt, 0, None, Some("Extra CC account — drain first via PTY")),
        acc("claude-acc3", AccountFamily::Claude, "anthropic-max",
            vec![AccessMethod::SmithersClaudeP], AccountRole::Pooled, 11,
            opus_sonnet_haiku(), smt, 0, None, Some("Extra CC account — drain first via PTY")),
        acc("claude-acc4", AccountFamily::Claude, "anthropic-max",
            vec![AccessMethod::SmithersClaudeP], AccountRole::Pooled, 12,
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
            vec!["gemini-2.5-flash-preview-05-20".into()],
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
        ModelRoute { account_id: "claude-acc3".into(), method: SmithersClaudeP },
        ModelRoute { account_id: "claude-acc4".into(), method: SmithersClaudeP },
        ModelRoute { account_id: "claude-acc1".into(), method: NativeCc },
    ];
    vec![
        mme("claude-opus-4-5", Claude, "T1",
            vec![ModelRoute{account_id:"claude-acc1".into(),method:NativeCc},
                 ModelRoute{account_id:"claude-acc2".into(),method:SmithersClaudeP},
                 ModelRoute{account_id:"claude-acc3".into(),method:SmithersClaudeP},
                 ModelRoute{account_id:"claude-acc4".into(),method:SmithersClaudeP}],
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
        mme("gemini-2.5-flash-preview-05-20", Gfp, "T3-cheap",
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
        assert_eq!(decoded.accounts.len(), 8, "expected 8 seeded accounts");
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
}
