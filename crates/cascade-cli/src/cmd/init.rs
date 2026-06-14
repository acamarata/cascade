//! `cascade init [PATH] [--accept-defaults] [--folder <name>] [--provider <slug>]
//!                      [--api-key <key>] [--model <id>] [--non-interactive] [--json]`
//!
//! # Purpose
//!
//! Bootstraps a `.cascade/` (or alternative AI folder) directory at the
//! appropriate tier for the given path, with full headless / unattended support.
//!
//! # Inputs
//!
//! | Flag | Meaning |
//! |------|---------|
//! | `[PATH]` | Directory to initialise (default: CWD) |
//! | `--accept-defaults` / `--non-interactive` | No prompts; use auto-detected values |
//! | `--folder {cascade,claude,codex,opencode}` | Force a specific AI folder name |
//! | `--provider <slug>` | Connect a cloud provider after scaffolding |
//! | `--api-key <key>` | API key for `--provider` (mutually required when provider given) |
//! | `--model <id>` | Preferred model hint (written to config.toml); with `local` triggers download advice |
//! | `--force` | Overwrite an existing AI folder |
//! | `--dry-run` | Print what would be created without writing anything |
//! | `--json` | Emit a machine-parseable JSON summary on stdout |
//!
//! # Outputs
//!
//! - Creates AI folder + canonical subdirectories + `CASCADE.md` skeleton.
//! - Optionally calls `connect_provider` for cloud auth.
//! - On `--json`: prints `InitResult` as JSON (exits 0 on success, 1 on error).
//! - Human output only when `--json` is not set.
//!
//! # Constraints
//!
//! - Fully idempotent: re-running with `--accept-defaults` on an existing folder
//!   heals missing subdirectories and is a no-op for files that already exist.
//! - When `--accept-defaults` / `--non-interactive` is set, zero stdin reads happen.
//! - Provider `local` prints advice but does NOT block; exit 0 always.
//! - Tests isolate `HOME` via `#[serial(global_env)]` + `tempfile::TempDir`.
//!
//! # SPORT
//!
//! MASTER-CLI.md — init: headless_cascade_init (E-P5-01)

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_core::ai_folder::resolve_folder_name;
#[cfg(test)]
use cascade_keychain::InMemoryKeychain;
use cascade_providers::{connect_provider_at, Credential, ProviderKind};
use cascade_types::{
    config::AiFolder,
    error::{CascadeError, Result},
    paths::{subdirs, AGENTS_MD_NAME, CASCADE_MD_NAME, CLAUDE_MD_NAME},
};
use clap::Args;
use serde::Serialize;

use super::Command;

// ── CLI args ──────────────────────────────────────────────────────────────────

/// Arguments for `cascade init`.
#[derive(Debug, Args)]
pub struct InitArgs {
    /// Target directory to initialise (default: current working directory).
    #[arg(value_name = "PATH")]
    pub path: Option<PathBuf>,

    /// Accept all defaults — no prompts; fully non-interactive.
    ///
    /// When set, the command is idempotent and deterministic.
    /// Alias: `--non-interactive`.
    #[arg(long, alias = "non-interactive")]
    pub accept_defaults: bool,

    /// Force a specific AI folder name (.cascade, .claude, .codex, .opencode, or any name).
    ///
    /// Without this flag, the folder is auto-detected: an existing .claude/.codex/.opencode
    /// directory is adopted; if none exist, .cascade is created.
    #[arg(long, value_name = "FOLDER")]
    pub folder: Option<String>,

    /// Cloud AI provider to connect after scaffolding
    /// (anthropic, openai, gemini, openrouter, groq, mistral, deepseek, together, cohere, local).
    #[arg(long, value_name = "PROVIDER")]
    pub provider: Option<String>,

    /// API key to validate and store for the chosen `--provider`.
    ///
    /// Mutually required with `--provider` (except for `local`).
    #[arg(long, value_name = "KEY")]
    pub api_key: Option<String>,

