//! # cascade-core
//!
//! Cascade discovery, resolution, file watching, and runtime state management.
//!
//! This crate owns the six-tier cascade lifecycle:
//! 1. **Discovery** (`discovery`) — walk CWD upward, locate `.cascade/CASCADE.md`
//!    at each tier (GCI → PCI → APC → PPC → PRC → PAC).
//! 2. **Resolution** (`resolution`) — merge discovered tiers in priority order
//!    (GCI highest) and return a [`ResolvedCascade`].
//! 3. **File watching** (`watcher`) — subscribe to `notify` events for any
//!    `.cascade/CASCADE.md` path; emit [`CascadeChanged`] on modification.
//! 4. **Symlink management** (`symlinks`) — create/verify/repair the AGENTS.md,
//!    CLAUDE.md, and `.cursorrules` siblings that point to CASCADE.md.
//! 5. **Derived-file regeneration** (`derived`) — rebuild non-markdown
//!    tool formats (`.aider.conf.yml`, `.cursorrules` text body) when the
//!    source CASCADE.md changes.
//! 6. **Inbox protocol** (`inbox`) — read/write/route PPI/PRI/PAI messages
//!    under `.cascade/inbox/`.
//! 7. **Memory files** (`memory`) — read/write decisions.md, lessons.md,
//!    patterns.md under `.cascade/memory/`.
//! 8. **Resolution cache** (`cache`) — persist the merged cascade to
//!    `.cascade/temp/.resolved-cascade.md` so downstream tools can read it
//!    without re-walking the filesystem.
//!
//! ## Tier ordering
//!
//! Cascade tiers from highest to lowest priority:
//!
//! | Tier | Abbreviation | Typical path |
//! |------|-------------|--------------|
//! | Global Config Instructions | GCI | `~/.cascade/` |
//! | Project Config Instructions | PCI | `~/Sites/.cascade/` |
//! | App-level Config | APC | `~/Sites/{project}/.cascade/` |
//! | Package-level Config | PPC | `~/Sites/{project}/{repo}/.cascade/` |
//! | Repo Config | PRC | git repo root `.cascade/` |
//! | Per-App Config | PAC | app subdirectory `.cascade/` |
//!
//! Higher-tier content prepends in the merged output. When two tiers define
//! the same instruction, the higher tier wins.
//!
//! ## Usage
//!
//! ```rust,no_run
//! use cascade_core::resolution::Resolver;
//! use std::path::Path;
//!
//! # async fn example() -> cascade_types::error::Result<()> {
//! let resolved = Resolver::new().resolve(Path::new(".")).await?;
//! println!("Active tiers: {}", resolved.tier_sources.len());
//! println!("{}", resolved.merged_text);
//! # Ok(())
//! # }
//! ```

pub mod ai_folder;
pub mod auth_detector;
pub mod cache;
pub mod cascade_resolution;
pub mod cascade_resolve;
pub mod lint_budget;
pub mod lint_conflicts;
pub mod lint_dangling;
pub mod lint_duplication;
pub mod lint_generated;
pub mod var_substitute;
pub mod compat_gen;
pub mod config;
pub mod context_pack;
pub mod derived;
pub mod discovery;
pub mod eie;
pub mod hook_store;
pub mod import_engine;
pub mod import_expand;
pub mod injection_scan;
pub mod inbox;
pub mod library;
pub mod loader;
pub mod maps;
pub mod memory;
pub mod pbd;
pub mod providers_store;
pub mod quota_aggregator;
pub mod quota_store;
pub mod resolution;
pub mod routing_table;
pub mod security;
pub mod sensitivity;
pub mod settings;
pub mod symlinks;
pub mod task_store;
pub mod tasks;
pub mod templates;
pub mod watcher;
pub mod worktree_store;

// Re-export the most commonly used top-level items.
pub use discovery::{DiscoveredTier, TierDiscovery};
pub use injection_scan::{scan_for_injection, InjectionMatch, InjectionReport, Risk, Sensitivity};
pub use hook_store::HookStore;
pub use quota_store::{read_quota_store, write_quota_store};
pub use resolution::{ResolvedCascade, Resolver};
pub use watcher::{CascadeChanged, CascadeWatcher};
