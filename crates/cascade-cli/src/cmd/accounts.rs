//! `cascade accounts` — manage the fleet account registry.
//!
//! Purpose: CLI surface for inspecting and refreshing `~/.cascade/accounts/`.
//! Subcommands: list, status, matrix, detect. All support `--json`.
//!
//! Layout: all files live under `~/.cascade/accounts/`:
//!   - accounts.json — the registry
//!   - quota.json    — live usage (widget reads this)
//!   - README.md     — human docs
//!   - matrix.md     — model routing table
//!
//! Constraints: no `unwrap()` outside `#[cfg(test)]`; never log key values; ≤300 lines.
//! SPORT: cascade-cli / accounts subcommand

use async_trait::async_trait;
use cascade_core::accounts_store::{
    accounts_dir, accounts_path, count_gfp_keys, default_registry, detect_cli, init_accounts_dir,
    migrate_accounts_if_needed, read_accounts_registry, write_accounts_registry, write_matrix_md,
    write_quota_json,
};
use cascade_core::quota_store::read_quota_store;
use cascade_types::accounts::{Account, AccountFamily};
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};

use super::Command;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade accounts`.
#[derive(Debug, Args)]
pub struct AccountsArgs {
    #[command(subcommand)]
    pub command: AccountsCommands,
}

/// Subcommands under `cascade accounts`.
#[derive(Debug, Subcommand)]
pub enum AccountsCommands {
    /// List all registered accounts as a table.
    List(JsonFlag),
    /// Show per-account usage from quota-store.json.
    Status(JsonFlag),
    /// Show the model routing matrix.
    Matrix(JsonFlag),
    /// Re-scan CLI availability + key count; write all accounts/ files.
    Detect(JsonFlag),
}

/// Shared `--json` flag used by all subcommands.
#[derive(Debug, Args)]
pub struct JsonFlag {
    /// Output as machine-readable JSON.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for AccountsArgs {
    async fn run(&self) -> Result<()> {
        // Migrate old flat file before any subcommand runs.
        let _ = migrate_accounts_if_needed();
        match &self.command {
            AccountsCommands::List(f) => run_list(f.json).await,
            AccountsCommands::Status(f) => run_status(f.json).await,
            AccountsCommands::Matrix(f) => run_matrix(f.json).await,
            AccountsCommands::Detect(f) => run_detect(f.json).await,
        }
    }
}

// ── list ──────────────────────────────────────────────────────────────────────

async fn run_list(as_json: bool) -> Result<()> {
    let registry = read_accounts_registry(&accounts_path())?;
    if as_json {
        println!("{}", to_json(&registry.accounts)?);
        return Ok(());
    }
    print_accounts_table(&registry.accounts);
    Ok(())
}

/// Print accounts as a formatted table to stdout.
///
/// Inputs: slice of [`Account`] records.
/// Outputs: formatted table on stdout; never panics.
pub fn print_accounts_table(accounts: &[Account]) {
    println!(
        "{:<16} {:<10} {:<18} {:<20} {:<6} {:<6} {:<14} {:<12} {:<8}",
        "ID", "Family", "Subscription", "Access", "CLI?", "Keys", "Quota-Link", "Role", "Priority"
    );
    println!("{}", "-".repeat(115));
    for a in accounts {
        let fam = format!("{:?}", a.family);
        let access = a
            .access_methods
            .iter()
            .map(|m| format!("{:?}", m))
            .collect::<Vec<_>>()
            .join(",");
        let quota = a.quota_account_id.as_deref().unwrap_or("-");
        let role = format!("{:?}", a.role);
        println!(
            "{:<16} {:<10} {:<18} {:<20} {:<6} {:<6} {:<14} {:<12} {:<8}",
            trunc(&a.id, 16),
            trunc(&fam, 10),
            trunc(&a.subscription, 18),
            trunc(&access, 20),
            if a.cli_available { "yes" } else { "no" },
            a.key_count,
            trunc(quota, 14),
            trunc(&role, 12),
            a.exhaustion_priority
        );
    }
}

// ── status ────────────────────────────────────────────────────────────────────

async fn run_status(as_json: bool) -> Result<()> {
    let registry = read_accounts_registry(&accounts_path())?;
    // quota-store.json lives next to accounts.json in the accounts/ dir.
    let store_path = accounts_dir().join("..").join("quota-store.json");
    let store_path = store_path.canonicalize().unwrap_or_else(|_| {
        dirs::home_dir()
            .unwrap_or_default()
            .join(".cascade")
            .join("quota-store.json")
    });

    let quota = match read_quota_store(&store_path) {
        Ok(s) => Some(s),
        Err(CascadeError::PathNotFound { .. }) => None,
        Err(e) => return Err(e),
    };

    if as_json {
        println!("{}", to_json(&registry.accounts)?);
        return Ok(());
    }

    println!(
        "{:<16} {:<14} {:<14} {:<14}",
        "ID", "Week Used", "Month Used", "Last Polled"
    );
    println!("{}", "-".repeat(62));
    for acc in &registry.accounts {
        let (week, month, polled) = quota
            .as_ref()
            .and_then(|qs| {
                acc.quota_account_id
                    .as_deref()
                    .and_then(|id| qs.accounts.iter().find(|e| e.account_id == id))
            })
            .map(|e| {
                (
                    e.week_total_used.to_string(),
                    e.month_total_used.to_string(),
                    e.last_polled.clone(),
                )
            })
            .unwrap_or_else(|| ("-".into(), "-".into(), "-".into()));
        println!(
            "{:<16} {:<14} {:<14} {:<14}",
            acc.id,
            week,
            month,
            trunc(&polled, 14)
        );
    }
    Ok(())
}

// ── matrix ────────────────────────────────────────────────────────────────────

async fn run_matrix(as_json: bool) -> Result<()> {
    let registry = read_accounts_registry(&accounts_path())?;
    if as_json {
        println!("{}", to_json(&registry.model_matrix)?);
        return Ok(());
    }

    println!(
        "{:<36} {:<30} {:<28} {:<10}",
        "Model", "Available-Via", "Best-For", "Tier"
    );
    println!("{}", "-".repeat(106));
    for e in &registry.model_matrix {
        let via = e
            .available_via
            .iter()
            .map(|r| format!("{}:{:?}", r.account_id, r.method))
            .collect::<Vec<_>>()
            .join(",");
        let best = e
            .best_for
            .iter()
            .map(|t| format!("{:?}", t))
            .collect::<Vec<_>>()
            .join(",");
        println!(
            "{:<36} {:<30} {:<28} {:<10}",
            trunc(&e.model_id, 36),
            trunc(&via, 30),
            trunc(&best, 28),
            e.tier
        );
    }
    Ok(())
}

// ── detect ────────────────────────────────────────────────────────────────────

async fn run_detect(as_json: bool) -> Result<()> {
    let path = accounts_path();
    let dir = accounts_dir();

    let mut registry = if path.exists() {
        read_accounts_registry(&path)?
    } else {
        // First run: build registry and write all accounts/ files.
        let reg = default_registry();
        init_accounts_dir(&reg)?;
        reg
    };

    let gfp_keys = count_gfp_keys();
    let mut changes: Vec<String> = Vec::new();

    for acc in &mut registry.accounts {
        if acc.family == AccountFamily::Gfp {
            if acc.key_count != gfp_keys {
                changes.push(format!(
                    "{}: key_count {} → {}",
                    acc.id, acc.key_count, gfp_keys
                ));
                acc.key_count = gfp_keys;
            }
        } else {
            let new_avail = acc.access_methods.iter().any(|m| match m {
                cascade_types::accounts::AccessMethod::NativeCc => detect_cli("claude"),
                cascade_types::accounts::AccessMethod::SmithersClaudeP => {
                    detect_cli("smithers") || detect_cli("claude-p")
                }
                cascade_types::accounts::AccessMethod::CodexCli => detect_cli("codex"),
                cascade_types::accounts::AccessMethod::AgyCli => detect_cli("agy"),
                cascade_types::accounts::AccessMethod::OpencodeRun => detect_cli("opencode"),
                cascade_types::accounts::AccessMethod::GfpKeypool => true,
            });
            if new_avail != acc.cli_available {
                changes.push(format!(
                    "{}: cli_available {} → {}",
                    acc.id, acc.cli_available, new_avail
                ));
                acc.cli_available = new_avail;
            }
        }
    }

    registry.updated_at = chrono::Utc::now().to_rfc3339();

    // Write all four accounts/ files.
    write_accounts_registry(&path, &registry)?;
    write_quota_json(&dir.join("quota.json"), &registry)?;
    write_matrix_md(&dir.join("matrix.md"), &registry)?;
    // README.md only written on first-run (init_accounts_dir) — stable human doc.

    if as_json {
        let out = serde_json::json!({
            "dir": dir.display().to_string(),
            "changes": changes,
            "accounts": registry.accounts.len(),
        });
        println!("{}", to_json(&out)?);
        return Ok(());
    }

    println!("accounts/ written to {}", dir.display());
    if changes.is_empty() {
        println!("No changes detected.");
    } else {
        println!("Changes:");
        for c in &changes {
            println!("  {c}");
        }
    }
    println!("{} accounts registered.", registry.accounts.len());
    Ok(())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Truncate a string to `max` chars for table display.
fn trunc(s: &str, max: usize) -> &str {
    if s.len() <= max {
        s
    } else {
        &s[..max]
    }
}

/// Serialise a value to pretty JSON.
fn to_json<T: serde::Serialize>(val: &T) -> Result<String> {
    serde_json::to_string_pretty(val).map_err(|e| CascadeError::Other(e.to_string()))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Build a test registry and call print_accounts_table without panicking.
    #[test]
    fn accounts_list_runs_without_panic() {
        let registry = default_registry();
        print_accounts_table(&registry.accounts);
    }
}