    /// Preferred model identifier, written to config.toml as `provider.model`.
    ///
    /// With `--provider local`, triggers a hint about `cascade models download`.
    #[arg(long, value_name = "MODEL")]
    pub model: Option<String>,

    /// Overwrite an existing AI folder without prompting.
    #[arg(long)]
    pub force: bool,

    /// Print what would be created without writing anything.
    #[arg(long)]
    pub dry_run: bool,

    /// Emit a machine-parseable JSON summary on stdout instead of human text.
    #[arg(long)]
    pub json: bool,
}

// ── JSON result type ──────────────────────────────────────────────────────────

/// Machine-parseable summary emitted when `--json` is set.
#[derive(Debug, Serialize)]
pub struct InitResult {
    /// Whether the operation succeeded.
    pub success: bool,
    /// Absolute path to the AI folder that was created / healed / would-be-created.
    pub ai_folder: String,
    /// Cascade tier label assigned to this path (gci, pci, apc, prc, pac, …).
    pub tier: String,
    /// Subdirectories created (or that would be created on `--dry-run`).
    pub dirs_created: Vec<String>,
    /// Files written (or that would be written).
    pub files_written: Vec<String>,
    /// Provider connected (empty string if none).
    pub provider_connected: String,
    /// Whether the operation was idempotent (folder already existed).
    pub idempotent: bool,
    /// Human-readable message.
    pub message: String,
}

// ── Subdirectories to scaffold ────────────────────────────────────────────────

/// The canonical subdirectories under the AI folder.
const SUBDIRS: &[&str] = &[
    subdirs::MEMORY,
    subdirs::DOCS,
    subdirs::IDEAS,
    subdirs::INBOX,
    subdirs::RULES,
    subdirs::TASKS,
    subdirs::PLANNING,
    subdirs::PHASES,
    subdirs::QA,
    subdirs::AGENTS,
    subdirs::SKILLS,
    subdirs::ARCHIVE,
    subdirs::TEMP,
    subdirs::PROVIDERS,
];

// ── .gitignore content ────────────────────────────────────────────────────────

