//! migrate_keys — `cascade migrate-keys`: move `GEMINI_API_KEY_{n}` secrets out
//! of `~/.claude/vault.env` and into the OS keychain (T-P2-E07-15).
//!
//! Purpose: improve secret hygiene by relocating plaintext API keys from a
//!   dotfile into the OS-native credential store (via the cascade-keychain
//!   crate), leaving a `keychain://` placeholder behind so other tooling can
//!   see the key was migrated.
//! Inputs: `--dry-run` flag; the vault.env file at `$HOME/.claude/vault.env`.
//! Outputs: writes secrets to the keychain and rewrites vault.env in place
//!   (atomic tmp+rename). Never prints or logs a secret value.
//! Constraints: partial failure leaves the original vault.env untouched (the
//!   file is only rewritten after every keychain write succeeds).
//! SPORT: MASTER-CLI.md — `cascade migrate-keys`.

use std::fs;
use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_keychain::{platform_keychain, Keychain};
use cascade_types::error::{CascadeError, Result};
use clap::Args;

use super::Command;

/// Service name under which all cascade keys are stored in the OS keychain.
const KEYCHAIN_SERVICE: &str = "dev.cascade";

/// Placeholder written into vault.env once a key has moved to the keychain.
const PLACEHOLDER: &str = "keychain://";

/// `cascade migrate-keys` arguments.
#[derive(Debug, Args)]
pub struct MigrateKeysArgs {
    /// Show the migration plan without writing to the keychain or vault.env.
    #[arg(long)]
    pub dry_run: bool,
}

/// Default vault.env location: `$HOME/.claude/vault.env`.
///
/// Uses `$HOME` (honoured on every platform; required for test isolation) and
/// falls back to the current directory if `$HOME` is unset.
fn default_vault_path() -> PathBuf {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".claude/vault.env")
}

/// A migratable key parsed from vault.env: its slot index and secret value.
struct VaultKey {
    n: u32,
    value: String,
}

/// Parse `GEMINI_API_KEY_{n}=value` lines from a vault.env body.
///
/// Skips comments, blank values, and already-migrated `keychain://` placeholders
/// so the command is idempotent.
fn parse_vault_keys(content: &str) -> Vec<VaultKey> {
    let mut out = Vec::new();
    for raw in content.lines() {
        let line = raw.trim();
        if line.starts_with('#') {
            continue;
        }
        let line = line.strip_prefix("export ").unwrap_or(line);
        if let Some(rest) = line.strip_prefix("GEMINI_API_KEY_") {
            if let Some((idx, val)) = rest.split_once('=') {
                if let Ok(n) = idx.trim().parse::<u32>() {
                    let value = val.trim().trim_matches('"').to_string();
                    if value.is_empty() || value == PLACEHOLDER {
                        continue;
                    }
                    out.push(VaultKey { n, value });
                }
            }
        }
    }
    out
}

/// Rewrite each migrated key's vault.env line to the `keychain://` placeholder,
/// preserving all other lines and any `export ` prefix.
fn rewrite_with_placeholders(content: &str, migrated: &[u32]) -> String {
    content
        .lines()
        .map(|raw| {
            let trimmed = raw.trim_start();
            let body = trimmed.strip_prefix("export ").unwrap_or(trimmed);
            if let Some(rest) = body.strip_prefix("GEMINI_API_KEY_") {
                if let Some((idx, _)) = rest.split_once('=') {
                    if let Ok(n) = idx.trim().parse::<u32>() {
                        if migrated.contains(&n) {
                            let prefix = if trimmed.starts_with("export ") {
                                "export "
                            } else {
                                ""
                            };
                            return format!("{prefix}GEMINI_API_KEY_{n}={PLACEHOLDER}");
                        }
                    }
                }
            }
            raw.to_string()
        })
        .collect::<Vec<_>>()
        .join("\n")
}