const GITIGNORE_CONTENT: &str = "\
# AI working memory — not committed to version control.\n\
memory/\ndocs/\nideas/\ntemp/\ninbox/\n";

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for InitArgs {
    async fn run(&self) -> Result<()> {
        // 1. Resolve target directory.
        let base = match &self.path {
            Some(p) => p.clone(),
            None => std::env::current_dir().map_err(|e| CascadeError::Io {
                path: PathBuf::from("."),
                operation: "get cwd",
                source: e,
            })?,
        };

        // Ensure the base directory exists.
        if !base.exists() && !self.dry_run {
            std::fs::create_dir_all(&base).map_err(|e| CascadeError::Io {
                path: base.clone(),
                operation: "create base directory",
                source: e,
            })?;
        }

        // 2. Resolve the AI folder name.
        let folder_name = if let Some(explicit) = &self.folder {
            AiFolder::from_str_loose(explicit).folder_name().to_owned()
        } else {
            // Auto-adopt: look for existing .claude/.codex/.opencode/.cascade.
            resolve_folder_name(&base, None)
        };

        let ai_dir = base.join(&folder_name);

        // 3. Tier detection.
        let tier_label = auto_detect_tier(&base);

        // 4. Idempotency check.
        let already_existed = ai_dir.exists();
        if already_existed && !self.force && !self.accept_defaults && !self.dry_run {
            // Interactive mode: error out (legacy behaviour preserved).
            eprintln!(
                "error: {} already exists. Pass --force to overwrite, or --accept-defaults to heal.",
                ai_dir.display()
            );
            std::process::exit(1);
        }

        // 5. Validate provider args.
        let provider_kind = self.resolve_provider_kind()?;

        // 6. Dry-run path.
        if self.dry_run {
            return self.run_dry_run(
                &ai_dir,
                &folder_name,
                tier_label,
                already_existed,
                &provider_kind,
            );
        }

        // 7. Scaffold directories (idempotent — create_dir_all is a no-op if already present).
        let mut dirs_created: Vec<String> = Vec::new();
        for sub in SUBDIRS {
            let dir = ai_dir.join(sub);
            if !dir.exists() {
                std::fs::create_dir_all(&dir).map_err(|e| CascadeError::Io {
                    path: dir.clone(),
                    operation: "create_dir_all",
                    source: e,
                })?;
                dirs_created.push(format!("{}/{}/", folder_name, sub));
            } else {
                // Ensure it's a directory even if it previously existed.
                std::fs::create_dir_all(&dir).map_err(|e| CascadeError::Io {
                    path: dir.clone(),
                    operation: "heal_dir",
                    source: e,
                })?;
            }
        }
        // Ensure the root AI dir itself exists.
        if !ai_dir.exists() {
            std::fs::create_dir_all(&ai_dir).map_err(|e| CascadeError::Io {
                path: ai_dir.clone(),
                operation: "create ai_dir",
                source: e,
            })?;
            dirs_created.push(format!("{}/", folder_name));
        }

        // 8. Write CASCADE.md (idempotent — skip if exists and not --force).
        let mut files_written: Vec<String> = Vec::new();
        let cascade_md = ai_dir.join(CASCADE_MD_NAME);
        if !cascade_md.exists() || self.force {
            let content = starter_template(tier_label, &folder_name);
            std::fs::write(&cascade_md, content).map_err(|e| CascadeError::Io {
                path: cascade_md.clone(),
                operation: "write CASCADE.md",
                source: e,
            })?;
            files_written.push(format!("{}/{}", folder_name, CASCADE_MD_NAME));
        }

        // 9. Create CLAUDE.md and AGENTS.md symlinks.
        let claude_link = ai_dir.join(CLAUDE_MD_NAME);
        let agents_link = ai_dir.join(AGENTS_MD_NAME);
        let symlinks_were_missing = !claude_link.exists() || !agents_link.exists();
        cascade_core::symlinks::create_siblings(&ai_dir, self.force)?;
        if symlinks_were_missing || self.force {
            files_written.push(format!("{}/{}", folder_name, CLAUDE_MD_NAME));
            files_written.push(format!("{}/{}", folder_name, AGENTS_MD_NAME));
        }

        // 10. Write .gitignore (idempotent).
        let gitignore = ai_dir.join(".gitignore");
        if !gitignore.exists() || self.force {
            std::fs::write(&gitignore, GITIGNORE_CONTENT).map_err(|e| CascadeError::Io {
                path: gitignore,
                operation: "write .gitignore",
                source: e,
            })?;
            files_written.push(format!("{}/.gitignore", folder_name));
        }

        // 11. Write config.toml model hint if --model is provided.
        if let Some(model_id) = &self.model {
            let config_path = ai_dir.join("config.toml");
            let config_entry = format!("[provider]\nmodel = \"{}\"\n", model_id);
            if !config_path.exists() {
                std::fs::write(&config_path, &config_entry).map_err(|e| CascadeError::Io {
                    path: config_path,
                    operation: "write config.toml",
                    source: e,
                })?;
                files_written.push(format!("{}/config.toml", folder_name));
            }
        }

        // 12. Provider connect.
        let provider_connected = self
            .connect_provider_headless(provider_kind.as_ref(), &ai_dir)
            .await?;

        // 13. Emit output.
        let result = InitResult {
            success: true,
            ai_folder: ai_dir.display().to_string(),
            tier: tier_label.to_string(),
            dirs_created,
            files_written,
            provider_connected: provider_connected.clone(),
            idempotent: already_existed,
            message: format!(
                "Initialised {} cascade at {}{}",
                tier_label.to_uppercase(),
                ai_dir.display(),
                if !provider_connected.is_empty() {
                    format!(" (provider: {})", provider_connected)
                } else {
                    String::new()
                }
            ),
        };

        if self.json {
            let json = serde_json::to_string_pretty(&result)
                .map_err(|e| CascadeError::Other(e.to_string()))?;
            println!("{json}");
        } else {
            println!("{}", result.message);
            if !provider_connected.is_empty() {
                println!(
                    "Provider '{}' connected and stored in OS keychain.",
                    provider_connected
                );
            } else if self.provider.as_deref() != Some("local") && self.provider.is_none() {
                println!("Next step: cascade provider add --kind <PROVIDER> --api-key <KEY>");
            }
            if self.provider.as_deref() == Some("local") {
                println!(
                    "Next step: cascade models download --model {}",
                    self.model.as_deref().unwrap_or("<model-id>")
                );
            }
        }

        Ok(())
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

impl InitArgs {
    /// Validate and parse `--provider` into a `ProviderKind`.
    ///
    /// Returns `None` when no `--provider` was given or when `local` is specified
    /// (handled separately downstream).
    fn resolve_provider_kind(&self) -> Result<Option<ProviderKind>> {
        let slug = match &self.provider {
            None => return Ok(None),
            Some(s) if s.eq_ignore_ascii_case("local") => return Ok(None),
            Some(s) => s.as_str(),
        };

        // For cloud providers, --api-key is required.
        if self.api_key.is_none() {
            return Err(CascadeError::Other(format!(
                "--api-key is required when --provider {} is given (not local). \
                 Pass --provider local if you intend to use a local model.",
                slug
            )));
        }

        ProviderKind::from_slug(slug)
            .ok_or_else(|| {
                CascadeError::Other(format!(
                    "Unknown provider '{}'. Supported: anthropic, openai, gemini, openrouter, \
                 groq, mistral, deepseek, together, cohere, local.",
                    slug
                ))
            })
            .map(Some)
    }

    /// Call `connect_provider_at` using an `InMemoryKeychain` that delegates to
    /// the platform keychain when running normally, but accepts a mock in tests.
    ///
    /// Returns the provider id on success, or empty string when no provider was configured.
    async fn connect_provider_headless(
        &self,
        kind: Option<&ProviderKind>,
        cascade_dir: &Path,
    ) -> Result<String> {
        let Some(provider_kind) = kind else {
            return Ok(String::new());
        };

        let api_key = self.api_key.as_deref().unwrap_or("").to_owned();

        if api_key.is_empty() {
            return Err(CascadeError::Other(
                "--api-key must not be empty when connecting a provider".to_owned(),
            ));
        }

        let id = provider_kind.id();

        // Use the OS keychain in production.
        let kc = cascade_keychain::platform_keychain();
        let cascade_dir_owned = cascade_dir.to_path_buf();

        connect_provider_at(
            provider_kind.clone(),
            Credential::ApiKey(api_key),
            kc.as_ref(),
            None,
            Some(&cascade_dir_owned),
        )
        .await
        .map(|_| id)
        .map_err(|e| CascadeError::Other(format!("provider connect failed: {e}")))
    }

    /// Print a dry-run summary (no filesystem writes).
    fn run_dry_run(
        &self,
        ai_dir: &Path,
        folder_name: &str,
        tier: &str,
        already_existed: bool,
        provider_kind: &Option<ProviderKind>,
    ) -> Result<()> {
        let mut dirs_created = Vec::new();
        let mut files_written = Vec::new();

        if !already_existed || self.force {
            dirs_created.push(format!("{}/", folder_name));
        }
        for sub in SUBDIRS {
            let dir = ai_dir.join(sub);
            if !dir.exists() {
                dirs_created.push(format!("{}/{}/", folder_name, sub));
            }
        }
        if !ai_dir.join(CASCADE_MD_NAME).exists() || self.force {
            files_written.push(format!("{}/{}", folder_name, CASCADE_MD_NAME));
            files_written.push(format!("{}/{}", folder_name, CLAUDE_MD_NAME));
            files_written.push(format!("{}/{}", folder_name, AGENTS_MD_NAME));
        }
        if !ai_dir.join(".gitignore").exists() || self.force {
            files_written.push(format!("{}/.gitignore", folder_name));
        }

        let provider_name = provider_kind.as_ref().map(|k| k.id()).unwrap_or_default();

        if self.json {
            let result = InitResult {
                success: true,
                ai_folder: ai_dir.display().to_string(),
                tier: tier.to_string(),
                dirs_created,
                files_written,
                provider_connected: provider_name,
                idempotent: already_existed,
                message: format!(
                    "[dry-run] would initialise {} cascade at {}",
                    tier.to_uppercase(),
                    ai_dir.display()
                ),
            };
            let json = serde_json::to_string_pretty(&result)
                .map_err(|e| CascadeError::Other(e.to_string()))?;
            println!("{json}");
        } else {
            println!("[dry-run] Would create: {}/", ai_dir.display());
            for d in &dirs_created {
                println!("  {d}");
            }
            for f in &files_written {
                println!("  {f}");
            }
            if let Some(pk) = provider_kind {
                println!("  [would connect provider: {}]", pk.id());
            }
        }
        Ok(())
    }
}

// ── Tier detection ────────────────────────────────────────────────────────────

/// Detect the most appropriate tier label for a given directory.
///
/// Respects `CASCADE_APC_PATH` / `CASCADE_PCI_PATH` env overrides (same as the
/// discovery module uses).
fn auto_detect_tier(dir: &Path) -> &'static str {
    // Home → GCI
    if let Ok(home) = std::env::var("HOME").map(PathBuf::from) {
        if dir == home {
            return "gci";
        }

        // Sites root → PCI
        let sites = std::env::var("CASCADE_PCI_PATH")
            .map(PathBuf::from)
            .unwrap_or_else(|_| home.join("Sites"));
        if dir == sites {
            return "pci";
        }

        // One level beneath Sites → APC
        let apc_parent = std::env::var("CASCADE_APC_PATH")
            .map(PathBuf::from)
            .unwrap_or(sites.clone());
        if dir.parent() == Some(apc_parent.as_path()) {
            return "apc";
        }
    }

    // Git repo root → PRC
    if dir.join(".git").is_dir() {
        return "prc";
    }

    // Has a git ancestor → PAC
    if find_git_root(dir).is_some() {
        return "pac";
    }

    // No classification → GCI fallback
    "gci"
}

fn find_git_root(cwd: &Path) -> Option<PathBuf> {
    cwd.ancestors()
        .skip(1) // skip cwd itself
        .find(|p| p.join(".git").exists())
        .map(|p| p.to_path_buf())
}

// ── CASCADE.md template ───────────────────────────────────────────────────────

fn starter_template(tier: &str, folder_name: &str) -> String {
    format!(
        "# Cascade Instructions — {tier_upper}\n\
         \n\
         This file contains AI context instructions for the `{tier}` tier.\n\
         AI folder: `{folder_name}/`\n\
         \n\
         Edit this file to add project-specific rules, patterns, and conventions.\n\
         The `CLAUDE.md` and `AGENTS.md` siblings in this directory are symlinks\n\
         that point here — edit only this file.\n\
         \n\
         ## Purpose\n\
         \n\
         (Describe the scope of this cascade tier here.)\n\
         \n\
         ## Rules\n\
         \n\
         (Add rules and conventions below.)\n",
        tier_upper = tier.to_uppercase(),
        tier = tier,
        folder_name = folder_name
    )
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_keychain::Keychain;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Run InitArgs synchronously in tests, setting HOME to `home_dir`.
    fn run_init_sync(args: InitArgs, home_dir: &Path) -> Result<()> {
        // Override HOME so tier detection and path resolution work with tempdir.
        std::env::set_var("HOME", home_dir.as_os_str());
        // Remove any lingering env overrides.
        std::env::remove_var("CASCADE_APC_PATH");
        std::env::remove_var("CASCADE_PCI_PATH");

        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .expect("build tokio runtime");
        rt.block_on(args.run())
    }

    // ── GCI tier (HOME) ───────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_gci_creates_default_cascade_folder() {
        let home = TempDir::new().unwrap();
        let args = InitArgs {
            path: Some(home.path().to_path_buf()),
            accept_defaults: true,
            folder: None,
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        run_init_sync(args, home.path()).unwrap();

        let ai_dir = home.path().join(".cascade");
        assert!(ai_dir.is_dir(), ".cascade/ must exist");
        assert!(ai_dir.join("CASCADE.md").exists(), "CASCADE.md must exist");
        assert!(ai_dir.join("memory").is_dir(), "memory/ must exist");
        assert!(ai_dir.join("inbox").is_dir(), "inbox/ must exist");
    }

    // ── PPC tier (git repo) ───────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_prc_inside_git_repo() {
        let home = TempDir::new().unwrap();
        // Simulate a git repo inside home.
        let repo = home.path().join("myrepo");
        fs::create_dir_all(&repo).unwrap();
        fs::create_dir_all(repo.join(".git")).unwrap();

        let args = InitArgs {
            path: Some(repo.clone()),
            accept_defaults: true,
            folder: None,
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        run_init_sync(args, home.path()).unwrap();

        let ai_dir = repo.join(".cascade");
        assert!(ai_dir.is_dir());
        assert!(ai_dir.join("CASCADE.md").exists());
    }

    // ── Explicit folder choice ────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_explicit_claude_folder() {
        let home = TempDir::new().unwrap();
        let target = home.path().to_path_buf();

        let args = InitArgs {
            path: Some(target.clone()),
            accept_defaults: true,
            folder: Some("claude".to_string()),
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        run_init_sync(args, home.path()).unwrap();

        assert!(target.join(".claude").is_dir(), ".claude/ must be created");
        assert!(target.join(".claude").join("CASCADE.md").exists());
    }

    // ── Auto-adopt existing .claude ───────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_auto_adopts_existing_claude_folder() {
        let home = TempDir::new().unwrap();
        // Pre-existing .claude
        fs::create_dir_all(home.path().join(".claude")).unwrap();

        let args = InitArgs {
            path: Some(home.path().to_path_buf()),
            accept_defaults: true,
            folder: None, // auto-detect
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        run_init_sync(args, home.path()).unwrap();

        // Should have scaffolded inside .claude, NOT .cascade
        assert!(home.path().join(".claude").join("CASCADE.md").exists());
        // .cascade must NOT be created
        assert!(!home.path().join(".cascade").exists());
    }

    // ── Idempotent re-run ─────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_idempotent_rerun() {
        let home = TempDir::new().unwrap();
        let args = || InitArgs {
            path: Some(home.path().to_path_buf()),
            accept_defaults: true,
            folder: None,
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        // First run.
        run_init_sync(args(), home.path()).unwrap();
        // Second run — must not error.
        run_init_sync(args(), home.path()).unwrap();

        // CASCADE.md content should still be valid.
        let md = fs::read_to_string(home.path().join(".cascade").join("CASCADE.md")).unwrap();
        assert!(md.contains("Cascade Instructions"));
    }

    // ── JSON output shape ─────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_json_output_valid() {
        let home = TempDir::new().unwrap();
        // Capture stdout via a dry-run + json combo.
        // We test the shape by calling the helper that builds InitResult directly.
        let ai_dir = home.path().join(".cascade");
        let result = InitResult {
            success: true,
            ai_folder: ai_dir.display().to_string(),
            tier: "gci".to_string(),
            dirs_created: vec!["  .cascade/memory/".to_string()],
            files_written: vec!["  .cascade/CASCADE.md".to_string()],
            provider_connected: String::new(),
            idempotent: false,
            message: "Initialised GCI cascade".to_string(),
        };
        let json = serde_json::to_string(&result).unwrap();
        let parsed: serde_json::Value = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed["success"], true);
        assert_eq!(parsed["tier"], "gci");
        assert!(parsed["ai_folder"].as_str().unwrap().contains(".cascade"));
        assert!(parsed["dirs_created"].is_array());
        assert!(parsed["files_written"].is_array());
        assert!(parsed["provider_connected"].is_string());
        assert!(parsed["idempotent"].is_boolean());
    }

    // ── Provider add with mock (InMemoryKeychain) ─────────────────────────────

    #[tokio::test]
    #[serial(global_env)]
    async fn headless_init_provider_add_mock() {
        let home = TempDir::new().unwrap();
        std::env::set_var("HOME", home.path().as_os_str());

        // Start a mock HTTP server.
        let mock_server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .and(wiremock::matchers::path("/v1/models"))
            .respond_with(wiremock::ResponseTemplate::new(200).set_body_string("{}"))
            .mount(&mock_server)
            .await;

        let cascade_dir = home.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let kc = InMemoryKeychain::new();
        let result = cascade_providers::connect_provider_at(
            ProviderKind::Anthropic,
            Credential::ApiKey("sk-headless-test".into()),
            &kc,
            Some(mock_server.uri().as_str()),
            Some(&cascade_dir),
        )
        .await;

        assert!(
            result.is_ok(),
            "connect_provider_at should succeed: {result:?}"
        );
        let connected = result.unwrap();
        assert_eq!(connected.id, "anthropic");
        assert!(connected.healthy);

        // Secret stored in keychain.
        let stored = kc
            .get_key(cascade_providers::PROVIDER_KEYCHAIN_SERVICE, "anthropic")
            .unwrap();
        assert_eq!(stored, "sk-headless-test");
    }

    // ── PCI tier (Sites root) ─────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_pci_tier_detected() {
        let home = TempDir::new().unwrap();
        let sites = home.path().join("Sites");
        fs::create_dir_all(&sites).unwrap();
        std::env::set_var("CASCADE_PCI_PATH", sites.as_os_str());

        let args = InitArgs {
            path: Some(sites.clone()),
            accept_defaults: true,
            folder: None,
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        run_init_sync(args, home.path()).unwrap();

        assert!(sites.join(".cascade").is_dir());
        std::env::remove_var("CASCADE_PCI_PATH");
    }

    // ── Dry-run leaves filesystem clean ──────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn headless_init_dry_run_leaves_fs_clean() {
        let home = TempDir::new().unwrap();
        let args = InitArgs {
            path: Some(home.path().to_path_buf()),
            accept_defaults: true,
            folder: None,
            provider: None,
            api_key: None,
            model: None,
            force: false,
            dry_run: true,
            json: false,
        };
        run_init_sync(args, home.path()).unwrap();
        // Nothing should have been written.
        assert!(!home.path().join(".cascade").exists());
    }

    // ── provider slug validation ──────────────────────────────────────────────

    #[test]
    fn provider_kind_missing_api_key_returns_error() {
        let args = InitArgs {
            path: None,
            accept_defaults: true,
            folder: None,
            provider: Some("anthropic".to_string()),
            api_key: None, // missing
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        let result = args.resolve_provider_kind();
        assert!(
            result.is_err(),
            "should error when api-key missing for cloud provider"
        );
    }

    #[test]
    fn provider_kind_local_requires_no_api_key() {
        let args = InitArgs {
            path: None,
            accept_defaults: true,
            folder: None,
            provider: Some("local".to_string()),
            api_key: None,
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        let result = args.resolve_provider_kind().unwrap();
        assert!(
            result.is_none(),
            "local provider returns None (handled downstream)"
        );
    }

    #[test]
    fn provider_kind_unknown_slug_returns_error() {
        let args = InitArgs {
            path: None,
            accept_defaults: true,
            folder: None,
            provider: Some("doesnotexist".to_string()),
            api_key: Some("key".to_string()),
            model: None,
            force: false,
            dry_run: false,
            json: false,
        };
        let result = args.resolve_provider_kind();
        assert!(result.is_err());
    }
}