/// Testable migration core.
///
/// Reads `vault_path`, writes each parsed key into `kc`, and (unless `dry_run`)
/// rewrites vault.env atomically with placeholders. Returns the number of keys
/// migrated (or that would be migrated, in dry-run). Never prints secret values.
fn migrate(kc: &dyn Keychain, vault_path: &Path, dry_run: bool) -> Result<usize> {
    let content = fs::read_to_string(vault_path)
        .map_err(|e| CascadeError::io(vault_path.to_path_buf(), "read vault.env", e))?;
    let keys = parse_vault_keys(&content);

    if dry_run {
        println!("Would migrate {} keys to OS keychain.", keys.len());
        for k in &keys {
            println!("  Would migrate gemini_key_{}", k.n);
        }
        return Ok(keys.len());
    }

    // Write every key to the keychain FIRST. If any write fails, return the
    // error before touching vault.env — the original file stays intact.
    let mut migrated_ns = Vec::with_capacity(keys.len());
    for k in &keys {
        kc.set_key(KEYCHAIN_SERVICE, &format!("gemini_key_{}", k.n), &k.value)
            .map_err(|e| {
                CascadeError::io(
                    vault_path.to_path_buf(),
                    "write key to OS keychain",
                    std::io::Error::other(format!("keychain set_key failed for slot {}: {e}", k.n)),
                )
            })?;
        migrated_ns.push(k.n);
    }

    // All keychain writes succeeded — rewrite vault.env atomically (tmp+rename).
    if !migrated_ns.is_empty() {
        let new_content = rewrite_with_placeholders(&content, &migrated_ns);
        let tmp = vault_path.with_extension("env.tmp");
        fs::write(&tmp, new_content)
            .map_err(|e| CascadeError::io(tmp.clone(), "write vault.env.tmp", e))?;
        if let Err(e) = fs::rename(&tmp, vault_path) {
            // Don't leave the partial .tmp behind on a failed rename.
            let _ = fs::remove_file(&tmp);
            return Err(CascadeError::io(
                vault_path.to_path_buf(),
                "replace vault.env",
                e,
            ));
        }
    }

    println!(
        "Migrated {} keys to OS keychain. vault.env entries replaced with placeholders.",
        migrated_ns.len()
    );
    Ok(migrated_ns.len())
}

#[async_trait]
impl Command for MigrateKeysArgs {
    async fn run(&self) -> Result<()> {
        let kc = platform_keychain();
        let vault_path = default_vault_path();
        if !vault_path.exists() {
            println!(
                "No vault.env found at {} — nothing to migrate.",
                vault_path.display()
            );
            return Ok(());
        }
        migrate(kc.as_ref(), &vault_path, self.dry_run)?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_keychain::InMemoryKeychain;
    use std::io::Write;

    fn write_vault(dir: &Path, body: &str) -> PathBuf {
        let p = dir.join("vault.env");
        let mut f = fs::File::create(&p).unwrap();
        f.write_all(body.as_bytes()).unwrap();
        p
    }

    #[test]
    fn dry_run_reports_count_without_writing() {
        let dir = tempfile::tempdir().unwrap();
        let body = "GEMINI_API_KEY_1=aaa\nGEMINI_API_KEY_2=bbb\n# comment\n";
        let vault = write_vault(dir.path(), body);
        let kc = InMemoryKeychain::new();

        let n = migrate(&kc, &vault, true).unwrap();
        assert_eq!(n, 2);
        // vault.env unchanged.
        assert_eq!(fs::read_to_string(&vault).unwrap(), body);
        // Keychain untouched.
        assert!(kc.list_keys("dev.cascade").unwrap().is_empty());
    }

    #[test]
    fn migration_moves_keys_and_writes_placeholders() {
        let dir = tempfile::tempdir().unwrap();
        let vault = write_vault(
            dir.path(),
            "GEMINI_API_KEY_1=secret-one\nexport GEMINI_API_KEY_2=\"secret-two\"\nOTHER=keep\n",
        );
        let kc = InMemoryKeychain::new();

        let n = migrate(&kc, &vault, false).unwrap();
        assert_eq!(n, 2);

        // Keychain now holds both keys.
        assert_eq!(
            kc.get_key("dev.cascade", "gemini_key_1").unwrap(),
            "secret-one"
        );
        assert_eq!(
            kc.get_key("dev.cascade", "gemini_key_2").unwrap(),
            "secret-two"
        );

        // vault.env shows placeholders; the original export prefix and other lines survive.
        let rewritten = fs::read_to_string(&vault).unwrap();
        assert!(rewritten.contains("GEMINI_API_KEY_1=keychain://"));
        assert!(rewritten.contains("export GEMINI_API_KEY_2=keychain://"));
        assert!(rewritten.contains("OTHER=keep"));
        assert!(!rewritten.contains("secret-one"));
        assert!(!rewritten.contains("secret-two"));
    }

    #[test]
    fn already_migrated_keys_are_skipped() {
        let dir = tempfile::tempdir().unwrap();
        let vault = write_vault(
            dir.path(),
            "GEMINI_API_KEY_1=keychain://\nGEMINI_API_KEY_2=fresh\n",
        );
        let kc = InMemoryKeychain::new();
        let n = migrate(&kc, &vault, false).unwrap();
        assert_eq!(n, 1);
        assert_eq!(kc.get_key("dev.cascade", "gemini_key_2").unwrap(), "fresh");
    }
}
